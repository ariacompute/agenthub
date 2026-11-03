# 🚀 AgentHub 发布说明

> AgentHub 是面向 A2A（Agent2Agent）协议的互连平台：单二进制后端 + PostgreSQL，外加一个九屏控制台。

---

## 📦 一、平台总览

- 🧠 **五类资产**：model（大脑）/ agent（数字员工）/ mcp（万能转接头）/ skill（岗位手册）/ cli（对讲机），外加 `custom` 自由 schema。
- 🧪 **执行沙盒**：`internal/sandbox` 以租约分配执行环境（默认 Docker，失败即拒绝，绝不回落宿主进程）。

---

## 🛰️ 二、A2A 协议层

- 🔍 **服务发现**：`GET /.well-known/agent.json`（兼容 `/a2a/agent-card`），每个资源渲染为一个 skill。
- 🎯 **资源选择**：`metadata.resourceId|resource_id|resource`（精确 id → 名称 → kind）→ `metadata.kind` 回退 → 无选择器时优先 agent；候选集只含 `Discoverable` 条目。
- 🔁 **生命周期**：`submitted → working → input-required → completed / failed / canceled`；`input-required` 可**无限期挂起**，凭 `taskId` 走 `tasks/resume` 续接。
- 🌊 **SSE 真流式**：帧名 `task_status_update` / `task_artifact_update` / `message`，无 `[DONE]` 哨兵，终止由终态帧表达；`GET /a2a/stream?task=<id>` 给控制台一条免 JSON-RPC 封装的旁路。

---

## 🗂️ 三、资源注册层

- 🧾 **条目模型**：`version` / `repository` / `tags` / `env` / `auth_schemes` 等 Registry 级元数据，外加 `capabilities` 与健康度；六类各有专属 per-kind 配置区块，后端按同一结构校验。
- 🔑 **去重启用键**：`(namespace, name, version)` 由唯一索引保证——同名换版本是新增，同名同版本是冲突。
- ✅ **审批生命周期**：`draft → pending → approved / rejected`，另有 `deprecated`；带 `auto_approve` 的命名空间可跳过待审。
- 🔄 **上游同步**：MCP / Skill 源按手动或定时把外部记录拉入本地目录，留痕 `last_sync_at` / 状态 / 计数 / 错误。
- 🧰 **资源级沙盒策略**：条目可声明 `provider` / `runtime` / `image` / 网络与出网白名单 / CPU·内存·pids / `args_policy` / `credential_env` 等；未知或不可用的后端在**注册阶段**即被拒绝。

---

## 🔌 四、适配层

- 🧩 **五类连接器**：首版全部为确定性 mock（无网络、无子进程、离线可跑通），真实对接点统一标 `TODO(real)`。
- 🧠 **Model Router**：provider 注册表 + 回退链，可指向真实的 OpenAI 兼容网关，失败自动回退 mock。
- 🛡️ **安全与可靠性**：`api_key_env` 只取环境变量名、`cli` 走白名单 + 超时 + 截断、出向 URL 过 SSRF 护栏。
- 🧷 **MCP 与远端 Agent**：stdio 型服务在租约内启动，工具白名单在进程真正运行处生效；远端调用经服务端代发并从 `credential_env` 注入凭据，密钥不进上下文与产物。

---

## 🎛️ 五、Hub 编排层

- 🧵 **任务状态机**：合法迁移校验 + 条件更新，`input-required` 可挂起、可恢复。
- 🪄 **flow 三原语**：`then` / `branch` / `parallel`。
- 📊 **span 时间线**：每次运行留一条可回看的耗时线；事件扇出 SSE 并**异步落 `events`**，供 `since=<seq>` 断线回放。
- 🔔 **pushNotification**：mock notifier 也会把每次回调落 `push_records`，可回查。
- 🧯 **优雅停摆**：`quiesce` 闸门在恢复期间拒新任务并等待在途任务，绝不半途截断。
- 👤 **归属人贯穿**：新增 `SendAs(ctx, ownerID, params)`（`Send` 保持签名并委派空 owner），调度器以工单的 actor 发起任务，租约不再落进「匿名不复用」分支。

