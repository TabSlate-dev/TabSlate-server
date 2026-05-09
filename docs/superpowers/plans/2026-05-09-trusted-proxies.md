# TRUSTED_PROXIES Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `TRUSTED_PROXIES` env var so Gin's `c.ClientIP()` returns the real client IP behind reverse proxies like Traefik, making all rate limiting and captcha IP counters correct.

**Architecture:** Parse `TRUSTED_PROXIES` into `Config.TrustedProxies []string` at startup, then call `router.SetTrustedProxies()` before routes are registered. An `envStringSlice` helper distinguishes "not set" (use RFC1918 defaults) from "set to empty" (trust only RemoteAddr).

**Tech Stack:** Go 1.21+, Gin v1, `os.LookupEnv`, `strings.Split`

---

## Files

| Action | Path | What changes |
|---|---|---|
| Modify | `app/config.go` | Add `TrustedProxies []string` field, `envStringSlice` helper, wire in `LoadConfig` |
| Create | `app/config_test.go` | Unit tests for `envStringSlice` |
| Modify | `app/server.go` | Extract router, call `SetTrustedProxies` before struct literal |
| Modify | `.env.example` | Add `TRUSTED_PROXIES` entry under `── Server ──` |
| Modify | `CLAUDE.md` (server repo root) | Add `TRUSTED_PROXIES` row to env var table |

---

## Task 1: Test and implement `envStringSlice` helper

**Files:**
- Create: `app/config_test.go`
- Modify: `app/config.go`

- [ ] **Step 1: Create `app/config_test.go` with failing tests**

```go
package app

import (
	"testing"
)

func TestEnvStringSlice_NotSet(t *testing.T) {
	// Use a key guaranteed to be absent from the environment.
	result := envStringSlice("TEST_PROXIES_UNSET_XYZ", []string{"default"})
	if len(result) != 1 || result[0] != "default" {
		t.Fatalf("expected [default], got %v", result)
	}
}

func TestEnvStringSlice_EmptyString(t *testing.T) {
	t.Setenv("TEST_PROXIES_EMPTY", "")
	result := envStringSlice("TEST_PROXIES_EMPTY", []string{"default"})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestEnvStringSlice_Values(t *testing.T) {
	t.Setenv("TEST_PROXIES_VALS", "172.16.0.0/12, 10.0.0.0/8 , 192.168.0.0/16")
	result := envStringSlice("TEST_PROXIES_VALS", nil)
	want := []string{"172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"}
	if len(result) != len(want) {
		t.Fatalf("expected %v, got %v", want, result)
	}
	for i, v := range want {
		if result[i] != v {
			t.Fatalf("index %d: expected %q, got %q", i, v, result[i])
		}
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /path/to/TabSlate-server
go test ./app/ -run TestEnvStringSlice -v
```

Expected: `FAIL — envStringSlice undefined`

- [ ] **Step 3: Add `strings` import and `envStringSlice` to `app/config.go`**

In the `import` block, add `"strings"`:

```go
import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)
```

Append `envStringSlice` after the existing `envDuration` helper at the bottom of the file:

```go
// envStringSlice reads a comma-separated list of strings from the environment.
// If the variable is not set, defaultVal is returned.
// If the variable is set to an empty string, nil is returned (trust only RemoteAddr).
func envStringSlice(key string, defaultVal []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./app/ -run TestEnvStringSlice -v
```

Expected:
```
--- PASS: TestEnvStringSlice_NotSet
--- PASS: TestEnvStringSlice_EmptyString
--- PASS: TestEnvStringSlice_Values
PASS
```

- [ ] **Step 5: Commit**

```bash
git add app/config.go app/config_test.go
git commit -m "feat(config): add envStringSlice helper with tests"
```

---

## Task 2: Add `TrustedProxies` to Config struct and `LoadConfig`

**Files:**
- Modify: `app/config.go`

- [ ] **Step 1: Add `TrustedProxies` field to the `Config` struct**

Locate the `RedisURL` field at the bottom of the `Config` struct and add `TrustedProxies` immediately before the closing brace:

```go
	// TrustedProxies is the list of trusted reverse-proxy IPs or CIDRs used by
	// Gin's ClientIP resolution. Defaults to RFC1918 private ranges, which covers
	// Docker + Traefik deployments. Set TRUSTED_PROXIES= (empty) to trust only
	// RemoteAddr (use when directly internet-exposed with no proxy).
	TrustedProxies []string

	// RedisURL is the optional Redis connection URL (e.g. "redis://localhost:6379").
	// Leave empty to use in-memory implementations for all infra providers.
	RedisURL string
}
```

- [ ] **Step 2: Wire `TrustedProxies` in `LoadConfig`**

Inside the `LoadConfig` return statement, add after the `RedisURL` line:

```go
		RedisURL: os.Getenv("REDIS_URL"),

		TrustedProxies: envStringSlice("TRUSTED_PROXIES", []string{
			"172.16.0.0/12",
			"10.0.0.0/8",
			"192.168.0.0/16",
		}),
```

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add app/config.go
git commit -m "feat(config): add TrustedProxies field"
```

---

## Task 3: Wire `SetTrustedProxies` into the Gin router

**Files:**
- Modify: `app/server.go`

- [ ] **Step 1: Extract `gin.Default()` before the struct literal and call `SetTrustedProxies`**

In `app/server.go`, the `New` function currently has `router: gin.Default()` inline in the struct literal. Replace that section — from just before the struct literal to just after — with:

```go
	r := gin.Default()
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("router: SetTrustedProxies: %v", err)
	}

	s := &Server{
		cfg:          cfg,
		db:           database,
		billing:      bp,
		captcha:      cv,
		mailer:       m,
		search:       sc,
		router:       r,
		ctx:          ctx,
		infra:        infraProviders,
		infraCleanup: infraCleanup,
	}
```

(The lines after — `s.setupCORS()`, `s.setupRoutes()`, etc. — remain unchanged.)

- [ ] **Step 2: Verify build and all tests pass**

```bash
go build ./...
go test ./...
```

Expected: build succeeds, all tests pass.

- [ ] **Step 3: Commit**

```bash
git add app/server.go
git commit -m "feat(server): call SetTrustedProxies on startup"
```

---

## Task 4: Update `.env.example` and `CLAUDE.md`

**Files:**
- Modify: `.env.example`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add `TRUSTED_PROXIES` to `.env.example`**

Locate the `── Server ──` section in `.env.example` (after `GIN_MODE=debug`) and append:

```
# Trusted reverse-proxy IPs or CIDRs, comma-separated.
# Defaults to RFC1918 private ranges (covers Docker + Traefik out of the box).
# Set to empty string to trust only RemoteAddr (use when directly internet-exposed).
# TRUSTED_PROXIES=172.16.0.0/12,10.0.0.0/8,192.168.0.0/16
```

- [ ] **Step 2: Add `TRUSTED_PROXIES` row to the env var table in `CLAUDE.md`**

In `CLAUDE.md`, find the env var table (the section with `DATABASE_URL`, `JWT_SECRET`, etc.) and add a row after `REDIS_URL`:

```markdown
| `TRUSTED_PROXIES` | | Comma-separated trusted proxy IPs/CIDRs for `c.ClientIP()` resolution. Defaults to RFC1918 ranges (`172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`). Set empty to trust only `RemoteAddr`. |
```

- [ ] **Step 3: Commit**

```bash
git add .env.example CLAUDE.md
git commit -m "docs: document TRUSTED_PROXIES env var"
```
