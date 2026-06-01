# keygen.sh License Authorization Design

**Date**: 2026-06-01  
**Scope**: TabSlate-server (OSS self-hosted edition)  
**Status**: Approved

---

## Background

TabSlate-server currently uses a homemade RSA-PS256 JWT (`billing/local/license.go`) for license management. The JWT embeds per-resource limits (`MaxBookmarks`, `MaxWorkspaces`, etc.) but does not enforce a user count limit. This design replaces that system with keygen.sh, adding user count enforcement with server-side revocation.

TabSlate-Cloud (private repo, uses Meteroid billing) is **not affected** by this change.

---

## Requirements

1. License validation powered by keygen.sh
2. keygen.sh endpoint URL configurable via env var — supports both official keygen.sh SaaS and enterprise self-hosted keygen.sh instances
3. TabSlate-Cloud has zero changes — `meteroid.Provider` is unaffected
4. User count = current active verified users (`is_verified = true`)
5. License limits are fully dynamic from keygen.sh metadata; Free tier = 3 users hardcoded (no license key required)
6. Periodic active refresh (background goroutine, default 1h), analogous to Cloud's `capacityStore`
7. When user count exceeds license limit (e.g. after DB-level INSERT), users beyond the limit are automatically suspended on the next license refresh cycle; their sessions are invalidated and login is blocked until the license limit is restored
8. One license key may only be activated on one machine at a time; attempting to start a second instance with the same license key is a fatal startup error

---

## Threat Model

| Threat | Mitigation |
|---|---|
| Honest operator accidentally registers more users than their license allows | `CheckRegistrationAllowed` blocks registration at the application layer |
| DB direct INSERT to bypass application-layer check | Detected on next license refresh (≤1h); excess users suspended; blocked from login. Explicitly violates license terms. |
| Simulating a Cloud instance to skip license check | Impossible — Cloud is a private repo requiring Meteroid API credentials; `billing.Provider` implementation is chosen at compile time, not runtime |
| Recompiling binary to remove license check | Not a realistic threat; TabSlate-server is non-open-source (publicly viewable but not freely modifiable/redistributable) |
| keygen.sh unavailability | Cold start: fallback to Free limits (3 users). Runtime: retain last successful cache value, log warning |
| License expiry or revocation | Downgrade to Free limits (3 users); existing users within Free limit are unaffected; excess users suspended on next refresh |
| Same license key used on a second machine | Startup fails with fatal error: "license already activated on another machine" |

---

## Architecture

### Core Principle

`billing.Provider` interface is **unchanged**. A new optional interface `billing.InstanceLimiter` is added — only `local.Provider` implements it. `meteroid.Provider` (Cloud) does not, so `auth.Register`'s type assertion returns `false` for Cloud instances, skipping the check entirely.

### Package Structure Changes

```
TabSlate-server/
├── billing/
│   ├── provider.go          MODIFIED  — add InstanceLimiter interface
│   └── local/
│       ├── keygen.go        NEW       — keygen.sh HTTP client
│       ├── license_cache.go NEW       — TTL cache + background refresh + enforceUserLimit
│       ├── provider.go      MODIFIED  — replace JWT with keygen cache; implement InstanceLimiter; add Start()
│       └── license.go       DELETED   — JWT parsing logic no longer needed
├── app/
│   └── config.go            MODIFIED  — add KEYGEN_API_URL, KEYGEN_ACCOUNT_ID, KEYGEN_LICENSE_KEY
├── cmd/server/
│   └── main.go              MODIFIED  — pass keygen config, call bp.Start(ctx)
├── internal/handler/
│   └── auth.go              MODIFIED  — Register: add InstanceLimiter check; Login: add suspended check
└── db/
    └── schema.pg.sql        MODIFIED  — users: add suspended_at; new server_config table
```

**TabSlate-Cloud: no changes.**

---

## Data Flow

### Registration

```
POST /auth/register
  │
  ├─ [existing] Captcha verification
  ├─ [existing] Password strength check
  ├─ [existing] Email uniqueness check
  │
  ├─ [NEW] InstanceLimiter type assertion
  │     if il, ok := h.billing.(billing.InstanceLimiter); ok {
  │         il.CheckRegistrationAllowed(ctx)  →  403 "user limit reached"
  │     }
  │     // meteroid.Provider: ok=false → skip (Cloud unaffected)
  │
  └─ [existing] Write users row, send OTP email...
```

`CheckRegistrationAllowed` internals:

