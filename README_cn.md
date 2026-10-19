# agenthub

[English](README.md) | [中文](README_cn.md)

基于 **A2A（Agent2Agent）协议**的互连平台：把 **model / agent / mcp / skill / CLI** 五类异构资产统一注册、发现、路由与编排，对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：Go 1.27 + `net/http`，单二进制（唯一生产依赖为 GORM）
- 前端：Vue 3 + Vite（bun），**13 种语言**，纯黑/白/灰单色视觉
- 持久化：**PostgreSQL + GORM**（资源、任务、artifact、span、事件流、回调留痕全部落库）
- 五类适配器**默认离线可跑**：无网络、无子进程、无凭据即可启动——model 条目可选接入真实的
  OpenAI 兼容网关

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
make db-up                                  # 起 PostgreSQL（宿主 5434）并等待 ready
make run                                    # 默认 :3500（Open → Migrate → Seed）
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` **必填**：缺失时 `config.Validate` 拒绝启动（不存在无库降级路径）。
本地地址：`postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable`（见 `.env.example`）。

控制台：`make web-dev`（开发）或 `make web-build && make run`（构建产物由 Go 进程托管于 `/`）。

registry 默认为**空**，不灌任何演示数据；置 `AGENTHUB_SEED_DEMO=true` 才会得到下面示例所依赖的
五条 `demo-*` 资源，也可以直接灌 [`example/`](example/README.md) 里那份真实目录。

### 版本打标

`make build` 与 `make run` 通过 `-ldflags -X` 把 `config.Version`（git tag，如 `v0.2.0`）、
`config.Commit`（7 位 commit）、`config.Distance`（tag 之后的提交数）与 `config.Dirty`
写进二进制。`/healthz` 同时返回，Agent Card 带版本；控制台在**侧边栏左下角**渲染
**`version@commit`**（如 `v0.2.0@1a2b3c4`），构建偏离干净 tag 时另起一行显示 `+3 dirty`
之类的偏离说明。未打标的构建上报哨兵值 `dev`，不冒充发布号；镜像经 `VERSION` / `COMMIT` /
`DISTANCE` / `DIRTY` 构建参数传入同一取值（`make docker-build`）。

## 示例

[`example/`](example/README.md) 提供一份可直接灌进运行实例的真实目录：**41 份完整
`registry.Entry` 载荷**加一个可重复执行的装载脚本，五类资产各有 Windows 与 Ubuntu 两套形态；
`deploy-agent.sh` 更进一步，在 WSL2 / Ubuntu 上把其中一个 agent 真跑起来，让目录里登记的
`card_url` 真能应答。

```bash
cp .env.example .env     # 然后把 AGENTHUB_EXAMPLE_ENABLED 改成 true，
                         # 并填 AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh        # 按 uname -s 选平台
./example/load.sh --all  # 两个平台都灌