---

## 🗄️ 六、持久化

- 🐘 **PostgreSQL + GORM**：连接池 25 / 空闲 10 / 30 分钟，`PrepareStmt` 开启。
- 📜 **迁移幂等**：`001_resources.sql` … `031_shared_files.sql` 共 31 份，`go:embed` + `schema_migrations` 按序单事务执行；`029` / `030` 在打开 `FORCE` 的同一个事务里回填继承来的归属。
- 🌱 **种子数据**：`ON CONFLICT (id) DO NOTHING`，重复启动不会炸。
- 💾 **检查点与恢复**：`pg_dump -Fc` / `pg_restore` 与纯 Go `COPY` 两条引擎自动选择；三种定时模式 + 手写 5 段 cron；恢复期间单飞并拒新任务。
- 🔐 **身份与授权**：PBKDF2-HMAC-SHA256 密码、会话 cookie、OAuth（Google / GitHub / 通用 OIDC，PKCE S256）、API 密钥；组 / 角色 / 权限 / 资源级授权，`owner` 持有全部已知权限。
- 🧪 **沙盒事实**：`sandbox_leases` / `sandbox_execs` / `sandbox_nodes` / `sandbox_jobs` / `sandbox_events` 五表 + `computers` 新列；审计只存命令摘要、退出码、耗时与输出摘要的 sha256，**不存全量输出**。
- 🧾 **审计账本**：`audit_log` 只追加，每次 `/api` 与 `/a2a` 调用一行，`hash = sha256(prev_hash ‖ canonical(row))` 逐行链接；删或改任一行都会断开其后所有链接，`/api/audit/verify` 指出第一处断点。
- 🔒 **归属列**：`tasks` 及其子表、`events`、`knowledge`、`schedules`、`channels`、`computers`，外加本轮补齐的 `threads` / `thread_messages` / `channel_messages` / `shared_files` 都带 `owner_id`；存量任务行不回填，但归属**可推导**的（线程消息、通道投递）在同事务内照抄父行。
- 📁 **共享文件元数据**：`shared_files` 只存 `name` / `mime` / `size` / `sha256` / `backend` / `object_key` / `owner_id`，字节落在本地磁盘或 S3 兼容存储。

---

## 🖥️ 七、控制台

- 🧭 **九屏**：registry / registries / sources / trace / topology / login / admin / audit / docs。
- 🌐 **13 种语言**：`en zh zh-TW es fr de it ru ja ko pt ar hi`，`ar` 下整站 RTL。
- 📚 **文档内嵌**：`/docs` 屏用 13 语记录全部端点，并由 `docs-content.js` 生成双语 Markdown。
- 🔍 **审计屏**（需 `audit:read`）：时间窗预设与筛选、四枚水位指标、明细表（毫秒时间 / 请求 ID / 主体 / 动作 / 资源 / 状态码 / 耗时条形），以及并排展示 `prev_hash` 与 `hash` 的取证抽屉与链校验。

---

## 🤖 八、Agent 能力、通道与语音

