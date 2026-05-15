# Quota Enforcement & Plan Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the legacy `internal/plan` quota checks with `billing.Provider.GetLimits()` across all write paths, add a `SavedGroups` check to sync Push, and expose a new `GET /api/plan` endpoint that returns subscription + limits + current usage counts.

**Architecture:** Each handler that performs quota enforcement gets a `billing billing.Provider` field injected via its constructor. `GetLimits()` is called once per request before the quota-sensitive operation; the handler then runs a focused COUNT query against its own DB tables. The new `GetPlan` method is added to the existing `BillingHandler` and runs subscription + limits fetches (both cached) followed by 5 COUNT queries.

**Tech Stack:** Go 1.22+, Gin, pgx/v5, `billing.Provider` interface (`github.com/tabslate/server/billing`)

---

## File Map

| File | Change |
|---|---|
| `internal/handler/workspaces.go` | Add `billing` field; replace `plan.CheckWorkspace` |
| `internal/handler/bookmarks.go` | Add `billing` field; replace `plan.CheckBookmark` |
| `internal/handler/collections.go` | Add `billing` field; replace `plan.CheckCollection` |
| `internal/handler/tags.go` | Add `billing` field; replace `plan.CheckTag` |
| `internal/handler/sync.go` | Add `billing` field; fetch limits once before loops; add SavedGroups check in groups loop |
| `internal/handler/billing.go` | Add `planResponse`/`planUsage` types and `GetPlan` handler |
| `app/server.go` | Pass `s.billing` to updated constructors; register `GET /api/plan` |
| `internal/plan/plan.go` | **Delete** |

---

## Task 1: Migrate WorkspaceHandler

**Files:**
- Modify: `internal/handler/workspaces.go`

- [ ] **Step 1: Add `billing` field and update constructor**

In `internal/handler/workspaces.go`, replace the struct and constructor:

```go
// Before
type WorkspaceHandler struct {
	db  *db.DB
	hub pubsub.Hub
}

func NewWorkspaceHandler(d *db.DB, hub pubsub.Hub) *WorkspaceHandler {
	return &WorkspaceHandler{db: d, hub: hub}
}
```

```go
// After
type WorkspaceHandler struct {
	db      *db.DB
	hub     pubsub.Hub
	billing billing.Provider
}

func NewWorkspaceHandler(d *db.DB, hub pubsub.Hub, bp billing.Provider) *WorkspaceHandler {
	return &WorkspaceHandler{db: d, hub: hub, billing: bp}
}
```

Add import `"github.com/tabslate/server/billing"` and remove `"github.com/tabslate/server/internal/plan"`.

- [ ] **Step 2: Replace quota check in `Create`**

In `WorkspaceHandler.Create`, replace:

```go
if err := plan.CheckWorkspace(ctx, h.db, userID); err != nil {
    c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
    return
}
```

with:

```go
limits, err := h.billing.GetLimits(ctx, userID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
    return
}
if limits.MaxWorkspaces != -1 {
    var count int
    h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM workspaces WHERE user_id = ?`), userID).Scan(&count)
    if count >= limits.MaxWorkspaces {
        c.JSON(http.StatusForbidden, gin.H{"error": "workspace limit reached", "code": "quota_exceeded"})
        return
    }
}
```

- [ ] **Step 3: Verify compile**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./internal/handler/
```

Expected: no output (clean build). If `plan` import error appears, it means the import was not removed — fix it.

- [ ] **Step 4: Commit**

```bash
git add internal/handler/workspaces.go
git commit -m "feat: migrate WorkspaceHandler quota check to billing.Provider"
```

---

## Task 2: Migrate BookmarkHandler

**Files:**
- Modify: `internal/handler/bookmarks.go`

- [ ] **Step 1: Add `billing` field and update constructor**

```go
// Before
type BookmarkHandler struct {
	db     *db.DB
	search *search.Client
	hub    pubsub.Hub
}

func NewBookmarkHandler(d *db.DB, sc *search.Client, hub pubsub.Hub) *BookmarkHandler {
	return &BookmarkHandler{db: d, search: sc, hub: hub}
}
```

