# example — Real “Registry resource” configuration samples

A catalogue you can load straight into a running agenthub instance: five kinds of assets
(model / agent / mcp / skill / cli), one set for Windows and one for Ubuntu, **41 JSON files**
in total, plus a re-runnable `load.sh`.

Every file is a **complete `registry.Entry` payload**: the server's `DisallowUnknownFields`,
the four enum checks and the `kind_config` validation prove the fields are written correctly,
and a clean `load.sh` run proves the sample is valid.

```sh
# from the repository root — the examples read the single root .env
cp .env.example .env          # then set AGENTHUB_EXAMPLE_ENABLED=true
                              # and AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh             # picks the platform from uname -s
./example/load.sh --all       # loads both platforms
./example/load.sh --platform windows
```

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
| `anthropics/skills` (sample: `pdf`) | `git clone https://github.com/anthropics/skills` | `file:///home/<user>/skills/pdf` | `file:///C:/Users/<user>/skills/pdf` |
| skills.sh (sample: `find-skills`) | `npx skills add vercel-labs/skills` | `file:///home/<user>/<project>/.agents/skills/find-skills` | `file:///C:/Users/<user>/<project>/.agents/skills/find-skills` |

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
- **No endpoint means `degraded`**: the three CLI samples have no endpoint, so they pin their
  displayed state with `metadata.mock_health: "healthy"`.
- **Development/demo only**: do not copy these into production. For seeding and production policy
  see the `AGENTHUB_SEED_DEMO` section of the root `AGENTS.md`.
- **Upstream licences** (per each repository's own LICENSE): ADK Apache-2.0, LangGraph MIT,
  CrewAI MIT, pi MIT, deepseek-harness MIT, github-mcp-server MIT,
  @modelcontextprotocol/server-slack MIT, @stripe/mcp MIT, workspace-mcp MIT, wechat-mcp MIT,
  gh MIT, huggingface_hub Apache-2.0, modelscope Apache-2.0, and anthropics/skills and
  vercel-labs/skills as documented in their repositories.
