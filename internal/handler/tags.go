package handler

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lieutenant/tabmaster/internal/middleware"
	"github.com/lieutenant/tabmaster/internal/model"
	"github.com/lieutenant/tabmaster/internal/plan"
)

type TagHandler struct{ db *sql.DB }

func NewTagHandler(db *sql.DB) *TagHandler { return &TagHandler{db: db} }

// GET /tags
func (h *TagHandler) List(c *gin.Context) {
	userID := middleware.UserID(c)
	rows, err := h.db.Query(`SELECT id, user_id, name, color FROM tags WHERE user_id=?`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := []model.Tag{}
	for rows.Next() {
		var t model.Tag
		rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color)
		items = append(items, t)
	}
	c.JSON(http.StatusOK, items)
}

// POST /tags
func (h *TagHandler) Create(c *gin.Context) {
	userID := middleware.UserID(c)
	if err := plan.CheckTag(h.db, userID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	var req model.TagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	t := model.Tag{ID: uuid.NewString(), UserID: userID, Name: req.Name, Color: req.Color}
	_, err := h.db.Exec(`INSERT INTO tags (id, user_id, name, color) VALUES (?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.Color)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, t)
}

// PUT /tags/:id
func (h *TagHandler) Update(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.TagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res, err := h.db.Exec(`UPDATE tags SET name=?, color=? WHERE id=? AND user_id=?`,
		req.Name, req.Color, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// DELETE /tags/:id
func (h *TagHandler) Delete(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")
	res, err := h.db.Exec(`DELETE FROM tags WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
