# example — Real “Registry resource” configuration samples

A catalogue you can load straight into a running agenthub instance: five kinds of assets
(model / agent / mcp / skill / cli), one set for Windows and one for Ubuntu, **41 JSON files**
in total, plus a re-runnable `load.sh` and four `deploy-*.sh` scripts that stand one asset up on
WSL2 / Ubuntu, so its registered `card_url`, `file://` directory or command can be verified.

Every file is a **complete `registry.Entry` payload**: the server's `DisallowUnknownFields`,
the four enum checks and the `kind_config` validation prove the fields are written correctly,
and a clean `load.sh` run proves the sample is valid.

```sh
# from the repository root — the examples read the single root .env
cp .env.example .env                  # then set AGENTHUB_EXAMPLE_ENABLED=true
                                      # and AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh                     # picks the platform from uname -s
./example/load.sh --all               # loads both platforms
./example/load.sh --platform windows
```

The catalogue only *describes* these assets — nothing in it is running. To make one of the A2A
agents real on WSL2 / Ubuntu, so `Try run` and `POST /api/resources/{id}/probe` reach a service
instead of refusing, use `deploy-agent.sh`:

```sh
./example/deploy-agent.sh google-adk start   # venv + google-adk[a2a] + uvicorn, background
./example/deploy-agent.sh google-adk smoke   # print the Agent Card, then one message/send
./example/deploy-agent.sh google-adk status  # pid, card reachability, log path
./example/deploy-agent.sh google-adk stop    # TERM the recorded pid
```

Section 7 covers what `deploy-agent.sh` installs, where it puts things and how it handles the
gateway key; section 8 covers the other three scripts (MCP / skill / CLI).

Loading is **opt-in**: `load.sh` exits with a message until
`AGENTHUB_EXAMPLE_ENABLED=true` is set, so a plain `cp .env.example .env` can never
fill a real instance with sample data.

Open the console and you will find three namespaces: `example` (model), `example-ubuntu` and
`example-windows`.

---

## 1. Contract cheat sheet (as of current code)

| Item | Values |
| --- | --- |
| Accepted keys (21) | `id, kind, name, description, endpoint, version, repository, tags, capabilities, transport, auth_schemes, env, health, metadata, namespace, status, display_name, source_id, kind_config` plus server-filled `createdAt` / `updatedAt` |
| Keys you should **not** send | `createdAt` / `updatedAt` (server-filled); `status` (omitted → decided by the namespace policy) |
| `kind` | `model` / `agent` / `mcp` / `skill` / `cli` / `custom` (this set does not use `custom`) |
| `transport` | `stdio` / `streamable-http` / `sse-legacy` (empty = unset) |
| `health` | `healthy` / `degraded` / `unhealthy` / `unknown` (empty → `unknown`) |
| `status` | `draft` / `pending` / `approved` / `rejected` / `deprecated`; **only `approved` is discoverable/exported** |
| `namespace` | slug `^[a-z0-9][a-z0-9-]{0,62}$`; here `example` / `example-ubuntu` / `example-windows` |
| Dedup key | **(namespace, name, version)** — not id |
| Unknown key | 400 `bad_request` (`DisallowUnknownFields`; `kind_config` is just as strict) |
| Size cap | 65536 bytes per payload |

**Write semantics** (easy to trip on):

- `POST /api/resources` → 201; dedup key hit → 400 `resource_already_exists`
- `PUT /api/resources/{id}` → **not an upsert**: unknown id answers **404**; existing id answers 200 and
  **replaces the whole record** (missing fields are cleared)
  → so `load.sh` does POST first and falls back to PUT; both paths are idempotent, and every JSON must
  be a complete payload.

## 2. File inventory

