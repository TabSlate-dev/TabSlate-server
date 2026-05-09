package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
	"github.com/tabslate/server/internal/plan"
	"github.com/tabslate/server/internal/pubsub"
	"github.com/tabslate/server/internal/search"
)

type BookmarkHandler struct {
	db     *db.DB
	search *search.Client
	hub    pubsub.Hub
}

func NewBookmarkHandler(d *db.DB, sc *search.Client, hub pubsub.Hub) *BookmarkHandler {
	return &BookmarkHandler{db: d, search: sc, hub: hub}
}

// GET /bookmarks?collection_id=&favorite=&archived=&trashed=
func (h *BookmarkHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	query := `SELECT id, user_id, collection_id, title, url, favicon_url, description,
	                 is_favorite, is_archived, is_trashed, position, seq, created_at, updated_at
	            FROM bookmarks WHERE user_id=$1 AND deleted_at IS NULL`
	args := []any{userID}
	n := 2

	if cid := c.Query("collection_id"); cid != "" {
		query += fmt.Sprintf(" AND collection_id=$%d", n)
		args = append(args, cid)
		n++
	}
	if c.Query("favorite") == "true" {
		query += " AND is_favorite=true"
	}
	if c.Query("archived") == "true" {
		query += " AND is_archived=true"
	}
	if c.Query("trashed") == "true" {
		query += " AND is_trashed=true"
	} else {
		query += " AND is_trashed=false"
	}
	query += " ORDER BY position ASC"

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

	if err := plan.CheckBookmark(ctx, h.db, userID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
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

	if _, err := tx.Exec(ctx,
		`INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url,
		  description, is_favorite, is_archived, is_trashed, position, seq, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)`,
		id, userID, req.CollectionID, req.Title, req.URL, req.FaviconURL,
		req.Description, req.IsFavorite, req.IsArchived, req.IsTrashed, req.Position, seq, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create bookmark"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	h.search.UpsertBookmark(search.BookmarkDoc{
		ID:           id,
		UserID:       userID,
		Title:        req.Title,
		URL:          req.URL,
		Description:  req.Description,
		CollectionID: derefStr(req.CollectionID),
		IsArchived:   req.IsArchived,
	})
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
		`UPDATE bookmarks SET collection_id=$1, title=$2, url=$3, favicon_url=$4, description=$5,
		  is_favorite=$6, is_archived=$7, is_trashed=$8, position=$9, seq=$10, updated_at=$11
		  WHERE id=$12 AND user_id=$13 AND deleted_at IS NULL`,
		req.CollectionID, req.Title, req.URL, req.FaviconURL, req.Description,
		req.IsFavorite, req.IsArchived, req.IsTrashed, req.Position, seq, now, id, userID,
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
		`UPDATE bookmarks SET deleted_at=$1, seq=$2, updated_at=$1
		 WHERE id=$3 AND user_id=$4 AND deleted_at IS NULL`,
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
