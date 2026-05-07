package ratelimit

import (
	"context"
	"strconv"
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

// RedisLimiter is a Limiter backed by Redis.
// Allow uses a sorted-set sliding window; IncrCounter uses an atomic Lua script.
type RedisLimiter struct {
	rdb *redis.Client
}

func NewRedisLimiter(rdb *redis.Client) *RedisLimiter {
	return &RedisLimiter{rdb: rdb}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	now := time.Now().UnixNano()
	cutoff := now - window.Nanoseconds()

	pipe := l.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff, 10))
	countCmd := pipe.ZCard(ctx, key)
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
	pipe.Expire(ctx, key, window)

	if _, err := pipe.Exec(ctx); err != nil {
		return true // fail open on Redis error
	}
	return countCmd.Val() < int64(limit)
}

func (l *RedisLimiter) IncrCounter(ctx context.Context, key string, window time.Duration) (int64, error) {
	ttlSecs := int64(window.Seconds())
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
