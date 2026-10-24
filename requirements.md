# requirements.md — agenthub（A2A 互连平台）

> 功能边界 / 协议契约 / 数据模型 / 接口 / 异常 / 多语言与视觉约束 / 验收标准。
> 须经人工逐项审核后，方可据其生成 `task.md` 实施。当前实现对应 **M1（已落地）**。

## 1. 功能边界

### 1.1 范围内（M1）

- **A2A 服务端（冻结契约）**
  - `GET /.well-known/agent.json` 暴露 Agent Card（兼容路径 `GET /a2a/agent-card`）。
  - `POST /a2a` 为 JSON-RPC 2.0 **单入口**，支持 `message/send`、`message/stream`（SSE）、
    `tasks/get`、`tasks/cancel`、`tasks/resume`、`tasks/pushNotification/set`、
    `tasks/pushNotification/get`；`AGENTHUB_A2A_LEGACY_METHODS=true` 时额外接受 v0.1 别名
    `tasks/send`、`tasks/sendSubscribe`。
  - 任务状态机 `submitted → working → input-required → completed / failed / canceled`，
    含非法迁移校验。
  - `input-required` 支持**无限期挂起**，并携带 `taskId` 通过 `tasks/resume` 续接恢复
    （human-in-the-loop 的 suspend/resume）。
  - 流式 SSE **无 `[DONE]` 哨兵**：每帧是一个 JSON-RPC response（携带请求 id），
    运行以终态或 `input-required` 状态事件结束。
- **五类资源注册中心**：model / agent / mcp / skill / cli 统一条目的增删改查与发现；
  条目为 **Registry 级**（`name / version / description / repository / tags / auth_schemes /
  env / transport / endpoint / capabilities / health / metadata`）。
  支持**导出**（每个资源渲染为一个 Agent Card skill；整册导出为 MCP server 描述），
  与消费对称。
- **适配器层**：统一 `Connector` 接口 + `For(kind)` 工厂；首版五类全为**确定性 mock**
  （同输入必同输出，便于 `-race` 断言），真实实现以 `// TODO(real)` 标注：
  model→OpenAI 兼容端点（Model Router 形态：单一 `Generate/Stream` + provider 注册表 + 回退链）、
  agent→`aria-agent-cloud` SSE、mcp→stdio / Streamable HTTP JSON-RPC、skill→本地手册加载、
  cli→`exec.Command` + 白名单。CLI 必须实现「白名单前置校验 → 单次超时 → 输出截断 → 结构化 JSON」。
- **Hub 编排与任务机**：入站消息 → 资源选择 → 适配执行 → 状态机 → artifact 累积 →
  事件广播（SSE fan-out）→ pushNotification 回调（**落 `push_records`，不发真实网络请求**）。
  编排支持 **flow 三原语 `then / branch / parallel`**，用于多资源接力。
- **可观测**：每任务生成 **span 时间线**（kind、耗时、状态、错误 code），作为「任务追踪」屏数据源；
  首版只埋点，不接外部 exporter。
- **PostgreSQL 持久化（GORM）**：`DATABASE_URL` **必填**，缺失即拒绝启动。资源条目、任务主体、
  artifact、span、挂起问题、回调配置、事件流与回调留痕全部入库；未注入 GORM 仓库时
  registry/hub/broker/notifier 退化为**原有内存行为**（公开签名零变更），单测因此零外部依赖。
- **控制面与运维**：`/api/*` 管理接口 + `/healthz`（版本、适配器、资源计数、当前模式、鉴权模式、
  **数据库可达性**）；admin token 鉴权，open 模式仅本地可用。
- **Web 控制台八屏**：注册中心（六类 CRUD + per-kind 配置 + 审批状态 + **单资源「试跑」**）、
  命名空间与待审批队列、上游注册中心源、任务追踪（任务列表 + 事件/span 时间线 + artifact 原始内容）、
  拓扑视图（五列资产 + 调用边）、登录/注册、管理后台（用户 / 权限矩阵 / 资源授权 / 数据库检查点）、
  **文档**（`/docs`，控制台自带的功能与接口说明，13 语，无权限门控；详见 §9）。
