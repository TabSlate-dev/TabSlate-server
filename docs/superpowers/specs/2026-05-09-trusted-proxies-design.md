# TRUSTED_PROXIES Configuration — Design Spec

**Date:** 2026-05-09
**Status:** Approved

## Problem

`c.ClientIP()` in Gin resolves IP via `X-Forwarded-For` → `X-Real-IP` → `RemoteAddr`.
Without `SetTrustedProxies`, Gin defaults to trusting all proxies, making the header
trivially spoofable by any client. With `SetTrustedProxies(nil)`, the real client IP
is lost in Docker + Traefik deployments because `RemoteAddr` is Traefik's internal IP.

All rate limiting and captcha thresholds key on client IP, so both extremes are broken.

## Solution

Add a `TRUSTED_PROXIES` environment variable. The server calls
`router.SetTrustedProxies(cfg.TrustedProxies)` at startup so Gin only honours
`X-Forwarded-For` when the TCP peer is a known proxy.

## Behaviour by Env State

| `TRUSTED_PROXIES` env | `cfg.TrustedProxies` | Gin behaviour |
|---|---|---|
| Not set | `["172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"]` | Trust all RFC1918 ranges (default Docker networks) |
| Set to empty string (`TRUSTED_PROXIES=`) | `nil` | Trust only `RemoteAddr`; ignore all forwarded headers |
| Set to explicit list (`10.0.0.1,172.18.0.0/16`) | Parsed slice | Trust only those IPs/CIDRs |

`os.LookupEnv` is used to distinguish "not set" from "set to empty", because the two
cases have different semantics.

## Files Changed

### `app/config.go`

- Add `TrustedProxies []string` to `Config` struct, with a comment matching the
  env var table in CLAUDE.md.
- Add `envStringSlice(key string, defaultVal []string) []string` helper:
  - `os.LookupEnv` returns `(_, false)` → return `defaultVal`
  - value is empty string → return `nil`
  - otherwise → `strings.Split(value, ",")` with per-element `strings.TrimSpace`
- Wire in `LoadConfig`: `TrustedProxies: envStringSlice("TRUSTED_PROXIES", []string{"172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"})`

### `app/server.go`

- After `gin.Default()` in `New()`, add one line:
  `s.router.SetTrustedProxies(cfg.TrustedProxies)`

### `.env.example`

Add under the `── Server ──` section:

```
# Trusted reverse-proxy IPs or CIDRs, comma-separated.
# Defaults to RFC1918 private ranges (covers Docker + Traefik out of the box).
# Set to empty string to trust only RemoteAddr (use when directly internet-exposed).
# TRUSTED_PROXIES=172.16.0.0/12,10.0.0.0/8,192.168.0.0/16
```

## Out of Scope

- CLAUDE.md env var table update is part of implementation (not a separate concern).
- No changes to rate limiter logic, keys, or thresholds.
- No frontend changes.
