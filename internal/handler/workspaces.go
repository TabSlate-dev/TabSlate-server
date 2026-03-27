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

type WorkspaceHandler struct{ db *sql.DB }

func NewWorkspaceHandler(db *sql.DB) *WorkspaceHandler { return &WorkspaceHandler{db: db} }

// GET /workspaces
func (h *WorkspaceHandler) List(c *gin.Context) {
	userID := middleware.UserID(c)
	rows, err := h.db.Query(
		`SELECT id, user_id, name, icon, color, position, created_at, updated_at
		   FROM workspaces WHERE user_id = ? ORDER BY position ASC`, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
	userID := middleware.UserID(c)
	if err := plan.CheckWorkspace(h.db, userID); err != nil {
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
	_, err := h.db.Exec(
		`INSERT INTO workspaces (id, user_id, name, icon, color, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.UserID, w.Name, w.Icon, w.Color, w.Position, w.CreatedAt, w.UpdatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, w)
}

// PUT /workspaces/:id
func (h *WorkspaceHandler) Update(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.WorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().Unix()
	res, err := h.db.Exec(
		`UPDATE workspaces SET name=?, icon=?, color=?, position=?, updated_at=?
		  WHERE id=? AND user_id=?`,
		req.Name, req.Icon, req.Color, req.Position, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated_at": now})
}

// DELETE /workspaces/:id
func (h *WorkspaceHandler) Delete(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")
	res, err := h.db.Exec(`DELETE FROM workspaces WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