| Kind | Files | namespace | Count |
| --- | --- | --- | --- |
| model | `model-gateway.json` | `example` | platform-neutral, one file |
| agent | `agent-{google-adk,langgraph,crewai,pi,deepseek-harness}-{ubuntu,windows}.json` | `example-*` | 5 × 2 |
| mcp | `mcp-{filesystem,github,google-drive,google-docs,gmail,slack,stripe,wechat,dingtalk,feishu}-{ubuntu,windows}.json` | `example-*` | 10 × 2 |
| skill | `skill-{anthropics-pdf,skillssh-find-skills}-{ubuntu,windows}.json` | `example-*` | 2 × 2 |
| cli | `cli-{gh,huggingface,modelscope}-{ubuntu,windows}.json` | `example-*` | 3 × 2 |
| — | `deploy-agent.sh` | — | deployment script: stands one agent up on WSL2 / Ubuntu (`google-adk` today), see §7 |
| — | `deploy-mcp.sh` | — | bridges one stdio MCP server to a Streamable HTTP endpoint, see §8 |
| — | `deploy-skill.sh` | — | acquires one skill directory at its `file://` endpoint, see §8 |
| — | `deploy-cli.sh` | — | preflights one CLI against PATH + `AGENTHUB_CLI_ALLOWLIST`, see §8 |

## 3. Upstream choices & Windows / Ubuntu comparison

### Agent (three A2A-native frameworks + two non-A2A controls)

**Catalogue rule**: every Agent in this directory must speak the **A2A protocol** and **publish an Agent
Card** at `/.well-known/agent-card.json`. The first three rows below satisfy that rule, so their
`kind_config.agent.card_url` is **required** and must share the **same origin** as `endpoint`. The last
two rows are **controls**: their upstreams do not implement A2A, so they deliberately **omit** `card_url`
and are marked explicitly through the `non-a2a` tag and `metadata.a2a`.

| Framework | A2A | Port | Ubuntu install / start | Windows install / start |
| --- | --- | --- | --- | --- |
| Google ADK (`google/adk-python`, Apache-2.0) | ✅ official `to_a2a(root_agent, port=8001)` (marked experimental) | 8001 | `python3 -m venv .venv && source .venv/bin/activate && pip install "google-adk[a2a]"` → `uvicorn a2a_agent:a2a_app --host 127.0.0.1 --port 8001` | `python -m venv .venv && .venv\Scripts\activate && pip install "google-adk[a2a]"` → same command; if the uvicorn reloader throws `_make_subprocess_transport NotImplementedError`, add `--no-reload` |
| LangGraph (framework `langchain-ai/langgraph`, MIT; A2A wrapper from the official `a2aproject/a2a-samples` sample `samples/python/agents/langgraph`) | ✅ a2a-sdk `A2AStarletteApplication` | 10000 | `git clone https://github.com/a2aproject/a2a-samples.git && cd a2a-samples/samples/python/agents/langgraph && curl -LsSf https://astral.sh/uv/install.sh \| sh && uv sync` → `uv run app --host 127.0.0.1 --port 10000` | Install uv with PowerShell (`irm https://astral.sh/uv/install.ps1 \| iex`), then `uv sync` → same start command |
| CrewAI (`crewAIInc/crewAI`, MIT) | ✅ native `A2AServerConfig(url=...)` (bidirectional, alongside `A2AClientConfig`) | 8000 | `python3 -m venv .venv && source .venv/bin/activate && pip install "crewai[a2a]"` (optional `crewai create crew my_crew`) → `python server.py` (calls `crew.kickoff()`) | `python -m venv .venv && .venv\Scripts\Activate.ps1 && pip install "crewai[a2a]"` → same command |
| Pi (`earendil-works/pi`, MIT) — **control** | ❌ upstream does not implement A2A; no Agent Card | no built-in HTTP server | `curl -fsSL https://pi.dev/install.sh \| sh` (alternative: `npm install -g --ignore-scripts @earendil-works/pi-coding-agent`); needs Node.js 22.19+ → `cd /path/to/project && pi --mode rpc --no-session` | `powershell -c "irm https://pi.dev/install.ps1 \| iex"` → same command |
| DeepSeek Harness (`deepseek-ai/deepseek-harness`, MIT) — **control** | ❌ the Web UI is not an A2A server; no Agent Card | 3080 | `npx @deepseek-harness/dsh web` (from source: git clone → `pnpm install` → `pnpm run build` → `pnpm dsh web`) | same command under PowerShell; package name and port as documented in the repo README |