```go
// After
type BookmarkHandler struct {
	db      *db.DB
	search  *search.Client
	hub     pubsub.Hub
	billing billing.Provider
}

func NewBookmarkHandler(d *db.DB, sc *search.Client, hub pubsub.Hub, bp billing.Provider) *BookmarkHandler {
	return &BookmarkHandler{db: d, search: sc, hub: hub, billing: bp}
}
```

Add import `"github.com/tabslate/server/billing"` and remove `"github.com/tabslate/server/internal/plan"`.

- [ ] **Step 2: Replace quota check in `Create`**

In `BookmarkHandler.Create`, replace:

```go
if err := plan.CheckBookmark(ctx, h.db, userID); err != nil {
    c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
    return
}
```

with:

```go
limits, err := h.billing.GetLimits(ctx, userID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
    return
}
if limits.MaxBookmarks != -1 {
    var count int
    h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM bookmarks WHERE user_id = ? AND is_trashed = 0`), userID).Scan(&count)
    if count >= limits.MaxBookmarks {
        c.JSON(http.StatusForbidden, gin.H{"error": "bookmark limit reached", "code": "quota_exceeded"})
        return
    }
}
```

- [ ] **Step 3: Verify compile**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./internal/handler/
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/handler/bookmarks.go
git commit -m "feat: migrate BookmarkHandler quota check to billing.Provider"
```

---

## Task 3: Migrate CollectionHandler

**Files:**
- Modify: `internal/handler/collections.go`

- [ ] **Step 1: Add `billing` field and update constructor**

```go
// Before
type CollectionHandler struct {
	db  *db.DB
	hub pubsub.Hub
}

func NewCollectionHandler(d *db.DB, hub pubsub.Hub) *CollectionHandler {
	return &CollectionHandler{db: d, hub: hub}
}
```

```go
// After
type CollectionHandler struct {
	db      *db.DB
	hub     pubsub.Hub
	billing billing.Provider
}

func NewCollectionHandler(d *db.DB, hub pubsub.Hub, bp billing.Provider) *CollectionHandler {
	return &CollectionHandler{db: d, hub: hub, billing: bp}
}
```

Add import `"github.com/tabslate/server/billing"` and remove `"github.com/tabslate/server/internal/plan"`.

- [ ] **Step 2: Replace quota check in `Create`**

In `CollectionHandler.Create`, replace:

```go
if err := plan.CheckCollection(ctx, h.db, userID); err != nil {
    c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
    return
}
```

with:

```go
limits, err := h.billing.GetLimits(ctx, userID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
    return
}
if limits.MaxCollections != -1 {
    var count int
    h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM collections WHERE user_id = ? AND is_deleted = 0`), userID).Scan(&count)
    if count >= limits.MaxCollections {
        c.JSON(http.StatusForbidden, gin.H{"error": "collection limit reached", "code": "quota_exceeded"})
        return
    }
}
```

- [ ] **Step 3: Verify compile**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./internal/handler/
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/handler/collections.go
git commit -m "feat: migrate CollectionHandler quota check to billing.Provider"
```

---

## Task 4: Migrate TagHandler

**Files:**
- Modify: `internal/handler/tags.go`

- [ ] **Step 1: Add `billing` field and update constructor**

```go
// Before
type TagHandler struct {
	db  *db.DB
	hub pubsub.Hub
}

func NewTagHandler(d *db.DB, hub pubsub.Hub) *TagHandler {
	return &TagHandler{db: d, hub: hub}
}
```

```go
// After
type TagHandler struct {
	db      *db.DB
	hub     pubsub.Hub
	billing billing.Provider
}

func NewTagHandler(d *db.DB, hub pubsub.Hub, bp billing.Provider) *TagHandler {
	return &TagHandler{db: d, hub: hub, billing: bp}
}
```

