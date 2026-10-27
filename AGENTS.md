# AGENTS.md — agenthub（A2A 互连平台）

工程上下文入口，渐进式披露：先看概述/架构/目录，动手时再看规范/命令/注意。

## 概述

`agenthub` 是基于 **A2A（Agent2Agent）协议**的互连平台：把 **model（大脑）/ agent（数字员工）/
mcp（万能转接头）/ skill（岗位手册）/ cli（对讲机）** 五类异构资产统一注册、发现、路由与编排，
对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：**Go 1.27 + 标准库 `net/http`**，单二进制；持久层为 **PostgreSQL + GORM**
  （`gorm.io/gorm` + `gorm.io/driver/postgres`，与 `bankmind` 一致，是本仓唯一生产依赖）。
- 前端：Vue 3 + Vite（**bun** 包管理/测试），八屏控制台（registry / registries / sources / trace /
  topology / login / admin / **docs**），**13 种语言**，**纯黑/白/灰单色**视觉。
- 桌面端：`desktop/`，**Rust + Tauri v2** 外壳（Windows / macOS arm64 / Linux）。**不含后端、
  不含数据库**，用环回反向代理把同一份控制台对着用户指定的服务端打开；外壳自有文案同样 13 语。
  外壳只认**前缀**（`/api`、`/a2a`、`/healthz`、`/.well-known`），其余路径一律 SPA 回退到
  `index.html`，因此控制台新增屏幕与控制面新增端点**不需要改桌面端代码**。
- 首版五类适配器**全部为确定性 mock**：无网络、无子进程、离线可跑通；真实对接以 `TODO(real)` 标注边界。
- 设计参照 `mastra-ai/mastra` 的**协议与交互形态**（MCP Registry 条目模型、workflow 三原语、
  HITL→`input-required`、Studio 试跑、observability 分层），但**不引入其任何代码或 npm 依赖**。

## 架构（五层单向依赖 + 持久层）

```
a2a（协议类型/编解码，零依赖）
  → authz（权限解析 + 资源级授权）         ┐
    → auth（密码、会话、OAuth、Principal） │ 端口由本包定义，实现在 store
      → registry（五类 + custom 条目 + 治理：命名空间/审批/同步 + export） │ 依赖倒置：端口接口由消费方定义
        → adapter（Connector 工厂 + 五类实现）
          → hub（任务状态机 + 事件 broker + span + flow 编排）
            → handlers（/.well-known、/a2a、/api/*、/healthz、静态托管）
persistence ← internal/store（GORM 实现）
  ├─ migrations/*.sql（go:embed，幂等）+ schema_migrations 版本表
  └─ resources / registries / registry_sources / tasks / task_artifacts / task_spans /
     task_pending / task_push / events / push_records / users / sessions / oauth_accounts /
     groups / roles / permissions / grants
```

一次请求：身份解析（会话 cookie → `Principal`）→ 权限校验（`/api/*`）→ 方法规范化（v0.1 别名）→
资源可见性过滤 → 资源选择 → 适配执行 → 状态机推进 → artifact 累积 → 事件扇出（SSE +
**异步落 `events`**）→ pushNotification 回调（**落 `push_records`**）。

生命周期：`submitted → working → input-required → completed / failed / canceled`。
`input-required` 可**无限期挂起**，并凭 `taskId` 通过 `tasks/resume` 续接恢复（suspend/resume 语义）。

## 持久化分层

| 端口接口（由消费方包定义） | GORM 实现 | 落库内容 |
| --- | --- | --- |
| `registry.ResourceStore` | `store.NewResourceStore` | `resources`：五类 + custom 条目全字段（含 `namespace`/`status`/`display_name`/`source_id`/`kind_config`）+ jsonb 列 + 时间戳 |
| `registry.NamespaceStore` | `store.NewNamespaceStore` | `registries`：命名空间（含 `auto_approve` 审批策略） |
| `registry.SourceStore` | `store.NewSourceStore` | `registry_sources`：上游注册中心 + `last_sync_at/status/count/error` |
| `hub.TaskRepository` | `store.NewTaskRepository` | `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` |
| `hub.EventRepository` | `store.NewEventRepository` | `events(seq, task_id, kind, payload)`，供 `since=<seq>` 断线重连回放 |
| `hub.PushRepository` | `store.NewPushRepository` | `push_records`：mock notifier 捕获的每次回调（url/state/config 快照） |
| `auth.UserStore` / `SessionStore` / `OAuthStore` | `store.NewUserStore` / `NewSessionStore` / `NewOAuthStore` | `users` / `sessions` / `oauth_accounts` |
| `authz.AuthzStore` | `store.NewAuthzStore` | `groups` / `roles` / `permissions` / `user_groups` / `user_roles` / `group_roles` / `role_permissions` / `resource_grants` |

- **依赖倒置**：端口接口定义在 `registry` / `hub`，实现在 `store`，因此 `hub`、`registry` **不 import `store`**。
- **两套行为**：不注入 GORM 仓库时 `registry.Registry` / `hub.TaskStore` / `hub.Broker` /
  `hub.MockNotifier` 保持**原有内存语义**（公开签名零变更），单测因此**零外部依赖**；
  注入后同一批公开方法改走 PostgreSQL。
