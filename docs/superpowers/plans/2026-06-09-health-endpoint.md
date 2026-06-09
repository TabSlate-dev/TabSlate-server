# Health Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /health/ping` that checks DB, Redis, MeiliSearch (server-side) and Flexprice API (Cloud-side), returning `{"status":"ok"}` or `{"status":"degraded","failed":[...]}`.

**Architecture:** TabSlate-server exposes `HealthCheck(ctx)` (returns failing deps) and `RegisterRoute()` (generic route injection). TabSlate-Cloud adds `Provider.Ping(ctx)` for Flexprice, then wires the combined handler via `RegisterRoute`. Route is public, 5-second timeout, only failing component names shown.

**Tech Stack:** Go stdlib, pgxpool Ping, go-redis Ping, meilisearch-go `HealthWithContext`, existing `doPost` helper in flexprice package.

---

## File Map

| File | Repo | Change |
|---|---|---|
| `internal/infra/infra.go` | TabSlate-server | Add `rdb` field to `Providers`; add `Ping(ctx)` |
| `internal/infra/infra_test.go` | TabSlate-server | New — test `Ping` in in-memory mode |
| `internal/search/client.go` | TabSlate-server | Add nil-safe `Ping(ctx)` |
| `internal/search/search_test.go` | TabSlate-server | New — test `Ping` nil and non-nil cases |
| `app/server.go` | TabSlate-server | Add `RegisterRoute()` and `HealthCheck()` |
| `internal/flexprice/provider.go` | TabSlate-Cloud | Add `Ping(ctx)` |
| `internal/flexprice/provider_test.go` | TabSlate-Cloud | Add `TestPing_*` cases |
| `cmd/server/main.go` | TabSlate-Cloud | Register `GET /health/ping` handler |

**Local dev prerequisite:** Both repos must be linked via `go.work`. From the parent directory:
```bash
go work init ./TabSlate-Cloud ./TabSlate-server
```
This makes changes to TabSlate-server immediately visible in TabSlate-Cloud without tagging a release.

---

## Task 1: `infra.Providers.Ping` (TabSlate-server)

**Files:**
- Modify: `internal/infra/infra.go`
- Create: `internal/infra/infra_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/infra/infra_test.go`:

```go
package infra

import (
	"context"
	"testing"
)

func TestProviders_Ping_InMemory(t *testing.T) {
	p, cleanup, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	if err := p.Ping(context.Background()); err != nil {
		t.Errorf("in-memory Ping: expected nil, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd TabSlate-server && go test ./internal/infra/... -run TestProviders_Ping_InMemory -v
```
Expected: FAIL — `p.Ping undefined`

- [ ] **Step 3: Implement `Ping`**

In `internal/infra/infra.go`, add `rdb *redis.Client` to `Providers` and `Ping` method. Final file:

```go
package infra

import (
	"context"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/ratelimit"
	"github.com/TabSlate-dev/TabSlate-server/internal/store"
)

// Providers holds the three infrastructure providers wired by New.
type Providers struct {
	Hub     pubsub.Hub
	Cache   store.Cache
	Limiter ratelimit.Limiter
	rdb     *redis.Client // nil in in-memory mode; used for Ping
}

// Ping checks Redis connectivity. Returns nil when Redis is not configured (in-memory mode).
func (p *Providers) Ping(ctx context.Context) error {
	if p.rdb == nil {
		return nil
	}
	if err := p.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// New creates Providers from redisURL.
// If redisURL is empty all providers use in-memory implementations (OSS mode).
// The returned cleanup function must be called on process shutdown.
func New(redisURL string) (*Providers, func(), error) {
	if redisURL == "" {
		log.Println("infra: using in-memory providers (OSS mode)")
		hub := pubsub.NewInMemoryHub()
		cache := store.NewInMemoryCache()
		cleanup := func() { hub.Close(); cache.Close() }
		return &Providers{
			Hub:     hub,
			Cache:   cache,
			Limiter: ratelimit.NewInMemoryLimiter(),
		}, cleanup, nil
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	log.Printf("infra: using Redis providers (%s)", opt.Addr)
	rdb := redis.NewClient(opt)
	success := false
	defer func() {
		if !success {
			rdb.Close()
		}
	}()
	hub := pubsub.NewRedisHub(rdb)
	p := &Providers{
		Hub:     hub,
		Cache:   store.NewRedisCache(rdb),
		Limiter: ratelimit.NewRedisLimiter(rdb),
		rdb:     rdb,
	}
	success = true
	return p, func() { hub.Close(); rdb.Close() }, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/infra/... -run TestProviders_Ping_InMemory -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd TabSlate-server
git add internal/infra/infra.go internal/infra/infra_test.go
git commit -m "feat(infra): add Ping method to Providers for health checks"
```