Add import `"github.com/tabslate/server/billing"` and remove `"github.com/tabslate/server/internal/plan"`.

- [ ] **Step 2: Replace quota check in `Create`**

In `TagHandler.Create`, replace:

```go
if err := plan.CheckTag(ctx, h.db, userID); err != nil {
    c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
    return
}
```

with:

```go
limits, err := h.billing.GetLimits(ctx, userID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
    return
}
if limits.MaxTags != -1 {
    var count int
    h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM tags WHERE user_id = ?`), userID).Scan(&count)
    if count >= limits.MaxTags {
        c.JSON(http.StatusForbidden, gin.H{"error": "tag limit reached", "code": "quota_exceeded"})
        return
    }
}
```

- [ ] **Step 3: Verify compile**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./internal/handler/
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/handler/tags.go
git commit -m "feat: migrate TagHandler quota check to billing.Provider"
```

---

## Task 5: Migrate SyncHandler — fetch limits + add SavedGroups check

**Files:**
- Modify: `internal/handler/sync.go`

- [ ] **Step 1: Add `billing` field and update constructor**

In `internal/handler/sync.go`, replace:

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

with:

```go
type SyncHandler struct {
	db      *db.DB
	search  *search.Client
	hub     pubsub.Hub
	billing billing.Provider
}

func NewSyncHandler(d *db.DB, sc *search.Client, hub pubsub.Hub, bp billing.Provider) *SyncHandler {
	return &SyncHandler{db: d, search: sc, hub: hub, billing: bp}
}
```

Add import `"github.com/tabslate/server/billing"`.

- [ ] **Step 2: Fetch limits once before entity loops**

In `SyncHandler.Push`, find the line after the entity-count limit check (after the `if total > 1000` block) and before the `tx, err := h.db.Begin(ctx)` call. Insert:

```go
limits, err := h.billing.GetLimits(ctx, userID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
    return
}
```

- [ ] **Step 3: Replace the collections plan lookup inside the loop**

Find the existing quota check inside the Collections loop in `Push` (lines ~83-99 of the original file):

```go
if col.DeletedAt == nil {
    userPlan := plan.GetUserPlan(ctx, h.db, userID)
    limits := plan.Get(userPlan)
    if limits.MaxCollections != -1 {
        var count int
        if err := tx.QueryRow(ctx,
            `SELECT COUNT(*) FROM collections WHERE user_id = $1 AND is_deleted = 0`,
            userID,
        ).Scan(&count); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
            return
        }
        if count >= limits.MaxCollections {
            rejected = append(rejected, model.Rejected{ID: col.ID, Reason: "quota_exceeded"})
            continue
        }
    }
}
```

Replace with (uses the `limits` fetched in Step 2; COUNT stays inside tx for TOCTOU safety):

```go
if col.DeletedAt == nil && limits.MaxCollections != -1 {
    var count int
    if err := tx.QueryRow(ctx,
        `SELECT COUNT(*) FROM collections WHERE user_id = $1 AND is_deleted = 0`,
        userID,
    ).Scan(&count); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
        return
    }
    if count >= limits.MaxCollections {
        rejected = append(rejected, model.Rejected{ID: col.ID, Reason: "quota_exceeded"})
        continue
    }
}
```

Remove `"github.com/tabslate/server/internal/plan"` from imports.

- [ ] **Step 4: Add SavedGroups quota check to the Groups loop**

Find the Groups loop in `Push` (the `for _, g := range req.Entities.Groups {` block). At the top of the loop body, before the `tx.Exec` INSERT/UPDATE, add:

```go
if g.DeletedAt == nil && limits.MaxSavedGroups != -1 {
    var count int
    if err := tx.QueryRow(ctx,
        `SELECT COUNT(*) FROM groups WHERE user_id = $1 AND deleted_at IS NULL`,
        userID,
    ).Scan(&count); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
        return
    }
    if count >= limits.MaxSavedGroups {
        rejected = append(rejected, model.Rejected{ID: g.ID, Reason: "quota_exceeded"})
        continue
    }
}
```

