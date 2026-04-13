package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	var rows pgx.Rows
	var err error
	if wsID := c.Query("workspace_id"); wsID != "" {
		rows, err = h.db.Query(ctx,
			`SELECT id, user_id, workspace_id, name, icon, position, created_at, updated_at
			 FROM collections WHERE user_id=$1 AND workspace_id=$2 ORDER BY position ASC`,
			userID, wsID)
	} else {
		rows, err = h.db.Query(ctx,
			`SELECT id, user_id, workspace_id, name, icon, position, created_at, updated_at
			 FROM collections WHERE user_id=$1 ORDER BY position ASC`,
			userID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list collections"})
		return
	}
	defer rows.Close()

	items := []model.Collection{}
	for rows.Next() {
		var col model.Collection
		rows.Scan(&col.ID, &col.UserID, &col.WorkspaceID, &col.Name, &col.Icon, &col.Position, &col.CreatedAt, &col.UpdatedAt)
		items = append(items, col)
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

	now := time.Now().Unix()
	col := model.Collection{
		ID: uuid.NewString(), UserID: userID,
		WorkspaceID: req.WorkspaceID, Name: req.Name, Icon: req.Icon, Position: req.Position,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.db.Exec(ctx,
		`INSERT INTO collections (id, user_id, workspace_id, name, icon, position, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		col.ID, col.UserID, col.WorkspaceID, col.Name, col.Icon, col.Position, col.CreatedAt, col.UpdatedAt,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create collection"})
		return
	}
	c.JSON(http.StatusCreated, col)
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

	now := time.Now().Unix()
	tag, err := h.db.Exec(ctx,
		`UPDATE collections SET workspace_id=$1, name=$2, icon=$3, position=$4, updated_at=$5
		 WHERE id=$6 AND user_id=$7`,
		req.WorkspaceID, req.Name, req.Icon, req.Position, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update collection"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated_at": now})
}

// DELETE /collections/:id
func (h *CollectionHandler) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	tag, err := h.db.Exec(ctx, `DELETE FROM collections WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete collection"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
