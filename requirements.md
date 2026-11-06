# requirements.md — agenthub（A2A 互连平台）

> 功能边界 / 协议契约 / 数据模型 / 接口 / 异常 / 多语言与视觉约束 / 验收标准。
> 须经人工逐项审核后，方可据其生成 `task.md` 实施。当前实现对应
> **M1 / M2 / Router / M3（全部已落地）**。
>
> 正文按渐进式披露组织：M1–M2 的能力在这里写全；M3 的三块只写边界与落点，明细在各自子文档——
> 安全加固 `requirements-security.md`（实施 `task-security.md`）、
> 执行沙盒 `requirements-sandbox.md`（实施 `task-sandbox.md`）、
> 数据隔离（RLS）与全 API 审计 `requirements-isolation.md`（实施 `task-isolation.md`）。

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
- **适配器层**：统一 `Connector` 接口 + `For(kind)` 工厂。是否走真实路径由「资源有没有说明去哪儿找」决定，
  没有可用的端点或执行环境时回退**确定性 mock**（同输入必同输出，mock 结果一律带 `provider=mock`，
  便于 `-race` 断言且不至于把编造的答案当成真的）：
  model→OpenAI 兼容端点（`model_openai.go`，非流式；Model Router 形态：单一 `Generate/Stream` +
  provider 注册表 + 回退链）、agent→取 Agent Card 后 `message/send`、mcp→Streamable HTTP JSON-RPC、
  skill→本地手册加载、cli→在租约沙箱内 `exec`（白名单前置校验 → 单次超时 → 输出截断 → 结构化 JSON）。
  speech / custom 仍为 mock，各自保留 `TODO(real)` 边界。
- **Hub 编排与任务机**：入站消息 → 资源选择 → 适配执行 → 状态机 → artifact 累积 →
  事件广播（SSE fan-out）→ pushNotification 回调（落 `push_records`；设置 `AGENTHUB_PUSH_SECRET`
  后改走**真实投递**——https-only、SSRF 白名单、HMAC-SHA256 签名、`(taskId, state)` 一小时内去重，
  失败只告警、不改变终态；未设置时仍是只留痕的 mock）。`AGENTHUB_PUSH_ENABLED=false` 关闭整个能力。
  编排支持 **flow 三原语 `then / branch / parallel`**，用于多资源接力。
- **可观测**：每任务生成 **span 时间线**（kind、耗时、状态、错误 code），作为「任务追踪」屏数据源；
  首版只埋点，不接外部 exporter。
- **PostgreSQL 持久化（GORM）**：`DATABASE_URL` **必填**，缺失即拒绝启动。资源条目、任务主体、
  artifact、span、挂起问题、回调配置、事件流与回调留痕全部入库；未注入 GORM 仓库时
  registry/hub/broker/notifier 退化为**原有内存行为**（公开签名零变更），单测因此零外部依赖。
- **控制面与运维**：`/api/*` 管理接口 + `/healthz`（版本、提交、适配器、资源计数、当前模式、
  鉴权模式、**数据库可达性**）。鉴权为**会话 cookie + RBAC 权限矩阵**：共享 admin token
  （`AGENTHUB_ADMIN_TOKEN`）已不再读取，`AGENTHUB_ENV=prod` 缺 `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL`
  即拒绝启动；另有 API Key（只存 `token_hash`）供脚本与执行节点使用。
- **部署形态**：单二进制 + PostgreSQL；另提供 Kubernetes Operator（`operator/`，CRD
  `agenthub.ai/v1alpha1`）按 CR 渲染 ConfigMap / Secret / Deployment / Service / PVC
  （owner reference 随 CR 回收），详见 `releasenotes.md` §12。
- **Web 控制台十屏**：注册中心（六类 CRUD + per-kind 配置 + 审批状态 + **单资源「试跑」**）、
  命名空间与待审批队列、上游注册中心源、任务追踪（任务列表 + 事件/span 时间线 + artifact 原始内容）、
  拓扑视图（五列资产 + 调用边）、**试跑台**（`/playground`，`tasks:write`）、登录/注册、
  管理后台（用户 / 用户组 / 权限矩阵 / 资源授权 / 数据库检查点 / API Key）、
  **审计**（`/audit`，`audit:read`；全 API 账本 + 哈希链校验）、
  **文档**（`/docs`，控制台自带的功能与接口说明，13 语，无权限门控；详见 §9）。
- **多语言 13 种**：`en / zh / zh-TW / es / fr / de / it / ru / ja / ko / pt / ar / hi`（`ar` 为 RTL）。
- **纯黑/白/灰单色视觉**：禁止任何有彩色相，靠明度分级 + 线型 + 图形 + 动效编码语义。

### 1.2 范围外（列为后续项）

- **pgvector / 向量检索**（属 `memo` 与 `bankmind` 的职责；本仓只需要关系型 PostgreSQL）。
- RAG / 记忆存储（属 `memo` 与 `bankmind` 的职责）。
- 浏览器自动化、多语言 SDK、Agent 网络跨实例联邦。
- LLM-as-judge 评分与评测闭环（eval 仅留钩子，**不产出分数**）。
- MCP **stdio 子进程**传输、agent 与模型**流式**输出（非流式的 MCP Streamable HTTP 与 OpenAI 兼容端点
  已可用，CLI 已是真实 `exec`，见 §1.1；其余统一留 `TODO(real)`）。

### 1.3 范围内（M2，已实现）

在 M1 的资源注册与 A2A 编排之上，新增 **Persona** 与**协作通道**能力，统一由 agentbuddy 桌面客户端消费：

