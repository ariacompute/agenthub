# agenthub

[English](README.md) | [中文](README_cn.md)

An interconnection platform built on the **A2A (Agent2Agent) protocol**: it registers, discovers,
routes and orchestrates five heterogeneous asset classes — **model / agent / mcp / skill / cli** —
and exposes them as standard A2A endpoints that any protocol-conforming agent can call.

- Backend: Go 1.27 + `net/http`, single binary (GORM is the only production dependency)
- Frontend: Vue 3 + Vite (bun), **13 languages**, pure black / white / grey monochrome UI
- Persistence: **PostgreSQL + GORM** — resources, tasks, artifacts, spans, the event stream and
  push callback records are all durable
- All five adapters in this release are **deterministic mocks**: no network, no subprocess,
  fully runnable offline

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

## Configuration

See `.env.example`. Main knobs: `DATABASE_URL` (**required**), `AGENTHUB_HTTP_PORT` (default 3500),
`AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` + `AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD`, `AGENTHUB_SESSION_TTL`,
`AGENTHUB_MAX_PARALLEL`, `AGENTHUB_CLI_ALLOWLIST`, `AGENTHUB_CLI_TIMEOUT`,
`AGENTHUB_MAX_OUTPUT_BYTES`, `AGENTHUB_PUSH_RECORD_LIMIT` (default 100),
`AGENTHUB_SEED_DEMO` (default **false**), `AGENTHUB_BACKUP_DIR` (default empty = checkpoints off),
`AGENTHUB_BACKUP_KEEP_LAST` / `AGENTHUB_BACKUP_MAX_AGE_DAYS` (retention).

### Users, sessions and RBAC

The console and the `/api/*` control plane are behind a **session**, not a shared token:

- **Email + password**: `POST /api/auth/register` and `POST /api/auth/login` (the response sets an
  HttpOnly cookie). Passwords are hashed with PBKDF2-HMAC-SHA256; only the derived value is stored.
- **OAuth**: Google, GitHub and any generic OIDC issuer, hand-rolled authorization code flow with
  PKCE (S256) and `state`. A provider without credentials is simply not offered.
- **Bootstrap administrator**: `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` / `..._PASSWORD` is upserted at every
  boot and becomes the **owner**. With neither that nor an existing account, the first registered
  account becomes the owner, so a fresh instance is never unreachable.
- **RBAC**: users → groups → roles → permissions (`resources:read`, `resources:write`, `tasks:read`,
  `tasks:write`, `users:manage`, `roles:manage`, `grants:manage`, `sessions:manage`,
  `backups:manage`). The owner holds
  all of them, which is what keeps a misconfigured instance recoverable.
- **Resource grants**: `POST /api/grants` restricts one resource to a user or group (`read` / `write`).
  A resource **without** a grant stays visible to everyone; the first grant restricts it everywhere —
  listings, exports and the Agent Card — and a direct request to it is answered `404`.
- **Sessions**: server-side rows, revocable (`DELETE /api/auth/sessions/{id}`, or
  `DELETE /api/users/{id}/sessions` for `sessions:manage`). `/healthz` reports
  `auth: session | anonymous | disabled`.

Deleting a user **disables** it: tasks, spans and grants stay for the audit trail.

All of this is available in the console: `/login` for registration and login, `/admin` (visible with
`users:manage`, `roles:manage`, `grants:manage` or `backups:manage`) for users, the permission
matrix, grants and database checkpoints.

No demo data is inserted unless you ask for it: `AGENTHUB_SEED_DEMO=true` registers the five
`demo-*` resources (`demo-model`, `demo-agent`, `demo-mcp`, `demo-skill`, `demo-cli`) at startup,
idempotently, which is what the examples below target. Everything else starts with an empty
registry, and `config.Validate` refuses the combination `AGENTHUB_ENV=prod` + `AGENTHUB_SEED_DEMO=true`.

## Run

