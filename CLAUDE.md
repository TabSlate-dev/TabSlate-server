# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 仓库关系

TabSlate 由三个仓库组成：

| 仓库 | 可见性 | 职责 |
|---|---|---|
| **`TabSlate`** | 公开，AGPL | Chrome 扩展前端，TypeScript + React + WXT |
| **`TabSlate-server`**（本仓库） | 公开，AGPL-3.0 | Go 后端，AGPL 开源，用户数无上限，可自托管；禁止未经授权将本后端用于商业收费服务 |
| **`TabSlate-cloud`** | 私有 | Go 后端 Cloud 版，以本仓库为 Go module 依赖，注入 Flexprice 计费 |

`TabSlate-cloud` 通过 `require github.com/tabslate/server` + `replace` 指令引用本仓库，仅需替换 `billing.Provider` 实现即可获得完整后端能力。Cloud 仓库可直接导入本仓库的 `billing/`、`db/`、`app/` 公开包，`internal/` 包对外部模块不可见（Go 模块系统强制）。

---

## 项目概述

TabSlate-server 是 TabSlate Chrome 扩展的后端，Go 编写：
- **OSS 版**（本仓库）：AGPL-3.0 开源，用户数无上限，支持自托管；禁止使用本后端提供收费同步服务（无 TabSlate 商业授权）
- **Cloud 版**（私有仓库 `TabSlate-cloud`）：导入本仓库作为依赖，注入 `flexprice.Provider` 实现在线计费

详细架构说明见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 常用命令

```bash
go build ./...          # 编译检查
go vet ./...            # 静态分析
go test ./...           # 运行所有测试
go test ./internal/...  # 运行某个包的测试
go run ./cmd/server     # 本地启动（需要 .env 文件）
```

Docker 开发：
```bash
cp .env.example .env       # 填写 JWT_SECRET 和 DATABASE_URL
docker compose up --build
```

## API 路由概览

🔒 = 需要 `Authorization: Bearer <accessToken>` 头。Groups/saved_groups 无独立 CRUD 端点，通过 `/sync/push` 和 `/sync/pull` 同步。

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/auth/register` | 注册 |
| POST | `/auth/login` | 登录 |
| POST | `/auth/refresh` | 刷新 Access token |
| POST | `/auth/logout` | 登出 |
| POST | `/auth/verify-email` | 验证邮箱 OTP |
| POST | `/auth/resend-verification` | 重发验证 OTP |
| POST | `/auth/forgot-password` | 发送重置密码 OTP |
| POST | `/auth/reset-password` | 重置密码 |
| GET | `/auth/login-captcha-status` | 是否需要登录验证码 |
| GET | `/auth/otp-captcha-status` | OTP 重发是否需要验证码 |
| GET | `/auth/register-captcha-status` | 是否需要注册验证码 |
| GET | `/captcha/widget` | Prosopo 验证码 iframe 页 |
| GET | `/sync/stream` | SSE 实时推送（`?token=` 一次性鉴权） |
| GET | `/auth/me` 🔒 | 当前用户信息 |
| POST | `/auth/sse-token` 🔒 | 申请单次 SSE token（30s TTL） |
| GET/PUT | `/preferences` 🔒 | 用户偏好设置 |
| GET/POST/PUT/DELETE | `/workspaces[/:id]` 🔒 | 工作区 CRUD |
| GET/POST/PUT/DELETE | `/collections[/:id]` 🔒 | 集合 CRUD |
| GET/POST/PUT/DELETE | `/bookmarks[/:id]` 🔒 | 书签 CRUD |
| GET/POST/PUT/DELETE | `/tags[/:id]` 🔒 | 标签 CRUD |
| GET | `/search` 🔒 | 全文搜索（`?q=`，≥2 字符，需 MeiliSearch） |
| POST | `/sync/push` 🔒 | 增量推送变更（含 groups） |
| GET | `/sync/pull` 🔒 | 拉取服务端变更（`?since=<seq>`） |
| GET | `/api/plan` 🔒 | 当前套餐及用量 |
| GET | `/api/limits` 🔒 | 配额限制 |
| GET | `/api/subscription` 🔒 | 订阅详情 |
| POST | `/api/checkout` 🔒 | 立即切换套餐（Cloud 用，返回 `{"success": true}`） |
| GET | `/api/invoices` 🔒 | 账单列表 |
| DELETE | `/api/subscription` 🔒 | 取消订阅 |

---

## 架构要点

### 依赖注入模型

`billing.Provider` 接口是 OSS/Cloud 分叉的核心。所有需要计费逻辑的组件只接受接口，不知道底层实现：

```
cmd/server/main.go
  → local.New(database)                         # 创建 OSS Provider（无限制，订阅始终 PlanPro）
  → app.New(cfg, db, provider)                  # 注入（captcha + mailer 由 Config 自动创建）
  → handler.NewAuthHandler(db, secret, provider, captcha, mailer, ..., registrationOpen)
