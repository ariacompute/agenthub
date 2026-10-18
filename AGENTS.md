# AGENTS.md — agenthub（A2A 互连平台）

工程上下文入口，渐进式披露：先看概述/架构/目录，动手时再看规范/命令/注意。

## 概述

`agenthub` 是基于 **A2A（Agent2Agent）协议**的互连平台：把 **model（大脑）/ agent（数字员工）/
mcp（万能转接头）/ skill（岗位手册）/ cli（对讲机）** 五类异构资产统一注册、发现、路由与编排，
对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：**Go 1.27 + 标准库 `net/http`**，单二进制；持久层为 **PostgreSQL + GORM**
  （`gorm.io/gorm` + `gorm.io/driver/postgres`，与 `bankmind` 一致，是本仓唯一生产依赖）。
- 前端：Vue 3 + Vite（**bun** 包管理/测试），三屏控制台，**13 种语言**，**纯黑/白/灰单色**视觉。
- 首版五类适配器**全部为确定性 mock**：无网络、无子进程、离线可跑通；真实对接以 `TODO(real)` 标注边界。
- 设计参照 `mastra-ai/mastra` 的**协议与交互形态**（MCP Registry 条目模型、workflow 三原语、
  HITL→`input-required`、Studio 试跑、observability 分层），但**不引入其任何代码或 npm 依赖**。

## 架构（五层单向依赖 + 持久层）

```
a2a（协议类型/编解码，零依赖）
  → registry（五类资源条目 + export）      ┐
    → adapter（Connector 工厂 + 五类实现） │ 依赖倒置：三个端口接口由消费方定义
      → hub（任务状态机 + 事件 broker + span + flow 编排）
        → handlers（/.well-known、/a2a、/api/*、/healthz、静态托管）
persistence ← internal/store（GORM 实现）
  ├─ migrations/*.sql（go:embed，幂等）+ schema_migrations 版本表
  └─ resources / tasks / task_artifacts / task_spans / task_pending / task_push / events / push_records
```

一次请求：鉴权（`/api/*`）→ 方法规范化（v0.1 别名）→ 资源选择 → 适配执行 → 状态机推进 →
artifact 累积 → 事件扇出（SSE + **异步落 `events`**）→ pushNotification 回调（**落 `push_records`**）。

生命周期：`submitted → working → input-required → completed / failed / canceled`。
`input-required` 可**无限期挂起**，并凭 `taskId` 通过 `tasks/resume` 续接恢复（suspend/resume 语义）。

## 持久化分层

| 端口接口（由消费方包定义） | GORM 实现 | 落库内容 |
| --- | --- | --- |
| `registry.ResourceStore` | `store.NewResourceStore` | `resources`：五类条目全字段 + jsonb 列 +时间戳 |
| `hub.TaskRepository` | `store.NewTaskRepository` | `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` |
| `hub.EventRepository` | `store.NewEventRepository` | `events(seq, task_id, kind, payload)`，供 `since=<seq>` 断线重连回放 |
| `hub.PushRepository` | `store.NewPushRepository` | `push_records`：mock notifier 捕获的每次回调（url/state/config 快照） |

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
- `internal/store`：`db.go`（`Open`/`Ping`/`Close`/`RedactDSN`，连接池 25/10/30min、`PrepareStmt`）、
  `migrate.go`（`go:embed` + `schema_migrations` 按序单事务执行）、`seed.go`（demo 五类资源）、
  `resource_store.go` / `task_store.go` / `event_store.go` / `push_store.go`（四个仓库实现）
- `internal/registry`：`registry.go`（`Kind`/`Health`/`Transport`/`Entry` + CRUD + 发现 +
  `ResourceStore` 端口 + 委托）、`export.go`（聚合 Agent Card + MCP server 描述）
- `internal/adapter`：`connector.go`（`Connector` 接口 + `Options` + `For` 工厂）、
  `mock.go`（确定性基元）、`model.go`（provider 注册表 + 回退链）、`agent.go`、`mcp.go`、`skill.go`、`cli.go`
- `internal/hub`：`store.go`（`TaskRepository`/`EventRepository`/`PushRepository` 端口 + `Persistence`）、
  `task.go`（TaskStore + 合法迁移校验 + 挂起状态 + 条件更新委托）、`trace.go`（span 时间线）、
  `broker.go`（事件扇出 + 有界队列异步落库）、`flow.go`（`then/branch/parallel`）、
  `orchestrator.go`（send/stream/get/cancel/resume/push + MockNotifier 落库 + Prune ticker）
