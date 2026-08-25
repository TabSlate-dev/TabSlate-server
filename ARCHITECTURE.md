# ARCHITECTURE.md

## 总体结构

TabSlate-server 是 TabSlate Chrome 扩展的后端，Go 编写，Gin + pgx/v5 + PostgreSQL 17。

```
cmd/server/main.go
  └── app.New(cfg, db, billingProvider, ctx)
        ├── internal/infra       Hub / Cache / Limiter 工厂（REDIS_URL 为空 = in-memory）
        ├── internal/handler/*   HTTP handlers（各实体 + 认证 + 同步 + SSE）
        ├── internal/middleware  Auth JWT + IP 速率限制
        ├── billing.Provider     接口，OSS = local.Provider；Cloud = flexprice.Provider
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
│   ├── provider.go          # Provider 接口：OnUserCreated/GetLimits/GetSubscription/ChangePlan/ListInvoices/CancelSubscription
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
    │   ├── mailer.go        # Mailer 结构体；New() 解析嵌入模板；SendOTP(ctx, to, name, code, purpose, lang) 查翻译 → 渲染 otp.html → Send()
    │   └── templates/
    │       └── otp.html     # 品牌化 HTML 邮件模板（embed.FS 嵌入二进制）；变量：{{.Name/Heading/Intro/Code/Note/PrivacyText/PrivacyURL/TermsText/TermsURL}}
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
        ├── auth.go          # 注册、登录、OTP 验证、密码重置、SSE token 签发；parseLang(acceptLang) 将 Accept-Language 映射为 "zh"/"en"；sendOTPEmail 读取 Accept-Language 后在 goroutine 外提取 lang，再调 mailer.SendOTP
        ├── workspaces.go    # List/Create/Update（普通 CRUD）+ Delete/Restore/PermanentlyDelete（三者统一委托给 WorkspaceLifecycleService.Apply；见下文「Workspace 生命周期」）
        ├── workspace_lifecycle.go  # WorkspaceLifecycleService：Workspace 三态转换的唯一权威实现，REST 路由与 sync push v2 lifecycle_action 与 Cleanup 的过期回收都调用同一个 Apply/ApplyInTx
        ├── collections.go   # CRUD /collections；所有读写都要求父 Workspace `is_deleted=0`（active-parent-join，见下文）
        ├── bookmarks.go     # CRUD /bookmarks；Create/Update/Delete 后触发 MeiliSearch upsert/delete（fire-and-forget）；同样要求父 Collection 与祖先 Workspace 均 `is_deleted=0`
        ├── tags.go          # CRUD /tags
        ├── sync.go          # POST /sync/push（协议版本 0/1/2 三态兼容，见下文）、GET /sync/pull；Push 提交后批量触发 MeiliSearch 更新
        ├── sync_dependencies.go  # entityIDSet / parentAvailability / retainedQuota / classifyParent(Rejection)：push 内父子可用性判定与配额记账的共享小工具
        ├── sync_seq.go      # incrementSeq / currentSeq（per-user 单调序列）
        ├── cleanup.go       # CleanupHandler：后台 goroutine，每 24h 四阶段清理 + Workspace 按套餐保留期过期（见下文）
        ├── search.go        # GET /search?q=（书签全文搜索；最少 2 个 Unicode 字符；需 Bearer JWT）；visibleBookmarkIDs 用 PostgreSQL join 过滤 MeiliSearch 可能滞后返回的、父 Collection/Workspace 已不可见的书签
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
| GET/POST/PUT | `/workspaces` | 工作区 List/Create/Update | Bearer JWT |
| DELETE | `/workspaces/:id` | 软删除（Delete lifecycle action，state 0→1） | Bearer JWT |
| POST | `/workspaces/:id/restore` | 恢复（Restore lifecycle action，state 1→0；legacy 行按 `deletion_model` 走级联恢复） | Bearer JWT |
| DELETE | `/workspaces/:id/permanent` | 永久删除（Purge lifecycle action，state →2，descendant 硬删除） | Bearer JWT |
| GET/POST/PUT/DELETE | `/collections` | 集合 CRUD | Bearer JWT |
| GET/POST/PUT/DELETE | `/bookmarks` | 书签 CRUD | Bearer JWT |
| GET/POST/PUT/DELETE | `/tags` | 标签 CRUD | Bearer JWT |
| POST | `/sync/push` | 批量推送本地变更（512KB 限制） | Bearer JWT |
| GET | `/sync/pull` | 拉取指定 seq 之后的增量 | Bearer JWT |
| GET | `/search` | 全文搜索书签（`?q=`，最少 2 字符，MeiliSearch） | Bearer JWT |
| GET | `/api/plan` | 套餐 + 配额上限 + 当前使用量汇总 | Bearer JWT |
| GET | `/api/subscription` | 当前订阅信息 | Bearer JWT |
| GET | `/api/limits` | 当前配额上限（60s 缓存） | Bearer JWT |
| POST | `/api/checkout` | 立即切换套餐，返回 `{"success": true}`（Cloud） | Bearer JWT |
| GET | `/api/invoices` | 账单列表（Cloud） | Bearer JWT |
| DELETE | `/api/subscription` | 取消订阅（Cloud） | Bearer JWT |

## 同步系统

### 核心设计

- **序列号**：每个用户在 `user_sync_seq` 表有单调递增计数器，每次 push 事务内 `incrementSeq` +1
- **软删除**：所有实体表有 `deleted_at BIGINT` 列，删除操作写 `deleted_at = now` 而非 `DELETE`
- **永久删除三态**：bookmarks 的 `is_trashed INT`、collections/groups/**workspaces** 的 `is_deleted INT`：`0`=active，`1`=soft-deleted（回收站/保留期），`2`=permanently deleted（墓碑）。客户端 `permanentlyDelete*` 推送 state=2；Pull 响应原样返回 state=2 记录供其他设备同步删除。**服务端级联**：Push 处理 collection `is_deleted=2` 时，服务端自动将该集合下所有 `is_trashed < 2` 的书签更新为 `is_trashed=2`（防止客户端未推送书签 tombstone 时产生孤儿书签）。Workspace 的三态转换（含其 collections/bookmarks/groups 的级联可见性）由 `WorkspaceLifecycleService` 独立管理，见下文「Workspace 生命周期」——这是本次工作区管理重构新增的、workspaces 表特有的行为，其它四张表仍是本节描述的扁平三态
- **冲突解决（LWW）**：`ON CONFLICT (id) DO UPDATE ... WHERE updated_at < EXCLUDED.updated_at`，时间戳较大者胜出

### Push 流程

```
POST /sync/push  →  SyncHandler.Push
  1. 解析请求体（最大 512KB，`protocol_version` 省略/0 = legacy 客户端；实体数量上限 1000）
  2. 开启 Serializable 事务
  3. 调用 `billing.GetLimits()` 获取配额上限（事务外，结果复用于全部循环）；对 quota 受限类型（workspaces/collections/groups）在事务内**预取**所有活跃实体 ID 到内存 map（每类型一条查询，仅当 push payload 中有该类型时触发）；后续逐实体检查 `count >= limit` 时在 O(1) map 内完成，避免 per-entity COUNT（已消除 O(n) 查询风暴）
  4. incrementSeq → 本次 push 唯一的新 seq（无论后续多少实体被拒绝，只增一次）
  5. Workspace 处理（按 protocol_version 三态兼容，详见「Workspace 生命周期」）：
       protocol_version == 0（legacy）且 `deleted_at != null` → 走 `WorkspaceLifecycleService.ApplyInTx(..., Delete, deletionModel=0, seq, now)`（级联软删除 collections/bookmarks/groups）
       protocol_version == 2 且 `lifecycle_action` 非空       → `ApplyInTx(..., action, deletionModel=1, seq, now)`（parent-tombstone：只改 workspace 行）
       其余（普通 metadata upsert，任意版本）                  → LWW `ON CONFLICT` upsert（`is_deleted` 不受影响，已删除的 workspace 会被 active-parent 校验拒绝）
  6. LWW upsert collections / bookmarks / tags（各自独立 ON CONFLICT，均要求父实体通过 classifyParentRejection 校验）
     + LWW upsert groups（同样 ON CONFLICT + WHERE updated_at 守卫）
     + 原子替换 group_tabs：DELETE WHERE group_id = $id，然后 bulk INSERT（stale group 被拒绝则跳过）
  7. 提交事务
  8. h.hub.Broadcast(userID, seq)       // 通知所有 SSE 连接（in-memory 或 Redis pub/sub）
  9. 对成功 upsert 的书签触发 MeiliSearch upsert/delete（事务提交后，fire-and-forget）
 10. 返回 { server_seq, rejected: [] }
     rejected 项结构：{ id, reason, type, parent_id?, parent_type? }
     reason 取值：
       "quota_exceeded"                                    — type = "workspace" | "collection" | "bookmark" | "saved_group"
       "stale"                                             — LWW 竞争落败（updated_at 不够新）或引用不存在的 workspace
       "invalid_parent"                                    — 父实体 ID 未提供（且不允许 nil）或未知
       "parent_rejected"                                   — 父实体在本次 push 中同样被拒绝，子实体连带拒绝
       model.RejectionReasonLastActiveWorkspace  = "last_active_workspace"  — 用户仅剩一个 active workspace，禁止删除
       model.RejectionReasonWorkspaceDeleted     = "workspace_deleted"     — 父 workspace 处于 state 1（回收站/保留期）
       model.RejectionReasonParentDeleted        = "parent_deleted"       — 同上，用于非 workspace 顶层实体的父链
       model.RejectionReasonPermanentlyDeleted   = "permanently_deleted"  — 目标或父链已在 state 2（终态墓碑）
