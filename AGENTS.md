# AGENTS.md — agenthub（A2A 互连平台）

工程上下文入口，渐进式披露：先看概述/架构/目录，动手时再看规范/命令/注意。

## 概述

`agenthub` 是基于 **A2A（Agent2Agent）协议**的互连平台：把 **model（大脑）/ agent（数字员工）/
mcp（万能转接头）/ skill（岗位手册）/ cli（对讲机）** 五类异构资产统一注册、发现、路由与编排，
对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：**Go 1.27.3 + 标准库 `net/http`**，单二进制；持久层为 **PostgreSQL + GORM**
  （`gorm.io/gorm` + `gorm.io/driver/postgres`，与 `bankmind` 一致，是本仓唯一生产依赖）。
- 前端：Vue 3 + Vite（**bun 1.4.4+** 包管理/测试），十屏控制台（registry / registries / sources / trace /
  topology / **playground** / login / admin / audit / **docs**），**13 种语言**，**纯黑/白/灰单色**视觉。
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
| `computers.Store` | `store.NewComputersStore` | `computers`：agent 沙箱（browser/shell/screen），含状态与最近动作回显 |
| `schedules.Store` | `store.NewSchedulesStore` | `schedules`：目标/周期任务（cron_expr / enabled / next_run） |
| `knowledge.Store` | `store.NewKnowledgeStore` | `knowledge`：mock RAG 片段（agent_id / chunk_text / source_ref） |
| `router.Store` | `store.NewRouterStore` | `router_triggers` / `router_threads` / `router_subscriptions` / `router_tickets` / `router_assignments` / `router_decisions` / `router_policies` / `router_events`：触发流、订阅、工单与每次运行的事实，供断线 `since=<seq>` 回放 |

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
- `internal/model`：GORM 模型 —— `Resource` / `Task` / `TaskArtifact` / `TaskSpan` / `Computer` / `Schedule` / `Knowledge` /
  `TaskPending` / `TaskPush` / `PushRecord` / `Event` / `SchemaMigration`，以及 `JSONField`
  （`driver.Valuer` + `sql.Scanner`，列类型 `jsonb`，**不引入** `gorm.io/datatypes`）
- `internal/auth`：**认证内核**（零新依赖，全部标准库）。`password.go`（PBKDF2-HMAC-SHA256，
  `pbkdf2-sha256$iters$salt$key` 编码，盐与迭代参数一起入库便于日后升级）、`auth.go`
  （注册/登录/引导管理员/会话 cookie/`Principal`/吊销，端口 `UserStore`/`SessionStore`/`OAuthStore`）、
  `oauth.go`（Google/GitHub/通用 OIDC，手写授权码 + PKCE(S256) + state）
- `internal/authz`：**授权内核**。`Permission` 常量 + `Principal.Can`（owner 持有全部已知权限）+
  组/角色/权限解析（一次请求一次）+ `CanAccessResource` / `VisibleResourceIDs` + 端口 `AuthzStore`