- `internal/handlers`：`handlers.go`（路由注册中心 + `requireAdmin` + 静态回退 + `/healthz` 的 `database` 字段）、
  `a2a.go`、`api.go`（含 `/api/tasks/{id}/events?since=<seq>` 回放）
- `migrations/`：`001_resources.sql` … `004_push_records.sql`，经 `embed.go` 内嵌
- `tests/e2e_test.go`：httptest 真实 HTTP 端到端（内存模式）；`tests/integration_test.go`：`DATABASE_URL` 门控集成
- `docker-compose.yml`：`postgres:16-alpine`（宿主 **5434**）+ `agenthub`（依赖 `service_healthy`）
  + `web`（`profiles: [dev]`）
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
- **测试**：`go test -race ./...`（纯逻辑单测零外部依赖 —— memory 回退保证无 DB 也全绿）；
  集成用例以 `DATABASE_URL` 门控（**未设置则 `t.Skip`，绝不伪造结果**）；前端 `bunx vitest run`。
  新增功能须补**正常 + 异常**双路径用例。

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
make vet / make fmt
make web-install / make web-dev / make web-build / make web-test
```

## 镜像发布

- 发布 Release 触发 `.github/workflows/release.yml`：构建并推送 **`ariacompute/agenthub-agenthub`**
  到 DockerHub，tag 取 Release tag（`ariacompute/agenthub-agenthub:<tag>`）并额外打 `:latest`。
- tag 同时作为 `VERSION` build-arg、`7 位 commit` 作为 `COMMIT`、`DISTANCE=0`、`DIRTY=false`
  （CI 一定是干净 checkout），走的是 Dockerfile 已有的四个 `ARG`，与本地 `make docker-build` /
  `make compose-build` 完全一致；这样**镜像 tag === `/healthz` 的 `version@commit`**，可回溯。
- **不设兜底**：Release 无 tag、`./Dockerfile` 缺失都以 `::error::` 退出——宁可发布失败，也不推一个
  自报 `dev` 哨兵、说不清来源的 `:latest`。
- 必填 secrets：`DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`；`:latest` **只由 CI 推**，禁止本地手工推。
- 平台仅 `linux/amd64`（与 docker 仓 release.yml 一致）；如需多架构再引 `setup-qemu` + `setup-buildx`。

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
- 兜底哨兵 `"dev"` / `"unknown"`：未打标的构建在 `/healthz` 上报 `dev`，一眼可辨，
  绝不冒充某个发布号。

冒烟：`curl localhost:3500/healthz`；`curl localhost:3500/.well-known/agent.json`；
`POST /a2a` 的 `message/send` 与 `message/stream`（详见 `README_cn.md`）。

## 注意

- 端口：服务 **3500**，compose 数据库宿主 **5434**；避开 bankmind(8080)、cockpit store(3400)、
  宿主 postgres(5432)、bankmind 数据库(5433)。
- 控制面 `/api/*` 走 `AGENTHUB_ADMIN_TOKEN`（Bearer / ApiKey / 裸 token 均可）；未配置 token 时
  仅在 `AGENTHUB_AUTH_OPEN_MODE=true` 下开放，**`AGENTHUB_ENV=prod` 必须配 token**（`Validate` 拒绝）。
- 控制面错误码到状态码的映射集中在 `internal/handlers/api.go:writeDomainErr`
  （`invalid_*`/`duplicate` → 400，`*_not_found` → 404，`not_cancelable`/`not_resumable` → 400），
  新增领域错误需同步该映射，否则会退化为 500。
- 每次启动都会尝试写入 5 个 `demo-*` 种子资源（每类一个），`ON CONFLICT (id) DO NOTHING` 保证幂等；
  用户覆盖过的同名条目**不会被种子改写**，保证 README 冒烟可复现。
- 前端未执行 `make web-build` 时 `/` 返回 404（机器可读 `not_found`），不是服务故障。
- 不实现：RAG、语音、浏览器自动化、渠道接入、多语言 SDK、LLM-as-judge 评分（属 `memo` /
  `bankmind` / `playground` 或后续项）。