```
1. licenseCache.maxUsers()       ← read in-memory cache, no network call
   ├─ no LICENSE_KEY  → maxUsers = 3 (Free hardcoded)
   └─ LICENSE_KEY set → cached keygen.sh value (or fallback)

2. SELECT COUNT(*) FROM users WHERE is_verified = true

3. count >= maxUsers  →  return error
   count <  maxUsers  →  return nil
```

### Machine Activation (Startup, Synchronous)

Before the background goroutine starts, `local.Provider.Start(ctx)` runs machine activation synchronously:

```
1. Load fingerprint from server_config WHERE key = 'license_machine_fingerprint'
   └─ not found → generate UUIDv4, INSERT into server_config

2. ActivateMachine(fingerprint)
   ├─ 201 → first activation, continue
   ├─ 409 → already activated (same machine restart), continue
   └─ 422 → FATAL: log.Fatalf("license already activated on another machine;
                               deactivate it from the keygen.sh dashboard first")
```

This runs only when `KEYGEN_LICENSE_KEY` is set. Free mode skips it entirely.

### License Enforcement (Background Goroutine)

`local.Provider.Start(ctx)` then launches a goroutine that runs on startup and every `syncInterval` (default 1h):

```
1. licenseCache.refresh(ctx)
   └─ FetchLicense + ValidateMachine(fingerprint)
      ├─ both succeed → update cache (maxUsers, status, expiry)
      ├─ machine deactivated → cache status = SUSPENDED (treat as revoked), log warning
      └─ network failure → retain last value, log warning

2. enforceUserLimit(ctx)
   ├─ SELECT id, created_at FROM users
   │    WHERE is_verified = true ORDER BY created_at ASC
   ├─ users[0..maxUsers-1]   → ensure suspended_at = NULL (restore if limit increased)
   └─ users[maxUsers..]      → SET suspended_at = now()
                                DELETE FROM refresh_tokens WHERE user_id = $1
```

Suspended users lose their refresh tokens immediately. Their current access tokens expire naturally (typically within 15 minutes). After that, login is blocked.

### Login Blocking

```go
// auth.Login — after fetching user from DB, before issueTokens:
if user.SuspendedAt != nil {
    c.JSON(http.StatusForbidden, gin.H{
        "error": "account suspended: instance user limit exceeded",
    })
    return
}
```

---

## New Interface: `billing.InstanceLimiter`

```go
// InstanceLimiter is implemented by providers that enforce instance-level user count limits.
// OSS local.Provider implements this; Cloud meteroid.Provider does not.
// auth.Register uses a type assertion to call this — it is NOT part of billing.Provider.
type InstanceLimiter interface {
    CheckRegistrationAllowed(ctx context.Context) error
}
```

---

## keygen.sh Client (`billing/local/keygen.go`)

Single responsibility: keygen.sh REST API calls. Three methods:

### FetchLicense

```
GET /v1/accounts/{accountID}/licenses/{licenseKey}
Authorization: License {licenseKey}

Response parsed fields:
  data.attributes.status          → ACTIVE / EXPIRED / SUSPENDED
  data.attributes.expiry          → RFC3339 timestamp (nullable)
  data.attributes.metadata        → map[string]any
    "max_users"                   → int (0 = not set → treated as Free limit)
```

### ActivateMachine

Called once on startup (idempotent — 409 means already activated on this fingerprint).

```
POST /v1/accounts/{accountID}/machines
Authorization: License {licenseKey}
Body: { "data": { "type": "machines",
                  "attributes": { "fingerprint": "<uuid>", "name": "<hostname>" },
                  "relationships": { "license": { "data": { "type": "licenses",
                                                             "id": "<licenseKey>" } } } } }

201 Created        → first activation, success
409 Conflict       → already activated for this fingerprint → OK (same machine restart)
422 Unprocessable  → machine limit exceeded (another machine holds this license) → fatal
```

### ValidateMachine

Called on each periodic refresh to confirm the machine is still activated.

```
GET /v1/accounts/{accountID}/machines?fingerprint={fingerprint}
Authorization: License {licenseKey}

data[] empty       → machine was deactivated via keygen.sh dashboard → treat as revoked
data[0] present    → still active
```

Error format: `fmt.Errorf("keygen %s: %w", operation, err)`.  
Never log response headers (Authorization header contains license key).

---

## License Cache (`billing/local/license_cache.go`)

```go
type licenseCache struct {
    mu          sync.RWMutex
    data        LicenseData
    fetchedAt   time.Time
    client      *keygenClient  // nil = free tier, no keygen.sh calls
    fingerprint string         // machine UUID loaded from DB at construction
}

type LicenseData struct {
    MaxUsers int
    Status   string     // ACTIVE | EXPIRED | SUSPENDED | "" (free)
    Expiry   *time.Time
}
```

