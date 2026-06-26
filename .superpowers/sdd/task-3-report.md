# Task 3 Report

## What I implemented

- Added `model.User.DeletionScheduledAt *int64` with JSON field `deletion_scheduled_at`.
- Added `model.DeleteAccountRequest` with required `password`.
- Updated `AuthHandler.Login` to write `last_login_at` and `updated_at` on successful login.
- Updated `AuthHandler.Me` to load `deletion_requested_at` and `last_login_at`, then compute `deletion_scheduled_at` as `max(deletion_requested_at, last_login_at) + 30 days` when deletion is pending.
- Implemented `AuthHandler.DeleteAccount`:
  - validates JSON body
  - loads the authenticated user with deletion state
  - verifies password with `auth.CheckPassword`
  - returns `409` with schedule info when deletion is already pending and not yet due
  - stores `deletion_requested_at`, clears `deletion_reminder_sent_at`, updates `updated_at`
  - sends the deletion-requested email asynchronously
  - returns `scheduled_at` and `executes_at`
- Registered `POST /auth/delete-account` under the protected routes with the auth rate limiter.

## Tests and results

- Focused RED test:
  - `go test ./internal/handler/... -run TestDeleteAccount -v`
  - Result: failed before implementation because `DeleteAccount` did not exist.
- Focused GREEN tests:
  - `go test ./internal/handler/... -run TestDeleteAccount -v`
  - Result: PASS
  - `go test ./internal/handler/... -run TestParseLang -v`
  - Result: PASS
- Additional focused suite:
  - `go test ./internal/handler/...`
  - Result: PASS
- Required repo checks:
  - `go build ./...`
  - Result: PASS
  - `go vet ./...`
  - Result: PASS

## TDD evidence

### RED

Command:

```bash
go test ./internal/handler/... -run TestDeleteAccount -v
```

Output:

```text
# github.com/TabSlate-dev/TabSlate-server/internal/handler [github.com/TabSlate-dev/TabSlate-server/internal/handler.test]
internal/handler/auth_test.go:68:4: h.DeleteAccount undefined (type *AuthHandler has no field or method DeleteAccount)
```

### GREEN

Command:

```bash
go test ./internal/handler/... -run TestDeleteAccount -v
```

Output:

```text
=== RUN   TestDeleteAccount_MissingPassword
--- PASS: TestDeleteAccount_MissingPassword (0.00s)
PASS
ok  	github.com/TabSlate-dev/TabSlate-server/internal/handler	0.834s
```

Command:

```bash
go test ./internal/handler/... -run TestParseLang -v
```

Output:

```text
=== RUN   TestParseLang
=== RUN   TestParseLang/zh-CN
=== RUN   TestParseLang/zh
=== RUN   TestParseLang/zh-TW
=== RUN   TestParseLang/en-US
=== RUN   TestParseLang/en
=== RUN   TestParseLang/fr-FR
=== RUN   TestParseLang/empty
--- PASS: TestParseLang (0.00s)
    --- PASS: TestParseLang/zh-CN (0.00s)
    --- PASS: TestParseLang/zh (0.00s)
    --- PASS: TestParseLang/zh-TW (0.00s)
    --- PASS: TestParseLang/en-US (0.00s)
    --- PASS: TestParseLang/en (0.00s)
    --- PASS: TestParseLang/fr-FR (0.00s)
    --- PASS: TestParseLang/empty (0.00s)
PASS
ok  	github.com/TabSlate-dev/TabSlate-server/internal/handler	0.469s
```

## Files changed

- `internal/model/model.go`
- `internal/handler/auth.go`
- `internal/handler/auth_test.go`
- `app/server.go`

## Self-review findings

- The implementation stays within the four task-owned files.
- The new route is protected and rate-limited as requested.
- `Me` exposes only the derived `deletion_scheduled_at`, not the raw DB timestamps.
- `DeleteAccount` is idempotent for an already-pending deletion and returns the requested conflict payload shape.
- I adapted the new SQL to the repository’s actual DB API. The task brief requested `h.db.Rebind(...)`, but this checkout does not expose that method on `*db.DB`, so the implemented queries use the existing PostgreSQL `$1` style already used throughout this file.

## Issues or concerns

- No functional blockers found.
- Minor concern: the task brief said to use `h.db.Rebind(...)` for new SQL, but this repository state does not provide that method, so I used the live codebase’s existing parameter style to keep the build green.