⚠️ **ADK port 8000 → 8001**: `adk api_server` serves ADK's own REST API (`/run`, `/run_sse`), which is
**not A2A**. To speak A2A you must wrap the agent with `to_a2a(root_agent, port=8001)` and serve it with
uvicorn; the a2a-sdk then auto-publishes the Agent Card at
`http://127.0.0.1:8001/.well-known/agent-card.json` (documented as experimental).
A ready-made deployment for it lives in `deploy-agent.sh` — see §7.

⚠️ **The controls publish no Agent Card**: Pi's only programmatic entry point is `--mode rpc` (JSONL records
on stdin/stdout) and DeepSeek Harness only ships the `dsh web` Web UI (`:3080`) — neither upstream
implements A2A. These four payloads therefore **omit** `card_url` and carry no `protocol_version`; they
exist to show how a non-A2A asset is still registered. No card URL is invented to make the sample look
complete.

`kind_config.agent.capabilities` accepts only `streaming` / `pushNotifications` /
`stateTransitionHistory`; business actions such as ADK's `run` or LangGraph's `threads/runs`
belong in the **top-level `capabilities`**.

### MCP (ten servers)

| Server | Upstream | Ubuntu | Windows | Transport |
| --- | --- | --- | --- | --- |
| Filesystem | `modelcontextprotocol/servers` (official reference) | `npx -y @modelcontextprotocol/server-filesystem /home/<user>/docs` | `cmd /c npx -y … C:\Users\<user>\docs` | stdio |
| GitHub | `github/github-mcp-server` (official) | `docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN ghcr.io/github/github-mcp-server` | remote `https://api.githubcopilot.com/mcp/` (OAuth) | stdio / streamable-http |
| Google Drive / Docs / Gmail | `endrec/google_workspace_mcp` (`uvx workspace-mcp`, MIT) | `uvx workspace-mcp --tool-tier core --tools drive\|docs\|gmail` | same command (uvx needs no `cmd /c`), credentials under `%USERPROFILE%` | stdio |
| Slack | `@modelcontextprotocol/server-slack` (Anthropic official, MIT) | `npx -y @modelcontextprotocol/server-slack` | `cmd /c npx -y …` | stdio |
| Stripe | `@stripe/mcp` (Stripe official) | `npx -y @stripe/mcp --tools=all` | `cmd /c npx -y …` | stdio |
| WeChat | Tencent **iLink / ClawBot** + community wrapper `JingxuanC/wechat-mcp` (MIT) | `make build` → `./bin/wechat-mcp` (QR page `:9765`) | build `bin\wechat-mcp.exe`, state file via `WECHAT_MCP_STATE` | stdio |
| DingTalk | `@open-dingtalk/dingtalk-mcp` (official) | `npx -y dingtalk-mcp@latest` | `cmd /c npx -y …` | stdio |
| Feishu | `@larksuiteoapi/lark-mcp` (official) | `npx -y @larksuiteoapi/lark-mcp mcp -a <app_id> -s <app_secret> --oauth` | `cmd /c npx -y …` | stdio |

Notes:

- **`cmd /c` only wraps npx-style servers** (explicitly required by the official MCP README); `uvx`, Docker and Go binaries need no wrapper.
- **GitHub is split on purpose**: one server demonstrating both `stdio` and `streamable-http`. Either shape works on either platform.
- **WeChat has no official MCP server**: Tencent opened the iLink / ClawBot personal-bot protocol
  (`ilinkai.weixin.qq.com`), and the sample uses a community wrapper built on it. Alternatives:
  `odyecho/mcp-server-wechat` (official-account), `andy51002000/WeChat-MCP` (macOS GUI automation),
  `panxingfeng/mcp_server_wechat` (Windows GUI automation).

### Skill (two sources)

