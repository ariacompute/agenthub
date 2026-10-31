# AgentHub 发布说明

## 一、平台总览

`agenthub` 把 model（大脑）/ agent（数字员工）/ mcp（万能转接头）/ skill（岗位手册）/
cli（对讲机）五类异构资产统一注册、发现、路由与编排，对外以标准 A2A 端点被任何遵循协议的
Agent 调用。

### 五层单向依赖

- **分层依赖**：`a2a`（协议）→ `registry`（资源）→ `adapter`（适配）→ `hub`（编排）→
  `handlers`（接入），依赖方向严格单向。
- **持久层依赖倒置**：`ResourceStore` / `TaskRepository` / `EventRepository` /
  `PushRepository` 四个端口由消费方包定义，实现落在 `internal/store`，
  因此 `hub`、`registry` 不 import `store`。
- **单二进制**：Go 1.27 + 标准库 `net/http`，无框架；GORM 是唯一生产依赖。
- **默认端口 3500**，compose 数据库宿主端口 **5434**（避开 5432 / 5433）。
- **受治理的目录**：条目归属一个命名空间、带审批状态、按
  `(namespace, name, version)` 去重；消费方只见已批准的条目。

### 资产概念地图

- **model**：provider 注册表 + 回退链，输出 completion；可通过自身 router 配置指向真实的
  OpenAI 兼容网关。
- **agent**：目标驱动、可挂起待输入，步骤规划产物 `agent.plan`。
- **mcp**：`tools/list` + `tools/call` 协议转换；`transport` 仅为描述字段。
- **skill**：触发 → 手册 → 步骤 → 报告。
- **cli**：白名单 → 超时 → 截断 → 结构化 JSON 回执。
- **custom**：任意合法 JSON schema，用于五类之外的记录。

---

## 二、A2A 协议层

对外只暴露一套标准 A2A 端点：任何遵循协议的 Agent 都能发现、调用并接管任务编排。

### 端点与方法

- **服务发现**：`GET /.well-known/agent.json`（兼容 `GET /a2a/agent-card`），
  一资源渲染为一个 skill。
- **单入口调用**：`POST /a2a`，JSON-RPC 2.0 信封，`Content-Type: application/json`。
- **七种方法**：`message/send`、`message/stream`、`tasks/get`、`tasks/cancel`、
  `tasks/resume`、`tasks/pushNotification/set`、`tasks/pushNotification/get`。
- **v0.1 别名**：`tasks/send` / `tasks/sendSubscribe`，由 `AGENTHUB_A2A_LEGACY_METHODS`
  控制开关。
- **资源选择**：`metadata.resourceId|resource_id|resource`（精确 id → 名称 → kind）→
  `metadata.kind` 兜底 → 无选择器时优先 agent，否则按 id 字典序首个。候选集是
  `registry.Discoverable`，未获批准的记录选不中、也调不动。

### 任务状态机

- **生命周期**：`submitted → working → input-required → completed / failed / canceled`，
  终态不可再迁出，`unknown` 不可作为起点或终点。
- **挂起恢复**：`input-required` 可**无限期挂起**，凭 `taskId` 通过 `tasks/resume` 续接恢复；
  未挂起任务恢复返回 `task_not_resumable`（-32008）。

### SSE 流式契约

- **帧名**：`task_status_update` / `task_artifact_update` / `message`，错误帧名 `error`。
- **无 `[DONE]` 哨兵**：终止由 `completed` / `failed` / `canceled` / `input-required`
  状态事件表达，客户端不得依赖任何哨兵帧。
- **控制台旁路**：`GET /a2a/stream?task=<id>` 提供无 JSON-RPC 封装的 SSE。

### 统一错误模型

- **机器可读 code**：`bad_request`(400) / `unauthorized`(401) / `not_found`(404) /
  `method_not_allowed`(405) / `payload_too_large`(413) / `internal_error`(500)。
- **JSON-RPC 码**：标准 `-32700` / `-32600` / `-32601` / `-32602` / `-32603`；
  自定义 `-32001 task_not_found` … `-32008 task_not_resumable`。
- **error.message 即 code 字符串**：不含空格与自然语言，多语言 100% 由前端 copy 层承担。
- **适配器码**：`unknown_kind` / `cli_command_not_allowed` / `adapter_timeout` /
  `resource_unreachable`。

---

## 三、资源注册层

`registry` 以 MCP Registry 风格的条目模型登记五类资产与 `custom` 记录，并提供发现、导出与聚合。

### 条目模型（五类 + custom）

- **Registry 级元数据**：`version` / `repository` / `tags` / `env` / `auth_schemes`，
  以及 `endpoint` / `transport`（仅描述，适配器不得按它分支）。
- **能力与健康**：`capabilities`（MCP 为工具名、skill 为步骤工具）；
  `health` 取 `healthy` / `degraded` / `unhealthy` / `unknown`。
- **id 规则**：形如 `<kind>-<hex>`，缺省自动生成；重复 id 注册返回 400。
- **去重启用键**：`(namespace, name, version)`，由唯一索引保证——同名换版本是
  新记录，同名单同版本再注册返回 400。

### 目录治理

目录不再是平铺清单，而是一个受治理的 registry。驱动本次工作的 AWS Agent Registry 逐项对比
见 `README_cn.md` 的「AWS Agent Registry 特性对比」一章，其中包含 agenthub **明确不做**的项
（语义 hybrid search、CloudTrail 等价审计、跨账号 RAM 共享、VPC PrivateLink）。

