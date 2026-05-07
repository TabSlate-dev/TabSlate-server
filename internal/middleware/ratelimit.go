package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/internal/ratelimit"
)

// RateLimitByIP returns a Gin middleware that limits requests by client IP.
// limit and window configure the sliding-window threshold for this route group.
func RateLimitByIP(limiter ratelimit.Limiter, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "tabslate:ratelimit:" + c.FullPath() + ":" + c.ClientIP()
		if !limiter.Allow(c.Request.Context(), key, limit, window) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "too many requests, please try again later",
			})
			return
		}
		c.Next()
	}
}
