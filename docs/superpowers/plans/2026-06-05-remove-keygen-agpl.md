# Remove keygen.sh — AGPL Open-Source Edition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove all keygen.sh integration from the OSS backend, make registration unlimited by default (with an `ALLOW_REGISTRATION` kill-switch), always return PlanPro for subscription status, and update CLAUDE.md + Landing legal documents to reflect AGPL open-source status.

**Architecture:** Delete keygen files entirely; strip `licenseCache` and `InstanceLimiter` from the billing layer; handle registration gating as a simple boolean flag on `AuthHandler`; update Landing i18n JSON to add commercial-use restriction language.

**Tech Stack:** Go 1.25, pgxpool, Gin, Next.js i18n (next-intl), JSON translation files

---

## File Map

### TabSlate-server

| File | Action |
|------|--------|
| `billing/local/keygen.go` | Delete |
| `billing/local/keygen_test.go` | Delete |
| `billing/local/license_cache.go` | Delete |
| `billing/local/license_cache_test.go` | Delete |
| `billing/local/provider.go` | Rewrite (remove licenseCache, Start, enforceUserLimit; GetSubscription → always Pro) |
| `billing/local/provider_test.go` | Rewrite (remove keygen-dependent tests; add GetSubscription_alwaysPro) |
| `billing/provider.go` | Remove `InstanceLimiter` interface |
| `app/config.go` | Remove `KeygenLicenseKey`; add `AllowRegistration bool` |
| `internal/handler/auth.go` | Add `registrationOpen bool` field + param; replace InstanceLimiter check |
| `internal/handler/auth_test.go` | Add `TestRegister_registrationClosed` |
| `app/server.go` | Pass `cfg.AllowRegistration` to `NewAuthHandler` |
| `cmd/server/main.go` | `local.New(database)` (drop licenseKey); remove `bp.Start(ctx)` |
| `README.md` | Remove keygen build section; update license statement |
| `.env.example` | Remove `LICENSE_KEY` comment; add `ALLOW_REGISTRATION` |
| `CLAUDE.md` | Update env var table; update billing/local description |

### TabSlate-Landing (`/Users/lieutenant/Documents/github/TabSlate-Landing`)

| File | Action |
|------|--------|
| `src/messages/en.json` | Update `legal.license.sections.{intellectual,restrictions}` and `legal.terms.sections.{services,prohibited}` |
| `src/messages/zh.json` | Same keys in Chinese |
| `CLAUDE.md` | Note backend is now AGPL open-source |

---

## Task 1: Write failing test for registration-disabled feature

**Files:**
- Modify: `internal/handler/auth_test.go`

- [ ] **Step 1: Add the failing test**

Open `internal/handler/auth_test.go` and add the following (the file currently only contains `TestParseLang`):

```go
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseLang(t *testing.T) {
	tests := []struct {
		name       string
		acceptLang string
		want       string
	}{
		{name: "zh-CN", acceptLang: "zh-CN,zh;q=0.9,en;q=0.8", want: "zh"},
		{name: "zh", acceptLang: "zh", want: "zh"},
		{name: "zh-TW", acceptLang: "zh-TW,zh;q=0.9", want: "zh"},
		{name: "en-US", acceptLang: "en-US,en;q=0.9", want: "en"},
		{name: "en", acceptLang: "en", want: "en"},
		{name: "fr-FR", acceptLang: "fr-FR,fr;q=0.9", want: "en"},
		{name: "empty", acceptLang: "", want: "en"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLang(tc.acceptLang); got != tc.want {
				t.Fatalf("parseLang(%q) = %q, want %q", tc.acceptLang, got, tc.want)
			}
		})
	}
}

func TestRegister_registrationClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"email":"test@example.com","password":"test123456","name":"Test"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &AuthHandler{registrationOpen: false}
	h.Register(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "registration is disabled on this instance" {
		t.Errorf("unexpected error: %q", resp["error"])
	}
}
```