./example/deploy-agent.sh google-adk start  # 把某个 agent 跑起来，让它登记的 card_url 真能应答
./example/deploy-agent.sh google-adk smoke  # 打印它的 Agent Card，再发一条 message/send
```

| 类别 | 注册了什么 |
| --- | --- |
| model | OpenAI 兼容网关；凭据既不进 JSON 也不进库——`kind_config.model.api_key_env` 只存环境变量名，真实值由服务端在调用时读取 |
| agent | Google ADK、LangGraph、CrewAI、Pi、DeepSeek Harness |
| mcp | 官方 filesystem server，外加 GitHub、Google Drive、Google Docs、Gmail、Slack、Stripe、微信、钉钉、飞书 |
| skill | `anthropics/skills` 与 [skills.sh](https://www.skills.sh) |
| cli | `gh`、`hf`（HuggingFace）、`modelscope` |

两个平台各落一个命名空间（`example-ubuntu` / `example-windows`），因为去重启用键是
`(namespace, name, version)`。装载脚本先 POST、命中去重键再回退 PUT（`PUT` 不是 upsert），
因此可反复执行。

`example/README.md` 另附载荷契约（21 个可用键、`kind` / `transport` / `health` / `status` 四类枚举、
`kind_config`）、每类上游项目的 Windows/Ubuntu 安装与启动命令，以及两个最容易踩的配置项：
网关的 `AGENTHUB_EXAMPLE_API_KEY` 与 CLI 的 `AGENTHUB_CLI_ALLOWLIST`。

`deploy-agent.sh` 刻意与装载脚本分开：它不登录、不碰数据库、也不调 `/probe`，只负责安装、启动与
校验某一个 agent——而这正是 `Try run` 与健康探测要报出「实测值」的前提。

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

若构建卡在 `apk add`（运行时阶段去 `dl-cdn.alpinelinux.org` 取包），换成镜像源即可：

```bash
ALPINE_MIRROR=https://mirrors.aliyun.com docker compose up --build
# 或：ALPINE_MIRROR=https://mirrors.tuna.tsinghua.edu.cn
```

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
一切——注册中心、目录治理、任务追踪、拓扑、管理、SSE 实时流、归档下载——在窗口里同样可用。
发布的 macOS 产物为 arm64；Intel Mac 请用 `make desktop-build` 从源码构建。

外壳只认**路径前缀**：`/api`、`/a2a`、`/healthz`、`/.well-known` 转发给服务端，其余一律回退到
控制台的 `index.html`。因此新增控制面端点或控制台屏幕**不需要改桌面端代码**，只需要重新构建控制台
产物——`make desktop-dev` 与 `make desktop-build` 都会先做这件事。

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
make test-cover        # -coverprofile + go tool cover -func，按包与总计
make web-test          # cd web && bunx vitest run
```

覆盖率按包统计。读数字前要知道两件事：治理规则用**包内假 store** 驱动，因此 `registry` 的
store 分支不需要数据库就能覆盖；而 `internal/store` 只有在设置了 `DATABASE_URL` 时才测得出来——
它的用例是 skip，而不是伪造结果。

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

它还覆盖目录治理：建命名空间 → 在其中注册 → 记录走 `draft → pending → approved`（草稿不得出现在
可发现列表，`draft → approved` 必须被拒）、`(namespace, name, version)` 去重启用键，以及一个上游
注册中心源经离线 fixture 同步两次（首次导入、二次刷新）。

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
| `resources` | 五类 + `custom` 条目全字段，含 `namespace` / `status` / `display_name` / `source_id` / `kind_config`（`tags/capabilities/auth_schemes/env/metadata/kind_config` 走 `jsonb`），`(namespace, name, version)` 唯一 |
| `registries` | registry 命名空间，各带审批策略（`auto_approve`） |
| `registry_sources` | 上游注册中心源与最近一次同步结果（`last_sync_at/status/count/error`） |
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
push 回调留痕与保留上限、完整 A2A run 的落库回读，以及治理用例（命名空间 CRUD、治理列往返、
去重启用键冲突、源 CRUD 与同步留痕、内存 facade 与 store facade 行为一致）。
**无 DB 时自动 skip，不伪造结果**。

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

## AWS Agent Registry 特性对比

对照 [AWS Agent Registry（Amazon Bedrock AgentCore）](https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/registry.html)
与 [Concepts and terminology](https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/registry-concepts.html)
的逐项能力对比。

| | AWS Agent Registry | agenthub |
| --- | --- | --- |
| 本质 | **纯目录/发现服务**，记录的执行交给 Runtime / Gateway | **目录 + 执行一体**：注册的记录立刻可被 A2A 调用 |
| 一级公民 | Registry（目录）→ Registry record（条目） | 命名空间（`registries`）→ 记录（`resources`） |
| 协议 | 以原生 MCP endpoint 暴露目录；按 MCP / A2A 协议 schema 校验 | 对外讲 A2A（`POST /a2a`）；目录以 MCP server 描述与 Agent Card 形式**导出** |

agenthub 一直有 AWS 交给另一个服务的那一半——注册即运行（五类适配器）；缺的是目录那一半：
审批、命名空间、以及从外部注册中心同步。

### 记录类型与校验

