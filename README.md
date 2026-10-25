# agenthub

[English](README.md) | [中文](README_cn.md)

An interconnection platform built on the **A2A (Agent2Agent) protocol**: it registers, discovers,
routes and orchestrates five heterogeneous asset classes — **model / agent / mcp / skill / cli** —
and exposes them as standard A2A endpoints that any protocol-conforming agent can call.

- Backend: Go 1.27 + `net/http`, single binary (GORM is the only production dependency)
- Frontend: Vue 3 + Vite (bun), **13 languages**, pure black / white / grey monochrome UI
- Persistence: **PostgreSQL + GORM** — resources, tasks, artifacts, spans, the event stream and
  push callback records are all durable
- All five adapters run **offline by default**: no network, no subprocess, fully runnable
  without credentials — a model entry can opt into a real OpenAI-compatible gateway

## Concept map

| Concept | Role | In agenthub |
| --- | --- | --- |
| Model | The brain | `kind=model`, one `Generate/Stream` interface + provider fallback chain |
| Agent | Digital coworker | `kind=agent`, goal driven, can park for human input |
| MCP | Universal adapter | `kind=mcp`, protocol translation only, no business logic; `transport` is descriptive |
| Skill | Job manual | `kind=skill`, versioned process asset: trigger + steps + tool list |
| CLI | Walkie-talkie | `kind=cli`, allowlist → timeout → truncation → structured JSON receipt |

One call: user → Agent → Model plans → Skill loaded → MCP reaches the legacy system → the result
travels back the same path.

## Snapshot

![agenthub console — Registry screen](registry.png)

The Registry screen: eight kinds of screen in one console (registry, task trace, topology,
registries, sources, playground, administration and a built-in documentation page).

## Run

```bash
cp .env.example .env                        # local development: copy the env template, then edit values as needed
# cp .env.sec.example .env                  # hardened production: copy the security template, then replace every <...> placeholder with a real secret
make db-up                                  # start PostgreSQL (host port 5434) and wait until it is ready
make run                                    # defaults to :3500 (Open → Migrate → Seed)
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` is **mandatory**: `config.Validate` refuses to start without it (there is no
database-less fallback). Local value:
`postgres://agenthub:agenthub@localhost:5434/agenthub` (see `.env.example`).