- [ ] **Step 2: Verify the test fails to compile**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./internal/handler/... 2>&1 | head -10
```

Expected: compile error mentioning `AuthHandler` has no field `registrationOpen`.

---

## Task 2: Add `registrationOpen` to `AuthHandler` and implement the check

**Files:**
- Modify: `internal/handler/auth.go`

- [ ] **Step 1: Add `registrationOpen bool` field to `AuthHandler` struct**

The struct is at lines 43–57 of `internal/handler/auth.go`. Add `registrationOpen bool` as the last field:

```go
type AuthHandler struct {
	db      *db.DB
	secret  string
	billing billing.Provider
	captcha *captcha.Verifier
	mailer  *mailer.Mailer
	limiter ratelimit.Limiter
	cache   store.Cache

	registrationOpen bool

	registerCaptchaThreshold int
	registerCaptchaWindow    time.Duration

	otpCaptchaThreshold int
	otpCaptchaWindow    time.Duration
}
```

- [ ] **Step 2: Add `registrationOpen bool` parameter to `NewAuthHandler` and set it**

`NewAuthHandler` starts at line 59. Add the new parameter after `otpWindow time.Duration`:

```go
func NewAuthHandler(
	d *db.DB,
	secret string,
	bp billing.Provider,
	cv *captcha.Verifier,
	m *mailer.Mailer,
	l ratelimit.Limiter,
	cache store.Cache,
	registerThreshold int,
	registerWindow time.Duration,
	otpThreshold int,
	otpWindow time.Duration,
	registrationOpen bool,
) *AuthHandler {
	return &AuthHandler{
		db:                       d,
		secret:                   secret,
		billing:                  bp,
		captcha:                  cv,
		mailer:                   m,
		limiter:                  l,
		cache:                    cache,
		registrationOpen:         registrationOpen,
		registerCaptchaThreshold: registerThreshold,
		registerCaptchaWindow:    registerWindow,
		otpCaptchaThreshold:      otpThreshold,
		otpCaptchaWindow:         otpWindow,
	}
}
```

- [ ] **Step 3: Replace the `InstanceLimiter` check with the flag check in `Register()`**

Find this block (around line 123):

```go
	if il, ok := h.billing.(billing.InstanceLimiter); ok {
		if err := il.CheckRegistrationAllowed(ctx); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	}
```

Replace it with:

```go
	if !h.registrationOpen {
		c.JSON(http.StatusForbidden, gin.H{"error": "registration is disabled on this instance"})
		return
	}
```

- [ ] **Step 4: Run the test and verify it passes**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./internal/handler/... -run TestRegister_registrationClosed -v
```

Expected:
```
--- PASS: TestRegister_registrationClosed (0.00s)
PASS
```

---

## Task 3: Remove `InstanceLimiter` from `billing/provider.go`

**Files:**
- Modify: `billing/provider.go`

- [ ] **Step 1: Delete the `InstanceLimiter` interface**

Find and remove this block (the last 12 lines of `billing/provider.go`):

```go
// InstanceLimiter is implemented by providers that enforce instance-level user
// count limits. OSS local.Provider implements this; Cloud meteroid.Provider
// does not. auth.Register uses a type assertion — this is NOT part of
// billing.Provider.
type InstanceLimiter interface {
	// CheckRegistrationAllowed returns an error if registering a new user would
	// exceed the instance's licensed user count.
	CheckRegistrationAllowed(ctx context.Context) error
}
```

- [ ] **Step 2: Verify it compiles (will fail until Task 5 fixes provider.go)**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./billing/... 2>&1 | head -10
```

Expected: compile error in `billing/local/provider.go` referencing `billing.InstanceLimiter`. That's fine — fixed in Task 5.

---

## Task 4: Delete keygen files

**Files:**
- Delete: `billing/local/keygen.go`
- Delete: `billing/local/keygen_test.go`
- Delete: `billing/local/license_cache.go`
- Delete: `billing/local/license_cache_test.go`

- [ ] **Step 1: Delete the four files**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
rm billing/local/keygen.go billing/local/keygen_test.go billing/local/license_cache.go billing/local/license_cache_test.go
```

- [ ] **Step 2: Confirm deletion**