- **per-kind 配置**：六类在注册抽屉各有专属配置区块，后端按同一结构校验
  （`ValidateKind`，`invalid_kind_config`）——model 声明 provider 模式、agent 填 Agent Card
  与能力、MCP 按 transport 决定填命令还是 URL、skill 填步骤与手册、CLI 填命令白名单、
  custom 填任意 JSON schema。
- **审批生命周期**：`draft → pending → approved / rejected`，另有 `deprecated`，经
  `POST /api/resources/{id}/status` 推进（`registry:curate`）。`registry.Discoverable`
  是唯一过滤出口：列表、MCP 导出、Agent Card 与 A2A 寻址共用它，未批准的记录既找不到也
  调不动。空 `status` 按 `approved` 处理，保证升级后既有目录不会集体消失。
- **命名空间**：`registries` 表存命名空间，各自带审批策略（`auto_approve`）；`default`
  由迁移种子创建且不可删除。
- **上游注册中心同步**：源（MCP / Skill）按手动或定时把外部记录拉入本地目录，按
  `(name, version)` 合并，并把结果（`last_sync_at` / `status` / `count` / `error`）写回源。
  导入的记录与手工注册走同一条 `Upsert`，因此上游目录无法绕过校验与审批。真实客户端是
  `net/http`（超时 + 限长 + 按源鉴权），fixture 客户端离线确定性；`AGENTHUB_REGISTRY_SYNC_FIXTURE`
  （默认开）把所有源钉在离线客户端上——开启同步绝不等于静默开启出网。
- **新增错误码**：`invalid_kind_config`、`invalid_status_transition`、`invalid_namespace`、
  `invalid_source` → 400；`sync_in_flight` → 409；上游不可达 → 502 并透传上游 code。

### 控制面与导出

- **CRUD 与发现**：`GET/POST /api/resources`、`GET/PUT/DELETE /api/resources/{id}`、
  `POST /api/resources/{id}/health`，支持 `?kind=`、`?q=`、`?namespace=` 与 `?status=`
  过滤和计数。
- **实测健康探针**：`POST /api/resources/{id}/probe` 让资源自身的 connector 去测它到底好不好——agent 去拉它的 Agent Card、MCP 去 ping 它的 `initialize`——并把结论存回；探针若测不到，则记为 `unhealthy` 而非保留旧值。它与 `…/health` 不同：后者记录的是运维人员的判断。
- **身份与会话**：`GET /api/auth/config` 返回已启用的 OAuth 提供方；`GET /api/auth/sessions` 与
  `DELETE /api/auth/sessions/{id}` 供用户查看并吊销自己的会话，而 `DELETE /api/users/{id}/sessions`
  （`sessions:manage`）可吊销其他用户的全部会话。会话是入库的随机令牌（无 JWT/JTI），吊销即时生效、不可重放。
- **命名空间与审批**：`GET/POST /api/registries`、`PUT/DELETE /api/registries/{id}`、
  `GET /api/registries/approvals`（待审批队列）、`POST /api/resources/{id}/status`。
- **上游源**：`GET/POST /api/registry-sources`、`PUT/DELETE /api/registry-sources/{id}`、
  `POST /api/registry-sources/{id}/sync`。
- **MCP 导出**：`GET /api/export/mcp` 输出 MCP server 描述——仅 approved。
- **聚合 Agent Card**：`GET /api/export/agent-card`，每资源渲染为一个 skill——仅 approved。
- **协作（agentbuddy）**：`GET /api/workspace` 聚合可见 agent + 近期任务活动；`GET/POST /api/threads`、
  `GET /api/threads/{id}`、`POST /api/threads/{id}/messages`（每个 `@mention` 经 `Hub.Send` 走 A2A
  委派，以任务 id 关联）、`GET /api/threads/{id}/events`（SSE 回访 + 被委派任务实时回复）。复用
  `resources:read` / `tasks:read` / `tasks:write`，不新增角色或用户组。

---

## 四、适配层

`adapter` 以同一个 `Connector` 接口收敛五类资产的调用面。其中四类**仍为确定性 mock**：
无网络、无子进程、离线可跑通，真实对接以 `TODO(real)` 标注边界；model 连接器在条目声明时
可以跑**真实的 OpenAI 兼容 provider**。

### 五类连接器

- **model**：provider 注册表 + 回退链，产物 `model.completion`——现在带上命中的 provider 名。
- **agent**：步骤规划；prompt 含 `approve` / `confirm` / `approval`（**词级匹配**）→
  挂起并置 `RequiresInput`。
- **mcp**：`tools/list` + `tools/call` 协议转换，产物 `mcp.tools` / `mcp.call`。
- **skill**：触发 → 手册 → 步骤 → 报告，产物 `skill.manual`。
- **cli**：白名单 → 超时 → 截断 → JSON 回执，产物 `cli.receipt`（含 `truncated`）。
- **speech**：第六个连接器，语音端点（`POST /api/threads/{id}/audio`）的离线确定性 STT/TTS，
  与另五类一样是 mock，标 `TODO(real)`。

### 安全与可靠性