- 🎭 **Persona**：人像档案（`display_name` / `title` / `tagline` / `avatar_url`）归一化后落 `resources.persona` JSONB。
- 📨 **Channels**：Slack / Teams / Telegram / SMS 绑定到线程；入站走签名校验的 webhook（密钥只存环境变量名），出站走 SSRF 安全的 `Deliver`，回显原始响应 ≤512B、不重试。
- 🧩 **Connectors**：`kind=mcp` 资源的策略网关，`off / ask_first / allowed`；`ask_first` 经 `Hub.ParkConnector` 挂起 `input-required`，并把 `action` / `args` / `requestedBy` 一带落进 `metadata.connectorApproval`，于是连接器页能显示「在批准什么」并直接批准或拒绝（见十四）。
- 🎙️ **Speech**：`POST /api/threads/{id}/audio` 上传语音片段，服务端离线转写后作为 `origin=voice` 消息委派，并把 agent 回复合成 TTS 音频附回；超限返回 **413 `audio_too_large`**，与「格式不支持」（400）明确区分。
- 🖥️ **能力端点**：计算机（`browser` / `shell` / `screen`）指向沙盒租约（`provider` / `lease_id` / `node_id` / `expires_at`）并可一次性试跑；目标调度接触发路由器；mock RAG 仍为脚手架。

---

## ⏱️ 九、调度器（Router）

- 🧠 **双层调度**：Router 层判定「该不该做、交给谁」，Worker 层执行；判定分程序层（来源模式、白名单、`@提及`、关键词、静默时段）与语义层（规则或 LLM 批量评估，逐条回退规则）。
- ✍️ **单一写者**：状态在事务内、外部动作在提交后、命令与事实分离。
- 🎫 **工单与交回**：订阅扇出、`handback` 只能一次、关闭后拒绝改派；节拍单飞 + 请求合并 + panic 隔离。
- 📈 **可观测**：`/api/router/*` 暴露水位、工单、策略、节拍与 `events` SSE；默认关闭。

---

## 🧪 十、执行沙盒

- 🪜 **四层加平台层**：容器 `docker`（默认）/ `podman`、用户态内核 `runsc`、microVM `kata`、进程沙盒 `bwrap` 与声明式 `nsjail` / `isolate` / `systemd-run` / `local`，平台层 `e2b` / `microsandbox` / `daytona` / `cubesandbox` / `openhands` 以配置模板接入；`mock` 确定性离线，`off` 拒绝一切。
- 🎫 **租约活过任务**：作用域解析 `metadata.ticket` → `metadata.contextId` → `default`，同一 ticket 的多步共享一个环境；任务取消不销毁环境，环境消失则下一次调用透明重建。
- 📏 **并发联动**：启动校验 `SANDBOX_MAX_TOTAL ≥ ROUTER_MAX_TICKETS`；取不到租约返回**可重试**的 `sandbox_limit_reached`，交给调度器退避。
- 🔒 **收口四个执行面**：CLI 命令只来自登记信息、MCP stdio 在租约内启动且白名单生效、计算机指向真实租约、远端 Agent 出网经服务端代理并从 `credential_env` 注入凭据。
- 🖥️ **端点**：`/api/sandboxes`（列表 / 详情 / `/stats` / `/events` 支持 `since` 回放）、`/api/sandbox-providers`（按层分组并给出原因）、`/api/sandbox-nodes`（注册、心跳、长轮询领取、回报）。
- 🧭 **能力先声明再执行**：`local` 只声明 `platform`、`bwrap` 不再声明 `bridge`（`--share-net` 是宿主命名空间）、`docker` 三项与 `createArgs` 对齐、远端节点与平台垫片只声明 `delegated`；`Descriptor` 新增 `EnvKeys` 与 `SourcePlatform`。`Manager.Acquire` 发现申请的网络模式不在声明内即回 `sandbox_capability_unavailable`，**后端一次都不会被调用**。

---

## ☸️ 十一、部署与运维

- 📦 **交付形态**：单二进制 + `docker-compose`；`operator` 提供 Kubernetes 控制器。
- ⚙️ **配置**：环境变量 + 向上最多 6 层找 `.env`，只补未设置的键；`DATABASE_URL` 必填。
- 🔒 **安全加固**：`go 1.27.3` / `bun 1.4.4` 修 emSign 弱 CA；SSRF 护栏覆盖 A2A 回调与上游同步；限流对 loopback 客户端豁免。
- 🧪 **沙盒部署姿态**：宿主直跑与容器内两种都支持；容器内挂 socket 等价于宿主 root，故 compose 提供
  docker-socket-proxy sidecar（仅开 CONTAINERS / IMAGES / POST），socket 缺失时 fail closed 并写出 `reason`。