```bash
ls billing/local/
```

Expected output: `provider.go  provider_test.go` (only two files remain)

---

## Task 5: Rewrite `billing/local/provider.go` and `provider_test.go`

**Files:**
- Rewrite: `billing/local/provider.go`
- Rewrite: `billing/local/provider_test.go`

- [ ] **Step 1: Write new `provider_test.go`**

Replace the entire contents of `billing/local/provider_test.go`:

```go
package local

import (
	"context"
	"testing"

	"github.com/tabslate/server/billing"
)

var _ billing.Provider = (*Provider)(nil)

func TestNew_returnsProvider(t *testing.T) {
	p := New(nil)
	if p == nil {
		t.Fatal("New(nil) should return a non-nil Provider")
	}
}

func TestGetSubscription_alwaysPro(t *testing.T) {
	p := New(nil)
	sub, err := p.GetSubscription(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Plan != billing.PlanPro {
		t.Errorf("plan = %q, want %q", sub.Plan, billing.PlanPro)
	}
	if sub.Status != "active" {
		t.Errorf("status = %q, want active", sub.Status)
	}
}

func TestGetLimits_nilDB_returnsUnlimited(t *testing.T) {
	p := New(nil)
	limits, err := p.GetLimits(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limits.MaxBookmarks != -1 {
		t.Errorf("MaxBookmarks = %d, want -1 (unlimited)", limits.MaxBookmarks)
	}
	if limits.MaxWorkspaces != -1 {
		t.Errorf("MaxWorkspaces = %d, want -1 (unlimited)", limits.MaxWorkspaces)
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails (provider.go not yet updated)**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./billing/local/... 2>&1 | head -15
```

Expected: compile errors from `provider.go` referencing deleted types (`licenseCache`, `keygenClient`, `billing.InstanceLimiter`).

- [ ] **Step 3: Rewrite `billing/local/provider.go`**

Replace the entire contents of `billing/local/provider.go`:

```go
// Package local implements billing.Provider for the OSS self-hosted edition.
// All resource limits are unlimited by default. Subscription always reports
// PlanPro — self-hosters have full feature access.
package local

import (
	"context"
	"fmt"

	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
)

var _ billing.Provider = (*Provider)(nil)

// Provider is the OSS billing implementation.
type Provider struct {
	db *db.DB
}

// New creates a local Provider. Pass nil for db only in tests.
func New(d *db.DB) *Provider {
	return &Provider{db: d}
}

// OnUserCreated is a no-op for the OSS edition.
func (p *Provider) OnUserCreated(_ context.Context, _ billing.UserInfo) error {
	return nil
}

// GetLimits returns the OSS resource caps from subscription_capacity, falling
// back to unlimited defaults when the row is absent or db is nil.
func (p *Provider) GetLimits(ctx context.Context, _ string) (*billing.Limits, error) {
	if p.db != nil {
		var l billing.Limits
		err := p.db.QueryRow(ctx, `
			SELECT max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days
			FROM subscription_capacity WHERE plan_code = 'unlimited'
		`).Scan(&l.MaxWorkspaces, &l.MaxBookmarks, &l.MaxCollections, &l.MaxTags, &l.MaxSavedGroups, &l.TrashGraceDays)
		if err == nil {
			return &l, nil
		}
	}
	return unlimitedLimits(), nil
}

// GetSubscription always returns PlanPro for the OSS edition — self-hosters
// have full feature access without a license key.
func (p *Provider) GetSubscription(_ context.Context, _ string) (*billing.Subscription, error) {
	return &billing.Subscription{Plan: billing.PlanPro, Status: "active"}, nil
}

// GetCheckoutURL is not supported in the OSS edition.
func (p *Provider) GetCheckoutURL(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf(
		"online checkout is not available in the OSS edition; " +
			"visit https://tabslate.com/pricing to purchase a license",
	)
}

// CancelSubscription is not supported in the OSS edition.
func (p *Provider) CancelSubscription(_ context.Context, _ string) error {
	return fmt.Errorf("subscription management is not available in the OSS edition")
}

// ListInvoices returns an empty slice for the OSS edition.
func (p *Provider) ListInvoices(_ context.Context, _ string, _, _ int) ([]billing.Invoice, error) {
	return nil, nil
}

func unlimitedLimits() *billing.Limits {
	return &billing.Limits{
		MaxWorkspaces:  -1,
		MaxBookmarks:   -1,
		MaxCollections: -1,
		MaxTags:        -1,
		MaxSavedGroups: -1,
		TrashGraceDays: -1,
	}
}
```

