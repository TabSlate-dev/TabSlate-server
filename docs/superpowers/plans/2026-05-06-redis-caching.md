# Redis Caching & Horizontal Scaling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce three provider interfaces (pubsub.Hub, store.Cache, ratelimit.Limiter) backed by in-memory implementations for OSS and Redis implementations for Cloud, replacing the in-process SSE hub, DB-backed rate-limit tables, and DB-backed SSE tokens.

**Architecture:** Each concern gets its own `internal/` package with an interface and two implementations. A new `internal/infra` factory reads `REDIS_URL` from config and wires all three providers. `app.New()` signature is unchanged — infra is created internally from config.

**Tech Stack:** Go 1.25, github.com/redis/go-redis/v9, existing pgx/v5 + Gin stack.

**Spec:** `docs/superpowers/specs/2026-05-06-redis-caching-design.md`

---

## File Map

| Action | Path | Responsibility |
|---|---|---|
| Create | `internal/pubsub/hub.go` | `Hub` interface |
| Create | `internal/pubsub/memory.go` | `InMemoryHub` (migrated from `sse_hub.go`) |
| Create | `internal/pubsub/memory_test.go` | Unit tests for InMemoryHub |
| Create | `internal/pubsub/redis.go` | `RedisHub` |
| Create | `internal/store/cache.go` | `Cache` interface |
| Create | `internal/store/memory.go` | `InMemoryCache` |
| Create | `internal/store/memory_test.go` | Unit tests for InMemoryCache |
| Create | `internal/store/redis.go` | `RedisCache` |
| Create | `internal/ratelimit/limiter.go` | `Limiter` interface |
| Create | `internal/ratelimit/memory.go` | `InMemoryLimiter` |
| Create | `internal/ratelimit/memory_test.go` | Unit tests for InMemoryLimiter |
| Create | `internal/ratelimit/redis.go` | `RedisLimiter` |
| Create | `internal/infra/infra.go` | Factory: wires all 3 providers |
| Delete | `internal/handler/sse_hub.go` | Replaced by `internal/pubsub/memory.go` |
| Modify | `internal/handler/sse.go` | Inject Hub + Cache; remove DB token ops |
| Modify | `internal/handler/auth.go` | Inject Limiter + Cache; remove DB rate-limit calls + SSE token DB ops |
| Modify | `internal/handler/sync.go` | Inject Hub; replace globalHub.Broadcast |
| Modify | `internal/handler/billing.go` | Inject Cache; cache GetLimits results |
| Modify | `internal/middleware/ratelimit.go` | Refactor RateLimitByIP to accept Limiter interface |
| Modify | `app/config.go` | Add `RedisURL string` field |
| Modify | `app/server.go` | Call `infra.New(cfg.RedisURL)`; add `infraCleanup` to Server; wire all handlers |
| Modify | `db/schema.pg.sql` | Drop 4 tables in migration block |
| Modify | `go.mod` / `go.sum` | Add go-redis/v9 |

---

## Task 1: Add go-redis/v9 dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the dependency**

```bash
cd /path/to/TabSlate-server && go get github.com/redis/go-redis/v9
```

Expected output: `go: added github.com/redis/go-redis/v9 vX.Y.Z`

- [ ] **Step 2: Verify it compiles**

```bash
go build ./...
```