```bash
make db-up                                  # start PostgreSQL (host port 5434) and wait until it is ready
make run                                    # defaults to :3500 (Open → Migrate → Seed)
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` is **mandatory**: `config.Validate` refuses to start without it (there is no
database-less fallback). Local value:
`postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable` (see `.env.example`).

Console: `make web-dev` (development) or `make web-build && make run` (the bundle is hosted at `/`
by the Go process).

The registry starts **empty** — nothing is seeded. Set `AGENTHUB_SEED_DEMO=true` to get the five
`demo-*` resources the examples below talk to.

### Version stamping

`make build` and `make run` stamp `config.Version` (the git tag, e.g. `v0.2.0`),
`config.Commit` (the seven character revision), `config.Distance` (commits since the tag)
and `config.Dirty` via `-ldflags -X`. `/healthz` returns them, the Agent Card carries the
version, and the console shows **`version@commit`** (for example `v0.2.0@1a2b3c4`) in the
bottom-left corner of the sidebar, with a second line such as `+3 dirty` whenever the
build is not exactly a clean tag. An unstamped build reports the `dev` sentinel instead
of pretending to be a release. Images take the same values through the `VERSION` /
`COMMIT` / `DISTANCE` / `DIRTY` build args (`make docker-build`).

## Deployment

Both flows below bring **database + service** up — the console is baked into the image and served
at `/` by the Go process. Use `make` when you want the port map afterwards; plain compose needs
nothing but Docker and Compose.

### Make

```bash
make up     # docker compose up -d, then prints the port map
make ports  # re-print the port map of a running stack
make down   # stop; data survives in the agenthub-pgdata volume
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

Everything below applies to both flows.

Compose starts `postgres:16-alpine` first and only launches `agenthub` once it reports healthy
(pgvector is not needed); the server then runs its migrations and seeds itself.
`DATABASE_URL` is injected by compose; the published host port can be overridden with
`POSTGRES_PORT`.

The image is a three-stage build (`oven/bun` builds the console → `golang:1.27-alpine` compiles →
`alpine:3.21` runtime). It runs as a **non-root user** (uid 10001) and uses `/healthz` as its
healthcheck; the response carries a `"database":"up"` field and returns 503 when the database is
unreachable. The container is **stateless**: migrations ship inside the binary and every byte of
state lives in the PostgreSQL volume.

The build pulls npm packages and Go modules, so it needs outbound network (`make docker-build`,
`make compose-build`, or a plain `docker compose build`). Behind a proxy, pass it as `BUILD_PROXY` —
`make docker-build` takes it from `$HTTP_PROXY` and adds `--network=host`, which is what makes a
host-side `127.0.0.1:<port>` resolve inside the build container. Compose builds on the bridge
network where that address does not exist, so the Dockerfile retries
`bun install` and `go mod download` with the proxy variables cleared and falls back to the default
`GOPROXY` mirror (`https://goproxy.cn,direct`; override with `--build-arg GOPROXY=...`); only when
both attempts fail does the build stop with an explicit hint.

**Published images.** Publishing a release builds and pushes
`ariacompute/agenthub-agenthub` to DockerHub (`.github/workflows/release.yml`): `:<release tag>` for
that release, `:latest` for the newest one. The release tag and its commit go into the image as the
`VERSION` / `COMMIT` build args, so the image tag and the `version@commit` reported by `/healthz`
always agree. The workflow needs two repository secrets: `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN`.

```bash
docker pull ariacompute/agenthub-agenthub:<tag>  # e.g. v0.3.0
```

**Required for production:**

```bash
AGENTHUB_ENV=prod
AGENTHUB_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=<strong-random-string>
AGENTHUB_COOKIE_SECURE=true                               # behind HTTPS
AGENTHUB_PUBLIC_BASE_URL=https://hub.example.com
```

With `AGENTHUB_ENV=prod` and no `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL`, `config.Validate` **refuses to
start**: a production instance without an administrator is unrecoverable. The account is upserted at
every boot and never has its password overwritten, so a password changed in the console survives a
restart. Configuration is passed through the compose `.env` file or environment variables;
`AGENTHUB_HTTP_PORT` changes the published host port.

