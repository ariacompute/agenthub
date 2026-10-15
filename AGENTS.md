# AGENTS.md — agenthub（A2A 互连平台）

工程上下文入口，渐进式披露：先看概述/架构/目录，动手时再看规范/命令/注意。

## 概述

`agenthub` 是基于 **A2A（Agent2Agent）协议**的互连平台：把 **model（大脑）/ agent（数字员工）/
mcp（万能转接头）/ skill（岗位手册）/ cli（对讲机）** 五类异构资产统一注册、发现、路由与编排，
对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：**Go 1.27 + 标准库 `net/http`，零第三方依赖**，单二进制，无数据库（内存态 + 可选 JSON snapshot）。
- 前端：Vue 3 + Vite（**bun** 包管理/测试），三屏控制台，**13 种语言**，**纯黑/白/灰单色**视觉。
- 首版五类适配器**全部为确定性 mock**：无网络、无子进程、离线可跑通；真实对接以 `TODO(real)` 标注边界。
- 设计参照 `mastra-ai/mastra` 的**协议与交互形态**（MCP Registry 条目模型、workflow 三原语、
  HITL→`input-required`、Studio 试跑、observability 分层），但**不引入其任何代码或 npm 依赖**。

## 架构（五层单向依赖）

```
a2a（协议类型/编解码，零依赖）
  → registry（五类资源条目 + snapshot + export）
    → adapter（Connector 工厂 + 五类实现）
      → hub（任务状态机 + 事件 broker + span + flow 编排）
        → handlers（/.well-known、/a2a、/api/*、/healthz、静态托管）
```

一次请求：鉴权（`/api/*`）→ 方法规范化（v0.1 别名）→ 资源选择 → 适配执行 → 状态机推进 →
artifact 累积 → 事件扇出（SSE）→ pushNotification 回调（内存记录）。

生命周期：`submitted → working → input-required → completed / failed / canceled`。
`input-required` 可**无限期挂起**，并凭 `taskId` 通过 `tasks/resume` 续接恢复（suspend/resume 语义）。

## 目录

- `cmd/agenthub-server`：唯一入口（dotenv → config → registry 载入 snapshot → 种子 → hub → handlers →
  `http.Server` + 信号优雅退出 + snapshot 落盘）
- `internal/config`：环境变量与默认值 + `Validate()`；`dotenv.go` 向上最多 6 层找 `.env`，只补未设置的键
- `internal/httputil`：`WriteJSON/WriteError/WriteErr/Decode`（机器可读 code）+ SSE（`Stream/WriteSSE/Comment/KeepAlive`）
- `internal/a2a`：**冻结契约**。`types.go`（Task/Message/Part/Artifact/AgentCard）、
  `jsonrpc.go`（2.0 信封 + 错误码）、`methods.go`（方法名与 v0.1 别名映射）
- `internal/registry`：`registry.go`（`Kind`/`Health`/`Transport`/`Entry` + CRUD + 发现）、
  `snapshot.go`（JSON 快照原子写）、`export.go`（聚合 Agent Card + MCP server 描述）
- `internal/adapter`：`connector.go`（`Connector` 接口 + `Options` + `For` 工厂）、
  `mock.go`（确定性基元）、`model.go`（provider 注册表 + 回退链）、`agent.go`、`mcp.go`、`skill.go`、`cli.go`
- `internal/hub`：`task.go`（TaskStore + 合法迁移校验 + 挂起状态）、`trace.go`（span 时间线）、
  `broker.go`（事件扇出）、`flow.go`（`then/branch/parallel`）、`orchestrator.go`（send/stream/get/cancel/resume/push）
- `internal/handlers`：`handlers.go`（路由注册中心 + `requireAdmin` + 静态回退）、`a2a.go`、`api.go`
- `tests/e2e_test.go`：httptest 真实 HTTP 端到端
- `web/`：Vue 3 控制台（`src/lib/copy/*-copy.js` ×13 语、`languages.js`、`use-copy.js`、`api.js`、
  `errors.js`、`style.css` 单色 token；`views/` 三屏；`tests/` vitest）
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
  禁止吞错与静默 no-op。
- **并发**：`registry`/`TaskStore` 用 `sync.RWMutex`；`Hub` 不可变（runner 按请求创建）；
  broker 对慢消费者**丢弃最旧帧**而非阻塞运行；`parallel` 分支数受 `AGENTHUB_MAX_PARALLEL` 约束。
- **不做评分**：eval 仅留钩子，不接 LLM judge、不产出任何分数（对齐仓内「禁止伪造分数」约定）。
- **色彩禁令**：UI 不得出现任何有彩色相，一律落在 `#000 ↔ #fff` 明度轴；
  `web/tests/style.spec.js` 自动校验（hex 三通道相等 + 色相词黑名单）。
- **i18n 单向来源**：`languages.js`（13 语，与 ariacompute.cn 一致）+ 按语言分块的 copy 表，
  浅合并英文兜底；组件内禁止硬编码自然语言。
- **测试**：`go test -race ./...`（纯逻辑无外部依赖，DB/网络类本仓不存在）；前端 `bunx vitest run`。
  新增功能须补**正常 + 异常**双路径用例。

## 常用命令

```bash
make run          # go run（默认 :3500）
make build        # web-build + go build → bin/agenthub-server
make test         # go test -race ./...
make vet / make fmt
make web-install / make web-dev / make web-build / make web-test
```

冒烟：`curl localhost:3500/healthz`；`curl localhost:3500/.well-known/agent.json`；
`POST /a2a` 的 `message/send` 与 `message/stream`（详见 `README_cn.md`）。

## 注意

- 端口 **3500**，避开 bankmind(8080)、cockpit store(3400)、postgres(5432/5433)。
- 控制面 `/api/*` 走 `AGENTHUB_ADMIN_TOKEN`（Bearer / ApiKey / 裸 token 均可）；未配置 token 时
  仅在 `AGENTHUB_AUTH_OPEN_MODE=true` 下开放，**`AGENTHUB_ENV=prod` 必须配 token**（`Validate` 拒绝）。
- 控制面错误码到状态码的映射集中在 `internal/handlers/api.go:writeDomainErr`
  （`invalid_*`/`duplicate` → 400，`*_not_found` → 404，`not_cancelable`/`not_resumable` → 400），
  新增领域错误需同步该映射，否则会退化为 500。
- 启动时若 registry 为空会写入 5 个 `demo-*` 种子资源（每类一个），保证 README 冒烟可复现；
  已配置 `AGENTHUB_DATA_FILE` 时以快照为准，不重复种子。
- 前端未执行 `make web-build` 时 `/` 返回 404（机器可读 `not_found`），不是服务故障。
- 不实现：RAG、语音、浏览器自动化、渠道接入、多语言 SDK、LLM-as-judge 评分（属 `memo` /
  `bankmind` / `playground` 或后续项）。
