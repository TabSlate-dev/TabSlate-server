# Account Deletion Feature Design

Date: 2026-06-26  
Status: Approved

## Overview

Allow users to request account deletion from the Settings dialog. A 30-day grace period applies: if the user logs in during that window, deletion is automatically cancelled (by refreshing `last_login_at`, pushing the deadline into the future). Three emails are sent: at request time, 3 days before expiry, and after successful deletion.

## Decision Log

| Question | Decision |
|---|---|
| 30-day countdown basis | C — `max(last_login_at, deletion_requested_at) + 30 days` |
| Email notifications | C — request confirmation + 3-day reminder + deletion confirmation |
| Cancel mechanism | A — login auto-cancels; no explicit cancel button; emails make this clear |
| Button location | B — new "Account" tab in Settings dialog |

---

## Section 1: Database Changes

Three columns added to `users` via `ALTER TABLE … ADD COLUMN IF NOT EXISTS` (idempotent):

```sql
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at              BIGINT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deletion_requested_at      BIGINT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deletion_reminder_sent_at  BIGINT;
```

**`last_login_at`** — updated to current unix timestamp on every successful login. When a user with a pending deletion request logs in, `last_login_at` is refreshed, which pushes `max(last_login_at, deletion_requested_at) + 30 days` into the future, effectively cancelling the deletion without any explicit cancel logic.

**`deletion_requested_at`** — set when the user submits a deletion request; cleared (set NULL) after the account is deleted. Not cleared on login — the CleanupHandler uses the `max(...)` formula to determine whether deletion is still due.

**`deletion_reminder_sent_at`** — set after the 3-day reminder email is sent, preventing duplicate sends on subsequent CleanupHandler runs.

### Frontend visibility

`GET /auth/me` gains a derived field `deletion_scheduled_at` (unix timestamp or null), computed server-side as:

```
if deletion_requested_at IS NOT NULL:
    deletion_scheduled_at = max(last_login_at, deletion_requested_at) + 30 days
else:
    deletion_scheduled_at = null
```

This lets the frontend display "deletion pending — executes at \<date\>" without duplicating the logic.

---

## Section 2: Backend API & Business Logic

### New endpoint

**`POST /auth/delete-account`** (🔒 JWT required)

Request body:
```json
{ "password": "current password" }
```

Behaviour:
1. Verify password against `password_hash` using `bcrypt.CompareHashAndPassword`.
2. If wrong password → 401 `"invalid password"`.
3. If `deletion_requested_at` already set and deletion still pending → 409, return existing `scheduled_at` (idempotent).
4. Set `deletion_requested_at = now`, clear `deletion_reminder_sent_at = NULL` (so a fresh reminder will be sent for the new request cycle).
5. Send `deletion_requested` email asynchronously.
6. Respond 200: `{ "scheduled_at": <unix>, "executes_at": <unix> }`.

Rate-limited under the existing auth rate limiter.

### Login update

`AuthHandler.Login` success path gains one additional `UPDATE`:

```go
h.db.Exec(ctx,
    `UPDATE users SET last_login_at = $1, updated_at = $1 WHERE id = $2`,
    now, user.ID,
)
```

No explicit clearing of `deletion_requested_at` — the CleanupHandler's `max(...)` formula handles cancellation implicitly.

### `GET /auth/me` response extension

The handler computes and appends `deletion_scheduled_at` to the user object in the JSON response. Null when no deletion is pending.

### CleanupHandler extension (Phase 3 & Phase 4)

Both phases run inside `runOnce()`, executed daily (unchanged interval).

**Phase 3 — Send 3-day reminder:**

```sql
SELECT id, name, email
FROM users
WHERE deletion_requested_at IS NOT NULL
  AND GREATEST(last_login_at, deletion_requested_at) + (30 * 86400)  >  $now      -- not yet expired
  AND GREATEST(last_login_at, deletion_requested_at) + (30 * 86400)  <= $now + (3 * 86400)  -- within 3-day window
  AND deletion_reminder_sent_at IS NULL
```

For each match: send `deletion_reminder` email (async), set `deletion_reminder_sent_at = now`.

If `last_login_at` has moved since the reminder was sent (user logged in after reminder), the deletion deadline is > 3 days away again — Phase 4 will not fire, and `deletion_reminder_sent_at` stays set (no second reminder for the same request cycle). If the user requests deletion again after cancelling, `deletion_requested_at` is refreshed and `deletion_reminder_sent_at` is cleared.

**Phase 4 — Execute deletion:**

```sql
SELECT id, name, email
FROM users
WHERE deletion_requested_at IS NOT NULL
  AND GREATEST(COALESCE(last_login_at, 0), deletion_requested_at) + (30 * 86400) <= $now
```

For each match (expected to be rare — typically 0–1 rows per day):
1. Send `deletion_executed` email **before** deleting (to ensure the email address is still available).
2. `DELETE FROM users WHERE id = $1` — cascades to all child tables (bookmarks, collections, tags, workspaces, groups, refresh_tokens, subscriptions, user_sync_seq).
3. Call `billing.OnUserDeleted(ctx, userID)` if provider implements `billing.UserDeleter`.
4. Delete MeiliSearch documents for the user (if search client configured): `search.DeleteUserDocuments(ctx, userID)`.

### billing.UserDeleter optional interface

```go
// UserDeleter is implemented by billing providers that need to clean up
// external records when an account is permanently deleted.
type UserDeleter interface {
    OnUserDeleted(ctx context.Context, userID string) error
}
```

