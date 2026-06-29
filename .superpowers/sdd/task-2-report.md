# Task 2 Report: Account deletion email template and mailer method

## What I implemented

- Added `AccountDeletionEmailData` to `internal/mailer/mailer.go` with `ExecutesAt time.Time`.
- Added account deletion translation data for:
  - `deletion_requested`
  - `deletion_reminder`
  - `deletion_executed`
- Added `deletionTmpl *template.Template` to `Mailer` and parsed `templates/account_deletion.html` in `New`.
- Added `(*Mailer).SendAccountDeletion(ctx, to, name, purpose, lang string, data AccountDeletionEmailData) error`.
- Added `renderAccountDeletion(name, purpose, lang string, data AccountDeletionEmailData) (string, string, error)`.
- Created `internal/mailer/templates/account_deletion.html` using the exact HTML from the task brief and matching the existing OTP mail style.
- Added the requested render tests for requested, reminder, executed, and unknown-purpose flows.

## TDD evidence

### RED

Command:

```bash
go test ./internal/mailer/...
```

Output:

```text
# github.com/TabSlate-dev/TabSlate-server/internal/mailer [github.com/TabSlate-dev/TabSlate-server/internal/mailer.test]
internal/mailer/mailer_test.go:199:26: m.renderAccountDeletion undefined (type *Mailer has no field or method renderAccountDeletion)
internal/mailer/mailer_test.go:199:85: undefined: AccountDeletionEmailData
internal/mailer/mailer_test.go:217:20: m.renderAccountDeletion undefined (type *Mailer has no field or method renderAccountDeletion)
internal/mailer/mailer_test.go:217:79: undefined: AccountDeletionEmailData
internal/mailer/mailer_test.go:230:20: m.renderAccountDeletion undefined (type *Mailer has no field or method renderAccountDeletion)
internal/mailer/mailer_test.go:230:76: undefined: AccountDeletionEmailData
internal/mailer/mailer_test.go:241:17: m.renderAccountDeletion undefined (type *Mailer has no field or method renderAccountDeletion)
internal/mailer/mailer_test.go:241:71: undefined: AccountDeletionEmailData
FAIL	github.com/TabSlate-dev/TabSlate-server/internal/mailer [build failed]
FAIL
```

Result:
- Confirmed the new tests failed for the expected reason: the account deletion mailer API and render path did not exist yet.

### GREEN

Command:

```bash
go test ./internal/mailer/...
```

Output:

```text
ok  	github.com/TabSlate-dev/TabSlate-server/internal/mailer	0.420s
```

Result:
- The new account deletion tests and existing OTP mailer tests passed together.

## Tests and results

- `go test ./internal/mailer/...`
  - PASS
- `go build ./...`
  - PASS
- `go vet ./...`
  - PASS

## Files changed

- `internal/mailer/mailer.go`
- `internal/mailer/templates/account_deletion.html`
- `internal/mailer/mailer_test.go`

## Self-review findings

- Scope stayed within the three files assigned to Task 2.
- Reused the existing `legalLinks` map exactly as required.
- Kept the implementation minimal: no extra abstractions, no unrelated refactors.
- The date formatting matches the brief exactly via `January 2, 2006`.
- The `deletion_executed` path does not interpolate a date and is covered by test.

## Issues or concerns

- No functional concerns from this task.
- During GREEN verification, one test message string originally triggered Go's format-string check because it contained `%s` literally. I adjusted only that assertion message; no production behavior changed.
