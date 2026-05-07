package pubsub

import (
	"sync"
	"sync/atomic"
)

// InMemoryHub is a process-local Hub implementation. Safe for single-instance
// deployments. For multi-instance use RedisHub.
type InMemoryHub struct {
	mu   sync.RWMutex
	subs map[string]map[int64]chan int64
	next atomic.Int64
}

func NewInMemoryHub() *InMemoryHub {
	return &InMemoryHub{subs: make(map[string]map[int64]chan int64)}
}

func (h *InMemoryHub) Subscribe(userID string) (int64, <-chan int64) {
	connID := h.next.Add(1)
	ch := make(chan int64, 8)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[int64]chan int64)
	}
	h.subs[userID][connID] = ch
	h.mu.Unlock()
	return connID, ch
}

func (h *InMemoryHub) Unsubscribe(userID string, connID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if conns, ok := h.subs[userID]; ok {
		if ch, ok := conns[connID]; ok {
			close(ch)
			delete(conns, connID)
		}
		if len(conns) == 0 {
			delete(h.subs, userID)
		}
	}
}

func (h *InMemoryHub) Broadcast(userID string, seq int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs[userID] {
		select {
		case ch <- seq:
		default:
		}
	}
}

// Close shuts down the hub, closing all subscriber channels.
func (h *InMemoryHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, conns := range h.subs {
		for _, ch := range conns {
			close(ch)
		}
	}
	h.subs = make(map[string]map[int64]chan int64)
}
