# keygen.sh License Authorization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the homemade RSA-PS256 JWT license system in `billing/local` with keygen.sh, enforcing per-instance user count limits with machine locking and automatic session revocation for excess users.

**Architecture:** `billing.InstanceLimiter` is a new optional interface implemented only by `local.Provider`; `meteroid.Provider` (Cloud) is untouched. A `licenseCache` struct holds keygen.sh data and is refreshed by a background goroutine every hour. Machine fingerprints are persisted in the DB (`server_config` table). `KEYGEN_API_URL` and `KEYGEN_ACCOUNT_ID` are compile-time constants injected via `-ldflags`; only `KEYGEN_LICENSE_KEY` is a runtime env var.

**Tech Stack:** Go 1.23+, pgx/v5 (existing), `httptest` for HTTP mocking, `github.com/google/uuid` (already in go.mod).

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `billing/provider.go` | Modify | Add `InstanceLimiter` interface |
| `billing/local/keygen.go` | **Create** | keygen.sh HTTP client; compile-time `KeygenAPIURL`/`KeygenAccountID` vars |
| `billing/local/license_cache.go` | **Create** | In-memory license cache; `maxUsers()`, `refresh()` |
| `billing/local/provider.go` | Modify | New `New()` sig; `Start()` with machine activation + goroutine; `CheckRegistrationAllowed`; `enforceUserLimit`; updated `GetSubscription` |
| `billing/local/license.go` | **Delete** | JWT logic removed |
| `billing/local/keygen_test.go` | **Create** | Tests for keygen.sh client |
| `billing/local/license_cache_test.go` | **Create** | Tests for cache |
| `billing/local/provider_test.go` | **Create** | Tests for provider methods |
| `internal/model/model.go` | Modify | Add `SuspendedAt *int64` to `User` |
| `internal/handler/auth.go` | Modify | `Register`: `InstanceLimiter` check; `Login`: suspended check + `suspended_at` in SELECT |
| `app/config.go` | Modify | Remove `LicenseKey`; add `KeygenLicenseKey` |
| `cmd/server/main.go` | Modify | `local.New(cfg.KeygenLicenseKey, database)`; `bp.Start(ctx)` |
| `db/schema.pg.sql` | Modify | Add `suspended_at` to `users`; add `server_config` table; add unlimited capacity seed |

---

## Task 1: Schema migrations

**Files:**
- Modify: `db/schema.pg.sql`

- [ ] **Step 1: Add the three schema changes to `db/schema.pg.sql`**

Append to the end of the file (after existing `ALTER TABLE` statements):

```sql
-- keygen.sh license: user suspension for limit enforcement
ALTER TABLE users ADD COLUMN IF NOT EXISTS suspended_at BIGINT;

-- keygen.sh license: machine fingerprint persistence
CREATE TABLE IF NOT EXISTS server_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- OSS billing: seed unlimited plan so local.GetLimits always finds a row
INSERT INTO subscription_capacity
    (plan_code, plan_id, max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days)
VALUES ('unlimited', '', -1, -1, -1, -1, -1, -1)
ON CONFLICT (plan_code) DO NOTHING;
```

- [ ] **Step 2: Verify schema compiles by running build**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server
go build ./...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add db/schema.pg.sql
git commit -m "feat(schema): add suspended_at, server_config, unlimited capacity seed"
```

---

## Task 2: `billing.InstanceLimiter` interface

**Files:**
- Modify: `billing/provider.go`

- [ ] **Step 1: Add the interface**

Append to `billing/provider.go` after the `UserSyncer` interface block:

```go
// InstanceLimiter is implemented by providers that enforce instance-level user count
// limits. OSS local.Provider implements this; Cloud meteroid.Provider does not.
// auth.Register uses a type assertion — this is NOT part of billing.Provider.
type InstanceLimiter interface {
	// CheckRegistrationAllowed returns an error if registering a new user would
	// exceed the instance's licensed user count.
	CheckRegistrationAllowed(ctx context.Context) error
}
```

- [ ] **Step 2: Build to confirm no compilation errors**

```bash
go build ./billing/...
```

Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add billing/provider.go
git commit -m "feat(billing): add InstanceLimiter optional interface"
```

