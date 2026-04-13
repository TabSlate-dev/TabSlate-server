package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
)

type SyncHandler struct{ db *db.DB }

func NewSyncHandler(d *db.DB) *SyncHandler { return &SyncHandler{db: d} }

// GET /sync?since=<unix_timestamp>
func (h *SyncHandler) Pull(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	var since int64
	if s := c.Query("since"); s != "" {
		since = parseUnixParam(s)
	}

	resp := model.SyncResponse{ServerTime: time.Now().Unix()}

	if rows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, icon, color, position, created_at, updated_at
		 FROM workspaces WHERE user_id=$1 AND updated_at>$2`, userID, since); err == nil {
		defer rows.Close()
		for rows.Next() {
			var w model.Workspace
			rows.Scan(&w.ID, &w.UserID, &w.Name, &w.Icon, &w.Color, &w.Position, &w.CreatedAt, &w.UpdatedAt)
			resp.Workspaces = append(resp.Workspaces, w)
		}
	}

	if rows, err := h.db.Query(ctx,
		`SELECT id, user_id, workspace_id, name, icon, position, created_at, updated_at
		 FROM collections WHERE user_id=$1 AND updated_at>$2`, userID, since); err == nil {
		defer rows.Close()
		for rows.Next() {
			var col model.Collection
			rows.Scan(&col.ID, &col.UserID, &col.WorkspaceID, &col.Name, &col.Icon, &col.Position, &col.CreatedAt, &col.UpdatedAt)
			resp.Collections = append(resp.Collections, col)
		}
	}

	if rows, err := h.db.Query(ctx,
		`SELECT id, user_id, collection_id, title, url, favicon_url, description,
		        is_favorite, is_archived, is_trashed, position, created_at, updated_at
		 FROM bookmarks WHERE user_id=$1 AND updated_at>$2`, userID, since); err == nil {
		defer rows.Close()
		for rows.Next() {
			var b model.Bookmark
			rows.Scan(&b.ID, &b.UserID, &b.CollectionID, &b.Title, &b.URL,
				&b.FaviconURL, &b.Description, &b.IsFavorite, &b.IsArchived,
				&b.IsTrashed, &b.Position, &b.CreatedAt, &b.UpdatedAt)
			resp.Bookmarks = append(resp.Bookmarks, b)
		}
	}

	if rows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, color FROM tags WHERE user_id=$1`, userID); err == nil {
		defer rows.Close()
		for rows.Next() {
			var t model.Tag
			rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color)
			resp.Tags = append(resp.Tags, t)
		}
	}

	c.JSON(http.StatusOK, resp)
}

// POST /sync
func (h *SyncHandler) Push(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var payload model.SyncPush
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "transaction failed"})
		return
	}
	defer tx.Rollback(ctx)

	for _, w := range payload.Workspaces {
		if w.UserID != userID {
			continue
		}
		tx.Exec(ctx, `
			INSERT INTO workspaces (id, user_id, name, icon, color, position, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(id) DO UPDATE SET
			  name=excluded.name, icon=excluded.icon, color=excluded.color,
			  position=excluded.position, updated_at=excluded.updated_at
			WHERE excluded.updated_at > workspaces.updated_at`,
			w.ID, userID, w.Name, w.Icon, w.Color, w.Position, w.CreatedAt, w.UpdatedAt)
	}

	for _, col := range payload.Collections {
		if col.UserID != userID {
			continue
		}
		tx.Exec(ctx, `
			INSERT INTO collections (id, user_id, workspace_id, name, icon, position, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(id) DO UPDATE SET
			  workspace_id=excluded.workspace_id, name=excluded.name, icon=excluded.icon,
			  position=excluded.position, updated_at=excluded.updated_at
			WHERE excluded.updated_at > collections.updated_at`,
			col.ID, userID, col.WorkspaceID, col.Name, col.Icon, col.Position, col.CreatedAt, col.UpdatedAt)
	}

	for _, b := range payload.Bookmarks {
		if b.UserID != userID {
			continue
		}
		tx.Exec(ctx, `
			INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url,
			  description, is_favorite, is_archived, is_trashed, position, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT(id) DO UPDATE SET
			  collection_id=excluded.collection_id, title=excluded.title, url=excluded.url,
			  favicon_url=excluded.favicon_url, description=excluded.description,
			  is_favorite=excluded.is_favorite, is_archived=excluded.is_archived,
			  is_trashed=excluded.is_trashed, position=excluded.position,
			  updated_at=excluded.updated_at
			WHERE excluded.updated_at > bookmarks.updated_at`,
			b.ID, userID, b.CollectionID, b.Title, b.URL, b.FaviconURL,
			b.Description, b.IsFavorite, b.IsArchived, b.IsTrashed, b.Position, b.CreatedAt, b.UpdatedAt)
	}

	for _, t := range payload.Tags {
		if t.UserID != userID {
			continue
		}
		tx.Exec(ctx, `
			INSERT INTO tags (id, user_id, name, color) VALUES ($1,$2,$3,$4)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, color=excluded.color`,
			t.ID, userID, t.Name, t.Color)
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "server_time": time.Now().Unix()})
}

func parseUnixParam(s string) int64 {
	var v int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		v = v*10 + int64(ch-'0')
	}
	return v
}