- **启动顺序**：`Open`（空 DSN 返回 `ErrNoDatabase`）→ `Migrate`（按文件名 + `schema_migrations`）→
  `Seed`（`ON CONFLICT (id) DO NOTHING`）→ hub → handlers，退出时 `Hub.Close()` 后 `store.Close(db)`。

## 目录

- `cmd/agenthub-server`：唯一入口（dotenv → config → `store.Open` → `Migrate` → `Seed` →
  registry/hub → handlers → `http.Server` + 信号优雅退出 → `Hub.Close()` → `store.Close`）
- `internal/config`：环境变量与默认值 + `Validate()`（`DATABASE_URL` 必填）；
  `dotenv.go` 向上最多 6 层找 `.env`，只补未设置的键
- `internal/httputil`：`WriteJSON/WriteError/WriteErr/Decode`（机器可读 code）+ SSE（`Stream/WriteSSE/Comment/KeepAlive`）
- `internal/a2a`：**冻结契约**。`types.go`（Task/Message/Part/Artifact/AgentCard）、
  `jsonrpc.go`（2.0 信封 + 错误码）、`methods.go`（方法名与 v0.1 别名映射）
- `internal/model`：GORM 模型 —— `Resource` / `Task` / `TaskArtifact` / `TaskSpan` /
  `TaskPending` / `TaskPush` / `PushRecord` / `Event` / `SchemaMigration`，以及 `JSONField`
  （`driver.Valuer` + `sql.Scanner`，列类型 `jsonb`，**不引入** `gorm.io/datatypes`）
- `internal/auth`：**认证内核**（零新依赖，全部标准库）。`password.go`（PBKDF2-HMAC-SHA256，
  `pbkdf2-sha256$iters$salt$key` 编码，盐与迭代参数一起入库便于日后升级）、`auth.go`
  （注册/登录/引导管理员/会话 cookie/`Principal`/吊销，端口 `UserStore`/`SessionStore`/`OAuthStore`）、
  `oauth.go`（Google/GitHub/通用 OIDC，手写授权码 + PKCE(S256) + state）
- `internal/authz`：**授权内核**。`Permission` 常量 + `Principal.Can`（owner 持有全部已知权限）+
  组/角色/权限解析（一次请求一次）+ `CanAccessResource` / `VisibleResourceIDs` + 端口 `AuthzStore`
- `internal/store`：`db.go`（`Open`/`Ping`/`Close`/`RedactDSN`，连接池 25/10/30min、`PrepareStmt`）、
  `migrate.go`（`go:embed` + `schema_migrations` 按序单事务执行）、`seed.go`（demo 五类资源）、
  `resource_store.go` / `task_store.go` / `event_store.go` / `push_store.go` /
  `auth_store.go` / `authz_store.go` / `backup_store.go` / `registry_store.go`
  （命名空间 + 审批 + 上游源，八个仓库实现）
- `internal/registry`：`registry.go`（`Kind`/`Health`/`Transport`/`Entry` + CRUD + 发现 +
  `ResourceStore` 端口 + 委托）、`export.go`（聚合 Agent Card + MCP server 描述，**只导出
  approved**）、`kinds.go`（五类 + `custom` 的 per-kind 配置：`ModelConfig`/`AgentConfig`/
  `MCPConfig`/`SkillConfig`/`CLIConfig`/`CustomConfig`）、`validate.go`（`ValidateKind` 按 Kind
  分发校验）、`lifecycle.go`（`Status` 枚举 + `CanTransition` 审批状态机 + `Discoverable` 消费方
  可见性唯一出口）、`namespace.go`（命名空间 + `(namespace, name, version)` 去重启用键）、
  `source.go`（`RegistrySource` + `Client`/`SourceStore` 端口）、`sync.go`（同步编排：单飞 + 拉取 +
  解析 + 去重 upsert + 状态回写）、`sync_fixture.go`（离线确定性实现）、`sync_http.go`（真实
  `net/http` 客户端，限长 + 超时 + 鉴权头，`TODO(real)` 标注协议适配点）
- `internal/backup`：**数据库检查点内核**（端口在此定义，GORM 实现在 `internal/store`）。
  `engine.go`（`Engine` 接口 + `Manifest`/`Checkpoint` + `SelectEngine` 自动选择）、
  `pgdump.go`（`pg_dump -Fc` / `pg_restore --clean --if-exists --single-transaction`）、
  `copy.go`（纯 Go `COPY` 导出写回 + 表发现 + 拓扑排序 + 序列修正）、
  `archive.go`（tar.gz 打包解包 + manifest + sha256）、
  `service.go`（单飞打点/恢复编排/保留清理/回滚点）、`schedule.go`（三种模式 + 手写 5 段 cron）、
  `store.go`（`BackupStore`/`Quiesce`/`Reloader` 端口）
- `internal/adapter`：`connector.go`（`Connector` 接口 + `Options` + `For` 工厂）、
  `mock.go`（确定性基元）、`model.go`（provider 注册表 + `ModelRouter` 回退链 + 按
  `Entry.KindConfig.ModelConfig` 装配）、`model_openai.go`（OpenAI 兼容 provider，chat completions，
  `api_key_env` 只取环境变量名，失败回退 mock，`TODO(real)` 标注 streaming 边界）、
  `agent.go`、`mcp.go`、`skill.go`、`cli.go`
