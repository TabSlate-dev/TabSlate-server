# Quota Enforcement & Plan Endpoint Design

**Date:** 2026-05-14  
**Status:** Approved

---

## Overview

Two goals:

1. **Migrate write-path quota enforcement** from the legacy `internal/plan` package (reads the `subscriptions` DB table, unmaintained in Cloud) to `billing.Provider.GetLimits()` (authoritative for both OSS and Cloud).
2. **Add `GET /api/plan`** — a combined endpoint that returns subscription info, plan limits, and current usage counts, for the frontend to render quota indicators.

---

## Background

The `internal/plan` package derives the user's plan by querying the `subscriptions` table and maps it to hardcoded limits. In the Cloud edition this table is never populated; all Cloud users therefore silently receive free-tier limits regardless of their actual subscription. Meanwhile `billing.Provider.GetLimits()` is the correct, cached, Meteroid-backed source of truth that is already used by the existing `GET /api/limits` endpoint.

---

## Part 1: Write-Path Migration

### Affected handlers

| Handler | Current call | Replacement |
|---|---|---|
| `WorkspacesHandler.Create` | `plan.CheckWorkspace(ctx, h.db, userID)` | `h.billing.GetLimits()` + COUNT |
| `BookmarksHandler.Create` | `plan.CheckBookmark(ctx, h.db, userID)` | `h.billing.GetLimits()` + COUNT |
| `CollectionsHandler.Create` | `plan.CheckCollection(ctx, h.db, userID)` | `h.billing.GetLimits()` + COUNT |
| `TagsHandler.Create` | `plan.CheckTag(ctx, h.db, userID)` | `h.billing.GetLimits()` + COUNT |
| `SyncHandler.Push` | `plan.GetUserPlan` + `plan.Get` | `h.billing.GetLimits()` once before loop |

### Handler struct changes

Add `billing billing.Provider` field and constructor parameter to each of the five handlers above. `app/server.go` passes the already-available `s.billing` to each updated constructor.

### Quota check pattern (REST handlers)

```go
limits, err := h.billing.GetLimits(c.Request.Context(), userID)
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
    return
}
if limits.MaxBookmarks != -1 {
    var count int
    h.db.QueryRow(ctx, h.db.Rebind(
        `SELECT COUNT(*) FROM bookmarks WHERE user_id = ? AND is_trashed = 0`,
    ), userID).Scan(&count)
    if count >= limits.MaxBookmarks {
        c.JSON(http.StatusForbidden, gin.H{"error": "bookmark limit reached", "code": "quota_exceeded"})
        return
    }
}
```

Error response uses `403 Forbidden` with `"code": "quota_exceeded"` — consistent with the sync Push rejection reason already used (`"quota_exceeded"`).

### Sync Push special case

`GetLimits()` is called **once before the entity loops** to avoid repeated Meteroid cache lookups per entity. The collections COUNT query stays inside the transaction (preventing TOCTOU). SavedGroups quota check is also added to the sync Push groups loop (same pattern as collections: COUNT inside tx, skip upsert on quota_exceeded). Bookmark and workspace quota checks are not added to sync Push (the REST create paths already guard these; the sync path is used for cross-device delta sync where client-enforced limits apply).

Groups have no dedicated REST create endpoint — they are exclusively managed via sync Push. Therefore the SavedGroups quota check lives only in `SyncHandler.Push`.

### COUNT query conditions per resource

| Resource | Table | WHERE condition |
|---|---|---|
| Workspaces | `workspaces` | `user_id = ?` |
| Bookmarks | `bookmarks` | `user_id = ? AND is_trashed = 0` |
| Collections | `collections` | `user_id = ? AND is_deleted = 0` |
| Tags | `tags` | `user_id = ?` |
| Saved Groups | `groups` | `user_id = ? AND deleted_at IS NULL` |

### Cleanup

Delete `internal/plan/plan.go` (entire package) after all references are replaced.

---

## Part 2: `GET /api/plan` Endpoint

### Route

```
GET /api/plan
Authorization: Bearer <access_token>
```

Registered in the existing `bill` group in `app/server.go`, alongside `/api/subscription`, `/api/limits`, etc.

### Response shape

```json
{
  "subscription": {
    "plan": "free",
    "status": "active",
    "expires_at": null
  },
  "limits": {
    "max_workspaces": 1,
    "max_bookmarks": 3000,
    "max_collections": -1,
    "max_tags": 1000,
    "max_saved_groups": 10,
    "trash_grace_days": 7
  },
  "usage": {
    "workspaces": 1,
    "bookmarks": 245,
    "collections": 8,
    "tags": 12,
    "saved_groups": 3
  }
}
```

### Implementation

Added as `GetPlan` method on the existing `BillingHandler`.

1. Call `GetSubscription()` and `GetLimits()` — both are cached, typically sub-millisecond.
2. Run 5 COUNT queries sequentially against the handler's DB.
3. Assemble and return the response.

No additional caching on usage counts: `GetLimits()` is already cached 60 s; usage numbers change with every write and caching them would give stale UI.

### Usage counting rules

| Field | Table | Condition | Notes |
|---|---|---|---|
| `workspaces` | `workspaces` | `user_id = ?` | no soft-delete field |
| `bookmarks` | `bookmarks` | `user_id = ? AND is_trashed = 0` | archived counts, trashed does not |
| `collections` | `collections` | `user_id = ? AND is_deleted = 0` | archived (archived_at set, is_deleted=0) counts, trashed (is_deleted≥1) does not |
| `tags` | `tags` | `user_id = ?` | no soft-delete field |
| `saved_groups` | `groups` | `user_id = ? AND deleted_at IS NULL` | soft-deleted groups excluded |

### Response type definition

Defined inline in `handler/billing.go` (anonymous struct or named type — no new model file needed given the type is only used by one handler method):

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

---

## Scope

| In scope | Out of scope |
|---|---|
| Migrate 5 handler quota checks to `billing.Provider` | Changing the `billing.Provider` interface |
| Add saved-groups quota check to sync Push | Caching usage counts |
| Add `GET /api/plan` | Frontend implementation |
| Delete `internal/plan` package | Sync Push bookmark/workspace quota checks |
| Update `app/server.go` constructor calls | Invoice / checkout endpoints |

---

## Files Changed

**TabSlate-server:**

- `internal/handler/workspaces.go` — add billing field, replace quota check
- `internal/handler/bookmarks.go` — add billing field, replace quota check
- `internal/handler/collections.go` — add billing field, replace quota check
- `internal/handler/tags.go` — add billing field, replace quota check
- `internal/handler/sync.go` — add billing field, replace plan lookup before loop, add saved_groups quota check in groups loop
- `internal/handler/billing.go` — add `GetPlan` handler + response types
- `app/server.go` — pass `s.billing` to updated constructors, register `/api/plan` route
- `internal/plan/plan.go` — **delete**
