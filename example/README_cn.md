# example — Registry resource 真实配置示例

一份可以直接灌进运行中 agenthub 的资源清单：五类资产（model / agent / mcp / skill / cli），
Windows 与 Ubuntu 各一套，共 **41 份 JSON**，外加可重复执行的 `load.sh`，以及四个
`deploy-*.sh` 脚本（在 WSL2 / Ubuntu 上把其中一项资产落地，验证登记的 `card_url`、
`file://` 目录或命令）。

每份 JSON 都是一份**完整的 `registry.Entry` 载荷**：服务端的 `DisallowUnknownFields`、
四类枚举校验和 `kind_config` 校验会自动证明字段写对；`load.sh` 跑通即示例有效。

```sh
# 在仓库根执行——示例统一读取根目录那一份 .env
cp .env.example .env                  # 然后把 AGENTHUB_EXAMPLE_ENABLED 改成 true，
                                      # 并填 AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh                     # 按 uname -s 选平台
./example/load.sh --all               # 两个平台都灌
./example/load.sh --platform windows
```

目录只**描述**这些资产，它们并没有在跑。要让其中一个 A2A agent 在 WSL2 / Ubuntu 上真正跑起来
（这样 `Try run` 与 `POST /api/resources/{id}/probe` 打到的是真服务，而不是直接拒绝），用 `deploy-agent.sh`：

```sh
./example/deploy-agent.sh google-adk start   # venv + google-adk[a2a] + uvicorn，后台常驻
./example/deploy-agent.sh google-adk smoke   # 打印 Agent Card，再发一条 message/send
./example/deploy-agent.sh google-adk status  # pid、card 可达性、日志路径
./example/deploy-agent.sh google-adk stop    # TERM 记录的 pid
```

`deploy-agent.sh` 装什么、东西放哪、网关 key 怎么传，见第 7 节；其余三个脚本（MCP / skill / CLI）见第 8 节。

装载是**显式开启**的：未设置 `AGENTHUB_EXAMPLE_ENABLED=true` 时 `load.sh` 直接报错退出，
因此单纯 `cp .env.example .env` 不会把示例数据灌进真实实例。

打开控制台即可看到：`example`（model）、`example-ubuntu`、`example-windows` 三个 namespace。

---

## 1. 契约速查（按当前代码）

| 项 | 取值 |
| --- | --- |
| 可用键（21） | `id, kind, name, description, endpoint, version, repository, tags, capabilities, transport, auth_schemes, env, health, metadata, namespace, status, display_name, source_id, kind_config` + 服务端回填的 `createdAt` / `updatedAt` |
| **不要写**的键 | `createdAt` / `updatedAt`（服务端回填）；`status`（省略即由 namespace 策略决定） |
| `kind` | `model` / `agent` / `mcp` / `skill` / `cli` / `custom`（本批不用 `custom`） |
| `transport` | `stdio` / `streamable-http` / `sse-legacy`（空 = unset） |
| `health` | `healthy` / `degraded` / `unhealthy` / `unknown`（空 → `unknown`） |
| `status` | `draft` / `pending` / `approved` / `rejected` / `deprecated`；**只有 `approved` 可被发现/导出** |
| `namespace` | slug `^[a-z0-9][a-z0-9-]{0,62}$`；本例用 `example` / `example-ubuntu` / `example-windows` |
| 去重键 | **(namespace, name, version)**，不是 id |
| 未知键 | 400 `bad_request`（`DisallowUnknownFields`，`kind_config` 内部同样严格） |
| 体量上限 | 65536 字节 / 条 |

**写入语义**（容易踩）：

- `POST /api/resources` → 201；命中去重键 → 400 `resource_already_exists`
- `PUT /api/resources/{id}` → **不是 upsert**：id 不存在直接 **404**；存在则 200，且是**整条替换**（缺字段清零）
  → 所以 `load.sh` 走「先 POST，已存在再 PUT」，两条路径都是幂等的；也要求每份 JSON 写全字段。

## 2. 文件清单