- **多语言 13 种**：`en / zh / zh-TW / es / fr / de / it / ru / ja / ko / pt / ar / hi`（`ar` 为 RTL）。
- **纯黑/白/灰单色视觉**：禁止任何有彩色相，靠明度分级 + 线型 + 图形 + 动效编码语义。

### 1.2 范围外（列为后续项）

- **pgvector / 向量检索**（属 `memo` 与 `bankmind` 的职责；本仓只需要关系型 PostgreSQL）。
- RAG / 记忆存储（属 `memo` 与 `bankmind` 的职责）。
- 语音、浏览器自动化、渠道接入、多语言 SDK、Agent 网络跨实例联邦。
- LLM-as-judge 评分与评测闭环（eval 仅留钩子，**不产出分数**）。
- 真实 MCP stdio 子进程、真实 CLI `exec`、真实模型端点（统一留 `TODO(real)`）。

## 2. 协议契约

### 2.1 传输与信封

| 项 | 约定 |
| --- | --- |
| 发现 | `GET /.well-known/agent.json` / `GET /a2a/agent-card` |
| 调用 | `POST /a2a`，`Content-Type: application/json` |
| 信封 | JSON-RPC 2.0：`{jsonrpc:"2.0", id, method, params}` / `{jsonrpc:"2.0", id, result\|error}` |
| 版本不符 | `-32600 invalid_request`（HTTP 400） |
| 方法不存在 | `-32601 method_not_found`（HTTP 404） |
| params 缺失/含未知字段 | `-32602 invalid_params` |
| 流式 | `Content-Type: text/event-stream`，帧名 `task_status_update` /
  `task_artifact_update` / `message`；错误帧名 `error`；**无 `[DONE]`** |

### 2.2 任务状态

`submitted` / `working` / `input-required` / `completed` / `failed` / `canceled` / `unknown`。

合法迁移（`CanTransition`）：
- `submitted → working | failed | canceled`
- `working → input-required | completed | failed | canceled`
- `input-required → working | failed | canceled`
- 终态不可再迁出；`unknown` 不可作为起点或终点。

### 2.3 Part 与 Artifact

- `Part`：`text` / `file`（`FileContent`：name/mimeType/bytes/uri）/ `data`。
- `Message.Text()` 拼接所有 text part；`Message.Context()` 取 `contextId`，回退 `sessionId`。
- `Artifact` 可追加（`append` / `lastChunk`），任务结束时按累积列表返回。

### 2.4 flow 三原语

`metadata.flow` 接受 JSON 数组 / JSON 字符串 / 单对象（`ParseSteps`），非法输入 → `invalid_flow`。

| 原语 | 语义 |
| --- | --- |
| `then` | 顺序执行一个资源 |
| `branch` | 按 `when` 选**首个匹配臂**；无匹配则整臂跳过（span 记 `skipped`） |
| `parallel` | 并发执行成员；成员只读各自上下文副本；分支数受 `AGENTHUB_MAX_PARALLEL` 截断 |

`when` 语法：`""` / `always` / `true`（默认臂）、`contains:<text>`、`kind:<kind>`、`has:<key>`。
任一成员失败 → 该 `parallel` 失败并把错误 code 带到 span；任一节点要求输入 → 流程**提前停止**并挂起。

## 3. 数据模型

### 3.1 资源条目（`registry.Entry`）