- [ ] **Step 4: Run the tests**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./billing/... -v
```

Expected: all tests in `billing/local/...` pass; `billing/` package has no tests (just interfaces), so PASS or no test files message.

---

## Task 6: Update `app/config.go`

**Files:**
- Modify: `app/config.go`

- [ ] **Step 1: Remove `KeygenLicenseKey` field and add `AllowRegistration bool`**

In the `Config` struct, remove:

```go
	// KeygenLicenseKey is the optional keygen.sh license key. Leave empty for
	// free-tier mode (3 users max).
	KeygenLicenseKey string
```

Add (after `GinMode`):

```go
	// AllowRegistration controls whether new user registration is open.
	// Set ALLOW_REGISTRATION=false to close registration after initial setup.
	// Defaults to true.
	AllowRegistration bool
```

- [ ] **Step 2: Update `LoadConfig()` to remove the keygen field and add the new one**

Remove from the `LoadConfig()` return literal:

```go
		KeygenLicenseKey: os.Getenv("KEYGEN_LICENSE_KEY"),
```

Add (after `GinMode: os.Getenv("GIN_MODE"),`):

```go
		AllowRegistration: os.Getenv("ALLOW_REGISTRATION") != "false",
```

The expression `os.Getenv("ALLOW_REGISTRATION") != "false"` defaults to `true` (returns `true` when the var is unset or any value other than `"false"`).

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./app/... 2>&1 | head -10
```

Expected: compile error in `cmd/server/main.go` (still passes `cfg.KeygenLicenseKey` to `local.New`). Fixed in Task 8.

---

## Task 7: Update `app/server.go`

**Files:**
- Modify: `app/server.go`

- [ ] **Step 1: Add `cfg.AllowRegistration` to the `NewAuthHandler` call**

Find the call at line 180:

```go
	authH := handler.NewAuthHandler(s.db, s.cfg.JWTSecret, s.billing, s.captcha, s.mailer,
		s.infra.Limiter, s.infra.Cache,
		s.cfg.RegisterCaptchaThreshold, s.cfg.RegisterCaptchaWindow,
		s.cfg.OTPCaptchaThreshold, s.cfg.OTPCaptchaWindow)
```

Replace with:

```go
	authH := handler.NewAuthHandler(s.db, s.cfg.JWTSecret, s.billing, s.captcha, s.mailer,
		s.infra.Limiter, s.infra.Cache,
		s.cfg.RegisterCaptchaThreshold, s.cfg.RegisterCaptchaWindow,
		s.cfg.OTPCaptchaThreshold, s.cfg.OTPCaptchaWindow,
		s.cfg.AllowRegistration)
```

- [ ] **Step 2: Verify app/server.go compiles**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./app/... 2>&1 | head -10
```

Expected: compile error only in `cmd/server/main.go`. Fixed next.

---

## Task 8: Update `cmd/server/main.go`

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Fix `local.New()` call and remove `bp.Start(ctx)`**

Find:

```go
	// ── Billing provider ──────────────────────────────────────────────────────────────────
	// OSS edition: quota is derived from the configured keygen.sh license, or
	// free-tier defaults when no license key is set.
	bp, err := local.New(cfg.KeygenLicenseKey, database)
	if err != nil {
		log.Fatalf("billing provider: %v", err)
	}
	bp.Start(ctx)
```

Replace with:

```go
	// ── Billing provider ──────────────────────────────────────────────────────────────────
	// OSS edition: unlimited users, all features unlocked. Registration gating
	// is controlled by ALLOW_REGISTRATION env var (handled in AuthHandler).
	bp := local.New(database)