```

`app.New()` 内部根据 `Config` 的 Prosopo / Mail 字段自动创建 `captcha.Verifier` 和 `mailer.Mailer`，外部调用方无需导入 `internal/` 包。Cloud 仓库的 `main.go` 只需将 `local.New(...)` 替换为 `flexprice.New(...)`，并调用 `bp.ResolvePlans(ctx)` 解析套餐 UUID，其余不变。`RegisterWebhook()` 供 Cloud 额外挂载 webhook 路由。

### 数据库

`db.DB` 包装 `*sql.DB`，仅支持 PostgreSQL 17+（pgx/v5 驱动）。DSN 必须以 `postgres://` 或 `postgresql://` 开头，否则启动报错。

**所有 handler 的 SQL 查询必须通过 `h.db.Rebind(query)` 包装**，它将 `?` 占位符转为 PostgreSQL 的 `$1, $2...`。所有查询只写 `?`，不要直接写 `$N`。

Schema 文件嵌入在 `db/schema.pg.sql`（`//go:embed`），`db.Migrate(d)` 无需参数，幂等执行（IF NOT EXISTS）。

### 包职责

| 包 | 可见性 | 职责 |
|---|---|---|
| `billing` | 公开 | `Provider` 接口 + 共享类型（`Limits`, `Subscription`, `Invoice`）；Cloud 模块可导入 |
| `billing/local` | 公开 | OSS 实现：用户数无上限，订阅始终返回 PlanPro；从 `subscription_capacity` DB 表读取资源配额（默认 -1 不限制）；注册开关由 `ALLOW_REGISTRATION` 环境变量控制（在 auth handler 层检查，不在 billing 层） |
| `db` | 公开 | DB 包装器、双 schema embed、`Rebind`；Cloud 模块可导入 |
| `app` | 公开 | `Config`（LoadConfig）+ `Server`（路由注册）；Cloud 模块可导入 |
| `internal/infra` | 内部 | `Providers` 工厂：`REDIS_URL` 非空 → Redis 实现；空 → in-memory 实现 |
| `internal/pubsub` | 内部 | `Hub` 接口（Subscribe/Broadcast/Unsubscribe）；`InMemoryHub` / `RedisHub` |
| `internal/store` | 内部 | `Cache` 接口（Set/Get/Del + TTL）；`InMemoryCache` / `RedisCache` |
| `internal/ratelimit` | 内部 | `Limiter` 接口（Allow 滑动窗口 / IncrCounter / ResetCounter）；`InMemoryLimiter` / `RedisLimiter` |
| `internal/handler` | 内部 | HTTP handler；写路径 handler（workspaces/bookmarks/collections/tags/sync）各自注入 `billing.Provider`，调用 `GetLimits()` + COUNT 查询执行配额检查；auth handler 额外持有 `ratelimit.Limiter`、`store.Cache`、`captcha.Verifier`、`mailer.Mailer` |
| `internal/captcha` | 内部 | Prosopo procaptcha 服务端验证；`PROSOPO_SECRET` 为空则跳过验证（开发模式），支持自部署 `PROSOPO_SERVER_URL` |
| `internal/mailer` | 内部 | 邮件发送，支持 SMTP、Resend 和 Amazon SES 三种后端；`MAIL_PROVIDER` 为空则禁用（用户自动验证）；`SendOTP(ctx, to, name, code, purpose, lang)` 是高层 OTP 邮件方法，查询 `translations[purpose][lang]`（fallback "en"），渲染 `templates/otp.html`（`embed.FS` 嵌入，`html/template`），再调 `Send()`；`lang` 由 `parseLang(Accept-Language)` 从请求头提取，支持 "en" / "zh" |
| `internal/auth` | 内部 | JWT 签发/验证、bcrypt、refresh token 生成 |
| `internal/middleware` | 内部 | Bearer JWT 验证中间件；`RateLimitByIP(limiter, limit, window)` 接受 `ratelimit.Limiter` 接口 |
| `internal/model` | 内部 | 请求/响应结构体、`Plan` 常量 |