---

## 🛡️ 十二、数据隔离与审计

- 🏷️ **归属列**：`tasks` 及子表、`events`、`knowledge`、`schedules`、`channels`、`computers`、`threads` / `thread_messages` / `channel_messages` / `shared_files` 加 `owner_id`；`''` 表示进程自身写入，存量任务行**不回填**，而线程消息与通道投递继承父行属主。
- 🚧 **行级安全**：上述各表与 `audit_log` 全部 `ENABLE` + `FORCE ROW LEVEL SECURITY`（**十五张**，由 `026` / `028` / `029` / `030` / `031` 覆盖）；策略读 `app.owner_id` / `app.bypass` / `app.system` 三个事务内设置，未注入即读作 NULL → **什么都不返回**（fail-closed）。
- ⚡ **短事务注入**：`internal/store.scoped` 每次仓储调用开一个事务并 `set_config(..., true)`，请求不持连接，SSE 不会吃光连接池。
- 👥 **角色分离**：`agenthub_migrator`（DDL / schema 属主）、`agenthub_app`（只有 DML、非表属主、无 `BYPASSRLS`）、`agenthub_readonly`；脚本发现受保护表缺 `FORCE` 策略会直接中断。
- 🚨 **降级开关**：`AGENTHUB_RLS=off` 让全部策略通过，仅供本地；`AGENTHUB_ENV=prod` 下需 `AGENTHUB_RLS_ACK=1`，且启动日志会写明边界已放松。
- 🧾 **审计**：每次 `/api`、`/a2a` 调用一行，字段对齐 CloudTrail 词汇；query 值一律 `REDACTED`，不存请求体与响应体。触发器对 `UPDATE` / `DELETE` / `TRUNCATE` 一律拒绝（含表属主），运行角色只被授予 `INSERT` / `SELECT`。
- 🔗 **哈希链**：`hash = sha256(prev_hash ‖ canonical(row))`，启动时读末行续链；单写者 + 有界队列（8192）+ 批量插入（64 条 / 500 ms），队列满丢帧计数而**不阻塞请求**。
- 🔑 **新权限**：`audit:read` 独立于 `tasks:read` / `users:manage`，由 `027_audit_permission.sql` 授予 admin；控制台权限矩阵已补齐 13 语标签。
- 🛰️ **A2A 授权**：`message/send|stream` 必须先解析出具体 `resourceId`；`tasks/get|cancel|resume` 与 `pushNotification/set|get` 要求属主或管理员，他人任务一律 `task_not_found`（不泄露存在性），匿名调用需身份的方法返回 `-32009`。

---

## 📁 十三、共享文件与审批队列