- `internal/hub`：`store.go`（`TaskRepository`/`EventRepository`/`PushRepository` 端口 + `Persistence`）、
  `task.go`（TaskStore + 合法迁移校验 + 挂起状态 + 条件更新委托）、`trace.go`（span 时间线）、
  `broker.go`（事件扇出 + 有界队列异步落库）、`flow.go`（`then/branch/parallel`）、
  `orchestrator.go`（send/stream/get/cancel/resume/push + MockNotifier 落库 + Prune ticker）、
  `quiesce.go`（`Drain`/`Allow`/`ActiveTaskCount` 闸门 + `ResetEventSeq`，恢复期间拒新任务并等待在途任务）
- `internal/handlers`：`handlers.go`（路由注册中心 + 静态回退 + `/healthz` 的 `database` 字段）、
  `principal.go`（`identify`/`requireAuth`/`requirePermission` + 可见性过滤 + 计数重算）、
  `a2a.go`、`api.go`（含 `/api/tasks/{id}/events?since=<seq>` 回放）、
  `api_auth.go`（注册/登录/登出/me/会话/OAuth 回调）、
  `api_admin.go`（users/groups/roles/permissions/grants 管理）、
  `api_backups.go`（检查点列表/打点/下载/删除/恢复 + 定时读写，全部 `backups:manage`）、
  `api_registry.go`（命名空间 CRUD + `/api/resources/{id}/status` 审批动作 + 待审批队列）、
  `api_sync.go`（上游源 CRUD + 手动同步，上游失败返回 502 并透传上游 code）
- `migrations/`：`001_resources.sql` … `004_push_records.sql`、`005_auth.sql`（users/sessions/
  oauth_accounts）、`006_rbac.sql`（groups/roles/permissions/各种关联表/resource_grants + 内置
  角色权限种子）、`007_backups.sql`（`backup_checkpoints` / `backup_schedules` + `backups:manage`
  权限与 admin 角色关联，全部 `IF NOT EXISTS` / `ON CONFLICT DO NOTHING`）、
  `008_registry_governance.sql`（`resources` 治理列 + `idx_resources_dedup` 唯一索引 +
  `registries` 表 + 三个新权限与 admin 关联）、`009_registry_sync.sql`（`registry_sources`），
  经 `embed.go` 内嵌
- `README.md` / `README_cn.md` 的「AWS Agent Registry 特性对比」章：与 AWS Agent Registry 的
  逐项对比（含已有 / 本次补齐 / 明确不做三张清单与 recordType ↔ Kind 字段级映射）
- `tests/e2e_test.go`：httptest 真实 HTTP 端到端（内存模式）；`tests/integration_test.go` 与
  `tests/registry_governance_integration_test.go`：`DATABASE_URL` 门控集成
- `docker-compose.yml`：`postgres:16-alpine`（宿主 **5434**）+ `agenthub`（依赖 `service_healthy`）
  + `web`（`profiles: [dev]`）
- `web/`：Vue 3 控制台（`src/lib/copy/*-copy.js` ×13 语、`languages.js`、`use-copy.js`、`api.js`、
  `auth-store.js`（身份/权限/管理动作）、`errors.js`、`style.css` 单色 token；`views/` 八屏
  （registry / **registries** / **sources** / trace / topology / **login** / **admin** / **docs**，admin
  第四屏 backups 拆为 `views/admin/{BackupsPanel,BackupScheduleCard,BackupList,RestoreDrawer}.vue`；
  registry 抽屉的 per-kind 配置区块抽为 `views/components/ResourceKindForm.vue` 与
  `components/EnvEditor.vue`）；`tests/` vitest）
- `web/src/lib/docs-content.js` + `web/src/lib/copy/docs-copy.js` + `web/src/views/DocsView.vue`：
  **控制台自带文档屏**（侧边栏「管理」之后，无权限门控）。内容与文案分家——`docs-content.js` 是
  语言中立的机器数据（`/a2a` 七方法 + 别名、66 条 HTTP 路由、错误码按状态码分组、字段与 JSON 示例），
  原样渲染不翻译；`docs-copy.js` 只放散文，13 语齐备。二者的分界由 `tests/copy-fallback.spec.js`
  强制：该测试断言每个键在各语言下都**不等于英文**，所以路径/权限码/报文一旦进 copy 表就会失败。
  CRUD 行用「动词模板 + 名词」描述（`verbList` + `nounResources`），不为 60 行各写一套译文。
  HTTP 原因短语（`Bad Request` 等）同属协议数据，原样呈现。
- `docs/`：`docs.md` / `docs_cn.md` —— 控制台 `/docs` 屏的 markdown 副本，由
  `make docs-md`（`scripts/gen-docs-md.mjs`）从 `docs-content.js` + `docs-copy.js` 渲染，
  **不手工编辑**