### 认证流程

- Access token：HMAC HS256 JWT，7 天有效
- Refresh token：32 字节随机值，SHA-256 哈希后存 DB，90 天有效，使用后立即轮换
- **OTP**：6 位随机数字，SHA-256 哈希后存 DB，有效期 10 分钟，最多 5 次错误后自动失效
- 注册流程（`POST /auth/register`）：
  1. 条件 Captcha：该 IP 在 `REGISTER_CAPTCHA_WINDOW`（默认 24h）内成功注册数 ≥ `REGISTER_CAPTCHA_THRESHOLD`（默认 3）时才要求 Captcha；设为 0 = 始终要求（第一步，失败立即返回，避免消耗 DB 资源）
  2. 密码强度校验（≥10 字符，包含字母+数字）
  3. Email 唯一性检查 → 创建用户（`is_verified=false`）
  4. 若邮件服务已配置：生成 6 位 OTP → 写入 `otp_last_sent_at = now` → 异步发送邮件，`billing.OnUserCreated` **延迟到 OTP 验证成功后**；通过 Limiter 记录 IP 注册计数（key = `tabslate:auth:reg_ip:<ip>`）
  5. 若邮件服务未配置：用户自动验证，立即调 `billing.OnUserCreated`
- 登录流程（`POST /auth/login`）：
  1. 条件 Captcha：同一邮箱 15 分钟内失败 ≥5 次后才要求 Captcha
  2. 登录成功后清除该邮箱的失败记录
  3. 若用户 `is_verified=false` 且邮件服务已配置：检查 60s `otp_last_sent_at` 冷却，已过则自动生成新 OTP 发送（确保进入验证码界面时始终有未过期的 OTP）
- 邮箱 OTP 验证（`POST /auth/verify-email { email, code }`）：
  - 最多 5 次错误，超限后 OTP 字段清空（`verification_attempts` 计数）
  - 验证成功后触发 `billing.OnUserCreated`，前端通过 `GET /auth/me` 获取最新 `is_verified` 状态
- OTP 重发限流（`POST /auth/resend-verification`、`POST /auth/forgot-password`）：
  - **单邮箱**：60 秒冷却，超出返回 `429 + retry_after`
  - **单 IP**：`OTP_CAPTCHA_WINDOW`（默认 15 分钟）内 ≥`OTP_CAPTCHA_THRESHOLD`（默认 5）次后要求 Captcha（通过 Limiter 计数，key = `tabslate:auth:otp_ip:<ip>`）
- 找回密码：`POST /auth/forgot-password { email }` → 发 OTP；`POST /auth/reset-password { email, code, new_password }` → 验证 OTP + 更新密码 + 清除所有 refresh token

