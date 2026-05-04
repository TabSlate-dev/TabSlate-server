package handler

import (
	"sync"
	"sync/atomic"
)

// Hub is an in-memory pub/sub broadcaster for SSE connections.
// It maps userID → {connID → channel} so the server can notify all open
// SSE connections for a given user when new data is available.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[int64]chan int64 // userID → connID → seqChan
	next atomic.Int64
}

var globalHub = &Hub{
	subs: make(map[string]map[int64]chan int64),
}

// Subscribe registers a new SSE connection for userID and returns
// the connection ID and a channel that receives new seq values.
func (h *Hub) Subscribe(userID string) (connID int64, ch chan int64) {
	connID = h.next.Add(1)
	ch = make(chan int64, 8) // buffered to avoid blocking the broadcaster

	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[int64]chan int64)
	}
	h.subs[userID][connID] = ch
	h.mu.Unlock()
	return connID, ch
}

// Unsubscribe removes the connection and closes its channel.
func (h *Hub) Unsubscribe(userID string, connID int64) {
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

// Broadcast sends seq to all SSE connections registered for userID.
// Non-blocking: connections with full buffers are skipped (they will catch
// up on the next periodic pull).
func (h *Hub) Broadcast(userID string, seq int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs[userID] {
		select {
		case ch <- seq:
		default:
		}
	}
}