| 类别 | 文件 | namespace | 说明 |
| --- | --- | --- | --- |
| model | `model-gateway.json` | `example` | 平台无关，一份 |
| agent | `agent-{google-adk,langgraph,crewai,pi,deepseek-harness}-{ubuntu,windows}.json` | `example-*` | 5 × 2 |
| mcp | `mcp-{filesystem,github,google-drive,google-docs,gmail,slack,stripe,wechat,dingtalk,feishu}-{ubuntu,windows}.json` | `example-*` | 10 × 2 |
| skill | `skill-{anthropics-pdf,skillssh-find-skills}-{ubuntu,windows}.json` | `example-*` | 2 × 2 |
| cli | `cli-{gh,huggingface,modelscope}-{ubuntu,windows}.json` | `example-*` | 3 × 2 |
| — | `deploy-agent.sh` | — | 部署脚本：在 WSL2 / Ubuntu 上把某个 agent 跑起来（当前支持 `google-adk`），见第 7 节 |
| — | `deploy-mcp.sh` | — | 用 FIFO 起一个 stdio MCP server，见第 8 节 |
| — | `deploy-skill.sh` | — | 按 `file://` endpoint 落地 skill 目录，见第 8 节 |
| — | `deploy-cli.sh` | — | 预检 CLI 的 PATH 与 `AGENTHUB_CLI_ALLOWLIST`，见第 8 节 |

## 3. 上游选型与 Windows / Ubuntu 对照

### Agent（三个 A2A 原生框架 + 两个非 A2A 对照组）

**登记约束**：本目录的 Agent 一律要求采用 **A2A 协议**并**发布 Agent Card**（`/.well-known/agent-card.json`）。
下表前三行满足该约束，`kind_config.agent.card_url` **必须填**，且与 `endpoint` **同源**；
后两行是**对照组**，其官方形态不含 A2A，**不填** `card_url`，并以 `tags` 中的 `non-a2a` 与 `metadata.a2a` 显式标注。

| 框架 | A2A | 端口 | Ubuntu 安装 / 启动 | Windows 安装 / 启动 |
| --- | --- | --- | --- | --- |
| Google ADK（`google/adk-python`，Apache-2.0） | ✅ 官方 `to_a2a(root_agent, port=8001)`（标注 experimental） | 8001 | `python3 -m venv .venv && source .venv/bin/activate && pip install "google-adk[a2a]"` → `uvicorn a2a_agent:a2a_app --host 127.0.0.1 --port 8001` | `python -m venv .venv && .venv\Scripts\activate && pip install "google-adk[a2a]"` → 同命令；uvicorn reloader 报 `_make_subprocess_transport NotImplementedError` 时加 `--no-reload` |
| LangGraph（框架 `langchain-ai/langgraph`，MIT；A2A 封装用官方样例 `a2aproject/a2a-samples` 的 `samples/python/agents/langgraph`） | ✅ a2a-sdk `A2AStarletteApplication` | 10000 | `git clone https://github.com/a2aproject/a2a-samples.git && cd a2a-samples/samples/python/agents/langgraph && curl -LsSf https://astral.sh/uv/install.sh \| sh && uv sync` → `uv run app --host 127.0.0.1 --port 10000` | PowerShell 安装 uv（`irm https://astral.sh/uv/install.ps1 \| iex`）后 `uv sync` → 同启动命令 |
| CrewAI（`crewAIInc/crewAI`，MIT） | ✅ 原生 `A2AServerConfig(url=...)`（与 `A2AClientConfig` 双向一等公民） | 8000 | `python3 -m venv .venv && source .venv/bin/activate && pip install "crewai[a2a]"`（可选 `crewai create crew my_crew`）→ `python server.py`（内部 `crew.kickoff()`） | `python -m venv .venv && .venv\Scripts\Activate.ps1 && pip install "crewai[a2a]"` → 同命令 |
| Pi（`earendil-works/pi`，MIT）— **对照组** | ❌ 官方不实现 A2A，不发 Agent Card | 无内置 HTTP 服务 | `curl -fsSL https://pi.dev/install.sh \| sh`（另可选 `npm install -g --ignore-scripts @earendil-works/pi-coding-agent`），需 Node.js 22.19+ → `cd /path/to/project && pi --mode rpc --no-session` | `powershell -c "irm https://pi.dev/install.ps1 \| iex"` → 同命令 |
| DeepSeek Harness（`deepseek-ai/deepseek-harness`，MIT）— **对照组** | ❌ Web UI 不是 A2A server，不发 Agent Card | 3080 | `npx @deepseek-harness/dsh web`（源码：git clone → `pnpm install` → `pnpm run build` → `pnpm dsh web`） | 同命令（PowerShell 下执行），包名与端口以仓库 README 为准 |

