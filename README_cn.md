<div align="center">
<a name="readme-top"></a>

# agenthub

**注册一次，让每个智能体都能触达它**

model / agent / mcp / skill / cli 五类资产统一注册、发现与编排，<br/>
对外只暴露一个标准 A2A 端点，任何遵循协议的 Agent 都能调用。

[English](./README.md) · **简体中文** · [发布页][releases] · [文档][docs] · [Issue][issues]

[![][badge-release]][releases] [![][badge-ci]][ci] [![][badge-license]][license]
<br/>
[![][badge-go]][go] [![][badge-postgres]][postgres] [![][badge-vue]][vue] [![][badge-stars]][stars] [![][badge-forks]][forks]
</div>

<details>
<summary><kbd>Table of contents</kbd></summary>

#### TOC

**上手**

- [🧠 概念地图](#-概念地图)
- [📸 快照](#-快照)
- [🚀 运行](#-运行)
  - [🏷 版本打标](#-版本打标)
- [📚 示例](#-示例)

**部署**

- [🛳 部署](#-部署)
  - [🔨 Make](#-make)
  - [🐳 Docker compose](#-docker-compose)
  - [☸️ Kubernetes operator（k8s 控制器部署）](#-kubernetes-operatork8s-控制器部署)

**运行面**

- [🔒 安全加固](#-安全加固)
- [🧪 测试](#-测试)
- [🔗 A2A 端点](#-a2a-端点)
- [🧩 编排原语](#-编排原语)
- [⏰ 调度器（router）](#-调度器router)
  - [🆚 与 dsh superagent router 的能力对照](#-与-dsh-superagent-router-的能力对照)
- [🧱 执行沙盒](#-执行沙盒)
  - [🔍 哪些执行面进了沙盒](#-哪些执行面进了沙盒)
  - [🐋 默认 Docker，缺 Docker 也是一种答案](#-默认-docker缺-docker-也是一种答案)
  - [🆚 与托管运行时对比](#-与托管运行时对比)
- [🚨 错误约定](#-错误约定)
- [🔑 API 密钥](#-api-密钥)

**数据面**

- [🗄 持久化](#-持久化)
- [💾 数据库检查点](#-数据库检查点)
- [📁 共享文件](#-共享文件)

**对比与治理**

- [🏛 AWS Agent Registry 特性对比](#-aws-agent-registry-特性对比)
  - [🧾 记录类型与校验](#-记录类型与校验)
  - [♻️ 生命周期与治理](#-生命周期与治理)
  - [🔎 发现](#-发现)
  - [🔐 授权与同步](#-授权与同步)
  - [📊 运维与可观测](#-运维与可观测)
  - [📋 三张清单](#-三张清单)
  - [🔁 字段级映射（recordType ↔ Kind）](#-字段级映射recordtype--kind)
  - [🧭 剩余差距](#-剩余差距)
- [📝 许可证](#-许可证)
- [🤝 工程约定](#-工程约定)

</details>

基于 **A2A（Agent2Agent）协议**的互连平台：把 **model / agent / mcp / skill / CLI** 五类异构资产统一注册、发现、路由与编排，对外以标准 A2A 端点被任何遵循协议的 Agent 调用。

- 后端：Go 1.27 + `net/http`，单二进制（生产依赖为 GORM + pgx）
- 前端：Vue 3 + Vite（bun），**13 种语言**，纯黑/白/灰单色视觉
- 持久化：**PostgreSQL + GORM**（资源、任务、artifact、span、事件流、回调留痕全部落库）
- 五类适配器**默认离线可跑**：无网络、无子进程、无凭据即可启动——model 条目可选接入真实的
  OpenAI 兼容网关

## 🧠 概念地图

| 概念 | 角色 | 在 agenthub 中 |
| --- | --- | --- |
| Model | 大脑 | `kind=model`，统一 `Generate/Stream` 接口 + provider 回退链 |
| Agent | 数字员工 | `kind=agent`，目标驱动、可挂起等待人工输入 |
| MCP | 万能转接头 | `kind=mcp`，只做协议转换、不碰业务逻辑；`transport` 仅作描述字段 |
| Skill | 岗位手册 | `kind=skill`，触发条件 + 步骤 + 工具清单的版本化流程资产 |
| CLI | 对讲机 | `kind=cli`，白名单 → 超时 → 截断 → 结构化 JSON 回执 |

一次调用：用户 → Agent → Model 规划 → 加载 Skill → 经 MCP 调用老系统 → 结果沿原路返回。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📸 快照

注册中心屏 —— 你可以在一个界面里注册、发现并治理全部五类资产：

![agenthub 控制台 —— 注册中心](screenshots/en/02_Registry.png)

<details>
<summary><kbd>全部 10 个界面 · English / 简体中文</kbd></summary>

| # | 屏幕 | English | 简体中文 |
| --- | --- | --- | --- |
| 01 | 登录 | [PNG](screenshots/en/01_Sign_in.png) | [PNG](screenshots/zh/01_登录.png) |
| 02 | 注册中心 | [PNG](screenshots/en/02_Registry.png) | [PNG](screenshots/zh/02_注册中心.png) |
| 03 | 任务追踪 | [PNG](screenshots/en/03_Task_trace.png) | [PNG](screenshots/zh/03_任务追踪.png) |
| 04 | 拓扑视图 | [PNG](screenshots/en/04_Topology.png) | [PNG](screenshots/zh/04_拓扑视图.png) |
| 05 | 命名空间 | [PNG](screenshots/en/05_Namespaces.png) | [PNG](screenshots/zh/05_命名空间.png) |
| 06 | 上游注册中心 | [PNG](screenshots/en/06_Sources.png) | [PNG](screenshots/zh/06_上游注册中心.png) |
| 07 | 演练场 | [PNG](screenshots/en/07_Playground.png) | [PNG](screenshots/zh/07_演练场.png) |
| 08 | 审计轨迹 | [PNG](screenshots/en/08_Audit_trail.png) | [PNG](screenshots/zh/08_审计轨迹.png) |
| 09 | 管理 | [PNG](screenshots/en/09_Administration.png) | [PNG](screenshots/zh/09_管理.png) |
| 10 | 文档 | [PNG](screenshots/en/10_Docs.png) | [PNG](screenshots/zh/10_文档.png) |

</details>

截图由 `screenshots/capture.mjs`（Playwright + Chromium）对着运行实例生成。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🚀 运行

```bash
cp .env.example .env                        # 本地开发：复制环境变量模板，按需修改取值
# cp .env.sec.example .env                  # 加固生产：复制安全模板，再把每个 <...> 占位符换成真实密钥
make db-up                                  # 起 PostgreSQL（宿主 5434）并等待 ready
make run                                    # 默认 :3500（Open → Migrate → Seed）
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` **必填**：缺失时 `config.Validate` 拒绝启动（不存在无库降级路径）。
两处 DSN 的主机不同，别混用：

- `.env`（复制自 `.env.example`）填**容器内**地址
  `postgres://agenthub:agenthub@postgres:5434/agenthub`：compose 会把它注入 `agenthub`
  容器，容器里的 `localhost` 是容器自己，不是数据库。
- `make run` 会内联覆盖成 `postgres://agenthub:agenthub@localhost:5434/agenthub`，供宿主
  进程使用。`make db-up` 现在同时等容器内健康检查与**宿主端口可达**，端口没发布会立刻
  报错并打印诊断，不会再拖到 `go run` 才抛 `connection refused`。

若是加固的生产部署而非本地开发，从加固模板起步更省事：`cp .env.sec.example .env`（见
[安全加固](#-安全加固)）。它已预先套用下面全部生产防护项，并仅带占位密钥，需你先替换才能启动。

控制台：`make web-dev`（开发）或 `make web-build && make run`（构建产物由 Go 进程托管于 `/`）。

文档：控制台自带 **`/docs`** 屏，说明核心概念、7 个 A2A 方法与全部 144 条 HTTP 路由。同一份内容
由 `make docs-md` 渲染到 [`docs/docs_cn.md`](docs/docs_cn.md)；该文件是**生成的**——来源是文档屏
所读的两个模块（`web/src/lib/docs-content.js` 与 `web/src/lib/copy/docs-copy.js`），要改就改这两处
再重跑，不要直接编辑 markdown。

registry 默认为**空**，不灌任何演示数据；置 `AGENTHUB_SEED_DEMO=true` 才会得到下面示例所依赖的
五条 `demo-*` 资源，也可以直接灌 [`example/`](example/README.md) 里那份真实目录。

### 🏷 版本打标

`make build` 与 `make run` 通过 `-ldflags -X` 把 `config.Version`（git tag，如 `v0.2.0`）、
`config.Commit`（7 位 commit）、`config.Distance`（tag 之后的提交数）与 `config.Dirty`
写进二进制。`/healthz` 同时返回，Agent Card 带版本；控制台在**侧边栏左下角**渲染
**`version@commit`**（如 `v0.2.0@1a2b3c4`），构建偏离干净 tag 时另起一行显示 `+3 dirty`
之类的偏离说明。未打标的构建上报哨兵值 `dev`，不冒充发布号；镜像经 `VERSION` / `COMMIT` /
`DISTANCE` / `DIRTY` 构建参数传入同一取值（`make docker-build`）。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📚 示例

[`example/`](example/README.md) 提供一份可直接灌进运行实例的真实目录：**41 份完整
`registry.Entry` 载荷**加一个可重复执行的装载脚本，五类资产各有 Windows 与 Ubuntu 两套形态；
四个 `deploy-*.sh` 脚本更进一步，在 WSL2 / Ubuntu 上各把一项资产落地：A2A agent、
Streamable HTTP MCP server、skill 目录或 CLI 命令。

```bash
cp .env.example .env     # 然后把 AGENTHUB_EXAMPLE_ENABLED 改成 true，
                         # 并填 AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh        # 按 uname -s 选平台
./example/load.sh --all  # 两个平台都灌

./example/deploy-agent.sh google-adk start      # 把某个 agent 跑起来，让它登记的 card_url 真能应答
./example/deploy-agent.sh google-adk smoke      # 打印它的 Agent Card，再发一条 message/send
./example/deploy-mcp.sh filesystem start        # 后台起一个 Streamable HTTP MCP server
./example/deploy-skill.sh anthropics-pdf start  # 按 file:// endpoint 落地 skill 目录
./example/deploy-cli.sh gh start                # 检查 PATH 与 AGENTHUB_CLI_ALLOWLIST
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
`kind_config`）、每类上游项目的 Windows/Ubuntu 安装与启动命令、四个部署脚本各自的职责，
以及两个最容易踩的配置项：网关的 `AGENTHUB_EXAMPLE_API_KEY` 与 CLI 的 `AGENTHUB_CLI_ALLOWLIST`。

四个部署脚本都刻意与装载脚本分开：不登录、不碰数据库、也不调 `/probe`。`deploy-agent.sh` 只负责
安装、启动与校验某一个 agent——而这正是 `Try run` 与健康探测要报出「实测值」的前提；
`deploy-mcp.sh` 用 supergateway 把目录里的 stdio 命令桥接成无状态 Streamable HTTP 端点
（`127.0.0.1:8002/mcp/<server>`）并经 HTTP 核对工具列表，`deploy-skill.sh` 按 `file://` 落地目录并读取
`SKILL.md`，`deploy-cli.sh` 先检查 PATH 与 `AGENTHUB_CLI_ALLOWLIST` 再跑只读探测。
服务端 mcp / skill / cli connector 仍是 `TODO(real)`，所以后三者只校验本地资产。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🛳 部署

下面两条路径都能起**数据库 + 服务**——控制台已烘焙进镜像、由 Go 进程托管于 `/`。
想要起完之后的那份端口表，就用 `make`；只要 Docker 本身，用原生 compose 即可。

### 🔨 Make

```bash
cp .env.example .env  # 本地开发：复制环境变量模板，按需修改取值
# cp .env.sec.example .env  # 加固生产：复制安全模板，再把每个 <...> 占位符换成真实密钥
make up               # docker compose up -d --build（从源码重建镜像后），完成后打印端口清单
make ports            # 对已运行的栈重打端口清单
make down             # 停止；数据保留在 agenthub-pgdata 卷
```

compose 没有 after-up 钩子，所以 `make up` 在栈健康后立刻打印端口清单，`make ports` 之后可随时重打：
哪个宿主端口在应答、分别是做什么的：

```text
$ make ports

agenthub listening ports

  SERVICE            HOST -> CTR      STATE      PURPOSE
  agenthub-server    3500 -> 3500     healthy    the only HTTP listener: console + A2A + control plane + healthz
  postgres           5434 -> 5434     healthy    the only state: resources, tasks, spans, events, push records

  ENDPOINT                                            USE
  http://127.0.0.1:3500/                              Vue 3 console (bundle baked into the image)
  http://127.0.0.1:3500/.well-known/agent.json        A2A Agent Card (discovery)
  POST http://127.0.0.1:3500/a2a                      A2A JSON-RPC: message/send, message/stream, tasks/*
  http://127.0.0.1:3500/api/*                         control plane: resources, tasks, events, push records
  http://127.0.0.1:3500/healthz                       health probe, also the container healthcheck
  postgres://agenthub@localhost:5434/agenthub         host DSN (inside compose it is postgres:5434)
```

上面这些端点在容器内同属**一个监听端口**（恒为 3500），宿主侧映射只有 compose 知道，所以要这张表。
服务自身启动时也会在日志里逐条打印同样的端点清单，因此 `docker compose up`（前台）或
`docker compose logs -f agenthub` 也能看到用途说明。

数据库还额外挂在专用的 `hostpg` 网络：Docker 不会给**只连 `internal` 网络**的容器发布宿主端口，
缺了它 `ports: 5434:5434` 就是空配置，`make run` 必然连不上。`hostpg` 只有 postgres 一个成员，
且关闭了 masquerade，因此控制台侧仍碰不到数据库，数据库也没多出一条出网路径。

### 🐳 Docker compose

```bash
docker compose up -d --build                # 从源码重建镜像后，再起 postgres + agenthub，:3500 / :5434
docker compose up -d --force-recreate       # 即便 compose 认为无变化也重建容器——例如改了 .env / env_file 后，确保新环境变量进入容器
docker compose ps                           # STATUS 应为 Up (healthy)
docker compose logs -f agenthub
docker compose --profile dev up -d --build  # 从源码重建；额外起控制台 dev server，:5173
docker compose down                         # 停止；数据保留在 agenthub-pgdata 卷
```

`--force-recreate` 会无条件重建容器。改了 `.env`（栈通过 `env_file` 读取配置）后用它：单独的 `up -d` 在仅文件内容变化、compose 判断“无变更”时可能跳过重建，导致容器内仍是旧环境变量——这正是“明明改了 `.env`，应用却还是读旧值”的常见原因。

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

**生产部署必须**：一份覆盖下面全部变量（外加 SSRF 护航的同步与签名的 A2A push 设置）的加固模板已随仓库提供，名为 `.env.sec.example`。用 `cp .env.sec.example .env` 复制，再把每个 `<...>` 占位符换成带外生成的强随机密钥（如 `openssl rand -base64 48`）。该模板在占位符替换前**故意无效**，因此加固实例不可能带着提交进仓的密钥启动。

```bash
AGENTHUB_ENV=prod
AGENTHUB_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=<强随机串>
AGENTHUB_COOKIE_SECURE=true                       # 走 HTTPS 时开启
AGENTHUB_PUBLIC_BASE_URL=https://hub.example.com
AGENTHUB_ENCRYPTION_KEY=<32+ 字节随机密钥>         # 生产必填；支撑静态加密与备份静态加密
# AGENTHUB_PUSH_SECRET=<16+ 字节随机密钥>          # 可选：签名 + SSRF 护航的 A2A push 回调
# AGENTHUB_DB_MAX_OPEN_CONNS=25 AGENTHUB_DB_MAX_IDLE_CONNS=10 AGENTHUB_DB_CONN_MAX_LIFETIME=30m  # 连接池调优
```

`AGENTHUB_ENV=prod` 且未配置 `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` 时 `config.Validate` **拒绝启动**：
没有管理员的生产实例无法挽回。该账号每次启动幂等 upsert，且**不会覆盖**已有口令，
因此在控制台改过的密码能跨重启保留。配置通过 compose 的 `.env` 或环境变量传入，
`AGENTHUB_HTTP_PORT` 可改宿主端口。

### ☸️ Kubernetes operator（k8s 控制器部署）

要在 Kubernetes 上以声明式、GitOps 友好的方式部署，可用 `operator/` 下的 controller-runtime
控制器。用 Helm 装好控制器与 `AgentHub` CRD，再 apply 一份声明式 `AgentHub` 自定义资源，控制器即把它
调和成运行中的 agenthub-server Deployment。PostgreSQL 仍由外部提供，连接串经 CR 注入。

- `operator/README.md` —— 安装步骤、完整的 `AgentHub` CRD 字段清单、外部数据库要求、生产前置条件
  （`BOOTSTRAP_ADMIN_EMAIL` + `ENCRYPTION_KEY` ≥ 32 字节）与多副本迁移说明（迁移在
  `pg_advisory_xact_lock` 下串行，无需先单副本建库再扩容）。
- `operator/deploy/agenthub-operator/` —— Helm chart（控制器 Deployment + 随包 CRD + 命名空间级 RBAC）。
- `operator/examples/agenthub-sample.yaml` —— 一份可直接 apply 的 `AgentHub` CR。

```bash
helm install agenthub-operator ./operator/deploy/agenthub-operator --namespace agenthub-operator --create-namespace
kubectl apply -f operator/examples/agenthub-sample.yaml -n agenthub-operator
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔒 安全加固

**传输 / 边缘**：agenthub 是单 Go 二进制，安全响应头改由中间件发出（`internal/middleware`）：
`X-Content-Type-Options`、`Referrer-Policy`、`COOP`/`CORP`，以及灰度期的
`Content-Security-Policy-Report-Only`。HSTS 仅在 `AGENTHUB_ENV=prod` 或请求已走 TLS（`r.TLS` 或
`X-Forwarded-Proto: https`）时发出。**生产须在 agenthub 之前前置 TLS 终止反代**（Caddy/nginx），
二进制本身不终止 TLS。

**认证与限流**：暴力破解防护——**5 次**失败登录锁定 **15 分钟**，成功登录清零计数与锁定；不存在的账户
一律返回相同错误，无法被枚举（`migrations/011_login_protection.sql`）。按 IP 限流：认证端点 **10 次/分钟/IP**，
`/api` 与 `/a2a` 其余 **200 次/分钟/IP**（`429` + `Retry-After`）。**环回客户端**（agentbuddy 桌面外壳与本地
开发，均识别为 `127.0.0.1`/`::1`）豁免，因为其流量会塌缩进同一个共享桶，且该防御对仅本机可达的地址并不适用。CSRF：变更型请求要求 `Origin` 同源
（环回与非浏览器客户端如桌面代理、脚本豁免），否则 `403`；控制台同源 fetch + cookie，不受影响。
CLI 资源执行的是**记录里声明的**命令，绝不取自提示词，白名单匹配为精确命令名（不匹配前缀、不接受路径）；
调用方传参只有在记录的 `args_policy` 允许时才放行。`internal/adapter/cli_test.go` 把这两条规则钉成回归用例——
白名单里的二进制不许再变回 shell。
审计行只带调用者、IP、HTTP 方法、脱敏后的路径、结果与哈希——**绝不含**密码、令牌、Cookie 或请求/响应体
（见上文「全 API 审计」）。

**A2A 回调与上游同步（SSRF）**：push 通知现真实 POST 到调用方提供的 `URL`（`HTTPNotifier` 替换旧记录型
mock）。目标仅 https 且绝不解析到私网/环回/链路本地地址（`internal/netutil`），并在配置 `AGENTHUB_PUSH_SECRET`
时对载荷做 HMAC-SHA256 签名（时间戳 + nonce + body），供接收方验证真实性、新鲜度与重放；`(taskId, state)`
1 小时幂等去重。上游 registry 同步客户端复用同一护航客户端。

**密钥与数据**：`AGENTHUB_ENCRYPTION_KEY`（生产**≥ 32 字节**，否则拒绝启动）隔离静态加密与系统其余部分；
缺失时回退到会话密钥并告警。API key 以 `token_hash` 哈希存储，OAuth 客户端密钥为环境变量引用、绝不入库——
注入审计确认全部 SQL 已参数化。

**数据库**：运行期须用最小权限角色，且**该角色不得是表属主、不得有 `BYPASSRLS`**——任一条件成立都会
静默跳过隔离策略。`scripts/provision-db-roles.sql` 生成三个角色（`migrator` 拥有 schema 并执行 DDL、
`app` 只有 DML、`readonly` 只有 SELECT），且末尾断言：十五张受保护表缺
`ENABLE`+`FORCE ROW LEVEL SECURITY` 就 `RAISE EXCEPTION`。DDL 用 `AGENTHUB_MIGRATE_DATABASE_URL`
指向 migrator；未设置时回退到 `DATABASE_URL` 并在启动日志告警，因为单角色部署的边界更弱。
连接池可经 `AGENTHUB_DB_MAX_OPEN_CONNS` / `_MAX_IDLE_CONNS` / `_CONN_MAX_LIFETIME` 调优。

**数据隔离（行级安全）**：任务及其 artifact/span/pending/push 行与事件、知识块，调度目标、通道、
agent 计算机，以及线程与其消息、通道投递明细、共享文件都带 `owner_id`。PostgreSQL 策略（任务管道在
`migrations/026`，`028` 覆盖三张 owned 面，`029` 覆盖投递明细，`030` 覆盖线程与线程消息，`031` 覆盖
共享文件，合计**十五张**）按三个事务内设置判定可见性——`app.owner_id`（登录用户）、
`app.bypass`（管理员）、`app.system`（进程内路径：任务状态机、事件扇出、cron 扫描、seed、迁移、
备份、审计写入）。未设置的设置读作 NULL，比较一律为假，因此**忘记注入身份的请求什么也看不到**，
而不是看到全部。每次仓储调用开一个短事务（`internal/store.scoped`）并用 `set_config(..., true)`
注入身份，请求不持连接。

应用层把同一条规则写进了端口签名：按 id 取行是 `Get(ctx, id, ownerID)`（`Update` / `Delete`
同样带 owner），别人的行一律回 `404` 而不是 `403`——不泄露存在性。唯一的「无归属读」是被显式命名的
`channels.GetUnscoped`，只有入站 webhook 用：它必须先读出行才知道用哪个密钥验签，此时还没有任何会话；
调用时带进程身份。该端点的验签、重放窗与去重语义未变。

`AGENTHUB_RLS=enforce` 是默认值。`AGENTHUB_RLS=off` 让全部策略通过（在 DSN 追加
`options=-c app.system=on`）并打印醒目告警；`AGENTHUB_ENV=prod` 下会被拒绝，除非同时设
`AGENTHUB_RLS_ACK=1`。

资源可见性是**显式**的：`resources.visibility` 默认 `restricted`，需要时标 `public`。判定为
`public || 有 grant`，因此既无 grant 又未标 `public` 的资源既不可见也不可调用（匿名 A2A 对端同样如此）。
**这是破坏性变更**——升级步骤与三种恢复方式见 `requirements-isolation.md` §3.1；演示数据
（`AGENTHUB_SEED_DEMO=true`）以 `public` 写入。

**全 API 审计**：每次 `/api` 与 `/a2a` 调用向 `audit_log` 追加一行，字段沿用 CloudTrail 词汇：
`event_id`/`ts`/`source`/`action`/`read_only`/`actor_*`/`ip`/`method`/`path`/`resource_*`/`status`/
`error_code`/`latency_ms`/`user_agent`。路径脱敏（query 值置 `REDACTED`），绝不存请求体与响应体。
该表被两道关锁成只追加：触发器对 `UPDATE`/`DELETE`/`TRUNCATE` 一律拒绝（**包括表属主**），运行角色只被
授予 `INSERT`/`SELECT`。行之间哈希链接（`hash = sha256(prev_hash ‖ canonical(event))`），篡改或删除任一行
都会断开其后所有链接，`GET /api/audit/verify` 会指出第一个断点。单写者串行维护链：有界队列 + 批量插入
（默认 8192 / 64 条 / 500 ms），队列满则丢帧并计数告警，**绝不阻塞业务请求**。

| 变量 | 默认 | 含义 |
| --- | --- | --- |
| `AGENTHUB_AUDIT_ENABLED` | `true` | 是否记录审计 |
| `AGENTHUB_AUDIT_RETENTION` | `2160h` | 单行最长存活时间；清理动作本身也留痕 |
| `AGENTHUB_AUDIT_QUEUE` | `8192` | 有界队列深度（超出部分计数后丢弃） |
| `AGENTHUB_AUDIT_BATCH` | `64` | 每次插入行数（`1..AGENTHUB_AUDIT_QUEUE`） |
| `AGENTHUB_RLS` | `enforce` | `off` 放开全部策略（仅开发；prod 下需 `AGENTHUB_RLS_ACK=1`） |
| `AGENTHUB_MIGRATE_DATABASE_URL` | 未设 | DDL 连接；缺省回退 `DATABASE_URL` 并告警 |

读取审计需要独立权限 `audit:read`：`GET /api/audit`（过滤 `from`、`to`、`actor`、`action`、`resource`、
`source`、`outcome`、`q`、`before`、`limit`）、`GET /api/audit/{id}`、`GET /api/audit/verify?from=&to=`。
控制台对应 **审计** 页。

审计行只带调用者、IP、HTTP 方法、脱敏后的路径、结果与哈希——**绝不含**密码、令牌、Cookie 或请求/响应体。

**CLI 资源（回归加固）**：执行的是**记录里声明的命令**，绝不取提示词里的词；白名单按命令名精确匹配
（不认前缀、不认路径）；调用者参数默认拒绝，除非记录的 `args_policy` 放行。
`internal/adapter/cli_test.go` 把这两条钉成回归用例——被允许的二进制不能再退化成一个 shell。

**备份**：配置 `AGENTHUB_ENCRYPTION_KEY` 时检查点归档以 AES-256-GCM 静态加密为 `<file>.tar.gz.enc` 并写
`<file>.enc.sha256` 清单，明文绝不落盘。定期用 `scripts/backup-restore-drill.sh` 验证可恢复性。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧪 测试

单元与集成套件：

```bash
make db-up             # 集成用例需要真实 PostgreSQL
make db-migrate        # 只建 schema；两个 test 目标都会先跑它
make test              # go test -race ./...（集成用例在 DATABASE_URL 未设置时 skip）
make test-integration  # 只跑 integration_test.go
make test-cover        # -coverprofile + go tool cover -func，按包与总计
make web-test          # cd web && bunx vitest run
```

覆盖率按包统计。读数字前要知道两件事：治理规则用**包内假 store** 驱动，因此 `registry` 的
store 分支不需要数据库就能覆盖；而 `internal/store` 只有在设置了 `DATABASE_URL` 时才测得出来——
它的用例是 skip，而不是伪造结果。

对着运行中实例的端到端测试——`e2e/` 下两套彼此独立的「用户视角」套件（完整开关清单与报告格式见
[`e2e/README.md`](e2e/README.md)）。接口套件是声明式用例表，覆盖**全部 144 条文档端点**：发现、
认证与会话、资源与治理、A2A 各方法（含 HITL park → resume / cancel）、流式与 `since` 回放、任务、
权限与 API 密钥、渠道/连接器、线程/语音、文件、沙箱、调度/计算机/知识、路由、审计与检查点；界面套件
用 Playwright 跑六段真实控制台旅程（登录、登记资源、试跑、任务追踪、治理审批、后台管理）。两者都只
通过 `BASE_URL` 指向已启动的服务，只测不起停，自动回收全部自建数据，并产出 JSON + Markdown 报告。

```bash
# 1. 服务端：所有 AGENTHUB_* 开关只在启动时读取一次，必须在启动/重启实例之前设置，
#    不能加在 `make e2e` 前面。
AGENTHUB_SEED_DEMO=true \
AGENTHUB_CHANNELS_ENABLED=true \
AGENTHUB_ROUTER_ENABLED=true AGENTHUB_ROUTER_RESOURCE=demo-mcp \
AGENTHUB_CLI_ALLOWLIST=git,ls,cat,echo,date,aria-memo,gh \
AGENTHUB_BACKUP_DIR=/var/lib/agenthub/backups AGENTHUB_BACKUP_MODE=off \
make run  # 宿主机运行 Go（WSL2 / 本地开发）
# 或：`make up` 走 compose；两种启动方式也都会从 .env 读取同样的键

# 2. 客户端：两套套件只通过 BASE_URL 访问服务。
cd e2e && bun install && cd ..  # 首次运行；测试依赖隔离安装
make e2e-api                    # 接口套件（./scripts/test-api.sh 为转发器）
make e2e-ui                     # 界面旅程（需 bun x playwright install chromium）
make e2e                        # 两套都跑
make e2e-coverage               # 仅端点覆盖度守卫，不发请求
```

`AGENTHUB_SEED_DEMO=true` 的写法同样适用于其余可选模块：它们是**服务端启动期**配置，不是测试脚本的
开关，把变量加在 `make e2e` 前面不会改变服务端行为。`make run`（宿主机运行 Go，WSL2 常用）通过
`config.LoadDotEnv()` 读 `.env`，且只补未设置的键，因此 `VAR=… make run` 前缀优先；改完 `.env` 要重启
该 Go 进程。`make up` 则由 compose 的 `environment:` 块转发同一批键，并用 `--force-recreate` 使其生效。
全部服务端变量见 `docker-compose.yml`、`.env.example` 与 `.env.sec.example`。

| 服务端变量 | 默认值 | 为什么完整运行需要设置 |
| --- | --- | --- |
| `AGENTHUB_SEED_DEMO=true` | `false` | 灌入五个 `demo-*` 资源，作为 A2A / 路由的目标；不设置时套件会自建等价资源 |
| `AGENTHUB_CHANNELS_ENABLED=true` | `false` | 否则 `/api/channels*` 返回 `503 channel_disabled`，整组 SKIP |
| `AGENTHUB_ROUTER_ENABLED=true` | `false` | 否则 `/api/router/*` 与 `/api/schedules/{id}/run` 返回 `503 feature_unavailable` |
| `AGENTHUB_ROUTER_RESOURCE=demo-mcp` | 空 | 兜底执行者；未设置时工单可能返回 `409 router_resource_unset` |
| `AGENTHUB_CLI_ALLOWLIST=…,gh` | `git,ls,cat,echo,date,aria-memo` | 整串 `EqualFold` 匹配；种子里的 `demo-cli` 用 `git`（已在默认列表），但 `example/` 的 CLI 资源不在其中——补上 `gh`（其余再加 `hf` / `modelscope`），否则 `deploy-cli.sh gh start` 会拒绝启动，CLI 探测也判 `degraded` |
| `AGENTHUB_BACKUP_DIR=/var/lib/agenthub/backups` | 空 | **这才是**检查点功能的开关（compose 会把卷挂到该路径；`make run` 下请换成宿主机可写路径） |
| `AGENTHUB_BACKUP_MODE=off\|daily\|interval\|cron` | `off` | 只是调度模式；`off` 仍允许套件手工打点 |
| `AGENTHUB_FILES_STORE=local`（配合 `AGENTHUB_FILES_DIR`） | `off` | 否则共享文件路由返回 `503 files_disabled`；compose 需为该路径额外挂载可写卷 |
| `AGENTHUB_SPEECH_MODE=mock` | `mock` | 当前唯一可用的语音后端，其它值会让线程音频 SKIP |
| `AGENTHUB_SANDBOX_DEFAULT_IMAGE=debian:bookworm-slim` | `debian:bookworm-slim` | `POST /api/computers` 会用 `docker run --pull missing` 申请容器；先预拉镜像（`docker pull debian:bookworm-slim`），否则该请求可能超过 `MAX_TIME`。设 `AGENTHUB_SANDBOX_ENABLED=false` 可完全跳过租约（沙箱组将 SKIP） |

凭据来自 `AGENTHUB_TEST_EMAIL` / `AGENTHUB_TEST_PASSWORD`（bootstrap 管理员）或
`AGENTHUB_API_KEY=ahk_…`（长效 bearer 密钥），`BASE_URL` 默认 `http://127.0.0.1:3500`；
`MAX_TIME` / `READY_TIMEOUT` 控制单请求与就绪等待上限。

实例上未接线的能力一律记为 SKIP，绝不伪造结果；需要第三方服务、真实数据库恢复、S3、签名 webhook、
push 接收端或真实容器执行的用例，默认 SKIP，只有显式设置对应的 `AGENTHUB_E2E_*` 开关才执行。
内置守卫会把用例表与 `docs/docs.md` 做差集，新增端点漏写用例即失败。两套套件与 Go 单测、
web Vitest 基线完全隔离。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔗 A2A 端点

| 端点 | 说明 |
| --- | --- |
| `GET /.well-known/agent.json` | Agent Card（`GET /a2a/agent-card` 为兼容路径） |
| `POST /a2a` | JSON-RPC 2.0 单入口 |
| `GET /api/resources` 等 | 控制台控制面（`/api/*`） |
| `GET /healthz` | 健康与模式 |

支持方法：`message/send`、`message/stream`（SSE）、`tasks/get`、`tasks/cancel`、`tasks/resume`、`tasks/pushNotification/set`、`tasks/pushNotification/get`；`AGENTHUB_A2A_LEGACY_METHODS=true` 时额外接受 v0.1 别名 `tasks/send`、`tasks/sendSubscribe`。

流式**没有 `[DONE]` 哨兵**：运行以 `completed / failed / canceled / input-required` 状态事件结束。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧩 编排原语

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

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## ⏰ 调度器（router）

`internal/router` 把平台从「注册即被调用」的被动目录升级为双层调度：**Router 层**判断一个事件
值不值得开工，**Worker 层**把这件事交给既有的任务状态机执行。`hub` 的公开语义零改动——一张工单会变成
一次 `message/send`（或 `tasks/resume`），因此 span、事件流、push 回调与 HITL 挂起全部照旧生效。

四类来源汇入同一队列：cron 表达式到期、协作线程里的一条消息、渠道 webhook 入站、以及手动注入
（`POST /api/router/triggers`）。每条触发都带去重键，重复投递的 webhook 只会得到
`200 {"duplicate":true}`，不会开出第二张工单。

过滤分两级：

- **程序层**——按来源的策略（`off | candidate | request_only`）叠加发起者白名单、`@提及`、关键词、
  静默时段与自回显抑制。它离线、确定，能筛掉绝大部分流量。
- **语义层**——一拍内放行的触发合并成**一次批量调用**判定（`accept / attach / stop / ignore`）：
  `AGENTHUB_ROUTER_ASSESS_MODE=model` 时问询一个 model 资源，否则只跑规则。触发正文一律按
  不可信数据对待，回答只解析白名单枚举，非法或失败的输出逐条回退到规则结论——绝不因此「放行」。

落在已有人订阅的线程上的触发不再决策，直接派给订阅者；人类消息优先于评估结论。执行者可以把一条触发
交回队列，但只允许一次；工单结束时未读事件回流评估，而不是被丢弃。

节拍固定（`AGENTHUB_ROUTER_INTERVAL`，默认 `2s`）且单飞：节拍进行中到达的请求会并入紧随其后的那一拍。
超出 `AGENTHUB_ROUTER_MAX_TICKETS`（默认 `8`）的工单留到下一拍，不额外开 goroutine；失败的运行按退避
重试，上限 `AGENTHUB_ROUTER_MAX_ATTEMPTS`（默认 `3`）。

调度器**默认关闭**——`AGENTHUB_ROUTER_ENABLED=true` 才开启，因为一个还没想好「触发该落到哪个执行者」
的实例不该自己开工。关闭期间 `/api/router/*` 与 `/api/schedules/{id}/run` 返回
`503 feature_unavailable`，线程消息保持原有直发 `Hub.Send` 的路径。`AGENTHUB_ROUTER_RESOURCE` 指定兜底
执行者；cron 表达式复用检查点调度器已有的五段解析器，因此「下次到期」在保存时算出、每次触发后再算一次。

### 🆚 与 dsh superagent router 的能力对照

| 能力 | agenthub |
| --- | --- |
| 统一入口 + 幂等键 | 有——四类来源，按 `(source, dedupe_key)` 去重，重复投递返回 `200 {duplicate:true}` |
| 快速规则（来源模式 / 白名单 / `@提及` / 关键词 / 静默时段） | 有——`internal/router/filter.go`，离线且确定 |
| 语义层「一拍一次批量调用」+ 封闭结论词表 | 有——`accept / attach / stop / ignore`，非法或失败逐条回退规则结论，绝不放行 |
| 会话订阅直通 | 有——人类消息优先于评估结论 |
| 每条触发仅可交回一次 | 有——第二次返回 `409 router_handback_exhausted` |
| 工单结束时未读事件回流评估 | 有 |
| 固定节拍 / 单飞 / 请求合并 | 有——默认 `2s`，同一刻只跑一拍，节拍中的请求并入下一拍 |
| Worker 隔离 + 并发槽位 + 退避 | 有——`hub.Send`/`hub.Resume` 在信号量下执行，重试上限 `AGENTHUB_ROUTER_MAX_ATTEMPTS` |
| 持久 Effects 队列 / 事务 Outbox | **明确不做**——提交后同步驱动意图，靠「单写者 + 幂等重放」覆盖失败窗口 |
| 人格化回复 / 长期记忆 | **不在范围**——执行者通过既有任务与会话表面发声 |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧱 执行沙盒

平台不再以宿主进程身份执行任何东西。`internal/sandbox` 是控制面：按 `(归属人, 资源, 作用域)`
租用执行环境、在其中执行、把每次执行落审计、空闲后回收。`hub`、`router` 与各适配器都不认识
「容器」，它们向 `sandbox.Service` 要一次执行，只有 `Manager` 知道执行发生在哪里。

结构与调度器刻意保持一致：消费方定义端口（`store.go`）、单写者（`Manager`）、单飞节拍
（`Sweeper`）、可回放事件总线，以及同一套接口背后的「内存 / PostgreSQL」双实现。

| | |
| --- | --- |
| 默认后端 | `docker`（`AGENTHUB_SANDBOX_PROVIDER`） |
| 拒绝语义 | 运行时缺失返回 `sandbox_unsupported`，绝不静默回落到宿主执行 |
| 隔离层 | 容器 · 用户态内核（gVisor）· microVM（Kata/Firecracker）· 进程沙盒（bubblewrap / nsjail / isolate）· 平台层（E2B / Daytona / CubeSandbox / OpenHands）· 远端节点 · off |
| 租约键 | `(归属人, 资源, 作用域)`；作用域解析顺序 `metadata.ticket` → `metadata.contextId` → `default`，因此一个 ticket 的多步执行共享同一环境 |
| 复用 | 命中即续租，超过 `AGENTHUB_SANDBOX_IDLE_TTL` 回收；匿名调用不复用 |
| 上限 | `AGENTHUB_SANDBOX_MAX_PER_OWNER` / `_MAX_TOTAL`，与 `AGENTHUB_ROUTER_MAX_TICKETS` 交叉校验；取不到租约返回**可重试**的 `sandbox_limit_reached`，交由调度器退避消化 |
| 审计 | 命令摘要、退出码、耗时与输出的 sha256——从不落全量输出 |
| API | `/api/sandboxes`、`/api/sandboxes/{id}`、`/api/sandboxes/stats`、`/api/sandbox-providers`、`/api/sandbox-nodes`、`/api/sandboxes/events`（SSE，支持 `since`） |
| 控制台 | 可交互的沙盒屏在 **agentbuddy**（`ComputersView`）；本仓只提供 API 与 `/docs` 里的那张表 |
| 网络 | `none` / `egress_allowlist` 在**两种后端上都**断网，`bridge` / `platform` 放行；白名单本身仍需出网 sidecar，因此按 `none`（fail-closed）读 |

某个后端**真正能兑现**什么，由它自己声明，并在发放租约**之前**校验：bubblewrap 没有 NAT 接口，
因此向它要 `bridge` 会得到 `sandbox_capability_unavailable`，而不是悄悄拿到宿主的网络命名空间；
远端节点与平台垫片声明的是 `delegated`——本进程约束不了它们，约束发生在节点侧并由该节点的审计留痕。

一类后端是**目录条目**，不是代码分支。不改控制面即可新增：

1. **编译期**——实现 `sandbox.Provider` 并注册（`docker`、`podman`、`runsc`、`bwrap`、`mock`、`remote`）。
2. **声明式**——通过 `AGENTHUB_SANDBOX_PROVIDERS` 交给运营者一份模板。argv 逐元素展开，
   绝不经过 shell；未知占位符、空 argv 或相对路径二进制在**启动时即拒绝加载**。
3. **节点协议**——登记一台机器，它用长轮询领取任务。

`off` 拒绝一切执行；`mock` 是确定性的离线实现，且必须显式选择，离线演示不会变成默认姿态。

### 🔍 哪些执行面进了沙盒

四个执行面共用同一个 `Manager`：

- **CLI**——命令必须等于资源登记的命令；参数按 `args_policy`（`none | prefix | passthrough`）
  校验，提示词最多成为标准输入。「提示词首词即命令」的路径已删除。
- **MCP stdio**——服务在租约内启动，工具白名单在进程真正运行的地方生效。
- **计算机**——`shell / browser / screen` 会话引用真实租约，可在控制台一次性试跑（审计标 `dry_run`）。
- **远端 Agent**——出网经服务端代理，密钥从 `credential_env` 注入，永不进入模型上下文或任务产物。

### 🐋 默认 Docker，缺 Docker 也是一种答案

`AGENTHUB_SANDBOX_PROVIDER` 默认 `docker`。套接字不可达时——没有守护进程、容器内没有
`/var/run/docker.sock`、socket 代理未运行——目录端点如实报告 `available: false` 与原因，
`/healthz` 同步反映，执行一律拒绝，控制台显示同一句话。不能隔离的实例不会改为在宿主上跑。

在容器里部署：默认不挂载任何东西，或用 `AGENTHUB_SANDBOX_DOCKER_HOST` 指向只开
`CONTAINERS`、`IMAGES`、`POST` 的 socket-proxy sidecar。直接挂 `/var/run/docker.sock`
等价于把宿主 root 交给容器，`docker-compose.yml` 因此把该代理做成可选 profile。

### 🆚 与托管运行时对比

完整对比（含许可证）：[`docs/sandbox-landscape_cn.md`](docs/sandbox-landscape_cn.md)。

| | agenthub |
| --- | --- |
| 边界 | 默认容器，可按资源升级到用户态内核、microVM 或你接入的节点 |
| 厂商 SDK | 无——托管运行时用声明式模板接入 |
| 远端 Agent 凭据 | 服务端注入，模型只看到变量名 |
| 生命周期绑任务？ | 不绑——任务取消保留环境，环境消失则下次调用重建 |
| 审计 | `sandbox_execs` 里只有摘要、退出码与耗时 |
| 离线模式 | 显式 `mock`，绝不隐藏回落 |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🚨 错误约定

后端**只返回机器可读 code**，绝不含自然语言；控制台在前端把 code 映射为本地化文案。

- HTTP：`{"code":"..."}`（`bad_request` / `unauthorized` / `not_found` / ...）
- JSON-RPC：标准码 + A2A 自定义 `-32001 task_not_found`、`-32002 task_not_cancelable`、`-32003 push_notification_not_supported`、`-32004 unsupported_operation`、`-32005 invalid_state_transition`、`-32006 resource_not_found`、`-32007 resource_unreachable`、`-32008 task_not_resumable`

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔑 API 密钥

密钥是面向非交互访问（脚本、其他语言、CI）的长效 bearer 凭证。它以**所属用户**身份鉴权，但只拥有**创建时选定的权限**，且明文令牌仅返回一次——库内仅存其 `sha256`（`token_hash`），绝不存明文。

- **创建**：`POST /api/api-keys`，请求体 `{ "name", "scopes": [...], "expiresInDays"? }`。响应中的 `token`（`ahk_…` 字符串）只出现一次，请立即复制。
- **使用**：在任意 `/api/*` 路由上以 `Authorization: Bearer ahk_…` 发送，或以对称头 `X-AgentHub-Key` 发送，亦可用于 A2A 端点。密钥的权限 = 其 scopes 与所属用户权限的交集，因此永远不会超过所属用户。
- **管理**：`GET /api/api-keys` 列出你自己的密钥（持有 `apikeys:manage` 时列出全部）；`PUT /api/api-keys/{id}` 轮换名称 / 权限 / 过期时间；`DELETE /api/api-keys/{id}` 吊销——令牌立即失效，但行记录保留以供审计。

```bash
# 创建一个可读资源与任务的密钥，并用它调用控制面。
TOKEN=$(curl -s -X POST localhost:3500/api/api-keys \
  -H 'Content-Type: application/json' \
  -H 'X-AgentHub-Session: <session>' \
  -d '{"name":"ci","scopes":["resources:read","tasks:read"]}' | jq -r .token)
curl -s localhost:3500/api/resources -H "Authorization: Bearer $TOKEN" | jq .
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🗄 持久化

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
| `audit_log` | 只追加审计账本（见「全 API 审计」）；同样**不参与**任何导出与恢复：`025` 对任何角色都拒绝 `TRUNCATE`，而「把账本回滚」正是它的哈希链要防的事 |
| `api_keys` / `api_key_scopes` | 长效 bearer 凭证；只存令牌的 `sha256`（`token_hash`，唯一索引）与展示用 `prefix`（绝不存明文），含可选 `expires_at` 与软 `revoked_at`；`api_key_scopes` 是 key → 权限 的边表 |

- 迁移：`migrations/*.sql` 经 `//go:embed` 内嵌，启动时按文件名**单事务**顺序执行；SQL 自身幂等。
  建 `schema_migrations` 与每个文件都在 `pg_advisory_xact_lock` 下进行、并在锁内复查台账，因此
  多个实例同时启动（或 `go test` 的两个包共用一库）只会排队，不会在系统目录里撞 23505。
- 写入策略：任务/artifact/span **同步写**（状态迁移为条件更新 `WHERE state=?`，防并发丢更新）；
  events 与 push_records **尽力而为**（有界队列 + 异步 writer，失败只告警，**绝不回压任务执行**）。
- 事件重连：`GET /api/tasks/{id}/events?since=<seq>` 回放该 seq 之后的所有帧，再接实时流。

集成用例覆盖：迁移幂等、资源 CRUD 与 jsonb 往返、任务全生命周期与级联删除、事件回放、
push 回调留痕与保留上限、完整 A2A run 的落库回读，以及治理用例（命名空间 CRUD、治理列往返、
去重启用键冲突、源 CRUD 与同步留痕、内存 facade 与 store facade 行为一致）。
**无 DB 时自动 skip，不伪造结果**。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 💾 数据库检查点

控制台的**备份**标签页（`backups:manage`）把整个 agenthub schema 导出为一个 `tar.gz` 归档，
并能把数据库回退到其中任意一个检查点。

- **归档**：单个文件内含 `manifest.json`（表清单、行数、引擎、当时的迁移集合）与数据载荷。
  文件的 sha256 记在目录行里，下载与恢复前都会重新校验——卷上损坏的归档会被拒绝，而不是被交出去。
- **引擎**：运行时镜像里装了 PostgreSQL 客户端就用 `pg_dump -Fc`；否则回落到纯 Go 引擎，直接流式
  `COPY … TO STDOUT` / `COPY … FROM STDIN`。引擎名随归档一起保存；归档只能由写出它的引擎读回。
- **归档里有什么**：只有应用表。检查点目录（`backup_checkpoints` / `backup_schedules`）与只追加账本
  （`audit_log`）都不在内，因此一次回退既不会抹掉「归档自身的记录」，也不会回滚审计证据——哈希链跨
  恢复保持连续。`pg_dump` 归档因此由**正向 `--table` 清单**构建，而不是按名排除：按名排除一张表时，
  它的序列与触发器函数仍会进归档，`pg_restore --clean` 随后会试图把它们从仍在使用它们的表下面删掉。
- **受保护的表**：纯 Go 引擎用一条 `TRUNCATE` 清空 schema，所以只要它要携带的表上带 `BEFORE TRUNCATE`
  触发器，它在 **dump 与 restore 两处**都按表名以 `backup_table_guarded` 拒绝。一份读不回去的检查点
  比没有更糟，而且这个失败应当在「还来得及换到带 pg_dump 的镜像」时暴露，而不是在真正需要它的那天。
  只认 `BEFORE TRUNCATE`：`TRUNCATE` 不会触发行触发器。
- **定时**（控制台内编辑并落库）：关闭 / 每 N 分钟 / 每日固定时刻（带时区）/ 五段 cron 表达式。
  调度器重启后会补跑一次；进程死掉留下的 `running` 行在下一次启动时被结清。
- **保留策略**：保留最近 N 个、删除超过 M 天的归档。最新那份永不被清理；恢复前自动打的回滚点
  有 24 小时豁免期——退路不能被清理任务删掉。
- **恢复护栏**，按顺序：操作员重输检查点 id → 校验和核对 → 比对归档迁移集合与当前构建（归档来自
  **更新**的 schema 一律拒绝，更旧则先载入再迁移）→ 先给当前数据库打一个检查点 → 停止接收新任务
  并等待在途任务结束 → 重写各表 → 重新载入内存状态。全程无需重启，任何失败都让数据库保持原状。
- **开关**：`AGENTHUB_BACKUP_DIR` 就是开关。不配置时标签页显示「未启用」并说明原因，其余功能不受
  影响；`docker-compose.yml` 默认如此——`agenthub-backups` 卷已挂载，但留到你配置该变量时才启用。
- **静态加密**：配置 `AGENTHUB_ENCRYPTION_KEY` 时归档以 `<file>.tar.gz.enc`（AES-256-GCM，明文绝不落盘）
  写出并附带 `<file>.enc.sha256` 清单，完整性校验覆盖密文。用 `scripts/backup-restore-drill.sh` 定期演练
  （解密进空库并报告关键表行数）。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📁 共享文件

工作台可以上传一份文档、一张截图或一段录音，并在会话里把它交给 agent。字节存在数据库之外，
库里只有元数据。

- **开关**：`AGENTHUB_FILES_STORE=off|local|s3`（默认 `off`）。关着时所有 `/api/files*` 一律回
  `503 files_disabled`，界面直接说明「该服务端已关闭共享文件」，而不是显示一个空列表。
- **端点**：`GET /api/files`（列表 + `maxBytes` + 当前后端）、`POST /api/files`（multipart，取第一个
  文件分片）、`GET /api/files/{id}`、`GET /api/files/{id}/content`（附件下载，支持 `Range`，
  因此大文件可续传）、`DELETE /api/files/{id}`。权限沿用 `tasks:read` / `tasks:write`，**不新增权限码**。
- **上限**：`AGENTHUB_FILES_MAX_BYTES`（默认 32 MiB）在字节流入临时文件的过程中判定，超限的上传
  根本到不了存储。每行都带 sha256 与嗅探出的 MIME；只有嗅探给不出结论时才采信客户端声明的类型。
- **本地后端**：`AGENTHUB_FILES_DIR`。先写临时文件，落盘后以 rename 原子就位到分片目录
  （`<sha256 前两位>/<摘要>`），权限 `0600`。
- **S3 兼容后端**（RustFS、MinIO、AWS）：`AGENTHUB_FILES_S3_ENDPOINT`、`..._BUCKET`、`..._REGION`、
  `..._PATH_STYLE`（RustFS/MinIO 必须保持 `true`），以及存放密钥的环境变量**名字**
  （`..._ACCESS_KEY_ENV`、`..._SECRET_KEY_ENV`）。客户端用标准库手写 SigV4，因此不引入任何新依赖；
  选了 `s3` 却没给 endpoint 或 bucket，`config.Validate()` 会在启动时失败。
- **归属**：行带 `owner_id`，表在 `FORCE` 行级安全之下，因此第二个账号即使猜到 id 也无法列出、
  读取或删除别人的文件。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🏛 AWS Agent Registry 特性对比

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

### 🧾 记录类型与校验

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

### ♻️ 生命周期与治理

| AWS Agent Registry | agenthub |
| --- | --- |
| Draft → 提交 → Curator **approve / reject**（带反馈）→ 可 **deprecate** | `draft → pending → approved / rejected → deprecated`；非法迁移返回 400 |
| 消费方**只看到 approved** 记录 | `registry.Discoverable` 是列表、MCP 导出、Agent Card 与 A2A 寻址共用的唯一过滤出口 |
| 四角色：Administrator / Publisher / Curator / Consumer | RBAC（owner / 组 / 角色 / 权限）+ 资源级 grant，目录侧另增 `registries:manage` / `registry:curate` / `sources:manage` |
| 命名空间可按类型 / 环境 / 团队拆分 | 多命名空间，各带审批策略与可见性边界 |

### 🔎 发现

| AWS Agent Registry | agenthub |
| --- | --- |
| **Hybrid search**：语义 + 关键词 | 仅关键词检索——见下方「明确不做」 |
| **Catalog browsing**：分页 list + 按 recordType 过滤 + `Get` / `BatchGet` | `GET /api/resources?kind=&q=&namespace=&status=`；暂无分页与批量读 |
| **原生 MCP endpoint**（`InvokeRegistryMcp`，任意 MCP 客户端直连） | `GET /api/export/mcp` 只**导出** MCP server 描述，不自建 MCP 服务端 |
| Agent Card | `GET /api/export/agent-card` 与 `/.well-known/agent.json`——每个 approved 记录渲染为一个 advertised skill |

### 🔐 授权与同步

| AWS Agent Registry | agenthub |
| --- | --- |
| Inbound：IAM 或 JWT | 会话 cookie → `Principal`；`/api/*` 走 `requirePermission`，A2A 走 `identify` |
| Outbound：**credential provider**（OAuth 凭据或 IAM role），供同步时调用远端端点 | `AuthScheme`（none / bearer）+ `auth_token_env`，**只存环境变量名** |
| **URL-based synchronization**：记录与远端 MCP / A2A 端点保持同步 | 源（mcp / skill）按手动或定时拉取，按 `(name, version)` 合并并记录同步结果；fixture 客户端默认离线，运维显式关闭后才出网 |

### 📊 运维与可观测

| AWS Agent Registry | agenthub |
| --- | --- |
| EventBridge 通知（记录提交审批时） | SSE broker + `events` 表按 `since=<seq>` 回放——能力等价，自有实现 |
| CloudTrail 审计全部 API 调用 | `slog` 结构化日志（不记密钥、不记用户内容、不记向量）；审计面弱于 CloudTrail，**明确不补** |
| AWS Organizations / 跨账号 RAM 共享 | 单实例多租户（principal + 资源级 grant）；绑定 AWS 基础设施，**不复制** |
| VPC / PrivateLink、service-linked role | 自部署单二进制无对等物 |
| Tags（key-value，成本归属 / 访问控制） | `tags []string` + `metadata map`，够用 |
| — | 数据库检查点与定时备份（`internal/backup`）：AWS 侧无对应能力，agenthub **更强** |

### 📋 三张清单

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

### 🔁 字段级映射（recordType ↔ Kind）

| AWS recordType | 校验 schema | agenthub kind | per-kind 配置 |
| --- | --- | --- | --- |
| `MCP` | MCP 协议 schema | `mcp` | transport、command+args 或 URL、tool allowlist、auth schemes、env |
| `AGENT` | A2A Agent Card schema | `agent` | Agent Card URL、protocolVersion、capabilities、skills、鉴权 |
| `SKILL` | 描述符（Name / Description / Package / Repository / markdown） | `skill` | version、repository / package、触发词、步骤、工具清单、markdown |
| `CUSTOM` | 任意合法 JSON | `custom` | JSON schema |
| — | — | `model` | provider 模式、endpoint、model_id、api_key_env、回退链 + 权重、超时 |
| — | — | `cli` | 可执行命令、命令白名单、超时、输出截断字节、工作目录、env |

### 🧭 剩余差距

1. **中**：语义检索缺失 —— 目录规模上千后关键词检索召回不足；建议以「外部检索源」形态接入，
   而非内嵌嵌入模型。
2. **中**：批量读接口与列表分页 —— 目录变大后列表接口无分页会拖慢控制台。
3. **低**：审计面 —— 缺全量 API 调用留痕，合规场景需要。
4. **低**：定时同步的调度表达式目前手写 5 段 cron，与 EventBridge 规则表达力不对等，但自部署够用。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📝 许可证

MIT.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🤝 工程约定

本仓库遵循 Harness Engineering：

- [`AGENTS.md`](AGENTS.md)：Agent 工程上下文入口与目录索引
- [`requirements.md`](requirements.md)：需求规格（功能边界 / 异常 / 验收，人审后编码）
- [`task.md`](task.md)：实施清单

<div align="right">

[![][back-to-top]](#readme-top)

</div>

---

<div align="center">

Copyright © 2026 [ariacompute][org]. <br/>
This project is [MIT](./LICENSE) licensed.

</div>

<!-- LINK GROUP -->

[org]: https://github.com/ariacompute
[releases]: https://github.com/ariacompute/agenthub/releases
[issues]: https://github.com/ariacompute/agenthub/issues
[ci]: https://github.com/ariacompute/agenthub/actions/workflows/ci.yml
[stars]: https://github.com/ariacompute/agenthub/stargazers
[forks]: https://github.com/ariacompute/agenthub/network/members
[license]: ./LICENSE
[docs]: ./docs/docs_cn.md
[go]: https://go.dev
[postgres]: https://www.postgresql.org
[vue]: https://vuejs.org
[badge-go]: https://img.shields.io/badge/Go-1.27-151515?labelColor=black&style=flat-square&logo=go
[badge-postgres]: https://img.shields.io/badge/PostgreSQL-persistence-151515?labelColor=black&style=flat-square&logo=postgresql
[badge-vue]: https://img.shields.io/badge/Vue-3-151515?labelColor=black&style=flat-square&logo=vuedotjs
[badge-release]: https://img.shields.io/github/v/release/ariacompute/agenthub?labelColor=black&style=flat-square
[badge-ci]: https://img.shields.io/github/actions/workflow/status/ariacompute/agenthub/ci.yml?labelColor=black&style=flat-square
[badge-license]: https://img.shields.io/github/license/ariacompute/agenthub?labelColor=black&style=flat-square
[badge-stars]: https://img.shields.io/github/stars/ariacompute/agenthub?labelColor=black&style=flat-square
[badge-forks]: https://img.shields.io/github/forks/ariacompute/agenthub?labelColor=black&style=flat-square
[back-to-top]: https://img.shields.io/badge/-BACK_TO_TOP-151515?style=flat-square