- `scripts/`：`gen-docs-md.mjs`（文档 markdown 生成）、`compose-ports.sh`、`test-api.sh`
- `backend/static/`：Vue 构建产物（Go 进程托管，gitignore）
- `desktop/`：**桌面外壳（Tauri v2，Rust，独立 crate，不在任何 workspace 内）**。
  `src-tauri/src/proxy/`（`mod.rs` 环回监听与路由分发、`forward.rs` reqwest 流式转发 +
  cookie jar + `Set-Cookie`/`Location` 改写、`static_files.rs` rust-embed 控制台与 SPA 回退、
  `shell.rs` `/__shell/*` 自有端点）、`config.rs`（连接配置读写与 URL 校验）、
  `tray.rs`（托盘菜单）、`i18n/`（13 语文案 + 系统语言检测）、`assets/shell.html`（连接页/关于页，
  文案由 Rust 注入）、`scripts/stamp.mjs`（发布时写版本号）。
  **Logo 单一来源**：界面徽标与 `BrandMark.vue` 同一 hub 图形（30px 徽章 + 18px 字形，sheen/描边
  跟随明暗主题），应用图标由 `web/public/favicon.svg` 的同一几何栅格化为 `assets/app-icon.png`
  （`bun run icons` 重新派生 `src-tauri/icons/`）——两处都不允许再画第二份 mark。

## 开发规范

- **A2A 契约冻结**：帧形状只在 `internal/a2a` 定义，任何层不得改写；SSE **没有 `[DONE]` 哨兵**，
  终止由 `completed/failed/canceled/input-required` 状态事件表达。
- **错误只返回机器可读 code**：HTTP `{"code":"..."}`；JSON-RPC `error.message` 即 code 字符串。
  服务端自然语言绝不上屏，前端 `web/src/lib/errors.js` + `copy/errors-copy.js` 负责本地化。
- **后端保持语言无关**：HTTP 与 JSON-RPC 响应只含字段、枚举与 code；多语言 100% 由前端 copy 层承担。
- **传输无关**：MCP 的 `transport`（stdio / streamable-http / sse-legacy）是**记录字段**，
  适配器不得按它分支。
- **CLI 适配器安全底线**：白名单前置校验 → 单次超时（默认 15s）→ 输出截断 → 结构化 JSON 回执，
  四步缺一不可；真实实现沿用同一顺序。
- **日志与密钥**：用 `slog`（dev 为 debug、其余 info），禁止 `fmt.Println`；不打正文、token 与向量；
  DSN 一律经 `store.RedactDSN` 脱敏后再记录；禁止吞错与静默 no-op。
- **数据库必填**：`DATABASE_URL` 缺失即 `config.Validate` 拒绝启动，不存在「无库降级」路径；
  迁移 SQL 必须幂等（`CREATE TABLE/INDEX IF NOT EXISTS`），新增表或列走新文件、**不改已执行文件**。
- **持久化不得阻塞运行**：状态/artifact/span 同步写，`events` 与 `push_records` **尽力而为**
  （有界队列 + 异步 writer，队列满或失败只 `slog.Warn`，绝不回压任务执行）；
  所有持久层调用统一走 `context.Background()`（客户端断连不得丢弃任务状态）。
- **写路径一律条件更新**：任务状态迁移必须 `UPDATE ... WHERE id=? AND state=?` 并校验 `RowsAffected`，
  0 行再区分「任务不存在」与「并发冲突」。
- **并发**：`registry`/`TaskStore` 用 `sync.RWMutex`；`Hub` 不可变（runner 按请求创建）；
  broker 对慢消费者**丢弃最旧帧**而非阻塞运行；`parallel` 分支数受 `AGENTHUB_MAX_PARALLEL` 约束。
- **不做评分**：eval 仅留钩子，不接 LLM judge、不产出任何分数（对齐仓内「禁止伪造分数」约定）。
- **色彩禁令**：UI 不得出现任何有彩色相，一律落在 `#000 ↔ #fff` 明度轴；
  `web/tests/style.spec.js` 自动校验（hex 三通道相等 + 色相词黑名单）。
- **i18n 单向来源**：`languages.js`（13 语，与 ariacompute.cn 一致）+ 按语言分块的 copy 表，
  浅合并英文兜底；组件内禁止硬编码自然语言。
- **桌面端四条硬约束**：
  1. **控制台与 Go 服务端零改动**：外壳只靠环回代理工作，不得为桌面端在 `web/` 或
     `internal/handlers` 加专用分支或路由。
  2. **转发前缀不得漏**：`/api`、`/a2a`、`/healthz`、`/.well-known` 四类前缀全部转发给服务端，
     其余走 SPA 回退（与 Go 的 `staticOrIndex` 同语义）。漏一个前缀的表现为「桌面端某屏空白」。
  3. **SSE 必须真流式**：转发一律走 `reqwest` 字节流 → axum `Body::from_stream`，禁止缓冲，
     禁止设置整体超时（任务事件流可挂数分钟）；`accept-encoding: identity` 防止被压缩。
  4. **外壳文案也 13 语**：`desktop/src-tauri/src/i18n/copy.rs` 与控制台 copy 表受同一道约束
     （13 语齐备、每语无空串、非英语不得与英文原文相同），由 `cargo test` 守护；
     语言代码与 `web/src/lib/languages.js` 必须一致。
- **桌面端安全**：连接配置只存外壳自有 `config.json`（仅当前用户可读），URL 必须校验
  `http`/`https` scheme；「忽略自签证书」默认关闭；日志不得打印完整 URL（可能含凭据）与 Cookie 头。
- **破坏性操作需显式确认**：恢复（restore）是 agenthub 唯一会摧毁数据的操作——接口要求
  `{"confirm":"<checkpoint id>"}` 二次确认，执行前自动打回滚点、静默等待在途任务、失败时保持原状；
  真实恢复的集成用例与 `scripts/test-api.sh` 段落都以 `AGENTHUB_TEST_BACKUP_RESTORE=true` 门控。