- **确定性约束**：同输入同输出（按输入哈希派生）、不触网、不起进程，离线产物一律标注 `provider: mock`。
- **健康状态改为实测**：不再由 mock 声明（`mockHealth` 已删除）。每类 connector 去测自己真正要调的东西——
  model 的 `/models`（无法定论时退到 1 token 的 completion）、agent 的 Agent Card、mcp 的 `initialize`、
  skill/custom 的 endpoint、cli 的 `PATH` 查找；一次运行则记录这次调用证实的结论。确实没有可访问目标时
  回答 `unknown`，而不是填一个没人测过的值。
- **CLI 四步底线**：白名单前置校验 → 单次超时（默认 15s）→ 输出截断 → 结构化 JSON 回执，
  四步缺一不可。
- **artifact 累积**：支持 `append` / `lastChunk`，任务结束时按累积列表返回。

### Model Router

条目声明 `provider_mode=openai_compatible` 时，`endpoint` 可指向任意 OpenAI 兼容网关
（LiteLLM / OpenRouter / 自建路由），由真实 chat completions provider 承接。

- **配置**：`model_id`、`api_key_env`、有序回退链 + 权重、超时、可选 mock 兜底。router
  按条目的 `KindConfig` 装配，因此两个 model 资源可以指向两个网关。
- **凭据**：`api_key_env` **只存环境变量名**；密钥本体不入库、不落日志。
- **绝不伪造**：全部 provider 失败时连接器返回错误。只有显式开启 `fallback_mock` 才由
  确定性 mock 应答，且产物标注 `provider: mock`，不会被误认作真实 completion。
- **边界**：streaming 与真实 agent / mcp / skill / cli 适配器仍是 `TODO(real)`，这四类
  保持离线。

---

## 五、Hub 编排层

`hub` 把一次入站消息编排成可追踪、可挂起、可恢复的任务。

### 状态机与挂起恢复

- **合法迁移校验**：`CanTransition` 显式校验，非法迁移返回
  `invalid_state_transition`（-32005），绝不静默降级。
- **挂起态持久化**：`input-required` 的提问与提问时间随任务落 `task_pending`，恢复后清空。

### flow 三原语

- **`then`**：顺序执行一个资源，输出接下一步。
- **`branch`**：按 `when` 选**首个匹配臂**，无匹配则整臂跳过（span 记 `skipped`）。
- **`parallel`**：并发执行成员，成员只读各自上下文副本；分支数受
  `AGENTHUB_MAX_PARALLEL`（默认 8）截断；任一成员失败则整个 `parallel` 失败。
- **`when` 语法**：`""` / `always` / `true`（默认臂）、`contains:<text>`、`kind:<kind>`、
  `has:<key>`；任一节点要求输入 → 流程**提前停止**并挂起。

### span 时间线与事件扇出

- **span 时间线**：每步一个 span，含耗时、状态（`ok`/`error`/`input-required`/`skipped`）
  与错误 code，是「任务追踪」屏数据源。
- **事件扇出**：每任务订阅 channel，慢消费者**丢弃最旧帧**而非阻塞运行，
  缓冲上限 `AGENTHUB_EVENT_BUFFER`（默认 512）。
- **错误码透传**：编排失败把错误 code 带到对应 span，前端可直接呈现。

---

## 六、持久化（PostgreSQL + GORM）

资源、任务、产物、span、挂起问题、回调配置、事件流与回调留痕全部落库；服务重启后
可回读与回放，容器本身无状态。

### 数据表

- **`resources`**：条目全字段 + 治理列（`namespace`、`status`、`display_name`、
  `source_id` 与 per-kind 的 `kind_config`），`tags` / `capabilities` / `auth_schemes` /
  `env` / `metadata` / `kind_config` 为 `jsonb`；`(namespace, name, version)` 唯一。
- **`registries`**：命名空间，各带审批策略。
- **`registry_sources`**：上游注册中心源与最近一次同步结果
  （`last_sync_at` / `status` / `count` / `error`）。
- **`tasks` + 子表**：任务主体与状态；`task_artifacts`、`task_spans`、`task_pending`、
  `task_push` 随任务级联删除。
- **`events`**：每个 broker 帧，`(task_id, seq)` 主键，供 `since=<seq>` 回放。
- **`push_records`**：每次回调留痕（url / state / config 快照），
  按 `AGENTHUB_PUSH_RECORD_LIMIT`（默认每任务 100 条）保留。
- **`api_keys` / `api_key_scopes`**：长期持有的 bearer 凭据。库内只存令牌的
  `sha256`（`token_hash`）与展示用 `prefix`（`ahk_xxxxxxxx`），**绝不存密钥本身**；
  另含可选 `expires_at` 与软删除 `revoked_at`（吊销后仍保留行以留存审计线索，但
  立即失去认证能力）；`api_key_scopes` 是 key → permission 的边。
- **`schema_migrations`**：已执行的迁移文件，启动时跳过。
- **jsonb 载荷**：由自实现 `model.JSONField`（`driver.Valuer` / `sql.Scanner`）承载，
  **不引入** `gorm.io/datatypes`。

### 迁移与写入策略