Expected: no errors (nothing imports it yet, that's fine)

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add go-redis/v9 dependency"
```

---

## Task 2: pubsub package — Hub interface + InMemoryHub

**Files:**
- Create: `internal/pubsub/hub.go`
- Create: `internal/pubsub/memory.go`
- Create: `internal/pubsub/memory_test.go`
- Delete: `internal/handler/sse_hub.go`

- [ ] **Step 1: Write the failing test**

Create `internal/pubsub/memory_test.go`:

```go
package pubsub_test

import (
	"testing"
	"time"

	"github.com/tabslate/server/internal/pubsub"
)

func TestInMemoryHub_BroadcastReachesSubscriber(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	connID, ch := h.Subscribe("user1")
	_ = connID

	h.Broadcast("user1", 42)

	select {
	case seq := <-ch:
		if seq != 42 {
			t.Fatalf("got seq %d, want 42", seq)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for broadcast")
	}
}

func TestInMemoryHub_UnsubscribeClosesChannel(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	connID, ch := h.Subscribe("user1")
	h.Unsubscribe("user1", connID)

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout: channel not closed after Unsubscribe")
	}
}

func TestInMemoryHub_BroadcastToWrongUserIgnored(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	_, ch := h.Subscribe("user1")

	h.Broadcast("user2", 99)

	select {
	case <-ch:
		t.Fatal("expected no broadcast for user1")
	case <-time.After(50 * time.Millisecond):
		// correct: nothing received
	}
}

func TestInMemoryHub_MultipleSubscribersAllReceive(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	_, ch1 := h.Subscribe("user1")
	_, ch2 := h.Subscribe("user1")

	h.Broadcast("user1", 7)

	for _, ch := range []<-chan int64{ch1, ch2} {
		select {
		case seq := <-ch:
			if seq != 7 {
				t.Fatalf("got %d, want 7", seq)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/pubsub/... -v
```

Expected: `FAIL` — package does not exist yet.

- [ ] **Step 3: Create the Hub interface**

Create `internal/pubsub/hub.go`:

```go
package pubsub

// Hub is a pub/sub broadcaster for SSE connections.
// Subscribe returns a receive-only channel that receives new seq values.
// Implementations must be safe for concurrent use.
type Hub interface {
	Subscribe(userID string) (connID int64, ch <-chan int64)
	Broadcast(userID string, seq int64)
	Unsubscribe(userID string, connID int64)
}
```

- [ ] **Step 4: Create InMemoryHub**

Create `internal/pubsub/memory.go`:

```go
package pubsub

import (
	"sync"
	"sync/atomic"
)

// InMemoryHub is a process-local Hub implementation. Safe for single-instance
// deployments. For multi-instance use RedisHub.
type InMemoryHub struct {
	mu   sync.RWMutex
	subs map[string]map[int64]chan int64
	next atomic.Int64
}

func NewInMemoryHub() *InMemoryHub {
	return &InMemoryHub{subs: make(map[string]map[int64]chan int64)}
}

func (h *InMemoryHub) Subscribe(userID string) (int64, <-chan int64) {
	connID := h.next.Add(1)
	ch := make(chan int64, 8)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[int64]chan int64)
	}
	h.subs[userID][connID] = ch
	h.mu.Unlock()
	return connID, ch
}

func (h *InMemoryHub) Unsubscribe(userID string, connID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if conns, ok := h.subs[userID]; ok {
		if ch, ok := conns[connID]; ok {
			close(ch)
			delete(conns, connID)
		}
		if len(conns) == 0 {
			delete(h.subs, userID)
		}
	}
}

func (h *InMemoryHub) Broadcast(userID string, seq int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs[userID] {
		select {
		case ch <- seq:
		default:
		}
	}
}

// Close shuts down the hub, closing all subscriber channels.
func (h *InMemoryHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, conns := range h.subs {
		for _, ch := range conns {
			close(ch)
		}
	}
	h.subs = make(map[string]map[int64]chan int64)
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/pubsub/... -v
```

Expected: all 4 tests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add internal/pubsub/
git commit -m "feat(pubsub): add Hub interface and InMemoryHub"
```

> `internal/handler/sse_hub.go` is deleted in Task 7 together with the handler rewrites, keeping the build green throughout.

---

## Task 3: store package — Cache interface + InMemoryCache

**Files:**
- Create: `internal/store/cache.go`
- Create: `internal/store/memory.go`
- Create: `internal/store/memory_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/store/memory_test.go`:

```go
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/tabslate/server/internal/store"
)

func TestInMemoryCache_SetAndGet(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}

	val, found, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected key to be found")
	}
	if string(val) != "v" {
		t.Fatalf("got %q, want %q", val, "v")
	}
}

func TestInMemoryCache_GetMissing(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	_, found, err := c.Get(context.Background(), "no-such-key")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected key to not be found")
	}
}

func TestInMemoryCache_Del(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	ctx := context.Background()
	c.Set(ctx, "k", []byte("v"), time.Minute)
	c.Del(ctx, "k")

	_, found, _ := c.Get(ctx, "k")
	if found {
		t.Fatal("expected key to be gone after Del")
	}
}

func TestInMemoryCache_TTLExpiry(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	ctx := context.Background()
	c.Set(ctx, "k", []byte("v"), 50*time.Millisecond)

	time.Sleep(100 * time.Millisecond)

	_, found, _ := c.Get(ctx, "k")
	if found {
		t.Fatal("expected key to be expired")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/store/... -v
```

Expected: `FAIL` — package does not exist.

- [ ] **Step 3: Create the Cache interface**

Create `internal/store/cache.go`:

```go
package store

import (
	"context"
	"time"
)

// Cache is a key-value store with TTL support.
// Get returns (val, true, nil) on hit, (nil, false, nil) on miss, (nil, false, err) on error.
// Implementations must be safe for concurrent use.
type Cache interface {
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Del(ctx context.Context, key string) error
}
```

- [ ] **Step 4: Create InMemoryCache**

Create `internal/store/memory.go`:

```go
package store

import (
	"context"
	"sync"
	"time"
)

type cacheEntry struct {
	val      []byte
	expireAt int64 // UnixNano; 0 = never expires
}

// InMemoryCache is a process-local Cache implementation with lazy TTL expiry
// and a periodic background sweep every 30 seconds.
type InMemoryCache struct {
	mu   sync.RWMutex
	data map[string]cacheEntry
	done chan struct{}
}

func NewInMemoryCache() *InMemoryCache {
	c := &InMemoryCache{
		data: make(map[string]cacheEntry),
		done: make(chan struct{}),
	}
	go c.sweep()
	return c
}

func (c *InMemoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	var expireAt int64
	if ttl > 0 {
		expireAt = time.Now().Add(ttl).UnixNano()
	}
	c.mu.Lock()
	c.data[key] = cacheEntry{val: val, expireAt: expireAt}
	c.mu.Unlock()
	return nil
}

func (c *InMemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.RLock()
	e, ok := c.data[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if e.expireAt != 0 && time.Now().UnixNano() > e.expireAt {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		return nil, false, nil
	}
	return e.val, true, nil
}

func (c *InMemoryCache) Del(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.data, key)
	c.mu.Unlock()
	return nil
}

// Close stops the background sweep goroutine.
func (c *InMemoryCache) Close() {
	close(c.done)
}

func (c *InMemoryCache) sweep() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			c.mu.Lock()
			for k, e := range c.data {
				if e.expireAt != 0 && now > e.expireAt {
					delete(c.data, k)
				}
			}
			c.mu.Unlock()
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/store/... -v
```

Expected: all 4 tests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add internal/store/
git commit -m "feat(store): add Cache interface and InMemoryCache"
```

---

## Task 4: ratelimit package — Limiter interface + InMemoryLimiter

**Files:**
- Create: `internal/ratelimit/limiter.go`
- Create: `internal/ratelimit/memory.go`
- Create: `internal/ratelimit/memory_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/ratelimit/memory_test.go`:

```go
package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/tabslate/server/internal/ratelimit"
)

func TestInMemoryLimiter_Allow_UnderLimit(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if !l.Allow(ctx, "ip:1.2.3.4", 5, time.Minute) {
			t.Fatalf("expected Allow to return true on request %d", i+1)
		}
	}
}

func TestInMemoryLimiter_Allow_AtLimit(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.Allow(ctx, "ip:1.2.3.4", 5, time.Minute)
	}
	if l.Allow(ctx, "ip:1.2.3.4", 5, time.Minute) {
		t.Fatal("expected Allow to return false when at limit")
	}
}

func TestInMemoryLimiter_Allow_WindowExpiry(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.Allow(ctx, "ip:1.2.3.4", 5, 50*time.Millisecond)
	}
	// After window expires, limit resets
	time.Sleep(60 * time.Millisecond)
	if !l.Allow(ctx, "ip:1.2.3.4", 5, 50*time.Millisecond) {
		t.Fatal("expected Allow to return true after window expired")
	}
}

