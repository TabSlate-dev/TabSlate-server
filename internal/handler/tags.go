package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
	"github.com/tabslate/server/internal/plan"
	"github.com/tabslate/server/internal/pubsub"
)

type TagHandler struct {
	db  *db.DB
	hub pubsub.Hub
}

func NewTagHandler(d *db.DB, hub pubsub.Hub) *TagHandler {
	return &TagHandler{db: d, hub: hub}
}

// GET /tags
func (h *TagHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	rows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, color, seq FROM tags WHERE user_id=$1 AND deleted_at IS NULL`,
		userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tags"})
		return
	}
	defer rows.Close()

	items := []model.Tag{}
	for rows.Next() {
		var t model.Tag
		rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.Seq)
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

	id := uuid.NewString()

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
	if _, err := tx.Exec(ctx,
		`INSERT INTO tags (id, user_id, name, color, seq, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, userID, req.Name, req.Color, seq, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tag"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	c.JSON(http.StatusCreated, model.Tag{ID: id, UserID: userID, Name: req.Name, Color: req.Color, Seq: seq})
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
		`UPDATE tags SET name=$1, color=$2, seq=$3, updated_at=$4 WHERE id=$5 AND user_id=$6 AND deleted_at IS NULL`,
		req.Name, req.Color, seq, time.Now().UnixMilli(), id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update tag"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	c.JSON(http.StatusOK, gin.H{"id": id, "seq": seq})
}

// DELETE /tags/:id  →  soft-delete
func (h *TagHandler) Delete(c *gin.Context) {
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
		`UPDATE tags SET deleted_at=$1, seq=$2, updated_at=$1 WHERE id=$3 AND user_id=$4 AND deleted_at IS NULL`,
		now, seq, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete tag"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	c.Status(http.StatusNoContent)
}
