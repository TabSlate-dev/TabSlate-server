# ARCHITECTURE.md

## 总体结构

TabSlate-server 是 TabSlate Chrome 扩展的后端，Go 编写，Gin + pgx/v5 + PostgreSQL 17。

```
cmd/server/main.go
  └── app.New(cfg, db, billingProvider, ctx)
        ├── internal/infra       Hub / Cache / Limiter 工厂（REDIS_URL 为空 = in-memory）
        ├── internal/handler/*   HTTP handlers（各实体 + 认证 + 同步 + SSE）
        ├── internal/middleware  Auth JWT + IP 速率限制
        ├── billing.Provider     接口，OSS = local.Provider；Cloud = meteroid.Provider
        └── gin.Engine           路由
```

## 目录结构

```
TabSlate-server/
├── cmd/server/
│   └── main.go              # 入口：LoadConfig → db.Open → local.New → bp.Start → app.New → s.Run()
│
├── app/
│   ├── config.go            # Config（LoadConfig 从环境变量读取）
│   └── server.go            # Server 结构体：New、setupCORS、setupRoutes、Run、RegisterWebhook、SyncSubscription
│
├── billing/
│   ├── types.go             # 共享类型：Limits（MaxWorkspaces/MaxBookmarks/MaxCollections/MaxTags/MaxSavedGroups/TrashGraceDays），Subscription，Invoice
│   ├── provider.go          # Provider 接口：OnUserCreated/GetLimits/GetSubscription/CreateCheckout/ListInvoices/CancelSubscription
│   └── local/
│       ├── provider.go      # OSS 实现：keygen.sh License 验证用户数上限；超限用户自动暂停（suspended_at）+ 吊销 refresh token；实现 billing.InstanceLimiter
│       ├── keygen.go        # keygenClient：FetchLicense / ActivateMachine / ValidateMachine；KeygenAPIURL + KeygenAccountID 编译时写入（-ldflags -X）
│       └── license_cache.go # licenseCache：TTL 缓存 keygenLicense；maxUsers() 返回 License metadata 中的用户数上限（或 Free 默认 3）
│
├── db/
│   ├── db.go                # DB 包装器（*pgxpool.Pool）；QueryRow/Exec/Query/BeginTx 等
│   └── schema.pg.sql        # 全量 schema（所有列合并到 CREATE TABLE，无迁移补丁）
│
└── internal/
    ├── auth/                # JWT 签发/验证（HS256）、bcrypt、refresh token 生成
    ├── captcha/             # Prosopo procaptcha 验证；PROSOPO_SECRET 为空则跳过
    ├── mailer/              # 邮件发送：SMTP、Resend 或 Amazon SES（SigV4）；MAIL_PROVIDER 为空则禁用
    ├── infra/
    │   └── infra.go         # Providers 工厂：REDIS_URL 非空 → Redis；空 → in-memory（OSS 单机）
    ├── middleware/
    │   ├── auth.go          # Bearer JWT 验证中间件
    │   └── ratelimit.go     # RateLimitByIP(limiter, limit, window)：接受 ratelimit.Limiter 接口
    ├── model/               # 请求/响应结构体、Plan 常量
    ├── pubsub/
    │   ├── hub.go           # Hub 接口：Subscribe / Broadcast / Unsubscribe
    │   ├── memory.go        # InMemoryHub（OSS 单机）
    │   └── redis.go         # RedisHub（多实例，Redis pub/sub，key = tabslate:sync:<userID>）
    ├── ratelimit/
    │   ├── limiter.go       # Limiter 接口：Allow（滑动窗口）/ IncrCounter / ResetCounter / GetCounter
    │   ├── memory.go        # InMemoryLimiter（OSS 单机）
    │   └── redis.go         # RedisLimiter（多实例；Allow 用 sorted-set Lua 脚本，IncrCounter 用原子 Lua 脚本）
    ├── search/
    │   ├── types.go         # BookmarkDoc（MeiliSearch 文档结构）
    │   └── client.go        # Client：nil-safe 包装器；MEILISEARCH_HOST 为空时返回 nil（禁用）
    ├── store/
    │   ├── cache.go         # Cache 接口：Set / Get / Del（带 TTL）
    │   ├── memory.go        # InMemoryCache（lazy 过期 + 30s 后台清扫）
    │   └── redis.go         # RedisCache（TTL 由 Redis 原生管理）
    └── handler/
        ├── auth.go          # 注册、登录、OTP 验证、密码重置、SSE token 签发
        ├── workspaces.go    # CRUD /workspaces
        ├── collections.go   # CRUD /collections
        ├── bookmarks.go     # CRUD /bookmarks；Create/Update/Delete 后触发 MeiliSearch upsert/delete（fire-and-forget）
        ├── tags.go          # CRUD /tags
        ├── sync.go          # POST /sync/push、GET /sync/pull；Push 提交后批量触发 MeiliSearch 更新
        ├── sync_seq.go      # incrementSeq / currentSeq（per-user 单调序列）
        ├── cleanup.go       # CleanupHandler：后台 goroutine，每 24h 两阶段清理（见下文）
        ├── search.go        # GET /search?q=（书签全文搜索；最少 2 个 Unicode 字符；需 Bearer JWT）
        ├── sse.go           # GET /sync/stream（SSE 流；通过 pubsub.Hub 接收广播）
        ├── billing.go       # GET /api/plan（subscription+limits+usage 汇总）、/api/subscription、/api/limits（60s 缓存）、/api/checkout、/api/invoices、DELETE /api/subscription
        └── captcha.go       # GET /captcha/widget、/captcha/widget.js
```