### 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | ✅ | PostgreSQL DSN，格式 `postgres://user:pass@host:5432/dbname` |
| `JWT_SECRET` | ✅ | HMAC 签名密钥，`openssl rand -hex 32` 生成 |
| `PORT` | | 监听端口，默认 `8080` |
| `GIN_MODE` | | `debug` / `release` |
| `ALLOW_REGISTRATION` | | `true`（默认）/ `false`；`false` 时 `POST /auth/register` 返回 403，禁止新用户注册 |
| `PROSOPO_SECRET` | | Prosopo 站点密钥（Server Secret Key）；空 = 跳过验证码 |
| `PROSOPO_SERVER_URL` | | Prosopo 服务端验证端点，默认 `https://api.prosopo.io/siteverify`；可指向自部署实例 |
| `PROSOPO_BUNDLE_URL` | | Prosopo 前端 JS bundle URL，默认 `https://js.prosopo.io/js/procaptcha.bundle.js`；自部署时覆盖 |
| `MAIL_PROVIDER` | | `smtp`、`resend` 或 `ses`；空 = 禁用邮件（用户自动验证） |
| `SMTP_HOST` | | SMTP 服务器地址 |
| `SMTP_PORT` | | SMTP 端口，默认 `587` |
| `SMTP_USER` | | SMTP 用户名 |
| `SMTP_PASSWORD` | | SMTP 密码 |
| `SMTP_FROM` | | 发件人地址 |
| `RESEND_API_KEY` | | Resend API 密钥 |
| `RESEND_FROM` | | Resend 发件人，如 `TabSlate <noreply@tabslate.com>` |
| `SES_ACCESS_KEY_ID` | | AWS access key ID（`MAIL_PROVIDER=ses` 时必填） |
| `SES_SECRET_KEY` | | AWS secret access key（`MAIL_PROVIDER=ses` 时必填） |
| `SES_REGION` | | SES 所在 AWS region，如 `us-east-1`（`MAIL_PROVIDER=ses` 时必填） |
| `SES_FROM` | | SES 发件人，如 `TabSlate <noreply@tabslate.com>`（`MAIL_PROVIDER=ses` 时必填） |
| `REGISTER_CAPTCHA_THRESHOLD` | | 同 IP 在窗口内成功注册几次后要求验证码；0 = 始终要求；默认 `3` |
| `REGISTER_CAPTCHA_WINDOW` | | 注册计数窗口，Go duration 字符串（如 `24h`）；默认 `24h` |
| `OTP_CAPTCHA_THRESHOLD` | | 同 IP 在窗口内请求 OTP 邮件几次后要求验证码；0 = 始终要求；默认 `5` |
| `OTP_CAPTCHA_WINDOW` | | OTP 请求计数窗口，Go duration 字符串（如 `15m`）；默认 `15m` |
| `REDIS_URL` | | Redis 连接串（如 `redis://localhost:6379`）；空 = 全部使用 in-memory 实现（OSS 单机模式） |
| `TRUSTED_PROXIES` | | Comma-separated trusted proxy IPs/CIDRs for `c.ClientIP()` resolution. Defaults to RFC1918 ranges (`172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`). Set empty to trust only `RemoteAddr`. |
| `TRASH_GRACE_DAYS` | | 垃圾桶自动过期天数（state 1 → 2）；默认 `7` |
| `MEILISEARCH_HOST` | | MeiliSearch 实例内部 URL（如 `http://meilisearch:7700`）；空 = 禁用全文搜索 |
| `MEILISEARCH_API_KEY` | | MeiliSearch master/admin API key（对应 `MEILI_MASTER_KEY`） |
| `RATE_LIMIT_AUTH` | | auth 端点每窗口最大请求数/IP；默认 `10` |
| `RATE_LIMIT_AUTH_WINDOW` | | auth 限流窗口，Go duration；默认 `1m` |
| `RATE_LIMIT_SYNC_PUSH` | | POST /sync/push 每窗口最大请求数/IP；默认 `60` |
| `RATE_LIMIT_SYNC_PUSH_WINDOW` | | sync push 限流窗口；默认 `1m` |
| `RATE_LIMIT_SYNC_PULL` | | GET /sync/pull 每窗口最大请求数/IP；默认 `120` |
| `RATE_LIMIT_SYNC_PULL_WINDOW` | | sync pull 限流窗口；默认 `1m` |
| `RATE_LIMIT_SEARCH` | | GET /search 每窗口最大请求数/IP；默认 `60` |
| `RATE_LIMIT_SEARCH_WINDOW` | | search 限流窗口；默认 `1m` |

## 编码规范

### 错误处理

使用 `%w` 包装可向上传播的错误，`%v` 用于终止处（不再向上传递）：

```go
// ✅ 内部函数：包装，保留调用链
return nil, fmt.Errorf("lago create customer: %w", err)

// ✅ handler 最终层：不向客户端暴露内部错误细节
c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})

// ❌ 不要将 DB/第三方错误原文返回给客户端（泄露 schema、DSN 等信息）
c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
```

`errors.Is` / `errors.As` 用于错误判断，不要字符串匹配。

### Context 传播

DB 查询必须使用带 `Context` 的变体，传入请求的 `c.Request.Context()`，确保客户端断开时查询可被取消：

```go
// ✅
h.db.QueryRowContext(c.Request.Context(), h.db.Rebind(`SELECT ...`), args...)
h.db.ExecContext(c.Request.Context(), h.db.Rebind(`INSERT ...`), args...)

// ❌ 无法被取消，长查询会造成 goroutine 泄漏
h.db.QueryRow(h.db.Rebind(`SELECT ...`), args...)
```

`billing.Provider` 的方法已接受 `context.Context`，同样传 `c.Request.Context()`。

