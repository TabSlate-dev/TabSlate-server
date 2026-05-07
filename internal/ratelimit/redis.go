package ratelimit

import (
	"context"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// incrScript atomically increments a counter and sets its TTL on first
// increment. KEYS[1] = key, ARGV[1] = TTL in seconds.
var incrScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
return count
`)

// allowScript atomically removes expired entries, checks the count against the
// limit, and adds the current request only if it is permitted.
// KEYS[1] = key, ARGV[1] = cutoff (nanoseconds as string), ARGV[2] = score (nanoseconds),
// ARGV[3] = limit, ARGV[4] = member (unique), ARGV[5] = TTL in seconds.
// Returns 1 if allowed, 0 if denied.
var allowScript = redis.NewScript(`
local key    = KEYS[1]
local cutoff = ARGV[1]
local now    = tonumber(ARGV[2])
local limit  = tonumber(ARGV[3])
local member = ARGV[4]
local ttl    = tonumber(ARGV[5])
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count < limit then
  redis.call('ZADD', key, now, member)
  redis.call('EXPIRE', key, ttl)
  return 1
end
return 0
`)

// RedisLimiter is a Limiter backed by Redis.
// Allow uses a sorted-set sliding window via Lua script; IncrCounter uses an atomic Lua script.
type RedisLimiter struct {
	rdb  *redis.Client
	next atomic.Int64
}

func NewRedisLimiter(rdb *redis.Client) *RedisLimiter {
	return &RedisLimiter{rdb: rdb}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	now := time.Now().UnixNano()
	cutoff := now - window.Nanoseconds()
	member := strconv.FormatInt(now, 10) + ":" + strconv.FormatInt(l.next.Add(1), 10)
	ttlSecs := int64(math.Ceil(window.Seconds()))

	result, err := allowScript.Run(ctx, l.rdb, []string{key},
		strconv.FormatInt(cutoff, 10),
		strconv.FormatInt(now, 10),
		limit,
		member,
		ttlSecs,
	).Int64()
	if err != nil {
		return true // fail open on Redis error
	}
	return result == 1
}

func (l *RedisLimiter) IncrCounter(ctx context.Context, key string, window time.Duration) (int64, error) {
	ttlSecs := int64(math.Ceil(window.Seconds()))
	return incrScript.Run(ctx, l.rdb, []string{key}, ttlSecs).Int64()
}

func (l *RedisLimiter) ResetCounter(ctx context.Context, key string) error {
	return l.rdb.Del(ctx, key).Err()
}

func (l *RedisLimiter) GetCounter(ctx context.Context, key string) (int64, error) {
	val, err := l.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}
