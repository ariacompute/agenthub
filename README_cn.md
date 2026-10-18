# agenthub

[English](README.md) | [中文](README_cn.md)

基于 **A2A（Agent2Agent）协议**的互连平台：把 **model / agent / mcp / skill / CLI** 五类异构资产统一注册、发现、路由与编排，对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：Go 1.27 + `net/http`，单二进制（唯一生产依赖为 GORM）
- 前端：Vue 3 + Vite（bun），**13 种语言**，纯黑/白/灰单色视觉
- 持久化：**PostgreSQL + GORM**（资源、任务、artifact、span、事件流、回调留痕全部落库）
- 首版五类适配器**全部为确定性 mock**：无网络、无子进程、离线可跑通

## 概念地图

| 概念 | 角色 | 在 agenthub 中 |
| --- | --- | --- |
| Model | 大脑 | `kind=model`，统一 `Generate/Stream` 接口 + provider 回退链 |
| Agent | 数字员工 | `kind=agent`，目标驱动、可挂起等待人工输入 |
| MCP | 万能转接头 | `kind=mcp`，只做协议转换、不碰业务逻辑；`transport` 仅作描述字段 |
| Skill | 岗位手册 | `kind=skill`，触发条件 + 步骤 + 工具清单的版本化流程资产 |
| CLI | 对讲机 | `kind=cli`，白名单 → 超时 → 截断 → 结构化 JSON 回执 |

一次调用：用户 → Agent → Model 规划 → 加载 Skill → 经 MCP 调用老系统 → 结果沿原路返回。

## 运行

```bash
make db-up                # 起 PostgreSQL（宿主 5434）并等待 ready
make run                  # 默认 :3500（Open → Migrate → Seed）
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` **必填**：缺失时 `config.Validate` 拒绝启动（不存在无库降级路径）。
本地地址：`postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable`（见 `.env.example`）。

控制台：`make web-dev`（开发）或 `make web-build && make run`（构建产物由 Go 进程托管于 `/`）。

### 版本打标

`make build` 与 `make run` 通过 `-ldflags -X` 把 `config.Version`（git tag，如 `v0.2.0`）、
`config.Commit`（7 位 commit）、`config.Distance`（tag 之后的提交数）与 `config.Dirty`
写进二进制。`/healthz` 同时返回，Agent Card 带版本；控制台在**侧边栏左下角**渲染
**`version@commit`**（如 `v0.2.0@1a2b3c4`），构建偏离干净 tag 时另起一行显示 `+3 dirty`
之类的偏离说明。未打标的构建上报哨兵值 `dev`，不冒充发布号；镜像经 `VERSION` / `COMMIT` /
`DISTANCE` / `DIRTY` 构建参数传入同一取值（`make docker-build`）。

## 持久化

| 表 | 内容 |
| --- | --- |
| `resources` | 五类资源条目全字段（`tags/capabilities/auth_schemes/env/metadata` 走 `jsonb`） |
| `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` | 任务主体、输出、span 时间线、`input-required` 挂起问题、回调配置（子表随任务级联删除） |
| `events` | 每次状态/artifact/message/span 帧，支持 SSE 断线重连按 `since=<seq>` 回放 |
| `push_records` | mock notifier 捕获的每次回调（task/url/state/config 快照），按 `AGENTHUB_PUSH_RECORD_LIMIT` 保留 |
| `schema_migrations` | 已执行的迁移文件，启动时跳过 |

- 迁移：`migrations/*.sql` 经 `//go:embed` 内嵌，启动时按文件名**单事务**顺序执行；SQL 自身幂等。
- 写入策略：任务/artifact/span **同步写**（状态迁移为条件更新 `WHERE state=?`，防并发丢更新）；
  events 与 push_records **尽力而为**（有界队列 + 异步 writer，失败只告警，**绝不回压任务执行**）。
- 事件重连：`GET /api/tasks/{id}/events?since=<seq>` 回放该 seq 之后的所有帧，再接实时流。

## Docker 部署

一条命令起**数据库 + 服务**（控制台已烘焙进镜像，由 Go 进程托管于 `/`）：

```bash
docker compose up -d                 # 构建并后台启动 postgresql + agenthub，:3500 / :5434
docker compose ps                    # STATUS 应为 Up (healthy)
docker compose logs -f agenthub
docker compose down                  # 停止；数据保留在 agenthub-pgdata 卷
```

compose 会先拉起 `postgres:16-alpine`，健康检查通过后（pgvector 不需要）再启动 `agenthub`，
后者启动时自动执行迁移与种子。`DATABASE_URL` 由 compose 注入，宿主端口可用 `POSTGRES_PORT` 覆盖。

前端热更新开发（可选，控制台 dev server 走 compose 服务名代理）：

```bash
docker compose --profile dev up -d   # 额外起 web，:5173
```

镜像为三段构建（`oven/bun` 构建控制台 → `golang:1.27-alpine` 编译 → `alpine:3.21` 运行时），
运行时以**非 root 用户**（uid 10001）启动，`/healthz` 为健康检查端点（响应含 `"database":"up"` 字段，
库不可达时返回 503）。容器**无状态**：迁移随二进制内嵌，状态全在 PostgreSQL 卷里。

构建需要拉 npm 包与 Go 模块，即需要出网。走代理时用 `BUILD_PROXY` 传入——`make docker-build`
从 `$HTTP_PROXY` 取值并追加 `--network=host`，容器内的 `127.0.0.1:<port>` 才会指向宿主机代理。
compose 构建跑在 bridge 网络上，该地址不存在，因此 `Dockerfile` 对 `bun install` 与
`go mod download` 各做一次「清掉代理变量」的重试，并回落默认 `GOPROXY` 镜像
（`https://goproxy.cn,direct`，可用 `--build-arg GOPROXY=…` 覆盖）；两次都失败才带提示报错退出，
不会静默装作成功。