- **内嵌幂等迁移**：`migrations/*.sql` 经 `//go:embed` 内嵌，按文件名顺序、每文件单事务
  执行；SQL 自身幂等，可重复执行、可审计、可回滚。`008_registry_governance.sql` 与
  `009_registry_sync.sql`补上治理列、去重启用唯一索引、命名空间与上游源——
  语句全部 `IF NOT EXISTS` / `ON CONFLICT DO NOTHING`，升级前已存在的条目保持 `approved`。
  后续迁移沿用同一契约：`010_api_keys.sql`（API 密钥表）、`011_login_protection.sql`（账户锁定列）、
  `012_threads.sql`（协作线程）、  `013_persona.sql` / `014_channels.sql` / `015_thread_origin.sql`
  （Persona / Channels / 语音 origin），以及 `016_wechat_login.sql`（放开 `users.email` 非空约束，
  以便仅由 OAuth 绑定 `(provider, subject)` 标识的微信账号也能入库），以及 `017_computers.sql` /
  `018_schedules.sql` / `019_knowledge.sql`（agent 沙箱 / 主动调度 / mock RAG 片段）。
- **条件更新防丢更新**：任务状态迁移走 `UPDATE ... WHERE id=? AND state=?` 并校验
  `RowsAffected`，0 行时区分「任务不存在」与「并发冲突」。
- **持久化不阻塞运行**：状态 / artifact / span 同步写；`events` 与 `push_records`
  **尽力而为**（有界队列 + 异步 writer，队列满或失败只告警，**绝不回压任务执行**）。
- **客户端断连不丢状态**：所有持久层调用统一走 `context.Background()`。
- **移除 JSON snapshot**：`DATABASE_URL` 必填，缺失即 `config.Validate` 拒绝启动，
  不存在无库降级路径。

### 事件回放与回调留痕

- **断线重连回放**：`GET /api/tasks/{id}/events?since=<seq>` 先回放该 seq 之后的持久化帧，
  再接实时流；无 `since` 时回放 span / artifact / status 后订阅直播。
- **seq 续接**：事件序号在启动后从库内最大 seq 续起，回放帧与实时帧不会撞号。
- **回调留痕**：`MockNotifier` 每次回调落 `push_records`，失败只告警并回退内存，
  **不得影响任务终态**；真实 HTTP 投递仍属 M2。

---

## 七、控制台

Vue 3 + Vite（bun）构建产物由 Go 进程托管于 `/`，既是运维界面，也是 A2A 契约的可视化
检查器。

### 八屏

- **注册中心**：六类（含 `custom`）筛选 + 搜索、命名空间与状态筛选、健康指示与统计条、
  单资源「试跑」。注册抽屉改为按 kind 切换专属配置区块——model 看 router、MCP 看 transport、
  skill 看步骤与手册；每张卡片带审批徽标，只给出当前状态允许的迁移动作。
- **命名空间**：命名空间与其审批策略，外加待审批队列（行内批准 / 驳回）。
- **上游源**：上游注册中心源、手动「立即同步」，以及最近一次同步的
  导入 / 更新 / 跳过三计数。
- **任务追踪**：任务列表 + 状态过滤、span 时间线回放、artifact 原文可复制。
- **拓扑视图**：五泳道资产与调用边，线型区分活跃与失败。
- **登录注册**：首个账号即 owner，`/api/*` 无会话一律关闭。
- **管理**：用户、组、角色、权限与资源授权，外加第四个子页「数据库检查点」
  （打点 / 下载 / 恢复 / 定时），以及新增的**API 密钥**子页——为非交互式访问
  签发、限定范围、轮换与吊销长期持有的 bearer 凭据。
- **文档**（`/docs`）：控制台自带文档，随构建发版、离线可读。五章——核心概念、A2A 方法、
  HTTP 接口、认证与权限、错误码——左侧索引随滚动高亮。  端点表覆盖**完整路由表（约 100 条）**，
  逐条给出方法、路径、所需权限，以及取自各 handler 真实编解码结构的请求/响应示例。
  入口位于侧边栏「管理」之后，**不加权限门控**：任何已登录账号都可读；而「管理」自身
  仍按其权限显隐，文档项不随之消失。
  内容与文案分家：`lib/docs-content.js` 承载机器形状的一半（方法、路径、权限码、字段名、
  JSON 示例、HTTP 原因短语），各语言原样渲染；`lib/copy/docs-copy.js` 只放散文，13 语齐备。
  这条边界由 `web/tests/copy-fallback.spec.js` 强制——它断言每条非英译文都异于英文，
  因此在任何语言下都相同的串**不能**进 copy 表。

### 多语言与单色体系

- **13 种语言**：en / zh / zh-TW / es / fr / de / it / ru / ja / ko / pt / ar / hi，
  全量母语翻译，英文兜底，`ar` 为 RTL；运行时切换同步 `<html lang>` 与 `dir`。
- **零硬编码自然语言**：组件内一律 `t()`，后端枚举由 `copy/enums-copy.js` 映射。
- **纯黑/白/灰单色**：所有 hex 满足 **R=G=B**，禁止任何有彩色相；状态靠
  填充 / 描边 / 动效编码，各类资产以「灰阶明度 + 专属图标 + 文字标签」三重冗余区分。
- **自动护栏**：`web/tests/style.spec.js` 校验色彩；`prefers-contrast` 与
  `prefers-reduced-motion` 均有降级。

---

## 八、部署与运维

### 交付形态