- `internal/store`：`db.go`（`Open`/`Ping`/`Close`/`RedactDSN`，连接池 25/10/30min、`PrepareStmt`）、
  `migrate.go`（`go:embed` + `schema_migrations` 按序单事务执行，`pg_advisory_xact_lock`
  串行化并发迁移者）、`seed.go`（demo 五类资源）、
  `resource_store.go` / `task_store.go` / `event_store.go` / `push_store.go` /
  `auth_store.go` / `authz_store.go` / `backup_store.go` / `registry_store.go` /
  `computers_store.go` / `schedules_store.go` / `knowledge_store.go`
  （命名空间 + 审批 + 上游源 + agent 能力，十一个仓库实现）
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
- `internal/router`：**调度内核**（双层调度的 Router 层，端口在此定义、实现在 `store`，零新依赖）。
  `types.go`（`Trigger`/`Disposition`/`Thread`/`Subscription`/`Ticket`/`Assignment`/`Decision`/`Event`/`Stats`
  与哨兵错误）、`ports.go`（`Store`/`Assessor`/`Executor`/`Clock`）、`memory.go`（内存 `Store`，
  与 GORM 实现同语义，单测零外部依赖）、`engine.go`（**单写者**：去重 `Intake` + 订阅扇出 +
  `EvaluateDue` + `Bind`/`Unsubscribe`/`Handback`/`CloseTicket`/`Prune`，只写库并返回待执行意图）、
  `filter.go`（程序层：来源模式 `off|candidate|request_only` + 白名单/`@提及`/关键词/静默时段 + 自回显抑制）、
  `assess.go`（语义层：`RuleAssessor` 与 `LLMAssessor`，一拍批量一次调用 + 严格枚举解析 + 逐条回退规则）、
  `compose.go`（工单简报拼装：正文截断 + 最近 N 条 + 关联 ID）、`passes.go`（节拍单飞 + 请求合并 +
  panic 隔离 + 健康计数）、`executor.go`（`HubExecutor`：`hub.Send`/`hub.Resume` + 槽位信号量 +
  `input-required → waitsForPerson` + 退避重试）、`bus.go`（有界事件扇出 + `since` 回放）、
  `service.go`（对外门面 `Run`/`Stop`/`Stats`/`ListTriggers`/`ListTickets`/`Cancel`/`Handback`/
  `Unsubscribe`/`Kick`/`Policies`/`Inject`/`Subscribe`）
- `internal/sandbox`：**执行控制面**（大脑与双手解耦；端口在此定义、GORM 实现在 `store`，零新依赖）。
  `types.go`（`Layer`/`State`/`Descriptor`/`Profile`/`Spec`/`Handle`/`ExecRequest`/`ExecResult`/`Lease`/
  `ExecRecord`/`Node`/`Job`/`Stats`/`Event` + `Code*` 错误码）、`provider.go`（`Provider` 端口）、
  `registry.go`（类型目录：builtin/declarative/node 三源 + 30s 探测缓存单飞）、`presets.go`（进程沙盒与
  平台层声明式预设）、`declarative.go`（逐 argv 元素展开 + 占位符白名单 + 加载期校验，**绝不拼 shell**）、
  `policy.go`（`Defaults` 与资源策略合并出 `Profile` + `ValidateProfile`/`ArgsAllowed`）、
  `manager.go`（**单写者**：`Acquire` 键复用 + 单飞、`Exec`/`Attach`、`Renew`、`Sweep`、审计写入）、
  `sweeper.go`（节拍单飞 + `kick` 合并）、`bus.go`（事件扇出 + `since` 回放）、`service.go`（门面：
  `Run`/`Stop`/`Kick`/`Stats`/`Catalog`/`Execs`/`EventsSince`/节点与队列）、`memory.go`（内存双实现）、
  `mock.go`（离线确定性）、`docker.go`（docker/podman，含 `runtime` 收口 runsc/kata）、`gvisor.go`、
  `bwrap.go`、`remote.go`（节点协议：`JobQueue` 长轮询 + 认领/回传 + `RemoteProvider`）
- `internal/schedules`：`cron.go`（`NextRun`/`ValidateCron`/`Advance`，复用 `backup.ParseCron` 的五段解析器，
  保存即算出下次到期、触发后再算一次）
- `internal/handlers`：`handlers.go`（路由注册中心 + 静态回退 + `/healthz` 的 `database` 字段）、
  `principal.go`（`identify`/`requireAuth`/`requirePermission` + 可见性过滤 + 计数重算）、
  `a2a.go`、`api.go`（含 `/api/tasks/{id}/events?since=<seq>` 回放）、
  `api_auth.go`（注册/登录/登出/me/会话/OAuth 回调）、
  `api_admin.go`（users/groups/roles/permissions/grants 管理）、
  `api_backups.go`（检查点列表/打点/下载/删除/恢复 + 定时读写，全部 `backups:manage`）、
  `api_registry.go`（命名空间 CRUD + `/api/resources/{id}/status` 审批动作 + 待审批队列）、
  `api_sync.go`（上游源 CRUD + 手动同步，上游失败返回 502 并透传上游 code）
