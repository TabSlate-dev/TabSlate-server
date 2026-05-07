package ratelimit

import (
	"context"
	"sync"
	"time"
)

type windowEntry struct {
	timestamps []int64 // UnixNano
}

type counterEntry struct {
	count   int64
	resetAt int64 // UnixNano
}

// InMemoryLimiter is a process-local Limiter. For multi-instance deployments
// use RedisLimiter so rate-limit state is shared across instances.
type InMemoryLimiter struct {
	mu       sync.Mutex
	windows  map[string]*windowEntry
	counters map[string]*counterEntry
}

func NewInMemoryLimiter() *InMemoryLimiter {
	return &InMemoryLimiter{
		windows:  make(map[string]*windowEntry),
		counters: make(map[string]*counterEntry),
	}
}

func (l *InMemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()
	cutoff := now - window.Nanoseconds()

	e, ok := l.windows[key]
	if !ok {
		e = &windowEntry{}
		l.windows[key] = e
	}

	// Drop timestamps outside the window.
	valid := e.timestamps[:0]
	for _, ts := range e.timestamps {
		if ts > cutoff {
			valid = append(valid, ts)
		}
	}
	e.timestamps = valid

	if len(e.timestamps) >= limit {
		return false
	}
	e.timestamps = append(e.timestamps, now)
	return true
}

func (l *InMemoryLimiter) IncrCounter(_ context.Context, key string, window time.Duration) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()
	e, ok := l.counters[key]
	if !ok || now > e.resetAt {
		l.counters[key] = &counterEntry{count: 1, resetAt: now + window.Nanoseconds()}
		return 1, nil
	}
	e.count++
	return e.count, nil
}

func (l *InMemoryLimiter) ResetCounter(_ context.Context, key string) error {
	l.mu.Lock()
	delete(l.counters, key)
	l.mu.Unlock()
	return nil
}

func (l *InMemoryLimiter) GetCounter(_ context.Context, key string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UnixNano()
	e, ok := l.counters[key]
	if !ok || now > e.resetAt {
		return 0, nil
	}
	return e.count, nil
}