⚠️ **ADK 端口 8000 → 8001**：`adk api_server` 暴露的是 ADK 自有 REST API（`/run`、`/run_sse`），
**不是 A2A**。要走 A2A 必须 `to_a2a(root_agent, port=8001)` 再用 uvicorn 起，
Agent Card 由 a2a-sdk 自动发布在 `http://127.0.0.1:8001/.well-known/agent-card.json`（官方标注 experimental）。
现成的部署脚本是 `deploy-agent.sh`，见第 7 节。

⚠️ **对照组不发 Agent Card**：Pi 的官方程序化入口只有 `--mode rpc`（stdin/stdout 上的 JSONL 记录），
DeepSeek Harness 只有 `dsh web` 的 Web UI（`:3080`），两者官方均不实现 A2A。
因此这四份示例**不填** `card_url`、也不写 `protocol_version`，只演示「非 A2A 资产如何登记进来」——
不为了让示例好看而伪造一个 card 地址。

`kind_config.agent.capabilities` 只接受 `streaming` / `pushNotifications` / `stateTransitionHistory`；
ADK 的 `run`、LangGraph 的 `threads/runs` 这类业务动作写在**顶层 `capabilities`**。

### MCP（十个 server）

| Server | 上游 | Ubuntu | Windows | 传输 |
| --- | --- | --- | --- | --- |
| Filesystem | `modelcontextprotocol/servers`（官方 reference） | `npx -y @modelcontextprotocol/server-filesystem /home/<user>/docs` | `cmd /c npx -y … C:\Users\<user>\docs` | stdio |
| GitHub | `github/github-mcp-server`（官方） | `docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN ghcr.io/github/github-mcp-server` | 远程 `https://api.githubcopilot.com/mcp/`（OAuth） | stdio / streamable-http |
| Google Drive / Docs / Gmail | `endrec/google_workspace_mcp`（`uvx workspace-mcp`，MIT） | `uvx workspace-mcp --tool-tier core --tools drive\|docs\|gmail` | 同命令（uvx 不需要 `cmd /c`），凭据路径 `%USERPROFILE%` | stdio |
| Slack | `@modelcontextprotocol/server-slack`（Anthropic 官方，MIT） | `npx -y @modelcontextprotocol/server-slack` | `cmd /c npx -y …` | stdio |
| Stripe | `@stripe/mcp`（Stripe 官方） | `npx -y @stripe/mcp --tools=all` | `cmd /c npx -y …` | stdio |
| 微信 | 腾讯 **iLink / ClawBot** + 社区封装 `JingxuanC/wechat-mcp`（MIT） | `make build` → `./bin/wechat-mcp`（扫码页 `:9765`） | 构建 `bin\wechat-mcp.exe`，`WECHAT_MCP_STATE` 指定状态文件 | stdio |
| 钉钉 | `@open-dingtalk/dingtalk-mcp`（官方） | `npx -y dingtalk-mcp@latest` | `cmd /c npx -y …` | stdio |
| 飞书 | `@larksuiteoapi/lark-mcp`（官方） | `npx -y @larksuiteoapi/lark-mcp mcp -a <app_id> -s <app_secret> --oauth` | `cmd /c npx -y …` | stdio |

要点：

- **`cmd /c` 只包 npx 类**（官方 MCP README 明文要求）；`uvx`、Docker、Go 二进制不需要。
- **GitHub 拆两份是刻意的**：同一 server 演示 `stdio` 与 `streamable-http` 两种 transport；两种形态在两个平台都能用。
- **微信没有官方 MCP server**：腾讯开放的是 iLink / ClawBot 个人 Bot 协议（`ilinkai.weixin.qq.com`），
  示例用的是基于该协议的社区封装。备选：公众号向 `odyecho/mcp-server-wechat`、
  macOS GUI 自动化 `andy51002000/WeChat-MCP`、Windows GUI 自动化 `panxingfeng/mcp_server_wechat`。

### Skill（两个来源）

| 来源 | 获取方式 | Ubuntu endpoint | Windows endpoint |
| --- | --- | --- | --- |
| `anthropics/skills`（示例取 `pdf`） | `git clone https://github.com/anthropics/skills` | `file:///home/<user>/skills/pdf` | `file:///C:/Users/<user>/skills/pdf` |
| skills.sh（示例取 `find-skills`） | `npx skills add vercel-labs/skills` | `file:///home/<user>/<project>/.agents/skills/find-skills` | `file:///C:/Users/<user>/<project>/.agents/skills/find-skills` |

