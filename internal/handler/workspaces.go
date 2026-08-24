package handler

import (
	"net/http"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type WorkspaceHandler struct {
	db        *db.DB
	hub       pubsub.Hub
	billing   billing.Provider
	lifecycle *WorkspaceLifecycleService
}

func NewWorkspaceHandler(
	d *db.DB,
	hub pubsub.Hub,
	bp billing.Provider,
	lifecycle *WorkspaceLifecycleService,
) *WorkspaceHandler {
	return &WorkspaceHandler{
		db:        d,
		hub:       hub,
		billing:   bp,
		lifecycle: lifecycle,
	}
}

// GET /workspaces
func (h *WorkspaceHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	rows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, icon, color, position, seq, created_at, updated_at
		 FROM workspaces WHERE user_id=$1 AND is_deleted=0 ORDER BY position ASC`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list workspaces"})
		return
	}
	defer rows.Close()

	items := []model.Workspace{}
	for rows.Next() {
		var w model.Workspace
		rows.Scan(&w.ID, &w.UserID, &w.Name, &w.Icon, &w.Color, &w.Position, &w.Seq, &w.CreatedAt, &w.UpdatedAt)
		items = append(items, w)
	}
	c.JSON(http.StatusOK, items)
}

// POST /workspaces
func (h *WorkspaceHandler) Create(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
		return
	}
	if limits.MaxWorkspaces != -1 {
		var count int
		if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM workspaces WHERE user_id = $1 AND deleted_at IS NULL`, userID).Scan(&count); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		if count >= limits.MaxWorkspaces {
			c.JSON(http.StatusForbidden, gin.H{"error": "workspace limit reached", "code": "quota_exceeded"})
			return
		}
	}

	var req model.WorkspaceRequest
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
		`INSERT INTO workspaces (id, user_id, name, icon, color, position, seq, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		id, userID, req.Name, req.Icon, req.Color, req.Position, seq, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create workspace"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	c.JSON(http.StatusCreated, model.Workspace{
		ID: id, UserID: userID,
		Name: req.Name, Icon: req.Icon, Color: req.Color, Position: req.Position,
		Seq: seq, CreatedAt: now, UpdatedAt: now,
	})
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
		`UPDATE workspaces SET name=$1, icon=$2, color=$3, position=$4, seq=$5, updated_at=$6
		 WHERE id=$7 AND user_id=$8 AND is_deleted=0`,
		req.Name, req.Icon, req.Color, req.Position, seq, now, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update workspace"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)
	c.JSON(http.StatusOK, gin.H{"id": id, "seq": seq, "updated_at": now})
}

// DELETE /workspaces/:id  →  soft-delete
func (h *WorkspaceHandler) Delete(c *gin.Context) {
	h.applyLifecycle(c, model.WorkspaceLifecycleDelete, http.StatusNoContent)
}

// POST /workspaces/:id/restore
func (h *WorkspaceHandler) Restore(c *gin.Context) {
	h.applyLifecycle(c, model.WorkspaceLifecycleRestore, http.StatusOK)
}

// DELETE /workspaces/:id/permanent
func (h *WorkspaceHandler) PermanentlyDelete(c *gin.Context) {
	h.applyLifecycle(c, model.WorkspaceLifecyclePurge, http.StatusNoContent)
}

func (h *WorkspaceHandler) applyLifecycle(
	c *gin.Context,
	action model.WorkspaceLifecycleAction,
	successStatus int,
) {
	userID := middleware.UserID(c)
	id := c.Param("id")
	effect, rejection, err := h.lifecycle.Apply(c.Request.Context(), userID, id, action, 1)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace lifecycle failed"})
		return
	}
	if rejection != nil {
		c.JSON(workspaceLifecycleStatus(rejection.Reason), rejection)
		return
	}
	if successStatus == http.StatusNoContent {
		c.Status(successStatus)
		return
	}
	c.JSON(successStatus, gin.H{"id": id, "seq": effect.Seq})
}

func workspaceLifecycleStatus(reason string) int {
	switch reason {
	case model.RejectionReasonLastActiveWorkspace, model.RejectionReasonWorkspaceDeleted:
		return http.StatusConflict
	case model.RejectionReasonPermanentlyDeleted:
		return http.StatusGone
	default:
		return http.StatusNotFound
	}
}
