package ratelimit

import (
	"context"
	"time"
)

// Limiter provides rate limiting and counter primitives.
// Allow uses a sliding-window algorithm; true = request is permitted.
// IncrCounter atomically increments a named counter, setting a TTL on first
// increment. Implementations must be safe for concurrent use.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) bool
	IncrCounter(ctx context.Context, key string, window time.Duration) (int64, error)
	ResetCounter(ctx context.Context, key string) error
	GetCounter(ctx context.Context, key string) (int64, error)
}
