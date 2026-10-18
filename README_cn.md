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

## 配置

见 `.env.example`。主要项：`DATABASE_URL`（**必填**）、`AGENTHUB_HTTP_PORT`（默认 3500）、
`AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` + `AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD`、`AGENTHUB_SESSION_TTL`、
`AGENTHUB_MAX_PARALLEL`、`AGENTHUB_CLI_ALLOWLIST`、`AGENTHUB_CLI_TIMEOUT`、
`AGENTHUB_MAX_OUTPUT_BYTES`、`AGENTHUB_PUSH_RECORD_LIMIT`（默认 100）、
`AGENTHUB_SEED_DEMO`（默认 **false**）、`AGENTHUB_BACKUP_DIR`（默认为空 = 关闭检查点）、
`AGENTHUB_BACKUP_KEEP_LAST` / `AGENTHUB_BACKUP_MAX_AGE_DAYS`（保留策略）。

### 用户、会话与 RBAC

控制台与 `/api/*` 控制面走的是**会话**，不再是共享 token：

- **邮箱 + 密码**：`POST /api/auth/register`、`POST /api/auth/login`（响应下发 HttpOnly cookie）。
  口令以 PBKDF2-HMAC-SHA256 派生存储，明文与原始口令都不落库。
- **OAuth**：Google、GitHub 与任意通用 OIDC 发行方，手写授权码流程，带 PKCE（S256）与 `state`。
  未配置凭据的提供方不会出现在登录页。
- **引导管理员**：`AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` / `..._PASSWORD` 每次启动幂等 upsert，并成为
  **owner**。若两者都未配置且库中无账号，第一个注册的账号即成为 owner，实例因此不会无人可用。
- **RBAC**：用户 → 组 → 角色 → 权限（`resources:read`、`resources:write`、`tasks:read`、`tasks:write`、
  `users:manage`、`roles:manage`、`grants:manage`、`sessions:manage`、`backups:manage`）。owner 持有全部权限，
  这是配置出错时仍可挽回的兜底。
- **资源级授权**：`POST /api/grants` 把某条资源限定给某个用户或组（`read` / `write`）。
  **没有**授权的资源对所有人可见；一旦产生授权，它在列表、导出与 Agent Card 中都被隐藏，
  直接访问返回 `404`。
- **会话**：服务端行记录，可吊销（`DELETE /api/auth/sessions/{id}`；`sessions:manage` 可用
  `DELETE /api/users/{id}/sessions` 踢人）。`/healthz` 的 `auth` 字段为
  `session | anonymous | disabled`。

删除用户是**停用**：任务、span 与授权都会保留以备审计。

以上能力在控制台中均可使用：`/login` 注册与登录；`/admin`（需 `users:manage`、`roles:manage`、
`grants:manage` 或 `backups:manage`）管理用户、权限矩阵、资源授权与数据库检查点。

默认不灌任何演示数据：只有显式 `AGENTHUB_SEED_DEMO=true`，启动时才会幂等地写入五条 `demo-*` 资源
（`demo-model`、`demo-agent`、`demo-mcp`、`demo-skill`、`demo-cli`），下面的示例正是面向它们。
除此之外实例以**空 registry** 启动；`config.Validate` 会拒绝 `AGENTHUB_ENV=prod` +
`AGENTHUB_SEED_DEMO=true` 这一组合。

## 运行

```bash
make db-up                                  # 起 PostgreSQL（宿主 5434）并等待 ready
make run                                    # 默认 :3500（Open → Migrate → Seed）
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` **必填**：缺失时 `config.Validate` 拒绝启动（不存在无库降级路径）。
本地地址：`postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable`（见 `.env.example`）。

控制台：`make web-dev`（开发）或 `make web-build && make run`（构建产物由 Go 进程托管于 `/`）。

registry 默认为**空**，不灌任何演示数据；置 `AGENTHUB_SEED_DEMO=true` 才会得到下面示例所依赖的
五条 `demo-*` 资源。