```

### Pull 流程

```
GET /sync/pull?after_seq=N  →  SyncHandler.Pull
  1. after_seq < 0 → 400
  2. 五张表各自 SELECT WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC
     workspaces 额外返回 is_deleted、deletion_model（state 2 行的 name/icon/color/position 已在写入时被清空，见下文）
     collections 额外用 CTE 计算 is_default（每个 workspace 下 position 最小的 active collection）
     groups 用 LEFT JOIN group_tabs 聚合 tabs（ANY($1) 批量取 tab，groupIdx map 分发）
  3. rows.Err() 检查
  4. currentSeq → server_seq
  5. 返回 { server_seq, entities: {...}, capabilities: { workspace_parent_tombstone: true } }
     （含软删除记录，deleted_at != NULL 表示墓碑；软删除 group 的 tabs 字段为 []；capabilities 是本次重构新增字段，始终为 true——所有生产环境服务端都已实现 parent-tombstone 语义，字段的存在本身就是客户端探测「服务端已升级」的信号，见下文 Capability 协商）
```

## Workspace 生命周期（三态 + 迁移）

这是本次工作区管理重构的核心：Workspace 拥有独立于其它四张表的三态生命周期，其转换（含 collections/bookmarks/groups 的可见性级联）由单一权威 `internal/handler/workspace_lifecycle.go` 中的 `WorkspaceLifecycleService` 实现。REST 路由（`workspaces.go`）、sync push 的 protocol_version 2 `lifecycle_action`、legacy（version 0）delete/restore、以及 `CleanupHandler` 的保留期过期任务，**全部**调用同一个 `Apply` / `ApplyInTx`——不存在第二套状态机实现。

### 终态表

| `is_deleted` | 含义 | `deleted_at` | `deletion_model` | 子实体（collections/bookmarks/groups） |
|---|---|---|---|---|
| `0` | active | `NULL` | 不变（保留上一次的值，默认 `1`） | 正常可见，`is_deleted`/`is_trashed` 各自独立 |
| `1` | 回收站 / 保留期中（retained） | 设置为删除时刻 | `0`=legacy（旧客户端发起，子实体已被级联软删除）；`1`=parent-tombstone（新协议，只有 workspace 行本身被标记，子实体行不变但对所有 REST/搜索/配额查询不可见——见下文 active-parent-join） | legacy：随 workspace 级联为 `is_deleted/is_trashed=1`；parent-tombstone：子实体行的 `is_deleted/is_trashed` 值不变，可见性完全由 join 到 workspace 的 `is_deleted=0` 决定 |
| `2` | 永久删除（终态墓碑） | 保留（不清空，供 Pull 判定为墓碑） | 不再有意义（终态） | **物理删除**：`applyWorkspacePurge` 硬删除 `group_tabs → groups → bookmarks → collections`（按 FK 依赖顺序），workspace 行本身**保留**但被清空为墓碑形状：`name=''、icon=NULL、color=NULL、position=0`，仅 `id/user_id/seq/deleted_at/is_deleted/deletion_model` 有意义 |

`is_deleted=2` 是**吸收态**：`ApplyInTx` 对任何目标 state 已为 2 的 workspace，无论请求的 action 是什么，只有 `Purge` 会被当作幂等 no-op（返回当前 seq、`Changed=false`）放行，其余任何 action（`Delete`、`Restore`，包括来自离线设备的迟到推送）一律被拒绝，reason = `permanently_deleted`。这个检查在 `ApplyInTx` 最前面、先于 action 的 switch 语句执行，因此对 Delete/Restore 一视同仁。

### protocol_version 2 action 合约

`SyncWorkspaceMutation.lifecycle_action`（`"delete" | "restore" | "purge"`，`model.WorkspaceLifecycleAction`）：

```
Delete（state 0 → 1，deletion_model 写 1，parent-tombstone）
  前置条件：目标不是用户唯一的 active workspace（activeCount<=1 → reject "last_active_workspace"）
  幂等：目标已是 state 1 且 deletion_model 匹配请求方期望 → no-op；deletion_model 不匹配（如与 legacy 冲突）→ reject "stale"