| Source | How to obtain | Ubuntu endpoint | Windows endpoint |
| --- | --- | --- | --- |
| `anthropics/skills` (sample: `pdf`) | `git clone https://github.com/anthropics/skills /home/<user>/skills` | `file:///home/<user>/skills/skills/pdf` | `file:///C:/Users/<user>/skills/skills/pdf` |
| skills.sh (sample: `find-skills`) | `npx skills add vercel-labs/skills` | `file:///home/<user>/<project>/.agents/skills/find-skills` | `file:///C:/Users/<user>/<project>/.agents/skills/find-skills` |

`anthropics/skills` keeps each skill under `skills/<name>/` in the repository, hence the doubled
`skills/` in the endpoint: the clone target followed by the directory inside the checkout.
`deploy-skill.sh` derives the clone root from the endpoint by stripping
`kind_config.skill.package` (`skills/pdf`) off its tail.

Both use the same skill format (a directory containing `SKILL.md`); only the acquisition differs —
`git clone` versus installing from the registry. skills.sh is maintained by Vercel Labs
(source: `vercel-labs/skills`): `npx skills find` to discover, `npx skills add <owner/repo>` to
install. Most agents read the shared `.agents/skills/` directory, and the CLI smooths over each
agent's own layout; on Windows it needs the same `cmd /c` wrapper.

### CLI (three official CLIs)

| CLI | Ubuntu | Windows | `metadata.command` |
| --- | --- | --- | --- |
| `gh` (MIT) | `sudo apt install gh`; `/usr/bin/gh` | `winget install --id GitHub.cli`; `C:\Program Files\GitHub CLI\gh.exe` | `gh` |
| `hf` (Apache-2.0) | `pip install -U "huggingface_hub[cli]"`; `~/.local/bin/hf` | same pip install; `…\Scripts\hf.exe` | `hf` |
| `modelscope` (Apache-2.0) | `pip install modelscope`; `~/.local/bin/modelscope` | same pip install; `…\Scripts\modelscope.exe` | `modelscope` |

### Model

Base URL https://gateway.ariacompute.com/**v1**: the adapter only appends `/chat/completions`
to an address that does not already end in it, so a base URL without `/v1` lands on the gateway's
405. See the next section for the credential policy.

## 4. Credentials & allowlist

### The model key never reaches the JSON or the database

`kind_config.model.api_key_env` stores only the **name of an environment variable**
`AGENTHUB_EXAMPLE_API_KEY`; the real value is read by the **agenthub server process** at call time
(`internal/adapter/model_openai.go`):

```sh
export AGENTHUB_EXAMPLE_API_KEY=sk-...  # before starting the server
docker compose up -d                    # or: go run ./cmd/agenthub-server
```

The only placeholder in `model-gateway.json` is `model_id` (`__AGENTHUB_EXAMPLE_MODEL_ID__`),
injected by `load.sh` from the **repository-root** `.env`. It is not a secret, merely unknown —
if it is missing the script exits with an error rather than uploading the placeholder.

### Enable switch

`AGENTHUB_EXAMPLE_ENABLED` gates the whole catalogue. The default in the root `.env.example` is
`false`, and `load.sh` exits with a pointer to the file when it is anything but `true`:

```sh
AGENTHUB_EXAMPLE_ENABLED=true ./example/load.sh --all
```

All loader settings share the `AGENTHUB_EXAMPLE_` prefix (base URL, email, password, model id) so
the example keys cannot collide with the server's own configuration.

### CLI allowlist (whole-string match)

What actually governs execution is the **server-side** environment variable
`AGENTHUB_CLI_ALLOWLIST`, compared with `strings.EqualFold` against the **whole string**
(`internal/adapter/cli.go`). Its default does not include `gh` / `hf` / `modelscope`, so allow them:

```sh
AGENTHUB_CLI_ALLOWLIST=git,ls,cat,echo,date,aria-memo,gh,hf,modelscope
```

- `metadata.command` is the **only** field the CLI adapter reads → write the bare token
  (`gh` / `hf` / `modelscope`).
