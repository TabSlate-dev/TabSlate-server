# Account Deletion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a self-service account deletion flow: users request deletion via Settings → Account, a 30-day grace period begins (login cancels it implicitly), three emails are sent (request / 3-day reminder / confirmation), then all data is hard-deleted.

**Architecture:** Backend-only scheduling via the existing `CleanupHandler` daily loop (new Phase 3 & 4). Three new DB columns on `users`. One new protected endpoint `POST /auth/delete-account`. Frontend adds an Account tab to the Settings dialog with a password-confirmed deletion dialog. Landing page legal docs updated after ship.

**Tech Stack:** Go 1.22+ (server), PostgreSQL 17+ (pgx/v5), MeiliSearch meilisearch-go v0.36.2, React + TypeScript + Zustand (extension), Next.js + JSON i18n (landing page).

## Global Constraints

- All DB queries use `h.db.Rebind(query)` with `?` placeholders — never `$N` directly.
- All DB queries pass `c.Request.Context()` (or a `context.Context` arg) — never context-free variants.
- Multi-step writes use `BEGIN`/`COMMIT` transactions with `defer tx.Rollback()`.
- No string-concat SQL — parameterised queries only.
- Error responses never return raw DB errors — only generic messages.
- Passwords compared with `auth.CheckPassword` (bcrypt) — never plain equality.
- New Go files: `package handler` (no `_test` suffix for white-box tests).
- Frontend translation keys added to both `public/_locales/en/messages.json` **and** `public/_locales/zh_CN/messages.json`.
- `go build ./...` and `go vet ./...` must pass after every server task.
- Spec: `docs/superpowers/specs/2026-06-26-account-deletion-design.md`

---

## File Map

**TabSlate-server**
| File | Action | Responsibility |
|---|---|---|
| `db/schema.pg.sql` | Modify | Add 3 `ALTER TABLE` columns |
| `billing/provider.go` | Modify | Add optional `UserDeleter` interface |
| `internal/search/client.go` | Modify | Add `DeleteUserDocumentsAsync(userID)` |
| `internal/mailer/mailer.go` | Modify | Add `deletionTmpl`, `AccountDeletionEmailData`, `deletionTranslations`, `SendAccountDeletion`, `renderAccountDeletion` |
| `internal/mailer/templates/account_deletion.html` | Create | HTML email template (no OTP block) |
| `internal/mailer/mailer_test.go` | Modify | Tests for `renderAccountDeletion` |
| `internal/model/model.go` | Modify | Add `DeletionScheduledAt` to `User`, add `DeleteAccountRequest` |
| `internal/handler/auth.go` | Modify | Add `DeleteAccount()`, update `Login()`, update `Me()` |
| `internal/handler/auth_test.go` | Modify | Tests for `DeleteAccount` |
| `internal/handler/cleanup.go` | Modify | Add Phase 3 & 4, extend `CleanupHandler` struct + constructor |
| `app/server.go` | Modify | Pass mailer/billing/search to `NewCleanupHandler`; register `POST /auth/delete-account` |

**TabSlate (extension)**
| File | Action | Responsibility |
|---|---|---|
| `lib/api.ts` | Modify | Extend `ApiUser`, add `DeleteAccountResponse`, add `api.deleteAccount()` |
| `store/auth-store.ts` | Modify | Add `requestAccountDeletion(password)` action |
| `public/_locales/en/messages.json` | Modify | Add Account-tab i18n keys |
| `public/_locales/zh_CN/messages.json` | Modify | Same keys in Chinese |
| `components/dashboard/settings-dialog.tsx` | Modify | Add Account tab + `DeleteAccountDialog` component |

**TabSlate-Landing**
| File | Action | Responsibility |
|---|---|---|
| `src/messages/en.json` | Modify | Update 3 legal text sections |
| `src/messages/zh.json` | Modify | Same updates in Chinese |

---

## Task 1: Database schema + billing.UserDeleter + search.DeleteUserDocumentsAsync

**Files:**
- Modify: `db/schema.pg.sql`
- Modify: `billing/provider.go`
- Modify: `internal/search/client.go`
- Test: `internal/search/search_test.go` (nil-safe test for new method)

**Interfaces:**
- Produces:
  - DB columns: `users.last_login_at BIGINT`, `users.deletion_requested_at BIGINT`, `users.deletion_reminder_sent_at BIGINT`
  - `billing.UserDeleter` interface with `OnUserDeleted(ctx context.Context, userID string) error`
  - `(*search.Client).DeleteUserDocumentsAsync(userID string)` — nil-safe, fire-and-forget goroutine

- [ ] **Step 1: Add 3 columns to schema**

At the end of `db/schema.pg.sql`, before the final comment block, add:

```sql
-- Account deletion grace period
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at             BIGINT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deletion_requested_at     BIGINT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deletion_reminder_sent_at BIGINT;
```

- [ ] **Step 2: Add UserDeleter to billing/provider.go**

At the end of `billing/provider.go`, after the existing interface definitions, add:

```go
// UserDeleter is an optional interface implemented by billing providers that
// need to clean up external records when an account is permanently deleted.
// OSS local.Provider does not implement this. Cloud flexprice.Provider may.
type UserDeleter interface {
	OnUserDeleted(ctx context.Context, userID string) error
}
```

- [ ] **Step 3: Add DeleteUserDocumentsAsync to internal/search/client.go**

Add after `DeleteBookmark`:

```go
// DeleteUserDocumentsAsync removes all indexed bookmarks belonging to a user.
// Fire-and-forget goroutine. Nil-safe (no-op when search is disabled).
func (c *Client) DeleteUserDocumentsAsync(userID string) {
	if c == nil {
		return
	}
	go func() {
		filter := fmt.Sprintf(`userId = "%s"`, userID)
		if _, err := c.index.DeleteDocumentsByFilterWithContext(context.Background(), filter, nil); err != nil {
			log.Printf("[search] deleteUserDocuments %s: %v", userID, err)
		}
	}()
}
```

- [ ] **Step 4: Write nil-safe test for DeleteUserDocumentsAsync**

In `internal/search/search_test.go`, add:

```go
func TestDeleteUserDocumentsAsync_NilSafe(t *testing.T) {
	var c *Client
	// Must not panic when called on a nil client.
	c.DeleteUserDocumentsAsync("any-user-id")
}
```

- [ ] **Step 5: Run test**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./internal/search/... -run TestDeleteUserDocumentsAsync -v
```

Expected: PASS

- [ ] **Step 6: Build check**

```bash
go build ./...
go vet ./...
```

Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add db/schema.pg.sql billing/provider.go internal/search/client.go internal/search/search_test.go
git commit -m "feat(deletion): add db columns, UserDeleter interface, search.DeleteUserDocumentsAsync"
```

