package store

import (
	"context"
	"sync"
	"time"
)

type cacheEntry struct {
	val      []byte
	expireAt int64 // UnixNano; 0 = never expires
}

// InMemoryCache is a process-local Cache implementation with lazy TTL expiry
// and a periodic background sweep every 30 seconds.
type InMemoryCache struct {
	mu   sync.RWMutex
	data map[string]cacheEntry
	done chan struct{}
}

func NewInMemoryCache() *InMemoryCache {
	c := &InMemoryCache{
		data: make(map[string]cacheEntry),
		done: make(chan struct{}),
	}
	go c.sweep()
	return c
}

func (c *InMemoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	var expireAt int64
	if ttl > 0 {
		expireAt = time.Now().Add(ttl).UnixNano()
	}
	c.mu.Lock()
	c.data[key] = cacheEntry{val: val, expireAt: expireAt}
	c.mu.Unlock()
	return nil
}

func (c *InMemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.RLock()
	e, ok := c.data[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if e.expireAt != 0 && time.Now().UnixNano() > e.expireAt {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		return nil, false, nil
	}
	return e.val, true, nil
}

func (c *InMemoryCache) Del(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.data, key)
	c.mu.Unlock()
	return nil
}

// Close stops the background sweep goroutine.
func (c *InMemoryCache) Close() {
	close(c.done)
}

func (c *InMemoryCache) sweep() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			c.mu.Lock()
			for k, e := range c.data {
				if e.expireAt != 0 && now > e.expireAt {
					delete(c.data, k)
				}
			}
			c.mu.Unlock()
		}
	}
}