- 🗃️ **共享文件**：`AGENTHUB_FILES_STORE=off|local|s3`（默认 off，关着时所有 `/api/files*` 回 `503 files_disabled`）。`GET/POST /api/files`、`GET /api/files/{id}`、`GET /api/files/{id}/content`（`http.ServeContent`，支持 Range 续传）、`DELETE /api/files/{id}`；权限沿用 `tasks:read` / `tasks:write`。
- 📏 **上限与校验**：`AGENTHUB_FILES_MAX_BYTES`（32 MiB）在字节流入临时文件时判定，超限 413 `file_too_large`；每行带 sha256 与嗅探出的 MIME；文件名只留基名。
- 💾 **两个后端**：本地（临时文件 + 原子 rename、`0600`、按摘要前两位分片，键必须是 `<2 hex>/<64 hex>`）；S3 兼容（RustFS / MinIO / AWS）——标准库手写 SigV4、path-style、单次 `PUT` 带 `x-amz-content-sha256`，密钥只从 `_ACCESS_KEY_ENV` / `_SECRET_KEY_ENV` 点名的环境变量读取，**零新依赖**。
- 🔐 **按属主收口**：`shared_files` 在 `FORCE` 行级安全下，别人的文件给 id 也只能得到 `404 file_not_found`。
- ✅ **审批队列**：`GET /api/connectors/approvals` 从 `input-required` 任务派生队列（**没有审批表**）；`POST /api/connectors/approvals/{id}/approve` 带挂起参数恢复任务、连接器**恰好执行一次**，`.../reject` 取消且什么都不执行；非待审批 / 已决策 / 他人任务回 404，重复决策回 409。
- 🧪 **用例**：`internal/files` 语句覆盖 86.4%（上限、哈希、文件名、后端故障、键穿越、取消），`s3_test.go` 用 httptest 桩验签名头与路径转义，`handlers/files_test.go` 覆盖上传 → 列表 → 带 Range 下载 → 删除与 413 / 415 / 404 / 502 / 503；`handlers/connectors_approval_test.go` 覆盖批准只跑一次、重复批准 409、拒绝后再批准 409、未知与无关任务 404。

---

## 🧪 十四、测试

- ✅ **后端**：`go build ./...` + `go vet ./...` + `go test ./...` 全绿；纯逻辑用例零外部依赖，DB 用例以 `DATABASE_URL` 门控，未设则 skip 且**绝不伪造结果**。
- 📈 **覆盖率**（带库）：`identity` 与 `schedules` 100%、`sandbox` 98.2%、`a2a` 98%、`model` 95.7%、`adapter` 95.4%、`audit` 95.1%、`config` 94.4%、`authz` 91.8%、`registry` 87.9%、`hub` 82.1%、`handlers` 73.1%、`backup` 57.3%。
- 🧷 **隔离与审计护栏**：`model_test.go` 用 45 张表的表名钉住迁移；`internal/audit` 覆盖续链、插入失败只计数、队列满丢帧、`Verify` 定位断点；`internal/handlers` 断言「读审计不被提权为 system」；前端 `auth-store.spec.js` 直接读 Go 源码的 `authz.AllPermissions` 做集合比对。
- 🛡️ **异常路径护栏**：`WriteErr` 全码穷举 + AST 守卫、`wrap` 的 panic → JSON 与 HSTS、API 密钥窗口与范围、调度门面与事件总线、SSRF 白名单。
- 🧪 **沙盒**：`internal/sandbox/sandbox_test.go` 覆盖复用、作用域隔离、匿名不复用、每人上限、审计行、终止、续租上限、清扫、目录原因、重复注册、统计、事件回放与任务队列（单次领取、能力匹配、未知回报）；`tests/sandbox_e2e_test.go` 用真实 HTTP 打控制台端点（空命令被拒、未知租约 404、他人租约不可见、未接线 503、节点注册 → 领取 → 回报、SSE 回放）。
- 🧾 **端到端与脚本**：`tests/e2e_test.go` 六段链路；`scripts/test-api.sh` 对运行实例全量探针；`example/load.sh` 灌入示例并回读校验。
- 🌐 **前端**：`bun run test` 21 个文件 **417 项通过**（languages / copy-fallback / enums-copy / api / api-endpoints 的端点契约 / audit / auth-store / errors / style 守卫 / docs-content 的 13 语分组等）；`bun run test:coverage`（v8）语句 100%、分支 95.8%、函数 100%。
- 🧑‍⚖️ **生产角色也跑一遍**：CI 第二个 job 用 `scripts/provision-db-roles.sql` 建 `agenthub_migrator` / `agenthub_app`，迁移后以只有 DML、`NOBYPASSRLS`、无 DDL 的 `agenthub_app` 跑完整套件；需要建表的用例走 `internal/testdb` 的临时 schema（`TempSchema`），无 DDL 权限时 `RequireDDL` 带原因跳过，而不是报「权限不足」。