### 版本打标

`make build` 与 `make run` 通过 `-ldflags -X` 把 `config.Version`（git tag，如 `v0.2.0`）、
`config.Commit`（7 位 commit）、`config.Distance`（tag 之后的提交数）与 `config.Dirty`
写进二进制。`/healthz` 同时返回，Agent Card 带版本；控制台在**侧边栏左下角**渲染
**`version@commit`**（如 `v0.2.0@1a2b3c4`），构建偏离干净 tag 时另起一行显示 `+3 dirty`
之类的偏离说明。未打标的构建上报哨兵值 `dev`，不冒充发布号；镜像经 `VERSION` / `COMMIT` /
`DISTANCE` / `DIRTY` 构建参数传入同一取值（`make docker-build`）。

## 部署

下面两条路径都能起**数据库 + 服务**——控制台已烘焙进镜像、由 Go 进程托管于 `/`。
想要起完之后的那份端口表，就用 `make`；只要 Docker 本身，用原生 compose 即可。

### Make

```bash
make up     # docker compose up -d，完成后打印端口清单
make ports  # 对已运行的栈重打端口清单
make down   # 停止；数据保留在 agenthub-pgdata 卷
```

compose 没有 after-up 钩子，所以 `make up` 在栈健康后立刻打印端口清单，`make ports` 之后可随时重打：
哪个宿主端口在应答、分别是做什么的：

```text
$ make ports

agenthub listening ports

  SERVICE            HOST -> CTR      STATE      PURPOSE
  agenthub-server    3500 -> 3500     healthy    the only HTTP listener: console + A2A + control plane + healthz
  postgres           5434 -> 5432     healthy    the only state: resources, tasks, spans, events, push records

  ENDPOINT                                            USE
  http://127.0.0.1:3500/                              Vue 3 console (bundle baked into the image)
  http://127.0.0.1:3500/.well-known/agent.json        A2A Agent Card (discovery)
  POST http://127.0.0.1:3500/a2a                      A2A JSON-RPC: message/send, message/stream, tasks/*
  http://127.0.0.1:3500/api/*                         control plane: resources, tasks, events, push records
  http://127.0.0.1:3500/healthz                       health probe, also the container healthcheck
  postgres://agenthub@localhost:5434/agenthub         host DSN (inside compose it is postgres:5432)
```

上面这些端点在容器内同属**一个监听端口**（恒为 3500），宿主侧映射只有 compose 知道，所以要这张表。
服务自身启动时也会在日志里逐条打印同样的端点清单，因此 `docker compose up`（前台）或
`docker compose logs -f agenthub` 也能看到用途说明。

### Docker compose

```bash
docker compose up -d                # 需要时先构建，再起 postgres + agenthub，:3500 / :5434
docker compose ps                   # STATUS 应为 Up (healthy)
docker compose logs -f agenthub
docker compose --profile dev up -d  # 额外起控制台 dev server，:5173
docker compose down                 # 停止；数据保留在 agenthub-pgdata 卷
```

与 `make` 路径有两点不同：

* **没有端口清单。** 栈起来后不会打印任何东西。`make ports` 在这里照样能用——它只读运行中的容器，
  所以想看上面的表随时调它。
* **没有构建戳。** `.git` 被 `.dockerignore` 排除，这样构建的镜像在 `/healthz` 里上报的是 `dev` /
  `unknown` 哨兵而不是发布号。要打标就自己传参数（或用 `make compose-build`）：

```bash
VERSION=$(git describe --tags --abbrev=0 --always) \
COMMIT=$(git rev-parse --short=7 HEAD) \
DISTANCE=$(git rev-list --count "$(git describe --tags --abbrev=0)"..HEAD) \
  docker compose build agenthub
```

以下内容对两条路径都适用。