- `maxUsers()`: returns `data.MaxUsers` if status is ACTIVE and not expired; otherwise 3 (Free fallback)
- `refresh(ctx)`: calls `FetchLicense` + `ValidateMachine`; on error retains previous values, logs warning
- `Start(ctx, interval)`: performs initial machine activation + sync (synchronous, fatal on activation failure), then background ticker calling `refresh`

---

## Provider Changes (`billing/local/provider.go`)

```go
type Provider struct {
    db    *db.DB
    cache *licenseCache   // replaces *License
}

func New(keygenURL, accountID, licenseKey string, d *db.DB) (*Provider, error)
func (p *Provider) Start(ctx context.Context)
func (p *Provider) CheckRegistrationAllowed(ctx context.Context) error  // InstanceLimiter
```

`GetLimits` continues to return unlimited (`-1`) for all resource fields — the OSS edition does not restrict per-user resource usage via keygen.sh.

`GetSubscription` derives plan from cache: ACTIVE license → `billing.PlanPro`; otherwise → `billing.PlanFree`.

Compile-time assertion preserved:
```go
var _ billing.Provider = (*Provider)(nil)
var _ billing.InstanceLimiter = (*Provider)(nil)
```

---

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `KEYGEN_API_URL` | No | `https://api.keygen.sh` | keygen.sh base URL; override for self-hosted instances |
| `KEYGEN_ACCOUNT_ID` | When LICENSE_KEY set | — | keygen.sh account ID |
| `KEYGEN_LICENSE_KEY` | No | — | License key; empty = Free mode (3 users max) |

`local.New()` returns an error if `KEYGEN_LICENSE_KEY` is non-empty but `KEYGEN_ACCOUNT_ID` is empty (fast-fail at startup).

Inherited from existing config: `DATABASE_URL`, `JWT_SECRET`, `PORT`, `GIN_MODE`, mail/captcha vars.

---

## Schema Changes

```sql
-- For user suspension enforcement
ALTER TABLE users ADD COLUMN IF NOT EXISTS suspended_at BIGINT;

-- For machine fingerprint persistence across restarts
CREATE TABLE IF NOT EXISTS server_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

Both applied via `db.Migrate()` (idempotent). Cloud's schema includes these after migration runs (Cloud calls the same `db.Migrate`). The `server_config` table is unused by Cloud.

The machine fingerprint is stored as:
```
server_config WHERE key = 'license_machine_fingerprint'  →  value = '<uuidv4>'
```

---

## Fallback Matrix

| Scenario | Behavior |
|---|---|
| No `KEYGEN_LICENSE_KEY` | Free mode — `maxUsers=3`, no keygen.sh calls |
| keygen.sh unreachable at cold start | Fallback to Free limits until first successful refresh |
| keygen.sh refresh fails (runtime) | Retain last cached value, log warning, continue serving |
| License expired or revoked | Downgrade to Free limit (3 users); existing users within limit unaffected |
| DB direct INSERT beyond limit | Detected on next refresh (≤1h); excess users suspended, sessions invalidated |
| License upgraded (maxUsers increases) | Suspended users restored in `created_at ASC` order on next refresh |
| Machine deactivated via keygen.sh dashboard | Periodic refresh detects empty machines list → treat as revoked (Free limits, excess users suspended); re-activation required |

---

## What Does NOT Change

- `billing.Provider` interface (5 methods, same signatures)
- `billing.Limits` struct (no `MaxUsers` field — user count is instance-level, not per-user)
- `subscription_capacity` table seeding (still seeds "unlimited" row for OSS)
- TabSlate-Cloud (`meteroid.Provider`, `cmd/server/main.go` in TabSlate-Cloud repo)
- All resource limit enforcement in handlers (unchanged, still unlimited for OSS)

## Cleanup: Removed Code

The following are deleted as part of this migration:

- `billing/local/license.go` — entire file (JWT parsing: `ParseAndVerify`, `licenseClaims`, `parseRSAPublicKey`, `License` struct)
- `billing/local/provider.go` — `*License` field on `Provider`, `unlimitedLimits()` helper, `seedCapacity()` (replaced by DB seeding in `db.Migrate`)
- `app/config.go` — `LicenseKey string` field
- `go.mod` / `go.sum` — `github.com/golang-jwt/jwt/v5` dependency (if no other consumer remains)
