package store

import (
	"context"
	"time"
)

// Cache is a key-value store with TTL support.
// Get returns (val, true, nil) on hit, (nil, false, nil) on miss, (nil, false, err) on error.
// Implementations must be safe for concurrent use.
type Cache interface {
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Del(ctx context.Context, key string) error
}
