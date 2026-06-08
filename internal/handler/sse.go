package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/store"
)

type SSEHandler struct {
	hub   pubsub.Hub
	cache store.Cache
}

func NewSSEHandler(hub pubsub.Hub, cache store.Cache) *SSEHandler {
	return &SSEHandler{hub: hub, cache: cache}
}

// GET /sync/stream?token=<sse_token>
// Streams server-sent events to the client. Auth via single-use SSE token
// stored in Cache (issued by POST /auth/sse-token). Keeps alive with ": ping"
// every 30 seconds.
func (h *SSEHandler) Stream(c *gin.Context) {
	ctx := c.Request.Context()
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing SSE token"})
		return
	}

	// Validate and atomically consume the single-use token from cache.
	val, found, err := h.cache.Get(ctx, "tabslate:sse_token:"+token)
	if err != nil || !found {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired SSE token"})
		return
	}
	userID := string(val)
	// Consume: delete so it cannot be reused.
	h.cache.Del(ctx, "tabslate:sse_token:"+token)

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	connID, ch := h.hub.Subscribe(userID)
	defer h.hub.Unsubscribe(userID, connID)

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