| AWS Agent Registry | agenthub |
| --- | --- |
| 两级：Registry（name / description / authorization / approval）→ Registry record | 命名空间 → 记录；命名空间自带审批策略 |
| `recordType` ∈ `AGENT` / `MCP` / `SKILL` / `CUSTOM` | `kind` ∈ `model` / `agent` / `mcp` / `skill` / `cli` / `custom` |
| MCP 记录 = server definition + 工具 schema，按 MCP 协议 schema 校验 | MCP 配置块：transport（stdio / streamable-http / sse-legacy）、command+args 或 URL、tool allowlist、auth schemes、env——按 kind 校验 |
| Agent 记录 = A2A Agent Card，按 Agent Card schema 校验 | Agent 配置块：Agent Card URL、protocolVersion、capabilities、skills、鉴权 |
| Skill 记录 = Name / Description / Package / Repository / markdown | Skill 配置块：version、repository / package、触发词、步骤、工具清单、markdown |
| Custom 记录 = 任意合法 JSON | `custom` 类型：任意合法 JSON schema |
| `name`、可选 `displayName`、`recordVersion` | `name`、`display_name`、`version` |
| 去重键 `name + recordVersion`（registry 内唯一），同名可跨版本 | 去重键 `(namespace, name, version)`（唯一索引保证），同名可跨版本 |
| —（无 Model 与 CLI 两类） | `model`（provider 回退链，可指向真实 OpenAI 兼容 router）与 `cli`（白名单 → 超时 → 截断）为 agenthub 特有 |

### 生命周期与治理

| AWS Agent Registry | agenthub |
| --- | --- |
| Draft → 提交 → Curator **approve / reject**（带反馈）→ 可 **deprecate** | `draft → pending → approved / rejected → deprecated`；非法迁移返回 400 |
| 消费方**只看到 approved** 记录 | `registry.Discoverable` 是列表、MCP 导出、Agent Card 与 A2A 寻址共用的唯一过滤出口 |
| 四角色：Administrator / Publisher / Curator / Consumer | RBAC（owner / 组 / 角色 / 权限）+ 资源级 grant，目录侧另增 `registries:manage` / `registry:curate` / `sources:manage` |
| 命名空间可按类型 / 环境 / 团队拆分 | 多命名空间，各带审批策略与可见性边界 |

### 发现

| AWS Agent Registry | agenthub |
| --- | --- |
| **Hybrid search**：语义 + 关键词 | 仅关键词检索——见下方「明确不做」 |
| **Catalog browsing**：分页 list + 按 recordType 过滤 + `Get` / `BatchGet` | `GET /api/resources?kind=&q=&namespace=&status=`；暂无分页与批量读 |
| **原生 MCP endpoint**（`InvokeRegistryMcp`，任意 MCP 客户端直连） | `GET /api/export/mcp` 只**导出** MCP server 描述，不自建 MCP 服务端 |
| Agent Card | `GET /api/export/agent-card` 与 `/.well-known/agent.json`——每个 approved 记录渲染为一个 advertised skill |

### 授权与同步

| AWS Agent Registry | agenthub |
| --- | --- |
| Inbound：IAM 或 JWT | 会话 cookie → `Principal`；`/api/*` 走 `requirePermission`，A2A 走 `identify` |
| Outbound：**credential provider**（OAuth 凭据或 IAM role），供同步时调用远端端点 | `AuthScheme`（none / bearer）+ `auth_token_env`，**只存环境变量名** |
| **URL-based synchronization**：记录与远端 MCP / A2A 端点保持同步 | 源（mcp / skill）按手动或定时拉取，按 `(name, version)` 合并并记录同步结果；fixture 客户端默认离线，运维显式关闭后才出网 |

### 运维与可观测

| AWS Agent Registry | agenthub |
| --- | --- |
| EventBridge 通知（记录提交审批时） | SSE broker + `events` 表按 `since=<seq>` 回放——能力等价，自有实现 |
| CloudTrail 审计全部 API 调用 | `slog` 结构化日志（不记密钥、不记用户内容、不记向量）；审计面弱于 CloudTrail，**明确不补** |
| AWS Organizations / 跨账号 RAM 共享 | 单实例多租户（principal + 资源级 grant）；绑定 AWS 基础设施，**不复制** |
| VPC / PrivateLink、service-linked role | 自部署单二进制无对等物 |
| Tags（key-value，成本归属 / 访问控制） | `tags []string` + `metadata map`，够用 |
| — | 数据库检查点与定时备份（`internal/backup`）：AWS 侧无对应能力，agenthub **更强** |

