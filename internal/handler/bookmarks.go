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
)

type BookmarkHandler struct{ db *db.DB }

func NewBookmarkHandler(d *db.DB) *BookmarkHandler { return &BookmarkHandler{db: d} }

// GET /bookmarks?collection_id=&favorite=&archived=&trashed=
func (h *BookmarkHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	query := `SELECT id, user_id, collection_id, title, url, favicon_url, description,
	                 is_favorite, is_archived, is_trashed, position, created_at, updated_at
	            FROM bookmarks WHERE user_id=$1`
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
			&b.IsTrashed, &b.Position, &b.CreatedAt, &b.UpdatedAt)
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

	now := time.Now().Unix()
	b := model.Bookmark{
		ID: uuid.NewString(), UserID: userID,
		CollectionID: req.CollectionID, Title: req.Title, URL: req.URL,
		FaviconURL: req.FaviconURL, Description: req.Description,
		IsFavorite: req.IsFavorite, IsArchived: req.IsArchived, IsTrashed: req.IsTrashed,
		Position: req.Position, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.db.Exec(ctx,
		`INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url,
		  description, is_favorite, is_archived, is_trashed, position, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		b.ID, b.UserID, b.CollectionID, b.Title, b.URL, b.FaviconURL,
		b.Description, b.IsFavorite, b.IsArchived, b.IsTrashed, b.Position, b.CreatedAt, b.UpdatedAt,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create bookmark"})
		return
	}
	c.JSON(http.StatusCreated, b)
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

	now := time.Now().Unix()
	tag, err := h.db.Exec(ctx,
		`UPDATE bookmarks SET collection_id=$1, title=$2, url=$3, favicon_url=$4, description=$5,
		  is_favorite=$6, is_archived=$7, is_trashed=$8, position=$9, updated_at=$10
		  WHERE id=$11 AND user_id=$12`,
		req.CollectionID, req.Title, req.URL, req.FaviconURL, req.Description,
		req.IsFavorite, req.IsArchived, req.IsTrashed, req.Position, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update bookmark"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated_at": now})
}

// DELETE /bookmarks/:id
func (h *BookmarkHandler) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	tag, err := h.db.Exec(ctx, `DELETE FROM bookmarks WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete bookmark"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