- **Go 单二进制**：`bin/agenthub-server` + 构建期生成的 `backend/static`。
- **三段构建镜像**：`oven/bun` 构建控制台 → `golang:1.27-alpine` 编译 →
  `alpine:3.21` 运行时；以**非 root 用户**（uid 10001）启动，`/healthz` 为健康检查端点。
- **docker compose**：一条 `docker compose up -d` 起 `postgres:16-alpine`（宿主 5434）+
  `agenthub`，数据库 healthy 后服务才启动，启动时自动执行迁移与种子；
  `--profile dev` 另起 bun 开发服务器（5173）。
- **健康检查**：`/healthz` 返回版本、协议版本、资源与任务计数、模式、鉴权模式与
  **`database`（up / down / unknown）**；库不可达返回 503。

### 配置与安全治理

- **必填项**：`DATABASE_URL`（缺失即拒绝启动）。
- **端口与环境**：`AGENTHUB_HTTP_PORT`（默认 3500）、`AGENTHUB_ENV`
  （dev / staging / prod）。
- **鉴权**：真实账号 + 会话 + RBAC——首个账号即 owner，`/api/*` 无会话一律关闭，
  `AGENTHUB_COOKIE_SECURE` 把会话 cookie 钉在 HTTPS 上；**生产红线**：
  `AGENTHUB_ENV=prod` 未配 `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` 时拒绝启动
  （共享 admin token 已移除）。OAuth 支持 Google、GitHub、通用 OIDC 与**微信**网页登录
  （`snsapi_login`）；微信账号仅以 `(provider, subject)` 绑定标识，故 `users.email` 放开非空
  （迁移 `016_wechat_login.sql`）。
- **限额**：`AGENTHUB_MAX_PARALLEL`（8）、`AGENTHUB_CLI_TIMEOUT`（15s）、
  `AGENTHUB_CLI_ALLOWLIST`（git / ls / cat / echo / date / aria-memo）、
  `AGENTHUB_MAX_OUTPUT_BYTES`（65536）、`AGENTHUB_EVENT_BUFFER`（512）、
  `AGENTHUB_PUSH_RECORD_LIMIT`（100）。
- **安全中间件**（见第十三节）：按 IP 限流（认证端点 10 次/分钟/IP，其余 200 次/分钟/IP）、
  对变更型请求做 CSRF 同源 `Origin` 校验、以及安全响应头（HSTS 仅在 TLS 下发出）。账户暴力破解
  防护为**连续 5 次失败锁定 15 分钟**（迁移 `011_login_protection.sql`）。
- **目录相关**：`AGENTHUB_MODEL_ROUTER_MODE`（`mock` | `openai_compatible`，
  默认 `mock`——条目自身配置优先于这个全局默认）、`AGENTHUB_REGISTRY_SYNC_ENABLED`
  （默认 **false**）、`AGENTHUB_REGISTRY_SYNC_INTERVAL`（默认 `1h`，开启同步时须为正）
  与 `AGENTHUB_REGISTRY_SYNC_FIXTURE`（默认 **true**，把所有源钉在离线客户端上——要真正
  出网，运维必须显式关掉它）。
- **API 密钥认证**：面向非交互式访问（脚本、CI、其他语言）的长期 bearer 凭据
  （形如 `ahk_…`）。密钥以其**持有者**身份完成认证，但仅拥有创建时选定的**权限子集**；
  该子集与持有者自身权限取交集，因此密钥永不能超过其持有者、也**不能静默签发更多密钥**
  （`apikeys:manage` 是独立权限，不被任何其他权限隐含）。原始令牌在
  `POST /api/api-keys` 时**仅返回一次**；库内只存其 `sha256`（`token_hash`）与展示用
  `prefix`（`ahk_xxxxxxxx`），绝不存密钥本体。密钥带可选 `expires_at` 与软删除
  `revoked_at`，吊销后行保留、立即失效。在任意 `/api/*` 路由与 A2A 端点上，可经
  `Authorization: Bearer ahk_…` 或 `X-AgentHub-Key` 头携带；不可用的密钥直接回退为
  匿名，因此公开端点依旧可用。
- **治理权限**：`registries:manage`（命名空间）、`registry:curate`
  （批准 / 驳回 / 废弃）、`sources:manage`（上游源与同步）、`apikeys:manage`
  （为任意用户列出 / 创建 / 轮换 / 吊销 API 密钥——密钥可行动但不可再签发密钥）。
  owner 持有全部权限。
- **加密与数据库加固**：`AGENTHUB_ENCRYPTION_KEY`（生产须 ≥ 32 字节，由 `config.Validate`
  强制；缺失则回退会话密钥并告警）隔离静态加密、并为备份归档加密。`AGENTHUB_PUSH_SECRET`
  （设置时须 ≥ 16 字节）开启 HMAC 签名的 A2A 回调。连接池经 `AGENTHUB_DB_MAX_OPEN_CONNS`（25）/
  `AGENTHUB_DB_MAX_IDLE_CONNS`（10）/ `AGENTHUB_DB_CONN_MAX_LIFETIME`（30m）可调；运行期凭据
  改用 `scripts/provision-db-roles.sql` 生成的 `app` / `readonly` 最小权限角色。
- **日志**：`slog` JSON，不打正文、密钥与向量；DSN 经 `store.RedactDSN` 脱敏后再记录。

---

## 九、测试与验收