---

## Task 2: Account deletion email template and mailer method

**Files:**
- Modify: `internal/mailer/mailer.go`
- Create: `internal/mailer/templates/account_deletion.html`
- Modify: `internal/mailer/mailer_test.go`

**Interfaces:**
- Consumes: `legalLinks` map (already in mailer.go)
- Produces:
  - `mailer.AccountDeletionEmailData` struct: `ExecutesAt time.Time`
  - `(*Mailer).SendAccountDeletion(ctx, to, name, purpose, lang string, data AccountDeletionEmailData) error`
  - `purpose` values: `"deletion_requested"`, `"deletion_reminder"`, `"deletion_executed"`

- [ ] **Step 1: Create account_deletion.html template**

Create `internal/mailer/templates/account_deletion.html`:

```html
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html dir="ltr" lang="en">
  <head>
    <meta content="text/html; charset=UTF-8" http-equiv="Content-Type" />
    <meta name="x-apple-disable-message-reformatting" />
    <meta name="color-scheme" content="light dark" />
    <meta name="supported-color-schemes" content="light dark" />
    <style>
      @media(prefers-color-scheme:dark){.dark_bg{background-color:rgb(10,10,10) !important}.dark_text{color:rgb(250,250,250) !important}.dark_muted{color:rgb(212,212,212) !important}}
    </style>
  </head>
  <body class="dark_bg dark_text" style="background-color:rgb(255,255,255);color:rgb(10,10,10)">
    <!--$-->
    <table border="0" width="100%" cellpadding="0" cellspacing="0" role="presentation" align="center">
      <tbody>
        <tr>
          <td style="background-color:rgb(255,255,255);color:rgb(10,10,10)">
            <table align="center" width="100%" border="0" cellpadding="0" cellspacing="0" role="presentation"
              style="max-width:600px;padding-left:0;padding-right:0">
              <tbody>
                <tr style="width:100%">
                  <td>
                    <!-- Logo -->
                    <table border="0" cellpadding="0" cellspacing="0" role="presentation">
                      <tr>
                        <td>
                          <img alt="TabSlate" src="https://tabslate.com/logo.svg"
                            style="height:32px;width:auto;display:block;outline:none;border:none;text-decoration:none" />
                        </td>
                        <td style="padding-left:8px;font-size:20px;font-weight:700;vertical-align:middle;">TabSlate</td>
                      </tr>
                    </table>
                    <!-- Heading + Greeting -->
                    <div style="padding-top:16px;padding-bottom:2px;padding-left:4px;padding-right:4px;text-align:left">
                      <h1 style="font-size:36px;line-height:36px;font-weight:800;letter-spacing:-0.4px;margin:0;padding:0">
                        <span style="font-weight:700">{{.Heading}}</span>
                      </h1>
                      <p style="font-size:18px;line-height:28px;margin:8px 0 0 0;">Hi {{.Name}},</p>
                    </div>
                    <!-- Body -->
                    <div style="padding-top:3px;padding-bottom:3px;text-align:left">
                      <p style="font-size:16px;line-height:28px;margin:8px;padding:0;white-space:pre-line">{{.Intro}}</p>
                    </div>
                    <!-- Spacer -->
                    <div style="padding-top:3px;padding-bottom:3px;">
                      <p style="font-size:16px;line-height:1.75;margin:0;padding:0;">&nbsp;</p>
                    </div>
                    <!-- Note -->
                    <div style="padding-top:3px;padding-bottom:3px;text-align:left">
                      <p style="font-size:14px;line-height:20px;margin:0;padding:0;">
                        <span>{{.Note}}</span>
                      </p>
                    </div>
                    <hr style="width:100%;border:none;border-top:1px solid #eaeaea" />
                    <!-- Footer logo -->
                    <table border="0" cellpadding="0" cellspacing="0" role="presentation">
                      <tr>
                        <td>
                          <img alt="TabSlate" src="https://tabslate.com/logo.svg"
                            style="height:24px;width:auto;display:block;outline:none;border:none;text-decoration:none" />
                        </td>
                        <td style="padding-left:6px;font-size:14px;font-weight:700;vertical-align:middle;">TabSlate</td>
                      </tr>
                    </table>
                    <div style="padding-top:8px;padding-bottom:3px;text-align:left">
                      <p style="font-size:14px;line-height:20px;margin:0;padding:0;">
                        <a href="{{.PrivacyURL}}" style="color:rgb(115,115,115);text-decoration:underline;">{{.PrivacyText}}</a>
                        <span class="dark_muted" style="color:rgb(115,115,115);padding:0 6px;">·</span>
                        <a href="{{.TermsURL}}" style="color:rgb(115,115,115);text-decoration:underline;">{{.TermsText}}</a>
                      </p>
                    </div>
                    <div style="padding-top:8px;padding-bottom:3px;text-align:left">
                      <p style="font-size:14px;line-height:20px;margin:0;padding:0;">
                        <span class="dark_muted" style="color:rgb(115,115,115);">©2026 TabSlate. All rights reserved.</span>
                      </p>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </td>
        </tr>
      </tbody>
    </table>
    <!--7--><!--/$-->
  </body>
</html>
```

- [ ] **Step 2: Add types and translations to mailer.go**

In `internal/mailer/mailer.go`, add after the `legalLinks` map:

```go
// AccountDeletionEmailData carries the computed execution date for deletion emails.
// ExecutesAt is zero for the "deletion_executed" purpose (already happened).
type AccountDeletionEmailData struct {
	ExecutesAt time.Time
}

type deletionStrings struct {
	Subject string
	Heading string
	// Intro may contain a single %s placeholder for the formatted execution date.
	// If ExecutesAt is zero, Intro is used as-is.
	Intro string
	Note  string
}

type deletionEmailData struct {
	Name        string
	Heading     string
	Intro       string
	Note        string
	PrivacyText string
	PrivacyURL  string
	TermsText   string
	TermsURL    string
}

var deletionTranslations = map[string]map[string]deletionStrings{
	"deletion_requested": {
		"en": {
			Subject: "Your TabSlate account deletion is scheduled",
			Heading: "Account deletion scheduled",
			Intro:   "We've received your account deletion request. Your account and all associated data will be permanently deleted on %s.\n\nTo cancel this request, simply log in to your TabSlate account before that date — no other action is required.",
			Note:    "If you didn't request this, please log in immediately to cancel the deletion.",
		},
		"zh": {
			Subject: "您的 TabSlate 账号注销申请已受理",
			Heading: "账号注销申请已受理",
			Intro:   "我们已收到您的账号注销申请。您的账号及所有关联数据将于 %s 被永久删除。\n\n如需取消注销，只需在此日期之前重新登录 TabSlate 即可——无需其他任何操作。",
			Note:    "如果您未发起此申请，请立即登录以取消注销。",
		},
	},
	"deletion_reminder": {
		"en": {
			Subject: "Your TabSlate account will be deleted in 3 days",
			Heading: "Account deletion in 3 days",
			Intro:   "This is a reminder that your TabSlate account is scheduled for permanent deletion on %s.\n\nTo cancel, simply log in to your account before that date — no other action is required.",
			Note:    "If you want to keep your account, log in before the deadline.",
		},
		"zh": {
			Subject: "您的 TabSlate 账号将在 3 天后注销",
			Heading: "账号将在 3 天后注销",
			Intro:   "提醒您：您的 TabSlate 账号已计划于 %s 永久删除。\n\n如需取消，只需在此日期之前重新登录账号即可——无需其他操作。",
			Note:    "如果您希望保留账号，请在截止日期前登录。",
		},
	},
	"deletion_executed": {
		"en": {
			Subject: "Your TabSlate account has been deleted",
			Heading: "Account deleted",
			Intro:   "Your TabSlate account and all associated data have been permanently deleted. Thank you for using TabSlate.",
			Note:    "If you didn't request account deletion, please contact us at privacy@cs.tabslate.com.",
		},
		"zh": {
			Subject: "您的 TabSlate 账号已注销",
			Heading: "账号已注销",
			Intro:   "您的 TabSlate 账号及所有关联数据已永久删除。感谢您使用 TabSlate。",
			Note:    "如果您未申请注销账号，请通过 privacy@cs.tabslate.com 联系我们。",
		},
	},
}
```

- [ ] **Step 3: Add deletionTmpl field to Mailer struct and New()**

In `internal/mailer/mailer.go`, add `deletionTmpl *template.Template` field to the `Mailer` struct (after `tmpl`):

```go
tmpl         *template.Template
deletionTmpl *template.Template
```

In `New()`, add after `tmpl: template.Must(...)`:

```go
deletionTmpl: template.Must(template.ParseFS(tmplFS, "templates/account_deletion.html")),
```

- [ ] **Step 4: Add SendAccountDeletion and renderAccountDeletion methods**

Add to `internal/mailer/mailer.go` (after `SendOTP`):

```go
// SendAccountDeletion renders the account-deletion email and sends it.
// purpose: "deletion_requested" | "deletion_reminder" | "deletion_executed"
func (m *Mailer) SendAccountDeletion(ctx context.Context, to, name, purpose, lang string, data AccountDeletionEmailData) error {
	subject, body, err := m.renderAccountDeletion(name, purpose, lang, data)
	if err != nil {
		return err
	}
	return m.Send(ctx, to, subject, body)
}

func (m *Mailer) renderAccountDeletion(name, purpose, lang string, data AccountDeletionEmailData) (string, string, error) {
	byPurpose, ok := deletionTranslations[purpose]
	if !ok {
		return "", "", fmt.Errorf("unknown deletion purpose %q", purpose)
	}

	strs := byPurpose["en"]
	if byLang, ok := byPurpose[lang]; ok {
		strs = byLang
	}

	links := legalLinks["en"]
	if localized, ok := legalLinks[lang]; ok {
		links = localized
	}

	intro := strs.Intro
	if !data.ExecutesAt.IsZero() {
		intro = fmt.Sprintf(strs.Intro, data.ExecutesAt.Format("January 2, 2006"))
	}

	var body bytes.Buffer
	if err := m.deletionTmpl.Execute(&body, deletionEmailData{
		Name:        name,
		Heading:     strs.Heading,
		Intro:       intro,
		Note:        strs.Note,
		PrivacyText: links.PrivacyText,
		PrivacyURL:  links.PrivacyURL,
		TermsText:   links.TermsText,
		TermsURL:    links.TermsURL,
	}); err != nil {
		return "", "", fmt.Errorf("render account_deletion template: %w", err)
	}

	return strs.Subject, body.String(), nil
}
```

- [ ] **Step 5: Write failing tests for renderAccountDeletion**

In `internal/mailer/mailer_test.go`, add:

```go
func TestRenderAccountDeletion_RequestedContainsDate(t *testing.T) {
	m := New(Config{})
	executes := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	subject, body, err := m.renderAccountDeletion("Alice", "deletion_requested", "en", AccountDeletionEmailData{ExecutesAt: executes})
	if err != nil {
		t.Fatalf("renderAccountDeletion: %v", err)
	}
	if subject != "Your TabSlate account deletion is scheduled" {
		t.Errorf("unexpected subject: %q", subject)
	}
	for _, want := range []string{"Alice", "July 26, 2026", "log in", "Privacy Policy"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestRenderAccountDeletion_ReminderZhContainsDate(t *testing.T) {
	m := New(Config{})
	executes := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	_, body, err := m.renderAccountDeletion("李明", "deletion_reminder", "zh", AccountDeletionEmailData{ExecutesAt: executes})
	if err != nil {
		t.Fatalf("renderAccountDeletion zh: %v", err)
	}
	for _, want := range []string{"李明", "July 26, 2026", "隐私政策"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestRenderAccountDeletion_ExecutedNoDate(t *testing.T) {
	m := New(Config{})
	_, body, err := m.renderAccountDeletion("Bob", "deletion_executed", "en", AccountDeletionEmailData{})
	if err != nil {
		t.Fatalf("renderAccountDeletion executed: %v", err)
	}
	if strings.Contains(body, "%s") {
		t.Error("body contains unreplaced %s placeholder")
	}
}

func TestRenderAccountDeletion_UnknownPurpose(t *testing.T) {
	m := New(Config{})
	_, _, err := m.renderAccountDeletion("Bob", "unknown_purpose", "en", AccountDeletionEmailData{})
	if err == nil {
		t.Fatal("expected error for unknown purpose, got nil")
	}
}
```

- [ ] **Step 6: Run tests**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./internal/mailer/... -v
```

Expected: all tests PASS (including existing OTP tests)

- [ ] **Step 7: Build check**

```bash
go build ./...
go vet ./...
```

- [ ] **Step 8: Commit**

```bash
git add internal/mailer/mailer.go internal/mailer/templates/account_deletion.html internal/mailer/mailer_test.go
git commit -m "feat(deletion): add account deletion email template and SendAccountDeletion mailer method"
```

---

## Task 3: Model, AuthHandler.DeleteAccount, Login + Me updates, route registration

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/handler/auth.go`
- Modify: `internal/handler/auth_test.go`
- Modify: `app/server.go`

**Interfaces:**
- Consumes:
  - `auth.CheckPassword(hash, password string) error` (already exists)
  - `middleware.UserID(c *gin.Context) string` (already exists)
  - `(*mailer.Mailer).SendAccountDeletion(...)` (Task 2)