compose 会先拉起 `postgres:16-alpine`，健康检查通过后（pgvector 不需要）再启动 `agenthub`，
后者启动时自动执行迁移与种子。`DATABASE_URL` 由 compose 注入，宿主端口可用 `POSTGRES_PORT` 覆盖。

镜像为三段构建（`oven/bun` 构建控制台 → `golang:1.27-alpine` 编译 → `alpine:3.21` 运行时），
运行时以**非 root 用户**（uid 10001）启动，`/healthz` 为健康检查端点（响应含 `"database":"up"` 字段，
库不可达时返回 503）。容器**无状态**：迁移随二进制内嵌，状态全在 PostgreSQL 卷里。

构建要拉 npm 包与 Go 模块，因此需要出网（`make docker-build`、`make compose-build`，或原生
`docker compose build`）。走代理时用 `BUILD_PROXY` 传入——`make docker-build` 从 `$HTTP_PROXY` 取值
并追加 `--network=host`，容器内的 `127.0.0.1:<port>` 才会指向宿主机代理。compose 构建跑在 bridge
网络上，该地址不存在，因此 `Dockerfile` 对 `bun install` 与 `go mod download` 各做一次「清掉代理变量」
的重试，并回落默认 `GOPROXY` 镜像（`https://goproxy.cn,direct`，可用 `--build-arg GOPROXY=…` 覆盖）；
两次都失败才带提示报错退出，不会静默装作成功。

**已发布镜像。** 发布 Release 会自动构建并推送 `ariacompute/agenthub-agenthub` 到 DockerHub
（`.github/workflows/release.yml`）：`:<release tag>` 对应该次发布，`:latest` 指向最新一次发布。
Release tag 与该 tag 的 commit 会作为 `VERSION` / `COMMIT` 构建参数写进镜像，因此镜像 tag 与
`/healthz` 上报的 `version@commit` 永远一致。该工作流需要两个仓库 secrets：`DOCKERHUB_USERNAME`
与 `DOCKERHUB_TOKEN`。

```bash
docker pull ariacompute/agenthub-agenthub:<tag>  # 例如 v0.3.0
```

**生产部署必须**：

```bash
AGENTHUB_ENV=prod
AGENTHUB_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=<强随机串>
AGENTHUB_COOKIE_SECURE=true                       # 走 HTTPS 时开启
AGENTHUB_PUBLIC_BASE_URL=https://hub.example.com
```

`AGENTHUB_ENV=prod` 且未配置 `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` 时 `config.Validate` **拒绝启动**：
没有管理员的生产实例无法挽回。该账号每次启动幂等 upsert，且**不会覆盖**已有口令，
因此在控制台改过的密码能跨重启保留。配置通过 compose 的 `.env` 或环境变量传入，
`AGENTHUB_HTTP_PORT` 可改宿主端口。

## 桌面端

`desktop/` 是 **Tauri v2** 外壳，覆盖 Windows、macOS（arm64）与 Linux。它**不带后端、
不带数据库**：把服务端托管的同一份控制台嵌进窗口，对着你指定的 agenthub 服务端打开。浏览器里能做的
一切——注册中心、任务追踪、拓扑、管理、SSE 实时流、归档下载——在窗口里同样可用。
发布的 macOS 产物为 arm64；Intel Mac 请用 `make desktop-build` 从源码构建。

```bash
make desktop-install  # cd desktop && bun install
make desktop-dev      # 先构建控制台，再 tauri dev
make desktop-build    # 先构建控制台，再 tauri build
make desktop-test     # cd desktop/src-tauri && cargo test
```

**连接。** 首次启动进入连接页：填写服务端地址（如 `https://hub.example.com`）、
「忽略自签证书」只对你自己网络内的服务端勾选、选一个 13 语之一，然后点**连接**——
外壳会先探 `/healthz` 再落盘。之后托盘常驻四项：**打开控制台 / 连接设置… / 关于 / 退出**；
关闭窗口只是收进托盘，不会退出。配置存于外壳自有的 `config.json`（当前用户配置目录下）。

