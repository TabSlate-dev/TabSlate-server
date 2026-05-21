package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/store"
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
}

type planUsage struct {
	Workspaces  int `json:"workspaces"`
	Bookmarks   int `json:"bookmarks"`
	Collections int `json:"collections"`
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

	if err := h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM workspaces WHERE user_id = $1 AND deleted_at IS NULL`,
		userID,
	).Scan(&usage.Workspaces); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch usage"})
		return
	}

	if err := h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM bookmarks WHERE user_id = $1 AND is_trashed < 2`,
		userID,
	).Scan(&usage.Bookmarks); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch usage"})
		return
	}

	if err := h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM collections WHERE user_id = $1 AND is_deleted < 2`,
		userID,
	).Scan(&usage.Collections); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch usage"})
		return
	}

	if err := h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM tags WHERE user_id = $1 AND deleted_at IS NULL`,
		userID,
	).Scan(&usage.Tags); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch usage"})
		return
	}

	if err := h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM groups WHERE user_id = $1 AND deleted_at IS NULL`,
		userID,
	).Scan(&usage.SavedGroups); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch usage"})
		return
	}

	c.JSON(http.StatusOK, planResponse{
		Subscription: subscription,
		Limits:       limits,
		Usage:        usage,
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
	url, err := h.billing.GetCheckoutURL(c.Request.Context(), userID, body.PlanCode)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
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