- **Persona（资源画像）**：`Entry` 增加 `persona` 字段（`AvatarURL` + `Bio` + `Capabilities`），由 `normalizePersona` 归一化（截断 `Bio` ≤256 字符、仅允许 http(s) 的 `AvatarURL`、整体为空时归 nil）。落 `resources` 表的 `persona` JSONB 列（迁移 `013_persona.sql`）。
- **Channels（外部消息通道）**：把 Slack / Teams / Telegram / SMS 四类外部消息面绑定到线程。入站经签名校验的公开 webhook（`POST /api/channels/{id}/webhook`，密钥仅存 env var 名）转发进线程；出站经 SSRF 安全的 `Deliver`（`POST /api/channels/{id}/deliver`，复用 `netutil.ValidateURL` + `SafeClient`）投递到 `config.target_url`，回显 Provider 原始响应（≤512B）。落 `channels` 与 `channel_messages` 两表（`014_channels.sql`）。
- **Connectors（策略网关）**：对 `kind=mcp` 资源暴露外部调用，`GET /api/connectors` 列出、`PUT /api/connectors/{id}/policy` 设置 `off / ask_first / allowed` 策略；`ask_first` 经 `Hub.ParkConnector` 挂起一个 `input-required` 任务等待人工批准。`GET/POST /api/connectors/{id}/actions` 触发动作。
- **Speech（语音）**：`POST /api/threads/{id}/audio` 上传语音片段，服务端离线转写（STT mock）后作为 `origin=voice` 的消息委派进线程，并把 agent 回复合成 TTS 音频作为 artifact 附回。受 `AGENTHUB_SPEECH_MODE` / `AGENTHUB_AUDIO_MAX_BYTES` 控制（迁移 `015_thread_origin.sql` 为 `thread_messages` 增加 `origin` 列）。

上述端点复用既有 `resources:read` / `tasks:read` / `tasks:write` 权限，不新增角色或用户组；详见 `releasenotes.md` §11 与 `/docs` 控制台的 Collaboration 分组。

### 1.4 范围内（M2.5：agent 能力 F10–F16，已落地）

参考 agentbuddy `requirements.md` 的 F10–F16 标杆特性分析，agenthub 侧落地「服务端契约」：
computers 已接真实执行租约（§1.6）、schedules 已由 Router 接管（§1.5）；仍未接真实后端的
RAG 检索保持确定性 mock 并标 `TODO(real)`。

- **Computers（F11）**：`GET/POST /api/computers`、`GET/DELETE /api/computers/{id}` 暴露 agent 沙箱
  （browser / shell / screen）；创建时**真实申请执行环境租约**并回写 `provider` / `lease_id` /
  `node_id` / `expires_at`（申请失败回滚该行并返回机器码），删除先终止租约再删行；控制面未接线时
  该行本身即全部功能。新机器码 `computer_not_found`(404) / `feature_unavailable`(503，store 未接入
  时的统一哨兵)。落 `computers` 表（`017_computers.sql`）。
- **Schedules（F13）**：`GET/POST /api/schedules`、`PUT/DELETE /api/schedules/{id}` 暴露目标 / 周期任务；
  调度执行已由 §1.5 的 Router 接管（内存 ticker 的 mock 调度器退场）。
  新机器码 `schedule_not_found`(404) / `schedule_invalid`(400)。落 `schedules` 表（`018_schedules.sql`）。
- **Knowledge（F14）**：`GET /api/knowledge` 暴露 mock RAG 检索（确定性片段），真实向量检索标 `TODO(real)`；
  技能蒸馏仅记录触发点（`skill_distill_disabled`(403)）。落 `knowledge` 表（`019_knowledge.sql`）。
- **Persona（F10）**：`Entry.Persona` 扩展角色画像（`role` / `instruction` / `mbti` / `avatar_ref`，
  与 agentbuddy 表单的字段名一致），经 `POST/PUT /api/resources` 写入，`resources.persona` JSONB 列
  （复用 `013_persona.sql`）。`normalizePersona` 负责 trim、标签 ≤256 字符、`instruction` ≤4096、
  `mbti` 按 **16 型白名单**校验（拼错的代码清空而不是入库，控制台才能画出可解释的徽章）、整体为空归 nil。
- **复用与约束**：上述端点复用既有 `resources:*` / `tasks:*` 权限，不新增角色或用户组；
  迁移均为 `017/018/019`（接 `016_wechat_login.sql` 之后），全 `IF NOT EXISTS`。

### 1.5 范围内（M2.5：调度器 / Router 层，已落地）

参考《Super-agent 设计探索》与 dsh-superagent-prototype 的 router 实现，把 agenthub 从「注册即被调用」
的被动目录升级为 **Router 层 + Worker 层** 双层调度：环境事件先入 Trigger 队列，程序规则快速筛除噪音，
候选再交语义判定，通过的交给 Worker 执行真实任务。F13 的 mock 调度器随之退场。

- **统一入口**：cron 到期、线程消息、渠道 webhook、手动注入四类来源归一化为 `Trigger`，
  按 `(source, dedupe_key)` 幂等；保留来源、发起者、正文与结构化事实。
- **两级过滤**：程序层（来源模式 `off|candidate|request_only` + 发起者白名单 + `@提及` + 关键词 +
  静默时段 + 自回显抑制）→ 语义层（一拍批量一次调用，输出限于 `accept|attach|stop|ignore`；
  模型不可用或回答非法时逐条回退规则结论，绝不放行）。默认 `AGENTHUB_ROUTER_ASSESS_MODE=rule` 离线可跑。
- **订阅直通**：`Ticket ↔ Thread` 多对多订阅；同一 Thread 后续事件直接扇出到已订阅 Worker，
  人类消息优先于判定；支持取消订阅、交回 Router（每条 Trigger 仅一次）、Ticket 结束时未读事件回流判定。
- **节拍批处理**：固定节拍（默认 2s）驱动，同刻单飞、重复请求合并；一拍候选合并为单次判定调用，
  记录批大小与分派耗时。
- **工作单元与执行**：`Ticket` 表达一件要做的事、`Assignment` 表达一次运行；执行复用 `hub.Send` /
  `hub.Resume`，人工介入沿用 `input-required` 挂起与恢复；并发受槽位限制（默认 8），失败按退避重试（默认 3 次）。
- **真实 cron**：保存即按五段表达式算出 `next_run`，到期自动触发并回写 `last_run_at` / `last_status` / `next_run`。
- **默认关闭**：`AGENTHUB_ROUTER_ENABLED=false` 时 `/api/router/*` 与 `/api/schedules/{id}/run` 返回
  `503 feature_unavailable`，线程入口保持原直发 `Hub.Send`。迁移 `020_router.sql`（8 张表，全 `IF NOT EXISTS`）。

详见 §10。