两者的 skill 格式相同（含 `SKILL.md` 的目录），差别只在获取方式：git clone vs registry 安装。
skills.sh 由 Vercel Labs 维护（源码 `vercel-labs/skills`），`npx skills find` 发现、`npx skills add <owner/repo>` 安装，
多数 agent 读共享目录 `.agents/skills/`，CLI 负责抹平各 agent 的目录差异；Windows 同样需要 `cmd /c`。

### CLI（三个官方 CLI）

| CLI | Ubuntu | Windows | `metadata.command` |
| --- | --- | --- | --- |
| `gh`（MIT） | `sudo apt install gh`；`/usr/bin/gh` | `winget install --id GitHub.cli`；`C:\Program Files\GitHub CLI\gh.exe` | `gh` |
| `hf`（Apache-2.0） | `pip install -U "huggingface_hub[cli]"`；`~/.local/bin/hf` | 同 pip；`…\Scripts\hf.exe` | `hf` |
| `modelscope`（Apache-2.0） | `pip install modelscope`；`~/.local/bin/modelscope` | 同 pip；`…\Scripts\modelscope.exe` | `modelscope` |

### Model

base url 是 `https://gateway.ariacompute.com/**v1**`：适配器只给「不以 `/chat/completions` 结尾」的地址补 `/chat/completions`，
不带 `/v1` 前缀会打到网关的 405。见下一节的密钥策略。

## 4. 密钥与 allowlist

### Model 的 key 不进 JSON、也不进库

`kind_config.model.api_key_env` 只存**环境变量名** `AGENTHUB_EXAMPLE_API_KEY`，
真实值由 **agenthub 服务端进程**在调用时读取（`internal/adapter/model_openai.go`）：

```sh
export AGENTHUB_EXAMPLE_API_KEY=sk-...  # 起服务之前
docker compose up -d                    # 或直接 go run ./cmd/agenthub-server
```

`model-gateway.json` 里唯一的占位符是 `model_id`（`__AGENTHUB_EXAMPLE_MODEL_ID__`），
由 `load.sh` 从**仓库根**的 `.env` 注入；它非密但未知，缺了会直接报错退出，绝不上传占位符。

### 使能开关

`AGENTHUB_EXAMPLE_ENABLED` 是整个目录的总开关。根 `.env.example` 里默认为 `false`；
只要不是 `true`，`load.sh` 就报错退出并提示去哪个文件改：

```sh
AGENTHUB_EXAMPLE_ENABLED=true ./example/load.sh --all
```

装载脚本的所有配置统一用 `AGENTHUB_EXAMPLE_` 前缀（base URL、邮箱、密码、模型 id），
避免示例的键与服务端自身配置撞名。

### CLI allowlist（整串匹配）

运行期真正生效的是**服务端**环境变量 `AGENTHUB_CLI_ALLOWLIST`，且是 `strings.EqualFold` 对**整串**比对
（`internal/adapter/cli.go`）。默认值不含 `gh` / `hf` / `modelscope`，所以要放行：

```sh
AGENTHUB_CLI_ALLOWLIST=git,ls,cat,echo,date,aria-memo,gh,hf,modelscope
```

- `metadata.command` 是 CLI adapter **唯一读取**的字段 → 写裸 token（`gh` / `hf` / `modelscope`）。
- `kind_config.cli.command` / `allowlist` 只是注册期声明，**不参与运行期校验**（示例也写了，方便人读）。
- 若改成真实绝对路径（`/usr/bin/gh`、`C:\Program Files\GitHub CLI\gh.exe`），
  allowlist 必须写成**同样的整串**，例如 `…,C:\Program Files\GitHub CLI\gh.exe`。

## 5. 占位符

JSON 里不写任何真实用户名、路径或密钥，统一占位：

| 占位符 | 含义 |
| --- | --- |
| `<user>` | 你的系统用户名（`C:\Users\<user>` / `/home/<user>`） |
| `<project>` | 你跑 `npx skills add` 的项目根目录 |
| `<app_id>` / `<app_secret>` | 飞书自建应用凭证 |
| `__AGENTHUB_EXAMPLE_MODEL_ID__` | 网关上的模型名，由 `load.sh` 从 `.env` 注入 |