Restore（state 1 → 0）
  实际执行路径由**数据库当前存的** deletion_model 决定，而非调用方传入的参数：
    deletion_model == 0（legacy 行）→ applyLegacyRestoreInTx：按「保存时的 seq 证据」级联恢复 collections/bookmarks/groups
    deletion_model == 1（parent-tombstone）→ applyWorkspaceRestore：只清 workspace 行的 is_deleted/deleted_at，子实体本来就没被动过
  幂等：目标已是 state 0 → no-op

Purge（state 1 → 2）
  前置条件：目标必须已经是 state 1（state 0 直接 purge → reject "stale"，防止误删活跃工作区）
  无 last-active 限制（用户可以永久删除自己最后一个工作区）
  幂等：目标已是 state 2 → no-op，返回当前 seq
```

三个 action 共用 `FOR UPDATE ... ORDER BY id` 行锁 + `pgx.Serializable` 隔离级别（见下文「串行化顺序」），确保并发调用（例如两台设备同时 delete + restore 同一个 workspace）不会产生撕裂状态。

### legacy（版本迁移）路径

`protocol_version` 省略或为 `0` 的客户端（迁移前的旧扩展版本）没有 `lifecycle_action` 字段概念：

- **删除**：`sync.go` 检测到 `ProtocolVersion == 0 && ws.DeletedAt != nil` 时，直接调用 `ApplyInTx(..., Delete, deletionModel=0, ...)`——强制走 `applyLegacyDeleteInTx`，与 workspace 行当前存的 `deletion_model` 无关。级联更新 collections/bookmarks/groups 为 `is_deleted/is_trashed=1`（真正修改子实体行，不是 parent-tombstone）。
- **恢复**：legacy 客户端没有独立的“恢复”推送格式；REST `POST /workspaces/:id/restore`（协议版本无关）委托给同一个 `Apply(..., Restore, ...)`，`ApplyInTx` 读到该行的 `deletion_model == 0` 后自动路由到 `applyLegacyRestoreInTx`，按删除时刻的 seq 作为证据下限（`seq >= workspace.Seq`）挑选候选 collections/bookmarks/groups 级联恢复——独立于本次删除、更早就已经处于回收站的记录（seq 低于证据下限）不会被误恢复。
- 因此“legacy cascade”不是由客户端标记出来的，而是由服务端在 restore 时刻读到的 `deletion_model` 列**追溯**出来的；一旦某个 workspace 曾经历过 legacy delete，它此后的 restore 语义就固定为级联恢复，直到成功恢复为 state 0（此时 `deletion_model` 被写回 `1`，之后的删除若走 protocol v2 就变回 parent-tombstone）。

### Capability 协商与 full-pull 迁移

`SyncPullResponse.capabilities.workspace_parent_tombstone`（`model.SyncCapabilities`）目前恒为 `true`——服务端始终支持 parent-tombstone 语义。这个字段存在的意义是让前端在首次连接到已升级的服务端时，能够区分「服务端从不支持三态」与「服务端支持但这次 pull 还没有相关数据」，从而决定是否需要触发一次 IndexedDB 升级 + 全量重新拉取（`after_seq=0`）来补齐本地状态机需要的字段（如 `is_deleted`、`deletion_model`）。该迁移的本地存储细节（IndexedDB `tabslate-db` v3 的新增索引/键）属于 TabSlate 前端仓库，此处不重复；服务端侧只需保证：Pull 响应无论 `after_seq` 是否为 0，都会带上完整的 `is_deleted`/`deletion_model`/`deleted_at` 字段，使全量重拉是幂等且自描述的。

### 可见性：active-parent-join

Workspace 处于 state 1 或 2 时，其下的 collections/bookmarks/groups **行本身可能仍是 active**（parent-tombstone 语义下子实体行不会被改写），因此所有 REST 读写路径都必须显式 JOIN 到 workspace 并要求 `w.is_deleted=0`，否则会把已回收站/已终态的 workspace 下的内容错误地暴露出来：

- `collections.go`（List/Create/Update/Delete）、`bookmarks.go`（List/Create/Update/Delete）：每条 SQL 都带 `JOIN workspaces w ON w.id=c.workspace_id AND w.user_id=c.user_id ... AND w.is_deleted=0`
- `search.go` 的 `visibleBookmarkIDs`：MeiliSearch 返回的候选 ID 集合，在写回响应前用同样的三层 JOIN（bookmarks → collections → workspaces，三者 `is_deleted`/`is_trashed` 均需为 0）做二次校验，过滤掉索引侧因异步更新滞后而返回的、父 Collection 或祖先 Workspace 已不再可见的书签命中
- sync push 的 `ownedWorkspaceIDs`/`unavailableWorkspaceIDs`（`sync_dependencies.go` 的 `classifyParent`）在内存中实现同样的判定，避免在同一事务内对刚被拒绝的父实体重复查库

### 串行化顺序

`WorkspaceLifecycleService.ApplyInTx` 对同一用户的**所有** workspace 行执行 `SELECT ... WHERE user_id=$1 AND is_deleted<2 ORDER BY id FOR UPDATE`（而不仅仅锁目标行），原因是 `Delete` 需要在同一把锁下计算 `activeCount`（用于 last-active 校验），避免两个并发 Delete 请求都读到「还有 ≥2 个 active」而同时把最后两个 workspace 都删掉。加上外层 `pgx.Serializable` 事务隔离级别，构成对同一用户 workspace 状态机的全序化：任意两次 `Apply` 调用（无论来自 REST、push、还是 Cleanup）在提交顺序上等价于串行执行。

### 配额方程

Workspace 配额与其它四类实体遵循同一条规则：`billing.Limits.MaxWorkspaces == -1` 时不限制；否则以 `COUNT(*) WHERE user_id=$1 AND is_deleted < 2` 为准——state 0（active）与 state 1（保留期）**都占用配额**，只有 Purge 把行推进到 state 2 才释放。

- `POST /workspaces`（REST Create）：`workspaces.go` 直接执行上述 COUNT 查询
- `POST /sync/push`：`sync.go` 用 `newRetainedQuota(limits.MaxWorkspaces)` 在事务内预取所有 `is_deleted<2` 行的 `updated_at`，随后逐实体 `Admit` 判定（terminal=true，即本次请求把该行推向 state 2，走「归还容量」分支；否则走「新增/续用容量」分支），避免 O(n) COUNT
- `GET /api/plan`（`billing.go`）：`workspace_usage` CTE 用同一个 `is_deleted < 2` 过滤计算 `usage.workspaces`；`trash_usage.workspaces` 额外统计 `is_deleted = 1`（仅保留期，不含终态）
- Purge 释放容量的两条独立路径：① push v2 的 `WorkspaceLifecyclePurge` 分支在内存 quota 结构上调用 `ReleaseApplied`（连带释放其下 collections/bookmarks/groups 的配额占用，因为它们被物理删除了）；② REST `/workspaces/:id/permanent` 与 Cleanup 的过期任务不维护内存 quota 账本——它们直接把 DB 行推进到 state 2，下一次任何配额检查重新 COUNT 时自然反映新值。两条路径最终读到的配额数字完全一致，因为都以同一份 `is_deleted` 列为准

### Cleanup 归属

`CleanupHandler.runOnce`（`cleanup.go`）按固定顺序执行四个阶段，Workspace 的保留期过期插在 phase1 和 phase2 之间，是独立于四阶段编号之外、由同一个 `expireWorkspaces` 方法负责的一段：

```
phase1   — bookmarks/collections/groups 的 state 1→2（按用户维度批量提升 + incrementSeq，不涉及 workspaces 表）
expireWorkspaces — 按用户套餐的 TrashGraceDays 遍历所有 state=1 workspace，逐个调用
                    h.lifecycle.Apply(ctx, userID, workspaceID, WorkspaceLifecyclePurge, 1)
                    这与 REST /workspaces/:id/permanent 调用的是**同一个** *WorkspaceLifecycleService 实例/同一段生产代码，
                    唯一区别是触发者（定时任务 vs 用户请求）与传入的 deletionModel 参数（此参数对 Purge 分支不影响其判定逻辑）