### 1.6 范围内（M3：执行沙盒，已落地）

把「执行」从适配器里的一次性动作升级为**带租约的一等资源**，大脑（Router/Hub）与双手（执行环境）解耦；
结构上与 `internal/router` 同构：消费方定义端口、单写者、节拍单飞、内存 / GORM 双实现。

- **租约**：按 `(owner_id, resource_id, scope)` 复用（`scope` 取 ticket → `contextId` → `default`），
  空闲回收 + 最长存活 + 每归属人 / 全局上限；任务取消或重启**不销毁**租约，租约丢失时下次调用透明重建。
- **类型目录**：内置 `mock` / `docker` / `podman` / `gvisor` / `kata` / `bwrap` / `remote`，声明式预设
  `nsjail` / `isolate` / `systemd-run` / `local` / `nerdctl`，平台层 `e2b` / `microsandbox` / `daytona` /
  `cubesandbox` / `openhands`；默认 `docker`，`AGENTHUB_SANDBOX_PROVIDER=off` 拒绝一切执行。
  声明式条目**逐 argv 展开、绝不拼 shell**，未知占位符或非绝对路径**拒绝启动**。
- **执行节点**：本机可注册为节点（`/api/sandbox-nodes/{id}` 长轮询领取任务、回传结果），凭证复用 API Key。
- **审计**：每次执行只存命令摘要、退出码、耗时、输出 sha256 与截断标记，不存原始输出。
- **computers 收口**：`POST /api/computers` 申请租约、`DELETE /api/computers/{id}` 终止租约（见 §1.4）。
- **不新增权限码**：沿用 `resources:*` / `tasks:*`；未接线 → 503 `feature_unavailable`，后端不可用 →
  409 `sandbox_unsupported`（重试无用）。迁移 `021_sandboxes.sql`（5 张表 + `computers` 增列）。

明细（配置项、API 表、声明式 schema、错误码、验收）见 `requirements-sandbox.md`。

### 1.7 范围内（M3：数据隔离与全 API 审计，已落地）

把「谁能看见哪一行」从应用层约定下沉为数据库承诺，并把「谁在何时调用了什么」从日志文本升级为
可校验的只追加账本。

- **归属**：`tasks` 及全部子表、`knowledge` 增 `owner_id`（`''` = 系统产生，仅系统身份 / 管理员可见）；
  子表冗余归属列以避免 JOIN；**不做存量回填**。
- **RLS**：八张表 `ENABLE` + `FORCE ROW LEVEL SECURITY`，策略
  `system || bypass || (owner_id <> '' AND owner_id = app.owner_id)`；身份经 `internal/identity` 进
  `context`，由 `store.scoped` 在**短事务**内 `set_config` 注入——策略未设置即拒绝（fail-closed）。
  `AGENTHUB_RLS=enforce`（默认）/ `off`（追加 `app.system=on`；`env=prod` 需 `AGENTHUB_RLS_ACK=1`）。
  角色分离：`agenthub_migrator`（DDL）/ `agenthub_app`（DML、`NOBYPASSRLS`、非表属主）/ `agenthub_readonly`。
- **资源可见性**：`resources.visibility`（`public` / `restricted`）——**破坏性变更**，未显式 `public`
  且无 grant 的记录从所有人列表消失，调用返回 404（不是 403，避免 ID 枚举）。
- **审计**：`audit_log` 对齐 CloudTrail（`event_id` / `action` / `actor_*` / `ip` / `path` 脱敏 /
  `status` / `error_code` / `latency_ms` / `prev_hash` + `hash`），应用只 `INSERT`、触发器拒绝
  `UPDATE/DELETE/TRUNCATE`（保留期清理例外），写入走有界队列 + 批量，绝不阻塞业务；
  `GET /api/audit/verify` 重算哈希链。新增权限码 `audit:read`。`AGENTHUB_AUDIT_RETENTION` 默认 90 天。
- **`/a2a` 授权**：`message/send|stream` 必须解析出具体资源；`tasks/*` 必须已认证且归属调用者
  （或管理员），否则按「不存在」返回；匿名只能触达 `public` 资源。

明细（升级指引、策略 SQL、表结构）见 `requirements-isolation.md`；传输 / 认证 / SSRF / 密钥等
既有加固见 `requirements-security.md`。

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
| 授权 | 匿名只可触达 `visibility=public` 的资源；`tasks/*` 需已认证且任务归属调用者或管理员
  （否则按不存在返回）；匿名调用需认证的方法 → `-32009 unauthenticated`（见 §1.7） |

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

- `Task`：`id`、`contextId`、`status`、`artifacts[]`、`history[]`、`metadata`、`resourceId`，
  以及归属 `owner_id`（`''` 表示系统产生；越权访问一律按不存在返回，见 §1.7）。
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
| `computers` | agent 沙箱（browser/shell/screen）：状态 + 最近动作回显（迁移 `017`） |
| `schedules` | 目标/周期任务：cron_expr / enabled / next_run / last_run（迁移 `018`） |
| `knowledge` | mock RAG 片段：agent_id / chunk_text / source_ref / `owner_id`（`019`/`023`） |
| `users` / `sessions` / `oauth_accounts` | 账号、会话、OAuth 绑定（`005` / `016`） |
| `groups` / `roles` / `permissions` 及 `user_groups` / `user_roles` / `group_roles` / `role_permissions` | RBAC 解析（`006`） |
| `resource_grants` | 资源级授权（`006`） |
| `api_keys` / `api_key_scopes` | 程序化凭证，只存 `token_hash`（`010`） |
| `backup_checkpoints` / `backup_schedules` | 检查点清单与定期打点策略（`007`） |
| `threads` / `thread_messages` | 协作线程与消息（`012` / `015`） |
| `channels` / `channel_messages` | 外部消息通道绑定与入站/出站留痕（`014`） |
| `sandbox_leases` / `sandbox_execs` / `sandbox_nodes` / `sandbox_jobs` / `sandbox_events` | 执行租约、执行摘要、节点、作业与事件（`021`） |
| `audit_log` | 只追加审计账本（哈希链；`025`，`audit:read` 授权见 `027`）；**不进检查点、不被恢复改写** |
| `schema_migrations` | 已执行的迁移文件 |