- `internal/handlers`（续，agent 能力端点 F10–F16）：`computers.go`（F11 agent 计算机：
  `GET/POST /api/computers`、`GET /api/computers/{id}`，mock 适配器，真实控制标 `TODO(real)`）、
  `schedules.go`（F13 目标调度：`GET/POST /api/schedules`、`GET/PUT/DELETE /api/schedules/{id}`、
  `POST /api/schedules/{id}/run` 立即触发一次；cron 保存即算 `next_run`，非法表达式返回
  `schedule_invalid`）、`knowledge.go`（F14 知识库：`GET /api/knowledge` mock RAG 检索 +
  技能蒸馏触发点记录，真实向量检索标 `TODO(real)`）、  `router.go`（`/api/router/*` 控制面：
  `stats`/`triggers`/`tickets`/`policies`/`passes`/`events` SSE，`routerReady` 在未注入
  `Deps.Router` 时统一回 503 `feature_unavailable`）、
  `connectors.go`（策略/动作目录 + **审批队列**：`GET /api/connectors/approvals` 与
  `POST /api/connectors/approvals/{id}/approve|reject`，队列从 `input-required` 任务派生，
  批准即 `hub.Resume` 真跑一次，拒绝即 `hub.Cancel`）、
  `files.go`（共享文件：`GET/POST /api/files`、`GET /api/files/{id}`、
  `GET /api/files/{id}/content`（`http.ServeContent`，支持 Range + `Content-Disposition`、
  `nosniff`）、`DELETE /api/files/{id}`；未配置后端时统一 503 `files_disabled`）
- `migrations/`：`001_resources.sql` … `004_push_records.sql`、`005_auth.sql`（users/sessions/
  oauth_accounts）、`006_rbac.sql`（groups/roles/permissions/各种关联表/resource_grants + 内置
  角色权限种子）、`007_backups.sql`（`backup_checkpoints` / `backup_schedules` + `backups:manage`
  权限与 admin 角色关联，全部 `IF NOT EXISTS` / `ON CONFLICT DO NOTHING`）、
  `008_registry_governance.sql`（`resources` 治理列 + `idx_resources_dedup` 唯一索引 +
  `registries` 表 + 三个新权限与 admin 关联）、  `009_registry_sync.sql`（`registry_sources`）、
  `017_computers.sql`（`computers`：agent 沙箱 browser/shell/screen）、
  `018_schedules.sql`（`schedules`：目标/周期任务）、
  `019_knowledge.sql`（`knowledge`：mock RAG 片段）、
  `020_router.sql`（`router_triggers` / `router_threads` / `router_subscriptions` / `router_tickets` /
  `router_assignments` / `router_decisions` / `router_policies` / `router_events`，含
  `(source, dedupe_key)` 与 `(disposition, next_due)` 部分索引），
  `029_channel_messages_owner.sql`（`channel_messages.owner_id` + 按父通道回填 + 索引 + RLS）、
  `030_threads_rls.sql`（`threads` 补 `ENABLE`/`FORCE` + 策略；`thread_messages.owner_id` +
  按父线程回填 + 索引 + 策略）、`031_shared_files.sql`（`shared_files` 元数据表 + owner 索引 + RLS），
  均 `IF NOT EXISTS`，经 `embed.go` 内嵌
- `README.md` / `README_cn.md` 的「AWS Agent Registry 特性对比」章：与 AWS Agent Registry 的
  逐项对比（含已有 / 本次补齐 / 明确不做三张清单与 recordType ↔ Kind 字段级映射）
- `tests/e2e_test.go`：httptest 真实 HTTP 端到端（内存模式）；`tests/integration_test.go` 与
  `tests/registry_governance_integration_test.go`：`DATABASE_URL` 门控集成
- `docker-compose.yml`：`postgres:16-alpine`（宿主 **5434**）+ `agenthub`（依赖 `service_healthy`）
  + `web`（`profiles: [dev]`）