| 字段 | 说明 |
| --- | --- |
| `id` | 形如 `<kind>-<hex>`，缺省自动生成 |
| `kind` | `model`/`agent`/`mcp`/`skill`/`cli`/`custom`（`invalid_kind`）；新增字段全部 `omitempty`，向后兼容 |
| `name` | 必填，去空白（`invalid_entry`） |
| `namespace` | 归属的 registry 命名空间，缺省 `default`（`invalid_namespace`） |
| `status` | `draft`/`pending`/`approved`/`rejected`/`deprecated`；空值按 `approved` 处理（`invalid_status_transition`） |
| `display_name` / `source_id` | 展示名；由上游注册中心同步而来时的来源 id |
| `version` / `repository` / `tags` / `env` / `auth_schemes` | Registry 级元数据 |
| `endpoint` / `transport` | 传输**描述**字段，适配器不得分支（`invalid_transport`） |
| `capabilities` | 能力清单（MCP 为工具名，skill 为步骤工具） |
| `health` | `healthy`/`degraded`/`unhealthy`/`unknown`（`invalid_health`） |
| `metadata` | KV；`cli` 用 `command` 指定白名单命令 |
| `kind_config` | per-kind 专属配置（`jsonb`，见 §3.1.1），注册时经 `ValidateKind` 按 Kind 校验（`invalid_kind_config`） |

#### 3.1.1 per-kind 配置（`KindConfig`）

注册抽屉按 kind 切换配置区块，后端按同一结构校验（对照 AWS `recordType` 的 schema 校验）：

| Kind | 配置区块 |
| --- | --- |
| `model` | `provider_mode`（`mock` / `openai_compatible`）、`endpoint`（OpenAI 兼容网关：LiteLLM / OpenRouter / 自建）、`model_id`、`api_key_env`（**只存环境变量名**）、`targets`（有序回退链 + 权重）、`timeout_ms`、`fallback_mock`（可选 mock 兜底） |
| `agent` | Agent Card URL、`protocol_version`、capabilities（streaming / pushNotifications / stateTransitionHistory）、skills、`auth_scheme` |
| `mcp` | `transport`（stdio / streamable-http / sse-legacy）分流的 command+args 或 URL、tool allowlist、auth schemes、`env` |
| `skill` | version、repository / package、触发词、步骤、工具清单、markdown 文档 |
| `cli` | 可执行命令、命令白名单、超时、输出截断字节、工作目录、`env` |
| `custom` | 任意合法 JSON schema 文本 |

#### 3.1.2 去重启用键与治理

- 去重启用键是 `(namespace, name, version)`：同名跨版本是**不同**记录（对齐 AWS
  `name + recordVersion`）；由 `idx_resources_dedup` 唯一索引（内存模式同语义）保证，冲突 →
  `resource_already_exists`（400）。
- 审批状态机：`draft → pending → approved / rejected`、`approved → deprecated`、
  `rejected|deprecated → pending`；非法迁移 → `invalid_status_transition`（400）。
  状态通过 `POST /api/resources/{id}/status` 推进（`registry:curate`）。
- 消费方**只见 approved**：`registry.Discoverable` 是唯一过滤出口，覆盖列表、MCP 导出、
  Agent Card 与 A2A 寻址；未批准记录不得被调用。
- 命名空间（`registries`）：名称、描述、`auto_approve`（新记录是否直接可发现）；`default`
  由迁移种子创建且不可删除。
- 上游注册中心同步（`registry_sources`）：kind（`mcp` / `skill`）、url、auth（`none` / `bearer`，
  `auth_token_env` **只存环境变量名**）、schedule（手写 5 段 cron）、`fixture`（离线确定性模式）、
  手动或定时同步；按 `(name, version)` 去重 upsert，回写 `last_sync_at/status/count/error`。
  真实 HTTP 客户端设超时 + 限长 + 条目上限；外网用例缺凭据 skip，**绝不伪造同步结果**。

### 3.2 任务与追踪

- `Task`：`id`、`contextId`、`status`、`artifacts[]`、`history[]`、`metadata`、`resourceId`。
- `Span`：`id`、`taskId`、`resourceId`、`kind`、`label`、`startAt`、`endAt`、`durationMs`、
  `status`（`ok`/`error`/`input-required`/`skipped`）、`code`。
- `Event`（broker 帧）：`seq`、`taskId`、`kind`（`status`/`artifact`/`message`/`span`）、`at`、载荷。
- `PushRecord`（回调留痕）：`taskId`、`url`、`state`、`config` 快照。

