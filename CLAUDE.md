# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 仓库关系

TabSlate 由三个仓库组成：

| 仓库 | 可见性 | 职责 |
|---|---|---|
| **`TabSlate`** | 公开，AGPL | Chrome 扩展前端，TypeScript + React + WXT |
| **`TabSlate-server`**（本仓库） | 公开，AGPL | Go 后端 OSS 版，可自托管，计费基于本地 License JWT |
| **`TabSlate-cloud`** | 私有 | Go 后端 Cloud 版，以本仓库为 Go module 依赖，注入 Lago 计费 |

`TabSlate-cloud` 通过 `require github.com/tabslate/server` + `replace` 指令引用本仓库，仅需替换 `billing.Provider` 实现即可获得完整后端能力。Cloud 仓库可直接导入本仓库的 `billing/`、`db/`、`app/` 公开包，`internal/` 包对外部模块不可见（Go 模块系统强制）。

---

## 项目概述

TabSlate-server 是 TabSlate Chrome 扩展的后端 OSS 版，Go 编写：
- **OSS 版**（本仓库）：MIT License，计费基于本地 License JWT，无外部依赖，支持自托管
- **Cloud 版**（私有仓库 `TabSlate-cloud`）：导入本仓库作为依赖，注入 `lago.Provider` 实现在线计费

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

## 架构要点

### 依赖注入模型

`billing.Provider` 接口是 OSS/Cloud 分叉的核心。所有需要计费逻辑的组件只接受接口，不知道底层实现：

```
cmd/server/main.go
  → local.New(licenseKey)                       # 创建 OSS Provider
  → app.New(cfg, db, provider)                  # 注入（captcha + mailer 由 Config 自动创建）
  → handler.NewAuthHandler(db, secret, provider, captcha, mailer)
```

`app.New()` 内部根据 `Config` 的 Prosopo / Mail 字段自动创建 `captcha.Verifier` 和 `mailer.Mailer`，外部调用方无需导入 `internal/` 包。Cloud 仓库的 `main.go` 只需将 `local.New(...)` 替换为 `lago.New(...)`，其余不变。`RegisterWebhook()` 供 Cloud 额外挂载 Lago webhook 路由。

### 数据库

`db.DB` 包装 `*sql.DB`，仅支持 PostgreSQL 17+（pgx/v5 驱动）。DSN 必须以 `postgres://` 或 `postgresql://` 开头，否则启动报错。

**所有 handler 的 SQL 查询必须通过 `h.db.Rebind(query)` 包装**，它将 `?` 占位符转为 PostgreSQL 的 `$1, $2...`。所有查询只写 `?`，不要直接写 `$N`。

Schema 文件嵌入在 `db/schema.pg.sql`（`//go:embed`），`db.Migrate(d)` 无需参数，幂等执行（IF NOT EXISTS）。

### 包职责

| 包 | 可见性 | 职责 |
|---|---|---|
| `billing` | 公开 | `Provider` 接口 + 共享类型（`Limits`, `Subscription`, `Invoice`）；Cloud 模块可导入 |
| `billing/local` | 公开 | OSS 实现：从 License JWT 读取配额；无 License = 免费限额 |
| `db` | 公开 | DB 包装器、双 schema embed、`Rebind`；Cloud 模块可导入 |
| `app` | 公开 | `Config`（LoadConfig）+ `Server`（路由注册）；Cloud 模块可导入 |
| `internal/handler` | 内部 | HTTP handler，每个 handler 持有 `*db.DB`，auth handler 额外持有 `billing.Provider`、`captcha.Verifier`、`mailer.Mailer` |
| `internal/captcha` | 内部 | Prosopo procaptcha 服务端验证；`PROSOPO_SECRET` 为空则跳过验证（开发模式），支持自部署 `PROSOPO_SERVER_URL` |
| `internal/mailer` | 内部 | 邮件发送，支持 SMTP 和 Resend 两种后端；`MAIL_PROVIDER` 为空则禁用（用户自动验证） |
| `internal/plan` | 内部 | 基于本地 DB 的配额检查（过渡期保留，长期应迁移到 `billing.GetLimits`） |
| `internal/auth` | 内部 | JWT 签发/验证、bcrypt、refresh token 生成 |
| `internal/middleware` | 内部 | Bearer JWT 验证中间件、IP 速率限制中间件 |
| `internal/model` | 内部 | 请求/响应结构体、`Plan` 常量 |