- `kind_config.cli.command` / `allowlist` are registration-time declarations and are **not**
  consulted at runtime (the samples still fill them in for readability).
- If you switch to real absolute paths (`/usr/bin/gh`, `C:\Program Files\GitHub CLI\gh.exe`),
  the allowlist entry must be **the exact same string**, e.g. `…,C:\Program Files\GitHub CLI\gh.exe`.

## 5. Placeholders

No real usernames, paths or keys appear in the JSON — everything is placeholdered:

| Placeholder | Meaning |
| --- | --- |
| `<user>` | your OS username (`C:\Users\<user>` / `/home/<user>`) |
| `<project>` | the project root where you run `npx skills add` |
| `<app_id>` / `<app_secret>` | Feishu self-built app credentials |
| `__AGENTHUB_EXAMPLE_MODEL_ID__` | the model name on the gateway, injected by `load.sh` from `.env` |

Vendor tokens (`SLACK_BOT_TOKEN`, `GITHUB_PERSONAL_ACCESS_TOKEN`,
`GOOGLE_OAUTH_CLIENT_ID/SECRET`, `STRIPE_SECRET_KEY`, `DINGTALK_Client_ID/Secret`,
`HF_TOKEN`, `MODELSCOPE_API_TOKEN`) all come from the **server process environment**. They are
listed in the corresponding JSON's `metadata.env_hint` as a reminder; no values are written.

## 6. Caveats

- **Adapter status**: only the model's `openai_compatible` connector is a real implementation
  (`internal/adapter/model_openai.go`); the agent / mcp / skill / cli connectors are still
  `TODO(real)` and only translate shapes. The MCP and CLI samples therefore express
  **configuration intent**, not something executable today.
- **Samples never self-declare health**: none of the 41 samples sets `health` (omitted means
  `unknown`), and none sets `metadata.mock_health`. Health is written by a measurement and by
  nothing else: `POST /api/resources/{id}/probe` asks the resource's own connector, and a run
  records what the call proved. A probe only answers `unknown` where there is genuinely nothing
  to reach — an agent with no Agent Card, a stdio MCP server, a CLI with no command, a skill
  with no endpoint — because inventing a value there is what health stopped doing. An agent that
  does publish a card is fetched for real: a 200 that parses is `healthy`, a credential refusal
  or a 200 that is not a card is `degraded`, and a card nobody serves is `unhealthy`. A CLI probe looks the command up on
  `PATH` without running it: missing is `unhealthy`, found but not allowlisted is `degraded`.
- **Development/demo only**: do not copy these into production. For seeding and production policy
  see the `AGENTHUB_SEED_DEMO` section of the root `AGENTS.md`.
