package infra

import (
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/tabslate/server/internal/pubsub"
	"github.com/tabslate/server/internal/ratelimit"
	"github.com/tabslate/server/internal/store"
)

// Providers holds the three infrastructure providers wired by New.
type Providers struct {
	Hub     pubsub.Hub
	Cache   store.Cache
	Limiter ratelimit.Limiter
}

// New creates Providers from redisURL.
// If redisURL is empty all providers use in-memory implementations (OSS mode).
// The returned cleanup function must be called on process shutdown.
func New(redisURL string) (*Providers, func(), error) {
	if redisURL == "" {
		hub := pubsub.NewInMemoryHub()
		cache := store.NewInMemoryCache()
		cleanup := func() { hub.Close(); cache.Close() }
		return &Providers{
			Hub:     hub,
			Cache:   cache,
			Limiter: ratelimit.NewInMemoryLimiter(),
		}, cleanup, nil
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	success := false
	defer func() {
		if !success {
			rdb.Close()
		}
	}()
	hub := pubsub.NewRedisHub(rdb)
	p := &Providers{
		Hub:     hub,
		Cache:   store.NewRedisCache(rdb),
		Limiter: ratelimit.NewRedisLimiter(rdb),
	}
	success = true
	return p, func() { hub.Close(); rdb.Close() }, nil
}