```

(`local.New` no longer returns an error, so drop the error variable and check.)

- [ ] **Step 2: Full build — verify everything compiles**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./... 2>&1
```

Expected: no output (clean build).

- [ ] **Step 3: Run all tests**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./... 2>&1
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
git add billing/ app/ internal/handler/ cmd/
git commit -m "feat: remove keygen.sh, open-source under AGPL

- Delete billing/local/keygen.go and license_cache.go (keygen.sh client + cache)
- Remove InstanceLimiter interface from billing/provider.go
- Simplify billing/local/provider.go: no license key, GetSubscription always PlanPro
- Add ALLOW_REGISTRATION env var (default true) to disable new registrations
- AuthHandler.registrationOpen replaces type-assertion InstanceLimiter check"
```

---

## Task 9: Update `README.md` and `.env.example`

**Files:**
- Modify: `README.md`
- Modify: `.env.example`

- [ ] **Step 1: Rewrite `README.md`**

Replace the entire file with:

```markdown
# TabSlate Server

Go backend for the TabSlate Chrome extension. Released under **AGPL-3.0**.

---

## Building

```bash
go build -o tabslate-server ./cmd/server
```

Docker:

```bash
docker build -t tabslate-server .
```

---

## Runtime Environment Variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | ✅ | PostgreSQL DSN (`postgres://...`) |
| `JWT_SECRET` | ✅ | HMAC secret for access tokens |
| `PORT` | | HTTP port (default `8080`) |
| `GIN_MODE` | | Gin mode: `release` / `debug` (default `debug`) |
| `ALLOW_REGISTRATION` | | Set to `false` to disable new user registration (default `true`) |
| `PROSOPO_SECRET` | | Captcha secret; omit to disable captcha |
| `MAIL_PROVIDER` | | `smtp` / `resend` / `ses`; omit to auto-verify all registrations |

See `.env.example` for the full list including SMTP, Resend, SES, and rate-limit tunables.

---

## License

TabSlate Server is free software: you can redistribute it and/or modify it
under the terms of the **GNU Affero General Public License** as published by
the Free Software Foundation, either version 3 of the License, or (at your
option) any later version.

You may **not** use this software to operate a paid commercial synchronization
service for third parties without a separate commercial license from TabSlate.
See LICENSE for details.
```

- [ ] **Step 2: Update `.env.example`**

Remove the entire `── License (OSS self-hosted) ───` section:

```
# ── License (OSS self-hosted) ─────────────────────────────────────────────────
# License JWT purchased from tabslate.com/pricing.
# Leave empty to run in free-tier mode (1 workspace, 1 000 bookmarks, etc.).
# LICENSE_KEY=
```

Add the following block after the `GIN_MODE=debug` line:

```
# ── Registration ──────────────────────────────────────────────────────────────
# Set to "false" to close registration (e.g., after initial setup). Default: true.
# ALLOW_REGISTRATION=true
```

- [ ] **Step 3: Commit**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
git add README.md .env.example
git commit -m "docs: update README and .env.example for AGPL open-source"
```

---

## Task 10: Update `CLAUDE.md` in TabSlate-server

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the env var table**

Find and remove this row from the env var table:

```
| `KEYGEN_LICENSE_KEY` | | keygen.sh License key；空 = 免费模式（3 用户上限） |
```

Replace with:

```
| `ALLOW_REGISTRATION` | | `true`（默认）/ `false`；`false` 时 `POST /auth/register` 返回 403，禁止新用户注册 |
```

- [ ] **Step 2: Update the `billing/local` package description**

Find the row in the 包职责 table:

```
| `billing/local` | 公开 | OSS 实现：通过 keygen.sh License 验证用户数限制（Free = 3，Pro = 来自 License metadata）；机器激活、后台刷新、超限用户自动暂停；从 `subscription_capacity` DB 表读取资源配额（默认 -1 不限制） |
```

Replace with:

```
| `billing/local` | 公开 | OSS 实现：用户数无上限，订阅始终返回 PlanPro；从 `subscription_capacity` DB 表读取资源配额（默认 -1 不限制）；注册开关由 `ALLOW_REGISTRATION` 环境变量控制（在 auth handler 层检查，不在 billing 层） |
```

- [ ] **Step 3: Update `注意事项` — remove license_machine_fingerprint note**

Find and remove this sentence (part of the schema 位置 注意事项):

The note about `license_machine_fingerprint` in `server_config`. Specifically remove any sentence mentioning `license_machine_fingerprint` or keygen activation.

- [ ] **Step 4: Update the `项目概述` section**

Find:

```
- **免费版**（本仓库）：非开源，提供免费版，计费基于 keygen.sh License 验证（用户数限制），支持自托管
```

Replace with:

```
- **OSS 版**（本仓库）：AGPL-3.0 开源，用户数无上限，支持自托管；禁止使用本后端提供收费同步服务（无 TabSlate 商业授权）
```

- [ ] **Step 5: Update the `仓库关系` table description for TabSlate-server**

Find:

```
| **`TabSlate-server`**（本仓库） | 公开，非开源 | Go 后端，提供免费版，可自托管，计费基于 keygen.sh License 验证（用户数限制） |
```

Replace with:

```
| **`TabSlate-server`**（本仓库） | 公开，AGPL-3.0 | Go 后端，AGPL 开源，用户数无上限，可自托管；禁止未经授权将本后端用于商业收费服务 |
```

- [ ] **Step 6: Commit**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md to reflect AGPL open-source and keygen removal"
```

---

## Task 11: Update TabSlate-Landing i18n files

**Files:**
- Modify: `src/messages/en.json` (in `TabSlate-Landing`)
- Modify: `src/messages/zh.json` (in `TabSlate-Landing`)

All edits are in the `legal` key. Use your editor's find-and-replace on the exact strings below.

### en.json

- [ ] **Step 1: Update `legal.license.sections.intellectual.content`**

Find:

```
"**Open Source &amp; Free Components:** The core TabSlate Chrome extension is released under AGPL-3.0. The backend server is not open source but is available as a free version that permits self-hosting.\n\n**Proprietary Service Elements:**
```

Replace the first paragraph only (keep "**Proprietary Service Elements:**" paragraph intact):

```
"**Open Source Components:** Both the TabSlate Chrome extension and the backend server (TabSlate-server) are released under **AGPL-3.0**. Self-hosting is permitted under the terms of that license.\n\n**Proprietary Service Elements:**
```

- [ ] **Step 2: Update `legal.license.sections.restrictions.content` — add commercial-use bullet**

Find the last bullet in the restrictions content:

```
* Bypassing subscription keys or license verification algorithms to unlock premium quotas."
```

Replace with:

```
* Bypassing subscription keys or license verification algorithms to unlock premium quotas.\n* Using the AGPL-licensed **TabSlate-server** backend to operate a paid commercial synchronization service (i.e., charging end users for access to a hosted instance) without a separate commercial license from TabSlate. Personal and organizational self-hosting for non-commercial purposes is permitted under AGPL-3.0."
```

- [ ] **Step 3: Update `legal.terms.sections.services.content` — update "Free Components" bullet**

Find:

```
* **Free Components:** The Chrome extension frontend (AGPL-3.0) and the Go backend server (available as a free version, permitting self-hosting).
```

Replace with:

```
* **Free Components:** The Chrome extension frontend (AGPL-3.0) and the Go backend server (AGPL-3.0, open-source, permitting self-hosting).
```

- [ ] **Step 4: Update `legal.terms.sections.prohibited.content` — add commercial-use bullet**

Find the last bullet in prohibited content:

```
* Impersonating other users or attempting to hijack active JWT sessions."
```

Replace with:

```
* Impersonating other users or attempting to hijack active JWT sessions.\n* Using the open-source TabSlate-server backend to provide a paid commercial synchronization service to third parties without a separate commercial license from TabSlate."
```

### zh.json

- [ ] **Step 5: Update `legal.license.sections.intellectual.content` (Chinese)**

Find:

```
"**开源与免费组件：** TabSlate 核心 Chrome 扩展基于 AGPL-3.0 协议开源。后端服务器非开源，但提供免费版，允许自行部署。\n\n**专有服务组件：**
```

Replace the first paragraph:

```
"**开源组件：** TabSlate Chrome 扩展和后端服务器（TabSlate-server）均基于 **AGPL-3.0** 协议开源。在遵守该协议条款的前提下，允许自行部署。\n\n**专有服务组件：**
```

- [ ] **Step 6: Update `legal.license.sections.restrictions.content` (Chinese) — add commercial-use bullet**

Find the last bullet:

```
* 绕过订阅密钥或授权验证算法来解锁高级配额。"
```

Replace with:

```
* 绕过订阅密钥或授权验证算法来解锁高级配额。\n* 在未取得 TabSlate 单独商业授权的情况下，使用 AGPL 授权的 **TabSlate-server** 后端运营以向终端用户收费为目的的商业同步服务。出于非商业目的的个人或组织自托管行为在 AGPL-3.0 协议下属于合法使用。"
```

- [ ] **Step 7: Update `legal.terms.sections.services.content` (Chinese) — update "免费组件" bullet**

Find:

```
* **免费组件：** Chrome 扩展前端（AGPL-3.0）与 Go 后端服务器（提供免费版，允许自行部署）。
```

Replace with:

```
* **免费组件：** Chrome 扩展前端（AGPL-3.0）与 Go 后端服务器（AGPL-3.0 开源，允许自行部署）。
```

- [ ] **Step 8: Update `legal.terms.sections.prohibited.content` (Chinese) — add commercial-use bullet**

Find the last bullet:

```
* 冒充其他用户，或企图劫持活跃的 JWT 会话。"
```

Replace with:

```
* 冒充其他用户，或企图劫持活跃的 JWT 会话。\n* 在未取得 TabSlate 单独商业授权的情况下，使用开源的 TabSlate-server 后端向第三方提供以收费为目的的商业同步服务。"
```

- [ ] **Step 9: Verify JSON is valid**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Landing
python3 -c "import json; json.load(open('src/messages/en.json')); print('en.json OK')"
python3 -c "import json; json.load(open('src/messages/zh.json')); print('zh.json OK')"
```