For a hardened production deployment rather than local development, start from the
security-hardened template instead: `cp .env.sec.example .env` (see
[Security hardening](#security-hardening)). It pre-applies every production safeguard below and
ships with placeholder secrets you must replace before the instance can boot.

Console: `make web-dev` (development) or `make web-build && make run` (the bundle is hosted at `/`
by the Go process).

Documentation: the console documents itself on a **`/docs`** screen — concepts, the seven A2A
methods and all 66 HTTP routes. The same content is rendered to
[`docs/docs.md`](docs/docs.md) by `make docs-md`; that file is **generated** from the two modules
the screen reads (`web/src/lib/docs-content.js` and `web/src/lib/copy/docs-copy.js`), so edit those
and re-run rather than editing the markdown.

The registry starts **empty** — nothing is seeded. Set `AGENTHUB_SEED_DEMO=true` to get the five
`demo-*` resources the examples below talk to, or load [`example/`](example/README.md) to fill it
with a realistic catalogue.

### Version stamping

`make build` and `make run` stamp `config.Version` (the git tag, e.g. `v0.2.0`),
`config.Commit` (the seven character revision), `config.Distance` (commits since the tag)
and `config.Dirty` via `-ldflags -X`. `/healthz` returns them, the Agent Card carries the
version, and the console shows **`version@commit`** (for example `v0.2.0@1a2b3c4`) in the
bottom-left corner of the sidebar, with a second line such as `+3 dirty` whenever the
build is not exactly a clean tag. An unstamped build reports the `dev` sentinel instead
of pretending to be a release. Images take the same values through the `VERSION` /
`COMMIT` / `DISTANCE` / `DIRTY` build args (`make docker-build`).

## Examples

[`example/`](example/README.md) registers a realistic catalogue straight from the repository: 41
complete `registry.Entry` payloads plus a re-runnable loader, covering all five kinds in both a
Windows and an Ubuntu shape. `deploy-agent.sh` goes one step further and stands one of the agents
up on WSL2 / Ubuntu, so the `card_url` the catalogue registers really answers.

```bash
cp .env.example .env     # then set AGENTHUB_EXAMPLE_ENABLED=true and
                         # AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh        # picks the platform from uname -s
./example/load.sh --all  # both platforms

./example/deploy-agent.sh google-adk start  # stand one agent up so its card_url answers
./example/deploy-agent.sh google-adk smoke  # print its Agent Card, then send one message/send
```

| Kind | What it registers |
| --- | --- |
| model | an OpenAI-compatible gateway; the credential never reaches the JSON or the database — `kind_config.model.api_key_env` names the environment variable and the server reads the value at call time |
| agent | Google ADK, LangGraph, CrewAI, Pi and DeepSeek Harness |
| mcp | the official filesystem server plus GitHub, Google Drive, Google Docs, Gmail, Slack, Stripe, WeChat, DingTalk and Feishu |
| skill | `anthropics/skills` and [skills.sh](https://www.skills.sh) |
| cli | `gh`, `hf` (HuggingFace) and `modelscope` |

Each platform lands in its own namespace (`example-ubuntu`, `example-windows`) because the dedup key
is `(namespace, name, version)`. The loader POSTs first and falls back to PUT when the record already
exists — PUT is not an upsert — so it is safe to re-run.

`example/README.md` also documents the payload contract (the 21 accepted keys, the `kind` /
`transport` / `health` / `status` enums and `kind_config`), the upstream install and start commands
per platform, and the two settings people trip over: `AGENTHUB_EXAMPLE_API_KEY` for the gateway and
`AGENTHUB_CLI_ALLOWLIST` for the CLI commands.

`deploy-agent.sh` is deliberately separate from the loader: it never logs in, never touches the
database and never calls `/probe` — it only installs, serves and verifies one agent, which is what
`Try run` and a health probe need before they can report anything measured.

## Deployment

Both flows below bring **database + service** up — the console is baked into the image and served
at `/` by the Go process. Use `make` when you want the port map afterwards; plain compose needs
nothing but Docker and Compose.

### Make

```bash
cp .env.example .env  # local development: copy the env template, then edit values as needed
# cp .env.sec.example .env  # hardened production: copy the security template, then replace every <...> placeholder with a real secret
make up               # docker compose up -d, then prints the port map
make ports            # re-print the port map of a running stack
make down             # stop; data survives in the agenthub-pgdata volume
```

Compose has no post-up hook, so `make up` prints the map once the stack is healthy, and
`make ports` prints it again later — which host port answers, and what it serves:

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

Everything above is one listener inside the container (always 3500); the published host port is
compose-only knowledge, hence the table. The server itself logs the same endpoint list on boot, so
an attached `docker compose up` or `docker compose logs -f agenthub` spells it out too.

### Docker compose

```bash
docker compose up -d                # build if needed, then start postgres + agenthub, :3500 / :5434
docker compose ps                   # STATUS should be Up (healthy)
docker compose logs -f agenthub
docker compose --profile dev up -d  # also starts the web dev server on :5173
docker compose down                 # stop; data survives in the agenthub-pgdata volume
```

Two differences from the `make` flow:

* **No port map.** Nothing is printed once the stack is up. `make ports` still works here — it only
  reads the running containers — so use it whenever you want the table shown above.
* **No build stamp.** `.git` is excluded by `.dockerignore`, so an image built this way reports the
  `dev` / `unknown` sentinels in `/healthz` instead of a release. Pass the args yourself (or use
  `make compose-build`) to stamp it:

```bash
VERSION=$(git describe --tags --abbrev=0 --always) \
COMMIT=$(git rev-parse --short=7 HEAD) \
DISTANCE=$(git rev-list --count "$(git describe --tags --abbrev=0)"..HEAD) \
  docker compose build agenthub
```

If the build hangs on `apk add` — the runtime stage fetching from
`dl-cdn.alpinelinux.org` — switch the mirror it installs from:

```bash
ALPINE_MIRROR=https://mirrors.aliyun.com docker compose up --build
# or: ALPINE_MIRROR=https://mirrors.tuna.tsinghua.edu.cn
```

**Published images.** Publishing a release builds and pushes
`ariacompute/agenthub-agenthub` to DockerHub (`.github/workflows/release.yml`): `:<release tag>` for
that release, `:latest` for the newest one. The release tag and its commit go into the image as the
`VERSION` / `COMMIT` build args, so the image tag and the `version@commit` reported by `/healthz`
always agree. The workflow needs two repository secrets: `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN`.

```bash
docker pull ariacompute/agenthub-agenthub:<tag>  # e.g. v0.3.0
```

**Required for production:** A ready-made hardened template covering every variable below — plus the
SSRF-guarded registry sync and signed A2A push settings — ships as `.env.sec.example`. Copy it with
`cp .env.sec.example .env`, then replace each `<...>` placeholder with a secret generated out of band
(`openssl rand -base64 48`). The template is intentionally invalid until those placeholders are set,
so a hardened instance cannot boot with committed secrets.

```bash
AGENTHUB_ENV=prod
AGENTHUB_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=<strong-random-string>
AGENTHUB_COOKIE_SECURE=true                               # behind HTTPS
AGENTHUB_PUBLIC_BASE_URL=https://hub.example.com
AGENTHUB_ENCRYPTION_KEY=<32+ byte random secret>          # required in prod; backs static encryption + backup at-rest
# AGENTHUB_MIGRATOR_DATABASE_URL=postgres://migrator@db:5432/agenthub  # optional: separate high-privilege migration role
# AGENTHUB_PUSH_SECRET=<16+ byte random secret>           # optional: signed, SSRF-guarded A2A push callbacks
# AGENTHUB_DB_MAX_OPEN_CONNS=25 AGENTHUB_DB_MAX_IDLE_CONNS=10 AGENTHUB_DB_CONN_MAX_LIFETIME=30m  # pool tuning
```

With `AGENTHUB_ENV=prod` and no `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL`, `config.Validate` **refuses to
start**: a production instance without an administrator is unrecoverable. The account is upserted at
every boot and never has its password overwritten, so a password changed in the console survives a
restart. Configuration is passed through the compose `.env` file or environment variables;
`AGENTHUB_HTTP_PORT` changes the published host port.

## Security hardening

A defence-in-depth pass, adapted from the ariacompute/serve hardening and shaped to agenthub's
architecture (no nginx, DB-backed sessions, A2A push callbacks and upstream synchronisation as
server-side outbound requests). Full rationale and acceptance criteria are in
`requirements-security.md`; the implementation record is `task-security.md`. Highlights:

**Transport / edge.** agenthub is a single Go binary, so the security response headers live in a
middleware (`internal/middleware`): `X-Content-Type-Options`, `Referrer-Policy`, `COOP`/`CORP`, and a
grace-period `Content-Security-Policy-Report-Only`. HSTS is emitted only when `AGENTHUB_ENV=prod` or
the request is already TLS-terminated (`r.TLS` or `X-Forwarded-Proto: https`). **Put a TLS-terminating
reverse proxy (Caddy/nginx) in front of agenthub in production** — the binary itself does not terminate
TLS.

**Authentication & rate limiting.**
- Brute-force protection: **5** failed logins lock an account for **15 minutes**; a successful login
  clears the counter and lock. Unknown accounts always return the same error, so they cannot be
  enumerated. (`migrations/011_login_protection.sql`)
- Per-IP rate limiting: auth endpoints are capped at **10 req/min/IP**, everything under `/api` and
  `/a2a` at **200 req/min/IP** (`429` + `Retry-After`).
- CSRF: state-changing requests must carry a same-origin `Origin` (loopback and non-browser clients
  such as the Tauri desktop proxy and scripts are exempt), else `403`. The console uses same-origin
  fetch with the session cookie, so it is unaffected.
- Audit logs record only IP and user id — never passwords, tokens or PII.

**A2A callbacks & upstream sync (SSRF).** Push notifications now actually POST to the caller-supplied
`URL` (`HTTPNotifier` replaces the old record-only mock). The destination is https-only and never
resolves to a private/loopback/link-local address (`internal/netutil`), and when `AGENTHUB_PUSH_SECRET`
is set the payload is HMAC-SHA256 signed (timestamp + nonce + body) so the receiver can verify
authenticity, freshness and replay; `(taskId, state)` is de-duplicated for one hour. The upstream
registry sync client uses the same guarded client.

**Secrets & data.** `AGENTHUB_ENCRYPTION_KEY` (**≥ 32 bytes in production**, else the server refuses
to start) isolates any future static encryption from the rest of the system; a missing key falls back
to the session key with a startup warning. API keys are stored hashed (`token_hash`) and OAuth client
secrets are environment-variable references, never database rows — an injection audit confirmed every
SQL statement is parameterised.

**Database.** Run-time connections should use a least-privilege role; migrations can run as a
separate role via `AGENTHUB_MIGRATOR_DATABASE_URL`. Provision the roles with
`scripts/provision-db-roles.sql` (`app` / `migrator` / `readonly`). The connection pool is now
tunable (`AGENTHUB_DB_MAX_OPEN_CONNS` / `_MAX_IDLE_CONNS` / `_CONN_MAX_LIFETIME`).

**Backups.** When `AGENTHUB_ENCRYPTION_KEY` is set, checkpoint archives are encrypted at rest
(AES-256-GCM) into `<file>.tar.gz.enc` with a `<file>.enc.sha256` manifest — plaintext never touches
disk. Verify recoverability periodically with `scripts/backup-restore-drill.sh`.

## Desktop

`desktop/` is a **Tauri v2** shell for Windows, macOS (arm64) and Linux. It carries no
backend and no database of its own: it embeds the same console the server serves, and opens it
against an agenthub server you point it at. Everything you can do in a browser — registry,
registry governance, task trace, topology, administration, SSE streams, downloads — works the same
way inside the window. The published macOS binary is arm64; on an Intel Mac build from source with
`make desktop-build`.

The shell only ever looks at a **path prefix**: `/api`, `/a2a`, `/healthz` and `/.well-known` go to
the server, everything else falls back to the console's `index.html`. A new control plane endpoint
or a new console screen therefore needs no change here — only a rebuilt console bundle, which
`make desktop-dev` and `make desktop-build` both produce.

```bash
make desktop-install  # cd desktop && bun install
make desktop-dev      # builds the console, then runs `tauri dev`
make desktop-build    # builds the console, then runs `tauri build`
make desktop-test     # cd desktop/src-tauri && cargo test
```

**Connecting.** The first start shows the connection page: enter the server address
(`https://hub.example.com`), tick *ignore self-signed certificates* only for a server on your own
network, choose one of the 13 languages, and press **Connect** — the shell probes `/healthz` before
it saves anything. After that the tray carries *Open console*, *Connection settings…*, *About* and
*Quit*; closing the window parks the app in the tray instead of ending it. The setting lives in the
shell's own `config.json` under the per-user config directory.

**Unsigned builds.** The published binaries are not code-signed, so the first launch is stopped by
the platform:

| Platform | What to do |
| --- | --- |
| macOS | *System Settings → Privacy & Security → Open Anyway*, or `xattr -d com.apple.quarantine /Applications/agenthub.app` after copying it out of the archive |
| Windows | SmartScreen: *More info → Run anyway* |
| Linux | Nothing to bypass: install the `.deb`, or unpack the `.tar.gz` — the binary keeps its executable bit |

Every asset ships with a `SHA256SUMS.txt` next to it.

## Tests

Unit and integration suites:

```bash
make db-up             # integration cases need a real PostgreSQL
make test              # go test -race ./... (integration cases skip without DATABASE_URL)
make test-integration  # only integration_test.go
make test-cover        # -coverprofile + go tool cover -func, per package and in total
make web-test          # cd web && bunx vitest run
```

Coverage is measured per package. Two things are worth knowing before reading the numbers: the
governance rules are driven through in-package fakes, so the store-backed branches of `registry`
are covered without a database, while `internal/store` only measures once `DATABASE_URL` is set —
its cases skip rather than fabricate.

End-to-end API test against a running instance — the scripted form of every `curl`-able endpoint:
health and discovery, the whole `/api` control plane (resources CRUD, exports, tasks, spans, event
replay), the A2A methods (send, get, streaming, push notifications, input-required → resume →
cancel), the JSON-RPC error contract and the console bundle. One PASS / FAIL line per assertion and
a non-zero exit as soon as one of them fails, so CI can gate on it:

```bash
AGENTHUB_SEED_DEMO=true make up                     # the test targets the demo-* resources
AGENTHUB_SEED_DEMO=true make run                    # or locally
./scripts/test-api.sh                               # defaults to http://127.0.0.1:3500
BASE_URL=https://hub.example.com ./scripts/test-api.sh
AGENTHUB_TEST_EMAIL=me@example.com AGENTHUB_TEST_PASSWORD='…' ./scripts/test-api.sh
AGENTHUB_API_KEY=ahk_xxx ./scripts/test-api.sh      # authenticate with a long-lived key
MAX_TIME=60 READY_TIMEOUT=60 ./scripts/test-api.sh  # slow networks / cold boots
```

The script logs in first (registering the first account when the instance is brand new) and asserts
that `/api/*` stays closed without a session, then walks users, roles, the permission matrix and a
resource grant — including the fact that a granted resource disappears from discovery.

Alternatively, set `AGENTHUB_API_KEY=ahk_…` to authenticate every request with a long-lived bearer key
instead of logging in: the script skips login and uses the key as its identity, and the API-keys section
then verifies the imported key rather than minting and revoking one (so it never disables the credential
the run depends on). The key must carry every permission the run exercises — for a full smoke test that
means owner-equivalent scopes.

It also covers the catalogue governance: creating a namespace, registering into it and watching the
record go `draft → pending → approved` (a draft must stay out of the discoverable list, and
`draft → approved` has to be refused), the `(namespace, name, version)` dedup key, and an upstream
registry source syncing twice through the offline fixture — imported on the first run, refreshed on
the second.

It targets the seeded resources (`demo-model` / `demo-agent` / `demo-mcp` / `demo-skill` /
`demo-cli`), needs nothing beyond `curl` and `python3`, deletes the resource it creates, and exits
2 when the service never answers `/healthz`.

## A2A endpoints

| Endpoint | Description |
| --- | --- |
| `GET /.well-known/agent.json` | Agent Card (`GET /a2a/agent-card` is a compatibility path) |
| `POST /a2a` | JSON-RPC 2.0 single entry point |
| `GET /api/resources` etc. | Console control plane (`/api/*`) |
| `GET /healthz` | Health and mode |

Methods: `message/send`, `message/stream` (SSE), `tasks/get`, `tasks/cancel`, `tasks/resume`,
`tasks/pushNotification/set`, `tasks/pushNotification/get`; with `AGENTHUB_A2A_LEGACY_METHODS=true`
the v0.1 aliases `tasks/send` and `tasks/sendSubscribe` are accepted as well.

Streaming has **no `[DONE]` sentinel**: a run ends with the
`completed / failed / canceled / input-required` status event.

## Flow primitives

`metadata.flow` accepts three primitives for relaying a task across resources:

```json
[
  {"op":"then","resource":"demo-model"},
  {"op":"branch","when":"contains:leave",
   "steps":[{"op":"parallel","steps":[{"op":"then","resource":"demo-skill"},{"op":"then","resource":"demo-cli"}]}]},
  {"op":"then","resource":"demo-mcp"}
]
```

Branch conditions: `""` (default arm) / `always` / `contains:<text>` / `kind:<kind>` / `has:<key>`.

## Error contract

The backend returns **machine readable codes only**, never prose; the console maps each code to
localised copy on the client.

- HTTP: `{"code":"..."}` (`bad_request` / `unauthorized` / `not_found` / …)
- JSON-RPC: standard codes plus the A2A specific `-32001 task_not_found`,
  `-32002 task_not_cancelable`, `-32003 push_notification_not_supported`,
  `-32004 unsupported_operation`, `-32005 invalid_state_transition`,
  `-32006 resource_not_found`, `-32007 resource_unreachable`, `-32008 task_not_resumable`

## API keys

A key is a long-lived bearer credential for non-interactive access — scripts, other
languages, CI. It authenticates as its **owner** but only with the **permissions
chosen at creation**, and the raw token is returned exactly once; only its `sha256`
is ever stored (`token_hash`), never the secret.

- **Mint**: `POST /api/api-keys` with `{ "name", "scopes": [...], "expiresInDays"? }`.
  The response carries `token` (an `ahk_…` string) once — copy it then.
- **Use**: send it as `Authorization: Bearer ahk_…` on any `/api/*` route, or as the
  symmetric `X-AgentHub-Key` header, and on the A2A endpoint. A key's permissions are
  the intersection of its scopes and what the owner can do, so it can never exceed the
  owner.
- **Manage**: `GET /api/api-keys` lists your own keys (or every key if you hold
  `apikeys:manage`); `PUT /api/api-keys/{id}` rotates name / scopes / expiry;
  `DELETE /api/api-keys/{id}` revokes — the token dies immediately but the row stays
  for the audit trail.

```bash
# Create a key that can read resources and tasks, then call the control plane with it.
TOKEN=$(curl -s -X POST localhost:3500/api/api-keys \
  -H 'Content-Type: application/json' \
  -H 'X-AgentHub-Session: <session>' \
  -d '{"name":"ci","scopes":["resources:read","tasks:read"]}' | jq -r .token)
curl -s localhost:3500/api/resources -H "Authorization: Bearer $TOKEN" | jq .
```

## Persistence

| Table | Content |
| --- | --- |
| `resources` | Entries of the five kinds plus `custom`, with `namespace` / `status` / `display_name` / `source_id` / `kind_config` and `tags/capabilities/auth_schemes/env/metadata` stored as `jsonb`; unique on `(namespace, name, version)` |
| `registries` | Registry namespaces, each with its own approval policy (`auto_approve`) |
| `registry_sources` | Upstream registry sources plus the outcome of their last sync (`last_sync_at/status/count/error`) |
| `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` | Task body, outputs, span timeline, the parked `input-required` question and the callback config (children cascade with the task) |
| `events` | Every status / artifact / message / span frame, replayable with `since=<seq>` after a reconnect |
| `push_records` | Every callback the mock notifier captures (task / url / state / config snapshot), capped by `AGENTHUB_PUSH_RECORD_LIMIT` |
| `schema_migrations` | Migration files already applied, skipped at startup |
| `backup_checkpoints` / `backup_schedules` | The checkpoint catalogue and the schedule — **excluded** from every dump and restore, so an operator can still see the archive they restored from |
| `api_keys` / `api_key_scopes` | Long-lived bearer credentials; only the `sha256` of the token (`token_hash`) and a display `prefix` are stored (never the secret), with an optional `expires_at` and a soft `revoked_at`; `api_key_scopes` is the key → permission edge |

- Migrations: `migrations/*.sql` embedded with `//go:embed`, executed in filename order, one
  transaction per file; the SQL itself is idempotent.
- Write strategy: tasks / artifacts / spans are **synchronous** (state transitions use a
  conditional update `WHERE state=?` so concurrent writers cannot lose an update); events and push
  records are **best effort** (bounded queue + async writer, failures only warn and **never become
  back pressure on task execution**).
- Reconnect: `GET /api/tasks/{id}/events?since=<seq>` replays every frame after `seq`, then follows
  the live stream.

Integration coverage: idempotent migrations, resource CRUD with jsonb round trips, the full task
lifecycle with cascade deletes, event replay, push record persistence plus the retention cap,
reading a complete A2A run back from the database, and the governance cases (namespace CRUD,
governance column round trips, the dedup key, source CRUD and sync bookkeeping, the in-memory and
store-backed facades agreeing). **They skip without a database and never fabricate results.**

## Database checkpoints

The console's **Backups** tab (`/admin`, `backups:manage`) takes a complete dump of the agenthub
schema into one `tar.gz` on disk and can rewind the database to any of them.

- **Archive**: `manifest.json` (tables, row counts, engine, the migration set it was taken at) plus
  the payload, in one file. The file's sha256 is stored on the catalogue row and re-checked before a
  download and before a restore, so an archive that rotted on its volume is refused rather than
  handed over.
- **Engines**: `pg_dump -Fc` when the PostgreSQL client is installed in the runtime image, otherwise
  a pure-Go engine that streams `COPY … TO STDOUT` / `COPY … FROM STDIN`. The engine travels with the
  archive; an archive can only be loaded by the engine that wrote it.
- **Schedule** (edited in the console, persisted): off, every N minutes, daily at a fixed time in a
  timezone, or a five-field cron expression. The scheduler catches up once after a restart, and rows
  left `running` by a process that died are closed at the next boot.
- **Retention**: keep the newest *N* checkpoints and delete anything older than *M* days. The newest
  checkpoint is never swept, and the automatic checkpoint a restore takes first is exempt for 24
  hours — the way back must survive a cleanup run.
- **Restore guardrails**, in order: the operator retypes the checkpoint id → the checksum is verified
  → the archive's migration set is compared against the running build (an archive from a *newer*
  schema is refused; an older one is loaded and then migrated) → a checkpoint of the current
  database is taken → the hub stops accepting new tasks and waits for the running ones to finish →
  the tables are rewritten → in-memory state is reloaded. No restart, and every failure leaves the
  database as it was.
- **Configuration**: `AGENTHUB_BACKUP_DIR` is the switch. Without it the tab reports the feature as
  disabled and nothing else changes — the default in `docker-compose.yml`, where the
  `agenthub-backups` volume is mounted but left unused until you set the variable.
- **Encryption at rest**: when `AGENTHUB_ENCRYPTION_KEY` is set, the archive is written as
  `<file>.tar.gz.enc` (AES-256-GCM, plaintext never on disk) with a `<file>.enc.sha256` manifest. The
  integrity check then covers the ciphertext. Validate recoverability with
  `scripts/backup-restore-drill.sh` (decrypts into a throwaway database and reports key-table row counts).

## AWS Agent Registry comparison

An item-by-item capability comparison against
[AWS Agent Registry](https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/registry.html)
([concepts and terminology](https://docs.aws.amazon.com/bedrock-agentcore/latest/devguide/registry-concepts.html)).

| | AWS Agent Registry | agenthub |
| --- | --- | --- |
| What it is | A **catalogue and discovery service**; executing a record belongs to Runtime / Gateway | **Catalogue and execution in one**: a registered record is callable over A2A immediately |
| First-class citizens | Registry (catalogue) → Registry record (entry) | Namespace (`registries`) → record (`resources`) |
| Protocol | Exposes the catalogue through a native MCP endpoint; validates MCP and A2A schemas | Speaks A2A (`POST /a2a`); the catalogue is **exported** as MCP server descriptions and an Agent Card |

agenthub has always had the half AWS leaves to another service — register a record and it runs,
through one of five adapters. What it lacked was the catalogue half: curation, namespaces and
synchronisation from an external registry.

### Record types and validation

| AWS Agent Registry | agenthub |
| --- | --- |
| Two levels: Registry (name / description / authorization / approval) → Registry record | Namespace → record; a namespace carries its own approval policy |
| `recordType` ∈ `AGENT` / `MCP` / `SKILL` / `CUSTOM` | `kind` ∈ `model` / `agent` / `mcp` / `skill` / `cli` / `custom` |
| MCP record = server definition + tool schemas, validated against the MCP protocol schema | MCP block: transport (stdio / streamable-http / sse-legacy), command+args or URL, tool allowlist, auth schemes, env — validated per kind |
| Agent record = A2A Agent Card, validated against the Agent Card schema | Agent block: card URL, protocol version, capabilities, skills, auth |
| Skill record = name / description / package / repository / markdown | Skill block: version, repository / package, triggers, steps, tool list, markdown |
| Custom record = any valid JSON | `custom` kind: a free-form JSON schema |
| `name`, optional `displayName`, `recordVersion` | `name`, `display_name`, `version` |
| Dedup key `name + recordVersion` inside a registry; the same name may be republished at a new version | Dedup key `(namespace, name, version)` behind a unique index; the same name may be republished at a new version |
| — (no model or CLI type) | `model` (provider fallback chain, optionally a real OpenAI-compatible router) and `cli` (allowlist → timeout → truncation) are agenthub's own |

### Lifecycle and governance

| AWS Agent Registry | agenthub |
| --- | --- |
| Draft → submit → curator **approve / reject** (with feedback) → **deprecate** | `draft → pending → approved / rejected → deprecated`; an illegal transition is a 400 |
| Consumers only ever see approved records | `registry.Discoverable` is the single filter the listing, the MCP export, the Agent Card and A2A addressing share |
| Four roles: Administrator / Publisher / Curator / Consumer | RBAC (owner / groups / roles / permissions) plus resource grants, with `registries:manage`, `registry:curate` and `sources:manage` for the catalogue |
| Namespaces split by type, environment or team | Namespaces, each with its own approval policy and visibility boundary |

### Discovery

| AWS Agent Registry | agenthub |
| --- | --- |
| **Hybrid search**: semantic + keyword | Keyword search only — see *deliberately not* below |
| **Catalogue browsing**: paginated list, filter by recordType, `Get` / `BatchGet` | `GET /api/resources?kind=&q=&namespace=&status=`; no pagination and no batch read yet |
| **Native MCP endpoint** (`InvokeRegistryMcp`) any MCP client can call | `GET /api/export/mcp` **exports** MCP server descriptions; agenthub does not run an MCP server |
| Agent Card | `GET /api/export/agent-card` and `/.well-known/agent.json`, one advertised skill per approved record |

### Authorization and synchronisation

| AWS Agent Registry | agenthub |
| --- | --- |
| Inbound: IAM or JWT | Session cookie → `Principal`; `/api/*` behind `requirePermission`, A2A behind `identify` |
| Outbound: a **credential provider** (OAuth credential or IAM role) for reaching remote endpoints | `AuthScheme` (none / bearer) plus `auth_token_env`, which stores the environment variable **name** only |
| **URL-based synchronisation**, keeping records in step with remote MCP / A2A endpoints | A source (mcp or skill) pulls on demand or on an interval, merges on `(name, version)` and records the outcome; the fixture client keeps it offline until an operator opts out |

### Operations and observability

| AWS Agent Registry | agenthub |
| --- | --- |
| EventBridge notifications (a record submitted for approval) | SSE broker plus the `events` table, replayable with `since=<seq>` — equivalent in kind, agenthub's own implementation |
| CloudTrail auditing of every API call | `slog` structured logs (no secrets, no user content, no vectors); a weaker audit surface, deliberately left alone |
| Organizations, cross-account RAM sharing | One instance, multi-tenant by principal and resource grants; AWS-only constructs that are not replicated |
| VPC / PrivateLink, service-linked role | Not applicable to a self-hosted binary |
| Key-value tags (cost attribution, access control) | `tags []string` plus a `metadata` map — enough for the use |
| — | Database checkpoints with scheduled dumps and in-place restore (`internal/backup`): no AWS counterpart, agenthub is ahead here |

### What agenthub had, what it added, what it refuses

**Already had** — register-and-run adapters for all five kinds (deterministic, offline); the A2A
endpoint with the full task state machine, including `input-required` parked indefinitely and
resumed with `tasks/resume`; event fan-out over SSE with `since` replay; push callback records;
catalogue export as MCP server descriptions and an Agent Card; an auth kernel (PBKDF2 passwords,
sessions, OAuth with PKCE) and RBAC with resource grants; database checkpoints; 13 languages and a
monochrome UI, on the web and on the desktop.

**Added for this comparison** — per-kind configuration and validation for all six kinds; a model
router that can point at an OpenAI-compatible gateway; upstream registry synchronisation with dedup
and recorded outcomes; the approval lifecycle with consumers limited to approved records; multiple
namespaces; the `(namespace, name, version)` dedup key; and the `custom` record type.

**Deliberately not** —

| Not doing | Why |
| --- | --- |
| Semantic / hybrid search | It needs an embedding model or an external service, which conflicts with "no new production dependency, runs offline". If it is ever needed it arrives as an external search source, not inside the catalogue. |
| Running an MCP JSON-RPC server (`InvokeRegistryMcp`) | agenthub's protocol is A2A; the catalogue already exports MCP server descriptions for anyone who wants them. A second protocol server would split the contract. |
| CloudTrail-equivalent auditing (every API call) | It overlaps `slog` and the `events` table; it belongs to an observability milestone of its own, not to catalogue governance. |
| Organizations, cross-account RAM sharing, VPC PrivateLink, service-linked roles | They are bound to AWS infrastructure with no cross-cloud counterpart; multi-tenancy already comes from the principal plus resource grants. |
| Inbound authorization beyond sessions and RBAC | Session + RBAC already covers a self-hosted deployment. |
| A full OAuth authorization-code flow per record | Synchronisation only needs a bearer token held in an environment variable; the full flow belongs to the "real adapters" milestone. |

### recordType ↔ Kind

| AWS recordType | Validated against | agenthub kind | Per-kind configuration |
| --- | --- | --- | --- |
| `MCP` | MCP protocol schema | `mcp` | transport, command+args or URL, tool allowlist, auth schemes, env |
| `AGENT` | A2A Agent Card schema | `agent` | card URL, protocol version, capabilities, skills, auth |
| `SKILL` | descriptor (name / description / package / repository / markdown) | `skill` | version, repository / package, triggers, steps, tool list, markdown |
| `CUSTOM` | any valid JSON | `custom` | JSON schema |
| — | — | `model` | provider mode, endpoint, model id, api key env, fallback chain with weights, timeout |
| — | — | `cli` | command, allowlist, timeout, output truncation, working directory, env |

### Remaining gaps

1. **Semantic retrieval** (medium): keyword search recalls poorly once a catalogue runs to thousands
   of records; the fix would be an external search source rather than an embedded model.
2. **Batch read and pagination** (medium): the listing has neither yet, which will slow the console
   down as the catalogue grows.
3. **Audit surface** (low): no record of every API call, which compliance scenarios ask for.
4. **Schedule expressiveness** (low): a sync schedule is a hand-rolled five-field cron, less
   expressive than EventBridge rules but enough for a self-hosted instance.

## License

MIT.

## Engineering conventions

This repository follows Harness Engineering:

- [`AGENTS.md`](AGENTS.md): entry point and directory index for agent engineering context
- [`requirements.md`](requirements.md): requirement spec (scope / errors / acceptance, reviewed by
  a human before coding)
- [`task.md`](task.md): implementation checklist
- [`docs/docs.md`](docs/docs.md): the console's own documentation, generated by `make docs-md`