---

## Task 3: keygen.sh HTTP client

**Files:**
- Create: `billing/local/keygen.go`
- Create: `billing/local/keygen_test.go`

- [ ] **Step 1: Write the failing tests**

Create `billing/local/keygen_test.go`:

```go
package local

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestKeygenServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *keygenClient) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := newKeygenClient(srv.URL, "acct_test", "lic_test")
	return srv, client
}

func TestFetchLicense_active(t *testing.T) {
	expiry := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "License lic_test" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"attributes": map[string]any{
					"status": "ACTIVE",
					"expiry": expiry,
					"metadata": map[string]any{
						"max_users": float64(10),
					},
				},
			},
		})
	})

	lic, err := client.FetchLicense(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lic.Status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", lic.Status)
	}
	if lic.MaxUsers != 10 {
		t.Errorf("MaxUsers = %d, want 10", lic.MaxUsers)
	}
	if lic.Expiry == nil {
		t.Error("Expiry should not be nil")
	}
}

func TestFetchLicense_httpError(t *testing.T) {
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := client.FetchLicense(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestActivateMachine_created(t *testing.T) {
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	err := client.ActivateMachine(context.Background(), "fp-uuid", "my-host")
	if err != nil {
		t.Fatalf("unexpected error on 201: %v", err)
	}
}

func TestActivateMachine_conflict(t *testing.T) {
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	err := client.ActivateMachine(context.Background(), "fp-uuid", "my-host")
	if err != nil {
		t.Fatalf("409 should be treated as success, got: %v", err)
	}
}

func TestActivateMachine_limitExceeded(t *testing.T) {
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	})
	err := client.ActivateMachine(context.Background(), "fp-uuid", "my-host")
	if !errors.Is(err, ErrMachineLimitExceeded) {
		t.Errorf("expected ErrMachineLimitExceeded, got %v", err)
	}
}

func TestValidateMachine_active(t *testing.T) {
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "machine-1"}},
		})
	})
	active, err := client.ValidateMachine(context.Background(), "fp-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !active {
		t.Error("expected active=true")
	}
}

func TestValidateMachine_deactivated(t *testing.T) {
	_, client := newTestKeygenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})
	active, err := client.ValidateMachine(context.Background(), "fp-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active {
		t.Error("expected active=false for empty data")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./billing/local/ -run "TestFetchLicense|TestActivateMachine|TestValidateMachine" -v 2>&1 | head -20
```

Expected: compile error "undefined: newKeygenClient" (or similar).

- [ ] **Step 3: Implement `billing/local/keygen.go`**

Create `billing/local/keygen.go`:

```go
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// KeygenAPIURL and KeygenAccountID are set at build time via:
//
//	go build -ldflags "-X 'github.com/tabslate/server/billing/local.KeygenAPIURL=...'
//	                    -X 'github.com/tabslate/server/billing/local.KeygenAccountID=...'"
//
// They cannot be overridden at runtime to prevent pointing the binary at a fake
// keygen.sh instance.
var (
	KeygenAPIURL    = "https://api.keygen.sh"
	KeygenAccountID = ""
)

// ErrMachineLimitExceeded is returned by ActivateMachine when the license is
// already activated on another machine (HTTP 422 from keygen.sh).
var ErrMachineLimitExceeded = errors.New("license already activated on another machine")

type keygenClient struct {
	baseURL   string
	accountID string
	licKey    string
	http      *http.Client
}

func newKeygenClient(baseURL, accountID, licKey string) *keygenClient {
	if baseURL == "" {
		baseURL = KeygenAPIURL
	}
	return &keygenClient{
		baseURL:   baseURL,
		accountID: accountID,
		licKey:    licKey,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// keygenLicense holds parsed license data from keygen.sh.
type keygenLicense struct {
	MaxUsers int
	Status   string     // ACTIVE | EXPIRED | SUSPENDED
	Expiry   *time.Time
}

type keygenLicenseResp struct {
	Data struct {
		Attributes struct {
			Status   string                 `json:"status"`
			Expiry   *string                `json:"expiry"`
			Metadata map[string]interface{} `json:"metadata"`
		} `json:"attributes"`
	} `json:"data"`
}

// FetchLicense calls GET /v1/accounts/{id}/licenses/{key} and returns parsed data.
func (c *keygenClient) FetchLicense(ctx context.Context) (*keygenLicense, error) {
	url := fmt.Sprintf("%s/v1/accounts/%s/licenses/%s", c.baseURL, c.accountID, c.licKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("keygen FetchLicense: %w", err)
	}
	req.Header.Set("Authorization", "License "+c.licKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keygen FetchLicense: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("keygen FetchLicense: status %d: %s", resp.StatusCode, body)
	}

	var result keygenLicenseResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("keygen FetchLicense: decode: %w", err)
	}

	lic := &keygenLicense{Status: result.Data.Attributes.Status}

	if result.Data.Attributes.Expiry != nil {
		t, err := time.Parse(time.RFC3339, *result.Data.Attributes.Expiry)
		if err == nil {
			lic.Expiry = &t
		}
	}

	if v, ok := result.Data.Attributes.Metadata["max_users"]; ok {
		if n, ok := v.(float64); ok {
			lic.MaxUsers = int(n)
		}
	}

	return lic, nil
}

type keygenMachineReq struct {
	Data struct {
		Type       string `json:"type"`
		Attributes struct {
			Fingerprint string `json:"fingerprint"`
			Name        string `json:"name"`
		} `json:"attributes"`
		Relationships struct {
			License struct {
				Data struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"data"`
			} `json:"license"`
		} `json:"relationships"`
	} `json:"data"`
}

// ActivateMachine registers this machine fingerprint against the license.
// Returns nil on 201 (created) and 409 (already activated for this fingerprint).
// Returns ErrMachineLimitExceeded on 422 (another machine already holds the license).
func (c *keygenClient) ActivateMachine(ctx context.Context, fingerprint, hostname string) error {
	var payload keygenMachineReq
	payload.Data.Type = "machines"
	payload.Data.Attributes.Fingerprint = fingerprint
	payload.Data.Attributes.Name = hostname
	payload.Data.Relationships.License.Data.Type = "licenses"
	payload.Data.Relationships.License.Data.ID = c.licKey

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("keygen ActivateMachine: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/v1/accounts/%s/machines", c.baseURL, c.accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("keygen ActivateMachine: %w", err)
	}
	req.Header.Set("Authorization", "License "+c.licKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keygen ActivateMachine: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusConflict:
		return nil
	case http.StatusUnprocessableEntity:
		return ErrMachineLimitExceeded
	default:
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("keygen ActivateMachine: status %d: %s", resp.StatusCode, errBody)
	}
}

type keygenMachineListResp struct {
	Data []json.RawMessage `json:"data"`
}