---

## Task 2: `search.Client.Ping` (TabSlate-server)

**Files:**
- Modify: `internal/search/client.go`
- Create: `internal/search/search_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/search/search_test.go`:

```go
package search

import (
	"context"
	"errors"
	"testing"

	meilisearch "github.com/meilisearch/meilisearch-go"
)

// fakeHealthySvc implements only HealthWithContext; embedding the interface
// provides nil stubs for all other methods (never called in Ping).
type fakeHealthySvc struct{ meilisearch.ServiceManager }

func (fakeHealthySvc) HealthWithContext(_ context.Context) (*meilisearch.Health, error) {
	return &meilisearch.Health{Status: "available"}, nil
}

type fakeUnhealthySvc struct{ meilisearch.ServiceManager }

func (fakeUnhealthySvc) HealthWithContext(_ context.Context) (*meilisearch.Health, error) {
	return nil, errors.New("meilisearch down")
}

func TestPing_NilClient(t *testing.T) {
	var c *Client
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("nil client Ping: expected nil, got %v", err)
	}
}

func TestPing_Healthy(t *testing.T) {
	c := &Client{svc: fakeHealthySvc{}}
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("healthy Ping: expected nil, got %v", err)
	}
}

func TestPing_Unhealthy(t *testing.T) {
	c := &Client{svc: fakeUnhealthySvc{}}
	if err := c.Ping(context.Background()); err == nil {
		t.Error("unhealthy Ping: expected error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/search/... -run TestPing -v
```
Expected: FAIL — `c.Ping undefined`

- [ ] **Step 3: Add `Ping` to `internal/search/client.go`**

Add after the `SearchBookmarks` method:

```go
// Ping checks MeiliSearch connectivity. Returns nil when client is nil (not configured).
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if _, err := c.svc.HealthWithContext(ctx); err != nil {
		return fmt.Errorf("meilisearch ping: %w", err)
	}
	return nil
}
```

Also add `"context"` and `"fmt"` to the import block in `client.go` if not already present.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/search/... -run TestPing -v
```
Expected: all three PASS

- [ ] **Step 5: Commit**

```bash
git add internal/search/client.go internal/search/search_test.go
git commit -m "feat(search): add nil-safe Ping method for health checks"
```

---

## Task 3: `Server.HealthCheck` and `RegisterRoute` (TabSlate-server)

**Files:**
- Modify: `app/server.go`

These are simple additions. `HealthCheck` calls the two `Ping` methods written in Tasks 1–2. `RegisterRoute` is a one-liner. Both are verified end-to-end in Task 6.

- [ ] **Step 1: Add `context` to `app/server.go` imports if missing**

Check the existing import block — `"context"` should already be present (used by `New` and `SyncSubscription`). No change needed if it is.

- [ ] **Step 2: Add `RegisterRoute` and `HealthCheck` to `app/server.go`**

Add after the existing `RegisterWebhook` method:

```go
// RegisterRoute registers a handler for any HTTP method. Used by the Cloud edition
// to inject edition-specific routes (e.g. GET /health/ping) after construction.
func (s *Server) RegisterRoute(method, path string, h gin.HandlerFunc) {
	s.router.Handle(method, path, h)
}

// HealthCheck probes server-owned dependencies and returns a map of
// component name → error for every failing check.
// Redis is omitted when not configured (in-memory mode); MeiliSearch is
// omitted when not configured (s.search == nil).
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

- [ ] **Step 3: Verify the build**

```bash
go build ./...
```
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add app/server.go
git commit -m "feat(app): add RegisterRoute and HealthCheck to Server"
```

---

## Task 4: `flexprice.Provider.Ping` (TabSlate-Cloud)

**Files:**
- Modify: `internal/flexprice/provider.go`
- Modify: `internal/flexprice/provider_test.go`

- [ ] **Step 1: Write the failing tests**

Add to the end of `internal/flexprice/provider_test.go`:

```go
// ── Ping ─────────────────────────────────────────────────────────────────────

