# Health Endpoint Design

**Date:** 2026-06-09
**Repos affected:** `TabSlate-server` (OSS), `TabSlate-Cloud`

## Overview

Add `GET /health/ping` to report real server health. Returns `{"status":"ok"}` when all dependencies are healthy; on failure, returns HTTP 503 with only the failing component names listed.

## Requirements

- Public endpoint, no authentication required (friendly to uptime monitors and K8s probes)
- All healthy → `{"status":"ok"}` + HTTP 200
- Any failure → `{"status":"degraded","failed":["<name>",...]}` + HTTP 503
- Error details not exposed (avoid leaking internal state)
- Optional dependencies (Redis, MeiliSearch) skipped when not configured — never appear in `failed`
- Checks run with a 5-second timeout

## Architecture

### Responsibility split

| Layer | Checks | Notes |
|---|---|---|
| `TabSlate-server` `HealthCheck()` | database, redis, meilisearch | redis/meilisearch skipped if not configured |
| `TabSlate-Cloud` handler | flexprice | always present in Cloud edition |

Route registration is done by Cloud via a new `RegisterRoute` extension point on `Server`, mirroring the existing `RegisterWebhook` pattern. `TabSlate-server` does **not** register `/health/ping` itself.

## Changes

### TabSlate-server (3 additions, no existing code modified)

**`internal/infra/infra.go`** — add `Ping(ctx) error` to `Providers`:
- In-memory mode: return `nil` (Redis not configured, skip)
- Redis mode: call `rdb.Ping(ctx).Err()`

**`internal/search/client.go`** — add nil-safe `Ping(ctx context.Context) error`:
```go
func (c *Client) Ping(ctx context.Context) error {
    if c == nil {
        return nil // MeiliSearch not configured, skip
    }
    _, err := c.svc.HealthWithContext(ctx)
    return err
}
```

**`app/server.go`** — add two methods:
```go
// RegisterRoute registers an arbitrary-method route, for use by the Cloud edition.
func (s *Server) RegisterRoute(method, path string, h gin.HandlerFunc) {
    s.router.Handle(method, path, h)
}

// HealthCheck probes server-side dependencies.
// Returns a map of component name → error for any failing check.
// Optional components (redis, meilisearch) are omitted when not configured.
func (s *Server) HealthCheck(ctx context.Context) map[string]error {
    result := map[string]error{}
    if err := s.db.Ping(ctx); err != nil {
        result["database"] = err
    }
    if err := s.infra.Ping(ctx); err != nil {
        result["redis"] = err
    }
    if err := s.search.Ping(ctx); err != nil {
        result["meilisearch"] = err
    }
    return result
}
```

### TabSlate-Cloud (2 additions)

**`internal/flexprice/provider.go`** — add `Ping(ctx context.Context) error`:
- Calls `POST /plans/search` with `{"limit":1}` using the existing `doPost` helper
- 2xx response → healthy (connectivity + API key valid)
- Non-2xx, network error, or timeout → return error (marks `flexprice` as failed)

**`cmd/server/main.go`** — register the route after `srv` is constructed, before `srv.Run()`:
```go
srv.RegisterRoute("GET", "/health/ping", func(c *gin.Context) {
    ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
    defer cancel()

    var failed []string
    for name, err := range srv.HealthCheck(ctx) {
        if err != nil {
            failed = append(failed, name)
        }
    }
    if err := bp.Ping(ctx); err != nil {
        failed = append(failed, "flexprice")
    }

    if len(failed) == 0 {
        c.JSON(http.StatusOK, gin.H{"status": "ok"})
    } else {
        c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "failed": failed})
    }
})
```

## Response Examples

```json
// All healthy → HTTP 200
{"status": "ok"}

// MeiliSearch offline → HTTP 503
{"status": "degraded", "failed": ["meilisearch"]}

// DB + Flexprice both down → HTTP 503
{"status": "degraded", "failed": ["database", "flexprice"]}
```

## Out of Scope

- Error detail exposure (security: only component names shown)
- Authentication on the endpoint
- `failed` array ordering stability (Go map iteration is random)
- MCP / infra-level health (process metrics, memory, CPU)