phase2   — DELETE bookmarks/collections/groups WHERE state=2 AND deleted_at < 过墓碑窗口
            显式排除 workspaces 表：state=2 的 workspace 根行是 expireWorkspaces 已经清空过的永久墓碑，
            不应该也不需要被 phase2 再次处理（它已经不含任何子实体，本身也已经被清空为最小形状）
phase3   — 发送账号注销 3 天提醒邮件
phase4   — 硬删除已过 30 天宽限期的账号
```

`expireWorkspaces` 对每个受影响用户单独调用 `billing.GetLimits`（`TrashGraceDays < 0` 表示无限期保留，跳过该用户所有候选）；单个 workspace 的 purge 失败（如触发器/约束异常）只记录到 `errors.Join` 返回的聚合错误里，不阻塞同批次其它用户或其它 workspace，下一次 24h 循环会重试——因为失败的 workspace 行仍停留在 state 1，天然可重入。

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

`CleanupHandler` 随 `app.New()` 以 goroutine 启动，绑定 server context，每 24h 跑一轮 `runOnce`（启动时立即执行第一次）。完整的四阶段 + Workspace 过期顺序、以及它与 REST 永久删除路由共享同一个 `WorkspaceLifecycleService` 的细节见上文「Workspace 生命周期 → Cleanup 归属」；本节只覆盖 bookmarks/collections/groups（非 workspaces）的两阶段清理：

```
Phase 1 — 自动过期（state 1 → 2，仅 bookmarks/collections/groups）：
  UNION 查询找出所有 deleted_at < (now - TRASH_GRACE_DAYS) 的 state=1 记录的用户
  per-user 事务：incrementSeq + UPDATE is_trashed/is_deleted = 2
  → 产生新 seq，确保其他设备的下次 delta-pull 能收到 state=2 墓碑