### SQL 查询

- 所有带参数的查询必须经过 `h.db.Rebind(query)`，占位符只用 `?`
- 禁止任何形式的字符串拼接构造 SQL（`fmt.Sprintf`、`+` 拼接）
- 多步骤写操作必须在事务中执行，使用 `defer tx.Rollback()` + 显式 `tx.Commit()`
- 查询结果的 `rows.Close()` 错误应检查（用 `defer` 时可忽略返回值，但不要遗漏 `defer rows.Close()`）

### 接口设计

新的 `billing.Provider` 实现（如 Cloud 的 `flexprice.Provider`）必须满足以下约定：
- `OnUserCreated` 应设计为幂等（重复调用不产生副作用）。Cloud 的 `flexprice.Provider` 通过两层 guard 实现：① 进程内 `sync.Map` 防止同一 userID 并发重入；② Flexprice 原生幂等（`POST /customers` 409 = 已存在继续，`POST /subscriptions/search` 有活跃订阅则跳过创建）防止重复创建。
- 实现类型应在包级别通过编译期断言验证接口：`var _ billing.Provider = (*Provider)(nil)`
- `GetLimits` 的实现应带本地缓存，避免每次请求都打外部 API

### 包与命名

- 构造函数统一命名为 `New` 或 `NewXxx`，返回具体类型（不返回接口）
- handler 结构体字段均为小写（非导出），只通过 `NewXxx` 创建
- 测试文件放在同包（`package handler`）或黑盒（`package handler_test`），均可
- `internal/` 下的包不得被 `cmd/` 以外的外部模块直接导入（Go module 系统已强制）

---

## 安全规范

### SQL 注入

唯一允许的查询方式是参数化查询。以下模式**绝对禁止**：

```go
// ❌ 禁止
query := "SELECT * FROM users WHERE email = '" + email + "'"
query := fmt.Sprintf("SELECT * FROM users WHERE id = %s", id)

// ✅ 强制
h.db.QueryRowContext(ctx, h.db.Rebind(`SELECT * FROM users WHERE email = ?`), email)
```

### 密码与凭证

- 密码使用 `bcrypt.GenerateFromPassword`（`bcrypt.DefaultCost = 10`），比较用 `bcrypt.CompareHashAndPassword`，两者均为恒定时间操作，不要替换为其他算法
- 密码强度要求：≥10 字符，包含至少一个字母和一个数字（`validatePasswordStrength`）
- Refresh token 只存 SHA-256 哈希，原始值仅在响应中返回一次后丢弃（已实现）
- JWT secret 生产环境必须 ≥ 32 字节随机值（`openssl rand -hex 32`），不得硬编码

### HTTP 响应中的信息泄露

- 认证失败统一返回 `"invalid email or password"`，不区分"用户不存在"和"密码错误"（防止用户枚举）
- 500 错误只返回通用消息（如 `"failed to create user"`），不返回 DB 错误原文
- Gin 在 `release` 模式下不输出调试信息，生产部署必须设置 `GIN_MODE=release`

### JWT

- 验证时必须检查签名算法是否为预期算法（代码中已有 `if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok` 检查），新增 JWT 解析路径必须保留此检查
- Refresh token 使用后立即轮换（删除旧 hash，插入新 hash），已实现；新增 token 相关逻辑不得打破轮换机制

### Webhook（Cloud）

- Cloud 版当前使用 Flexprice 计费，Webhook 通过 Svix（Standard Webhooks）推送，签名验证使用 `svix-id`、`svix-timestamp`、`svix-signature` 三个头部。
- 接收 Webhook 时须先验签再读 payload body，`WebhookSecret` 为空时跳过验签（开发环境）

### CORS

当前配置允许所有 `chrome-extension://` 来源，这是有意为之（支持扩展直接调用）。若新增 Web 前端，需在 `AllowOriginFunc` 中明确列出允许的域名，不得使用 `AllowAllOrigins: true`。

---

## 注意事项