Expected: both print `OK`.

- [ ] **Step 10: Commit Landing changes**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Landing
git add src/messages/en.json src/messages/zh.json
git commit -m "legal: update license + terms for AGPL open-source backend

- Backend server is now AGPL-3.0 open-source (was closed-source free version)
- Add commercial-use restriction: using TabSlate-server to operate a paid
  service requires a separate commercial license from TabSlate"
```

---

## Task 12: Update `TabSlate-Landing/CLAUDE.md`

**Files:**
- Modify: `TabSlate-Landing/CLAUDE.md`

- [ ] **Step 1: Update backend description in the 仓库关系 table**

Find:

```
| **`TabSlate-server`** | 公开，非开源 | Go 后端，提供免费版，可自托管，计费基于本地 License JWT |
```

Replace with:

```
| **`TabSlate-server`** | 公开，AGPL-3.0 | Go 后端，AGPL 开源，用户数无上限，可自托管；禁止未经授权将本后端用于商业收费服务 |
```

- [ ] **Step 2: Commit**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Landing
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md — backend is now AGPL-3.0 open-source"
```

---

## Final Verification

- [ ] **Run full test suite in TabSlate-server**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./... -v 2>&1 | tail -20
```

Expected: `ok` for all packages with tests; no FAIL lines.

- [ ] **Run full build**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./... && go vet ./...
```

Expected: no output (clean).

- [ ] **Verify ALLOW_REGISTRATION=false blocks registration at runtime**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go test ./internal/handler/... -run TestRegister_registrationClosed -v
```

Expected: `--- PASS: TestRegister_registrationClosed`

- [ ] **Verify Landing JSON is valid**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Landing
python3 -c "import json; [json.load(open(f'src/messages/{l}.json')) for l in ['en','zh']]; print('all OK')"
```

Expected: `all OK`