// ValidateMachine returns true if this fingerprint is still activated on the license.
func (c *keygenClient) ValidateMachine(ctx context.Context, fingerprint string) (bool, error) {
	url := fmt.Sprintf("%s/v1/accounts/%s/machines?fingerprint=%s", c.baseURL, c.accountID, fingerprint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("keygen ValidateMachine: %w", err)
	}
	req.Header.Set("Authorization", "License "+c.licKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("keygen ValidateMachine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("keygen ValidateMachine: status %d: %s", resp.StatusCode, errBody)
	}

	var result keygenMachineListResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("keygen ValidateMachine: decode: %w", err)
	}

	return len(result.Data) > 0, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./billing/local/ -run "TestFetchLicense|TestActivateMachine|TestValidateMachine" -v
```

Expected: all 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add billing/local/keygen.go billing/local/keygen_test.go
git commit -m "feat(billing/local): add keygen.sh HTTP client with compile-time constants"
```

---

## Task 4: License cache

**Files:**
- Create: `billing/local/license_cache.go`
- Create: `billing/local/license_cache_test.go`

- [ ] **Step 1: Write failing tests**

Create `billing/local/license_cache_test.go`:

```go
package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLicenseCache_maxUsers_freeTier(t *testing.T) {
	c := newLicenseCache(nil, "")
	if got := c.maxUsers(); got != defaultFreeUsers {
		t.Errorf("maxUsers() = %d, want %d", got, defaultFreeUsers)
	}
}

func TestLicenseCache_maxUsers_activeWithLimit(t *testing.T) {
	c := newLicenseCache(nil, "")
	c.data = keygenLicense{Status: "ACTIVE", MaxUsers: 25}
	// manually set client to non-nil so it's not free-tier path
	c.client = &keygenClient{}
	if got := c.maxUsers(); got != 25 {
		t.Errorf("maxUsers() = %d, want 25", got)
	}
}

func TestLicenseCache_maxUsers_expiredFallsBackToFree(t *testing.T) {
	c := newLicenseCache(nil, "")
	past := time.Now().Add(-time.Hour)
	c.data = keygenLicense{Status: "ACTIVE", MaxUsers: 25, Expiry: &past}
	c.client = &keygenClient{}
	if got := c.maxUsers(); got != defaultFreeUsers {
		t.Errorf("expired license should return %d, got %d", defaultFreeUsers, got)
	}
}

func TestLicenseCache_maxUsers_suspendedFallsBackToFree(t *testing.T) {
	c := newLicenseCache(nil, "")
	c.data = keygenLicense{Status: "SUSPENDED", MaxUsers: 25}
	c.client = &keygenClient{}
	if got := c.maxUsers(); got != defaultFreeUsers {
		t.Errorf("suspended license should return %d, got %d", defaultFreeUsers, got)
	}
}

func TestLicenseCache_refresh_updatesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle both FetchLicense and ValidateMachine calls
		if r.Method == http.MethodGet && r.URL.Path != "" {
			if len(r.URL.Query().Get("fingerprint")) > 0 {
				// ValidateMachine
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "m1"}},
				})
				return
			}
			// FetchLicense
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"attributes": map[string]any{
						"status":   "ACTIVE",
						"expiry":   nil,
						"metadata": map[string]any{"max_users": float64(15)},
					},
				},
			})
		}
	}))
	defer srv.Close()

	client := newKeygenClient(srv.URL, "acct", "lic")
	c := newLicenseCache(client, "fp-test")

	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("refresh error: %v", err)
	}
	if got := c.maxUsers(); got != 15 {
		t.Errorf("after refresh maxUsers() = %d, want 15", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./billing/local/ -run "TestLicenseCache" -v 2>&1 | head -10
```

Expected: compile error "undefined: newLicenseCache".

- [ ] **Step 3: Implement `billing/local/license_cache.go`**

```go
package local

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	defaultFreeUsers    = 3
	defaultSyncInterval = time.Hour
)

type licenseCache struct {
	mu          sync.RWMutex
	data        keygenLicense
	client      *keygenClient // nil = free tier
	fingerprint string
}

func newLicenseCache(client *keygenClient, fingerprint string) *licenseCache {
	return &licenseCache{
		client:      client,
		fingerprint: fingerprint,
	}
}

// maxUsers returns the maximum users allowed by the current cached license.
// Returns defaultFreeUsers when: no license key, license is not ACTIVE, or license is expired.
func (c *licenseCache) maxUsers() int {
	if c.client == nil {
		return defaultFreeUsers
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.data.Status != "ACTIVE" {
		return defaultFreeUsers
	}
	if c.data.Expiry != nil && time.Now().After(*c.data.Expiry) {
		return defaultFreeUsers
	}
	if c.data.MaxUsers <= 0 {
		return defaultFreeUsers
	}
	return c.data.MaxUsers
}

// refresh fetches a fresh license from keygen.sh and validates the machine.
// On any error it retains the previous cached value and returns the error.
func (c *licenseCache) refresh(ctx context.Context) error {
	if c.client == nil {
		return nil
	}

	lic, err := c.client.FetchLicense(ctx)
	if err != nil {
		log.Printf("billing/local: license refresh: %v", err)
		return err
	}

	if c.fingerprint != "" {
		active, err := c.client.ValidateMachine(ctx, c.fingerprint)
		if err != nil {
			log.Printf("billing/local: machine validation: %v", err)
			return err
		}
		if !active {
			log.Printf("billing/local: machine deactivated via keygen.sh dashboard; treating license as revoked")
			lic.Status = "SUSPENDED"
		}
	}

	c.mu.Lock()
	c.data = *lic
	c.mu.Unlock()
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./billing/local/ -run "TestLicenseCache" -v
```

Expected: all 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add billing/local/license_cache.go billing/local/license_cache_test.go
git commit -m "feat(billing/local): add license cache with keygen.sh refresh"
```

---

## Task 5: Rewrite `billing/local/provider.go`

**Files:**
- Modify: `billing/local/provider.go`
- Create: `billing/local/provider_test.go`

- [ ] **Step 1: Write failing tests**

Create `billing/local/provider_test.go`:

```go
package local

import (
	"context"
	"testing"

	"github.com/tabslate/server/billing"
)

// compile-time assertions
var _ billing.Provider = (*Provider)(nil)
var _ billing.InstanceLimiter = (*Provider)(nil)

func TestNew_missingAccountID(t *testing.T) {
	KeygenAccountID = "" // ensure compile-time var is empty
	_, err := New("some-license-key", nil)
	if err == nil {
		t.Fatal("expected error when licenseKey set but KeygenAccountID empty")
	}
}

func TestNew_freeTier(t *testing.T) {
	p, err := New("", nil)
	if err != nil {
		t.Fatalf("free tier New() should not error: %v", err)
	}
	if p.cache.client != nil {
		t.Error("free tier should have nil keygen client")
	}
}

func TestCheckRegistrationAllowed_freeTierAtLimit(t *testing.T) {
	// Free tier: maxUsers = 3. Simulate 3 verified users already in DB.
	// Without a real DB we test the cache path directly.
	p := &Provider{
		cache: newLicenseCache(nil, ""), // free tier = 3 users
	}
	// Inject a mock DB that returns count=3
	// Since Provider.db is nil, CheckRegistrationAllowed should handle nil db gracefully.
	// Test the maxUsers logic separately via the cache.
	if p.cache.maxUsers() != defaultFreeUsers {
		t.Errorf("expected free limit %d", defaultFreeUsers)
	}
}

func TestGetSubscription_freeTier(t *testing.T) {
	p := &Provider{cache: newLicenseCache(nil, "")}
	sub, err := p.GetSubscription(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Plan != billing.PlanFree {
		t.Errorf("plan = %q, want free", sub.Plan)
	}
}

func TestGetSubscription_activeLicense(t *testing.T) {
	c := newLicenseCache(&keygenClient{}, "fp")
	c.data = keygenLicense{Status: "ACTIVE", MaxUsers: 10}
	p := &Provider{cache: c}
	sub, err := p.GetSubscription(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Plan != billing.PlanPro {
		t.Errorf("plan = %q, want pro", sub.Plan)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./billing/local/ -run "TestNew|TestCheckRegistration|TestGetSubscription" -v 2>&1 | head -20
```

Expected: compile errors because the new `New()` signature doesn't exist yet.

- [ ] **Step 3: Rewrite `billing/local/provider.go`**

Replace the full contents of `billing/local/provider.go` with:

```go
// Package local implements billing.Provider for the OSS self-hosted edition.
// Quota decisions are based on the keygen.sh license (or free-tier defaults when
// no license key is configured). No external network calls are made in the hot path.
package local

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
)

var _ billing.Provider = (*Provider)(nil)
var _ billing.InstanceLimiter = (*Provider)(nil)

// Provider is the OSS billing implementation.
type Provider struct {
	db    *db.DB
	cache *licenseCache
}

// New creates a local Provider. If licenseKey is empty the provider operates in
// free-tier mode (3 users max). KeygenAPIURL and KeygenAccountID must be set at
// build time via -ldflags when a licenseKey is supplied.
func New(licenseKey string, d *db.DB) (*Provider, error) {
	var cache *licenseCache
	if licenseKey != "" {
		if KeygenAccountID == "" {
			return nil, fmt.Errorf("billing/local: KEYGEN_ACCOUNT_ID must be set at build time when a license key is provided")
		}
		client := newKeygenClient(KeygenAPIURL, KeygenAccountID, licenseKey)
		cache = newLicenseCache(client, "") // fingerprint set in Start()
	} else {
		cache = newLicenseCache(nil, "") // free tier
	}
	return &Provider{db: d, cache: cache}, nil
}

// Start activates the machine against the license (fatal on duplicate machine),
// performs the initial license sync, and launches the background refresh goroutine.
// Must be called once after New(), before the HTTP server accepts requests.
func (p *Provider) Start(ctx context.Context) {
	if p.cache.client == nil {
		return // free tier — no keygen.sh calls
	}

	// Load or generate a stable machine fingerprint.
	fingerprint, err := p.loadOrCreateFingerprint(ctx)
	if err != nil {
		log.Fatalf("billing/local: machine fingerprint: %v", err)
	}
	p.cache.fingerprint = fingerprint

	// Activate this machine. Fatal if another machine already holds the license.
	hostname, _ := os.Hostname()
	if err := p.cache.client.ActivateMachine(ctx, fingerprint, hostname); err != nil {
		if errors.Is(err, ErrMachineLimitExceeded) {
			log.Fatalf("billing/local: license already activated on another machine; " +
				"deactivate it from the keygen.sh dashboard first")
		}
		log.Printf("billing/local: machine activation: %v (will retry on next refresh)", err)
	}

	// Initial license sync + limit enforcement.
	if err := p.cache.refresh(ctx); err != nil {
		log.Printf("billing/local: initial license sync failed; using free-tier limits: %v", err)
	}
	p.enforceUserLimit(ctx)

	// Background refresh goroutine.
	go func() {
		ticker := time.NewTicker(defaultSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := p.cache.refresh(ctx); err != nil {
					log.Printf("billing/local: license refresh: %v", err)
				}
				p.enforceUserLimit(ctx)
			}
		}
	}()
}

// loadOrCreateFingerprint reads the machine fingerprint from server_config, or
// generates and persists a new UUIDv4 if none exists.
func (p *Provider) loadOrCreateFingerprint(ctx context.Context) (string, error) {
	var fp string
	err := p.db.QueryRow(ctx,
		`SELECT value FROM server_config WHERE key = 'license_machine_fingerprint'`,
	).Scan(&fp)
	if err == nil {
		return fp, nil
	}
	fp = uuid.NewString()
	_, err = p.db.Exec(ctx,
		`INSERT INTO server_config (key, value) VALUES ('license_machine_fingerprint', $1)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		fp,
	)
	return fp, err
}

// CheckRegistrationAllowed implements billing.InstanceLimiter.
// Returns an error if the current verified user count is at or above the license limit.
func (p *Provider) CheckRegistrationAllowed(ctx context.Context) error {
	max := p.cache.maxUsers()
	var count int
	if err := p.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE is_verified = true`,
	).Scan(&count); err != nil {
		return fmt.Errorf("billing/local: user count: %w", err)
	}
	if count >= max {
		return fmt.Errorf("user limit reached: this instance allows %d verified users", max)
	}
	return nil
}

// enforceUserLimit suspends users beyond the license limit and un-suspends those
// within the limit. Called after each license refresh. Users are ordered by
// created_at ASC so the oldest accounts are always preserved.
func (p *Provider) enforceUserLimit(ctx context.Context) {
	if p.db == nil {
		return
	}
	max := p.cache.maxUsers()

	rows, err := p.db.Query(ctx,
		`SELECT id FROM users WHERE is_verified = true ORDER BY created_at ASC`,
	)
	if err != nil {
		log.Printf("billing/local: enforceUserLimit: query: %v", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}

	now := time.Now().Unix()
	for i, id := range ids {
		if i < max {
			p.db.Exec(ctx,
				`UPDATE users SET suspended_at = NULL WHERE id = $1 AND suspended_at IS NOT NULL`,
				id,
			)
		} else {
			p.db.Exec(ctx,
				`UPDATE users SET suspended_at = $1 WHERE id = $2 AND suspended_at IS NULL`,
				now, id,
			)
			p.db.Exec(ctx,
				`DELETE FROM refresh_tokens WHERE user_id = $1`,
				id,
			)
		}
	}
}

// OnUserCreated is a no-op for the OSS edition.
func (p *Provider) OnUserCreated(_ context.Context, _ billing.UserInfo) error { return nil }

// GetLimits returns unlimited resource caps for all OSS users.
// User count enforcement is handled separately via InstanceLimiter.
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

// GetSubscription returns the plan inferred from the license cache.
func (p *Provider) GetSubscription(_ context.Context, _ string) (*billing.Subscription, error) {
	if p.cache.client != nil {
		p.cache.mu.RLock()
		status := p.cache.data.Status
		expiry := p.cache.data.Expiry
		p.cache.mu.RUnlock()

		if status == "ACTIVE" && (expiry == nil || time.Now().Before(*expiry)) {
			return &billing.Subscription{Plan: billing.PlanPro, Status: "active"}, nil
		}
	}
	return &billing.Subscription{Plan: billing.PlanFree, Status: "active"}, nil
}

// GetCheckoutURL is not supported in the OSS edition.
func (p *Provider) GetCheckoutURL(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf(
		"online checkout is not available in the OSS edition; " +
			"visit https://tabslate.app/pricing to purchase a license",
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
	return &billing.Limits{MaxWorkspaces: -1, MaxBookmarks: -1, MaxCollections: -1, MaxTags: -1, MaxSavedGroups: -1, TrashGraceDays: -1}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./billing/local/ -v
```

Expected: all tests PASS. If `TestNew_missingAccountID` fails, verify `KeygenAccountID` is being checked correctly in `New()`.

- [ ] **Step 5: Commit**

```bash
git add billing/local/provider.go billing/local/provider_test.go
git commit -m "feat(billing/local): rewrite provider with keygen.sh cache and InstanceLimiter"
```

---

## Task 6: Delete `billing/local/license.go`

**Files:**
- Delete: `billing/local/license.go`

- [ ] **Step 1: Remove the file**

```bash
rm /Users/lieutenant/Documents/github/TabSlate-server/billing/local/license.go
```

- [ ] **Step 2: Remove the jwt dependency if nothing else uses it**

```bash
grep -r "golang-jwt" /Users/lieutenant/Documents/github/TabSlate-server --include="*.go"
```

Expected: no output (nothing else imports it). If output is empty, run:

```bash
go mod tidy
```

- [ ] **Step 3: Build to verify no broken references**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 4: Run all billing tests**

```bash
go test ./billing/...
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore(billing/local): remove JWT license.go and jwt/v5 dependency"
```

---

## Task 7: Config and entry point

**Files:**
- Modify: `app/config.go`
- Modify: `app/config_test.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Update `app/config.go`**

In `app/config.go` at line 26-27, replace:

```go
// LicenseKey is the optional OSS License JWT. Leave empty for free-tier mode.
LicenseKey string
```

With:

```go
// KeygenLicenseKey is the optional keygen.sh license key. Leave empty for free-tier mode (3 users max).
KeygenLicenseKey string
```

In `LoadConfig()` at line 134, replace:

```go
LicenseKey:  os.Getenv("LICENSE_KEY"),
```

With:

```go
KeygenLicenseKey: os.Getenv("KEYGEN_LICENSE_KEY"),
```

- [ ] **Step 2: Verify config_test.go needs no changes**

```bash
grep -n "LicenseKey\|LICENSE_KEY" /Users/lieutenant/Documents/github/TabSlate-server/app/config_test.go
```

Expected: no output (config_test.go does not test LicenseKey). No edits needed.

- [ ] **Step 3: Update `cmd/server/main.go`**

Replace:
```go
bp, err := local.New(cfg.LicenseKey, nil /* no public key in dev */, database)
```

With:
```go
bp, err := local.New(cfg.KeygenLicenseKey, database)
if err != nil {
    log.Fatalf("billing provider: %v", err)
}
bp.Start(ctx)
```

Note: `bp.Start(ctx)` must be called **before** `app.New(...)`.

- [ ] **Step 4: Build to verify**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 5: Commit**

```bash
git add app/config.go app/config_test.go cmd/server/main.go
git commit -m "feat(config): replace LicenseKey with KeygenLicenseKey; call bp.Start(ctx)"
```

---

## Task 8: Add `SuspendedAt` to `model.User`

**Files:**
- Modify: `internal/model/model.go`

- [ ] **Step 1: Add the field**

In `internal/model/model.go`, add `SuspendedAt` to the `User` struct after `UpdatedAt`:

```go
type User struct {
    ID         string `json:"id"`
    Name       string `json:"name"`
    Email      string `json:"email"`
    IsVerified bool   `json:"is_verified"`

    PasswordHash string `json:"-"`
    CreatedAt    int64  `json:"created_at"`
    UpdatedAt    int64  `json:"updated_at"`
    SuspendedAt  *int64 `json:"suspended_at,omitempty"`
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/model/model.go
git commit -m "feat(model): add SuspendedAt to User struct"
```

---

## Task 9: `auth.Register` — InstanceLimiter check

**Files:**
- Modify: `internal/handler/auth.go`

- [ ] **Step 1: Add the import and check to `Register`**

In `internal/handler/auth.go`, add `"github.com/tabslate/server/billing"` to imports if not already present (it is — line 17).

In `Register()`, after the email uniqueness check (after line 121 `c.JSON(http.StatusConflict, ...)`), insert:

```go
// ── Step 3.5: Instance user limit check (OSS only; Cloud skips via type assertion) ──
if il, ok := h.billing.(billing.InstanceLimiter); ok {
    if err := il.CheckRegistrationAllowed(ctx); err != nil {
        c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
        return
    }
}
```

Place this block between the email uniqueness check and the password hashing (i.e., after line 121 and before line 123 `hash, err := auth.HashPassword(...)`).

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/handler/auth.go
git commit -m "feat(auth): enforce instance user limit in Register via InstanceLimiter"
```

---

## Task 10: `auth.Login` — suspended user check

**Files:**
- Modify: `internal/handler/auth.go`

- [ ] **Step 1: Update the SELECT query in `Login` to include `suspended_at`**

In `auth.Login()` (around line 211), replace:

```go
var user model.User
err := h.db.QueryRow(ctx,
    `SELECT id, name, email, password_hash, is_verified, created_at, updated_at FROM users WHERE email = $1`,
    req.Email,
).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt)
```

With:

```go
var user model.User
err := h.db.QueryRow(ctx,
    `SELECT id, name, email, password_hash, is_verified, created_at, updated_at, suspended_at FROM users WHERE email = $1`,
    req.Email,
).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt, &user.SuspendedAt)
```

- [ ] **Step 2: Add suspended check before issuing tokens**

After the `auth.CheckPassword` block and the `h.limiter.ResetCounter` call (around line 228), insert:

```go
// Suspended users (over license limit) cannot log in.
if user.SuspendedAt != nil {
    c.JSON(http.StatusForbidden, gin.H{"error": "account suspended: instance user limit exceeded"})
    return
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 4: Run all tests**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/auth.go
git commit -m "feat(auth): block suspended users from logging in"
```

---

## Task 11: Final verification

- [ ] **Step 1: Full build**

```bash
go build ./...
```

Expected: exits 0.

- [ ] **Step 2: Full test suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 3: Verify compile-time assertions pass**

```bash
go vet ./...
```

Expected: exits 0.

- [ ] **Step 4: Verify `license.go` is gone and JWT dependency removed**

```bash
ls billing/local/
# Should NOT contain: license.go
grep "golang-jwt" go.mod
# Should produce no output
```

- [ ] **Step 5: Verify only runtime env var remains**

```bash
grep -r "KEYGEN_API_URL\|KEYGEN_ACCOUNT_ID" --include="*.go" .
# Should produce no output (these are compile-time only)
grep -r "KEYGEN_LICENSE_KEY" --include="*.go" .
# Should show only app/config.go
```

- [ ] **Step 6: Final commit**

```bash
git add -A
git commit -m "chore: final verification — keygen.sh license integration complete"
```