- **新错误码必须登记**：`internal/httputil` 的 `Code*` 常量 + `WriteErr` 状态码映射，
  否则一律退化为 500；前端同步登记 `errors.js` 的 `CODE_KEYS` 与 `errors-copy.js` 的 13 语。
- **新权限需同步四处**：`authz.Perm*` 常量 → `AllPermissions` → 迁移里的权限种子与 admin 角色关联 →
  前端 `auth-store.js` 的 `PERMISSIONS` 与 `enums-copy.js` 的 `permission` 分组（13 语）。
- **API 密钥**：`api_keys` 只存 `sha256(token)`（`token_hash` 唯一索引）与展示 `prefix`，**绝不存明文**；
  软吊销（`revoked_at`）、可选 `expires_at`、`last_used_at` 由 `ByTokenHash` 命中后 `Touch` 写入。密钥
  解析收敛在 `auth.Service.Principal` 一处（先于会话 cookie 尝试 `Authorization: Bearer` / `X-AgentHub-Key`），
  构造 `IsOwner=false` 且 `permissions = scopes ∩ 所属用户权限` 的 Principal，一处改动即覆盖 `/api/*`
  与 `/a2a`（含 SSE stream）。错误码 `api_key_*` 已在 `httputil` 与 `errors-copy.js` 13 语登记；
  新增表走 `010_api_keys.sql`（`//go:embed *.sql` 自动收录，SQL 幂等）。
- **目录治理四条**：
  1. **消费方只见 approved**：`registry.Discoverable` 是唯一过滤出口，列表 / MCP 导出 / Agent Card /
     A2A 寻址全部走它；空 `status` 按 `approved` 处理，保证升级后既有资源不消失。
  2. **去重启用键是 `(namespace, name, version)`**：同名跨版本是不同记录；由 DB 唯一索引（内存模式
     用同语义 map）保证，冲突映射为 `resource_already_exists` → 400。
  3. **同步导入与手工注册同路**：上游记录先解析为 `Entry` 再走同一条 `Upsert`，共享同一套校验与审批；
     同步单飞，`last_sync_*` 只记状态/条数/错误码，不记响应体与凭据；全局开关
     `AGENTHUB_REGISTRY_SYNC_FIXTURE`（默认 **开**）把**所有**源钉在离线客户端上，
     要真正出网必须显式关掉它。
  4. **密钥只存名字**：`api_key_env` / `auth_token_env` 仅保存环境变量名，本体不入库、不落日志。
- **bool 字段不要写 `default` 到 GORM tag**：带 `default` 的零值字段会被 GORM 从 INSERT 中省略，
  DB 默认值生效，于是显式 `false`（如 `auto_approve=false`、`enabled=false`）被静默写成 `true`。
  列上的 SQL `DEFAULT` 保留给手写 SQL，模型 tag 只写 `not null`。
- **测试**：`go test -race ./...`（纯逻辑单测零外部依赖 —— memory 回退保证无 DB 也全绿）；
  集成用例以 `DATABASE_URL` 门控（**未设置则 `t.Skip`，绝不伪造结果**）；前端 `bunx vitest run`。
  新增功能须补**正常 + 异常**双路径用例。
- **覆盖率门在看板之外，但看板不能空**：新代码至少覆盖到「正常路径 + 每个错误码一条」，
  并按这三条惯例落位：
  1. store 端口（内存 / PostgreSQL 双模）用**包内假 store** 驱动，不需要 DB 就能跑到
     `store != nil` 的全部分支（`internal/registry/store_mode_test.go` 是模板）；
  2. 真正需要 PostgreSQL 的断言写在**被测包内**并以 `DATABASE_URL` 门控，这样覆盖率算在
     `internal/store` 而不是都归到 `tests/`（后者只有 0 语句，覆盖率不体现）；
  3. 离线优先是硬约束：任何会触网的用例必须走 `httptest` 或 fixture；同步类用例一律
     `Fixture: true`，否则一次 DNS 超时就是 5 秒的测试。
- **桌面端测试**：`cd desktop/src-tauri && cargo test`（配置校验、i18n 完整性、代理转发清单、
  `Set-Cookie` 改写，以及**起真实环回代理 + 假服务端**的端到端用例：转发、cookie、SSE 不缓冲、
  SPA 回退、连接保存；控制台未构建时静态用例自动 skip，不伪造结果）。

## 常用命令

```bash
make up                # docker compose up -d，完成后打印监听端口与用途
make down              # docker compose down（数据在 agenthub-pgdata 卷）
make ports             # 重打印运行栈的端口表（宿主端口 + 用途 + 健康态）
make db-up             # 只起 postgres（宿主 5434）+ 等待 ready
make db-down           # docker compose down
make db-psql           # psql 进 compose 数据库
make run              # db-up + go run（默认 :3500）
make build            # web-build + go build → bin/agenthub-server
make version          # 打印本次构建将写入的 version（git describe）
make docker-build     # docker build --build-arg VERSION=$(make version)
make compose-build    # 同一镜像改走 compose 构建（含 git 打标转发）
make test             # go test -race ./...（含DB 门控用例）
make test-integration # 只跑 Integration 用例
go test ./... -cover  # 逐包覆盖率；连接数据库时才会计入 store 与备份引擎（DB 门控用例）
make vet / make fmt
make web-install / make web-dev / make web-build / make web-test
make desktop-install / make desktop-dev / make desktop-build / make desktop-test
                             # 桌面外壳（Tauri v2）：dev/build 都会先构建控制台
                             # （rust-embed 在编译期嵌入 backend/static），test = cargo test
./scripts/test-api.sh        # 对运行中实例的完整 HTTP 接口自测（PASS/FAIL 逐条，失败非 0 退出）
                             # 备份段落：AGENTHUB_BACKUP_DIR 未配置则 SKIP；
                             # 真实恢复需 AGENTHUB_TEST_BACKUP_RESTORE=true（否则会清空会话）
```