各厂商 token（`SLACK_BOT_TOKEN`、`GITHUB_PERSONAL_ACCESS_TOKEN`、`GOOGLE_OAUTH_CLIENT_ID/SECRET`、
`STRIPE_SECRET_KEY`、`DINGTALK_Client_ID/Secret`、`HF_TOKEN`、`MODELSCOPE_API_TOKEN`）一律走**服务端进程环境**，
写在对应 JSON 的 `metadata.env_hint` 里提示，不写值。

## 6. 注意事项

- **adapter 现状**：只有 model 的 `openai_compatible` 是真实实现（`internal/adapter/model_openai.go`）；
  agent / mcp / skill / cli 的 connector 仍是 `TODO(real)`，只做形状翻译。
  因此 MCP / CLI 示例表达的是**配置意图**，不是「今天就能执行」。
- **示例不自我声明健康**：41 份示例都不写 `health`（省略即 `unknown`），也不写 `metadata.mock_health`；
  CLI 六例没有 endpoint。健康值只由**实测**写入，别无来源：`POST /api/resources/{id}/probe` 去问资源自己的
  connector，一次运行则记录这次调用证实的结论。只有当确实没有东西可访问时，探测才回答 `unknown`——不发 Agent Card 的 agent、stdio 的 MCP server、
  没有命令的 CLI、没有 endpoint 的 skill——因为在那里编一个值正是健康状态不再做的事。发了 card 的 agent
  会被真实抓取：200 且能解析是 `healthy`，凭据被拒或 200 但不是 card 是 `degraded`，没人应答是 `unhealthy`。
  CLI 的探测只在 `PATH` 上查找命令、不执行它：查不到是 `unhealthy`，查到但不在 allowlist 里是 `degraded`。
- **仅用于开发/演示**：不要照抄到生产；种子与生产策略见根目录 `AGENTS.md` 的 `AGENTHUB_SEED_DEMO` 一节。
- **上游许可证**（以各自仓库 LICENSE 为准）：ADK Apache-2.0、LangGraph MIT、CrewAI MIT、pi MIT、
  deepseek-harness MIT、github-mcp-server MIT、@modelcontextprotocol/server-slack MIT、@stripe/mcp MIT、
  workspace-mcp MIT、wechat-mcp MIT、gh MIT、huggingface_hub Apache-2.0、modelscope Apache-2.0、
  anthropics/skills 与 vercel-labs/skills 见各自仓库。

## 7. 在 Ubuntu 上部署 agent（`deploy-agent.sh`）

`load.sh` 登记的是资产「是什么」，`deploy-agent.sh` 让其中一个「真应答」。脚本面向 WSL2 / Ubuntu，
第一个参数是 agent 名：

```sh
./example/deploy-agent.sh <agent> [start|stop|restart|status|smoke] [options]
```

| Agent | 端口 | 装什么 | 怎么起 |
| --- | --- | --- | --- |
| `google-adk` | 8001 | venv 里的 `google-adk[a2a]` + `uvicorn` | `uvicorn a2a_agent:a2a_app --host 127.0.0.1 --port 8001` |

- `start`：建 venv、写 `a2a_agent.py`、用 `nohup` 起服务并等待 Agent Card（默认 60s，`--wait`）。
  host、端口与卡片路径从对应的 `resources/agent-<name>-ubuntu.json` 读出来，脚本不会跟目录脱节；
  与登记值不一致时会提示。
- 所有选项默认读仓库根目录的 `.env`，`.env` 没设的键再回落 `.env.example`：
  `AGENTHUB_EXAMPLE_MODEL_ID` 对应 `--model-id`，`AGENTHUB_EXAMPLE_API_BASE` 对应 `--api-base`，
  `AGENTHUB_EXAMPLE_API_KEY` 就是网关 key。已导出的变量优先于文件，命令行参数优先于两者——同一份
  `.env` 既配置服务端，也能把 agent 拉起来，不必到处重复。
- 变体跟着凭据走：shell、`.env` 或 `--api-key` 里有 key 就起网关版 agent，没有 key 的机器回落
  不调 LLM 的确定性 `BaseAgent`，所以没有凭据之前也能验证部署。`--offline` 强制离线桩，`--llm`
  强制网关版。
- 网关 key 只经服务进程环境传递：不写进生成的 agent、pid 文件与日志，也不回显。优先用环境变量或
  `.env`——命令行上的值会出现在 `ps` 里。