### 3.3 持久化（PostgreSQL + GORM）

| 表 | 承载 |
| --- | --- |
| `resources` | 3.1 条目全字段（含治理列与 `kind_config`）；`tags`/`capabilities`/`auth_schemes`/`env`/`metadata`/`kind_config` 为 `jsonb` |
| `registries` | 命名空间：`id`、`name`、`description`、`auto_approve` |
| `registry_sources` | 上游注册中心：配置 + `last_sync_at/status/count/error` |
| `tasks` | 3.2 的 Task（`status.message`/`history`/`metadata` 为 `jsonb`） |
| `task_artifacts` | `parts`/`metadata` 为 `jsonb`，按 `id` 保序，随任务级联删除 |
| `task_spans` | span 时间线，同上级联 |
| `task_pending` | `input-required` 的提问与提问时间 |
| `task_push` | `PushNotificationConfig` 快照 |
| `events` | 每个 broker 帧（`(task_id, seq)` 主键），供 `since=<seq>` 回放 |
| `push_records` | 每次回调留痕，按 `AGENTHUB_PUSH_RECORD_LIMIT` 保留最近 N 条/任务 |
| `schema_migrations` | 已执行的迁移文件 |

约束：

- 迁移为 `migrations/*.sql`（`//go:embed` 内嵌），按文件名顺序、每文件单事务执行，SQL 自身幂等。
- 任务状态迁移必须 **条件更新**（`WHERE id=? AND state=?`），0 行时区分「任务不存在」与「并发冲突」。
- 事件与回调写入**不得阻塞任务执行**：有界队列 + 异步写入，队列满或写入失败只告警。
- `jsonb` 载荷通过自实现 `model.JSONField`（`driver.Valuer` / `sql.Scanner`）承载，**不引入**
  `gorm.io/datatypes`。

## 4. 接口

### 4.1 A2A（`POST /a2a`）

| 方法 | params | 结果 |
| --- | --- | --- |
| `message/send` | `MessageSendParams` | `Task`（终态或 `input-required`） |
| `message/stream` | 同上 | SSE 帧序列 |
| `tasks/get` | `TaskQueryParams{id, historyLength?}` | `Task`（可裁剪历史） |
| `tasks/cancel` | `TaskQueryParams{id}` | `Task`（`canceled`） |
| `tasks/resume` | `TaskResumeParams{id, message}` | `Task` |
| `tasks/pushNotification/set` | `{taskId, pushNotificationConfig\|pushNotification}` | `PushNotificationConfig` |
| `tasks/pushNotification/get` | `{id}` | `PushNotificationConfig` |

资源选择优先级：`metadata.resourceId|resource_id|resource`（精确 id → 名称 → kind）→
`metadata.kind` 兜底 → 无选择器时优先 agent，否则按 id 字典序首个。

### 4.2 控制面（`/api/*`，admin token）