- **`go vet ./...` 无告警**：`go test -race ./...` 全绿，覆盖 a2a、config、registry、
  adapter、hub、model、store、handlers、httputil。
- **纯逻辑用例零外部依赖**：未注入仓库时 registry / TaskStore / Broker / MockNotifier
  退化为内存语义，无 DB 也能全绿。
- **DB 门控集成用例**：`tests/integration_test.go` 覆盖迁移幂等、资源 CRUD 与 jsonb 往返、
  任务全生命周期与级联删除、事件回放、push 留痕与保留上限、完整 A2A run 落库回读；
  **未设置 `DATABASE_URL` 则 skip，绝不伪造结果**。
- **治理用例**：`registry` 的 store 分支由**包内假 store** 驱动
  （`store_mode_test.go`），因此去重启用键、审批状态机与持久化错误归一都无需数据库即可覆盖；
  `internal/store/registry_governance_test.go` 与
  `tests/registry_governance_integration_test.go` 覆盖真正需要 PostgreSQL 的部分——治理列与
  per-kind 配置的 jsonb 往返、唯一索引拒绝重复、命名空间 CRUD、同步结果留痕。两者均以
  `DATABASE_URL` 门控。
- **覆盖率**：`make test-cover` 跑 `-coverprofile` 并打印 `go tool cover -func`。
  带库时：`a2a` 97.8%、`model` 93.9%、`registry` 89.7%、`authz` 87.9%、`hub` 87.8%、
  `adapter` 83.3%、`config` 80.3%、`httputil` 80.2%、`handlers` 68.7%、`backup` 58.2%、
  `auth` 56.9%、`store` 22.6%（未设 `DATABASE_URL` 时它的用例 skip，数字不代表真实覆盖）。
- **端到端用例**：`tests/e2e_test.go` 以 httptest 真实 HTTP 覆盖六段链路
  （启动检查 → 服务发现 → 发起任务 → 流式运行 → 挂起恢复 → 控制面闭环）。
- **前端**：`bunx vitest run` 57 项通过（languages / copy-fallback / enums-copy / api /
  format / errors / backup-api / style 护栏）。
- **A2A 与控制面冒烟**：send（五类各一次）、stream（无 `[DONE]`、终态帧结尾）、
  `input-required` → `tasks/resume`、`tasks/get` 历史裁剪、v0.1 别名、flow 分支与并行、
  错误码路径、注册 → 调用 → 删除闭环。
- **API 密钥覆盖**：`internal/handlers/api_apikeys_test.go` 以内存密钥仓库覆盖列表
  （本人 vs 全部）、创建时范围钳制、管理守卫（403 / 404 / 400）、更新与吊销，以及
  bearer 密钥认证；`tests/apikey_integration_test.go` 以 `DATABASE_URL` 门控 PostgreSQL
  路径（迁移、令牌哈希查找、范围往返、吊销 + 审计行）。
- **脚本化接口测试**：`scripts/test-api.sh` 新增治理段落——建命名空间、记录走
  `draft → pending → approved`（并验证 `draft → approved` 被拒、草稿不进可发现列表）、
  去重启用键、fixture 源同步两次（首轮导入、次轮刷新）。对着运行中实例：**167 通过、
  0 失败、3 跳过**。
- **示例装载脚本**：`example/load.sh` 对着运行实例灌入全部 41 份示例载荷——首轮
  41 条 created，后两轮各 41 条 updated，经 `GET /api/resources?namespace=…` 回读全部为
  `approved` 且 `healthy`。

---

## 十、示例目录

`example/` 随仓库交付一份可直接灌进运行实例的目录：**41 份完整 `registry.Entry` 载荷**加一个
可重复执行的装载脚本，于是新实例不再只能靠五条 `demo-*` 种子起步。

```bash
cp .env.example .env       # 然后把 AGENTHUB_EXAMPLE_ENABLED 改成 true，
                           # 并填 AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh          # 按 uname -s 选平台
./example/load.sh --all  # 两个平台都灌
```

装载是显式开启的：未设置 `AGENTHUB_EXAMPLE_ENABLED=true` 时装载脚本拒绝执行；
示例相关配置统一使用 `AGENTHUB_EXAMPLE_` 前缀，避免与服务端自身配置撞名。