检查点范围（与故障恢复的边界）：`backup_checkpoints` / `backup_schedules` / `audit_log` 三张表**既不进归档也不被恢复改写**。
前两张是不可回滚的检查点清单；`audit_log` 则因 `025` 的触发器对 `TRUNCATE` / `DELETE` / `UPDATE`
一律拒绝（含 owner 与 superuser），把它写回等于回滚审计证据——因此归档既不带它、恢复也不清空它，
哈希链在回滚前后保持连续。内建引擎（`gonative`）另有**类级预检**：任何将被 dump 的表若带
`BEFORE TRUNCATE` 触发器，dump 与 restore 都以 `backup_table_guarded`（409）按表名拒绝，
并提示改用 `pg_dump` 引擎（后者按表名白名单构建归档，能安全应对受触发器保护的表）。

共 47 张表（含关联表）；归属列、`resources.visibility` 与 RLS 策略见 §1.7。

约束：

- 迁移为 `migrations/*.sql`（`//go:embed` 内嵌），按文件名顺序、每文件单事务执行，SQL 自身幂等。
  当前已到 `027_audit_permission.sql`，新增只加新文件，并同步 `store_test.go` 的 `want` 清单。
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
`metadata.kind` 兜底 → 无选择器时优先 agent，否则按 id 字典序首个；解析不到 → `resource_not_found`。

任务维度方法共用 `ResolveTaskID()`：`tasks/pushNotification/set` 用 `taskId`、`get` 用 `id` 的历史
不一致按向后兼容处理（两字段都接受，同时出现以规范字段为准，都缺 → `-32602`）。

### 4.2 控制面（`/api/*`，会话 + RBAC）

