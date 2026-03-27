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

type CollectionHandler struct{ db *sql.DB }

func NewCollectionHandler(db *sql.DB) *CollectionHandler { return &CollectionHandler{db: db} }

// GET /collections
func (h *CollectionHandler) List(c *gin.Context) {
	userID := middleware.UserID(c)
	wsID := c.Query("workspace_id") // optional filter

	var rows *sql.Rows
	var err error
	if wsID != "" {
		rows, err = h.db.Query(
			`SELECT id, user_id, workspace_id, name, icon, position, created_at, updated_at
			   FROM collections WHERE user_id=? AND workspace_id=? ORDER BY position ASC`,
			userID, wsID,
		)
	} else {
		rows, err = h.db.Query(
			`SELECT id, user_id, workspace_id, name, icon, position, created_at, updated_at
			   FROM collections WHERE user_id=? ORDER BY position ASC`,
			userID,
		)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
	userID := middleware.UserID(c)
	if err := plan.CheckCollection(h.db, userID); err != nil {
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
	_, err := h.db.Exec(
		`INSERT INTO collections (id, user_id, workspace_id, name, icon, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		col.ID, col.UserID, col.WorkspaceID, col.Name, col.Icon, col.Position, col.CreatedAt, col.UpdatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, col)
}

// PUT /collections/:id
func (h *CollectionHandler) Update(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")

	var req model.CollectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().Unix()
	res, err := h.db.Exec(
		`UPDATE collections SET workspace_id=?, name=?, icon=?, position=?, updated_at=?
		  WHERE id=? AND user_id=?`,
		req.WorkspaceID, req.Name, req.Icon, req.Position, now, id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "updated_at": now})
}

// DELETE /collections/:id
func (h *CollectionHandler) Delete(c *gin.Context) {
	userID := middleware.UserID(c)
	id := c.Param("id")
	res, err := h.db.Exec(`DELETE FROM collections WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "collection not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