### 三张清单

**已有**——注册即可执行的五类适配器（确定性、离线可跑）；A2A 端点与完整任务状态机（含可无限期
挂起并经 `tasks/resume` 恢复的 `input-required`）；事件扇出（SSE + `since` 回放）；push 回调留痕；
目录导出（MCP server 描述 + Agent Card）；认证内核（PBKDF2 口令 / 会话 / OAuth + PKCE）与 RBAC
（组 / 角色 / 权限 / 资源级 grant）；数据库检查点；13 语 i18n 与单色视觉，Web 与桌面端同一份。

**本次补齐**——六类各自的 per-kind 配置与校验；Model router（可指向 OpenAI 兼容网关）；上游注册中心
同步（去重 + 记录同步结果）；审批生命周期（消费方只见 approved）；多 registry 命名空间；
`(namespace, name, version)` 去重启用键；`custom` 记录类型。

**明确不做**——

| 项 | 理由 |
| --- | --- |
| 语义 / hybrid search（向量检索） | 需引入嵌入模型或外部服务，与「零新增生产依赖、离线可跑」冲突；如需，应以「外部检索源」形态接入，而非内嵌模型。 |
| 自建 MCP JSON-RPC 服务端（`InvokeRegistryMcp` 等价物） | agenthub 的对外协议是 A2A；目录已导出 MCP server 描述供接入方自取。做第二套协议服务端会分裂契约。 |
| CloudTrail 等价审计（全量 API 调用留痕） | 与现有 `slog` + `events` 表重叠；应作为独立可观测里程碑，不与目录治理耦合。 |
| Organizations / 跨账号 RAM 共享 / VPC PrivateLink / service-linked role | 绑定 AWS 基础设施，跨云自建无对等物；多租户隔离已由 principal + 资源级 grant 提供。 |
| IAM / JWT 之外的 inbound 授权形态 | 现有会话 + RBAC 已满足自部署场景。 |
| 记录级 outbound OAuth 完整授权码流程 | 本次同步只需 bearer token（存环境变量名）；完整 OAuth 留给「真实适配器」里程碑。 |

### 字段级映射（recordType ↔ Kind）

| AWS recordType | 校验 schema | agenthub kind | per-kind 配置 |
| --- | --- | --- | --- |
| `MCP` | MCP 协议 schema | `mcp` | transport、command+args 或 URL、tool allowlist、auth schemes、env |
| `AGENT` | A2A Agent Card schema | `agent` | Agent Card URL、protocolVersion、capabilities、skills、鉴权 |
| `SKILL` | 描述符（Name / Description / Package / Repository / markdown） | `skill` | version、repository / package、触发词、步骤、工具清单、markdown |
| `CUSTOM` | 任意合法 JSON | `custom` | JSON schema |
| — | — | `model` | provider 模式、endpoint、model_id、api_key_env、回退链 + 权重、超时 |
| — | — | `cli` | 可执行命令、命令白名单、超时、输出截断字节、工作目录、env |

### 剩余差距

1. **中**：语义检索缺失 —— 目录规模上千后关键词检索召回不足；建议以「外部检索源」形态接入，
   而非内嵌嵌入模型。
2. **中**：批量读接口与列表分页 —— 目录变大后列表接口无分页会拖慢控制台。
3. **低**：审计面 —— 缺全量 API 调用留痕，合规场景需要。
4. **低**：定时同步的调度表达式目前手写 5 段 cron，与 EventBridge 规则表达力不对等，但自部署够用。

## 许可证

MIT.

## 工程约定

本仓库遵循 Harness Engineering：

- [`AGENTS.md`](AGENTS.md)：Agent 工程上下文入口与目录索引
- [`requirements.md`](requirements.md)：需求规格（功能边界 / 异常 / 验收，人审后编码）
- [`task.md`](task.md)：实施清单