- [ ] **Step 5: Verify compile**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./internal/handler/
```

Expected: no output. If `plan` import error: check it was removed from the import block.

- [ ] **Step 6: Commit**

```bash
git add internal/handler/sync.go
git commit -m "feat: migrate SyncHandler to billing.Provider, add SavedGroups quota check"
```

---

## Task 6: Update `app/server.go` constructors

**Files:**
- Modify: `app/server.go`

- [ ] **Step 1: Pass `s.billing` to updated constructors**

In `app/server.go`, `setupRoutes()`, replace the five constructor calls:

```go
// Before
wsH := handler.NewWorkspaceHandler(s.db, s.infra.Hub)
colH := handler.NewCollectionHandler(s.db, s.infra.Hub)
bmH := handler.NewBookmarkHandler(s.db, s.search, s.infra.Hub)
tagH := handler.NewTagHandler(s.db, s.infra.Hub)
syncH := handler.NewSyncHandler(s.db, s.search, s.infra.Hub)
```

```go
// After
wsH := handler.NewWorkspaceHandler(s.db, s.infra.Hub, s.billing)
colH := handler.NewCollectionHandler(s.db, s.infra.Hub, s.billing)
bmH := handler.NewBookmarkHandler(s.db, s.search, s.infra.Hub, s.billing)
tagH := handler.NewTagHandler(s.db, s.infra.Hub, s.billing)
syncH := handler.NewSyncHandler(s.db, s.search, s.infra.Hub, s.billing)
```

- [ ] **Step 2: Verify full build**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./...
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add app/server.go
git commit -m "chore: wire billing.Provider into handler constructors"
```

---

## Task 7: Delete `internal/plan` package

**Files:**
- Delete: `internal/plan/plan.go`

- [ ] **Step 1: Confirm no remaining references**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && grep -r "internal/plan" --include="*.go" .
```

Expected: no output. If any file still imports the package, fix it before proceeding.

- [ ] **Step 2: Delete the file**

```bash
rm /Users/lieutenant/Documents/github/TabSlate-server/internal/plan/plan.go
```

- [ ] **Step 3: Verify full build**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "chore: delete internal/plan package (replaced by billing.Provider)"
```

---

## Task 8: Add `GET /api/plan` endpoint

**Files:**
- Modify: `internal/handler/billing.go`
- Modify: `app/server.go`

- [ ] **Step 1: Add response types to `billing.go`**

At the top of `internal/handler/billing.go` (after the `import` block, before `BillingHandler`), add:

```go
type planResponse struct {
	Subscription *billing.Subscription `json:"subscription"`
	Limits       *billing.Limits       `json:"limits"`
	Usage        planUsage             `json:"usage"`
}

type planUsage struct {
	Workspaces  int `json:"workspaces"`
	Bookmarks   int `json:"bookmarks"`
	Collections int `json:"collections"`
	Tags        int `json:"tags"`
	SavedGroups int `json:"saved_groups"`
}
```

- [ ] **Step 2: Add `GetPlan` method to `BillingHandler`**

`BillingHandler` already has a `billing billing.Provider` field and a `cache store.Cache` field. Add a `db *db.DB` field so it can run COUNT queries. Update the struct and constructor:

```go
// Before
type BillingHandler struct {
	billing billing.Provider
	cache   store.Cache
}

func NewBillingHandler(bp billing.Provider, cache store.Cache) *BillingHandler {
	return &BillingHandler{billing: bp, cache: cache}
}
```

```go
// After
type BillingHandler struct {
	billing billing.Provider
	cache   store.Cache
	db      *db.DB
}

func NewBillingHandler(bp billing.Provider, cache store.Cache, d *db.DB) *BillingHandler {
	return &BillingHandler{billing: bp, cache: cache, db: d}
}
```

Add import `"github.com/tabslate/server/db"`.

- [ ] **Step 3: Implement `GetPlan`**