## 路由表

| 方法 | 路径 | 说明 | 认证 |
|---|---|---|---|
| POST | `/auth/register` | 注册（条件 captcha） | 无 |
| POST | `/auth/login` | 登录（条件 captcha） | 无 |
| POST | `/auth/refresh` | Refresh token 换新 access token | 无 |
| POST | `/auth/logout` | 吊销 refresh token | 无 |
| POST | `/auth/verify-email` | OTP 邮箱验证 | 无 |
| POST | `/auth/resend-verification` | 重发 OTP（60s 冷却） | 无 |
| POST | `/auth/forgot-password` | 发送密码重置 OTP | 无 |
| POST | `/auth/reset-password` | 验证 OTP + 重置密码 | 无 |
| GET | `/auth/login-captcha-status` | 是否需要登录 captcha | 无 |
| GET | `/auth/otp-captcha-status` | 是否需要 OTP captcha | 无 |
| GET | `/auth/register-captcha-status` | 是否需要注册 captcha | 无 |
| GET | `/captcha/widget` | Prosopo iframe widget HTML | 无 |
| GET | `/captcha/widget.js` | Prosopo bundle proxy | 无 |
| GET | `/sync/stream` | SSE 实时通知流（token 鉴权） | SSE token |
| GET | `/auth/me` | 当前用户信息 | Bearer JWT |
| POST | `/auth/sse-token` | 签发 30s 单次 SSE token | Bearer JWT |
| GET/POST/PUT/DELETE | `/workspaces` | 工作区 CRUD | Bearer JWT |
| GET/POST/PUT/DELETE | `/collections` | 集合 CRUD | Bearer JWT |
| GET/POST/PUT/DELETE | `/bookmarks` | 书签 CRUD | Bearer JWT |
| GET/POST/PUT/DELETE | `/tags` | 标签 CRUD | Bearer JWT |
| POST | `/sync/push` | 批量推送本地变更（512KB 限制） | Bearer JWT |
| GET | `/sync/pull` | 拉取指定 seq 之后的增量 | Bearer JWT |
| GET | `/search` | 全文搜索书签（`?q=`，最少 2 字符，MeiliSearch） | Bearer JWT |
| GET | `/api/plan` | 套餐 + 配额上限 + 当前使用量汇总 | Bearer JWT |
| GET | `/api/subscription` | 当前订阅信息 | Bearer JWT |
| GET | `/api/limits` | 当前配额上限（60s 缓存） | Bearer JWT |
| POST | `/api/checkout` | 创建结账会话（Cloud） | Bearer JWT |
| GET | `/api/invoices` | 账单列表（Cloud） | Bearer JWT |
| DELETE | `/api/subscription` | 取消订阅（Cloud） | Bearer JWT |

