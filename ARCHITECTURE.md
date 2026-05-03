# ARCHITECTURE.md

## 总体结构

TabSlate-server 是 TabSlate Chrome 扩展的后端，Go 编写，Gin + pgx/v5 + PostgreSQL 17。

```
cmd/server/main.go
  └── app.New(cfg, db, billingProvider, ctx)
        ├── internal/handler/*   HTTP handlers（各实体 + 认证 + 同步 + SSE）
        ├── internal/middleware  Auth JWT + IP 速率限制
        ├── billing.Provider     接口，OSS = local.Provider；Cloud = lago.Provider
        └── gin.Engine           路由
```

## 目录结构

```
TabSlate-server/
├── cmd/server/
│   └── main.go              # 入口：LoadConfig → db.Open → billing.New → app.New → s.Run()
│
├── app/
│   ├── config.go            # Config（LoadConfig 从环境变量读取）
│   └── server.go            # Server 结构体：New、setupCORS、setupRoutes、Run、RegisterWebhook、SyncSubscription
│
├── billing/
│   ├── provider.go          # Provider 接口：OnUserCreated/GetLimits/GetSubscription/CreateCheckout/ListInvoices/CancelSubscription
│   └── local/
│       └── provider.go      # OSS 实现：从 License JWT 读取配额；无 License = 免费限额
│
├── db/
│   ├── db.go                # DB 包装器（*pgxpool.Pool）；QueryRow/Exec/Query/BeginTx 等
│   └── schema.pg.sql        # 全量 schema（IF NOT EXISTS + ALTER TABLE 幂等迁移）
│
└── internal/
    ├── auth/                # JWT 签发/验证（HS256）、bcrypt、refresh token 生成
    ├── captcha/             # Prosopo procaptcha 验证；PROSOPO_SECRET 为空则跳过
    ├── mailer/              # 邮件发送：SMTP 或 Resend；MAIL_PROVIDER 为空则禁用
    ├── middleware/
    │   ├── auth.go          # Bearer JWT 验证中间件
    │   └── ratelimit.go     # IP 速率限制（滑动窗口，in-memory）
    ├── model/               # 请求/响应结构体、Plan 常量
    ├── plan/                # 本地 DB 配额检查（过渡期）
    └── handler/
        ├── auth.go          # 注册、登录、OTP 验证、密码重置、SSE token 签发
        ├── workspaces.go    # CRUD /workspaces
        ├── collections.go   # CRUD /collections
        ├── bookmarks.go     # CRUD /bookmarks
        ├── tags.go          # CRUD /tags
        ├── sync.go          # POST /sync/push、GET /sync/pull
        ├── sync_seq.go      # incrementSeq / currentSeq（per-user 单调序列）
        ├── sse.go           # GET /sync/stream（SSE 流）
        ├── sse_hub.go       # globalHub：in-memory pub/sub（userID → connID → chan int64）
        ├── billing.go       # GET+POST /api/subscription、/api/limits 等
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
| POST | `/sync/push` | 批量推送本地变更（512KB 限制，60 req/min） | Bearer JWT |
| GET | `/sync/pull` | 拉取指定 seq 之后的增量（120 req/min） | Bearer JWT |
| GET | `/api/subscription` | 当前订阅信息 | Bearer JWT |
| GET | `/api/limits` | 当前配额限制 | Bearer JWT |
| POST | `/api/checkout` | 创建结账会话（Cloud） | Bearer JWT |
| GET | `/api/invoices` | 账单列表（Cloud） | Bearer JWT |
| DELETE | `/api/subscription` | 取消订阅（Cloud） | Bearer JWT |

## 同步系统

### 核心设计

- **序列号**：每个用户在 `user_sync_seq` 表有单调递增计数器，每次 push 事务内 `incrementSeq` +1
- **软删除**：所有实体表（workspaces/collections/bookmarks/tags）有 `deleted_at BIGINT` 列，删除操作写 `deleted_at = now` 而非 `DELETE`
- **冲突解决（LWW）**：`ON CONFLICT (id) DO UPDATE ... WHERE updated_at < EXCLUDED.updated_at`，时间戳较大者胜出

### Push 流程

```
POST /sync/push  →  SyncHandler.Push
  1. 解析请求体（最大 512KB）
  2. 开启事务
  3. 并行检查配额（collections 上限）
  4. LWW upsert workspaces / collections / bookmarks / tags（各自独立 ON CONFLICT）
  5. incrementSeq → 新 seq
  6. 提交事务
  7. globalHub.Broadcast(userID, seq)  // 通知所有 SSE 连接
  8. 返回 { server_seq, rejected: [] }