- `web/`：Vue 3 控制台（`src/lib/copy/*-copy.js` ×13 语、`languages.js`、`use-copy.js`、`api.js`、
  `auth-store.js`（身份/权限/管理动作）、`errors.js`、`style.css` 单色 token；`views/` 十屏
  （registry / **registries** / **sources** / trace / topology / **playground** / **login** / **admin** /
  **audit** / **docs**，admin
  第四屏 backups 拆为 `views/admin/{BackupsPanel,BackupScheduleCard,BackupList,RestoreDrawer}.vue`；
  registry 抽屉的 per-kind 配置区块抽为 `views/components/ResourceKindForm.vue` 与
  `components/EnvEditor.vue`）；`tests/` vitest）
- `web/src/lib/docs-content.js` + `web/src/lib/copy/docs-copy.js` + `web/src/views/DocsView.vue`：
  **控制台自带文档屏**（侧边栏「管理」之后，无权限门控）。内容与文案分家——`docs-content.js` 是
  语言中立的机器数据（`/a2a` 七方法 + 别名、141 条 HTTP 路由、错误码按状态码分组、字段与 JSON 示例），
  原样渲染不翻译；`docs-copy.js` 只放散文，13 语齐备。二者的分界由 `tests/copy-fallback.spec.js`
  强制：该测试断言每个键在各语言下都**不等于英文**，所以路径/权限码/报文一旦进 copy 表就会失败。
  CRUD 行用「动词模板 + 名词」描述（`verbList` + `nounResources`），不为 60 行各写一套译文。
  HTTP 原因短语（`Bad Request` 等）同属协议数据，原样呈现。
- `docs/`：`docs.md` / `docs_cn.md` —— 控制台 `/docs` 屏的 markdown 副本，由
  `make docs-md`（`scripts/gen-docs-md.mjs`）从 `docs-content.js` + `docs-copy.js` 渲染，
  **不手工编辑**
- `scripts/`：`gen-docs-md.mjs`（文档 markdown 生成）、`compose-ports.sh`、`test-api.sh`
- `backend/static/`：Vue 构建产物（Go 进程托管，gitignore）

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
  幂等 ≠ 并发安全：`store.Migrate` 对建表与每个文件持 `pg_advisory_xact_lock` 并在锁内复查
  `schema_migrations`（两个副本、或 `go test` 的两个包同时迁移会撞系统目录 23505），别把这段去掉。
- **按 id 取行必须带 owner**：`schedules` / `channels` / `computers` 的端口签名是
  `Get(ctx, id, ownerID)` / `Update(ctx, x, ownerID)` / `Delete(ctx, id, ownerID)`，仓储统一走
  `store.ownedWhere`（`WHERE id = ? AND owner_id = ?`）。`ownerID == ""` 只匹配无主行，
  **永远不是「任意 owner」**；越权一律 404，不泄露存在性。唯一的无归属读是显式命名的
  `channels.GetUnscoped`（webhook 验签前要读行），调用方必须自带 `identity.System()`。
  新增任何「按 id 服务某个请求」的读，照抄这条，别再回到只认 id。
- **RLS 是第二道闸（15 张表）**：`026` 覆盖任务管道 + `knowledge` + `audit_log`，
  `028` 覆盖 `computers`/`schedules` 这类 owned 面，`029` 覆盖 `channel_messages`，
  `030` 覆盖 `threads` 与 `thread_messages`，`031` 覆盖 `shared_files`，
  `028` 补 `schedules` / `channels` / `computers`，全部 `ENABLE` + `FORCE`，谓词统一是
  `agenthub_row_visible(owner_id)`（`app.system` / `app.bypass` / 精确非空 owner）。
  两条配套要求：后台节拍（cron 扫描、sweeper、同步）必须 `identity.With(ctx, {System:true})`，
  否则 `FORCE` 下读到 0 行、表现为「定时任务再也不触发」；公开回调只有 webhook 一处例外，
  走 `GetUnscoped` + 系统身份。`tests/rls_test.go` 的表清单与策略名断言同步在 11 张表上。