**生产部署必须**：

```bash
AGENTHUB_ENV=prod
AGENTHUB_ADMIN_TOKEN=<强随机串>
```

`AGENTHUB_ENV=prod` 且未配置 `AGENTHUB_ADMIN_TOKEN` 时 `config.Validate` **拒绝启动**
（不允许开放控制面）；配置后 `/api/*` 需携带 `Authorization: Bearer <token>`。
配置通过 compose 的 `.env` 或环境变量传入，`AGENTHUB_HTTP_PORT` 可改宿主端口。

## A2A 端点

| 端点 | 说明 |
| --- | --- |
| `GET /.well-known/agent.json` | Agent Card（`GET /a2a/agent-card` 为兼容路径） |
| `POST /a2a` | JSON-RPC 2.0 单入口 |
| `GET /api/resources` 等 | 控制台控制面（`/api/*`） |
| `GET /healthz` | 健康与模式 |

支持方法：`message/send`、`message/stream`（SSE）、`tasks/get`、`tasks/cancel`、`tasks/resume`、`tasks/pushNotification/set`、`tasks/pushNotification/get`；`AGENTHUB_A2A_LEGACY_METHODS=true` 时额外接受 v0.1 别名 `tasks/send`、`tasks/sendSubscribe`。

流式**没有 `[DONE]` 哨兵**：运行以 `completed / failed / canceled / input-required` 状态事件结束。

### 冒烟

```bash
# 1) 发现
curl -s localhost:3500/.well-known/agent.json | head

# 2) 发一个任务（选中 demo-mcp）
curl -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":1,"method":"message/send",
  "params":{"message":{"role":"user","parts":[{"type":"text","text":"查一下上周的请假记录"}]},
            "metadata":{"resourceId":"demo-mcp"}}}'

# 3) 流式（SSE）
curl -N -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":2,"method":"message/stream",
  "params":{"message":{"role":"user","parts":[{"type":"text","text":"帮我请明天下午半天年假"}]},
            "metadata":{"flow":[{"op":"then","resource":"demo-skill"},{"op":"then","resource":"demo-mcp"}]}}}'

# 4) 挂起与恢复（agent 遇 approve/confirm 进入 input-required）
curl -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":3,"method":"message/send",
  "params":{"message":{"role":"user","parts":[{"type":"text","text":"approve the change"}]},
            "metadata":{"resourceId":"demo-agent"}}}'
curl -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":4,"method":"tasks/resume",
  "params":{"id":"<taskId>","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}}'
```

## 编排原语（flow）

`metadata.flow` 接受三种原语，用于多资源接力：

```json
[
  {"op":"then","resource":"demo-model"},
  {"op":"branch","when":"contains:leave",
   "steps":[{"op":"parallel","steps":[{"op":"then","resource":"demo-skill"},{"op":"then","resource":"demo-cli"}]}]},
  {"op":"then","resource":"demo-mcp"}
]
```

分支条件：`""`（默认臂）/ `always` / `contains:<text>` / `kind:<kind>` / `has:<key>`。

## 错误约定

后端**只返回机器可读 code**，绝不含自然语言；控制台在前端把 code 映射为本地化文案。

- HTTP：`{"code":"..."}`（`bad_request` / `unauthorized` / `not_found` / ...）
- JSON-RPC：标准码 + A2A 自定义 `-32001 task_not_found`、`-32002 task_not_cancelable`、`-32003 push_notification_not_supported`、`-32004 unsupported_operation`、`-32005 invalid_state_transition`、`-32006 resource_not_found`、`-32007 resource_unreachable`、`-32008 task_not_resumable`

## 配置

见 `.env.example`。主要项：`DATABASE_URL`（**必填**）、`AGENTHUB_HTTP_PORT`（默认 3500）、
`AGENTHUB_ADMIN_TOKEN` + `AGENTHUB_AUTH_OPEN_MODE`、`AGENTHUB_MAX_PARALLEL`、`AGENTHUB_CLI_ALLOWLIST`、
`AGENTHUB_CLI_TIMEOUT`、`AGENTHUB_MAX_OUTPUT_BYTES`、`AGENTHUB_PUSH_RECORD_LIMIT`（默认 100）。

## 测试

```bash
make db-up             # 集成用例需要真实 PostgreSQL
make test              # go test -race ./...（集成用例在 DATABASE_URL 未设置时 skip）
make test-integration  # 只跑 integration_test.go
make web-test          # cd web && bunx vitest run
```

集成用例覆盖：迁移幂等、资源 CRUD 与 jsonb 往返、任务全生命周期与级联删除、事件回放、
push 回调留痕与保留上限、以及完整 A2A run 的落库回读。**无 DB 时自动 skip，不伪造结果**。

## 与 mastra 的关系

设计参照了 `mastra-ai/mastra` 的特性盘点：采纳其 MCP Registry 条目模型、workflow 控制流三原语（then/branch/parallel）、human-in-the-loop 与 `input-required` 的映射、Studio 试跑交互与 observability 分层。**未引入任何 mastra 代码或 npm 依赖**，Go 侧全部自实现。

不实现的边界：RAG、语音、浏览器自动化、渠道接入、多语言 SDK、LLM-as-judge 评分（eval 仅留钩子，不产出分数）。

## 许可证

MIT.

## 工程约定

本仓库遵循 Harness Engineering：

- [`AGENTS.md`](AGENTS.md)：Agent 工程上下文入口与目录索引
- [`requirements.md`](requirements.md)：需求规格（功能边界 / 异常 / 验收，人审后编码）
- [`task.md`](task.md)：实施清单
