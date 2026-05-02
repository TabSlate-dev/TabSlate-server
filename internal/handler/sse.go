package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/db"
)

type SSEHandler struct{ db *db.DB }

func NewSSEHandler(d *db.DB) *SSEHandler { return &SSEHandler{db: d} }

// GET /sync/stream?token=<sse_token>
// Streams server-sent events to the client. Sends {"seq": N} on every write
// for the authenticated user. Keeps alive with ": ping" every 30 seconds.
// Auth is via a single-use SSE token (issued by POST /auth/sse-token) because
// EventSource cannot set Authorization headers.
func (h *SSEHandler) Stream(c *gin.Context) {
	ctx := c.Request.Context()
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing SSE token"})
		return
	}

	// Validate and consume the single-use SSE token atomically.
	var userID string
	var expiresAt int64
	err := h.db.QueryRow(ctx,
		`DELETE FROM sse_tokens WHERE token=$1 RETURNING user_id, expires_at`,
		token,
	).Scan(&userID, &expiresAt)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired SSE token"})
		return
	}
	if time.Now().UnixMilli() > expiresAt {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "SSE token expired"})
		return
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	connID, ch := globalHub.Subscribe(userID)
	defer globalHub.Unsubscribe(userID, connID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case seq, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "data: {\"seq\":%d}\n\n", seq); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