| 类别 | 注册了什么 |
| --- | --- |
| model | OpenAI 兼容网关；`kind_config.model.api_key_env` 只存环境变量名，凭据既不进 JSON 也不入库 |
| agent | Google ADK、LangGraph、CrewAI、Pi、DeepSeek Harness |
| mcp | 官方 filesystem server，外加 GitHub、Google Drive、Google Docs、Gmail、Slack、Stripe、微信、钉钉、飞书 |
| skill | `anthropics/skills` 与 [skills.sh](https://www.skills.sh) |
| cli | `gh`、`hf`（HuggingFace）、`modelscope` |

- **两个命名空间**：`example-ubuntu` 与 `example-windows`，因为去重启用键是
  `(namespace, name, version)`；model 记录共享在 `example` 下。
- **装载幂等**：先 POST（201），命中 `resource_already_exists` 再回退 PUT——
  `PUT /api/resources/{id}` 不是 upsert，id 不存在直接返回 404。
- **凭据纪律**：网关密钥只留在服务端进程环境（`AGENTHUB_EXAMPLE_API_KEY`）；CLI 命令要先写进
  `AGENTHUB_CLI_ALLOWLIST` 才能跑，且该白名单按**整串**忽略大小写匹配。
- **是配置意图、不是可执行对接**：只有 model 连接器是真实实现，agent / mcp / skill / cli 四
  类适配器仍是 `TODO(real)`，这些载荷描述的是配置而非真实集成。

`example/README.md` 另附载荷契约（21 个可用键、`kind` / `transport` / `health` / `status`
四类枚举、以及 per-kind 的 `kind_config` 与其校验），以及每类上游项目在两个平台上的安装与
启动命令。

---

## 十一、Persona、Agent 能力、Channels、Connectors 与 Speech（M2）

在 M1 的资源注册与 A2A 编排之上，agenthub 新增 agentbuddy 消费的协作面：资源 **Persona**、
外部 **Channels**、策略网关 **Connectors** 与 **Speech**（离线 STT/TTS）。全部新端点复用既有
`resources:read` / `tasks:read` / `tasks:write` 权限，不新增角色或用户组。

- **Persona（F10）**：`Entry.Persona`（`DisplayName` / `Title` / `Tagline` / `AvatarURL`）由
  `normalizePersona` 归一化——各文本字段去除首尾空格并截断到 256 字符、`AvatarURL` 仅限
  http(s)（否则置空）、整体为空时归 nil——落 `resources.persona` JSONB（迁移 `013_persona.sql`）。
- **Channels**：绑定到线程的 Slack / Teams / Telegram / SMS 通道。入站走签名校验的公开
  webhook（`POST /api/channels/{id}/webhook`，密钥仅存环境变量名）转发进线程；出站走 SSRF 安全的
  `Deliver`（`POST /api/channels/{id}/deliver`，`netutil.ValidateURL` + `SafeClient`，回显
  Provider 原始响应 ≤512B，不重试）投递到 `config.target_url`。落 `channels` + `channel_messages`
  （迁移 `014_channels.sql`）。
- **Connectors**：`kind=mcp` 资源的策略网关——`GET /api/connectors` 列出、`PUT /api/connectors/{id}/policy`
  设 `off` / `ask_first` / `allowed`；`ask_first` 经 `Hub.ParkConnector` 挂起一个 `input-required`
  任务等待人工批准。`GET/POST /api/connectors/{id}/actions` 触发动作。
- **Speech**：`POST /api/threads/{id}/audio` 上传语音片段，服务端离线转写（STT mock）后作为
  `origin=voice` 消息委派进线程，并把 agent 回复合成 TTS 音频作为 artifact。受
  `AGENTHUB_SPEECH_MODE` / `AGENTHUB_AUDIO_MAX_BYTES` 控制；迁移 `015_thread_origin.sql` 为
  `thread_messages` 增加 `origin` 列。
- **Agent 能力端点（F10–F16 脚手架）**：在 Persona 之外，agenthub 新增 agent 能力面的首片——
  agent 沙箱、主动调度与 mock RAG。全部为确定性脚手架：深层能力（真实 browser / shell / screen
  控制、真实调度引擎、向量检索）统一标 `TODO(real)`，留待后续里程碑。不新增角色或权限码，端点复用
  既有 `resources:*` 与 `tasks:*`。
  - **Computers（F11）**：绑定到 agent、种类为 `browser` / `shell` / `screen` 的沙箱。
    `GET/POST /api/computers` 与 `GET /api/computers/{id}` 创建、列出沙箱并读取单个。首版只记录
    控制台可挂载的会话（`session_ref` / `status` / `last_action`）；真实沙箱驱动标 `TODO(real)`。
    落 `computers`（迁移 `017_computers.sql`）。
  - **Schedules（F13）**：hub 按 cron 或外部触发主动推进的目标。`GET/POST /api/schedules`、
    `GET/PUT/DELETE /api/schedules/{id}` 管理目标；首版只存意图（`goal` / `trigger` / `cron_expr` /
    `enabled` / `next_run`），mock 调度器在被触发时复用 `Hub` 创建任务——把表达式注册进真实调度器
    标 `TODO(real)`。落 `schedules`（迁移 `018_schedules.sql`）。
  - **Knowledge（F14）**：mock RAG 检索。`GET /api/knowledge?agentId=…` 返回为该 agent 存储的
    `chunk_text` / `source_ref`；真实向量嵌入与相似度排序标 `TODO(real)`。落 `knowledge`
    （迁移 `019_knowledge.sql`）。
  - **权限与错误**：Computers 挂 `resources:read` / `resources:write`，Schedules 挂
    `tasks:read` / `tasks:write`，Knowledge 挂 `resources:read`。未知 id 返回 `computer_not_found`
    / `schedule_not_found`（404），无目标调度返回 `schedule_invalid`（400），而 store 未接入时端点直接
    返回 `feature_unavailable`（503）而非 404——因此未建这些表的部署可干净降级。
- **文档**：`/docs` 控制台已在 Collaboration 分组用 13 语完整记录全部新端点，含 agent 能力、
  Channels、Connectors 与 Speech（见 `web/src/lib/docs-content.js` 与 `web/src/lib/copy/docs-copy.js`）。

## 十二、Kubernetes Operator（运维控制器）

面向 Kubernetes 原生部署，`agenthub-operator` 对 `AgentHub` CRD
（`agenthub.ai/v1alpha1`）做调和（reconcile），为每个实例拉起完整工作负载：
ConfigMap（`<实例>-config`）、Secret（`<实例>-secrets`）、ServiceAccount、
应用侧最小权限的 Role + RoleBinding、Deployment、Service，以及（开启持久化时）
PersistentVolumeClaim。子资源都带 owner 引用，随 CR 删除而回收，控制器 watch
全部被管类型。

- **托管值 vs. 外部 Secret**：CR 内联字段（数据库 URL、引导管理员密码/邮箱、
  加密密钥、推送密钥、OAuth 客户端密钥）写入托管 Secret `<实例>-secrets`；
  某字段也可改设 `ExistingSecret`——此时 operator 不再托管其值，而是渲染一个
  指向你自备 Secret 的 `SecretKeyRef`，部署因此引用的是你自己掌控的 Secret。
- **Helm 安装**：`helm install agenthub-operator operator/deploy/agenthub-operator
  -n agenthub-operator --create-namespace` 一键铺好 Deployment、operator 自身的
  Role/RoleBinding/ServiceAccount 以及 CRD。
- **调和策略**：`upsert` 创建被管对象并以 merge patch（而非整体 `Update`）更新，
  避免应用侧的并发状态写触发乐观锁冲突。

---

## 十三、安全加固

agenthub 现已自带可直连公网所需的全部防护：安全响应头、按 IP 限流、CSRF 防御、账户锁定、
SSRF 防护的出站调用、签名 A2A 回调、静态加密与最小权限数据库角色。未引入任何新的生产依赖——
全部由标准库 + GORM 实现。设计同构于 `ariacompute/serve` 五大域加固，并按 agenthub 的单二进制、
入库会话架构适配（完整方案见 `requirements-security.md`，实施记录见 `task-security.md`）。

### 传输与边缘

- **安全响应头**（`internal/middleware`）：`X-Content-Type-Options`、`Referrer-Policy`、
  `Cross-Origin-Opener-Policy`、`Cross-Origin-Resource-Policy`，以及灰度期的
  `Content-Security-Policy-Report-Only`。HSTS 仅在 `AGENTHUB_ENV=prod` 或请求已走 TLS
  （`r.TLS` / `X-Forwarded-Proto: https`）时发出。二进制本身不终止 TLS——生产须在前面前置 TLS
  终止反代（Caddy / nginx）。

### 认证与限流

- **账户锁定**（`migrations/011_login_protection.sql`）：连续 **5 次**失败登录锁定账户
  **15 分钟**；登录成功清空计数与锁定。不存在的账户返回同一错误，无法被枚举；锁定期内返回新的
  `account_locked`（429）码。
- **按 IP 限流**：认证端点 **10 次/分钟/IP**；`/api` 与 `/a2a` 共用 **200 次/分钟/IP**
  （超出返回 `429` + `Retry-After`）。静态、`/healthz` 与发现路径不限。
- **CSRF**：变更型请求须携带同源 `Origin`；`GET/HEAD/OPTIONS`、无 `Origin` 的请求，以及非浏览器客户端
  （如脚本）放行——同源的控制台 fetch 不受影响。
- **审计卫生**：日志只记 IP 与 user id，绝不记密码、令牌或 PII。
- 会话是入库的随机令牌（无 JWT / 无 JTI），吊销即时生效、不可重放。

### A2A 回调与上游同步（SSRF）

- **真实推送通知**：`HTTPNotifier` 取代仅留痕的 mock，向调用方提供的 `URL` 发起 POST。目标仅限
  https，且永不会解析到环回 / 私网 / 链路本地 / 多播地址（`internal/netutil.SafeClient`）；当
  `AGENTHUB_PUSH_SECRET` 设置时，载荷以 HMAC-SHA256（时间戳 + nonce + body）签名，供接收方验证
  真实性、新鲜度（±5 分钟）与重放；`(taskId, state)` 做 **1 小时幂等**去重。投递失败只告警，
  绝不回压任务。
- **受防护的同步客户端**：上游注册中心同步客户端复用同一 `SafeClient`，并在 `Fetch` 入口校验 URL。

### 密钥、数据与数据库

- **静态加密**：`AGENTHUB_ENCRYPTION_KEY`（生产须 **≥ 32 字节**，否则拒绝启动）隔离未来的静态
  加密；缺失时回退会话密钥并告警，并支持旧密钥回退的密钥轮换。
- API 密钥以哈希存储（`token_hash`）；OAuth 客户端密钥是环境变量引用，绝不入库。注入审计确认全部
  SQL 均参数化。
- **数据库**：运行期改用最小权限角色；`scripts/provision-db-roles.sql` 生成 `app`（负责迁移）与
  `readonly` 两角色。连接池经 `AGENTHUB_DB_MAX_OPEN_CONNS`（25）/ `AGENTHUB_DB_MAX_IDLE_CONNS`（10）/
  `AGENTHUB_DB_CONN_MAX_LIFETIME`（30m）可调；数据库连接是否启用 TLS 由 DSN 自行决定（`sslmode=…`）。
- **备份加密**：设置 `AGENTHUB_ENCRYPTION_KEY` 时，检查点归档以 AES-256-GCM 静态加密为
  `<file>.tar.gz.enc` 并附 `<file>.enc.sha256` 清单；定期用 `scripts/backup-restore-drill.sh`
  演练可恢复性。
