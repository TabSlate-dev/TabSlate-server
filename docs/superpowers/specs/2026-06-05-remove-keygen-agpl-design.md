# Remove keygen.sh — AGPL Open-Source Edition

**Date:** 2026-06-05
**Branch:** codex/keygen-license → main
**Status:** Approved

## Context

TabSlate-server is being open-sourced under AGPL. The existing keygen.sh integration was used to enforce per-instance user-count limits (Free = 3, Pro = license metadata). Under AGPL, this enforcement has no value — anyone can fork and remove the check. The clean move is to remove keygen.sh entirely and ship an edition that is fully unlimited.

The Cloud edition (`TabSlate-cloud`) is unaffected: it replaces `billing.Provider` with `meteroid.Provider` and never implemented `InstanceLimiter`.

## Goals

- Remove all keygen.sh API calls, machine activation, and license caching.
- User registration becomes unlimited by default.
- New `ALLOW_REGISTRATION` env var (default `true`) lets operators close registration on a self-hosted instance (e.g., after initial setup).
- `GET /api/subscription` always returns `PlanPro` — self-hosters have full feature access.
- No other functionality changes.

## Files Changed

### Deleted

| File | Reason |
|------|--------|
| `billing/local/keygen.go` | keygen.sh HTTP client — no longer needed |
| `billing/local/keygen_test.go` | tests for deleted file |
| `billing/local/license_cache.go` | license caching layer — no longer needed |
| `billing/local/license_cache_test.go` | tests for deleted file |

### Modified

#### `billing/provider.go`
Remove the `InstanceLimiter` interface entirely (OSS no longer enforces user counts; Cloud never used it).

#### `billing/local/provider.go`
- `New(licenseKey string, d *db.DB)` → `New(d *db.DB)` (drop `licenseKey`)
- Remove `licenseCache` field and all methods that reference it: `Start()`, `loadOrCreateFingerprint()`, `enforceUserLimit()`, `CheckRegistrationAllowed()`
- `GetSubscription()` → always return `{Plan: PlanPro, Status: "active"}`
- `GetLimits()` — unchanged (reads `subscription_capacity` table, falls back to `unlimitedLimits()`)
- `OnUserCreated` / `GetCheckoutURL` / `CancelSubscription` / `ListInvoices` — unchanged

#### `billing/local/provider_test.go`
Remove any test cases referencing `licenseCache`, `keygenClient`, `Start()`, `CheckRegistrationAllowed()`, or `enforceUserLimit()`.

#### `app/config.go`
- Remove `KeygenLicenseKey string`
- Add `AllowRegistration bool` — read from `ALLOW_REGISTRATION` env var, default `true`

#### `internal/handler/auth.go`
- Add `registrationOpen bool` field to `AuthHandler`
- Add `registrationOpen bool` parameter to `NewAuthHandler`
- Replace the `InstanceLimiter` type-assertion block with a direct flag check at the top of `Register()`:

```go
if !h.registrationOpen {
    c.JSON(http.StatusForbidden, gin.H{"error": "registration is disabled on this instance"})
    return
}
```

This check happens before any DB access, consistent with the existing captcha check ordering.

#### `app/server.go` (NewAuthHandler call site)
Pass `cfg.AllowRegistration` as the `registrationOpen` argument.

#### `cmd/server/main.go`
- `local.New(cfg.KeygenLicenseKey, database)` → `local.New(database)`
- Remove `bp.Start(ctx)` call

### Documentation / Config files

| File | Change |
|------|--------|
| `CLAUDE.md` | Remove `KEYGEN_LICENSE_KEY` env var row; add `ALLOW_REGISTRATION` row |
| `.env.example` | Same removals/additions |
| `README.md` | Update env var table; update license/billing section |
| `Dockerfile` / CI scripts | Remove any `-ldflags -X '...KeygenAPIURL=...' -X '...KeygenAccountID=...'` |

## Error Message Contract

When `ALLOW_REGISTRATION=false`, `POST /auth/register` returns:

```json
HTTP 403 Forbidden
{ "error": "registration is disabled on this instance" }
```

Same `{"error": "..."}` shape used by all other auth errors — frontend reads `response.data.error` (or equivalent) consistently.

## What Does NOT Change

- `billing/types.go` — `PlanFree/Pro/Enterprise` constants kept (Cloud still uses them)
- `billing/provider.go` — `Provider`, `WebhookHandler`, `UserSyncer` interfaces unchanged
- `server_config` DB table — `license_machine_fingerprint` row may remain in existing DBs; harmless
- All handlers except `auth.go` — no changes
- `InstanceLimiter` removal has zero effect on Cloud (`meteroid.Provider` never implemented it)

## CLAUDE.md Updates

Two `CLAUDE.md` files need updating after the code changes:

**`TabSlate-server/CLAUDE.md`:**
- In the env var table: remove `KEYGEN_LICENSE_KEY` row; add `ALLOW_REGISTRATION` row (`true` / `false`，默认 `true`，`false` 时禁止新用户注册)
- In the `billing/local` package description: remove keygen references, update to reflect unlimited users + registration flag
- In the `注意事项` section: remove any keygen machine fingerprint / activation notes

**`TabSlate-Landing/CLAUDE.md`:** Add a note that the server backend is now AGPL open-source (was previously described as "non-open-source free version").

## Landing Legal Document Updates (`TabSlate-Landing`)

Content lives in `src/messages/en.json` and `src/messages/zh.json` under the `legal` key. The `license` page renders `legal.license.sections.*`; the `terms` page renders `legal.terms.sections.*`.

### `legal.license.sections.intellectual` (both locales)

Update to reflect the backend is now AGPL-3.0 open-source:

> **Open Source Components:** Both the TabSlate Chrome extension and the backend server (`TabSlate-server`) are released under **AGPL-3.0**. Self-hosting is permitted under the terms of that license.

### `legal.license.sections.restrictions` (both locales)

Add one bullet to the existing list:

> * **Using the AGPL-licensed `TabSlate-server` backend to operate a paid commercial synchronization service** (i.e., charging end users for access to a hosted instance) **is strictly prohibited** without a separate commercial license from TabSlate. Personal and organizational self-hosting for non-commercial purposes is permitted under AGPL-3.0.

### `legal.terms.sections.services` (both locales)

Update the "Free Components" bullet to reflect the backend is now AGPL open-source:

> * **Free Components:** The Chrome extension frontend (AGPL-3.0) and the Go backend server (AGPL-3.0, open-source, self-hostable).

### `legal.terms.sections.prohibited` (both locales)

Add one bullet:

> * Using the open-source `TabSlate-server` backend to provide a paid commercial synchronization service to third parties without a separate commercial license from TabSlate.

## Out of Scope

- Schema migration to drop `server_config` rows (harmless to leave)
- Frontend changes (extension already handles 403 from register endpoint)
- Cloud repo changes