Add this method to `BillingHandler` in `internal/handler/billing.go`:

```go
// GET /api/plan
func (h *BillingHandler) GetPlan(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	sub, err := h.billing.GetSubscription(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch subscription"})
		return
	}

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch limits"})
		return
	}

	var usage planUsage
	h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM workspaces WHERE user_id = ?`), userID).Scan(&usage.Workspaces)
	h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM bookmarks WHERE user_id = ? AND is_trashed = 0`), userID).Scan(&usage.Bookmarks)
	h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM collections WHERE user_id = ? AND is_deleted = 0`), userID).Scan(&usage.Collections)
	h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM tags WHERE user_id = ?`), userID).Scan(&usage.Tags)
	h.db.QueryRow(ctx, h.db.Rebind(`SELECT COUNT(*) FROM groups WHERE user_id = ? AND deleted_at IS NULL`), userID).Scan(&usage.SavedGroups)

	c.JSON(http.StatusOK, planResponse{
		Subscription: sub,
		Limits:       limits,
		Usage:        usage,
	})
}
```

- [ ] **Step 4: Update `NewBillingHandler` call in `app/server.go`**

In `setupRoutes()`, replace:

```go
billH := handler.NewBillingHandler(s.billing, s.infra.Cache)
```

with:

```go
billH := handler.NewBillingHandler(s.billing, s.infra.Cache, s.db)
```

- [ ] **Step 5: Register the new route**

In `setupRoutes()`, in the `bill` group, add:

```go
bill.GET("/plan", billH.GetPlan)
```

The block should look like:

```go
bill := api.Group("/api")
{
    bill.GET("/subscription", billH.GetSubscription)
    bill.GET("/limits", billH.GetLimits)
    bill.GET("/plan", billH.GetPlan)
    bill.POST("/checkout", billH.CreateCheckout)
    bill.GET("/invoices", billH.ListInvoices)
    bill.DELETE("/subscription", billH.CancelSubscription)
}
```

- [ ] **Step 6: Verify full build**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./...
```

Expected: no output.

- [ ] **Step 7: Manual smoke test**

Start the server locally and call the endpoint with a valid JWT:

```bash
curl -s -H "Authorization: Bearer <token>" http://localhost:8080/api/plan | jq .
```

Expected shape:
```json
{
  "subscription": { "plan": "free", "status": "active", "expires_at": null },
  "limits": { "max_workspaces": -1, "max_bookmarks": -1, "max_collections": -1, "max_tags": -1, "max_saved_groups": -1, "trash_grace_days": -1 },
  "usage": { "workspaces": 1, "bookmarks": 0, "collections": 0, "tags": 0, "saved_groups": 0 }
}
```

(OSS dev mode: all limits are -1 because `subscription_capacity` seeds the `unlimited` row.)

- [ ] **Step 8: Commit**

```bash
git add internal/handler/billing.go app/server.go
git commit -m "feat: add GET /api/plan endpoint with subscription, limits, and usage"
```

---

## Task 9: Final verification

- [ ] **Step 1: Full build and vet**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-server && go build ./... && go vet ./...
```

Expected: no output for both commands.

- [ ] **Step 2: Verify `internal/plan` is gone**

```bash
ls /Users/lieutenant/Documents/github/TabSlate-server/internal/plan/ 2>&1
```

Expected: `ls: ... No such file or directory`

- [ ] **Step 3: Verify no stale plan import**

```bash
grep -r '"github.com/tabslate/server/internal/plan"' /Users/lieutenant/Documents/github/TabSlate-server --include="*.go"
```

Expected: no output.

- [ ] **Step 4: Verify Cloud repo still builds**

```bash
cd /Users/lieutenant/Documents/github/TabSlate-Cloud && go build ./...
```

Expected: no output. The Cloud repo uses `NewBillingHandler` indirectly via `app.New` — since `NewBillingHandler` is called in `app/server.go` (not by Cloud directly), this should be transparent. If a build error appears, check if Cloud imports any handler constructors directly.
