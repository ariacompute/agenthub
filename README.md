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

## Run

```bash
make db-up                # start PostgreSQL (host port 5434) and wait until it is ready
make run                  # defaults to :3500 (Open → Migrate → Seed)
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` is **mandatory**: `config.Validate` refuses to start without it (there is no
database-less fallback). Local value:
`postgres://agenthub:agenthub@localhost:5434/agenthub?sslmode=disable` (see `.env.example`).

Console: `make web-dev` (development) or `make web-build && make run` (the bundle is hosted at `/`
by the Go process).

### Version stamping

`make build` and `make run` stamp `config.Version` (the git tag, e.g. `v0.2.0`),
`config.Commit` (the seven character revision), `config.Distance` (commits since the tag)
and `config.Dirty` via `-ldflags -X`. `/healthz` returns them, the Agent Card carries the
version, and the console shows **`version@commit`** (for example `v0.2.0@1a2b3c4`) in the
bottom-left corner of the sidebar, with a second line such as `+3 dirty` whenever the
build is not exactly a clean tag. An unstamped build reports the `dev` sentinel instead
of pretending to be a release. Images take the same values through the `VERSION` /
`COMMIT` / `DISTANCE` / `DIRTY` build args (`make docker-build`).

## Persistence

| Table | Content |
| --- | --- |
| `resources` | The five kinds of entries, `tags/capabilities/auth_schemes/env/metadata` stored as `jsonb` |
| `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` | Task body, outputs, span timeline, the parked `input-required` question and the callback config (children cascade with the task) |
| `events` | Every status / artifact / message / span frame, replayable with `since=<seq>` after a reconnect |
| `push_records` | Every callback the mock notifier captures (task / url / state / config snapshot), capped by `AGENTHUB_PUSH_RECORD_LIMIT` |
| `schema_migrations` | Migration files already applied, skipped at startup |

- Migrations: `migrations/*.sql` embedded with `//go:embed`, executed in filename order, one
  transaction per file; the SQL itself is idempotent.
- Write strategy: tasks / artifacts / spans are **synchronous** (state transitions use a
  conditional update `WHERE state=?` so concurrent writers cannot lose an update); events and push
  records are **best effort** (bounded queue + async writer, failures only warn and **never become
  back pressure on task execution**).
- Reconnect: `GET /api/tasks/{id}/events?since=<seq>` replays every frame after `seq`, then follows
  the live stream.

## Docker deployment

One command brings **database + service** up (the console is baked into the image and served at `/`
by the Go process):

```bash
docker compose up -d                 # build and start postgres + agenthub, :3500 / :5434
docker compose ps                    # STATUS should be Up (healthy)
docker compose logs -f agenthub
docker compose down                  # stop; data survives in the agenthub-pgdata volume
```

Compose starts `postgres:16-alpine` first and only launches `agenthub` once it reports healthy
(pgvector is not needed); the server then runs its migrations and seeds itself.
`DATABASE_URL` is injected by compose; the published host port can be overridden with
`POSTGRES_PORT`.

Frontend development with hot reload (optional; the dev server proxies through the compose
service name):

```bash
docker compose --profile dev up -d   # also starts web on :5173
```

The image is a three-stage build (`oven/bun` builds the console → `golang:1.27-alpine` compiles →
`alpine:3.21` runtime). It runs as a **non-root user** (uid 10001) and uses `/healthz` as its
healthcheck; the response carries a `"database":"up"` field and returns 503 when the database is
unreachable. The container is **stateless**: migrations ship inside the binary and every byte of
state lives in the PostgreSQL volume.

The build pulls npm packages and Go modules, so it needs outbound network. Behind a proxy, pass it
as `BUILD_PROXY` — `make docker-build` takes it from `$HTTP_PROXY` and adds `--network=host`, which
is what makes a host-side `127.0.0.1:<port>` resolve inside the build container. A compose build
runs on the bridge network where that address does not exist, so the Dockerfile retries
`bun install` and `go mod download` with the proxy variables cleared and falls back to the default
`GOPROXY` mirror (`https://goproxy.cn,direct`; override with `--build-arg GOPROXY=...`); only when
both attempts fail does the build stop with an explicit hint.

**Required for production:**

```bash
AGENTHUB_ENV=prod
AGENTHUB_ADMIN_TOKEN=<strong-random-string>
```

With `AGENTHUB_ENV=prod` and no `AGENTHUB_ADMIN_TOKEN`, `config.Validate` **refuses to start**
(an open control plane is not allowed). Once set, `/api/*` requires
`Authorization: Bearer <token>`. Configuration is passed through the compose `.env` file or
environment variables; `AGENTHUB_HTTP_PORT` changes the published host port.

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

### Smoke test

```bash
# 1) discovery
curl -s localhost:3500/.well-known/agent.json | head

# 2) send a task (targets demo-mcp)
curl -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":1,"method":"message/send",
  "params":{"message":{"role":"user","parts":[{"type":"text","text":"look up last week leave records"}]},
            "metadata":{"resourceId":"demo-mcp"}}}'

# 3) streaming (SSE)
curl -N -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":2,"method":"message/stream",
  "params":{"message":{"role":"user","parts":[{"type":"text","text":"apply half-day annual leave tomorrow afternoon"}]},
            "metadata":{"flow":[{"op":"then","resource":"demo-skill"},{"op":"then","resource":"demo-mcp"}]}}}'

# 4) park and resume (the agent parks on approve / confirm)
curl -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":3,"method":"message/send",
  "params":{"message":{"role":"user","parts":[{"type":"text","text":"approve the change"}]},
            "metadata":{"resourceId":"demo-agent"}}}'
curl -s localhost:3500/a2a -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":4,"method":"tasks/resume",
  "params":{"id":"<taskId>","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}}'
```

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

## Configuration

See `.env.example`. Main knobs: `DATABASE_URL` (**required**), `AGENTHUB_HTTP_PORT` (default 3500),
`AGENTHUB_ADMIN_TOKEN` + `AGENTHUB_AUTH_OPEN_MODE`, `AGENTHUB_MAX_PARALLEL`,
`AGENTHUB_CLI_ALLOWLIST`, `AGENTHUB_CLI_TIMEOUT`, `AGENTHUB_MAX_OUTPUT_BYTES`,
`AGENTHUB_PUSH_RECORD_LIMIT` (default 100).

## Tests

```bash
make db-up             # integration cases need a real PostgreSQL
make test              # go test -race ./... (integration cases skip without DATABASE_URL)
make test-integration  # only integration_test.go
make web-test          # cd web && bunx vitest run
```

Integration coverage: idempotent migrations, resource CRUD with jsonb round trips, the full task
lifecycle with cascade deletes, event replay, push record persistence plus the retention cap, and
reading a complete A2A run back from the database. **They skip without a database and never
fabricate results.**

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