- **文档与路由双向守恒**：新增/删除 `mux.HandleFunc` 必须同步 `web/src/lib/docs-content.js`
  （再 `bun scripts/gen-docs-md.mjs` 重生成 `docs/docs.md`）。`tests/docs_endpoints_test.go`
  对 `handlers.go` 与 `docs.md` 做双向差集：漏项与幽灵条目都会让 `go test` 变红。
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
- **破坏性操作需显式确认**：恢复（restore）是 agenthub 唯一会摧毁数据的操作——接口要求
  `{"confirm":"<checkpoint id>"}` 二次确认，执行前自动打回滚点、静默等待在途任务、失败时保持原状；
  真实恢复的集成用例与 `scripts/test-api.sh` 段落都以 `AGENTHUB_TEST_BACKUP_RESTORE=true` 门控。
- **新错误码必须登记**：`internal/httputil` 的 `Code*` 常量 + `WriteErr` 状态码映射，
  否则一律退化为 500；前端同步登记 `errors.js` 的 `CODE_KEYS` 与 `errors-copy.js` 的 13 语。
- **自助注册默认只读**：自助注册成功的账号由 handler 调 `Authz.Store().SetUserRoles` 写入
  `authz.RoleViewer`（`role-viewer`），因此任何能打开登录页的人注册到的账号只读、可解释。
  两条入口都算：`POST /api/auth/register`（密码注册）与 OAuth **首次**登录（
  `CompleteOAuth` 返回 `OAuthLogin.Created` 为真时才授予，回访登录绝不重新授予，否则管理员
  无法降权）。首个账号是 owner（隐式全权）不再叠加 viewer 行，管理员用 `POST /api/users`
  建号则完全按请求里的 `roles` 落库（未给就是无角色）。授予发生在建号之后，失败只
  `slog.Error` 不返回错误——账号已存在且邮箱已占用，让请求失败只会把用户卡死，管理员在控制台
  补一次成员关系即可。角色 id 走 `authz.Role*` 常量，不复制迁移里的字符串。
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

## 常用命令

```bash
make up                # docker compose up -d，完成后打印监听端口与用途
make down              # docker compose down（数据在 agenthub-pgdata 卷）
make ports             # 重打印运行栈的端口表（宿主端口 + 用途 + 健康态）
make db-up             # 只起 postgres（宿主 5434）+ 等待 ready
make db-migrate        # 只建 schema（跑 internal/store，其 DB 用例在 Open 时就 Migrate）
make db-down           # docker compose down
make db-psql           # psql 进 compose 数据库
make run              # db-up + go run（默认 :3500）
make build            # web-build + go build → bin/agenthub-server
make version          # 打印本次构建将写入的 version（git describe）
make docker-build     # docker build --build-arg VERSION=$(make version)
make compose-build    # 同一镜像改走 compose 构建（含 git 打标转发）
make test             # db-migrate + go test -race ./...（含DB 门控用例）
make test-integration # db-migrate + 只跑 Integration 用例
make test-cover       # db-migrate + 逐包覆盖率；连接数据库时才会计入 store 与备份引擎（DB 门控用例）
make vet / make fmt
make web-install / make web-dev / make web-build / make web-test
./scripts/test-api.sh        # 对运行中实例的完整 HTTP 接口自测（PASS/FAIL 逐条，失败非 0 退出）
                             # 备份段落：AGENTHUB_BACKUP_DIR 未配置则 SKIP；
                             # 真实恢复需 AGENTHUB_TEST_BACKUP_RESTORE=true（否则会清空会话）
```

## 发布（镜像）

- 发布 Release 触发 `.github/workflows/release.yml`：单一 job `build-and-publish` 构建并推送容器镜像。
- 镜像：构建并推送 **`ariacompute/agenthub-agenthub`**
  到 DockerHub，tag 取 Release tag（`ariacompute/agenthub-agenthub:<tag>`）并额外打 `:latest`。
- tag 同时作为 `VERSION` build-arg、`7 位 commit` 作为 `COMMIT`、`DISTANCE=0`、`DIRTY=false`
  （CI 一定是干净 checkout），走的是 Dockerfile 已有的四个 `ARG`，与本地 `make docker-build` /
  `make compose-build` 完全一致；这样**镜像 tag === `/healthz` 的 `version@commit`**，可回溯。
