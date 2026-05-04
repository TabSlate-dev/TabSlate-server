package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/internal/middleware"
)

// BillingHandler exposes plan, limits, checkout, and invoice endpoints.
// All behaviour is delegated to the injected billing.Provider so the same
// routes work for both the OSS (local provider) and Cloud (Lago) editions.
type BillingHandler struct {
	billing billing.Provider
}

func NewBillingHandler(bp billing.Provider) *BillingHandler {
	return &BillingHandler{billing: bp}
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

// GET /api/limits
func (h *BillingHandler) GetLimits(c *gin.Context) {
	userID := middleware.UserID(c)
	limits, err := h.billing.GetLimits(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, limits)
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
