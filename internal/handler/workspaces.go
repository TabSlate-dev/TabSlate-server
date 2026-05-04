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

type WorkspaceHandler struct{ db *db.DB }

func NewWorkspaceHandler(d *db.DB) *WorkspaceHandler { return &WorkspaceHandler{db: d} }

// GET /workspaces
func (h *WorkspaceHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	rows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, icon, color, position, created_at, updated_at
		 FROM workspaces WHERE user_id = $1 ORDER BY position ASC`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list workspaces"})
		return
	}
	defer rows.Close()

	items := []model.Workspace{}
	for rows.Next() {
		var w model.Workspace
		rows.Scan(&w.ID, &w.UserID, &w.Name, &w.Icon, &w.Color, &w.Position, &w.CreatedAt, &w.UpdatedAt)
		items = append(items, w)
	}
	c.JSON(http.StatusOK, items)
}

// POST /workspaces
func (h *WorkspaceHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	if err := plan.CheckWorkspace(ctx, h.db, userID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	var req model.WorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().Unix()
	w := model.Workspace{
		ID: uuid.NewString(), UserID: userID,
		Name: req.Name, Icon: req.Icon, Color: req.Color, Position: req.Position,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := h.db.Exec(ctx,
		`INSERT INTO workspaces (id, user_id, name, icon, color, position, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		w.ID, w.UserID, w.Name, w.Icon, w.Color, w.Position, w.CreatedAt, w.UpdatedAt,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create workspace"})
		return
	}
	c.JSON(http.StatusCreated, w)
}

// PUT /workspaces/:id
func (h *WorkspaceHandler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.WorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().Unix()
	tag, err := h.db.Exec(ctx,
		`UPDATE workspaces SET name=$1, icon=$2, color=$3, position=$4, updated_at=$5
		 WHERE id=$6 AND user_id=$7`,
		req.Name, req.Icon, req.Color, req.Position, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update workspace"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated_at": now})
}

// DELETE /workspaces/:id
func (h *WorkspaceHandler) Delete(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	id := c.Param("id")

	tag, err := h.db.Exec(ctx, `DELETE FROM workspaces WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete workspace"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