- **schema 位置**：schema 文件在 `db/schema.pg.sql`（PostgreSQL，`//go:embed` 引用）。所有列已合并到 `CREATE TABLE` 定义，新增列通过 `ALTER TABLE … ADD COLUMN IF NOT EXISTS` 追加以兼容已存在的表（幂等）。原有限流表（`login_failures`、`otp_ip_requests`、`register_ip_requests`、`sse_tokens`）不存在于 schema，对应状态由 `internal/ratelimit` 和 `internal/store` 管理（in-memory 或 Redis）。`groups` / `group_tabs` 两张表：`groups` 含 `seq`/`deleted_at`/`is_deleted`/`workspace_id` 同步字段；`group_tabs` 以 `group_id REFERENCES groups(id) ON DELETE CASCADE` 外键关联，tab 列表无 seq，整体快照替换。`subscription_capacity` 表：以 `plan_code` 为主键，存储各套餐的配额限制；OSS 版在启动时写入 `unlimited` 行（所有字段 -1）；Cloud 版（Flexprice）不使用此表，配额直接从 Flexprice Entitlement API 实时获取。`users.billing_synced_at BIGINT`：历史字段，由旧 Polar/Unibee provider 写入；Flexprice provider 不读写此列，幂等性改由 Flexprice 原生 409 + 订阅搜索保证。
- **三态删除**：`bookmarks.is_trashed INT`（0=active, 1=trashed, 2=permanently deleted）、`collections.is_deleted INT`、`groups.is_deleted INT` 含义相同。`model.Bookmark.IsTrashed` / `model.Collection.IsDeleted` / `model.Group.IsDeleted` 均为 `int`。`BookmarkRequest.IsTrashed` 仍为 `bool`（REST CRUD 路径，由 `boolToInt()` 转换为 int 写入 model）。Pull 响应原样返回 state=2 记录，供其他设备 `mergeFromServer` 删除本地副本。
- **Cloud 扩展点**：在 `billing.Provider` 接口之外，Cloud 还可以实现 `billing.WebhookHandler` 接口，通过 `server.RegisterWebhook` 注册路由
- **Captcha Widget**：`GET /captcha/widget` 提供一个 HTML 页面（由 `internal/handler/captcha.go` 提供），Chrome MV3 扩展将其嵌入 `<iframe>`，页面从配置的 `PROSOPO_BUNDLE_URL` 加载 Prosopo JS bundle，验证完成后通过 `postMessage` 将 token 传回父页面。CSP 由服务端动态构建，`script-src`/`connect-src` 均基于 `bundleOrigin`，支持官方 CDN 和自部署两种场景。**安全约束**：`frame-ancestors` 限制为 `chrome-extension:` 仅允许扩展页面嵌入；`postMessage` target 使用客户端通过 `parentOrigin` 查询参数传入的 `chrome-extension://` 来源（前端 `procaptcha.tsx` 传 `window.location.origin`），无该参数时回退 `'*'`（本地调试用）。`PUT /preferences` 请求体通过 `http.MaxBytesReader` 限制为 64 KB，超限返回 413。
- **OTP 安全存储**：`verification_token`（邮箱验证）和 `reset_otp_hash`（密码重置）均存 SHA-256(code)，明文 OTP 仅在发送邮件时使用一次后丢弃。`verification_attempts` / `reset_attempts` 计数器在 5 次错误后自动清空 OTP 字段，重发新 OTP 时计数器清零。
- **同步事务要求**：`SyncHandler.Push` 必须在 **`pgx.Serializable` 隔离级别**的单个事务中完成配额检查 + 所有 upsert + `incrementSeq`；禁止降低隔离级别（READ COMMITTED 下并发推送的配额 SELECT 存在 TOCTOU，导致超配额）或将配额检查移到事务外。Serializable 事务在高并发下可能产生 `40001 serialization_failure`（PostgreSQL abort），客户端 SyncQueue 的退避重试会自动处理。**配额检查实现**：事务内对受限类型（workspaces/collections/groups）各执行一条 SELECT 预取所有活跃 ID 到 `map[string]struct{}`（collections 条件 `is_deleted < 2`，workspaces/groups 条件 `deleted_at IS NULL`），随后逐实体在 O(1) 内判断是否为新增；仅当 push payload 包含该类型时才执行对应预取查询，不在 payload 中出现的类型跳过。配额上限由 `billing.Provider.GetLimits()` 返回（事务外调用一次，结果在循环内复用）。groups 的 Push 包含两步：LWW upsert `groups` 行（`WHERE groups.updated_at < excluded.updated_at`）+ 原子替换 `group_tabs`（`DELETE FROM group_tabs WHERE group_id = $id` 后 bulk INSERT），两步均在同一事务内执行；stale group 被拒绝时跳过其 tab 替换。
- **SSE token 单次消耗**：`POST /auth/sse-token` 将 `uuid → userID` 写入 Cache（key = `tabslate:sse_token:<token>`，TTL = 30s）；`SSEHandler.Stream` 调用 `cache.Get` 取得 userID，随即 `cache.Del` 消耗 token（不存在 → 401）。
- **SSE Hub**：`internal/pubsub.Hub` 接口，OSS 单机使用 `InMemoryHub`，Cloud 多实例使用 `RedisHub`（Redis pub/sub，channel = `tabslate:sync:<userID>`）。`infra.New(REDIS_URL)` 自动选择实现并注入所有需要广播的 handler。
- **软删除传播**：所有同步实体（workspaces/collections/bookmarks/tags/groups）的删除操作写 `deleted_at = unix_ms`，Pull 响应含墓碑（`deleted_at != NULL`），客户端负责从本地移除对应记录；直接 `DELETE` 不会传播到其他设备。groups 软删除时 `group_tabs` 通过 `ON DELETE CASCADE` 自动清理；Pull 仍返回墓碑 group 行（`tabs: []`）供客户端清理本地。
- **Collection `is_default` 字段**：`model.Collection` 含 `IsDefault bool \`json:"is_default"\`` 字段，由 Pull handler 的 CTE 计算（每个 workspace 中 `position` 最小的活跃集合 `is_default = true`，archived/trashed 集合不参与计算），**不存入 DB 列**，是查询时派生的只读字段。前端 `workspace-store.mergeFromServer` 读取此字段直接写入本地 state，不再本地推算 default 集合。
- **Tag 模型缺少 UpdatedAt**：`model.Tag` 结构体无 `UpdatedAt` 字段，Pull handler 的 tag SELECT 因此只取 6 列（无 `updated_at`）；LWW 通过 `seq` 而非 `updated_at` 实现。
- **Bookmark tag_ids**：`bookmarks` 表有 `tag_ids text[] NOT NULL DEFAULT '{}'` 列，存储该书签关联的 Tag ID 数组。`model.Bookmark.TagIDs []string` 对应此列；Push upsert 和 Pull SELECT 均包含 `tag_ids`；pgx 原生支持 `[]string ↔ text[]` 扫描，无需额外包。

