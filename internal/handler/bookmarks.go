package handler

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lieutenant/tabmaster/internal/middleware"
	"github.com/lieutenant/tabmaster/internal/model"
	"github.com/lieutenant/tabmaster/internal/plan"
)

type BookmarkHandler struct{ db *sql.DB }

func NewBookmarkHandler(db *sql.DB) *BookmarkHandler { return &BookmarkHandler{db: db} }

// GET /bookmarks?collection_id=&favorite=&archived=&trashed=
func (h *BookmarkHandler) List(c *gin.Context) {
	userID := middleware.UserID(c)

	query := `SELECT id, user_id, collection_id, title, url, favicon_url, description,
	                 is_favorite, is_archived, is_trashed, position, created_at, updated_at
	            FROM bookmarks WHERE user_id=?`
	args := []any{userID}

	if cid := c.Query("collection_id"); cid != "" {
		query += " AND collection_id=?"
		args = append(args, cid)
	}
	if c.Query("favorite") == "true" {
		query += " AND is_favorite=1"
	}
	if c.Query("archived") == "true" {
		query += " AND is_archived=1"
	}
	if c.Query("trashed") == "true" {
		query += " AND is_trashed=1"
	} else {
		query += " AND is_trashed=0"
	}
	query += " ORDER BY position ASC"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
	userID := middleware.UserID(c)
	if err := plan.CheckBookmark(h.db, userID); err != nil {
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
	_, err := h.db.Exec(
		`INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url,
		  description, is_favorite, is_archived, is_trashed, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.UserID, b.CollectionID, b.Title, b.URL, b.FaviconURL,
		b.Description, b.IsFavorite, b.IsArchived, b.IsTrashed, b.Position, b.CreatedAt, b.UpdatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, b)
}

// PUT /bookmarks/:id
func (h *BookmarkHandler) Update(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.BookmarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().Unix()
	res, err := h.db.Exec(
		`UPDATE bookmarks SET collection_id=?, title=?, url=?, favicon_url=?, description=?,
		  is_favorite=?, is_archived=?, is_trashed=?, position=?, updated_at=?
		  WHERE id=? AND user_id=?`,
		req.CollectionID, req.Title, req.URL, req.FaviconURL, req.Description,
		req.IsFavorite, req.IsArchived, req.IsTrashed, req.Position, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated_at": now})
}

// DELETE /bookmarks/:id
func (h *BookmarkHandler) Delete(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")
	res, err := h.db.Exec(`DELETE FROM bookmarks WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