```

### Pull 流程

```
GET /sync/pull?after_seq=N  →  SyncHandler.Pull
  1. after_seq < 0 → 400
  2. 四张表各自 SELECT WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC
  3. rows.Err() 检查
  4. currentSeq → server_seq
  5. 返回 { server_seq, entities: { workspaces, collections, bookmarks, tags } }
     （含软删除记录，deleted_at != NULL 表示墓碑）
```

### SSE 流程

```
GET /sync/stream?token=<token>  →  SSEHandler.Stream
  1. DELETE FROM sse_tokens WHERE token=$1 RETURNING user_id, expires_at
     （单次消耗，过期或不存在 → 401）
  2. 设置 SSE 响应头（text/event-stream, no-cache, X-Accel-Buffering: no）
  3. globalHub.Subscribe(userID) → connID, seqChan
  4. 事件循环：
     - seqChan 收到 seq → 写 data: {"seq": N}\n\n
     - 每 30s 写 : ping\n\n（心跳，防止代理超时）
     - 写入失败 → 退出循环
     - c.Request.Context().Done() → 退出循环
  5. globalHub.Unsubscribe(userID, connID)
```

### SSE Hub

`sse_hub.go` 中的 `globalHub` 是进程内 pub/sub：

```
Hub
  subs: map[userID] → map[connID] → chan int64（缓冲 8）
  next: atomic.Int64（connID 生成器）
```

- `Subscribe`：加 RWMutex 写锁，创建 connID + buffered channel
- `Broadcast`：加读锁，非阻塞发送（`select { case ch <- seq: default: }`），慢连接直接跳过
- `Unsubscribe`：加写锁，close channel + 清理 map

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
| `users` | id, email, password_hash, is_verified | 用户基础信息 |
| `user_sync_seq` | user_id PK, seq BIGINT | 每用户同步序列计数器 |
| `sse_tokens` | token PK, user_id, expires_at BIGINT | 30s 单次 SSE 鉴权令牌 |
| `workspaces` | id, user_id, seq, deleted_at | 含同步字段 |
| `collections` | id, user_id, workspace_id, seq, deleted_at | 含同步字段 |
| `bookmarks` | id, user_id, collection_id, seq, deleted_at, tag_ids text[] | 含同步字段；`tag_ids` 存书签关联的 Tag ID 数组 |
| `tags` | id, user_id, seq, deleted_at, updated_at | 含同步字段（updated_at 用于 LWW） |
| `refresh_tokens` | token_hash, user_id, expires_at | SHA-256 哈希存储，使用后轮换 |

**Delta-pull 索引**（`schema.pg.sql` 末尾）：
```sql
CREATE INDEX idx_workspaces_user_seq  ON workspaces  (user_id, seq);
CREATE INDEX idx_collections_user_seq ON collections (user_id, seq);
CREATE INDEX idx_bookmarks_user_seq   ON bookmarks   (user_id, seq);
CREATE INDEX idx_tags_user_seq        ON tags        (user_id, seq);
```

## 依赖注入模型

```
cmd/server/main.go
  → local.New(licenseKey)                    # OSS billing.Provider
  → app.New(cfg, db, provider, ctx)
      ├── captcha.New(cfg.ProsopoSecret, ...)
      ├── mailer.New(cfg.MailProvider, ...)
      └── handler.New*(db, ...)              # 每个 handler 持有 *db.DB；auth handler 额外持有 billing.Provider/captcha/mailer
```

Cloud 仓库只需将 `local.New(...)` 替换为 `lago.New(...)`，并调用 `s.RegisterWebhook("/webhooks/lago", lagoHandler)` 即可。

## 认证机制

| 凭证 | 算法 | 有效期 | 存储 |
|---|---|---|---|
| Access token | HMAC HS256 JWT | 7 天 | 响应体，客户端内存 |
| Refresh token | 32 字节随机，SHA-256 哈希 | 90 天，使用后轮换 | DB `refresh_tokens` |
| OTP（邮箱验证/密码重置） | 6 位随机数字，SHA-256 哈希 | 10 分钟，5 次失败后失效 | DB `users.verification_token` / `reset_otp_hash` |
| SSE token | 32 字节随机，明文 | 30 秒，单次消耗 | DB `sse_tokens` |