鉴权：`/api/auth/*` 可匿名（否则没人拿得到会话），其余一律 `requireAuth` + 具体权限码；
路由与权限逐条登记在 `internal/handlers/handlers.go`，并须同步 `handlers/listen.go` 的
`Listeners()` 与 `listen_test.go` 的 `probes`。下列为 M1–M2 的逐条清单，M2.5 起按组给出索引（§4.2.1）。

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
GET    /api/computers                         → {computers}（resources:read；F11 agent 沙箱列表）
POST   /api/computers                         → 201 Computer（resources:write；kind∈browser/shell/screen）
GET    /api/computers/{id}                    → Computer | 404（resources:read；含最近动作回显）
GET    /api/schedules                         → {schedules}（tasks:read；F13 目标/周期任务）
POST   /api/schedules                         → 201 Schedule（tasks:write；goal/cron_expr/enabled）
GET    /api/schedules/{id}                    → Schedule | 404（tasks:read）
PUT    /api/schedules/{id}                    → Schedule | 404（tasks:write；启停/编辑）
DELETE /api/schedules/{id}                    → 204 | 404（tasks:write）
GET    /api/knowledge[?agentId=]              → {chunks}（resources:read；F14 mock RAG 检索）
POST   /api/schedules/{id}/run                → 202 Trigger | 503（tasks:write；立即触发一次，需开调度器）
GET    /api/router/stats                      → Stats（tasks:read；队列水位/节拍/订阅数）
GET    /api/router/triggers                   → {triggers}（tasks:read；?source=&disposition=&thread=&ticket=&limit=）
POST   /api/router/triggers                   → 202 Trigger | 200 {duplicate:true}（tasks:write；手动注入）
GET    /api/router/triggers/{id}              → Trigger | 404 router_trigger_not_found（tasks:read）
GET    /api/router/tickets                    → {tickets}（tasks:read；?state=&open=true&limit=）
GET    /api/router/tickets/{id}               → TicketDetail | 404（tasks:read；订阅/运行/判定/原始触发）
POST   /api/router/tickets/{id}/cancel        → TicketDetail | 404 | 409 router_ticket_closed（tasks:write）
POST   /api/router/tickets/{id}/handback      → {requeued:true} | 409 router_handback_exhausted（tasks:write）
DELETE /api/router/threads/{id}/subscription  → 204 | 404 router_thread_not_found（tasks:write）
GET    /api/router/policies                   → {policies}（tasks:read；各类来源的程序层规则）
PUT    /api/router/policies/{source}          → Policy | 400 router_policy_invalid（tasks:write）
POST   /api/router/passes                     → 202 {kicked:true}（tasks:write；立即推进一拍）
GET    /api/router/events?since=<seq>         → SSE（tasks:read；事件名 router_event，先回放再实时）
```

#### 4.2.1 路由分组索引（当前 136 条 `/api` 路由）

不逐条罗列，避免与 `handlers.go` 反复漂移；每条路径的请求/响应示例由控制台 `/docs` 屏给出
（`web/src/lib/docs-content.js`），路由搬动时两处必须同步。

| 分组 | 条数 | 路径 | 权限 |
| --- | --- | --- | --- |
| 身份 | 9 | `/api/auth/config`、`register`、`login`、`logout`、`me`、`sessions`、`sessions/{id}`、`oauth/{provider}`、`oauth/{provider}/callback` | 前三条匿名；其余 `requireAuth` |
| 审计 | 3 | `/api/audit`、`/api/audit/verify`、`/api/audit/{id}` | `audit:read` |
| 资源 | 8 | `/api/resources`（GET/POST）、`{id}`（GET/PUT/DELETE）、`{id}/health`、`{id}/probe`、`{id}/status` | `resources:read` / `resources:write`；status 为 `registry:curate` |
| 命名空间 | 5 | `/api/registries`（GET/POST）、`{id}`（PUT/DELETE）、`/api/registries/approvals` | `registries:manage`；approvals 为 `registry:curate` |
| 上游源 | 5 | `/api/registry-sources`（GET/POST）、`{id}`（PUT/DELETE）、`{id}/sync` | `sources:manage` |
| 任务 | 6 | `/api/tasks`、`{id}`、`{id}/spans`、`{id}/events`、`{id}/cancel`、`{id}/resume` | `tasks:read` / `tasks:write` |
| 导出 | 2 | `/api/export/mcp`、`/api/export/agent-card` | `resources:read` |
| 工作台与线程 | 7 | `/api/workspace`、`/api/threads`（GET/POST）、`{id}`、`{id}/messages`、`{id}/events`、`{id}/audio` | `resources:read` / `tasks:read` / `tasks:write` |
| 通道 | 9 | `/api/channels`（GET/POST）、`{id}`（GET/PUT/DELETE）、`{id}/deliveries`、`{id}/deliveries/{did}/retry`、`{id}/webhook`（**公开，签名校验**）、`{id}/deliver` | `tasks:read` / `tasks:write`；webhook 无会话 |
| 连接器 | 7 | `/api/connectors`、`{id}/policy`（PUT）、`{id}/actions`（GET/POST）、`approvals`（GET）、`approvals/{id}/approve|reject`（POST） | `resources:read` / `resources:write`；审批队列用 `tasks:read` / `tasks:write` |
| 计算机 | 4 | `/api/computers`（GET/POST）、`{id}`（GET/DELETE） | `resources:read` / `resources:write` |
| 目标与周期 | 6 | `/api/schedules`（GET/POST）、`{id}`（GET/PUT/DELETE）、`{id}/run` | `tasks:read` / `tasks:write` |
| 调度器 | 13 | `/api/router/*`（stats / triggers / tickets / policies / passes / events） | `tasks:read` / `tasks:write` |
| 沙盒 | 7 | `/api/sandboxes`、`stats`、`events`、`{id}`、`{id}/exec`、`{id}/renew`、`{id}`（DELETE） | `resources:read` / `resources:write` |
| 类型目录 | 2 | `/api/sandbox-providers`、`/api/sandbox-providers/refresh` | `resources:read` / `resources:write` |
| 执行节点 | 6 | `/api/sandbox-nodes`（GET/POST）、`{id}`（DELETE）、`{id}/heartbeat`、`{id}/jobs`（长轮询）、`{id}/jobs/{job}` | `resources:read` / `resources:write` / `tasks:write` |
| 知识 | 1 | `/api/knowledge` | `resources:read` |
| 用户与授权 | 18 | `/api/users`（5）、`{id}/sessions`、`/api/permissions`、`/api/roles`（4）、`/api/groups`（4）、`/api/grants`（3） | `users:manage` / `roles:manage` / `sessions:manage` / `grants:manage` |
| 备份 | 8 | `/api/backups`（GET/POST）、`schedule`（GET/PUT）、`{id}`（GET/DELETE）、`{id}/download`、`{id}/restore` | `backups:manage` |
| API Key | 5 | `/api/api-keys`（GET/POST）、`{id}`（GET/PUT/DELETE） | `requireAuth`（各自持有者的 key） |
| 共享文件 | 5 | `/api/files`（GET/POST）、`{id}`（GET/DELETE）、`{id}/content` | `tasks:read` / `tasks:write`；下载支持 `Range` |

上表分组条数合计 **136** 条 `/api` 路由，与 `internal/handlers/handlers.go` 一致；
另有 5 条协议面路由（`POST /a2a`、`GET /a2a/stream`、`GET /a2a/agent-card`、
`GET /.well-known/agent.json`、`GET /healthz`），合计 **141** 条 `mux.HandleFunc`。
`/docs` 屏与 `docs/*.md` 的分组口径略有不同（把 `/api/schedules/{id}/run` 归入调度器组，
故该组为 14 条），但端点集合一致。

分组内不新增权限码：沙盒复用 `resources:*` / `tasks:*`，审计新增唯一的 `audit:read`。

### 4.3 健康检查

`/healthz`：`status`、`version`、`commit`、`buildNote`、`protocolVersion`、`env`、`mode`(mock)、
`resources`(按 kind 计数)、`health`(按健康计数)、`tasks`、`methods`、`legacyMethods`、`pushEnabled`、
`auth`(`session`|`anonymous`|`disabled`)、`database`(`up`|`down`|`unknown`，注入 DB 句柄时存在)；
库不可达时返回 503。

## 5. 异常

### 5.1 机器可读 code（HTTP）

`bad_request`(400) / `unauthorized`(401) / `not_found`(404) / `method_not_allowed`(405) /
`payload_too_large`(413) / `internal_error`(500)；领域码由 `writeDomainErr` 映射：
`invalid_kind`·`invalid_health`·`invalid_transport`·`invalid_entry`·`resource_already_exists` → 400，
`resource_not_found`·`task_not_found` → 404，
`invalid_state_transition`·`task_not_resumable`·`task_not_cancelable`·`no_resource_available`·`invalid_flow` → 400，
目录治理码：`invalid_kind_config`·`invalid_status_transition`·`invalid_namespace`·`invalid_source` → 400，
`sync_in_flight` → 409；上游注册中心不可达时 `/api/registry-sources/{id}/sync` 返回 502 并透传上游 code。
agent 能力码（F10–F16）：`computer_not_found`·`schedule_not_found` → 404、`skill_distill_disabled` → 403、
`schedule_invalid` → 400、`feature_unavailable` → 503（computers/schedules/knowledge 三端点 store 未接入时的统一哨兵，取代原 `computer_disabled`/`knowledge_unavailable`；状态映射见 `writeDomainErr`）。
调度器码（`/api/router/*`，映射见 `writeRouterErr`）：`router_ticket_not_found`·`router_trigger_not_found`·
`router_thread_not_found` → 404、`router_policy_invalid` → 400、`router_ticket_closed`·
`router_handback_exhausted`·`router_resource_unset` → 409；未注入 `Deps.Router` 时全部 503 `feature_unavailable`。

身份与授权码：`unauthenticated`·`session_expired`·`invalid_credentials` → 401，`account_locked` → 429
（5 次失败锁 15 分钟，未知账号同一错误以防枚举）；`forbidden`·`user_disabled`·`builtin_role` → 403；
`email_taken`·`role_already_exists`·`group_already_exists` → 409；
`weak_password`·`invalid_email`·`password_required`·`invalid_grant`·`unknown_permission` → 400；
`oauth_not_configured`·`oauth_state_mismatch`·`oauth_failed`·`oauth_no_email` → 502。
API Key 码：`api_key_not_found` → 404、`api_key_expired`·`api_key_revoked` → 401、
`api_key_name_required`·`api_key_invalid_scope` → 400。

通道码：`channel_not_found` → 404、`channel_disabled` → 403（控制面未接线时 503）、`channel_payload_invalid` → 400、
`channel_signature_missing`·`channel_signature_invalid`·`channel_timestamp_expired` → 401、`delivery_failed` → 502、
`delivery_not_found` → 404（回执不存在）、`delivery_not_retryable` → 409（已送达 / 入站 / 无原文）。
连接器码：`connector_not_found` → 404、`connector_action_denied` → 400、`connector_policy_off` → 403。
语音码：`audio_too_large` → 413、`audio_unsupported` → 400/415、`audio_transcribe_failed` → 422、
`speech_disabled` → 503。

沙盒码：`sandbox_disabled`·`sandbox_policy_denied` → 403、`sandbox_unsupported`·`sandbox_lease_lost` → 409、
`sandbox_unknown_provider` → 400、`sandbox_not_found` → 404、`sandbox_limit_reached` → 429、
`sandbox_node_offline` → 503、`sandbox_timeout` → 504；未注入 `Deps.Sandboxes` 时 503 `feature_unavailable`。

备份码：`backup_not_found` → 404；`backups_disabled`·`backup_in_progress`·`backup_checksum_mismatch`·
`backup_schema_mismatch`·`backup_invalid_archive`·`backup_table_guarded` → 409；`backup_schedule_invalid`·`restore_confirm_required` → 400；
`backup_engine_unavailable`·`restore_busy`·`restore_in_progress` → 503；
`backup_engine_failed`·`backup_failed`·`backup_interrupted`·`restore_failed` → 500。

可见性码：`invalid_visibility` → 400；越权资源统一 404（不泄露存在性，见 §1.7）。

限流：`429` + `Retry-After`——认证端点 10 req/min/IP，`/api` 与 `/a2a` 合计 200 req/min/IP，
loopback 客户端豁免；静态资源、`/healthz`、发现路径不限。

### 5.2 JSON-RPC 错误码

标准：`-32700 parse_error`、`-32600 invalid_request`、`-32601 method_not_found`、
`-32602 invalid_params`、`-32603 internal_error`。
自定义：`-32001 task_not_found`、`-32002 task_not_cancelable`、
`-32003 push_notification_not_supported`、`-32004 unsupported_operation`、
`-32005 invalid_state_transition`、`-32006 resource_not_found`、
`-32007 resource_unreachable`、`-32008 task_not_resumable`、
`-32009 unauthenticated`（仅用于「匿名调用需要认证的方法」）。
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

## 8. 验收标准（M1 — 核心契约）

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
7. **不产出任何评分/评测分数**；pushNotification 每次回调都落 `push_records` 可断言。
   真实投递在设置 `AGENTHUB_PUSH_SECRET` 后启用（https-only + SSRF 白名单 + HMAC 签名），
   未设置时保持只留痕；`AGENTHUB_PUSH_ENABLED=false` 关闭整个能力。
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

## 8.3 验收标准（M3 — 安全加固 / 沙盒 / 隔离与审计）

M3 各块的逐项验收写在对应子文档（`requirements-security.md` §验收、`requirements-sandbox.md` §7、
`requirements-isolation.md` §7），主文档只钉住不可退让的三条：

1. **安全加固**：安全响应头、per-IP 限流（429 + `Retry-After`）、CSRF 同源校验、账号锁定、
   SSRF 安全出站、签名 push、密钥加密全部有单测；`AGENTHUB_ENV=prod` 缺
   `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL`、或 `AGENTHUB_ENCRYPTION_KEY` < 32 字节时**拒绝启动**。
2. **沙盒**：租约复用 / 上限 / 回收有单测；声明式条目加载期校验（未知占位符、非绝对路径）拒绝启动；
   `AGENTHUB_SANDBOX_PROVIDER=off` 时执行被拒；未接线端点返回 503 `feature_unavailable`，
   后端缺失返回 409 `sandbox_unsupported`——两者语义不得混用。
3. **隔离与审计**：`AGENTHUB_RLS=enforce` 下越权读写返回「不存在」；`resources.visibility`
   未标记 `public` 且无 grant 的记录不可见（升级为破坏性变更）；`audit_log` 只追加、哈希链
   `GET /api/audit/verify` 可复算、审计写失败不阻塞业务；迁移 `021`–`027` 幂等并已登记
   `store_test.go` 的 `want` 清单。

## 9. 控制台文档屏（`/docs`）

控制台自带一份说明自身功能与接口的文档，随控制台发版、离线可读，不依赖外部文档站。

### 9.1 入口与可见性

- 侧边栏在「管理」**之后**新增「文档」项，**不加权限门控**：任何已登录账号都可读。
  「管理」自身仍按 `users/roles/grants/backups:manage` 显隐，文档项不得随之消失。
- 路由 `/docs` **不带** `meta.permissions`，守卫只校验会话存在。

### 9.2 覆盖范围

- 功能：六类资源通道、任务状态机（`submitted → working → input-required → completed/failed/canceled`）、
  健康值**只由探测或运行写入**的语义、记录审批状态机。
- 接口：`/a2a` 七方法 + v0.1 别名 + Message/Task 形状；**`/api/*` 全部 136 条路由**，逐条给出方法、
  路径、所需权限、请求体与响应体示例；无权限码的端点按 `public`（匿名）/ `session`（仅需登录）标注，
  按 §4.2.1 的分组组织（协议/鉴权/资源/命名空间/上游源/任务/导出/工作台与线程/通道/连接器/计算机/
  目标与周期/调度器/沙盒与节点/共享文件/知识/用户与授权/备份/API Key/审计）。
- 服务地址：HTTP 章开头给出默认 base URL 与端口，数值取自 `config.go` 的
  `AGENTHUB_HTTP_PORT` 与 compose 的 `POSTGRES_PORT`；语言中立的数值放
  `web/src/lib/docs-content.js` 的 `serverBase`，散文 13 语放 `docs-copy.js`，由
  `docs-content.spec.js` 与两处源头双向钉住。
- 沙盒：隔离层概念、类型目录与执行节点协议（与 `requirements-sandbox.md` 同源）。
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
  **已知差距（待补）**：`docs-content.js` 目前 116 条，缺 `DELETE /api/computers/{id}`、
  `POST /api/channels/{id}/deliveries/{did}/retry` 与 15 条沙盒/类型目录/执行节点路由
  （含 13 语 `descKey`）。
- 完成后必须 `make web-build`：`backend/static` 由 Go 在 3500 端口直接托管，不重建则新屏不可见。

## 10. 调度器（Router 层）

### 10.1 三层纪律

1. **单一写者**：Trigger / Thread / Subscription / Ticket / Assignment / Decision 只由 `router.Engine` 修改，
   所有变更方法串行化（内存实现用互斥锁，持久实现用单事务）。
2. **状态在事务内、外部动作在提交后**：`Engine` 只写库并返回「待执行意图」，真正的 `hub.Send` /
   `hub.Resume` 由 Passes 在提交后驱动——写库失败不会留下已派出去的幽灵任务。
3. **命令与事实分离**：外部入口是祈使命令（可被拒绝，返回机器码），执行器回报是用完成态事实。

Router 层只做过滤、分派与全局记忆，不执行具体任务；Worker 层（`HubExecutor`）独占执行，
结果只回写 Ticket，不直接对用户发声。

### 10.2 数据模型（`020_router.sql`）

| 表 | 内容 |
| --- | --- |
| `router_triggers` | 触发：`source`(cron\|thread\|channel\|manual) / `source_id` / `thread_id` / `kind` / `body` / `explicit` / `meta` / `dedupe_key` / `disposition` / `next_due`；`(source, dedupe_key)` 唯一 |
| `router_threads` | 会话登记：`thread_id` / 标题 / 状态 / `last_event_at` |
| `router_subscriptions` | `(thread_id, ticket_id)` 多对多订阅，按 `thread_id` 建索引供扇出 |
| `router_tickets` | 工作单元：`goal` / `state`(queued\|running\|waiting\|done\|failed\|canceled) / `resource` / `origin_trigger` / `follows_done` |
| `router_assignments` | 每次运行：`ticket_id` / `attempt` / `task_id` / `state` / `error_code` / 起止时间 |
| `router_decisions` | 判定记录：`pass` / `batch_size` / `assessor`(rule\|model) / `verdict` / `trigger_id` |
| `router_policies` | 程序层规则：按 `source` 的 `mode` / `allow` / `keywords` / `quiet_start` / `quiet_end` / `resource` |
| `router_events` | 领域事件（`seq` / `kind` / `subject` / `payload`），供 `?since=<seq>` 回放 |

保留策略：`AGENTHUB_ROUTER_TRIGGER_TTL`（默认 168h）到期后由独立 ticker 分批（每次 ≤5000 行）清理
触发/判定/事件；Ticket 与 Assignment 不清理（它们是「做过什么」的账）。

### 10.3 行为契约

- **去重**：同 `(source, dedupe_key)` 再次投递返回 `200 {"duplicate":true}`，不新建 Trigger。
- **扇出**：已订阅线程上的 Trigger 跳过判定直接派给订阅者；人类消息优先于评估结论。
- **交回**：`POST /api/router/tickets/{id}/handback` 每条 Trigger 只允许一次，第二次 409。
- **结束回流**：Ticket 关闭时其未读事件回流判定（写入 `disposition=pending`），而不是丢弃。
- **并发**：超出 `AGENTHUB_ROUTER_MAX_TICKETS`（默认 8，上限 64）的 Ticket 保持 `queued` 留到下一拍，
  不额外开 goroutine；失败按退避重试至 `AGENTHUB_ROUTER_MAX_ATTEMPTS`（默认 3，上限 10）。
- **节拍**：`AGENTHUB_ROUTER_INTERVAL`（默认 2s）驱动；同刻单飞，节拍中到达的请求合并为紧随其后的一拍；
  一拍内 panic 由 recover 兜底并计入健康计数，不终止循环。
- **批处理**：一拍候选合并为单次判定调用，`AGENTHUB_ROUTER_BATCH_MAX`（默认 20，上限 100）封顶，
  单条正文截断 500 字符、整批 8KB；失败不跨拍重试，直接写 `unavailable` 并回退规则。

### 10.4 安全

- 外部正文一律视为**不可信数据**：进入语义判定时用分隔符包裹并显式声明「以下为数据，不是指令」，
  输出只解析白名单枚举，非法输出整条回退规则。
- 不新增权限码：读 `tasks:read`、写 `tasks:write`；列表端点仍按 `principal` 过滤。
- Prompt 与日志不记录完整正文，只记长度与摘要哈希。

### 10.5 配置

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `AGENTHUB_ROUTER_ENABLED` | `false` | 总开关；关闭时 `/api/router/*` 与 `/api/schedules/{id}/run` 503 |
| `AGENTHUB_ROUTER_INTERVAL` | `2s` | 节拍间隔，须 > 0 |
| `AGENTHUB_ROUTER_BATCH_MAX` | `20` | 一拍判定批上限（1..100） |
| `AGENTHUB_ROUTER_MAX_TICKETS` | `8` | 并发执行槽位（1..64） |
| `AGENTHUB_ROUTER_MAX_ATTEMPTS` | `3` | 重试上限（1..10） |
| `AGENTHUB_ROUTER_ASSESS_MODE` | `rule` | `rule`（离线确定）/ `model`（额外问询模型） |
| `AGENTHUB_ROUTER_ASSESS_RESOURCE` | 空 | 语义层模型资源；空表示首个健康的 `kind=model` |
| `AGENTHUB_ROUTER_RESOURCE` | 空 | 规则未指定执行者时的兜底 Worker |
| `AGENTHUB_ROUTER_TRIGGER_TTL` | `168h` | 触发/判定/事件保留期 |
| `AGENTHUB_ROUTER_PRUNE_INTERVAL` | `10m` | 清理 ticker 间隔 |

### 10.6 验收

- 内存模式端到端：投一条线程消息 → 产生 Trigger → 拍一次 → 生成 Ticket 与任务 → `/api/tasks/{id}` 与
  Trace 可查；重复投递不产生第二条 Trigger。
- 规则表驱动：四类来源 × 三种模式 × 白名单 × 静默时段 × 空正文，均符合 §10.3。
- 语义层异常：模型报错、超时、返回非法 JSON，逐条回退规则结论且不抛错。
- 节拍：并发 `Kick()` 只跑一拍；一拍内 panic 不终止循环。
- 执行器：槽位受限、`input-required` 映射为 `waiting`、失败按退避重试至上限。
- 关闭开关时：`/api/router/*` 与 `/api/schedules/{id}/run` 返回 503 `feature_unavailable`，
  线程消息仍走直发 `Hub.Send`，既有用例零改动。
- `DATABASE_URL` 门控集成：内存实现与 GORM 实现在同一批语义断言下一致。

## 11. 共享文件与连接器审批队列（本轮）

### 11.1 共享文件 `/api/files`

| 方法与路径 | 权限 | 说明 |
| --- | --- | --- |
| `GET /api/files` | `tasks:read` | `{ files: File[], maxBytes, backend }`，只返回调用者的行 |
| `POST /api/files` | `tasks:write` | `multipart/form-data`，取**第一个文件分片**；表单字段一律跳过 |
| `GET /api/files/{id}` | `tasks:read` | 元数据；不存在或不属于调用者 → `404 file_not_found` |
| `GET /api/files/{id}/content` | `tasks:read` | `http.ServeContent`：`Content-Disposition: attachment`、支持 `Range`、`X-Content-Type-Options: nosniff` |
| `DELETE /api/files/{id}` | `tasks:write` | `204`；先删行再删字节，删两遍不报错 |

- `File` 字段：`id` / `name` / `mime` / `size` / `sha256` / `backend` / `createdAt`。
- 上限 `AGENTHUB_FILES_MAX_BYTES`（默认 32 MiB）在**写临时文件时**判定，超限 `413 file_too_large`；
  空文件 `415 file_unsupported`；后端读写失败 `502 file_store_error`；功能未配置 `503 files_disabled`。
- 文件名只保留基名（`../../etc/passwd` → `passwd`），空名回落 `upload.bin`；MIME 以嗅探为准，
  嗅探拿不准时才用客户端声明的类型。
- 行带 `owner_id` 并受行级隔离；仓储走 `scoped(ctx)`，列表按 owner 精确匹配。

### 11.2 连接器审批队列

- `GET /api/connectors/approvals`（`tasks:read`）→ `{ taskId, resourceId, connectorId, connectorName,
  action, args, requestedBy, asked, createdAt }[]`，来源是 `input-required` 且带
  `metadata.connectorApproval` 的任务：**不新增审批表**。
- `POST /api/connectors/approvals/{id}/approve`（`tasks:write`）：`hub.Resume` 真跑一次，
  动作与参数随 resume 回填，返回完成后的任务。
- `POST /api/connectors/approvals/{id}/reject`（`tasks:write`）：`hub.Cancel`，不执行任何动作。
- 不存在、非待审批、已决定、别人的任务统一 `404 connector_approval_not_found`；
  已决定的再次处理返回 `409 connector_approval_closed`。

### 11.3 验收

- 队列读得到动作与参数；批准后任务 `completed` 且动作只执行一次，队列变空，重复批准 409。
- 拒绝后任务 `canceled`，队列变空，再批准 409。
- 跨用户：别人的待审批任务按 404 返回，列表里不出现。
- 文件：上传→列表→元数据→下载（含 `Range` 206）→删除全链路通；超限 413、缺文件 404、
  功能关闭 503、后端故障 502 都有用例；`content` 下载带 `nosniff` 与 attachment。
- 能力准入：申请后端未声明的网络模式 → `409 sandbox_capability_unavailable`，后端零调用。

## 12. 本地运行链路修复（M3 网络隔离回归，2026-11-05）

### 12.1 边界

- 只修 `make run` / `make db-up` / `make up` 的本地启动链路：compose 网络拓扑、宿主可达性
  校验、两套 DSN 与端口表口径。不改 A2A / `/api` 契约、权限模型、schema 与业务代码。
- `data` 保持 `internal: true`，控制台侧与数据库的网络隔离不得放宽。

### 12.2 行为契约

- postgres 同时加入 `data` 与 `hostpg`；`hostpg` 只有 postgres 一个成员且关闭 masquerade，
  唯一用途是让 Docker 发布宿主端口（只连 internal 网络的容器会记下 `PortBindings`，
  但 `NetworkSettings.Ports` 仍为 null）。
- 宿主端口与容器内端口统一由 `POSTGRES_PORT`（默认 5434）决定，两端一起移动；compose 的
  默认容器内 DSN、`scripts/compose-ports.sh` 的容器端口常量、README 端口表都跟随它。
- `.env` / `.env.example` 的 `DATABASE_URL` 是**容器内**地址 `postgres:5434`（compose 会把它
  注入 agenthub 容器）；`make run` 内联覆盖成 `localhost:5434`，两者互不泄漏。
- `make db-up` 的 ready 判定 = 容器内 `pg_isready` 通过 **且** 宿主
  `127.0.0.1:POSTGRES_PORT` 的 TCP 可连。

### 12.3 异常

- 端口未发布或数据库未起：30s 后 `make db-up` 失败，打印
  `container pg_isready=<0|1>, host port=<0|1>` 与 `docker compose ps postgres`；
  不允许把容器健康当成宿主可达，也不允许把错误拖到 `go run` 才暴露。
- `POSTGRES_PORT` 被覆盖时，宿主 DSN、容器内监听、健康探测、端口表四处必须一致；
  不一致由 `docker compose config` 与 `make db-up` 暴露，不静默降级。

### 12.4 验收标准

- `docker compose port postgres 5434` 返回真实宿主端口，`NetworkSettings.Ports` 非 null。
- `make db-up` 打印 `postgres ready on localhost:5434`；异常路径能给出 `hp=0` 诊断。
- `make run` 完成 Open → Migrate → Seed，`curl :3500/healthz` 返回 `status: ok`。
- `make ports` 打印 `5434 -> 5434` 与 `inside compose it is postgres:5434`。
- 宿主 DSN 的库门控用例 `go test ./internal/store/` 全绿；`docker compose config` 中
  agenthub 的 DSN 为 `postgres:5434` 而非 `localhost`。
- `docker compose config -q`、`sh -n scripts/compose-ports.sh` 通过。