## 同步系统

### 核心设计

- **序列号**：每个用户在 `user_sync_seq` 表有单调递增计数器，每次 push 事务内 `incrementSeq` +1
- **软删除**：所有实体表有 `deleted_at BIGINT` 列，删除操作写 `deleted_at = now` 而非 `DELETE`
- **永久删除三态**：bookmarks 的 `is_trashed INT`、collections/groups 的 `is_deleted INT`：`0`=active，`1`=soft-deleted（回收站），`2`=permanently deleted（墓碑）。客户端 `permanentlyDelete*` 推送 state=2；Pull 响应原样返回 state=2 记录供其他设备同步删除。**服务端级联**：Push 处理 collection `is_deleted=2` 时，服务端自动将该集合下所有 `is_trashed < 2` 的书签更新为 `is_trashed=2`（防止客户端未推送书签 tombstone 时产生孤儿书签）
- **冲突解决（LWW）**：`ON CONFLICT (id) DO UPDATE ... WHERE updated_at < EXCLUDED.updated_at`，时间戳较大者胜出

### Push 流程

```
POST /sync/push  →  SyncHandler.Push
  1. 解析请求体（最大 512KB）
  2. 开启事务
  3. 调用 `billing.GetLimits()` 获取配额上限（事务外，结果复用于全部循环）；对 quota 受限类型（workspaces/collections/groups）在事务内**预取**所有活跃实体 ID 到内存 map（每类型一条查询，仅当 push payload 中有该类型时触发）；后续逐实体检查 `count >= limit` 时在 O(1) map 内完成，避免 per-entity COUNT（已消除 O(n) 查询风暴）
  4. LWW upsert workspaces / collections / bookmarks / tags（各自独立 ON CONFLICT）
     + LWW upsert groups（同样 ON CONFLICT + WHERE updated_at 守卫）
     + 原子替换 group_tabs：DELETE WHERE group_id = $id，然后 bulk INSERT（stale group 被拒绝则跳过）
  5. incrementSeq → 新 seq
  6. 提交事务
  7. h.hub.Broadcast(userID, seq)       // 通知所有 SSE 连接（in-memory 或 Redis pub/sub）
  8. 对成功 upsert 的书签触发 MeiliSearch upsert/delete（事务提交后，fire-and-forget）
  9. 返回 { server_seq, rejected: [] }
     rejected 项结构：{ id, reason, type? }；reason = "quota_exceeded" 时携带 type = "collection" | "saved_group"
```

### Pull 流程

```
GET /sync/pull?after_seq=N  →  SyncHandler.Pull
  1. after_seq < 0 → 400
  2. 五张表各自 SELECT WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC
     groups 用 LEFT JOIN group_tabs 聚合 tabs（ANY($1) 批量取 tab，groupIdx map 分发）
  3. rows.Err() 检查
  4. currentSeq → server_seq
  5. 返回 { server_seq, entities: { workspaces, collections, bookmarks, tags, groups } }
     （含软删除记录，deleted_at != NULL 表示墓碑；软删除 group 的 tabs 字段为 []）
```

### SSE 流程

```
GET /sync/stream?token=<token>  →  SSEHandler.Stream
  1. cache.Get("tabslate:sse_token:<token>") → userID（miss 或 err → 401）
     cache.Del("tabslate:sse_token:<token>")  // 单次消耗
  2. 设置 SSE 响应头（text/event-stream, no-cache, X-Accel-Buffering: no）
  3. hub.Subscribe(userID) → connID, seqChan
  4. 事件循环：
     - seqChan 收到 seq → 写 data: {"seq": N}\n\n
     - 每 30s 写 : ping\n\n（心跳，防止代理超时）
     - 写入失败 → 退出循环
     - c.Request.Context().Done() → 退出循环
  5. hub.Unsubscribe(userID, connID)
```

### Hub（pubsub 包）

`internal/pubsub.Hub` 接口，两种实现：