## Desktop

`desktop/` is a **Tauri v2** shell for Windows, macOS (arm64) and Linux. It carries no
backend and no database of its own: it embeds the same console the server serves, and opens it
against an agenthub server you point it at. Everything you can do in a browser — registry, task
trace, topology, administration, SSE streams, downloads — works the same way inside the window.
The published macOS binary is arm64; on an Intel Mac build from source with `make desktop-build`.

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

**How it works.** The window loads `http://127.0.0.1:<random port>`, where a loopback reverse proxy
serves the embedded console and forwards `/api/*`, `/a2a`, `/healthz` and `/.well-known/*` to the
configured server. That is what lets the console keep its relative URLs, its `HttpOnly` session
cookie, its server-sent event streams and its downloads — neither the console nor the Go server
changes by a byte. The proxy strips `Secure` and `Domain` from `Set-Cookie` (a plain-http loopback
origin cannot keep them), pins every cookie to `Path=/`, streams responses without buffering, and
rewrites redirects that point back at the server so an OAuth round trip stays inside the app.

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
make web-test          # cd web && bunx vitest run
```

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
MAX_TIME=60 READY_TIMEOUT=60 ./scripts/test-api.sh  # slow networks / cold boots
```

The script logs in first (registering the first account when the instance is brand new) and asserts
that `/api/*` stays closed without a session, then walks users, roles, the permission matrix and a
resource grant — including the fact that a granted resource disappears from discovery.

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

## Persistence

| Table | Content |
| --- | --- |
| `resources` | The five kinds of entries, `tags/capabilities/auth_schemes/env/metadata` stored as `jsonb` |
| `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` | Task body, outputs, span timeline, the parked `input-required` question and the callback config (children cascade with the task) |
| `events` | Every status / artifact / message / span frame, replayable with `since=<seq>` after a reconnect |
| `push_records` | Every callback the mock notifier captures (task / url / state / config snapshot), capped by `AGENTHUB_PUSH_RECORD_LIMIT` |
| `schema_migrations` | Migration files already applied, skipped at startup |
| `backup_checkpoints` / `backup_schedules` | The checkpoint catalogue and the schedule — **excluded** from every dump and restore, so an operator can still see the archive they restored from |

- Migrations: `migrations/*.sql` embedded with `//go:embed`, executed in filename order, one
  transaction per file; the SQL itself is idempotent.
- Write strategy: tasks / artifacts / spans are **synchronous** (state transitions use a
  conditional update `WHERE state=?` so concurrent writers cannot lose an update); events and push
  records are **best effort** (bounded queue + async writer, failures only warn and **never become
  back pressure on task execution**).
- Reconnect: `GET /api/tasks/{id}/events?since=<seq>` replays every frame after `seq`, then follows
  the live stream.

Integration coverage: idempotent migrations, resource CRUD with jsonb round trips, the full task
lifecycle with cascade deletes, event replay, push record persistence plus the retention cap, and
reading a complete A2A run back from the database. **They skip without a database and never
fabricate results.**

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

## Relationship to mastra

The design references a feature survey of `mastra-ai/mastra` and adopts its **protocol and
interaction shapes**: the MCP Registry entry model, the three workflow control-flow primitives
(then / branch / parallel), the human-in-the-loop → `input-required` mapping, the Studio trial-run
interaction and the observability layering. **No mastra code and no npm dependency is used** —
everything on the Go side is implemented here.

Out of scope: RAG, voice, browser automation, channel integrations, multi-language SDKs and
LLM-as-judge scoring (eval only keeps a hook and produces no scores).

## License

MIT.

## Engineering conventions

This repository follows Harness Engineering:

- [`AGENTS.md`](AGENTS.md): entry point and directory index for agent engineering context
- [`requirements.md`](requirements.md): requirement spec (scope / errors / acceptance, reviewed by
  a human before coding)
- [`task.md`](task.md): implementation checklist
