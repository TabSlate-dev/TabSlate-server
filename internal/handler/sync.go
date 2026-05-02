package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
	"github.com/tabslate/server/internal/plan"
)

type SyncHandler struct{ db *db.DB }

func NewSyncHandler(d *db.DB) *SyncHandler { return &SyncHandler{db: d} }

// POST /sync/push
// Accepts client changes, applies LWW upserts, stamps with server seq.
func (h *SyncHandler) Push(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var req model.SyncPushRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Enforce entity count limit.
	total := len(req.Entities.Workspaces) + len(req.Entities.Collections) +
		len(req.Entities.Bookmarks) + len(req.Entities.Tags)
	if total > 1000 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "too many entities in one push (max 1000)"})
		return
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tx begin failed"})
		return
	}
	defer tx.Rollback(ctx)

	seq, err := incrementSeq(ctx, tx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seq increment failed"})
		return
	}

	now := time.Now().UnixMilli()
	var rejected []model.Rejected

	// ── Workspaces ────────────────────────────────────────────────────────────
	for _, ws := range req.Entities.Workspaces {
		tag, err := tx.Exec(ctx, `
			INSERT INTO workspaces (id, user_id, name, icon, color, position, seq, deleted_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT (id) DO UPDATE
			  SET name=$3, icon=$4, color=$5, position=$6, seq=$7, deleted_at=$8, updated_at=$9
			WHERE workspaces.user_id = $2 AND workspaces.updated_at < $9`,
			ws.ID, userID, ws.Name, ws.Icon, ws.Color, ws.Position, seq, ws.DeletedAt, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace upsert failed"})
			return
		}
		if tag.RowsAffected() == 0 {
			rejected = append(rejected, model.Rejected{ID: ws.ID, Reason: "stale"})
		}
	}

	// ── Collections ───────────────────────────────────────────────────────────
	for _, col := range req.Entities.Collections {
		if col.DeletedAt == nil {
			userPlan := plan.GetUserPlan(ctx, h.db, userID)
			limits := plan.Get(userPlan)
			if limits.MaxCollections != -1 {
				var count int
				if err := tx.QueryRow(ctx,
					`SELECT COUNT(*) FROM collections WHERE user_id = $1 AND deleted_at IS NULL`,
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
		tag, err := tx.Exec(ctx, `
			INSERT INTO collections (id, user_id, workspace_id, name, icon, position, seq, deleted_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
			ON CONFLICT (id) DO UPDATE
			  SET workspace_id=$3, name=$4, icon=$5, position=$6, seq=$7, deleted_at=$8, updated_at=$9
			WHERE collections.user_id = $2 AND collections.updated_at < $9`,
			col.ID, userID, col.WorkspaceID, col.Name, col.Icon, col.Position, seq, col.DeletedAt, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "collection upsert failed"})
			return
		}
		if tag.RowsAffected() == 0 {
			rejected = append(rejected, model.Rejected{ID: col.ID, Reason: "stale"})
		}
	}

	// ── Bookmarks ─────────────────────────────────────────────────────────────
	for _, bm := range req.Entities.Bookmarks {
		tag, err := tx.Exec(ctx, `
			INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url, description,
			                       is_favorite, is_archived, is_trashed, position, seq, deleted_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
			ON CONFLICT (id) DO UPDATE
			  SET collection_id=$3, title=$4, url=$5, favicon_url=$6, description=$7,
			      is_favorite=$8, is_archived=$9, is_trashed=$10, position=$11, seq=$12, deleted_at=$13, updated_at=$14
			WHERE bookmarks.user_id = $2 AND bookmarks.updated_at < $14`,
			bm.ID, userID, bm.CollectionID, bm.Title, bm.URL, bm.FaviconURL, bm.Description,
			bm.IsFavorite, bm.IsArchived, bm.IsTrashed, bm.Position, seq, bm.DeletedAt, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "bookmark upsert failed"})
			return
		}
		if tag.RowsAffected() == 0 {
			rejected = append(rejected, model.Rejected{ID: bm.ID, Reason: "stale"})
		}
	}

	// ── Tags ──────────────────────────────────────────────────────────────────
	for _, t := range req.Entities.Tags {
		res, err := tx.Exec(ctx, `
			INSERT INTO tags (id, user_id, name, color, seq, deleted_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (id) DO UPDATE
			  SET name=$3, color=$4, seq=$5, deleted_at=$6, updated_at=$7
			WHERE tags.user_id = $2 AND tags.updated_at < $7`,
			t.ID, userID, t.Name, t.Color, seq, t.DeletedAt, now)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tag upsert failed"})
			return
		}
		if res.RowsAffected() == 0 {
			rejected = append(rejected, model.Rejected{ID: t.ID, Reason: "stale"})
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	globalHub.Broadcast(userID, seq)

	if rejected == nil {
		rejected = []model.Rejected{}
	}
	c.JSON(http.StatusOK, model.SyncPushResponse{ServerSeq: seq, Rejected: rejected})
}

// Pull is a placeholder — full implementation added in Task 7.
func (h *SyncHandler) Pull(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "not implemented"})
}