## 发布（镜像 + 桌面端）

- 发布 Release 触发 `.github/workflows/release.yml`：**两个并列且互不依赖的 job** ——
  `build-and-publish` 推镜像，`desktop` 出三平台桌面产物；concurrency 组名 `release` 同时守两者。
- 改这个 workflow 的三条纪律：`build-and-publish` 的 steps / secrets / build-args / outputs
  **逐字不动**；`desktop` 自带 stamp 步骤（无 tag 直接 `::error::` 退出），不跨 job 共享 outputs；
  `desktop` 单独声明 `permissions: contents: write`（上传 Release 资产），镜像 job 仍是 `read`。
- 镜像：构建并推送 **`ariacompute/agenthub-agenthub`**
  到 DockerHub，tag 取 Release tag（`ariacompute/agenthub-agenthub:<tag>`）并额外打 `:latest`。
- tag 同时作为 `VERSION` build-arg、`7 位 commit` 作为 `COMMIT`、`DISTANCE=0`、`DIRTY=false`
  （CI 一定是干净 checkout），走的是 Dockerfile 已有的四个 `ARG`，与本地 `make docker-build` /
  `make compose-build` 完全一致；这样**镜像 tag === `/healthz` 的 `version@commit`**，可回溯。
- **不设兜底**：Release 无 tag、`./Dockerfile` 缺失都以 `::error::` 退出——宁可发布失败，也不推一个
  自报 `dev` 哨兵、说不清来源的 `:latest`。
- 必填 secrets：`DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`；`:latest` **只由 CI 推**，禁止本地手工推。
- 平台仅 `linux/amd64`（与 docker 仓 release.yml 一致）；如需多架构再引 `setup-qemu` + `setup-buildx`。
- **桌面端**：三格矩阵（linux-x86_64 / macos-aarch64 / windows-x86_64），每格先
  `bun run build` 出控制台 → `tauri-action`（`releaseId` 指向触发本次运行的 Release，
  `uploadUpdaterJson: false`）出原生安装包（MSI / DMG / deb）并自行附着；随后脚本把
  应用目录打成**便携包**（zip / tar.gz），为安装包与便携包生成 `SHA256SUMS.txt`，最后由
  `softprops/action-gh-release` 附着便携包与校验和 —— **不新建 Release、不改 body**。
- **每个平台每种形态只留一个产物**：Linux `--bundles deb`（不出 AppImage，便携包已覆盖「解开即跑」）；
  macOS 的 `bundle/macos/<name>.app.tar.gz` 被 tauri-action **硬编码**附着（其入参只能改名、不能排除，
  `uploadUpdaterJson: false` 也拦不住），故上传后由脚本按 asset id 删除，只删 `.app.tar.gz` 与其 `.sig`，
  保留 `.dmg`。
- **macOS 只出 arm64**：原 Intel 一格跑在 `macos-13` 标签上，该 runner 已被 GitHub 退役——作业
  受理后永远等不到 runner，而 `fail-fast: false` 让整个 Release 一直 in_progress（其余格子产物已上传），
  既不成功也不失败。不要恢复该标签；要恢复 Intel 产物只能在 `macos-latest` 上交叉编译
  `x86_64-apple-darwin`。Intel Mac 由 `make desktop-build` 从源码构建。
- **版本号 === Release tag**：`desktop/scripts/stamp.mjs` 在编译前把 tag（去掉 `v`）写进
  `Cargo.toml` 与 `tauri.conf.json`；`AGENTHUB_COMMIT` 环境变量进入 `option_env!`，供「关于」显示。
- **产物未签名**（未配置证书 secrets）：文件名与发布说明必须明示，README 给出 macOS Gatekeeper
  与 Windows SmartScreen 的绕行步骤。

## 演示数据（demo resources）

- 唯一的 mock data 就是 `store.Seed()` 写入的五条 `demo-*` 资源，**默认不再灌**：新实例以空 registry
  启动，只有显式 `AGENTHUB_SEED_DEMO=true`（见 `internal/config`）才幂等写入
  （`ON CONFLICT DO NOTHING`），用于本地走查与 `scripts/test-api.sh`。
- `cmd/agenthub-server` 在未开启时打一条 `store: demo resources not seeded` 日志（带 hint），
  绝不静默。
- `config.Validate` 拒绝 `AGENTHUB_ENV=prod` + `AGENTHUB_SEED_DEMO=true`：生产实例里 demo 资源与真实
  资源无法区分。`AGENTHUB_SEED_DEMO` 取值解析走 `envBool`，不可解析回落默认 false。
- 因此 README 与 `scripts/test-api.sh` 都必须以 `AGENTHUB_SEED_DEMO=true` 启动依赖方能跑；
  脚本在 `/api/resources` 里找不到 `demo-mcp` / `demo-agent` 时直接 exit 2 并给出同一条命令。
