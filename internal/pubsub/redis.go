package pubsub

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

const hubChannelPrefix = "tabslate:sync:"

// RedisHub is a Hub backed by Redis pub/sub. All fan-out (including same-instance
// connections) flows through Redis so there is no double-delivery. Safe for
// multi-instance deployments.
type RedisHub struct {
	rdb       *redis.Client
	mu        sync.RWMutex
	subs      map[string]map[int64]chan int64 // userID → connID → local ch
	redisSubs map[string]*redis.PubSub        // userID → redis subscription
	next      atomic.Int64
}

func NewRedisHub(rdb *redis.Client) *RedisHub {
	return &RedisHub{
		rdb:       rdb,
		subs:      make(map[string]map[int64]chan int64),
		redisSubs: make(map[string]*redis.PubSub),
	}
}

func (h *RedisHub) Subscribe(userID string) (int64, <-chan int64) {
	connID := h.next.Add(1)
	ch := make(chan int64, 8)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.subs[userID] == nil {
		h.subs[userID] = make(map[int64]chan int64)
		ps := h.rdb.Subscribe(context.Background(), hubChannelPrefix+userID)
		h.redisSubs[userID] = ps
		go h.readFromRedis(userID, ps)
	}
	h.subs[userID][connID] = ch
	return connID, ch
}

func (h *RedisHub) readFromRedis(userID string, ps *redis.PubSub) {
	for msg := range ps.Channel() {
		seq, err := strconv.ParseInt(msg.Payload, 10, 64)
		if err != nil {
			log.Printf("pubsub: bad payload for user %s: %v", userID, err)
			continue
		}
		h.mu.RLock()
		conns := h.subs[userID]
		snapshot := make([]chan int64, 0, len(conns))
		for _, ch := range conns {
			snapshot = append(snapshot, ch)
		}
		h.mu.RUnlock()
		for _, ch := range snapshot {
			select {
			case ch <- seq:
			default:
			}
		}
	}
}

// Broadcast publishes seq to Redis. readFromRedis handles local fan-out so that
// all instances (including this one) receive the event exactly once.
func (h *RedisHub) Broadcast(userID string, seq int64) {
	if err := h.rdb.Publish(context.Background(), hubChannelPrefix+userID,
		fmt.Sprintf("%d", seq)).Err(); err != nil {
		log.Printf("pubsub: publish for user %s: %v", userID, err)
	}
}

func (h *RedisHub) Unsubscribe(userID string, connID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	conns, ok := h.subs[userID]
	if !ok {
		return
	}
	if ch, ok := conns[connID]; ok {
		close(ch)
		delete(conns, connID)
	}
	if len(conns) == 0 {
		delete(h.subs, userID)
		if ps, ok := h.redisSubs[userID]; ok {
			ps.Close()
			delete(h.redisSubs, userID)
		}
	}
}

// Close shuts down all Redis subscriptions and local channels.
func (h *RedisHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for userID, ps := range h.redisSubs {
		ps.Close()
		for _, ch := range h.subs[userID] {
			close(ch)
		}
	}
	h.subs = make(map[string]map[int64]chan int64)
	h.redisSubs = make(map[string]*redis.PubSub)
}
