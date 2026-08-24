package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/store"
	"github.com/gin-gonic/gin"
)

// BillingHandler exposes plan, limits, checkout, and invoice endpoints.
type BillingHandler struct {
	billing billing.Provider
	cache   store.Cache
	db      *db.DB
}

type planResponse struct {
	Subscription *billing.Subscription `json:"subscription"`
	Limits       *billing.Limits       `json:"limits"`
	Usage        planUsage             `json:"usage"`
	TrashUsage   planUsage             `json:"trash_usage"`
}

type planUsage struct {
	Workspaces  int `json:"workspaces"`
	Collections int `json:"collections"`
	Bookmarks   int `json:"bookmarks"`
	Tags        int `json:"tags"`
	SavedGroups int `json:"saved_groups"`
}

func NewBillingHandler(bp billing.Provider, cache store.Cache, d *db.DB) *BillingHandler {
	return &BillingHandler{billing: bp, cache: cache, db: d}
}

// GET /api/subscription
func (h *BillingHandler) GetSubscription(c *gin.Context) {
	userID := middleware.UserID(c)
	sub, err := h.billing.GetSubscription(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sub)
}

// GET /api/limits — result cached for 60s per user.
func (h *BillingHandler) GetLimits(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	cacheKey := "tabslate:billing:limits:" + userID

	if raw, found, _ := h.cache.Get(ctx, cacheKey); found {
		c.Data(http.StatusOK, "application/json", raw)
		return
	}

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if raw, err := json.Marshal(limits); err == nil {
		h.cache.Set(ctx, cacheKey, raw, 60*time.Second)
	}
	c.JSON(http.StatusOK, limits)
}

// GET /api/plan
func (h *BillingHandler) GetPlan(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	subscription, err := h.billing.GetSubscription(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch subscription"})
		return
	}

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch limits"})
		return
	}

	usage := planUsage{}
	trashUsage := planUsage{}
	if err := h.db.QueryRow(ctx, `
		WITH workspace_usage AS (
			SELECT
				COUNT(*) FILTER (WHERE is_deleted < 2) AS total,
				COUNT(*) FILTER (WHERE is_deleted = 1) AS trash
			FROM workspaces
			WHERE user_id = $1
		), collection_usage AS (
			SELECT
				COUNT(*) FILTER (WHERE c.is_deleted < 2) AS total,
				COUNT(*) FILTER (
					WHERE c.is_deleted < 2 AND (c.is_deleted = 1 OR w.is_deleted = 1)
				) AS trash
			FROM collections c
			LEFT JOIN workspaces w ON w.id = c.workspace_id AND w.user_id = c.user_id
			WHERE c.user_id = $1
		), bookmark_usage AS (
			SELECT
				COUNT(*) FILTER (WHERE b.is_trashed < 2) AS total,
				COUNT(*) FILTER (
					WHERE b.is_trashed < 2
					  AND (b.is_trashed = 1 OR c.is_deleted = 1 OR w.is_deleted = 1)
				) AS trash
			FROM bookmarks b
			LEFT JOIN collections c ON c.id = b.collection_id AND c.user_id = b.user_id
			LEFT JOIN workspaces w ON w.id = c.workspace_id AND w.user_id = c.user_id
			WHERE b.user_id = $1
		), tag_usage AS (
			SELECT COUNT(*) FILTER (WHERE deleted_at IS NULL) AS total
			FROM tags
			WHERE user_id = $1
		), group_usage AS (
			SELECT
				COUNT(*) FILTER (WHERE g.is_deleted < 2) AS total,
				COUNT(*) FILTER (
					WHERE g.is_deleted < 2 AND (g.is_deleted = 1 OR w.is_deleted = 1)
				) AS trash
			FROM groups g
			LEFT JOIN workspaces w ON w.id = g.workspace_id AND w.user_id = g.user_id
			WHERE g.user_id = $1
		)
		SELECT
			workspace_usage.total,
			collection_usage.total,
			bookmark_usage.total,
			tag_usage.total,
			group_usage.total,
			workspace_usage.trash,
			collection_usage.trash,
			bookmark_usage.trash,
			0,
			group_usage.trash
		FROM workspace_usage, collection_usage, bookmark_usage, tag_usage, group_usage`,
		userID,
	).Scan(
		&usage.Workspaces,
		&usage.Collections,
		&usage.Bookmarks,
		&usage.Tags,
		&usage.SavedGroups,
		&trashUsage.Workspaces,
		&trashUsage.Collections,
		&trashUsage.Bookmarks,
		&trashUsage.Tags,
		&trashUsage.SavedGroups,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch usage"})
		return
	}

	c.JSON(http.StatusOK, planResponse{
		Subscription: subscription,
		Limits:       limits,
		Usage:        usage,
		TrashUsage:   trashUsage,
	})
}

// POST /api/checkout  body: {"plan_code": "pro"}
func (h *BillingHandler) CreateCheckout(c *gin.Context) {
	userID := middleware.UserID(c)
	var body struct {
		PlanCode string `json:"plan_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.billing.ChangePlan(c.Request.Context(), userID, body.PlanCode); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// GET /api/invoices?page=1&per_page=20
func (h *BillingHandler) ListInvoices(c *gin.Context) {
	userID := middleware.UserID(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	invoices, err := h.billing.ListInvoices(c.Request.Context(), userID, page, perPage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, invoices)
}

// DELETE /api/subscription
func (h *BillingHandler) CancelSubscription(c *gin.Context) {
	userID := middleware.UserID(c)
	if err := h.billing.CancelSubscription(c.Request.Context(), userID); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