---

## Karpathy 编码行为准则

> 源自 [Andrej Karpathy 关于 LLM 编码陷阱的观察](https://x.com/karpathy/status/2015883857489522876)。这些准则偏向谨慎而非速度，对于琐碎任务请自行判断。

### 1. 编码前思考

**不要假设。不要隐藏困惑。呈现权衡。**

实现前：
- 明确说明假设。不确定时，询问而非猜测。
- 若存在多种解释，全部呈现——不要默默选择。
- 若有更简单的方案，说出来。应该提出异议时就提。
- 遇到不清楚的地方，停下来，指出困惑所在，要求澄清。

### 2. 简洁优先

**用最少代码解决问题。不做推测性实现。**

- 不添加未被要求的功能。
- 不为一次性代码创建抽象层。
- 不添加未被要求的"灵活性"或"可配置性"。
- 不为不可能发生的场景做错误处理。
- 如果 200 行代码可以写成 50 行，重写它。

自问：资深工程师会觉得这过于复杂吗？如果是，简化。

### 3. 精准修改

**只碰必须碰的。只清理自己造成的混乱。**

编辑现有代码时：
- 不要"改进"相邻代码、注释或格式。
- 不要重构没坏的东西。
- 匹配现有风格，即使你更倾向于不同写法。
- 发现无关死代码，提一下——不要删除它。

当你的改动产生孤儿代码时：
- 删除因你的改动而变得无用的导入/变量/函数。
- 不删除已有死代码，除非被要求。

检验标准：每一行修改都应能直接追溯到用户请求。

### 4. 目标驱动执行

**定义成功标准。循环验证直到达成。**

将任务转化为可验证目标：
- "添加验证" → "为无效输入编写测试，然后让它们通过"
- "修复 bug" → "编写重现 bug 的测试，然后让它通过"
- "重构 X" → "确保重构前后测试都能通过"

多步骤任务先说明计划：
```
1. [步骤] → 验证: [检查]
2. [步骤] → 验证: [检查]
```

**判断标准：** diff 中不必要改动更少，因过度复杂导致的重写更少，澄清问题在实现前提出。
