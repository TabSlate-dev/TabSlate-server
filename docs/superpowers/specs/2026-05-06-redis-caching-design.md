# Redis Caching & Horizontal Scaling Design

**Date:** 2026-05-06  
**Repo:** TabSlate-server  
**Status:** Approved

---

## 背景与目标

当前服务有三处 in-memory 状态在多实例部署时会失效：

1. `sse_hub.go` 的 `globalHub`：进程内 pub/sub，多实例时 SSE 广播只能通知同进程内的连接
2. `middleware/ratelimit.go` 的滑动窗口：per-instance，多实例时限速不共享
3. 认证计数表（`login_failures`、`otp_ip_requests`、`register_ip_requests`）：存 DB + 定时清理 goroutine，有额外写压力

同时，两处热点读可通过缓存降低 DB 压力：
- SSE token 验证：每次 SSE 建连都做 `DELETE FROM sse_tokens WHERE token=$1 RETURNING ...`
- Billing limits：`GetLimits` 在 Cloud 版调外部 API，CLAUDE.md 已标注"应带本地缓存"

**目标：** 引入 Redis 解决上述问题，同时保持 OSS 自托管无需 Redis（降级到 in-memory，单实例行为与现在一致）。

---

## 方案选择

采用**按关注点拆分独立接口**（方案 A）：

- 与现有 `billing.Provider` 模式一致
- 每个接口独立可测、可换
- `REDIS_URL` 未设置时全部回退 in-memory，OSS 用户零配置变化

---

## 新增包结构

```
internal/
  pubsub/
    hub.go          ← Hub 接口
    memory.go       ← in-memory 实现（现 sse_hub.go 逻辑迁入）
    redis.go        ← Redis pub/sub 实现
  store/
    cache.go        ← Cache 接口
    memory.go
    redis.go
  ratelimit/
    limiter.go      ← Limiter 接口
    memory.go
    redis.go
  infra/
    infra.go        ← 工厂函数，根据 REDIS_URL 返回对应实现
```

---

## 接口定义

### `pubsub.Hub`

```go
// internal/pubsub/hub.go
type Hub interface {
    Subscribe(userID string) (connID int64, ch <-chan int64)
    Broadcast(userID string, seq int64)
    Unsubscribe(userID string, connID int64)
}
```

与现有 `sse_hub.go` 方法签名完全对齐，handler 侧零改动。

### `store.Cache`

```go
// internal/store/cache.go
type Cache interface {
    Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
    Get(ctx context.Context, key string) ([]byte, bool, error) // (val, found, err)
    Del(ctx context.Context, key string) error
}
```

上层调用方负责序列化/反序列化。`Get` 返回 `(val, found, err)` 三元组，区分"key 不存在"和"读取出错"。

### `ratelimit.Limiter`

```go
// internal/ratelimit/limiter.go
type Limiter interface {
    // HTTP 请求限速，true = 放行
    Allow(ctx context.Context, key string, limit int, window time.Duration) bool
    // 认证计数器（Incr + 首次写入时设置 TTL）
    IncrCounter(ctx context.Context, key string, window time.Duration) (int64, error)
    ResetCounter(ctx context.Context, key string) error
    GetCounter(ctx context.Context, key string) (int64, error)
}
```

合并两个现有机制（middleware 滑动窗口 + DB 认证计数），统一接口。

---

## 实现细节

### In-Memory 实现（OSS）

| 组件 | 实现方式 |
|---|---|
| `InMemoryHub` | 现有 `sse_hub.go` 代码原样迁入，改为实现 `Hub` 接口 |
| `InMemoryCache` | `sync.Map` + 每条记录携带 `expireAt int64`；后台 goroutine 每 30s 扫描清理 |
| `InMemoryLimiter` | HTTP 限速沿用现有滑动窗口；认证计数用 `sync.Map` 存 `{count int64, resetAt int64}` |

### Redis 实现（Cloud）

**`RedisCache`：** 直接映射到 `go-redis/v9` 的 `SET key val EX ttl` / `GET` / `DEL`。

**`RedisLimiter`：**
- 认证计数器用 Lua 脚本保证 INCR + EXPIRE 原子性：
  ```lua
  local count = redis.call('INCR', KEYS[1])
  if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
  return count
  ```
- HTTP 滑动窗口用 Redis sorted set（`ZADD` + `ZREMRANGEBYSCORE` + `ZCARD`）

**`RedisHub`（本地扇出 + Redis 扇入）：**

```
Instance A                     Redis                    Instance B
─────────                      ─────                    ─────────
RedisHub                   channel:                     RedisHub
  localSubs:             tabslate:sync:userX              localSubs:
    userX → [ch1, ch2]  ◄── SUBSCRIBE ──┐                 userX → [ch3]
                                        │
  Broadcast(userX, seq) ──► PUBLISH ───►│──────────────────► ch3 收到 seq
  同时本地扇出 ch1, ch2
```

