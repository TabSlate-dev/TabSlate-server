package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
	"github.com/tabslate/server/internal/plan"
)

type TagHandler struct{ db *db.DB }

func NewTagHandler(d *db.DB) *TagHandler { return &TagHandler{db: d} }

// GET /tags
func (h *TagHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	rows, err := h.db.Query(ctx, `SELECT id, user_id, name, color FROM tags WHERE user_id=$1`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tags"})
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
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	if err := plan.CheckTag(ctx, h.db, userID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	var req model.TagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	t := model.Tag{ID: uuid.NewString(), UserID: userID, Name: req.Name, Color: req.Color}
	if _, err := h.db.Exec(ctx,
		`INSERT INTO tags (id, user_id, name, color) VALUES ($1,$2,$3,$4)`,
		t.ID, t.UserID, t.Name, t.Color,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tag"})
		return
	}
	c.JSON(http.StatusCreated, t)
}

// PUT /tags/:id
func (h *TagHandler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.TagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tag, err := h.db.Exec(ctx,
		`UPDATE tags SET name=$1, color=$2 WHERE id=$3 AND user_id=$4`,
		req.Name, req.Color, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update tag"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// DELETE /tags/:id
func (h *TagHandler) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	tag, err := h.db.Exec(ctx, `DELETE FROM tags WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete tag"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