| 实现 | 使用场景 | 说明 |
|---|---|---|
| `InMemoryHub` | OSS 单机（`REDIS_URL` 未设置） | 进程内 map + buffered channel（缓冲 8） |
| `RedisHub` | Cloud / 多实例 | Redis pub/sub，channel key = `tabslate:sync:<userID>` |

- `Subscribe`：返回 `(connID int64, ch <-chan int64)`
- `Broadcast`：快照当前订阅者 channel 列表，释放锁后非阻塞发送（慢消费者直接跳过）
- `Unsubscribe`：关闭 channel，清理 map；Redis 模式下最后一个连接离开时取消订阅
- `infra.New()` 根据 `REDIS_URL` 自动选择实现并返回 cleanup 函数

### 垃圾桶自动清理 Goroutine（cleanup.go）

`CleanupHandler` 随 `app.New()` 以 goroutine 启动，绑定 server context：

```
每 24h（启动时立即执行第一次）：

Phase 1 — 自动过期（state 1 → 2）：
  UNION 查询找出所有 deleted_at < (now - TRASH_GRACE_DAYS) 的 state=1 记录的用户
  per-user 事务：incrementSeq + UPDATE is_trashed/is_deleted = 2
  → 产生新 seq，确保其他设备的下次 delta-pull 能收到 state=2 墓碑

Phase 2 — 硬删除（state 2，已过墓碑窗口）：
  DELETE WHERE is_trashed/is_deleted = 2 AND deleted_at < (now - TRASH_GRACE_DAYS - 7 days)
  顺序：bookmarks → collections → groups（遵守 FK 依赖）
  每步失败则中止后续步骤（防止 FK 孤儿）
```

- `TRASH_GRACE_DAYS` 环境变量（默认 7）控制 Phase 1 触发时机
- 7 天墓碑窗口为固定常量，不可通过环境变量调整（协议决策，非运维决策）

### 序列号辅助函数（sync_seq.go）

```go
incrementSeq(ctx, tx pgx.Tx, userID) (int64, error)
  // UPDATE user_sync_seq SET seq = seq + 1 WHERE user_id = $1 RETURNING seq
  // 必须在已开启的事务内调用

currentSeq(ctx, d *db.DB, userID) (int64, error)
  // SELECT seq FROM user_sync_seq WHERE user_id = $1
  // 用于 Pull 响应的 server_seq 字段
```

## 数据库 Schema 要点

| 表 | 关键列 | 说明 |
|---|---|---|
| `users` | id, email, password_hash, is_verified, **suspended_at BIGINT** | 用户基础信息；`suspended_at` 非空 = 已被 License 限制暂停，禁止登录/刷新 token |
| `server_config` | key TEXT PK, value TEXT | 服务端持久化 KV；目前仅存 `license_machine_fingerprint`（UUIDv4，keygen.sh 机器激活用） |
| `user_sync_seq` | user_id PK, seq BIGINT | 每用户同步序列计数器 |
| `workspaces` | id, user_id, seq, deleted_at | 含同步字段 |
| `collections` | id, user_id, workspace_id, seq, deleted_at, archived_at, **is_deleted INT** | `archived_at` 非空 = 已归档；`is_deleted`: 0/1/2 三态 |
| `bookmarks` | id, user_id, collection_id, seq, deleted_at, tag_ids text[], **is_trashed INT** | `is_trashed`: 0/1/2 三态；`tag_ids` 存书签关联的 Tag ID 数组 |
| `tags` | id, user_id, seq, deleted_at, updated_at | 含同步字段（updated_at 用于 LWW） |
| `groups` | id, user_id, workspace_id, seq, deleted_at, **is_deleted INT** | `is_deleted`: 0/1/2 三态；软删除保留行 |
| `group_tabs` | id, group_id FK→groups, title, url, favicon, position | 组内 tab；ON DELETE CASCADE；无 seq，整体快照替换 |
| `refresh_tokens` | token_hash, user_id, expires_at | SHA-256 哈希存储，使用后轮换 |
| `subscription_capacity` | plan_code PK, plan_id, max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days, updated_at | 套餐配额；OSS 写 `unlimited`（全 -1）；Cloud 由 Meteroid 同步写入；-1 = 不限制 |