**原理。** 窗口加载 `http://127.0.0.1:<随机端口>`，那里有一个环回反向代理：托管内嵌的控制台，
并把 `/api/*`、`/a2a`、`/healthz`、`/.well-known/*` 转发到配置的服务端。正因如此，控制台的相对
URL、`HttpOnly` 会话 cookie、SSE 事件流与文件下载才能原样工作——控制台与 Go 服务端**一行都不用改**。
代理会剥掉 `Set-Cookie` 里的 `Secure` 与 `Domain`（明文 http 的环回源存不下它们）、把 cookie 统一
钉到 `Path=/`、转发时不缓冲（否则事件流只在结束时一次性到达），并把指回服务端的重定向改写回环回
源，让 OAuth 往返留在应用内。

**未签名产物。** 发布的二进制未做代码签名，首次启动会被系统拦下：

| 平台 | 绕行方式 |
| --- | --- |
| macOS | *系统设置 → 隐私与安全性 → 仍要打开*；或解压后执行 `xattr -d com.apple.quarantine /Applications/agenthub.app` |
| Windows | SmartScreen 选 *更多信息 → 仍要运行* |
| Linux | 无需绕行：安装 `.deb`，或解开 `.tar.gz` 直接运行（二进制保留可执行位） |

每个产物旁边都附带 `SHA256SUMS.txt`。

## 测试

单元与集成套件：

```bash
make db-up             # 集成用例需要真实 PostgreSQL
make test              # go test -race ./...（集成用例在 DATABASE_URL 未设置时 skip）
make test-integration  # 只跑 integration_test.go
make web-test          # cd web && bunx vitest run
```

对着运行中实例的端到端接口测试——把所有 `curl` 可达的端点都脚本化了：健康与发现、整套 `/api`
控制面（资源 CRUD、导出、任务、span、事件回放）、A2A 方法（send、get、流式、push 通知、
input-required → resume → cancel）、JSON-RPC 错误约定与控制台产物。每条断言打印一行 PASS / FAIL，
有一条失败就以非 0 退出，便于 CI 卡口：

```bash
AGENTHUB_SEED_DEMO=true make up                     # 脚本面向 demo-* 资源
AGENTHUB_SEED_DEMO=true make run                    # 或本地运行
./scripts/test-api.sh                               # 默认 http://127.0.0.1:3500
BASE_URL=https://hub.example.com ./scripts/test-api.sh
AGENTHUB_TEST_EMAIL=me@example.com AGENTHUB_TEST_PASSWORD='…' ./scripts/test-api.sh
MAX_TIME=60 READY_TIMEOUT=60 ./scripts/test-api.sh  # 网络慢 / 冷启动时放宽超时
```

脚本会先登录（实例全新时注册第一个账号），断言无会话时 `/api/*` 被拦，随后遍历用户、角色、
权限矩阵与资源授权，并验证「被授权的资源会从发现文档里消失」。

脚本面向种子资源（`demo-model` / `demo-agent` / `demo-mcp` / `demo-skill` / `demo-cli`），除
`curl` 与 `python3` 外无依赖，跑完会删掉自己创建的资源；若服务始终不应答 `/healthz` 则以 2 退出。

## A2A 端点

| 端点 | 说明 |
| --- | --- |
| `GET /.well-known/agent.json` | Agent Card（`GET /a2a/agent-card` 为兼容路径） |
| `POST /a2a` | JSON-RPC 2.0 单入口 |
| `GET /api/resources` 等 | 控制台控制面（`/api/*`） |
| `GET /healthz` | 健康与模式 |

支持方法：`message/send`、`message/stream`（SSE）、`tasks/get`、`tasks/cancel`、`tasks/resume`、`tasks/pushNotification/set`、`tasks/pushNotification/get`；`AGENTHUB_A2A_LEGACY_METHODS=true` 时额外接受 v0.1 别名 `tasks/send`、`tasks/sendSubscribe`。