- **不设兜底**：Release 无 tag、`./Dockerfile` 缺失都以 `::error::` 退出——宁可发布失败，也不推一个
  自报 `dev` 哨兵、说不清来源的 `:latest`。
- 必填 secrets：`DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`；`:latest` **只由 CI 推**，禁止本地手工推。
- 平台仅 `linux/amd64`（与 docker 仓 release.yml 一致）；如需多架构再引 `setup-qemu` + `setup-buildx`。

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

## 执行沙盒（硬约定）

- **默认 `docker`，失败即拒绝**：`AGENTHUB_SANDBOX_PROVIDER` 默认 `docker`；探测不到守护进程就是
  `sandbox_unsupported`（目录端点与 `/healthz` 写原因），**禁止**回落到宿主进程执行。离线走显式
  `AGENTHUB_SANDBOX_PROVIDER=mock`，`off` 表示全部拒绝。
- **新增后端只动三处**：实现 `sandbox.Provider` 并 `Register`、或写一条 `AGENTHUB_SANDBOX_PROVIDERS`
  声明式模板、或登记执行节点。`manager`/`hub`/`adapter`/`handlers` **不得**出现 provider 名字的
  `switch` 分支。
- **声明式模板是安全边界**：argv 逐元素展开，**绝不拼 shell、绝不经 `/bin/sh`**；占位符只能在白名单内；
  未知占位符/空 argv/相对路径二进制在启动期拒绝。模板只来自运营者配置，永不来自请求。
- **命令来源**：CLI 命令必须等于资源登记命令，参数按 `args_policy`（`none|prefix|passthrough`）校验；
  **禁止**再从提示词推导命令（首词回退已删除），提示词最多作 stdin。
- **参数在漏斗处再校验一次**：`Manager.Exec` 对 `req.Args` 逐个调 `sandbox.Argument(profile, arg)`，
  失败即 `sandbox_policy_denied`。CLI 连接器造租约前已问过 `ArgsAllowed`，但 `Exec` 是所有调用方
  （含 `POST /api/sandboxes/{id}/exec`）的唯一入口——「命令在白名单里、参数不在」正是绕过方式。
- **后端如实声明能力**：`local` 的 `Describe().Network` 只有 `platform`（裸子进程没有自己的网络
  命名空间，走宿主的路由与 DNS）；`docker` 声明 `none|bridge|platform`，`createArgs` 与之逐项对齐
  （`platform` 传**守护进程默认网络**而不是字面 `--network bridge`）；`bwrap` **不声明** `bridge`
  ——它的 `--share-net` 就是宿主命名空间，所以只报 `none|platform`；`remote`、编译进来的 HTTP 垫片
  与未声明网络的自定义模板一律只报 **`delegated`**：本机不施加任何网络约束，profile 随作业传给
  真正执行的那一层。`AGENTHUB_SANDBOX_EGRESS_ALLOWLIST` 目前**无人执行**，启动时非空即 `slog.Warn`。
- **能力声明会被准入消费（fail-closed）**：`Manager.Acquire` 解析出 provider 后调 `Describe()`，
  若 `NormalizeNetwork(profile.Network)` 不在 `desc.Network` 内即返回 `sandbox_capability_unavailable`，
  **不再静默降级**成宿主网络。`Describe().EnvKeys` 承载「需要哪些环境变量」（如远程节点的 base URL），
  与 `Requires`（探针要找的二进制）分开，`SourcePlatform` 标明编译进来的 HTTP 垫片不是声明式模板。
- **租约与任务解耦**：任务取消/失败/重启不销毁租约；租约丢失不使任务失败（`Manager.Exec` 标 failed，
  下次 `Acquire` 重建）。作用域解析 `metadata.ticket` → `metadata.contextId` → `default`。
- **owner 必须贯穿**：经 router 发起的执行走 `Hub.SendAs(ticket.ActorID)`，否则租约落进「匿名不复用」。
- **上限交叉校验**：`config.Validate()` 要求 `SANDBOX_MAX_TOTAL ≥ ROUTER_MAX_TICKETS`；取不到租约返回
  **可重试**的 `sandbox_limit_reached`（与 `sandbox_policy_denied` 区分：前者会重试，后者不会）。
