package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type BookmarkHandler struct {
	db      *db.DB
	search  *search.Client
	hub     pubsub.Hub
	billing billing.Provider
}

func NewBookmarkHandler(d *db.DB, sc *search.Client, hub pubsub.Hub, bp billing.Provider) *BookmarkHandler {
	return &BookmarkHandler{db: d, search: sc, hub: hub, billing: bp}
}

// GET /bookmarks?collection_id=&favorite=&archived=&trashed=
func (h *BookmarkHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	query := `SELECT b.id, b.user_id, b.collection_id, b.title, b.url, b.favicon_url, b.description,
	                 b.is_favorite, b.is_archived, b.is_trashed, b.position, b.seq, b.created_at, b.updated_at
	            FROM bookmarks b
	            JOIN collections c ON c.id = b.collection_id AND c.user_id = b.user_id
	            JOIN workspaces w ON w.id = c.workspace_id AND w.user_id = c.user_id
	            WHERE b.user_id=$1 AND b.deleted_at IS NULL
	              AND c.deleted_at IS NULL AND c.is_deleted=0 AND w.is_deleted=0`
	args := []any{userID}
	n := 2

	if cid := c.Query("collection_id"); cid != "" {
		query += fmt.Sprintf(" AND b.collection_id=$%d", n)
		args = append(args, cid)
		n++
	}
	if c.Query("favorite") == "true" {
		query += " AND b.is_favorite=true"
	}
	if c.Query("archived") == "true" {
		query += " AND b.is_archived=true"
	}
	if c.Query("trashed") == "true" {
		query += " AND b.is_trashed > 0"
	} else {
		query += " AND b.is_trashed = 0"
	}
	query += " ORDER BY b.position ASC"

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list bookmarks"})
		return
	}
	defer rows.Close()

	items := []model.Bookmark{}
	for rows.Next() {
		var b model.Bookmark
		rows.Scan(&b.ID, &b.UserID, &b.CollectionID, &b.Title, &b.URL,
			&b.FaviconURL, &b.Description, &b.IsFavorite, &b.IsArchived,
			&b.IsTrashed, &b.Position, &b.Seq, &b.CreatedAt, &b.UpdatedAt)
		items = append(items, b)
	}
	c.JSON(http.StatusOK, items)
}

// POST /bookmarks
func (h *BookmarkHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
		return
	}
	if limits.MaxBookmarks != -1 {
		var count int
		if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM bookmarks WHERE user_id = $1 AND is_trashed < 2`, userID).Scan(&count); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		if count >= limits.MaxBookmarks {
			c.JSON(http.StatusForbidden, gin.H{"error": "bookmark limit reached", "code": "quota_exceeded"})
			return
		}
	}

	var req model.BookmarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id := uuid.NewString()
	now := time.Now().UnixMilli()

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

	tag, err := tx.Exec(ctx,
		`INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url,
		  description, is_favorite, is_archived, is_trashed, position, seq, created_at, updated_at)
		 SELECT $1,$2,c.id,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13
		 FROM collections c
		 JOIN workspaces w ON w.id=c.workspace_id AND w.user_id=c.user_id
		 WHERE c.id=$3 AND c.user_id=$2 AND c.deleted_at IS NULL AND c.is_deleted=0 AND w.is_deleted=0`,
		id, userID, req.CollectionID, req.Title, req.URL, req.FaviconURL,
		req.Description, req.IsFavorite, req.IsArchived, boolToInt(req.IsTrashed), req.Position, seq, now,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create bookmark"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	if !req.IsTrashed {
		h.search.UpsertBookmark(search.BookmarkDoc{
			ID:           id,
			UserID:       userID,
			Title:        req.Title,
			URL:          req.URL,
			Description:  req.Description,
			CollectionID: derefStr(req.CollectionID),
			IsArchived:   req.IsArchived,
		})
	}
	c.JSON(http.StatusCreated, model.Bookmark{
		ID: id, UserID: userID, CollectionID: req.CollectionID,
		Title: req.Title, URL: req.URL, FaviconURL: req.FaviconURL,
		Description: req.Description, IsFavorite: req.IsFavorite,
		IsArchived: req.IsArchived, IsTrashed: boolToInt(req.IsTrashed),
		Position: req.Position, Seq: seq, CreatedAt: now, UpdatedAt: now,
	})
}

// PUT /bookmarks/:id
func (h *BookmarkHandler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.BookmarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().UnixMilli()

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

	tag, err := tx.Exec(ctx,
		`UPDATE bookmarks b
		 SET collection_id=$1, title=$2, url=$3, favicon_url=$4, description=$5,
		     is_favorite=$6, is_archived=$7, is_trashed=$8, position=$9, seq=$10, updated_at=$11
		 FROM collections current_c
		 JOIN workspaces current_w ON current_w.id=current_c.workspace_id AND current_w.user_id=current_c.user_id,
		 collections target_c
		 JOIN workspaces target_w ON target_w.id=target_c.workspace_id AND target_w.user_id=target_c.user_id
		 WHERE b.id=$12 AND b.user_id=$13 AND b.deleted_at IS NULL
		   AND current_c.id=b.collection_id AND current_c.user_id=b.user_id
		   AND current_c.deleted_at IS NULL AND current_c.is_deleted=0 AND current_w.is_deleted=0
		   AND target_c.id=$1 AND target_c.user_id=b.user_id
		   AND target_c.deleted_at IS NULL AND target_c.is_deleted=0 AND target_w.is_deleted=0`,
		req.CollectionID, req.Title, req.URL, req.FaviconURL, req.Description,
		req.IsFavorite, req.IsArchived, boolToInt(req.IsTrashed), req.Position, seq, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update bookmark"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	if req.IsTrashed {
		h.search.DeleteBookmark(id)
	} else {
		h.search.UpsertBookmark(search.BookmarkDoc{
			ID:           id,
			UserID:       userID,
			Title:        req.Title,
			URL:          req.URL,
			Description:  req.Description,
			CollectionID: derefStr(req.CollectionID),
			IsArchived:   req.IsArchived,
		})
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "seq": seq, "updated_at": now})
}

// DELETE /bookmarks/:id  →  soft-delete
func (h *BookmarkHandler) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")
	now := time.Now().UnixMilli()

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

	tag, err := tx.Exec(ctx,
		`UPDATE bookmarks b SET deleted_at=$1, seq=$2, updated_at=$1
		 FROM collections c
		 JOIN workspaces w ON w.id=c.workspace_id AND w.user_id=c.user_id
		 WHERE b.id=$3 AND b.user_id=$4 AND b.deleted_at IS NULL
		   AND c.id=b.collection_id AND c.user_id=b.user_id
		   AND c.deleted_at IS NULL AND c.is_deleted=0 AND w.is_deleted=0`,
		now, seq, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete bookmark"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	h.search.DeleteBookmark(id)
	c.Status(http.StatusNoContent)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
