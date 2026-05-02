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
)

type CollectionHandler struct{ db *db.DB }

func NewCollectionHandler(d *db.DB) *CollectionHandler { return &CollectionHandler{db: d} }

// GET /collections
func (h *CollectionHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var err error
	var items []model.Collection

	if wsID := c.Query("workspace_id"); wsID != "" {
		rows, qErr := h.db.Query(ctx,
			`SELECT id, user_id, workspace_id, name, icon, position, seq, created_at, updated_at
			 FROM collections WHERE user_id=$1 AND workspace_id=$2 AND deleted_at IS NULL ORDER BY position ASC`,
			userID, wsID)
		if qErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list collections"})
			return
		}
		defer rows.Close()
		for rows.Next() {
			var col model.Collection
			rows.Scan(&col.ID, &col.UserID, &col.WorkspaceID, &col.Name, &col.Icon, &col.Position, &col.Seq, &col.CreatedAt, &col.UpdatedAt)
			items = append(items, col)
		}
		err = rows.Err()
	} else {
		rows, qErr := h.db.Query(ctx,
			`SELECT id, user_id, workspace_id, name, icon, position, seq, created_at, updated_at
			 FROM collections WHERE user_id=$1 AND deleted_at IS NULL ORDER BY position ASC`,
			userID)
		if qErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list collections"})
			return
		}
		defer rows.Close()
		for rows.Next() {
			var col model.Collection
			rows.Scan(&col.ID, &col.UserID, &col.WorkspaceID, &col.Name, &col.Icon, &col.Position, &col.Seq, &col.CreatedAt, &col.UpdatedAt)
			items = append(items, col)
		}
		err = rows.Err()
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list collections"})
		return
	}

	if items == nil {
		items = []model.Collection{}
	}
	c.JSON(http.StatusOK, items)
}

// POST /collections
func (h *CollectionHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	if err := plan.CheckCollection(ctx, h.db, userID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	var req model.CollectionRequest
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
		`INSERT INTO collections (id, user_id, workspace_id, name, icon, position, seq, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		id, userID, req.WorkspaceID, req.Name, req.Icon, req.Position, seq, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create collection"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	globalHub.Broadcast(userID, seq)
	c.JSON(http.StatusCreated, model.Collection{
		ID: id, UserID: userID, WorkspaceID: req.WorkspaceID,
		Name: req.Name, Icon: req.Icon, Position: req.Position,
		Seq: seq, CreatedAt: now, UpdatedAt: now,
	})
}

// PUT /collections/:id
func (h *CollectionHandler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.CollectionRequest
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
		`UPDATE collections SET workspace_id=$1, name=$2, icon=$3, position=$4, seq=$5, updated_at=$6
		 WHERE id=$7 AND user_id=$8 AND deleted_at IS NULL`,
		req.WorkspaceID, req.Name, req.Icon, req.Position, seq, now, id, userID)
	if err != nil || tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	globalHub.Broadcast(userID, seq)
	c.JSON(http.StatusOK, gin.H{"id": id, "seq": seq, "updated_at": now})
}

// DELETE /collections/:id  →  soft-delete
func (h *CollectionHandler) Delete(c *gin.Context) {
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
		`UPDATE collections SET deleted_at=$1, seq=$2, updated_at=$1
		 WHERE id=$3 AND user_id=$4 AND deleted_at IS NULL`,
		now, seq, id, userID)
	if err != nil || tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	globalHub.Broadcast(userID, seq)
	c.Status(http.StatusNoContent)
}