- 关闭开关**不会**清理已存在的行（`store.Seed` 只做 insert，不做 purge）；要回到干净状态手动
  `DELETE FROM resources WHERE id LIKE 'demo-%'`（compose 环境用 `make db-psql`）。

## 端口清单

- 容器内**只有一个监听端口**（恒为 `3500`），宿主侧映射由 compose 决定；因此「哪个端口在应答、
  答的是什么」必须落在 compose 层，而不是二进制里。
- `scripts/compose-ports.sh` 是唯一出口：`make up`（`docker compose up -d` 之后立即打印）与
  `make ports`（对运行中的栈重打）都调它。宿主端口优先读 `docker compose port <svc> <port>`
  的真实映射，取不到才回落 `AGENTHUB_HTTP_PORT` / `POSTGRES_PORT` 默认值。
- 端点用途的单一事实来源是 `handlers.Listeners()`（`internal/handlers/listen.go`）：
  启动时由 `cmd/agenthub-server` 打成一条 `agenthub endpoints` 日志（前台 `docker compose up`
  与 `docker compose logs -f agenthub` 可见），脚本里的同名列表须与之同步，
  `TestListenersCoverRoutes` 负责兜住的漂移。

## 版本打标

- `config.Version` / `config.Commit` / `config.Distance` / `config.Dirty` 必须是
  **`var` 而不是 `const`**：`go build -ldflags "-X agenthub/internal/config.Version=…"`
  只能覆盖包级 string 变量，改成 const 会让构建静默丢标、所有二进制都上报哨兵值。
- 打标来源（`make build` / `make run` 经 `LDFLAGS` 注入）：
  `Version` = `git describe --tags --abbrev=0 --always`（**tag 原样保留 `v` 前缀**，
  未达 tag 时回落短 hash，兜底 `dev`）；`Commit` = `git rev-parse HEAD | cut -c1-7`
  （兜底 `unknown`）；`Distance` = `git rev-list --count <tag>..HEAD`（`0` 表示构建
  恰好落在 tag 上）；`Dirty` = `git status --porcelain` 非空则为 `true`。
  `ProtocolVersion`（A2A 协议版本）跟构建无关，**保持 const、不打标**。
- 控制台展示：侧边栏左下角渲染 **`version@commit`**（如 `v0.2.0@1a2b3c4`，commit 截
  7 位），数据来自 `/healthz` 的 `version` + `commit` 字段（`config.Build()` 负责拼串，
  commit 缺席时退化为仅 version）；`buildNote`（`config.BuildNote()`，形如 `+3 dirty`）
  是对 tag 的偏离说明，**另起一行**渲染（内联追加会被省略号裁掉），恰在干净 tag 上时为
  空且不渲染；顶栏 chip 只显示 version。这些全是数据字段，**不进 copy 层、不翻译**。
- 容器镜像：`.dockerignore` 排除 `.git`，故由 `Dockerfile` 的 `ARG VERSION=dev` /
  `ARG COMMIT=unknown` / `ARG DISTANCE=0` / `ARG DIRTY=false` 承接，
  `make docker-build` 或 compose 的 `VERSION` / `COMMIT` / `DISTANCE` / `DIRTY` 传入
  （CI 从干净 checkout 构建时后两者用默认值）。
- 镜像构建联网：`bun install` 与 `go mod download` 需要出网，代理走 `BUILD_PROXY`
  （`make docker-build` 从 `$HTTP_PROXY` 取值并加 `--network=host`，容器内 `127.0.0.1:<port>`
  才指向宿主机代理）。compose 构建跑在 bridge 网络上，该地址不存在，故 `Dockerfile`
  对这两步各做一次「清掉代理变量」的重试并回落默认 `GOPROXY` 镜像
  （`https://goproxy.cn,direct`，可用 `--build-arg GOPROXY=…` 覆盖）；两次都失败才报错退出，
  绝不静默装作成功。
- **运行时阶段的 `apk add` 是唯一没有镜像回落的一步**：bun 有 registry、go 有 `GOPROXY`，
  apk 只有 Alpine CDN。`dl-cdn.alpinelinux.org` 不可达时该步会一直挂着（表现为构建"卡死"），
  因此新增 `ALPINE_MIRROR`（默认 `https://dl-cdn.alpinelinux.org`，可换
  `https://mirrors.aliyun.com` / `https://mirrors.tuna.tsinghua.edu.cn`，镜像根后面会接 `/alpine/v3.21/...`），
  并用 `timeout` 给每一步限时：宁可带提示失败，不要静默干等。代理以命令行前缀传入而非 `ENV`，
  否则会残留到运行中的容器、代理服务端自身的出网请求。
- 兜底哨兵 `"dev"` / `"unknown"`：未打标的构建在 `/healthz` 上报 `dev`，一眼可辨，
  绝不冒充某个发布号。

冒烟：`curl localhost:3500/healthz`；`curl localhost:3500/.well-known/agent.json`；
`POST /a2a` 的 `message/send` 与 `message/stream`（详见 `README_cn.md`）。

## 注意

- 端口：服务 **3500**，compose 数据库宿主 **5434**；避开 bankmind(8080)、cockpit store(3400)、
  宿主 postgres(5432)、bankmind 数据库(5433)。