- **审计只存摘要**：`sandbox_execs` 存命令摘要、退出码、耗时与 stdout/stderr 的 sha256，**不得**落全量输出。
- **两种不可用语义**：未接线 → `503 feature_unavailable`；已接线但后端跑不了 → `sandbox_unsupported`。
  控制台据此显示「沙盒已关闭」而不是空数据。
- **新增 `/api/sandbox*` 路由**须同时登记 `handlers/listen.go` 的 `Listeners()` 与 `listen_test.go`
  的 `probes`；迁移新增走 `022_*` 及以后，并同步 `store_test.go` 的 `want`。
- **控制台试跑绕开 router**（`POST /api/sandboxes/{id}/exec`，审计标 `dry_run`）：手工探测不得凭空
  开 ticket、污染统计或触发人工审批。

## 共享文件（`internal/files`）

- **默认关闭**：`AGENTHUB_FILES_STORE=off|local|s3`，未配置即 off，所有 `/api/files*` 回
  `503 files_disabled`（与 `AGENTHUB_BACKUP_DIR` 同一惯例：空默认即功能开关）。
- **两个后端、一个端口**：`files.Store`（元数据，GORM 实现在 `internal/store/files_store.go`）与
  `files.Backend`（字节：`local.go` 落磁盘、`s3.go` 走 SigV4）分开；`Service` 负责大小上限
  （`AGENTHUB_FILES_MAX_BYTES`）、sha256、MIME 嗅探与后端选择。
- **本地后端**：临时文件写完校验字节数后**原子 rename**，目录按 sha256 前两位分片；文件 0600；
  key 只接受 `<2 位>/<64 位十六进制>`，其余（含 `..`）一律 `file_store_error`。
- **S3 后端**：标准库手写 SigV4（**不新增依赖**），单次 `PUT` 带 `x-amz-content-sha256`，
  回避分片签名与 chunked 上传；`path-style` 必选（RustFS/MinIO）；endpoint/bucket/region/
  `PATH_STYLE` 来自配置，**密钥只从环境变量名读**（`ACCESS_KEY_ENV`/`SECRET_KEY_ENV`，
  与 `model_openai.go` 的 `api_key_env` 同口径）。日志只记 backend / 对象键前缀 / 字节数，
  **绝不**打印 `Authorization`、`X-Amz-Signature`、密钥或带 userinfo 的 URL。
- **下载是流**：`GET /api/files/{id}/content` 用 `http.ServeContent`（Range + `Content-Disposition` +
  `X-Content-Type-Options: nosniff`），不整块进内存；越权与不存在都回 `404 file_not_found`。
- **归属与隔离**：`shared_files.owner_id` + `ENABLE`/`FORCE` + `shared_files_owner_access`；
  仓储用 `scoped(ctx)` 发布身份，列表按 owner 精确匹配（空 owner 不等于「任意 owner」）。

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
  （`invalid_*`/`duplicate` → 400，`*_not_found` → 404，`not_cancelable`/`not_resumable` → 400；
  agent 能力码（`writeDomainErr` 映射）：`computer_not_found`/`schedule_not_found` → 404、`skill_distill_disabled` → 403、
  `schedule_invalid` → 400、`feature_unavailable` → 503（computers/schedules/knowledge 三端点 store 未接入时的统一哨兵，
  取代原 `computer_disabled`/`knowledge_unavailable` 的分别命名），新增领域错误需同步该映射，否则会退化为 500。
  - 调度器码（`router.go:writeRouterErr`）：`router_ticket_not_found`/`router_trigger_not_found`/
    `router_thread_not_found` → 404、`router_ticket_closed`/`router_handback_exhausted`/
    `router_resource_unset` → 409、`router_policy_invalid` → 400；`Deps.Router == nil` 时全部走
    503 `feature_unavailable`。新增码须同时登记 `httputil` 常量、`writeRouterErr` 与前端
    `CODE_KEYS` + 13 语 `copy/errors-copy.js`。