- Produces:
  - `model.DeleteAccountRequest` struct with `Password string`
  - `model.User.DeletionScheduledAt *int64 \`json:"deletion_scheduled_at"\``
  - `(*AuthHandler).DeleteAccount(c *gin.Context)`
  - `POST /auth/delete-account` route (rate-limited under auth limiter)

- [ ] **Step 1: Extend model.User and add DeleteAccountRequest**

In `internal/model/model.go`, add `DeletionScheduledAt` to the `User` struct:

```go
type User struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	IsVerified bool   `json:"is_verified"`

	PasswordHash        string `json:"-"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
	SuspendedAt         *int64 `json:"-"`
	DeletionScheduledAt *int64 `json:"deletion_scheduled_at"`
}
```

After the `RefreshRequest` struct, add:

```go
// DeleteAccountRequest is the body for POST /auth/delete-account.
type DeleteAccountRequest struct {
	Password string `json:"password" binding:"required"`
}
```

- [ ] **Step 2: Update Login to write last_login_at**

In `internal/handler/auth.go`, in `AuthHandler.Login`, locate the line that calls `h.issueTokens(&user)` and add immediately before it:

```go
now := time.Now().Unix()
h.db.Exec(ctx,
	h.db.Rebind(`UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?`),
	now, now, user.ID,
)
```

Note: `now` may already be declared earlier in the function (it is not currently — there is no `now` in Login). Add the variable.

- [ ] **Step 3: Update Me to return deletion_scheduled_at**

In `AuthHandler.Me`, replace the SELECT query and Scan to also fetch `deletion_requested_at` and `last_login_at`:

```go
var deletionRequestedAt *int64
var lastLoginAt *int64
err := h.db.QueryRow(ctx,
	h.db.Rebind(`SELECT id, name, email, is_verified, created_at, updated_at, deletion_requested_at, last_login_at FROM users WHERE id = ?`), userID,
).Scan(&user.ID, &user.Name, &user.Email, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt, &deletionRequestedAt, &lastLoginAt)
```

Then compute `DeletionScheduledAt` and set it on the user before returning:

```go
if deletionRequestedAt != nil {
	basis := *deletionRequestedAt
	if lastLoginAt != nil && *lastLoginAt > basis {
		basis = *lastLoginAt
	}
	scheduled := basis + 30*24*60*60
	user.DeletionScheduledAt = &scheduled
}
```

- [ ] **Step 4: Implement DeleteAccount handler**

Add to `internal/handler/auth.go`:

```go
// POST /auth/delete-account
func (h *AuthHandler) DeleteAccount(c *gin.Context) {
	var req model.DeleteAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	now := time.Now().Unix()

	var user model.User
	var deletionRequestedAt *int64
	var lastLoginAt *int64
	if err := h.db.QueryRow(ctx,
		h.db.Rebind(`SELECT id, name, email, password_hash, deletion_requested_at, last_login_at FROM users WHERE id = ?`),
		userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &deletionRequestedAt, &lastLoginAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch user"})
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}

	// Idempotent: if a deletion is already pending, return its schedule.
	if deletionRequestedAt != nil {
		basis := *deletionRequestedAt
		if lastLoginAt != nil && *lastLoginAt > basis {
			basis = *lastLoginAt
		}
		executesAt := basis + 30*24*60*60
		if executesAt > now {
			c.JSON(http.StatusConflict, gin.H{
				"error":        "account deletion already scheduled",
				"scheduled_at": *deletionRequestedAt,
				"executes_at":  executesAt,
			})
			return
		}
	}

	if _, err := h.db.Exec(ctx,
		h.db.Rebind(`UPDATE users SET deletion_requested_at = ?, deletion_reminder_sent_at = NULL, updated_at = ? WHERE id = ?`),
		now, now, userID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to schedule deletion"})
		return
	}

	executesAt := now + 30*24*60*60
	lang := parseLang(c.GetHeader("Accept-Language"))
	go func() {
		data := mailer.AccountDeletionEmailData{ExecutesAt: time.Unix(executesAt, 0)}
		if err := h.mailer.SendAccountDeletion(context.Background(), user.Email, user.Name, "deletion_requested", lang, data); err != nil {
			log.Printf("failed to send account deletion email to %s: %v", user.Email, err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"scheduled_at": now,
		"executes_at":  executesAt,
	})
}
```

Note: add `"github.com/TabSlate-dev/TabSlate-server/internal/mailer"` to the import block in auth.go (it is not imported yet).

- [ ] **Step 5: Register the route in app/server.go**

In `app/server.go`, inside the `api` group (protected routes), add after `/auth/sse-token`:

```go
api.POST("/auth/delete-account",
    middleware.RateLimitByIP(s.infra.Limiter, s.cfg.RateLimitAuth, s.cfg.RateLimitAuthWindow),
    authH.DeleteAccount,
)
```

- [ ] **Step 6: Write failing tests**

In `internal/handler/auth_test.go`, add:

```go
func TestDeleteAccount_MissingPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/delete-account", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &AuthHandler{}
	h.DeleteAccount(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
```

- [ ] **Step 7: Run tests**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./internal/handler/... -run TestDeleteAccount -v
go test ./internal/handler/... -run TestParseLang -v
```

Expected: `TestDeleteAccount_MissingPassword` PASS; `TestParseLang` PASS

- [ ] **Step 8: Full build and vet**

```bash
go build ./...
go vet ./...
```

- [ ] **Step 9: Commit**

```bash
git add internal/model/model.go internal/handler/auth.go internal/handler/auth_test.go app/server.go
git commit -m "feat(deletion): add DeleteAccount endpoint, update Login + Me, register route"
```

---

## Task 4: CleanupHandler Phase 3 (reminder) and Phase 4 (execute deletion)

**Files:**
- Modify: `internal/handler/cleanup.go`
- Modify: `app/server.go`

**Interfaces:**
- Consumes:
  - `(*mailer.Mailer).SendAccountDeletion(...)` (Task 2)
  - `billing.UserDeleter` interface (Task 1)
  - `(*search.Client).DeleteUserDocumentsAsync(userID)` (Task 1)
- Produces: CleanupHandler runs Phase 3 (reminder emails) and Phase 4 (hard delete) daily

- [ ] **Step 1: Extend CleanupHandler struct and constructor**

In `internal/handler/cleanup.go`, update the struct and constructor:

```go
type CleanupHandler struct {
	db             *db.DB
	trashGraceDays int
	mailer         *mailer.Mailer
	billing        billing.Provider
	search         *search.Client
}

func NewCleanupHandler(d *db.DB, trashGraceDays int, m *mailer.Mailer, bp billing.Provider, sc *search.Client) *CleanupHandler {
	return &CleanupHandler{
		db:             d,
		trashGraceDays: trashGraceDays,
		mailer:         m,
		billing:        bp,
		search:         sc,
	}
}
```

Add the following imports to `cleanup.go` (they're not currently there):

```go
import (
	"context"
	"log"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/mailer"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
)
```

- [ ] **Step 2: Update runOnce to call Phase 3 and Phase 4**

In `CleanupHandler.runOnce`, add calls after `h.phase2(...)`:

```go
func (h *CleanupHandler) runOnce(ctx context.Context) {
	nowMs := time.Now().UnixMilli()
	graceMs := int64(h.trashGraceDays) * 24 * 60 * 60 * 1000
	tombstoneMs := int64(tombstoneWindowDays) * 24 * 60 * 60 * 1000

	h.phase1(ctx, nowMs, graceMs)
	h.phase2(ctx, nowMs, graceMs, tombstoneMs)
	h.phase3(ctx)
	h.phase4(ctx)
}
```

- [ ] **Step 3: Implement phase3 (send 3-day reminder)**

Add to `internal/handler/cleanup.go`:

```go
// phase3 sends the 3-day reminder email to users whose deletion is due within
// 3 days and who haven't yet received the reminder.
func (h *CleanupHandler) phase3(ctx context.Context) {
	now := time.Now().Unix()
	threeDays := int64(3 * 24 * 60 * 60)
	thirtyDays := int64(30 * 24 * 60 * 60)

	rows, err := h.db.Query(ctx,
		`SELECT id, name, email, GREATEST(COALESCE(last_login_at, 0), deletion_requested_at)
		 FROM users
		 WHERE deletion_requested_at IS NOT NULL
		   AND GREATEST(COALESCE(last_login_at, 0), deletion_requested_at) + $1 > $2
		   AND GREATEST(COALESCE(last_login_at, 0), deletion_requested_at) + $1 <= $2 + $3
		   AND deletion_reminder_sent_at IS NULL`,
		thirtyDays, now, threeDays,
	)
	if err != nil {
		log.Printf("cleanup phase3 query: %v", err)
		return
	}
	defer rows.Close()

	type candidate struct {
		id, name, email string
		basis           int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.name, &c.email, &c.basis); err != nil {
			log.Printf("cleanup phase3 scan: %v", err)
			continue
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		log.Printf("cleanup phase3 rows err: %v", err)
		return
	}

	for _, c := range candidates {
		executesAt := time.Unix(c.basis+thirtyDays, 0)
		go func(email, name string, execAt time.Time) {
			if err := h.mailer.SendAccountDeletion(context.Background(), email, name,
				"deletion_reminder", "en",
				mailer.AccountDeletionEmailData{ExecutesAt: execAt},
			); err != nil {
				log.Printf("cleanup phase3 send reminder to %s: %v", email, err)
			}
		}(c.email, c.name, executesAt)

		if _, err := h.db.Exec(ctx,
			`UPDATE users SET deletion_reminder_sent_at = $1 WHERE id = $2`,
			now, c.id,
		); err != nil {
			log.Printf("cleanup phase3 mark reminder sent for %s: %v", c.id, err)
		}
	}
}
```

- [ ] **Step 4: Implement phase4 (execute deletion)**

Add to `internal/handler/cleanup.go`:

```go
// phase4 hard-deletes accounts whose 30-day grace period has elapsed.
func (h *CleanupHandler) phase4(ctx context.Context) {
	now := time.Now().Unix()
	thirtyDays := int64(30 * 24 * 60 * 60)

	rows, err := h.db.Query(ctx,
		`SELECT id, name, email
		 FROM users
		 WHERE deletion_requested_at IS NOT NULL
		   AND GREATEST(COALESCE(last_login_at, 0), deletion_requested_at) + $1 <= $2`,
		thirtyDays, now,
	)
	if err != nil {
		log.Printf("cleanup phase4 query: %v", err)
		return
	}
	defer rows.Close()

	type account struct{ id, name, email string }
	var accounts []account
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.id, &a.name, &a.email); err != nil {
			log.Printf("cleanup phase4 scan: %v", err)
			continue
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		log.Printf("cleanup phase4 rows err: %v", err)
		return
	}

	for _, a := range accounts {
		// 1. Send confirmation email before deleting (address still exists).
		if err := h.mailer.SendAccountDeletion(context.Background(), a.email, a.name,
			"deletion_executed", "en",
			mailer.AccountDeletionEmailData{},
		); err != nil {
			log.Printf("cleanup phase4 send confirmation to %s: %v", a.email, err)
			// Non-fatal — proceed with deletion.
		}

		// 2. Hard delete — cascades to all child tables.
		if _, err := h.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, a.id); err != nil {
			log.Printf("cleanup phase4 delete user %s: %v", a.id, err)
			continue
		}

		// 3. Notify billing provider (optional interface).
		if ud, ok := h.billing.(billing.UserDeleter); ok {
			if err := ud.OnUserDeleted(context.Background(), a.id); err != nil {
				log.Printf("cleanup phase4 billing OnUserDeleted %s: %v", a.id, err)
			}
		}

		// 4. Remove from search index.
		h.search.DeleteUserDocumentsAsync(a.id)

		log.Printf("cleanup phase4: deleted account %s (%s)", a.id, a.email)
	}
}
```

- [ ] **Step 5: Update app/server.go to pass new args to NewCleanupHandler**

In `app/server.go`, find the line:

```go
cleanupH := handler.NewCleanupHandler(database, cfg.TrashGraceDays)
```

Replace with:

```go
cleanupH := handler.NewCleanupHandler(database, cfg.TrashGraceDays, m, bp, sc)
```

(`m`, `bp`, `sc` are already in scope as `s.mailer`, `s.billing`, `s.search` — but `New()` stores them on `s` after assignment, so use the local variables `m`, `bp`, `sc` directly before the `Server` struct is assembled. Alternatively reference `s.mailer`, `s.billing`, `s.search` if the cleanup handler is started after `s` is constructed. Looking at `New()`: `s` is created, then `s.setupRoutes()` runs, then `cleanupH` is created and `go cleanupH.Run(ctx)` is called. At that point `s.mailer`, `s.billing`, `s.search` are all set. Use `s.mailer`, `s.billing`, `s.search`.)

- [ ] **Step 6: Build and vet**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./...
go vet ./...
```

Expected: no errors

- [ ] **Step 7: Run all server tests**

```bash
go test ./...
```

Expected: all PASS

- [ ] **Step 8: Commit**

```bash
git add internal/handler/cleanup.go app/server.go
git commit -m "feat(deletion): CleanupHandler Phase 3 (reminder) and Phase 4 (hard delete)"
```

---

## Task 5: Frontend API, auth store, and translation keys

**Files:**
- Modify: `lib/api.ts` (TabSlate repo)
- Modify: `store/auth-store.ts` (TabSlate repo)
- Modify: `public/_locales/en/messages.json` (TabSlate repo)
- Modify: `public/_locales/zh_CN/messages.json` (TabSlate repo)

**Interfaces:**
- Produces:
  - `ApiUser.deletion_scheduled_at?: number | null`
  - `api.deleteAccount(baseUrl, accessToken, password)` → `Promise<{ scheduled_at: number; executes_at: number }>`
  - `useAuthStore` action: `requestAccountDeletion(password: string): Promise<{ scheduled_at: number; executes_at: number }>`
  - i18n keys: `settings_tabAccount`, `settings_accountInfoTitle`, `settings_accountEmail`, `settings_accountMemberSince`, `settings_accountDangerZoneTitle`, `settings_accountDangerZoneDesc`, `settings_accountDeleteBtn`, `settings_accountDeletionPendingTitle`, `settings_accountDeletionPendingDesc`, `settings_accountDeletionConfirmTitle`, `settings_accountDeletionConfirmDesc`, `settings_accountDeletionPasswordLabel`, `settings_accountDeletionPasswordError`, `settings_accountDeletionAlreadyScheduled`, `settings_accountDeletionSubmitBtn`

All work is in the `TabSlate` repo (`/Users/lieutenant/Documents/github/TabSlate`).

- [ ] **Step 1: Extend ApiUser in lib/api.ts**

In `lib/api.ts`, update `ApiUser`:

```typescript
export interface ApiUser {
  id: string;
  name: string;
  email: string;
  is_verified: boolean;
  created_at: number;
  updated_at: number;
  deletion_scheduled_at?: number | null;
}
```

- [ ] **Step 2: Add deleteAccount to api object in lib/api.ts**

In `lib/api.ts`, add to the `api` object (after `sseToken`):

```typescript
deleteAccount(
  baseUrl: string,
  accessToken: string,
  password: string,
): Promise<{ scheduled_at: number; executes_at: number }> {
  return request<{ scheduled_at: number; executes_at: number }>(
    baseUrl,
    "/auth/delete-account",
    {
      method: "POST",
      accessToken,
      body: JSON.stringify({ password }),
    },
  );
},
```

- [ ] **Step 3: Add requestAccountDeletion to auth store**

In `store/auth-store.ts`, add `requestAccountDeletion` to the `AuthState` interface:

```typescript
requestAccountDeletion: (password: string) => Promise<{ scheduled_at: number; executes_at: number }>;
```

In the store implementation, add the action (inside the `(set, get) => ({...})` object, after `resetPassword`):

```typescript
requestAccountDeletion: async (password) => {
  const { serverUrl, accessToken } = get();
  if (!accessToken) throw new Error("not authenticated");
  const result = await api.deleteAccount(serverUrl, accessToken, password);
  // Refresh user so deletion_scheduled_at is populated in UI.
  const me = await api.me(serverUrl, accessToken);
  set({ user: me.user });
  return result;
},
```

- [ ] **Step 4: Add i18n keys to en/messages.json**

In `public/_locales/en/messages.json`, add after the `"settings_done"` entry (before `"addBookmark_title"`):

```json
  "settings_tabAccount": {
    "message": "Account"
  },
  "settings_accountInfoTitle": {
    "message": "Account Info"
  },
  "settings_accountEmail": {
    "message": "Email"
  },
  "settings_accountMemberSince": {
    "message": "Member since"
  },
  "settings_accountDangerZoneTitle": {
    "message": "Delete Account"
  },
  "settings_accountDangerZoneDesc": {
    "message": "Your account and all associated data will be permanently deleted after a 30-day grace period. To cancel at any time, simply log in — no other action is required."
  },
  "settings_accountDeleteBtn": {
    "message": "Delete Account"
  },
  "settings_accountDeletionPendingTitle": {
    "message": "Account Deletion Scheduled"
  },
  "settings_accountDeletionPendingDesc": {
    "message": "Your account is scheduled for deletion on $1. Log in before that date to cancel.",
    "placeholders": {
      "1": {
        "content": "$1",
        "example": "July 26, 2026"
      }
    }
  },
  "settings_accountDeletionConfirmTitle": {
    "message": "Confirm Account Deletion"
  },
  "settings_accountDeletionConfirmDesc": {
    "message": "Your account and all data will be permanently deleted after a 30-day grace period. To cancel, simply log in before the deadline — no other action is required."
  },
  "settings_accountDeletionPasswordLabel": {
    "message": "Current password"
  },
  "settings_accountDeletionPasswordError": {
    "message": "Incorrect password. Please try again."
  },
  "settings_accountDeletionAlreadyScheduled": {
    "message": "An account deletion is already scheduled."
  },
  "settings_accountDeletionSubmitBtn": {
    "message": "Delete Account"
  },
```

- [ ] **Step 5: Add i18n keys to zh_CN/messages.json**

In `public/_locales/zh_CN/messages.json`, add the same keys after `"settings_done"`:

```json
  "settings_tabAccount": {
    "message": "账户"
  },
  "settings_accountInfoTitle": {
    "message": "账户信息"
  },
  "settings_accountEmail": {
    "message": "邮箱"
  },
  "settings_accountMemberSince": {
    "message": "注册时间"
  },
  "settings_accountDangerZoneTitle": {
    "message": "注销账户"
  },
  "settings_accountDangerZoneDesc": {
    "message": "您的账户及所有关联数据将在 30 天宽限期后永久删除。如需取消，在此期间重新登录即可——无需其他任何操作。"
  },
  "settings_accountDeleteBtn": {
    "message": "注销账户"
  },
  "settings_accountDeletionPendingTitle": {
    "message": "注销申请处理中"
  },
  "settings_accountDeletionPendingDesc": {
    "message": "您的账户已计划于 $1 删除。请在此日期之前登录以取消。",
    "placeholders": {
      "1": {
        "content": "$1",
        "example": "2026年7月26日"
      }
    }
  },
  "settings_accountDeletionConfirmTitle": {
    "message": "确认注销账户"
  },
  "settings_accountDeletionConfirmDesc": {
    "message": "您的账户及所有数据将在 30 天宽限期后永久删除。如需取消，只需在截止日期前登录——无需其他操作。"
  },
  "settings_accountDeletionPasswordLabel": {
    "message": "当前密码"
  },
  "settings_accountDeletionPasswordError": {
    "message": "密码错误，请重试。"
  },
  "settings_accountDeletionAlreadyScheduled": {
    "message": "账户注销申请已处理中。"
  },
  "settings_accountDeletionSubmitBtn": {
    "message": "注销账户"
  },
```

- [ ] **Step 6: TypeScript build check**

```bash
cd /Users/lieutenant/Documents/github/TabSlate
npx tsc --noEmit
```

Expected: no type errors

- [ ] **Step 7: Commit**

```bash
cd /Users/lieutenant/Documents/github/TabSlate
git add lib/api.ts store/auth-store.ts public/_locales/en/messages.json public/_locales/zh_CN/messages.json
git commit -m "feat(deletion): extend ApiUser, add deleteAccount API, requestAccountDeletion store action, i18n keys"
```

---

## Task 6: Settings dialog — Account tab and DeleteAccountDialog

**Files:**
- Modify: `components/dashboard/settings-dialog.tsx` (TabSlate repo)

**Interfaces:**
- Consumes:
  - `useAuthStore(s => s.user)` → `ApiUser | null` (with `deletion_scheduled_at`)
  - `useAuthStore(s => s.requestAccountDeletion)` (Task 5)
  - `t(key, substitutions?)` from `useTranslation`
  - All i18n keys defined in Task 5

All work is in `/Users/lieutenant/Documents/github/TabSlate`.

- [ ] **Step 1: Extend tab type and add tab button**

In `settings-dialog.tsx`, change the tab type declaration:

```typescript
// Change:
const [activeTab, setActiveTab] = React.useState<"general" | "engines" | "plan">("general");

// To:
const [activeTab, setActiveTab] = React.useState<"general" | "engines" | "plan" | "account">("general");
```

Also update the `initialTab` prop type in `SettingsDialogProps`:

```typescript
interface SettingsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialTab?: "general" | "engines" | "plan" | "account";
}
```

Add a fourth tab button after the "Plan & Quotas" button (inside the tab switcher div):

```tsx
<button
  onClick={() => setActiveTab("account")}
  className={cn(
    "flex-1 py-1.5 px-3 text-xs font-semibold rounded-lg transition-all text-center cursor-pointer",
    activeTab === "account"
      ? "bg-background text-foreground shadow-xs font-bold"
      : "text-muted-foreground hover:text-foreground"
  )}
>
  {t("settings_tabAccount")}
</button>
```

- [ ] **Step 2: Add Account tab content**

Import `useAuthStore` at the top of the file (add to existing imports):

```typescript
import { useAuthStore } from "@/store/auth-store";
```

Add the following hooks inside `SettingsDialog` (near other hooks, before `return`):

```typescript
const user = useAuthStore(s => s.user);
const requestAccountDeletion = useAuthStore(s => s.requestAccountDeletion);
const [deleteDialogOpen, setDeleteDialogOpen] = React.useState(false);
```

Add the Account tab panel inside the `<div className="flex-1 overflow-y-auto ...">` block, after the plan tab panel:

```tsx
{activeTab === "account" && (
  <div className="space-y-6 animate-in fade-in duration-200">
    {/* Account Info */}
    <div className="space-y-2">
      <h3 className="text-sm font-semibold">{t("settings_accountInfoTitle")}</h3>
      <div className="rounded-lg border p-3 bg-card/20 space-y-2">
        <div className="flex items-center justify-between text-sm">
          <span className="text-muted-foreground">{t("settings_accountEmail")}</span>
          <span className="font-medium truncate max-w-[60%] text-right">{user?.email}</span>
        </div>
        <div className="flex items-center justify-between text-sm">
          <span className="text-muted-foreground">{t("settings_accountMemberSince")}</span>
          <span className="font-medium">
            {user?.created_at
              ? new Date(user.created_at * 1000).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })
              : "—"}
          </span>
        </div>
      </div>
    </div>

    {/* Danger Zone */}
    <div className="space-y-2">
      <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 space-y-3">
        {user?.deletion_scheduled_at ? (
          <>
            <h3 className="text-sm font-semibold text-destructive">{t("settings_accountDeletionPendingTitle")}</h3>
            <p className="text-xs text-muted-foreground leading-relaxed">
              {t("settings_accountDeletionPendingDesc", [
                new Date(user.deletion_scheduled_at * 1000).toLocaleDateString("en-US", {
                  month: "long", day: "numeric", year: "numeric",
                }),
              ])}
            </p>
          </>
        ) : (
          <>
            <h3 className="text-sm font-semibold text-destructive">{t("settings_accountDangerZoneTitle")}</h3>
            <p className="text-xs text-muted-foreground leading-relaxed">
              {t("settings_accountDangerZoneDesc")}
            </p>
            <Button
              variant="destructive"
              size="sm"
              className="cursor-pointer"
              onClick={() => setDeleteDialogOpen(true)}
            >
              {t("settings_accountDeleteBtn")}
            </Button>
          </>
        )}
      </div>
    </div>
  </div>
)}
```

- [ ] **Step 3: Add DeleteAccountDialog component**

Add the following component at the top of the file (above `SortableSearchEngineItem`):

```tsx
function DeleteAccountDialog({
  open,
  onOpenChange,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (password: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = React.useState("");
  const [error, setError] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(false);

  React.useEffect(() => {
    if (!open) {
      setPassword("");
      setError(null);
      setLoading(false);
    }
  }, [open]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await onConfirm(password);
      onOpenChange(false);
    } catch (err: any) {
      if (err?.status === 401) {
        setError(t("settings_accountDeletionPasswordError"));
      } else if (err?.status === 409) {
        setError(t("settings_accountDeletionAlreadyScheduled"));
      } else {
        setError(err?.message ?? "Something went wrong.");
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle className="text-destructive">{t("settings_accountDeletionConfirmTitle")}</DialogTitle>
          <DialogDescription>{t("settings_accountDeletionConfirmDesc")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4 pt-2">
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("settings_accountDeletionPasswordLabel")}
            </label>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
              required
            />
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => onOpenChange(false)}>
              {t("settings_cancel")}
            </Button>
            <Button
              type="submit"
              variant="destructive"
              size="sm"
              disabled={!password || loading}
              className="cursor-pointer"
            >
              {loading ? <Loader2 className="size-3.5 animate-spin mr-1" /> : null}
              {t("settings_accountDeletionSubmitBtn")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
```

- [ ] **Step 4: Wire up DeleteAccountDialog in the return JSX**

In `SettingsDialog`'s return, after the `<ImportDialog .../>` line, add:

```tsx
<DeleteAccountDialog
  open={deleteDialogOpen}
  onOpenChange={setDeleteDialogOpen}
  onConfirm={requestAccountDeletion}
/>
```

- [ ] **Step 5: TypeScript build check**

```bash
cd /Users/lieutenant/Documents/github/TabSlate
npx tsc --noEmit
```

Expected: no type errors

- [ ] **Step 6: Commit**

```bash
git add components/dashboard/settings-dialog.tsx
git commit -m "feat(deletion): add Account tab and DeleteAccountDialog to settings dialog"
```

---

## Task 7: Landing page legal document updates

**Files:**
- Modify: `src/messages/en.json` (TabSlate-Landing repo)
- Modify: `src/messages/zh.json` (TabSlate-Landing repo)

All work is in `/Users/lieutenant/Documents/github/TabSlate-Landing`.

- [ ] **Step 1: Update Privacy Policy — Data Storage (en.json)**

In `src/messages/en.json`, find the `privacy.dataStorage.content` field. Locate the sentence that begins:

> "To permanently delete your account and all associated cloud data, please submit a deletion request to privacy@cs.tabslate.com"

Replace that sentence (up to "associated data.") with:

> "You can permanently delete your account directly from the TabSlate extension: open **Settings → Account → Delete Account**, confirm with your password, and a 30-day grace period begins. During this window you may cancel the deletion at any time simply by logging in again — no action required beyond signing in. After 30 days without a login, your account and all associated cloud data (synced tabs, bookmarks, workspaces, collections, tags, and account information) are permanently purged from our databases. Three email notifications are sent: one immediately upon request, one reminder 3 days before the deadline, and one confirmation after deletion is complete."

- [ ] **Step 2: Update Privacy Policy — Your Rights (en.json)**

In `src/messages/en.json`, find the `privacy.userRights.content` field. Locate the bullet point beginning:

> "**Correction and Deletion:** You can request rectification of inaccurate data. To permanently delete your account..."

Replace from "To permanently delete…" to "…within 30 days." with:

> "To permanently delete your account and all associated cloud data, use the self-service **Settings → Account → Delete Account** flow in the extension: confirm with your password, and your account will be scheduled for deletion after a 30-day grace period during which you can cancel by simply logging in. For edge cases where you are unable to log in, contact us at privacy@cs.tabslate.com."

- [ ] **Step 3: Update Terms — Account Closure (en.json)**

In `src/messages/en.json`, find the `terms.termination.content` field. Locate the second paragraph beginning:

> "You may close your account at any time."

Replace that entire paragraph with:

> "You may close your account at any time via **Settings → Account → Delete Account** in the extension. Upon submitting a deletion request, a 30-day grace period begins during which all your data remains intact. Logging in at any point during this window cancels the request. After 30 days without a login, all associated bookmarks, workspaces, tab groups, and account information stored on our synchronization servers will be permanently and irreversibly purged."

- [ ] **Step 4: Update Privacy Policy — Data Storage (zh.json)**

In `src/messages/zh.json`, find `privacy.dataStorage.content`. Locate and replace:

> "如需永久删除账户及所有关联云端数据，请向 privacy@cs.tabslate.com 提交删除申请——我们将在核实您的身份与意愿后的 30 天内，手动处理请求并永久清除所有相关数据。"

With:

> "您可直接在 TabSlate 扩展中自助注销账户：前往**设置 → 账户 → 注销账户**，输入当前密码确认后，30 天宽限期随即开始。在此期间，您只需重新登录，即可随时取消注销申请——无需任何其他操作。若 30 天内未登录，您的账户及所有关联云端数据（已同步的标签页、书签、工作区、收藏集、标签及账户信息）将从我们的数据库中永久清除。整个过程中，系统会发送三封邮件通知：申请提交时、到期前 3 天提醒时，以及注销完成后。"

- [ ] **Step 5: Update Privacy Policy — Your Rights (zh.json)**

In `src/messages/zh.json`, find `privacy.userRights.content`. Locate and replace:

> "如需永久删除账户及所有关联云端数据，请向 privacy@cs.tabslate.com 提交删除申请。我们将手动处理您的请求，并在核实您的身份与意愿后的 30 日内，永久清除所有关联数据，包括已同步的标签页、书签、工作区及账户信息。"

With:

> "如需永久删除账户及所有关联云端数据，请使用扩展内的自助注销功能：前往**设置 → 账户 → 注销账户**，输入密码确认后，账户将在 30 天宽限期后被删除，宽限期内登录即可取消。如因特殊情况无法登录，请通过 privacy@cs.tabslate.com 联系我们。"

- [ ] **Step 6: Update Terms — Account Closure (zh.json)**

In `src/messages/zh.json`, find `terms.termination.content`. Locate and replace:

> "您可随时主动关闭账户。账户关闭后，存储在我们同步服务器上的所有关联书签、工作区及标签页分组，将依照数据留存策略被永久且不可逆地从数据库中清除。"

With:

> "您可随时在扩展的**设置 → 账户 → 注销账户**中主动关闭账户。提交注销申请后，将进入 30 天宽限期，期间您的所有数据保持完好。在此期间登录即可取消申请。若 30 天内未登录，存储在我们同步服务器上的所有关联书签、工作区、标签页分组及账户信息将被永久且不可逆地从数据库中清除。"

- [ ] **Step 7: Build check**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Landing
npx tsc --noEmit
```

Expected: no type errors

- [ ] **Step 8: Commit**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Landing
git add src/messages/en.json src/messages/zh.json
git commit -m "docs(legal): update privacy policy and terms to describe self-service account deletion"
```

---

## Self-Review Checklist

- [x] **DB columns**: `last_login_at`, `deletion_requested_at`, `deletion_reminder_sent_at` — Task 1
- [x] **Login writes last_login_at** — Task 3, Step 2
- [x] **Me returns deletion_scheduled_at** — Task 3, Step 3
- [x] **POST /auth/delete-account** with password verification, idempotency, 409 conflict — Task 3, Step 4–5
- [x] **deletion_reminder_sent_at cleared on re-request** — Task 3, Step 4 (UPDATE sets it NULL)
- [x] **Phase 3: 3-day reminder** with `deletion_reminder_sent_at IS NULL` guard — Task 4, Step 3
- [x] **Phase 4: hard delete** with cascade + billing hook + search cleanup — Task 4, Step 4
- [x] **billing.UserDeleter** optional interface — Task 1
- [x] **search.DeleteUserDocumentsAsync** nil-safe — Task 1
- [x] **Email template** for all 3 purposes, en + zh — Task 2
- [x] **"login to cancel" prominently in emails** — Task 2 (Intro field for deletion_requested and deletion_reminder)
- [x] **Frontend ApiUser.deletion_scheduled_at** — Task 5
- [x] **requestAccountDeletion store action** with me refetch — Task 5
- [x] **Account tab** with pending/not-pending states — Task 6
- [x] **DeleteAccountDialog** with password input, 401/409 error handling — Task 6
- [x] **Legal docs updated (en + zh)** — Task 7
- [x] **No cancel button** (login-only cancellation per spec) — confirmed: no cancel button in Task 6