```
GET    /api/resources[?kind=&q=&namespace=&status=] → {resources, counts, health}
POST   /api/resources                          → 201 Entry（重复 id 或 (namespace,name,version) → 400）
GET    /api/resources/{id}                     → Entry | 404
PUT    /api/resources/{id}                     → Entry | 404
DELETE /api/resources/{id}                     → 204 | 404
POST   /api/resources/{id}/health              → {health}（运维人工判定）
POST   /api/resources/{id}/probe               → {id, health, probed:true[, reason]}（实测：问资源自己的 connector；
                                                 测不到时记 unhealthy 并回 reason；无可访问目标则 health=unknown）
POST   /api/resources/{id}/status              → Entry（registry:curate；非法迁移 → 400）
GET    /api/registries                         → {registries}（registries:manage）
POST   /api/registries                         → 201 Namespace | 400（非法 slug）
PUT    /api/registries/{id}                    → Namespace | 404（default 不可改 id）
DELETE /api/registries/{id}                    → 204 | 404（default 不可删）
GET    /api/registries/approvals               → {items}（registry:curate，待审批队列）
GET    /api/registry-sources[?namespace=]      → {sources}（sources:manage）
POST   /api/registry-sources                   → 201 RegistrySource | 400
PUT    /api/registry-sources/{id}              → RegistrySource | 404
DELETE /api/registry-sources/{id}              → 204 | 404
POST   /api/registry-sources/{id}/sync         → SyncResult（上游失败 → 502 透传上游 code，
                                                 并发同步 → 409 sync_in_flight）
GET    /api/tasks                              → {tasks}
GET    /api/tasks/{id}                         → Task | 404
GET    /api/tasks/{id}/spans                   → {spans} | 404
GET    /api/tasks/{id}/events[?since=<seq>]    → SSE（since>0 时先回放该 seq 之后的持久化帧，
                                                 无 since 时回放 span/artifact/status，再订阅直播）
POST   /api/tasks/{id}/cancel                  → Task
POST   /api/tasks/{id}/resume                  → Task
GET    /api/export/mcp                         → {servers}
GET    /api/export/agent-card                  → AgentCard
GET    /api/workspace                         → {agents, activity}（resources:read；agentbuddy 聚合视图：可见 agent + 近期任务）
GET    /api/threads                           → {threads}（tasks:read，调用者拥有的线程）
POST   /api/threads                           → 201 Thread（tasks:write；title?、participants?）
GET    /api/threads/{id}                      → {thread, messages} | 404
POST   /api/threads/{id}/messages             → 201 {message, delegated, failed}（tasks:write；@mention → Hub.Send 委派，task id 关联）
GET    /api/threads/{id}/events               → SSE（tasks:read；回访线程历史 + 跟随被委派任务，agent 回复落库）
GET    /a2a/stream?task=<id>                   → SSE（控制台用，无 JSON-RPC 封装）
GET    /healthz                                → 状态与计数（无鉴权）
```

### 4.3 健康检查

`/healthz`：`status`、`version`、`protocolVersion`、`env`、`mode`(mock)、`resources`(按 kind 计数)、
`health`(按健康计数)、`tasks`、`methods`、`legacyMethods`、`pushEnabled`、`auth`(`token`|`open`)、
`database`(`up`|`down`|`unknown`，注入 DB 句柄时存在)；库不可达时返回 503。

## 5. 异常

### 5.1 机器可读 code（HTTP）

`bad_request`(400) / `unauthorized`(401) / `not_found`(404) / `method_not_allowed`(405) /
`payload_too_large`(413) / `internal_error`(500)；领域码由 `writeDomainErr` 映射：
`invalid_kind`·`invalid_health`·`invalid_transport`·`invalid_entry`·`resource_already_exists` → 400，
`resource_not_found`·`task_not_found` → 404，
`invalid_state_transition`·`task_not_resumable`·`task_not_cancelable`·`no_resource_available`·`invalid_flow` → 400，
目录治理码：`invalid_kind_config`·`invalid_status_transition`·`invalid_namespace`·`invalid_source` → 400，
`sync_in_flight` → 409；上游注册中心不可达时 `/api/registry-sources/{id}/sync` 返回 502 并透传上游 code。

### 5.2 JSON-RPC 错误码

标准：`-32700 parse_error`、`-32600 invalid_request`、`-32601 method_not_found`、
`-32602 invalid_params`、`-32603 internal_error`。
自定义：`-32001 task_not_found`、`-32002 task_not_cancelable`、
`-32003 push_notification_not_supported`、`-32004 unsupported_operation`、
`-32005 invalid_state_transition`、`-32006 resource_not_found`、
`-32007 resource_unreachable`、`-32008 task_not_resumable`。
`error.message` **就是 code 字符串**（不含空格与自然语言）。

### 5.3 适配器码

`unknown_kind`、`cli_command_not_allowed`、`adapter_timeout`、`resource_unreachable`。

## 6. 适配器契约