OSS `local.Provider` does not implement this interface. Cloud `flexprice.Provider` can implement it to cancel subscriptions and remove the customer record from Flexprice.

---

## Section 3: Email Templates

### New mailer method

```go
type AccountDeletionEmailData struct {
    ExecutesAt time.Time // used by deletion_requested and deletion_reminder
}

func (m *Mailer) SendAccountDeletion(
    ctx context.Context,
    to, name, purpose, lang string,
    data AccountDeletionEmailData,
) error
```

`purpose` values: `"deletion_requested"`, `"deletion_reminder"`, `"deletion_executed"`.

### New template file

`internal/mailer/templates/account_deletion.html` — embedded via `embed.FS`, rendered with `html/template`. Uses the same structure as `otp.html`.

### Email content

| Purpose | Subject (en) | Subject (zh) | Key message |
|---|---|---|---|
| `deletion_requested` | Your TabSlate account deletion is scheduled | 您的 TabSlate 账号注销申请已受理 | Grace period starts; login at any time to cancel; executes at `ExecutesAt` |
| `deletion_reminder` | Your TabSlate account will be deleted in 3 days | 您的 TabSlate 账号将在 3 天后注销 | 3 days remaining; **login to cancel**; executes at `ExecutesAt` |
| `deletion_executed` | Your TabSlate account has been deleted | 您的 TabSlate 账号已注销 | All data permanently deleted; thank you for using TabSlate |

The "login to cancel" call-to-action must appear prominently in both `deletion_requested` and `deletion_reminder` emails, since there is no cancel button in the UI.

---

## Section 4: Frontend Changes

### Settings Dialog — Account tab

`settings-dialog.tsx`:
- Extend tab union type: `"general" | "engines" | "plan" | "account"`
- Add fourth tab button: "Account" (or localised equivalent)

Account tab layout:

**Account Info block (read-only):**
- Email address (from user store)
- Member since date (formatted from `created_at`)

**Danger Zone block** (red-border card, styled with `border-destructive/30 bg-destructive/5`):

*When no deletion pending (`deletion_scheduled_at` is null):*
- Title: "Delete Account"
- Description: data will be permanently deleted after a 30-day grace period; logging in will cancel the request
- "Delete Account" button (destructive variant) → opens `DeleteAccountDialog`

*When deletion pending (`deletion_scheduled_at` is set):*
- Title: "Account Deletion Scheduled"
- Description: "Your account is scheduled for deletion on \<date\>. Log in before that date to cancel."
- No cancel button

### DeleteAccountDialog component

New component `DeleteAccountDialog` (co-located in `settings-dialog.tsx` or extracted to `delete-account-dialog.tsx`):
- Modal dialog with title "Confirm Account Deletion"
- Explanation text
- Password input (type="password", required)
- Cancel + "Delete Account" buttons (destructive)
- Submit flow:
  1. Call `POST /auth/delete-account` with `{ password }`
  2. Show loading spinner on button
  3. On success: close dialog, refetch `/auth/me` to update `deletion_scheduled_at`
  4. On 401: show "incorrect password" inline error
  5. On 409: show "deletion already scheduled" inline message

### API client

New function in the auth API module:

```typescript
async function requestAccountDeletion(password: string): Promise<{
  scheduled_at: number;
  executes_at: number;
}>
```

### Auth store / user model

Extend the `User` type with `deletion_scheduled_at?: number | null`. The Account tab reads this field directly from the user store after `fetchMe()`.

---

---

## Section 5: Legal Document Updates (Post-Implementation)

After the feature is shipped, update `TabSlate-Landing/src/messages/en.json` (and the `zh.json` counterpart) in three places:

### Privacy Policy — Data Storage / Data Retention

Current text instructs users to email `privacy@cs.tabslate.com` for deletion. Replace with a description of the self-service flow:

> You can permanently delete your account directly from the TabSlate extension: open **Settings → Account → Delete Account**, confirm with your password, and a 30-day grace period begins. During this window you may cancel the deletion at any time simply by logging in again — no action required beyond signing in. After 30 days without a login, your account and all associated cloud data (synced tabs, bookmarks, workspaces, collections, tags, and account information) are permanently purged from our databases. Three email notifications are sent: one immediately upon request, one reminder 3 days before the deadline, and one confirmation after deletion is complete.

### Privacy Policy — Your Rights (Right to Deletion)

Same update as above — replace the manual email flow with the self-service account deletion description. Keep the `privacy@cs.tabslate.com` contact for data export requests and edge cases (e.g. users who cannot log in).

### Terms of Service — Account Closure

Current text implies immediate purge on closure. Update to reflect the 30-day grace period:

> You may close your account at any time via **Settings → Account → Delete Account** in the extension. Upon submitting a deletion request, a 30-day grace period begins during which all your data remains intact. Logging in at any point during this window cancels the request. After 30 days without a login, all associated bookmarks, workspaces, tab groups, and account information stored on our synchronization servers will be permanently and irreversibly purged.

---

## Scope Boundaries

- MeiliSearch cleanup on deletion: Phase 4 calls `search.DeleteUserDocuments(userID)` if the search client is configured. This needs a new method on `search.Client`.
- No "pause" or "extend grace period" functionality.
- No admin-triggered deletion endpoint in this spec.
- Cloud `billing.UserDeleter` implementation is out of scope for this spec (stub interface only).