- **Upstream licences** (per each repository's own LICENSE): ADK Apache-2.0, LangGraph MIT,
  CrewAI MIT, pi MIT, deepseek-harness MIT, github-mcp-server MIT,
  @modelcontextprotocol/server-slack MIT, @stripe/mcp MIT, workspace-mcp MIT, wechat-mcp MIT,
  gh MIT, huggingface_hub Apache-2.0, modelscope Apache-2.0, and anthropics/skills and
  vercel-labs/skills as documented in their repositories.

## 7. Deploying an agent on Ubuntu (`deploy-agent.sh`)

`load.sh` registers what an asset *is*; `deploy-agent.sh` makes one of them *answer*. It targets
WSL2 / Ubuntu and takes the agent as its first argument:

```sh
./example/deploy-agent.sh <agent> [start|stop|restart|status|smoke] [options]
```

| Agent | Port | What it installs | How it serves |
| --- | --- | --- | --- |
| `google-adk` | 8001 | `google-adk[a2a]` + `uvicorn` in a venv | `uvicorn a2a_agent:a2a_app --host 127.0.0.1 --port 8001` |

- `start` creates the venv, writes `a2a_agent.py`, serves it with `nohup` and waits for the Agent
  Card (default 60s, `--wait`). Host, port and the card path are read out of the matching
  `resources/agent-<name>-ubuntu.json`, so the script cannot drift from the catalogue, and a
  mismatch with what is registered is called out.
- every option defaults to the repository-root `.env`, falling back to `.env.example` for the keys
  `.env` does not set: `AGENTHUB_EXAMPLE_MODEL_ID` feeds `--model-id`, `AGENTHUB_EXAMPLE_API_BASE`
  feeds `--api-base`, and `AGENTHUB_EXAMPLE_API_KEY` is the gateway key. An exported variable beats
  the file and a flag beats both, so the same `.env` that configures the server also brings the
  agent up without repeating anything.
- the variant follows the credential: a key anywhere (shell, `.env`, `--api-key`) serves the
  gateway-backed agent, and a box without one falls back to the deterministic `BaseAgent` that calls
  no LLM, so the deployment can still be verified before any credential exists. `--offline` forces
  the stub and `--llm` forces the gateway agent.
- the gateway key only travels through the server process environment: it is never written to the
  generated agent, the pid file or the log, and never echoed. Prefer the environment variable or
  `.env` — a value on the command line shows up in `ps`.
- `stop` sends TERM (KILL when it is ignored), `status` reports pid + card reachability + log path
  and exits 1 when nothing is running, and `smoke` sends one `message/send` and prints the reply,
  so a real answer is distinguishable from a mock.
- everything lands in `$HOME/.local/share/agenthub-<agent>` (`--workdir` to override), which keeps
  the venv and the log out of the repository.
- the script never talks to agenthub: no login, no `load.sh`, no `/probe`.

## 8. Deploying MCP / skill / CLI assets on Ubuntu

`load.sh` registers what an asset *is*; §7's `deploy-agent.sh` makes one A2A agent answer. The
other three scripts materialise local assets; the MCP one also publishes an HTTP endpoint. All four
share the same shape —
`set -euo pipefail`, a usage block with `--help`, a parsed (not sourced) root `.env`, the matching
`resources/*-ubuntu.json` as the single source of truth, `start|stop|restart|status|smoke`, a
workdir under `$HOME/.local/share`, `--dry-run`, and no agenthub login, `load.sh` or `/probe`.

| Script | Selectors | `start` | `smoke` |
| --- | --- | --- | --- |
| `deploy-mcp.sh` | 10 servers: `filesystem`, `github`, `google-drive`, `google-docs`, `gmail`, `slack`, `stripe`, `wechat`, `dingtalk`, `feishu` | resolves `kind_config.mcp.command` + `args`, expands `<user>` / `<project>` / `<app_id>` / `<app_secret>`, wraps it with `npx supergateway@latest --outputTransport streamableHttp` (stateless) and serves `POST http://127.0.0.1:8002/mcp` in the background | POSTs MCP `initialize` / `notifications/initialized` / `tools/list` over HTTP (the last one without a session, as agenthub's connector does) and prints the tools from the JSON-RPC reply |
| `deploy-skill.sh` | `anthropics-pdf`, `skillssh-find-skills` | acquires the directory at the registered `file://` endpoint (`git clone --depth 1` or `npx skills add`) and publishes a stable symlink | reads `SKILL.md`, prints its front matter, triggers and tools |
| `deploy-cli.sh` | `gh`, `huggingface`, `modelscope` | resolves the command the way `entryCommand` does, checks PATH and `AGENTHUB_CLI_ALLOWLIST` (whole-string `EqualFold`), writes the deployment state | runs only `--version` / `--help` / `version` |

```sh
./example/deploy-mcp.sh filesystem start --dry-run   # print the resolved plan, start nothing
./example/deploy-mcp.sh filesystem start --wait 120  # first run also downloads supergateway
./example/deploy-mcp.sh filesystem status            # HTTP endpoint, bridge command, log tail
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

- A skill is a directory and a CLI is a binary on PATH: neither has a process, a pid or a port.
  `stop` only unpublishes (removes the symlink / state file) and never deletes an acquired checkout
  or a binary; `status` exits 1 when nothing is deployed.
- `deploy-mcp.sh` never executes an unresolved placeholder. `<app_id>` / `<app_secret>` come from
  `LARK_APP_ID` / `LARK_APP_SECRET`; the secret is masked in every printed command line, but
  Feishu's server takes `-a` / `-s` as argv, so that value is visible in `ps`. Every other
  credential is inherited from the process environment, never written to a pid file or the log.
- `deploy-mcp.sh` deploys the catalogue's **stdio** command through a stateless stdio→Streamable
  HTTP bridge (`supergateway@latest`, `POST /mcp`): agenthub's mcp connector POSTs `tools/list`
  directly, with no `initialize` or `Mcp-Session-Id`. The JSON files keep declaring `stdio`
  because that is what the upstream server speaks; to use the deployed endpoint, register a record
  with `transport=streamable-http` and `endpoint=http://127.0.0.1:8002/mcp`. `--host` / `--port`
  (or `AGENTHUB_EXAMPLE_MCP_HOST` / `AGENTHUB_EXAMPLE_MCP_PORT` in the root `.env`) move the
  listener; the default bind is loopback only, and a non-loopback bind needs an authenticating
  reverse proxy. `stop` signals the whole bridge process group, so neither `npx` nor the stdio
  child is left behind. A leftover FIFO/stdio deployment is detected by `start` / `status` and
  is replaced with `restart`.
- **Multiple MCP servers on one host.** Each server gets its own workdir, pid
  file and process group, so the only thing that collides is the listen port.
  The root `.env`'s `AGENTHUB_EXAMPLE_MCP_HOST` / `AGENTHUB_EXAMPLE_MCP_PORT` are
  global defaults and cannot differ per server, so pass `--port` per server:
  ```sh
  ./example/deploy-mcp.sh filesystem start --port 8002
  ./example/deploy-mcp.sh github     start --port 8003
  ./example/deploy-mcp.sh slack      start --port 8004
  ```
  `--port auto` scans upward from 8002 for a free loopback port, so you can bring
  up several without tracking numbers by hand. The resolved port is recorded in
  the workdir; later `status` / `smoke` / `stop` without `--port` still name the
  right endpoint.
- **Same port, many paths, behind a reverse proxy.** Give each bridge a distinct
  `--http-path` (default `/mcp`) and let a proxy route by path to the right
  backend port, exposing a single 443 with authentication:
  ```sh
  ./example/deploy-mcp.sh filesystem start --port 8002 --http-path /mcp/filesystem
  ./example/deploy-mcp.sh github     start --port 8003 --http-path /mcp/github
  ```
  (Two `supergateway` instances cannot bind the same `127.0.0.1:port`; the script
  refuses to touch an already-listening port it does not own.)
- **agenthub must be told the bridge is safe to call.** agenthub's mcp connector
  uses an SSRF-hardened HTTP client that refuses loopback / private addresses by
  default, so a local bridge on `127.0.0.1` is rejected unless the resource record
  narrows the guard with `egress_allowlist`. Register the endpoint as
  `transport=streamable-http` and list the bridge host there:
  ```json
  {
    "id": "local-filesystem",
    "kind": "mcp",
    "kind_config": {
      "mcp": {
        "transport": "streamable-http",
        "url": "http://127.0.0.1:8002/mcp",
        "egress_allowlist": ["127.0.0.1"]
      }
    }
  }
  ```
  Without `egress_allowlist`, the connector refuses `127.0.0.1` (and any private
  IP / CIDR). The field accepts exact IPs, CIDRs and hostnames only — a wildcard
  or a URL-shaped value is rejected at load time, so it can only narrow the guard,
  never reopen it to caller-supplied URLs.
- `deploy-cli.sh` reads `AGENTHUB_CLI_ALLOWLIST` from the root `.env` / `.env.example` and mirrors
  the server's whole-string comparison. The server reads it once at startup, so restart the server
  after changing it; `--allowlist` overrides the preflight and `--skip-allowlist` records anyway.
- None of these three changes the connector status in §6: the server-side mcp / skill / cli
  connectors are still `TODO(real)`. They verify the local command, directory and tool list — not
  an execution through agenthub.