| Kind | mock 行为（确定性） | 产物 |
| --- | --- | --- |
| model | `provider_mode=mock`：provider 注册表 + 回退链，确定性 completion；`openai_compatible`：真实 chat completions（endpoint 指向 OpenAI 兼容网关），按序回退，可选 mock 兜底 | artifact `model.completion`（含命中的 `provider` 名） |
| agent | 步骤规划；prompt 含 `approve`/`confirm`/`approval`（**词级匹配**）→ 挂起 | artifact `agent.plan`，`RequiresInput` |
| mcp | `tools/list` + `tools/call` 协议转换 | artifact `mcp.tools` / `mcp.call` |
| skill | 触发 → 手册 → 步骤 → 报告 | artifact `skill.manual` |
| cli | 白名单 → 超时 → 截断 → JSON 回执 | artifact `cli.receipt`（含 `truncated`） |

所有 mock 必须：同输入同输出（按输入哈希派生）、**调用路径**不触网、不起进程，且产物标注 `provider: mock`
（否则伪造答案与真实答案逐字节无法区分）。

**健康状态不由 mock 决定，只由实测决定**（`mockHealth` 已删除）：

- `Health` 是测量：model 取 `/models`（无法定论时退到 `max_tokens:1` 的 chat completions）、agent 抓
  Agent Card、mcp 发 JSON-RPC `initialize`、skill/custom 取 endpoint、cli 只做 `PATH` 查找（不执行）。
  取不到答案 → `unhealthy`；401/403 或「200 但不是它该返回的东西」→ `degraded`。
- 一次运行同样落健康：成功 `healthy`，`resource_unreachable`/`adapter_timeout` → `unhealthy`，
  `provider_malformed`/`provider_unauthorized` → `degraded`；其余错误（allowlist 拒绝、参数错误）不动，
  因为那不是可达性证据。
- 确实没有可访问目标（无 card 的 agent、stdio 的 mcp、无命令的 cli、无 endpoint 的 skill/custom）
  回答 `unknown`——在此编一个值正是健康状态不再做的事。

Model router 的真实 provider 只在 `provider_mode=openai_compatible` 时启用：`api_key_env` **只存环境变量名**
（本体不入库、不落日志），响应体限长，401/403 → `provider_unauthorized`、畸形响应 →
`provider_malformed`；全部 provider 失败时**绝不伪造 completion**（除非显式开启 `fallback_mock`，
且产物标注 `provider: mock`）。streaming 与真实 agent/mcp/skill/cli 适配器仍属后续项。

## 7. 多语言与视觉约束

### 7.1 i18n

- 语言清单与解析策略与 ariacompute.cn 完全一致：**恰好 13 种**，`matchLang` 处理
  `zh*`→`zh-TW`（tw/hk/hant）否则 `zh`，未命中回落 `en`；`isRTL` 仅 `ar`。
- 优先级：`?lang=` > `localStorage('agenthub.lang')` > `matchLang(navigator.language)` > `en`；
  切换时同步 `<html lang>` 与 `dir`。
- copy 表按语言分块，解析为 `{...copy.en, ...(copy[lang]||{})}`（英文兜底）。
- **零硬编码自然语言**：组件内一律 `t()`；后端枚举（kind/taskState/transport/health/flow/connection）
  由 `copy/enums-copy.js` 映射。
- 不翻译：品牌 `agenthub`、协议术语 `A2A`/`JSON-RPC`/`MCP`/`SSE`/`stdio`/`Agent Card`/`artifact`、
  资源 id、任务 id、时间戳、错误 code。
- **散文与机器数据分家**（见 §9）：文档屏的 HTTP 方法、路径、权限码、字段名与 JSON 示例属于协议数据，
  一律原样渲染；`tests/copy-fallback.spec.js` 断言每个 copy 键在各语言下**不等于英文**，因此这类串
  **不得**进入 copy 表。

### 7.2 单色体系

- 所有 hex 满足 **R=G=B**（中性灰）；`rgb()/rgba()/hsl()` 通道同值或 `0/255` 组合；
  禁止出现色相词（blue/purple/cyan/green/yellow/red/orange/pink…）。