### 认证流程

- Access token：HMAC HS256 JWT，7 天有效
- Refresh token：32 字节随机值，SHA-256 哈希后存 DB，90 天有效，使用后立即轮换
- **OTP**：6 位随机数字，SHA-256 哈希后存 DB，有效期 10 分钟，最多 5 次错误后自动失效
- 注册流程（`POST /auth/register`）：
  1. 条件 Captcha：该 IP 在 `REGISTER_CAPTCHA_WINDOW`（默认 24h）内成功注册数 ≥ `REGISTER_CAPTCHA_THRESHOLD`（默认 3）时才要求 Captcha；设为 0 = 始终要求（第一步，失败立即返回，避免消耗 DB 资源）
  2. 密码强度校验（≥10 字符，包含字母+数字）
  3. Email 唯一性检查 → 创建用户（`is_verified=false`）
  4. 若邮件服务已配置：生成 6 位 OTP → 写入 `otp_last_sent_at = now` → 异步发送邮件，`billing.OnUserCreated` **延迟到 OTP 验证成功后**；记录 IP 到 `register_ip_requests`
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
  - **单 IP**：`OTP_CAPTCHA_WINDOW`（默认 15 分钟）内 ≥`OTP_CAPTCHA_THRESHOLD`（默认 5）次后要求 Captcha（`otp_ip_requests` 表记录）
- 找回密码：`POST /auth/forgot-password { email }` → 发 OTP；`POST /auth/reset-password { email, code, new_password }` → 验证 OTP + 更新密码 + 清除所有 refresh token

### 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | ✅ | PostgreSQL DSN，格式 `postgres://user:pass@host:5432/dbname` |
| `JWT_SECRET` | ✅ | HMAC 签名密钥，`openssl rand -hex 32` 生成 |
| `PORT` | | 监听端口，默认 `8080` |
| `GIN_MODE` | | `debug` / `release` |
| `LICENSE_KEY` | | OSS License JWT；空 = 免费模式 |
| `PROSOPO_SECRET` | | Prosopo 站点密钥（Server Secret Key）；空 = 跳过验证码 |
| `PROSOPO_SERVER_URL` | | Prosopo 服务端验证端点，默认 `https://api.prosopo.io/siteverify`；可指向自部署实例 |
| `PROSOPO_BUNDLE_URL` | | Prosopo 前端 JS bundle URL，默认 `https://js.prosopo.io/js/procaptcha.bundle.js`；自部署时覆盖 |
| `MAIL_PROVIDER` | | `smtp` 或 `resend`；空 = 禁用邮件（用户自动验证） |
| `SMTP_HOST` | | SMTP 服务器地址 |
| `SMTP_PORT` | | SMTP 端口，默认 `587` |
| `SMTP_USER` | | SMTP 用户名 |
| `SMTP_PASSWORD` | | SMTP 密码 |
| `SMTP_FROM` | | 发件人地址 |
| `RESEND_API_KEY` | | Resend API 密钥 |
| `RESEND_FROM` | | Resend 发件人，如 `TabSlate <noreply@tabslate.app>` |
| `REGISTER_CAPTCHA_THRESHOLD` | | 同 IP 在窗口内成功注册几次后要求验证码；0 = 始终要求；默认 `3` |
| `REGISTER_CAPTCHA_WINDOW` | | 注册计数窗口，Go duration 字符串（如 `24h`）；默认 `24h` |
| `OTP_CAPTCHA_THRESHOLD` | | 同 IP 在窗口内请求 OTP 邮件几次后要求验证码；0 = 始终要求；默认 `5` |
| `OTP_CAPTCHA_WINDOW` | | OTP 请求计数窗口，Go duration 字符串（如 `15m`）；默认 `15m` |

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