流式**没有 `[DONE]` 哨兵**：运行以 `completed / failed / canceled / input-required` 状态事件结束。

## 编排原语

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

## 持久化

| 表 | 内容 |
| --- | --- |
| `resources` | 五类资源条目全字段（`tags/capabilities/auth_schemes/env/metadata` 走 `jsonb`） |
| `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` | 任务主体、输出、span 时间线、`input-required` 挂起问题、回调配置（子表随任务级联删除） |
| `events` | 每次状态/artifact/message/span 帧，支持 SSE 断线重连按 `since=<seq>` 回放 |
| `push_records` | mock notifier 捕获的每次回调（task/url/state/config 快照），按 `AGENTHUB_PUSH_RECORD_LIMIT` 保留 |
| `schema_migrations` | 已执行的迁移文件，启动时跳过 |
| `backup_checkpoints` / `backup_schedules` | 检查点目录与定时策略——两者**不参与**任何导出与恢复，因此操作员在恢复之后仍能看到自己所用的那个归档 |

- 迁移：`migrations/*.sql` 经 `//go:embed` 内嵌，启动时按文件名**单事务**顺序执行；SQL 自身幂等。
- 写入策略：任务/artifact/span **同步写**（状态迁移为条件更新 `WHERE state=?`，防并发丢更新）；
  events 与 push_records **尽力而为**（有界队列 + 异步 writer，失败只告警，**绝不回压任务执行**）。
- 事件重连：`GET /api/tasks/{id}/events?since=<seq>` 回放该 seq 之后的所有帧，再接实时流。

集成用例覆盖：迁移幂等、资源 CRUD 与 jsonb 往返、任务全生命周期与级联删除、事件回放、
push 回调留痕与保留上限、以及完整 A2A run 的落库回读。**无 DB 时自动 skip，不伪造结果**。

## 数据库检查点

控制台的**备份**标签页（`backups:manage`）把整个 agenthub schema 导出为一个 `tar.gz` 归档，
并能把数据库回退到其中任意一个检查点。

- **归档**：单个文件内含 `manifest.json`（表清单、行数、引擎、当时的迁移集合）与数据载荷。
  文件的 sha256 记在目录行里，下载与恢复前都会重新校验——卷上损坏的归档会被拒绝，而不是被交出去。
- **引擎**：运行时镜像里装了 PostgreSQL 客户端就用 `pg_dump -Fc`；否则回落到纯 Go 引擎，直接流式
  `COPY … TO STDOUT` / `COPY … FROM STDIN`。引擎名随归档一起保存；归档只能由写出它的引擎读回。
- **定时**（控制台内编辑并落库）：关闭 / 每 N 分钟 / 每日固定时刻（带时区）/ 五段 cron 表达式。
  调度器重启后会补跑一次；进程死掉留下的 `running` 行在下一次启动时被结清。
- **保留策略**：保留最近 N 个、删除超过 M 天的归档。最新那份永不被清理；恢复前自动打的回滚点
  有 24 小时豁免期——退路不能被清理任务删掉。
- **恢复护栏**，按顺序：操作员重输检查点 id → 校验和核对 → 比对归档迁移集合与当前构建（归档来自
  **更新**的 schema 一律拒绝，更旧则先载入再迁移）→ 先给当前数据库打一个检查点 → 停止接收新任务
  并等待在途任务结束 → 重写各表 → 重新载入内存状态。全程无需重启，任何失败都让数据库保持原状。
- **开关**：`AGENTHUB_BACKUP_DIR` 就是开关。不配置时标签页显示「未启用」并说明原因，其余功能不受
  影响；`docker-compose.yml` 默认如此——`agenthub-backups` 卷已挂载，但留到你配置该变量时才启用。

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