func TestPing_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/plans/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"items":[]}`)
	})
	p, srv := newTestProvider(t, mux)
	defer srv.Close()

	if err := p.Ping(context.Background()); err != nil {
		t.Errorf("Ping success: expected nil, got %v", err)
	}
}

func TestPing_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/plans/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `internal error`)
	})
	p, srv := newTestProvider(t, mux)
	defer srv.Close()

	if err := p.Ping(context.Background()); err == nil {
		t.Error("Ping server error: expected error, got nil")
	}
}

func TestPing_NetworkError(t *testing.T) {
	mux := http.NewServeMux()
	p, srv := newTestProvider(t, mux)
	srv.Close() // close before calling Ping to force network error

	if err := p.Ping(context.Background()); err == nil {
		t.Error("Ping network error: expected error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd TabSlate-Cloud && go test ./internal/flexprice/... -run TestPing -v
```
Expected: FAIL — `p.Ping undefined`

- [ ] **Step 3: Add `Ping` to `internal/flexprice/provider.go`**

Add after the `HasWebhookSecret` method (or any logical location at the end of provider-level methods):

```go
// Ping verifies Flexprice API connectivity and API key validity by calling
// POST /plans/search with limit 1. Returns nil on 2xx; error on any failure.
func (p *Provider) Ping(ctx context.Context) error {
	type req struct {
		Limit int `json:"limit"`
	}
	if _, err := doPost[struct{}](ctx, p.client, "/plans/search", req{Limit: 1}); err != nil {
		return fmt.Errorf("flexprice ping: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/flexprice/... -run TestPing -v
```
Expected: all three PASS

- [ ] **Step 5: Run the full flexprice test suite to catch regressions**

```bash
go test ./internal/flexprice/... -v
```
Expected: all existing tests still PASS

- [ ] **Step 6: Commit**

```bash
cd TabSlate-Cloud
git add internal/flexprice/provider.go internal/flexprice/provider_test.go
git commit -m "feat(flexprice): add Ping method for health endpoint"
```

---

## Task 5: Register `GET /health/ping` in `main.go` (TabSlate-Cloud)

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add the health handler registration**

In `cmd/server/main.go`, add after the `srv.RegisterWebhook(...)` block and before `srv.Run()`:

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

- [ ] **Step 2: Add missing imports to `main.go`**

The handler uses `*gin.Context` and `http.Status*` constants. Both packages must be imported directly. Update the import block in `cmd/server/main.go`:

```go
import (
    "context"
    "log"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/joho/godotenv"
    "github.com/TabSlate-dev/TabSlate-Cloud/internal/flexprice"
    tabapp "github.com/TabSlate-dev/TabSlate-server/app"
    "github.com/TabSlate-dev/TabSlate-server/db"
)
```

`gin` is an indirect dep via TabSlate-server; add it as a direct dep:

```bash
go get github.com/gin-gonic/gin
go mod tidy
```

- [ ] **Step 3: Full build verification**

```bash
go build ./...
```
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go go.mod go.sum
git commit -m "feat(server): register GET /health/ping endpoint"
```

---

## Task 6: End-to-End Verification

- [ ] **Step 1: Start the server locally**

Ensure `.env` is populated (at minimum `DATABASE_URL`, `FLEXPRICE_API_KEY`, `FLEXPRICE_PLAN_LOOKUP_KEY_FREE`):

```bash
go run ./cmd/server
```
Expected: server starts and logs `TabSlate server listening on :PORT`

- [ ] **Step 2: Check healthy state**

```bash
curl -s http://localhost:8080/health/ping | jq .
```
Expected:
```json
{"status": "ok"}
```
HTTP status: `200 OK`

- [ ] **Step 3: Verify 503 on dependency failure (optional — dev only)**

Stop Postgres locally and re-run:

```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/health/ping
```
Expected: `503`

```bash
curl -s http://localhost:8080/health/ping | jq .
```
Expected:
```json
{"status": "degraded", "failed": ["database"]}
```

- [ ] **Step 4: Run all tests in both repos**

```bash
cd TabSlate-server && go test ./...
cd TabSlate-Cloud   && go test ./...
```
Expected: all PASS

- [ ] **Step 5: Final commit if any loose changes remain**

```bash
git status
# commit anything uncommitted
```