- Token 集中在 `web/src/style.css` 的 `:root`：背景三层、描边三级、文字三级、`--grade-*`
  （五类资产明度分级）、`--accent`（纯白）与银白 `--sheen`。
- 状态编码用**填充/描边/动效**三件套（healthy 实心常亮 / degraded 半填充慢脉冲 2s /
  unhealthy 空心快脉冲 0.9s / unknown 点线静态），**不依赖色相**。
- 五类资产三重冗余区分：灰阶明度 + 专属图标（Cpu/Bot/Plug/BookOpen/Terminal）+ 文字标签。
- 拓扑连线只用线型（实线 / 流动虚线 / 点线）与线宽区分；手写 SVG，不引带调色板的图表库。
- `prefers-contrast: more` 提亮次级文本与描边；`prefers-reduced-motion` 降级为静态。

## 8. 验收标准（M1）

1. `go vet ./...` 无告警；`go test -race ./...` **全绿**（a2a、config、registry、adapter、hub、
   model、store、handlers、httputil 单测 + `tests/e2e_test.go` 真实 HTTP 端到端），
   且**纯逻辑用例零外部依赖**（memory 回退保证无 DB 也全绿）。
2. `bunx vitest run` 全绿：`languages`（恰 13 语 / `matchLang`）、`copy-fallback`
   （英文完整、每语自有译文、未支持语言回落 en）、`enums-copy`（13 语 × 枚举齐全）、
   `api`（错误码透传）、`style`（色彩护栏）。
3. `make build` 产出 `backend/static` 与 `bin/agenthub-server`；`make run` 后
   `/healthz` 与 `/.well-known/agent.json` 可用。
4. A2A 冒烟全通：send（五类各一次）、stream（无 `[DONE]`、终态帧结尾）、
   `input-required` → `tasks/resume` 恢复为 `completed`、cancel、`tasks/get` 历史裁剪、
   v0.1 别名、flow 分支 + 并行、错误码路径（`-32001`/`-32601`/`resource_not_found` 帧）。
5. 控制面冒烟全通：注册资源 → 出现在列表/导出/Agent Card → 立即被 A2A 调用 → 删除。
6. 前端零硬编码自然语言、零有彩色值；13 语切换与 RTL 不破版。
7. **不产出任何评分/评测分数**；pushNotification 只留痕（内存或 `push_records`，可断言），
   不发真实网络请求。
8. 持久化验收：`DATABASE_URL` 缺失时拒绝启动；迁移可重复执行（第二次为 no-op）；
   资源/任务/artifact/span/事件/回调重启后仍可回读，事件可按 `since` 回放；
   **集成用例在未设置 `DATABASE_URL` 时 skip，绝不伪造结果**。

## 8.1 测试与覆盖率约定

- 新代码须补**正常 + 异常**双路径用例，且每个新增机器可读错误码至少有一条断言。
- **双模分支用包内假 store 覆盖**：`registry` 的 `store != nil` 分支由
  `store_mode_test.go` 的内存假 store 驱动，不需要数据库就与内存语义对齐。
- **需要 PostgreSQL 的断言写在被测包内**（如 `internal/store/registry_governance_test.go`），
  以 `DATABASE_URL` 门控；放 `tests/` 只验证行为，不产生该包覆盖率。
- **离线优先**：会触网的用例一律走 `httptest` 或 fixture；同步类源必须 `Fixture: true`。
- 一键度量：`make test-cover`（`coverage.out` 已进 `.gitignore` 与 `make clean`）。

## 8.2 验收标准（M2 — 目录治理与注册中心互连）

1. `go vet ./...` 无告警；`go test -race ./...` 全绿，含 `tests/registry_governance_integration_test.go`
   在**真实 PostgreSQL** 下跑通的治理用例（未设 `DATABASE_URL` 时 skip）。