- 每次启动都会尝试写入 5 个 `demo-*` 种子资源（每类一个），`ON CONFLICT (id) DO NOTHING` 保证幂等；
  用户覆盖过的同名条目**不会被种子改写**，保证 README 冒烟可复现。
- 前端未执行 `make web-build` 时 `/` 返回 404（机器可读 `not_found`），不是服务故障。
- 不实现（仍属后续项）：多语言 SDK、LLM-as-judge 评分（属 `memo` / `bankmind` / `playground` 或后续项）。
  RAG 与浏览器自动化（Computer Use）已以**确定性 mock 适配器**形式落地（`/api/knowledge`、`/api/computers`），
  真实向量检索与真实计算机控制统一标 `TODO(real)`，不在首版。

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
- 桌面壳（agentbuddy，独立仓库）前缀白名单已含 `/api/workspace` 与 `/api/threads`，新增服务端端点
  无需改桌面端代码。
- 协作通道与语音（agenthub 增补，agentbuddy 工作台消费）：
  - `GET/POST /api/channels`、`GET/PUT/DELETE /api/channels/{id}`、`GET /api/channels/{id}/deliveries`、
    `POST /api/channels/{id}/webhook`（公开、签名校验）、`POST /api/channels/{id}/deliver`（出站，SSRF 安全投递到 `config.target_url`）。
  - `GET /api/connectors`、`PUT /api/connectors/{id}/policy`、`GET/POST /api/connectors/{id}/actions`：
    对 `kind=mcp` 资源暴露外部调用，`ask_first` 策略经 `Hub.ParkConnector` 挂起 `input-required` 任务等待人工批准。
  - `POST /api/threads/{id}/audio`：服务端转写（STT mock）后作为 `origin=voice` 消息委派进线程，并把 agent 回复合成 TTS 音频作为 artifact；受 `AGENTHUB_SPEECH_MODE` / `AGENTHUB_AUDIO_MAX_BYTES` 控制。
  - `Persona`：`Entry.Persona`（`AvatarURL`/`Bio`/`Capabilities`）经 `normalizePersona` 归一化，落 `resources.persona` JSONB；本轮进一步扩展为角色画像（`role`/`instruction`/`mbti`/`avatar`，F10），仍由 `POST/PUT /api/resources` 写入。
  - **Agent 能力端点（F10–F16）**：`GET/POST /api/computers`、`GET /api/computers/{id}`（F11 计算机，mock 适配器）、`GET/POST /api/schedules`、`PUT/DELETE /api/schedules/{id}`、`POST /api/schedules/{id}/run`（F13 目标调度，**cron 已真实推进**：保存即算 `next_run`，到期自动触发并回写 `last_run_at`/`last_status`/`next_run`）、`GET /api/knowledge`（F14 知识库 mock RAG）。深层能力中「真实计算机控制 / 向量检索」仍标 `TODO(real)`；调度不再 mock。
  - **调度器控制面（Router 层）**：`GET /api/router/stats`、`GET/POST /api/router/triggers`、`GET /api/router/triggers/{id}`、`GET /api/router/tickets`、`GET /api/router/tickets/{id}`、`POST /api/router/tickets/{id}/cancel`、`POST /api/router/tickets/{id}/handback`、`DELETE /api/router/threads/{id}/subscription`、`GET /api/router/policies`、`PUT /api/router/policies/{source}`、`POST /api/router/passes`、`GET /api/router/events`（SSE，`?since=<seq>&subject=` 先回放再实时）；全部只用 `tasks:read` / `tasks:write`，无新权限码。
  - **入口改造（唯一两处）**：`handlePostThreadMessage` 与 `delegateThread` 在 `Deps.Router != nil` 时改走 `Intake`，否则保留原有直发 `Hub.Send`——因此关闭调度器时既有行为与测试零变化。
  - 上述端点均在桌面壳（agentbuddy）前缀白名单（`/api`）覆盖之内，新增服务端端点无需改桌面端代码。