关键约束：
- 同一实例上同一用户的多个 SSE 连接共享一个 Redis subscription（按 userID 去重），避免 N 连接 = N Redis sub
- `Subscribe`：本地记录 channel；若该 userID 尚无 Redis sub 则新建，否则加入本地 map
- `Broadcast`：`PUBLISH tabslate:sync:{userID} {seq}`，同时本地扇出到当前实例所有连接
- `Unsubscribe`：移除本地 channel；若 userID 无更多本地连接则取消 Redis sub

---

## 工厂函数与注入

### `internal/infra/infra.go`

```go
type Providers struct {
    Hub     pubsub.Hub
    Cache   store.Cache
    Limiter ratelimit.Limiter
}

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
    cleanup := func() { rdb.Close() }
    return &Providers{
        Hub:     pubsub.NewRedisHub(rdb),
        Cache:   store.NewRedisCache(rdb),
        Limiter: ratelimit.NewRedisLimiter(rdb),
    }, cleanup, nil
}
```

`InMemoryCache.Close()` 停止后台清理 goroutine（通过内部 done channel 实现）。

三个实现共享同一个 `*redis.Client`，只建一个连接池。

### `app/config.go` 变更

```go
type Config struct {
    // ... 现有字段不变
    RedisURL string // REDIS_URL 环境变量，空 = in-memory
}
```

### `app/server.go` 变更

```go
func New(cfg Config, db *db.DB, billing billing.Provider,
         infra *infra.Providers, ctx context.Context) *Server
```

内部传递：
- `handler.NewSSEHandler(db, infra.Hub, infra.Cache)`
- `handler.NewAuthHandler(db, ..., infra.Limiter, infra.Cache)`
- `middleware.NewRateLimiter(infra.Limiter)`

### `cmd/server/main.go` 变更

```go
infra, cleanup, err := infra.New(cfg.RedisURL)
if err != nil { log.Fatal(err) }
defer cleanup()

s := app.New(cfg, db, billingProvider, infra, ctx)
s.Run()
```

---

## Redis Key 规范

| 用途 | Key 格式 | TTL |
|---|---|---|
| SSE token | `tabslate:sse_token:{token}` | 30s |
| Billing limits 缓存 | `tabslate:billing:limits:{userID}` | 60s |
| 登录失败计数 | `tabslate:auth:login_fail:{email}` | 15min |
| OTP IP 计数 | `tabslate:auth:otp_ip:{ip}` | `OTP_CAPTCHA_WINDOW` |
| 注册 IP 计数 | `tabslate:auth:reg_ip:{ip}` | `REGISTER_CAPTCHA_WINDOW` |
| SSE pub/sub 频道 | `tabslate:sync:{userID}` | 无（pub/sub） |
| HTTP 限速滑窗 | `tabslate:ratelimit:{route}:{ip}` | `window` |

所有 key 统一 `tabslate:` 前缀，多服务共用同一 Redis 实例时不冲突。

---

## DB Schema 清理

移除以下内容（在 `db/schema.pg.sql` 幂等迁移块末尾添加 `DROP TABLE IF EXISTS`）：

| 删除内容 | 替代方案 |
|---|---|
| `sse_tokens` 表 | `store.Cache` KV，30s TTL |
| `login_failures` 表 | `ratelimit.Limiter.IncrCounter` |
| `otp_ip_requests` 表 | `ratelimit.Limiter.IncrCounter` |
| `register_ip_requests` 表 | `ratelimit.Limiter.IncrCounter` |
| `AuthHandler.StartCleanup` goroutine | TTL 自动过期，不再需要 |

---

## 缓存数据汇总

| 数据 | 缓存策略 | 失效方式 |
|---|---|---|
| SSE token | `store.Cache` Set on issue, Del on consume | 30s TTL 或消费时 Del |
| Billing limits | `store.Cache` 60s TTL | 自然过期（允许短暂过期数据） |
| 登录失败计数 | `ratelimit.Limiter` IncrCounter | 15min TTL；登录成功后 ResetCounter |
| OTP IP 计数 | `ratelimit.Limiter` IncrCounter | `OTP_CAPTCHA_WINDOW` TTL |
| 注册 IP 计数 | `ratelimit.Limiter` IncrCounter | `REGISTER_CAPTCHA_WINDOW` TTL |
| SSE 实时通知 | `pubsub.Hub` pub/sub | 无缓存，纯事件流 |

**不缓存的数据：**
- sync/pull 增量结果（用户特定 + 已有 `(user_id, seq)` 复合索引，查询已足够快）
- 用户基础信息（auth 中间件 JWT 验证无需查 DB；`GET /auth/me` 频率极低）
- 工作区/集合/书签数据（体积大、失效复杂、无明显热点）

---

## 环境变量新增

| 变量 | 必填 | 说明 |
|---|---|---|
| `REDIS_URL` | 否 | Redis 连接地址，格式 `redis://[:password@]host:port[/db]`；空 = in-memory 模式 |

---

## 兼容性保证

- OSS 自托管用户：不设 `REDIS_URL`，行为与现在完全一致，无需改配置
- Cloud 版：设置 `REDIS_URL` 后自动启用所有 Redis 实现
- 现有 handler 代码改动局限于构造函数签名（注入新依赖），业务逻辑不变