- **身份与权限（重要）**：
  - 控制面 `/api/*` 走**会话**（HttpOnly cookie），由 `requirePermission(<perm>)` 逐路由声明；
    共享管理员 token（`AGENTHUB_ADMIN_TOKEN` / `AGENTHUB_AUTH_OPEN_MODE`）**已移除**。
  - 路由只声明**权限点**（`authz.Perm*`），永不判断角色或组：新角色不必改 handler。
  - **可见性只在 `principal.go` 过滤一次**：列表/详情/导出/Agent Card 必须都走
    `visibleEntries`，写路径必须再单独 `canAccess(..., write=true)`，否则列表过滤挡不住直接 PUT。
  - **没有授权的资源 = 公开**：`resource_grants` 为空表示不限制，第一条授权才使其受限
    （这是老实例升级后行为不变的保证）。
  - 删除用户是**软删除**（`disabled_at`），任务与审计链必须存活；owner 不可停用。
  - 未注入 `Auth`/`Authz`（单测、内存模式）时每个调用者都是 `authz.DevPrincipal()`，
    即身份体系出现前的开放行为，`/healthz` 如实上报 `auth: disabled`。
  - 凭据与口令只以派生值落库（`password_hash`、`session_token_hash`），日志只记 user id 与结果。
- 控制面错误码到状态码的映射集中在 `internal/handlers/api.go:writeDomainErr`
  （`invalid_*`/`duplicate` → 400，`*_not_found` → 404，`not_cancelable`/`not_resumable` → 400），
  新增领域错误需同步该映射，否则会退化为 500。
- 每次启动都会尝试写入 5 个 `demo-*` 种子资源（每类一个），`ON CONFLICT (id) DO NOTHING` 保证幂等；
  用户覆盖过的同名条目**不会被种子改写**，保证 README 冒烟可复现。
- 前端未执行 `make web-build` 时 `/` 返回 404（机器可读 `not_found`），不是服务故障。
- 不实现：RAG、浏览器自动化、多语言 SDK、LLM-as-judge 评分（属 `memo` /
  `bankmind` / `playground` 或后续项）。

## agentbuddy 接入（桌面客户端）

`agentbuddy` 是一个独立仓库的 **Tauri v2 桌面壳 + Vue 3 工作台前端**，以本 agenthub 作为服务端：
它把 `http://127.0.0.1:<随机端口>` 上的相对请求（`/api`、`/a2a`、`/healthz`、`.well-known` 及下方
两个端点）环回转发到用户配置的 agenthub 服务端，从而复用本仓的认证、A2A 与 hub 编排能力。

- `GET /api/workspace`：聚合视图。先 `visibleEntries(r, Reg.List())` 取可见条目，按
  `registry.ParseKind("agent")` 过滤出 agent，并附 `Hub.Store().List()` 的最近 8 条任务活动。
  只读聚合，不引入新查询面。
- `GET/POST /api/threads`、`GET /api/threads/{id}`、`POST /api/threads/{id}/messages`、
  `GET /api/threads/{id}/events`：多 agent 协作会话。
  - 线程归属 `owner_id`（取自 `authz.PrincipalFrom(r).UserID`），`threads_store` 在 `internal/store`
    实现，`threads.Store` 端口定义在 `internal/threads`。
  - `messages` 中带 `mentions`（agent 资源 id）时，逐一对每个目标调用 `Hub.Send` 并写入
    `Message.Metadata["resourceId"]` 进行 A2A 委派；结果以 `events` SSE 透传回前端
    （实现参考 `handlers/api.go` 的 `streamTaskEvents`）。
  - 迁移见 `migrations/012_threads.sql`（幂等：`CREATE TABLE IF NOT EXISTS` + `ON DELETE CASCADE`）。
- 权限点复用既有 `PermResourcesRead` / `PermTasksRead` / `PermTasksWrite`，不新增角色或组。
- 桌面壳前缀白名单（`internal/proxy/mod.rs` 的 `is_remote_path`）已含 `/api/workspace` 与
  `/api/threads`，新增服务端端点无需改桌面端代码。
- 协作通道与语音（agenthub 增补，agentbuddy 工作台消费）：
  - `GET/POST /api/channels`、`GET/PUT/DELETE /api/channels/{id}`、`GET /api/channels/{id}/deliveries`、
    `POST /api/channels/{id}/webhook`（公开、签名校验）、`POST /api/channels/{id}/deliver`（出站，SSRF 安全投递到 `config.target_url`）。
  - `GET /api/connectors`、`PUT /api/connectors/{id}/policy`、`GET/POST /api/connectors/{id}/actions`：
    对 `kind=mcp` 资源暴露外部调用，`ask_first` 策略经 `Hub.ParkConnector` 挂起 `input-required` 任务等待人工批准。
  - `POST /api/threads/{id}/audio`：服务端转写（STT mock）后作为 `origin=voice` 消息委派进线程，并把 agent 回复合成 TTS 音频作为 artifact；受 `AGENTHUB_SPEECH_MODE` / `AGENTHUB_AUDIO_MAX_BYTES` 控制。
  - `Persona`：`Entry.Persona`（`AvatarURL`/`Bio`/`Capabilities`）经 `normalizePersona` 归一化，落 `resources.persona` JSONB。
  - 上述端点均在桌面壳前缀白名单（`/api`）覆盖之内，新增服务端端点无需改桌面端代码。
