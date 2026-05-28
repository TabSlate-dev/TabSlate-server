package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
)

type PreferencesHandler struct {
	db *db.DB
}

func NewPreferencesHandler(d *db.DB) *PreferencesHandler {
	return &PreferencesHandler{db: d}
}

// GET /preferences
// Returns the user's preferences as a JSON object.
func (h *PreferencesHandler) Get(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var raw []byte
	err := h.db.QueryRow(ctx,
		`SELECT preferences FROM users WHERE id = $1`, userID,
	).Scan(&raw)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Return the raw JSON directly to avoid double-encoding.
	var prefs json.RawMessage
	if err := json.Unmarshal(raw, &prefs); err != nil {
		// Column might be empty/invalid — return empty object.
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	c.Data(http.StatusOK, "application/json", raw)
}

// PUT /preferences
// Replaces the user's preferences with the request body.
// The body must be a valid JSON object. The server stores it opaquely.
func (h *PreferencesHandler) Update(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)

	// Read the raw body and validate it is a JSON object.
	var body json.RawMessage
	if err := c.ShouldBindJSON(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large (max 64 KB)"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "request body must be valid JSON"})
		}
		return
	}

	// Ensure it's a JSON object (not array, string, etc.).
	var check map[string]interface{}
	if err := json.Unmarshal(body, &check); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "preferences must be a JSON object"})
		return
	}

	now := time.Now().Unix()
	if _, err := h.db.Exec(ctx,
		`UPDATE users SET preferences = $1, updated_at = $2 WHERE id = $3`,
		string(body), now, userID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update preferences"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "preferences updated"})
}