新的 `billing.Provider` 实现（如 Cloud 的 `lago.Provider`）必须满足以下约定：
- `OnUserCreated` 应设计为幂等（重复调用不产生副作用）
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
- `local.New(licenseKey, publicKey)` 的 `publicKey` 参数：测试可传 `nil`，**生产二进制必须嵌入真实 RSA 公钥**

### HTTP 响应中的信息泄露

- 认证失败统一返回 `"invalid email or password"`，不区分"用户不存在"和"密码错误"（防止用户枚举）
- 500 错误只返回通用消息（如 `"failed to create user"`），不返回 DB 错误原文
- Gin 在 `release` 模式下不输出调试信息，生产部署必须设置 `GIN_MODE=release`

### JWT

- 验证时必须检查签名算法是否为预期算法（代码中已有 `if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok` 检查），新增 JWT 解析路径必须保留此检查
- License JWT 使用 RSA-PSS（非对称），Access token 使用 HMAC-HS256（对称），两者用途不可混用
- Refresh token 使用后立即轮换（删除旧 hash，插入新 hash），已实现；新增 token 相关逻辑不得打破轮换机制

### Webhook（Cloud）

- Lago webhook 必须验证 `X-Lago-Signature` 签名头，拒绝签名无效的请求
- Webhook 处理函数应在验签通过后才读取 payload body，避免大量无效请求消耗内存

### CORS

当前配置允许所有 `chrome-extension://` 来源，这是有意为之（支持扩展直接调用）。若新增 Web 前端，需在 `AllowOriginFunc` 中明确列出允许的域名，不得使用 `AllowAllOrigins: true`。

---

## 注意事项

- **schema 位置**：schema 文件在 `db/schema.pg.sql`（PostgreSQL，`//go:embed` 引用），根目录的 `schema.sql` 仅作存档，不被代码引用。schema 末尾包含 `DO $$ ... ALTER TABLE ... EXCEPTION WHEN duplicate_column` 块，确保对已有数据库的兼容迁移。限流相关表：`login_failures`（登录失败）、`otp_ip_requests`（OTP 请求）、`register_ip_requests`（注册请求）均为 append-only，由 `AuthHandler.StartCleanup` 每 10 分钟清理窗口期之外的过期行。
- **后台清理 goroutine**：`AuthHandler.StartCleanup(ctx)` 在 `setupRoutes` 中启动，ctx 来自 `main.go` 的 `signal.NotifyContext`（SIGINT/SIGTERM 取消），进程退出时 goroutine 自动停止。`app.New()` 因此多接收一个 `context.Context` 参数。
- **boolean 差异**：SQLite schema 用 `INTEGER DEFAULT 0`，PG schema 用 `BOOLEAN DEFAULT FALSE`；Go 端 `model.Bookmark` 使用 `bool`，pgx 驱动自动处理类型映射
- **Cloud 扩展点**：在 `billing.Provider` 接口之外，Cloud 还可以实现 `billing.WebhookHandler` 接口，通过 `server.RegisterWebhook` 注册路由
- **Captcha Widget**：`GET /captcha/widget` 提供一个 HTML 页面（由 `internal/handler/captcha.go` 提供），Chrome MV3 扩展将其嵌入 `<iframe>`，页面从配置的 `PROSOPO_BUNDLE_URL` 加载 Prosopo JS bundle，验证完成后通过 `postMessage` 将 token 传回父页面。CSP 由服务端动态构建，`script-src`/`connect-src` 均基于 `bundleOrigin`，支持官方 CDN 和自部署两种场景。
- **OTP 安全存储**：`verification_token`（邮箱验证）和 `reset_otp_hash`（密码重置）均存 SHA-256(code)，明文 OTP 仅在发送邮件时使用一次后丢弃。`verification_attempts` / `reset_attempts` 计数器在 5 次错误后自动清空 OTP 字段，重发新 OTP 时计数器清零。