func TestInMemoryLimiter_IncrCounter_ReturnsMonotonicCount(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := int64(1); i <= 3; i++ {
		count, err := l.IncrCounter(ctx, "email:foo@bar.com", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if count != i {
			t.Fatalf("got count %d, want %d", count, i)
		}
	}
}

func TestInMemoryLimiter_ResetCounter(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	l.IncrCounter(ctx, "email:foo@bar.com", time.Minute)
	l.IncrCounter(ctx, "email:foo@bar.com", time.Minute)
	l.ResetCounter(ctx, "email:foo@bar.com")

	count, _ := l.GetCounter(ctx, "email:foo@bar.com")
	if count != 0 {
		t.Fatalf("got count %d after reset, want 0", count)
	}
}

func TestInMemoryLimiter_IncrCounter_WindowExpiry(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	l.IncrCounter(ctx, "ip:1.2.3.4", 50*time.Millisecond)
	l.IncrCounter(ctx, "ip:1.2.3.4", 50*time.Millisecond)

	time.Sleep(60 * time.Millisecond)

	count, _ := l.IncrCounter(ctx, "ip:1.2.3.4", 50*time.Millisecond)
	if count != 1 {
		t.Fatalf("got count %d after window expiry, want 1", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/ratelimit/... -v
```

Expected: `FAIL` — package does not exist.

- [ ] **Step 3: Create the Limiter interface**

Create `internal/ratelimit/limiter.go`:

```go
package ratelimit

import (
	"context"
	"time"
)

// Limiter provides rate limiting and counter primitives.
// Allow uses a sliding-window algorithm; true = request is permitted.
// IncrCounter atomically increments a named counter, setting a TTL on first
// increment. Implementations must be safe for concurrent use.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) bool
	IncrCounter(ctx context.Context, key string, window time.Duration) (int64, error)
	ResetCounter(ctx context.Context, key string) error
	GetCounter(ctx context.Context, key string) (int64, error)
}
```

- [ ] **Step 4: Create InMemoryLimiter**

Create `internal/ratelimit/memory.go`:

```go
package ratelimit

import (
	"context"
	"sync"
	"time"
)

type windowEntry struct {
	timestamps []int64 // UnixNano
}

type counterEntry struct {
	count   int64
	resetAt int64 // UnixNano
}

// InMemoryLimiter is a process-local Limiter. For multi-instance deployments
// use RedisLimiter so rate-limit state is shared across instances.
type InMemoryLimiter struct {
	mu       sync.Mutex
	windows  map[string]*windowEntry
	counters map[string]*counterEntry
}

func NewInMemoryLimiter() *InMemoryLimiter {
	return &InMemoryLimiter{
		windows:  make(map[string]*windowEntry),
		counters: make(map[string]*counterEntry),
	}
}

func (l *InMemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()
	cutoff := now - window.Nanoseconds()

	e, ok := l.windows[key]
	if !ok {
		e = &windowEntry{}
		l.windows[key] = e
	}

	// Drop timestamps outside the window.
	valid := e.timestamps[:0]
	for _, ts := range e.timestamps {
		if ts > cutoff {
			valid = append(valid, ts)
		}
	}
	e.timestamps = valid

	if len(e.timestamps) >= limit {
		return false
	}
	e.timestamps = append(e.timestamps, now)
	return true
}

func (l *InMemoryLimiter) IncrCounter(_ context.Context, key string, window time.Duration) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()
	e, ok := l.counters[key]
	if !ok || now > e.resetAt {
		l.counters[key] = &counterEntry{count: 1, resetAt: now + window.Nanoseconds()}
		return 1, nil
	}
	e.count++
	return e.count, nil
}

func (l *InMemoryLimiter) ResetCounter(_ context.Context, key string) error {
	l.mu.Lock()
	delete(l.counters, key)
	l.mu.Unlock()
	return nil
}

func (l *InMemoryLimiter) GetCounter(_ context.Context, key string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()
	e, ok := l.counters[key]
	if !ok || now > e.resetAt {
		return 0, nil
	}
	return e.count, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/ratelimit/... -v
```

Expected: all 6 tests `PASS`.

- [ ] **Step 6: Commit**

```bash
git add internal/ratelimit/
git commit -m "feat(ratelimit): add Limiter interface and InMemoryLimiter"
```

---

## Task 5: Redis implementations

**Files:**
- Create: `internal/pubsub/redis.go`
- Create: `internal/store/redis.go`
- Create: `internal/ratelimit/redis.go`

These files compile-check against the interfaces defined in Tasks 2–4. Integration tests are skipped when `REDIS_URL` is not set.

- [ ] **Step 1: Create RedisHub**

Create `internal/pubsub/redis.go`:

```go
package pubsub

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

const hubChannelPrefix = "tabslate:sync:"

// RedisHub is a Hub backed by Redis pub/sub. All fan-out (including same-instance
// connections) flows through Redis so there is no double-delivery. Safe for
// multi-instance deployments.
type RedisHub struct {
	rdb       *redis.Client
	mu        sync.RWMutex
	subs      map[string]map[int64]chan int64 // userID → connID → local ch
	redisSubs map[string]*redis.PubSub        // userID → redis subscription
	next      atomic.Int64
}

func NewRedisHub(rdb *redis.Client) *RedisHub {
	return &RedisHub{
		rdb:       rdb,
		subs:      make(map[string]map[int64]chan int64),
		redisSubs: make(map[string]*redis.PubSub),
	}
}

func (h *RedisHub) Subscribe(userID string) (int64, <-chan int64) {
	connID := h.next.Add(1)
	ch := make(chan int64, 8)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.subs[userID] == nil {
		h.subs[userID] = make(map[int64]chan int64)
		ps := h.rdb.Subscribe(context.Background(), hubChannelPrefix+userID)
		h.redisSubs[userID] = ps
		go h.readFromRedis(userID, ps)
	}
	h.subs[userID][connID] = ch
	return connID, ch
}

func (h *RedisHub) readFromRedis(userID string, ps *redis.PubSub) {
	for msg := range ps.Channel() {
		seq, err := strconv.ParseInt(msg.Payload, 10, 64)
		if err != nil {
			log.Printf("pubsub: bad payload for user %s: %v", userID, err)
			continue
		}
		h.mu.RLock()
		for _, ch := range h.subs[userID] {
			select {
			case ch <- seq:
			default:
			}
		}
		h.mu.RUnlock()
	}
}

// Broadcast publishes seq to Redis. readFromRedis handles local fan-out so that
// all instances (including this one) receive the event exactly once.
func (h *RedisHub) Broadcast(userID string, seq int64) {
	if err := h.rdb.Publish(context.Background(), hubChannelPrefix+userID,
		fmt.Sprintf("%d", seq)).Err(); err != nil {
		log.Printf("pubsub: publish for user %s: %v", userID, err)
	}
}

func (h *RedisHub) Unsubscribe(userID string, connID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	conns, ok := h.subs[userID]
	if !ok {
		return
	}
	if ch, ok := conns[connID]; ok {
		close(ch)
		delete(conns, connID)
	}
	if len(conns) == 0 {
		delete(h.subs, userID)
		if ps, ok := h.redisSubs[userID]; ok {
			ps.Close()
			delete(h.redisSubs, userID)
		}
	}
}

// Close shuts down all Redis subscriptions and local channels.
func (h *RedisHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for userID, ps := range h.redisSubs {
		ps.Close()
		for _, ch := range h.subs[userID] {
			close(ch)
		}
	}
	h.subs = make(map[string]map[int64]chan int64)
	h.redisSubs = make(map[string]*redis.PubSub)
}
```

- [ ] **Step 2: Create RedisCache**

Create `internal/store/redis.go`:

```go
package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache is a Cache backed by Redis. TTL is enforced natively by Redis.
type RedisCache struct {
	rdb *redis.Client
}

func NewRedisCache(rdb *redis.Client) *RedisCache {
	return &RedisCache{rdb: rdb}
}

func (c *RedisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

func (c *RedisCache) Del(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}

// Close is a no-op; the redis.Client is closed by infra.
func (c *RedisCache) Close() {}
```

- [ ] **Step 3: Create RedisLimiter**

Create `internal/ratelimit/redis.go`:

```go
package ratelimit

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// incrScript atomically increments a counter and sets its TTL on first
// increment. KEYS[1] = key, ARGV[1] = TTL in seconds.
var incrScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return count
`)

// RedisLimiter is a Limiter backed by Redis.
// Allow uses a sorted-set sliding window; IncrCounter uses an atomic Lua script.
type RedisLimiter struct {
	rdb *redis.Client
}

func NewRedisLimiter(rdb *redis.Client) *RedisLimiter {
	return &RedisLimiter{rdb: rdb}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	now := time.Now().UnixNano()
	cutoff := now - window.Nanoseconds()

	pipe := l.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff, 10))
	countCmd := pipe.ZCard(ctx, key)
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, key, window)

	if _, err := pipe.Exec(ctx); err != nil {
		return true // fail open on Redis error
	}
	return countCmd.Val() < int64(limit)
}

func (l *RedisLimiter) IncrCounter(ctx context.Context, key string, window time.Duration) (int64, error) {
	ttlSecs := int64(window.Seconds())
	return incrScript.Run(ctx, l.rdb, []string{key}, ttlSecs).Int64()
}

func (l *RedisLimiter) ResetCounter(ctx context.Context, key string) error {
	return l.rdb.Del(ctx, key).Err()
}

func (l *RedisLimiter) GetCounter(ctx context.Context, key string) (int64, error) {
	val, err := l.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}
```

- [ ] **Step 4: Verify it compiles**

```bash
go build ./internal/pubsub/... ./internal/store/... ./internal/ratelimit/...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/pubsub/redis.go internal/store/redis.go internal/ratelimit/redis.go
git commit -m "feat: add Redis implementations for Hub, Cache, and Limiter"
```

---

## Task 6: infra factory

**Files:**
- Create: `internal/infra/infra.go`

- [ ] **Step 1: Create the factory**

Create `internal/infra/infra.go`:

```go
package infra

import (
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/tabslate/server/internal/pubsub"
	"github.com/tabslate/server/internal/ratelimit"
	"github.com/tabslate/server/internal/store"
)

// Providers holds the three infrastructure providers wired by New.
type Providers struct {
	Hub     pubsub.Hub
	Cache   store.Cache
	Limiter ratelimit.Limiter
}

// New creates Providers from redisURL.
// If redisURL is empty all providers use in-memory implementations (OSS mode).
// The returned cleanup function must be called on process shutdown.
func New(redisURL string) (*Providers, func(), error) {
	if redisURL == "" {
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
	rdb := redis.NewClient(opt)
	hub := pubsub.NewRedisHub(rdb)
	cleanup := func() { hub.Close(); rdb.Close() }
	return &Providers{
		Hub:     hub,
		Cache:   store.NewRedisCache(rdb),
		Limiter: ratelimit.NewRedisLimiter(rdb),
	}, cleanup, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./internal/infra/...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/infra/
git commit -m "feat(infra): add factory that wires Hub, Cache, Limiter from REDIS_URL"
```

---

## Task 7: Wire SSEHandler and SyncHandler with Hub + Cache

**Files:**
- Delete: `internal/handler/sse_hub.go`
- Modify: `internal/handler/sse.go`
- Modify: `internal/handler/sync.go`

- [ ] **Step 1: Delete sse_hub.go**

```bash
rm internal/handler/sse_hub.go
git add internal/handler/sse_hub.go
```

The build will break until Steps 2 and 3 restore compilation — do not commit until Step 4.

- [ ] **Step 2: Update SSEHandler to accept Hub + Cache**

Replace the full contents of `internal/handler/sse.go`:

```go
package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/internal/pubsub"
	"github.com/tabslate/server/internal/store"
)

type SSEHandler struct {
	hub   pubsub.Hub
	cache store.Cache
}

func NewSSEHandler(hub pubsub.Hub, cache store.Cache) *SSEHandler {
	return &SSEHandler{hub: hub, cache: cache}
}

// GET /sync/stream?token=<sse_token>
// Streams server-sent events to the client. Auth via single-use SSE token
// stored in Cache (issued by POST /auth/sse-token). Keeps alive with ": ping"
// every 30 seconds.
func (h *SSEHandler) Stream(c *gin.Context) {
	ctx := c.Request.Context()
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing SSE token"})
		return
	}

	// Validate and atomically consume the single-use token from cache.
	val, found, err := h.cache.Get(ctx, "tabslate:sse_token:"+token)
	if err != nil || !found {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired SSE token"})
		return
	}
	userID := string(val)
	// Consume: delete so it cannot be reused.
	h.cache.Del(ctx, "tabslate:sse_token:"+token)

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	connID, ch := h.hub.Subscribe(userID)
	defer h.hub.Unsubscribe(userID, connID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case seq, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "data: {\"seq\":%d}\n\n", seq); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 3: Update SyncHandler to accept Hub**

At the top of `internal/handler/sync.go`, the `SyncHandler` struct and constructor need Hub. Replace only the struct, constructor, and the one `globalHub.Broadcast` call:

In `internal/handler/sync.go`, change:

```go
type SyncHandler struct {
	db     *db.DB
	search *search.Client
}

func NewSyncHandler(d *db.DB, sc *search.Client) *SyncHandler {
	return &SyncHandler{db: d, search: sc}
}
```

to:

```go
type SyncHandler struct {
	db     *db.DB
	search *search.Client
	hub    pubsub.Hub
}

func NewSyncHandler(d *db.DB, sc *search.Client, hub pubsub.Hub) *SyncHandler {
	return &SyncHandler{db: d, search: sc, hub: hub}
}
```

Add the import at the top of the file (merge into the existing `import` block):

```go
"github.com/tabslate/server/internal/pubsub"
```

Replace the `globalHub.Broadcast` call at line 180:

```go
globalHub.Broadcast(userID, seq)
```

with:

```go
h.hub.Broadcast(userID, seq)
```

- [ ] **Step 4: Verify the handler package compiles**

```bash
go build ./internal/handler/...
```

Expected: compile error about `handler.NewSSEHandler` and `handler.NewSyncHandler` call sites in `app/server.go` — that's expected; we fix those in Task 9. The `internal/handler` package itself must compile cleanly before continuing.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/sse.go internal/handler/sync.go
git commit -m "feat(handler): wire SSEHandler and SyncHandler with Hub and Cache interfaces"
```

---

## Task 8: Wire AuthHandler with Limiter + Cache

**Files:**
- Modify: `internal/handler/auth.go`

This task replaces all DB queries to `login_failures`, `otp_ip_requests`, `register_ip_requests`, and `sse_tokens` with Limiter and Cache calls. The `StartCleanup` method is removed.

- [ ] **Step 1: Add Limiter and Cache fields to AuthHandler**

In `internal/handler/auth.go`, change the `AuthHandler` struct and constructor:

```go
// Replace the existing AuthHandler struct definition with:
type AuthHandler struct {
	db      *db.DB
	secret  string
	billing billing.Provider
	captcha *captcha.Verifier
	mailer  *mailer.Mailer
	limiter ratelimit.Limiter
	cache   store.Cache

	registerCaptchaThreshold int
	registerCaptchaWindow    time.Duration

	otpCaptchaThreshold int
	otpCaptchaWindow    time.Duration
}
```

```go
// Replace the existing NewAuthHandler function with:
func NewAuthHandler(
	d *db.DB,
	secret string,
	bp billing.Provider,
	cv *captcha.Verifier,
	m *mailer.Mailer,
	l ratelimit.Limiter,
	cache store.Cache,
	registerThreshold int,
	registerWindow time.Duration,
	otpThreshold int,
	otpWindow time.Duration,
) *AuthHandler {
	return &AuthHandler{
		db:                       d,
		secret:                   secret,
		billing:                  bp,
		captcha:                  cv,
		mailer:                   m,
		limiter:                  l,
		cache:                    cache,
		registerCaptchaThreshold: registerThreshold,
		registerCaptchaWindow:    registerWindow,
		otpCaptchaThreshold:      otpThreshold,
		otpCaptchaWindow:         otpWindow,
	}
}
```

Add the two new imports to the `import` block in `auth.go`:

```go
"github.com/tabslate/server/internal/ratelimit"
"github.com/tabslate/server/internal/store"
```

Remove the `middleware` import if it only appeared for rate limiting (check the file — it's also used for `middleware.UserID(c)`, so keep it).

- [ ] **Step 2: Replace the four private helper methods**

Delete the following methods entirely from `auth.go`:
- `loginFailureCount`
- `recordLoginFailure`
- `otpIPRequestCount`
- `recordOTPIPRequest`
- `registerIPRequestCount`
- `recordRegisterIPRequest`
- `StartCleanup`
- `cleanupExpiredRateLimitRows`

Replace them with these four thin wrappers (add after the `NewAuthHandler` function):

```go
func (h *AuthHandler) loginFailureCount(ctx context.Context, email string) int {
	count, _ := h.limiter.GetCounter(ctx, "tabslate:auth:login_fail:"+email)
	return int(count)
}

func (h *AuthHandler) recordLoginFailure(ctx context.Context, email string) {
	h.limiter.IncrCounter(ctx, "tabslate:auth:login_fail:"+email, loginFailureWindow)
}

func (h *AuthHandler) otpIPRequestCount(ctx context.Context, ip string) int {
	count, _ := h.limiter.GetCounter(ctx, "tabslate:auth:otp_ip:"+ip)
	return int(count)
}

func (h *AuthHandler) recordOTPIPRequest(ctx context.Context, ip string) {
	h.limiter.IncrCounter(ctx, "tabslate:auth:otp_ip:"+ip, h.otpCaptchaWindow)
}

func (h *AuthHandler) registerIPRequestCount(ctx context.Context, ip string) int {
	count, _ := h.limiter.GetCounter(ctx, "tabslate:auth:reg_ip:"+ip)
	return int(count)
}

func (h *AuthHandler) recordRegisterIPRequest(ctx context.Context, ip string) {
	h.limiter.IncrCounter(ctx, "tabslate:auth:reg_ip:"+ip, h.registerCaptchaWindow)
}
```

- [ ] **Step 3: Replace the login success clear-failures call**

In `Login`, find:

```go
// Clear failures on success.
h.db.Exec(ctx, `DELETE FROM login_failures WHERE email = $1`, req.Email)
```

Replace with:

```go
h.limiter.ResetCounter(ctx, "tabslate:auth:login_fail:"+req.Email)
```

- [ ] **Step 4: Replace IssueSSEToken to use Cache**

Find the full `IssueSSEToken` method and replace it:

```go
// POST /auth/sse-token
// Issues a single-use, 30-second SSE authentication token stored in Cache.
func (h *AuthHandler) IssueSSEToken(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	token := uuid.NewString()

	if err := h.cache.Set(ctx, "tabslate:sse_token:"+token, []byte(userID), 30*time.Second); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue SSE token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}
```

- [ ] **Step 5: Verify auth.go compiles**

```bash
go build ./internal/handler/...
```

Expected: compile error about `app/server.go` call sites, but `internal/handler` itself should compile. Fix any errors within the package before continuing.

- [ ] **Step 6: Commit**

```bash
git add internal/handler/auth.go
git commit -m "feat(handler): replace DB rate-limit tables and SSE tokens with Limiter+Cache"
```

---

## Task 9: Wire middleware, BillingHandler, config, and app.New

**Files:**
- Modify: `internal/middleware/ratelimit.go`
- Modify: `internal/handler/billing.go`
- Modify: `app/config.go`
- Modify: `app/server.go`

- [ ] **Step 1: Refactor middleware/ratelimit.go**

Replace the full contents of `internal/middleware/ratelimit.go`:

```go
package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/internal/ratelimit"
)

// RateLimitByIP returns a Gin middleware that limits requests by client IP.
// limit and window configure the sliding-window threshold for this route group.
func RateLimitByIP(limiter ratelimit.Limiter, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "tabslate:ratelimit:" + c.FullPath() + ":" + c.ClientIP()
		if !limiter.Allow(c.Request.Context(), key, limit, window) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "too many requests, please try again later",
			})
			return
		}
		c.Next()
	}
}
```

- [ ] **Step 2: Add Cache to BillingHandler for limits caching**

Replace the full contents of `internal/handler/billing.go`:

```go
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/store"
)

// BillingHandler exposes plan, limits, checkout, and invoice endpoints.
type BillingHandler struct {
	billing billing.Provider
	cache   store.Cache
}

func NewBillingHandler(bp billing.Provider, cache store.Cache) *BillingHandler {
	return &BillingHandler{billing: bp, cache: cache}
}

// GET /api/subscription
func (h *BillingHandler) GetSubscription(c *gin.Context) {
	userID := middleware.UserID(c)
	sub, err := h.billing.GetSubscription(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sub)
}

// GET /api/limits — result cached for 60s per user.
func (h *BillingHandler) GetLimits(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	cacheKey := "tabslate:billing:limits:" + userID

	if raw, found, _ := h.cache.Get(ctx, cacheKey); found {
		c.Data(http.StatusOK, "application/json", raw)
		return
	}

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if raw, err := json.Marshal(limits); err == nil {
		h.cache.Set(ctx, cacheKey, raw, 60*time.Second)
	}
	c.JSON(http.StatusOK, limits)
}

// POST /api/checkout  body: {"plan_code": "pro"}
func (h *BillingHandler) CreateCheckout(c *gin.Context) {
	userID := middleware.UserID(c)
	var body struct {
		PlanCode string `json:"plan_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	url, err := h.billing.GetCheckoutURL(c.Request.Context(), userID, body.PlanCode)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// GET /api/invoices?page=1&per_page=20
func (h *BillingHandler) ListInvoices(c *gin.Context) {
	userID := middleware.UserID(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	invoices, err := h.billing.ListInvoices(c.Request.Context(), userID, page, perPage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, invoices)
}

// DELETE /api/subscription
func (h *BillingHandler) CancelSubscription(c *gin.Context) {
	userID := middleware.UserID(c)
	if err := h.billing.CancelSubscription(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
```

- [ ] **Step 3: Add RedisURL to Config**

In `app/config.go`, add one field to `Config` after `MeiliSearchAPIKey`:

```go
// RedisURL is the Redis connection URL (e.g. redis://localhost:6379).
// Leave empty to use in-memory implementations (single-instance OSS mode).
RedisURL string
```

Add one line to `LoadConfig()` return value (e.g. after `MeiliSearchAPIKey`):

```go
RedisURL: os.Getenv("REDIS_URL"),
```

- [ ] **Step 4: Wire infra inside app.New + update setupRoutes**

In `app/server.go`, add `infra *infra.Providers` and `infraCleanup func()` fields to the `Server` struct:

```go
type Server struct {
	cfg          *Config
	db           *db.DB
	billing      billing.Provider
	captcha      *captcha.Verifier
	mailer       *mailer.Mailer
	search       *search.Client
	infra        *infra.Providers
	infraCleanup func()
	router       *gin.Engine
	ctx          context.Context
}
```

In `New()`, after the `sc := search.New(...)` block and before `s := &Server{...}`, add:

```go
infraProviders, infraCleanup, err := infra.New(cfg.RedisURL)
if err != nil {
    log.Fatalf("infra: %v", err)
}
```

Add `infra` and `infraCleanup` to the `Server` literal:

```go
s := &Server{
    cfg:          cfg,
    db:           database,
    billing:      bp,
    captcha:      cv,
    mailer:       m,
    search:       sc,
    infra:        infraProviders,
    infraCleanup: infraCleanup,
    router:       gin.Default(),
    ctx:          ctx,
}
```

In `Run()`, call cleanup just before the shutdown log, after `<-s.ctx.Done()`:

```go
<-s.ctx.Done()
s.infraCleanup()
log.Println("shutting down...")
```

Add the infra import to `app/server.go`:

```go
"github.com/tabslate/server/internal/infra"
```

In `setupRoutes()`, update the five handler constructors and all `RateLimitByIP` calls:

```go
// Replace:
authH := handler.NewAuthHandler(s.db, s.cfg.JWTSecret, s.billing, s.captcha, s.mailer,
    s.cfg.RegisterCaptchaThreshold, s.cfg.RegisterCaptchaWindow,
    s.cfg.OTPCaptchaThreshold, s.cfg.OTPCaptchaWindow)
authH.StartCleanup(s.ctx)

// With:
authH := handler.NewAuthHandler(s.db, s.cfg.JWTSecret, s.billing, s.captcha, s.mailer,
    s.infra.Limiter, s.infra.Cache,
    s.cfg.RegisterCaptchaThreshold, s.cfg.RegisterCaptchaWindow,
    s.cfg.OTPCaptchaThreshold, s.cfg.OTPCaptchaWindow)
// (no StartCleanup call — TTL handles expiry)
```

```go
// Replace:
sseH := handler.NewSSEHandler(s.db)
// With:
sseH := handler.NewSSEHandler(s.infra.Hub, s.infra.Cache)
```

```go
// Replace:
syncH := handler.NewSyncHandler(s.db, s.search)
// With:
syncH := handler.NewSyncHandler(s.db, s.search, s.infra.Hub)
```

```go
// Replace:
billH := handler.NewBillingHandler(s.billing)
// With:
billH := handler.NewBillingHandler(s.billing, s.infra.Cache)
```

Replace the three `middleware.NewRateLimiter(...)` calls and all `middleware.RateLimitByIP(...)` calls. The rate limiters are no longer created separately — pass `s.infra.Limiter` directly to `RateLimitByIP`:

```go
// Delete these three lines:
authRL := middleware.NewRateLimiter(10, 1*time.Minute)
syncPushRL := middleware.NewRateLimiter(60, 1*time.Minute)
syncPullRL := middleware.NewRateLimiter(120, 1*time.Minute)

// Also delete the searchRL line inside the api group:
searchRL := middleware.NewRateLimiter(60, 1*time.Minute)
```

Update each `RateLimitByIP` call site (6 total):

```go
// auth routes (all 6 auth endpoints):
middleware.RateLimitByIP(s.infra.Limiter, 10, time.Minute)

// search route:
middleware.RateLimitByIP(s.infra.Limiter, 60, time.Minute)

// sync push:
middleware.RateLimitByIP(s.infra.Limiter, 60, time.Minute)

// sync pull:
middleware.RateLimitByIP(s.infra.Limiter, 120, time.Minute)
```

- [ ] **Step 5: Verify full build**

```bash
go build ./...
```

Expected: no errors. Fix any remaining compilation errors before committing.

- [ ] **Step 6: Commit**

```bash
git add internal/middleware/ratelimit.go internal/handler/billing.go app/config.go app/server.go
git commit -m "feat: wire infra providers into app.New, middleware, and billing handler"
```

---

## Task 10: Schema cleanup — drop the four migrated tables

**Files:**
- Modify: `db/schema.pg.sql`

- [ ] **Step 1: Append DROP TABLE migrations**

At the very end of `db/schema.pg.sql`, append:

```sql
-- ── Redis migration: drop tables now managed by Cache/Limiter ────────────────
DROP TABLE IF EXISTS sse_tokens;
DROP TABLE IF EXISTS login_failures;
DROP TABLE IF EXISTS otp_ip_requests;
DROP TABLE IF EXISTS register_ip_requests;
```

`DROP TABLE IF EXISTS` is idempotent — safe on fresh installs (tables were never created) and existing installs (tables exist and get removed).

- [ ] **Step 2: Verify the full build still passes**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```

Expected: all existing tests pass. The new unit tests from Tasks 2–4 should still pass.

- [ ] **Step 4: Commit**

```bash
git add db/schema.pg.sql
git commit -m "feat(schema): drop sse_tokens, login_failures, otp_ip_requests, register_ip_requests"
```

---

## Verification

After all tasks complete:

```bash
# Full build
go build ./...

# All tests
go test ./...

# Verify OSS mode (no REDIS_URL): start server and confirm no Redis errors
REDIS_URL="" go run ./cmd/server  # should start normally with in-memory providers

# Verify Redis mode (if Redis available):
REDIS_URL="redis://localhost:6379" go run ./cmd/server
```

For Cloud deployments: set `REDIS_URL` in the environment. No other config changes needed.