- `stop` 发 TERM（不理就 KILL），`status` 报 pid + 卡片可达性 + 日志路径（没在跑时退出码 1），
  `smoke` 发一条 `message/send` 并打印回复，真实回复与 mock 一眼可分。
- 产物都在 `$HOME/.local/share/agenthub-<agent>`（`--workdir` 可覆盖），venv 与日志都不进仓库。
- 脚本不碰 agenthub：不登录、不调 `load.sh`、不调 `/probe`。

## 8. 在 Ubuntu 上部署 MCP / skill / CLI 资产

`load.sh` 登记的是资产「是什么」，第 7 节的 `deploy-agent.sh` 让一个 A2A agent 真应答；其余三个脚本
落地的是**不是服务**的资产。四个脚本形态一致：`set -euo pipefail`、带 `--help` 的 usage 注释块、
解析（而非 source）根目录 `.env`、以对应的 `resources/*-ubuntu.json` 为唯一配置源、
`start|stop|restart|status|smoke`、`$HOME/.local/share` 下的 workdir、`--dry-run`，
以及一律不登录 agenthub、不调 `load.sh`、不调 `/probe`。

| 脚本 | 可选项 | `start` | `smoke` |
| --- | --- | --- | --- |
| `deploy-mcp.sh` | 10 个 server：`filesystem`、`github`、`google-drive`、`google-docs`、`gmail`、`slack`、`stripe`、`wechat`、`dingtalk`、`feishu` | 解析 `kind_config.mcp.command` + `args`，展开 `<user>` / `<project>` / `<app_id>` / `<app_secret>`，用 FIFO 托住 stdin 并后台起 stdio 进程 | 发送 MCP `initialize` / `notifications/initialized` / `tools/list`，打印从日志解析出的工具列表 |
| `deploy-skill.sh` | `anthropics-pdf`、`skillssh-find-skills` | 按登记的 `file://` endpoint 落地目录（`git clone --depth 1` 或 `npx skills add`），并发布固定软链 | 读取 `SKILL.md`，打印 front matter、triggers 与 tools |
| `deploy-cli.sh` | `gh`、`huggingface`、`modelscope` | 按 `entryCommand` 的顺序解析命令，检查 PATH 与 `AGENTHUB_CLI_ALLOWLIST`（整串 `EqualFold`），写入部署状态 | 只跑 `--version` / `--help` / `version` |

```sh
./example/deploy-mcp.sh filesystem start --dry-run  # 打印解析出的命令，不起任何东西
./example/deploy-mcp.sh filesystem start
./example/deploy-mcp.sh filesystem smoke
./example/deploy-mcp.sh filesystem stop

./example/deploy-skill.sh anthropics-pdf start
./example/deploy-skill.sh anthropics-pdf smoke
./example/deploy-skill.sh anthropics-pdf stop

AGENTHUB_CLI_ALLOWLIST=git,ls,cat,echo,date,aria-memo,gh ./example/deploy-cli.sh gh start
./example/deploy-cli.sh gh status
./example/deploy-cli.sh gh smoke
./example/deploy-cli.sh gh stop
```

- skill 是目录、CLI 是 PATH 上的二进制：都没有进程、pid 与端口。`stop` 只撤销发布（删软链 / 状态文件），
  绝不删除已获取的 checkout 或二进制；没部署时 `status` 退出码为 1。
- `deploy-mcp.sh` 绝不执行未解析的占位符。`<app_id>` / `<app_secret>` 来自
  `LARK_APP_ID` / `LARK_APP_SECRET`；密钥在每条打印的命令行里都脱敏，但飞书 server 把 `-a` / `-s`
  放在 argv 上，因此该值会出现在 `ps` 里。其余凭据一律从进程环境继承，不写进 pid 文件与日志。
- `deploy-cli.sh` 从根目录 `.env` / `.env.example` 读 `AGENTHUB_CLI_ALLOWLIST`，并复刻服务端的整串比对。
  服务端只在启动时读一次，改完要重启服务端；`--allowlist` 覆盖预检，`--skip-allowlist` 则照样记录。
- 这三个脚本都不改变第 6 节所述的 connector 现状：服务端 mcp / skill / cli connector 仍是
  `TODO(real)`。它们校验的是本地命令、目录与工具列表，不是经 agenthub 的一次执行。