2. per-kind 校验：六类各自配置可注册并回读；非法配置（如 `mcp` 缺 command+args 与 URL、
   `custom` 非法 JSON）返回 `invalid_kind_config`（400）。
3. 审批：新记录默认 `draft`（`auto_approve` 命名空间除外）→ 不出现在列表/导出/Agent Card/A2A 寻址；
   `draft → approved` 被拒（400），`draft → pending → approved` 后可见。
4. 去重启用键：同 `(namespace, name, version)` 第二次写入报 `resource_already_exists`（400）；
   换 version 可写入；换 namespace 可写入。
5. 同步：fixture 源离线同步导入固定条数并回写 `last_sync_*`；二次同步为 update 而非重复导入；
   上游不可达返回 502 并透传上游 code；并发同步返回 409。
6. Model router：`openai_compatible` 模式用 `httptest` 覆盖命中与回退链顺序；全部 provider 失败时
   不伪造 completion；未设凭据的用例 skip 并写 reason。
7. 前端：`bunx vitest run` 全绿（含 `copy-fallback` / `enums-copy` / `style` 三道护栏），
   `bun run build` 通过；新 copy 组 13 语齐全、非英语译文异于英文、无有彩色值。
8. README 的「AWS Agent Registry 特性对比」章与落地代码一致：三张清单逐项可追溯。
9. 覆盖率：`make test-cover` 可复现；`registry` / `model` / `a2a` / `hub` / `authz` 均在 85% 以上，
   `store` 在 `DATABASE_URL` 未设置时 skip（此时数值不代表真实覆盖）。

## 9. 控制台文档屏（`/docs`）

控制台自带一份说明自身功能与接口的文档，随控制台发版、离线可读，不依赖外部文档站。

### 9.1 入口与可见性

- 侧边栏在「管理」**之后**新增「文档」项，**不加权限门控**：任何已登录账号都可读。
  「管理」自身仍按 `users/roles/grants/backups:manage` 显隐，文档项不得随之消失。
- 路由 `/docs` **不带** `meta.permissions`，守卫只校验会话存在。

### 9.2 覆盖范围

- 功能：六类资源通道、任务状态机（`submitted → working → input-required → completed/failed/canceled`）、
  健康值**只由探测或运行写入**的语义、记录审批状态机。
- 接口：`/a2a` 七方法 + v0.1 别名 + Message/Task 形状；**`/api/*` 全部 66 条路由**，逐条给出方法、
  路径、所需权限、请求体与响应体示例，按协议/鉴权/资源/命名空间/上游源/任务/导出/用户与授权/备份/运维分组。
- 鉴权与错误码：会话 cookie、权限模型与 owner 语义；错误码按 HTTP 状态码分组 + 适配器码。

### 9.3 内容与文案的边界

- `web/src/lib/docs-content.js`：语言中立。字段与 JSON 示例取自后端真实编解码结构，
  随 `internal/handlers/handlers.go` 的路由表同步——**路由搬动，此文件必须同步**。
- `web/src/lib/copy/docs-copy.js`：只放散文，13 语齐全。
- markdown 副本：`docs/docs.md`（英）与 `docs/docs_cn.md`（中）由 `make docs-md`
  从上述两个模块渲染，**不得手工编辑**；改端点或改文案后重跑该目标即重新同步。
- CRUD 行由「动词模板 + 名词」生成（`verbList` + `nounResources`），不为每一行各写一套译文。
- HTTP 原因短语（`Bad Request` 等）属协议数据，原样呈现不翻译。
- 页头显示 `version@commit`，与侧边栏底部署同源，标明文档对应哪个运行实例。

### 9.4 验收

- `docs` 已加入 `tests/copy-fallback.spec.js` 的 `PROSE` 表：13 语键位齐全且非英语译文异于英文。
- 端点清单与 `internal/handlers/handlers.go` 注册一致（增删路由须同步 `docs-content.js`）。
- 完成后必须 `make web-build`：`backend/static` 由 Go 在 3500 端口直接托管，不重建则新屏不可见。
