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
  事件广播（SSE fan-out）→ pushNotification 回调占位（内存记录）。
  编排支持 **flow 三原语 `then / branch / parallel`**，用于多资源接力。
- **可观测**：每任务生成 **span 时间线**（kind、耗时、状态、错误 code），作为「任务追踪」屏数据源；
  首版只埋点，不接外部 exporter。
- **控制面与运维**：`/api/*` 管理接口 + `/healthz`（版本、适配器、资源计数、当前模式、鉴权模式）；
  admin token 鉴权，open 模式仅本地可用。
- **Web 控制台三屏**：注册中心（五类 CRUD + 健康指示 + **单资源「试跑」**）、
  任务追踪（任务列表 + 事件/span 时间线 + artifact 原始内容）、拓扑视图（五列资产 + 调用边）。
- **多语言 13 种**：`en / zh / zh-TW / es / fr / de / it / ru / ja / ko / pt / ar / hi`（`ar` 为 RTL）。
- **纯黑/白/灰单色视觉**：禁止任何有彩色相，靠明度分级 + 线型 + 图形 + 动效编码语义。

### 1.2 范围外（列为后续项）

- 数据库持久化（首版内存 + JSON snapshot；Postgres/pgvector 属后续里程碑）。
- RAG / 向量检索 / 记忆存储（属 `memo` 与 `bankmind` 的职责）。
- 语音、浏览器自动化、渠道接入、多语言 SDK、Agent 网络跨实例联邦。
- LLM-as-judge 评分与评测闭环（eval 仅留钩子，**不产出分数**）。
- 与 mastra agent 的 A2A 互通验证（形态对齐，首版不实现）。
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
| `kind` | `model`/`agent`/`mcp`/`skill`/`cli`（`invalid_kind`） |
| `name` | 必填，去空白（`invalid_entry`） |
| `version` / `repository` / `tags` / `env` / `auth_schemes` | Registry 级元数据 |
| `endpoint` / `transport` | 传输**描述**字段，适配器不得分支（`invalid_transport`） |
| `capabilities` | 能力清单（MCP 为工具名，skill 为步骤工具） |
| `health` | `healthy`/`degraded`/`unhealthy`/`unknown`（`invalid_health`） |
| `metadata` | KV；`cli` 用 `command` 指定白名单命令 |

### 3.2 任务与追踪

- `Task`：`id`、`contextId`、`status`、`artifacts[]`、`history[]`、`metadata`、`resourceId`。
- `Span`：`id`、`taskId`、`resourceId`、`kind`、`label`、`startAt`、`endAt`、`durationMs`、
  `status`（`ok`/`error`/`input-required`/`skipped`）、`code`。
- `Event`（broker 帧）：`seq`、`taskId`、`kind`（`status`/`artifact`/`message`/`span`）、`at`、载荷。

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
GET    /api/resources[?kind=&q=]              → {resources, counts, health}
POST   /api/resources                          → 201 Entry（重复 id → 400）
GET    /api/resources/{id}                     → Entry | 404
PUT    /api/resources/{id}                     → Entry | 404
DELETE /api/resources/{id}                     → 204 | 404
POST   /api/resources/{id}/health              → {health}
GET    /api/tasks                              → {tasks}
GET    /api/tasks/{id}                         → Task | 404
GET    /api/tasks/{id}/spans                   → {spans} | 404
GET    /api/tasks/{id}/events                  → SSE（先回放 span/artifact/status，再订阅直播）
POST   /api/tasks/{id}/cancel                  → Task
POST   /api/tasks/{id}/resume                  → Task
GET    /api/export/mcp                         → {servers}
GET    /api/export/agent-card                  → AgentCard
GET    /a2a/stream?task=<id>                   → SSE（控制台用，无 JSON-RPC 封装）
GET    /healthz                                → 状态与计数（无鉴权）
```

### 4.3 健康检查

`/healthz`：`status`、`version`、`protocolVersion`、`env`、`mode`(mock)、`resources`(按 kind 计数)、
`health`(按健康计数)、`tasks`、`methods`、`legacyMethods`、`pushEnabled`、`auth`(`token`|`open`)。

## 5. 异常

### 5.1 机器可读 code（HTTP）

`bad_request`(400) / `unauthorized`(401) / `not_found`(404) / `method_not_allowed`(405) /
`payload_too_large`(413) / `internal_error`(500)；领域码由 `writeDomainErr` 映射：
`invalid_kind`·`invalid_health`·`invalid_transport`·`invalid_entry`·`resource_already_exists` → 400，
`resource_not_found`·`task_not_found` → 404，
`invalid_state_transition`·`task_not_resumable`·`task_not_cancelable`·`no_resource_available`·`invalid_flow` → 400。

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
| model | provider 注册表 + 回退链，输出 completion | artifact `model.completion` |
| agent | 步骤规划；prompt 含 `approve`/`confirm`/`approval`（**词级匹配**）→ 挂起 | artifact `agent.plan`，`RequiresInput` |
| mcp | `tools/list` + `tools/call` 协议转换 | artifact `mcp.tools` / `mcp.call` |
| skill | 触发 → 手册 → 步骤 → 报告 | artifact `skill.manual` |
| cli | 白名单 → 超时 → 截断 → JSON 回执 | artifact `cli.receipt`（含 `truncated`） |

所有 mock 必须：同输入同输出（按输入哈希派生）、不触网、不起进程、健康检查返回确定性 `mockHealth`。

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
   handlers、httputil 单测 + `tests/e2e_test.go` 真实 HTTP 端到端），且**零外部依赖**。
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
7. **不产出任何评分/评测分数**；pushNotification 仅在内存记录（可断言），不发真实网络请求。
