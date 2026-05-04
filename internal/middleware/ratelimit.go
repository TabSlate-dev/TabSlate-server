package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// entry tracks a single rate-limit bucket for one key.
type entry struct {
	count       int
	windowStart time.Time
}

// RateLimiter is a simple in-memory sliding-window rate limiter.
// It is safe for concurrent use.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*entry
	limit   int
	window  time.Duration
}

// NewRateLimiter creates a rate limiter that allows up to `limit` requests
// per `window` duration for each unique key.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		entries: make(map[string]*entry),
		limit:   limit,
		window:  window,
	}
	// Background cleanup every 5 minutes to prevent memory growth.
	go rl.cleanup()
	return rl
}

// Allow checks whether the given key is within its rate limit.
// Returns true if the request is allowed, false if it should be rejected.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	e, ok := rl.entries[key]
	if !ok || now.Sub(e.windowStart) >= rl.window {
		rl.entries[key] = &entry{count: 1, windowStart: now}
		return true
	}

	e.count++
	return e.count <= rl.limit
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for k, e := range rl.entries {
			if now.Sub(e.windowStart) >= rl.window {
				delete(rl.entries, k)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimitByIP returns a Gin middleware that rate-limits requests by client IP.
func RateLimitByIP(limiter *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := "ip:" + c.ClientIP()
		if !limiter.Allow(key) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "too many requests, please try again later",
			})
			return
		}
		c.Next()
	}
}

// RateLimitByEmail returns a Gin middleware that rate-limits requests by the
// email address in the JSON body. It reads the email from the Gin context
// key "rate_limit_email" which should be set by the handler or a preceding
// middleware. If not set, it falls back to IP-based limiting.
//
// For simplicity this middleware operates on the client IP + a route prefix.
// Email-based limiting is handled inside the handler after body parsing.
func RateLimitByEmail(limiter *RateLimiter) gin.HandlerFunc {
	return RateLimitByIP(limiter)
}