**Delta-pull 索引**（`schema.pg.sql` 末尾）：
```sql
CREATE INDEX idx_workspaces_user_seq  ON workspaces  (user_id, seq);
CREATE INDEX idx_collections_user_seq ON collections (user_id, seq);
CREATE INDEX idx_bookmarks_user_seq   ON bookmarks   (user_id, seq);
CREATE INDEX idx_tags_user_seq        ON tags        (user_id, seq);
CREATE INDEX idx_groups_user_seq      ON groups      (user_id, seq);
CREATE INDEX idx_group_tabs_group     ON group_tabs  (group_id);
```

## MeiliSearch 搜索索引

`internal/search.Client` 是一个 nil-safe 包装器：

- `MEILISEARCH_HOST` 为空 → `search.New()` 返回 `nil`，所有方法均为 no-op，服务正常启动但不索引
- 非空 → 在 `bookmarks` 索引上设置 `FilterableAttributes: ["userId"]`，`SearchableAttributes: ["title", "url", "description"]`
- `UpsertBookmark` / `DeleteBookmark` — 单条 fire-and-forget goroutine（REST 路径：`/bookmarks` CRUD）
- `BulkUpsertAsync` / `BulkDeleteAsync` — 批量 fire-and-forget goroutine（sync push 路径：将当次 push 中所有成功的书签一次性提交到索引，避免 N-goroutine / N-connection 风暴）；失败时记录日志但不影响 HTTP 响应
- `SearchBookmarks` 在查询时强制追加 `Filter: userId = "<userID>"`，确保跨用户数据隔离

**触发时机：**

| 事件 | 操作 |
|---|---|
| `POST /bookmarks`（Create） | UpsertBookmark |
| `PUT /bookmarks/:id`（Update，非 trashed） | UpsertBookmark |
| `PUT /bookmarks/:id`（Update，is_trashed=true） | DeleteBookmark |
| `DELETE /bookmarks/:id`（软删除） | DeleteBookmark |
| `POST /sync/push`（Push，书签 upsert 成功） | 批量 Upsert 或 Delete（提交后） |

**冷启动注意：** MeiliSearch 的 `UpdateSettings` 是异步任务，极短时间内的首次搜索请求可能因 `userId` 尚未变为 filterable 而返回 500。通常在数秒内自动恢复。

## 依赖注入模型

```
cmd/server/main.go
  → local.New(licenseKey, database)  # OSS billing.Provider；licenseKey 空 = Free（3 用户）
  → bp.Start(ctx)                    # 机器激活 + 初始 License 同步 + 后台刷新 goroutine（1h）
  → app.New(cfg, db, provider, ctx)
      ├── infra.New(cfg.RedisURL)            # Providers{Hub, Cache, Limiter}；空 = in-memory
      ├── captcha.New(cfg.ProsopoSecret, ...)
      ├── mailer.New(cfg.MailProvider, ...)  # smtp | resend | ses | "" (disabled)
      ├── search.New(cfg.MeiliSearchHost, cfg.MeiliSearchAPIKey)  # nil if not configured
      └── handler.New*(db, infra, search, ...)  # 各 handler 注入 Hub/Cache/Limiter
```

Cloud 仓库只需将 `local.New(...)` 替换为 `meteroid.New(...)`，调用 `bp.Start(ctx)` 启动容量同步 goroutine，并设置 `REDIS_URL` 即可实现水平扩展。

## 认证机制

| 凭证 | 算法 | 有效期 | 存储 |
|---|---|---|---|
| Access token | HMAC HS256 JWT | 7 天 | 响应体，客户端内存 |
| Refresh token | 32 字节随机，SHA-256 哈希 | 90 天，使用后轮换 | DB `refresh_tokens` |
| OTP（邮箱验证/密码重置） | 6 位随机数字，SHA-256 哈希 | 10 分钟，5 次失败后失效 | DB `users.verification_token` / `reset_otp_hash` |
| SSE token | UUID v4，明文 | 30 秒，单次消耗 | Cache（`tabslate:sse_token:<token>`） |