Phase 2 — 硬删除（state 2，已过墓碑窗口，仅 bookmarks/collections/groups）：
  DELETE WHERE is_trashed/is_deleted = 2 AND deleted_at < (now - TRASH_GRACE_DAYS - 7 days)
  顺序：bookmarks → collections → groups（遵守 FK 依赖）
  每步失败则中止后续步骤（防止 FK 孤儿）
  workspaces 表被显式排除：它的 state=2 墓碑清理由 expireWorkspaces（Purge）负责，见上文
```

- `TRASH_GRACE_DAYS` 环境变量（默认 7，`CleanupHandler.trashGraceDays` 字段）控制 Phase 1/Phase 2 的触发时机，对**所有用户统一生效**
- 7 天墓碑窗口（`tombstoneWindowDays`）为固定常量，不可通过环境变量调整（协议决策，非运维决策）
- **与 Workspace 过期的区别**：`expireWorkspaces` 不使用这个全局 `TRASH_GRACE_DAYS`，而是逐用户调用 `billing.GetLimits(ctx, userID).TrashGraceDays`——OSS 默认套餐与 Cloud 各付费档位可以配置不同的 Workspace 保留期；`TrashGraceDays < 0` 表示该用户的 Workspace 永不自动过期

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
| `workspaces` | id, user_id, seq, deleted_at, **is_deleted INT**, **deletion_model SMALLINT** | `is_deleted`: 0/1/2 三态（详见「Workspace 生命周期」）；`deletion_model`: `0`=legacy（子实体已级联软删除）、`1`=parent-tombstone（默认，子实体行不受影响，仅 workspace 行改变）；CHECK 约束 `is_deleted IN (0,1,2)`、`deletion_model IN (0,1)`；state=2 时 `name/icon/color/position` 已被 `applyWorkspacePurge` 清空为墓碑形状 |
| `collections` | id, user_id, workspace_id, seq, deleted_at, archived_at, **is_deleted INT** | `archived_at` 非空 = 已归档；`is_deleted`: 0/1/2 三态 |
| `bookmarks` | id, user_id, collection_id, seq, deleted_at, tag_ids text[], **is_trashed INT** | `is_trashed`: 0/1/2 三态；`tag_ids` 存书签关联的 Tag ID 数组 |
| `tags` | id, user_id, seq, deleted_at, updated_at | 含同步字段（updated_at 用于 LWW） |
| `groups` | id, user_id, workspace_id, seq, deleted_at, **is_deleted INT** | `is_deleted`: 0/1/2 三态；软删除保留行 |
| `group_tabs` | id, group_id FK→groups, title, url, favicon, position | 组内 tab；ON DELETE CASCADE；无 seq，整体快照替换 |
| `refresh_tokens` | token_hash, user_id, expires_at | SHA-256 哈希存储，使用后轮换 |
| `subscription_capacity` | plan_code PK, plan_id, max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days, updated_at | 套餐配额；OSS 写 `unlimited`（全 -1）；Cloud（Flexprice）不使用此表，配额从 Entitlement API 实时读取；-1 = 不限制 |

**Delta-pull 索引**（`schema.pg.sql` 末尾）：
```sql
CREATE INDEX idx_workspaces_user_seq  ON workspaces  (user_id, seq);
CREATE INDEX idx_collections_user_seq ON collections (user_id, seq);
CREATE INDEX idx_bookmarks_user_seq   ON bookmarks   (user_id, seq);
CREATE INDEX idx_tags_user_seq        ON tags        (user_id, seq);
CREATE INDEX idx_groups_user_seq      ON groups      (user_id, seq);
CREATE INDEX idx_group_tabs_group     ON group_tabs  (group_id);
```

**Workspace 生命周期专用索引**（`schema.pg.sql`，随 `is_deleted`/`deletion_model` 列一起迁移进来）：
```sql
CREATE INDEX idx_workspaces_user_deleted ON workspaces (user_id, is_deleted);
CREATE INDEX idx_workspaces_retention    ON workspaces (user_id, is_deleted, deleted_at);
```
`idx_workspaces_user_deleted` 服务于 `WorkspaceLifecycleService.ApplyInTx` 的 `FOR UPDATE ... WHERE user_id=$1 AND is_deleted<2` 行锁查询与所有 active-parent-join；`idx_workspaces_retention` 服务于 `expireWorkspaces` 的 `WHERE is_deleted=1 AND deleted_at IS NOT NULL` 候选扫描。迁移脚本本身是幂等的一次性 `UPDATE ... SET is_deleted=1, deletion_model=0 WHERE deleted_at IS NOT NULL`（通过 `schema_migrations` 表的 `workspace_parent_tombstone_v1` 记录只跑一次）——把重构前所有已软删除的 workspace 统一标记为 legacy（`deletion_model=0`），保证它们后续按级联恢复语义处理，与它们被删除时的真实行为（旧代码原本就是级联软删除）保持一致。

## MeiliSearch 搜索索引

`internal/search.Client` 是一个 nil-safe 包装器：

- `MEILISEARCH_HOST` 为空 → `search.New()` 返回 `nil`，所有方法均为 no-op，服务正常启动但不索引
- 非空 → 在 `bookmarks` 索引上设置 `FilterableAttributes: ["userId"]`，`SearchableAttributes: ["title", "url", "description"]`
- `UpsertBookmark` / `DeleteBookmark` — 单条 fire-and-forget goroutine（REST 路径：`/bookmarks` CRUD）
- `BulkUpsertAsync` / `BulkDeleteAsync` — 批量 fire-and-forget goroutine（sync push 路径：将当次 push 中所有成功的书签一次性提交到索引，避免 N-goroutine / N-connection 风暴）；失败时记录日志但不影响 HTTP 响应
- `SearchBookmarks` 在查询时强制追加 `Filter: userId = "<userID>"`，确保跨用户数据隔离
- **索引延迟与可见性**：`BulkUpsertAsync`/`BulkDeleteAsync` 是 fire-and-forget 的，MeiliSearch 索引状态与 PostgreSQL 之间存在短暂窗口（例如书签所在的 workspace 刚被删除/永久删除，索引还没来得及收到对应的 delete 事件）。`SearchHandler.Search` 因此绝不直接信任 MeiliSearch 返回的候选集：`visibleBookmarkIDs` 用一次 PostgreSQL JOIN（bookmarks → collections → workspaces，三层 `deleted_at IS NULL`/`is_deleted=0`/`is_trashed=0` 均需满足）对候选 ID 做二次过滤，索引侧的滞后本身不会造成数据泄漏，只会造成命中数暂时性偏少（新书签还没入索引）而不会偏多（已不可见的书签绝不会穿透到响应）

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

Cloud 仓库只需将 `local.New(...)` 替换为 `flexprice.New(...)`，调用 `bp.ResolvePlans(ctx)` 解析套餐 UUID，并设置 `REDIS_URL` 即可实现水平扩展。Flexprice 无后台容量同步 goroutine，配额直接从 Entitlement API 按需读取（5 分钟 TTL 缓存）。

## 认证机制

| 凭证 | 算法 | 有效期 | 存储 |
|---|---|---|---|
| Access token | HMAC HS256 JWT | 7 天 | 响应体，客户端内存 |
| Refresh token | 32 字节随机，SHA-256 哈希 | 90 天，使用后轮换 | DB `refresh_tokens` |
| OTP（邮箱验证/密码重置） | 6 位随机数字，SHA-256 哈希 | 10 分钟，5 次失败后失效 | DB `users.verification_token` / `reset_otp_hash` |
| SSE token | UUID v4，明文 | 30 秒，单次消耗 | Cache（`tabslate:sse_token:<token>`） |
