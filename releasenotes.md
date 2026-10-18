# AgentHub Release Notes

> This document takes the perspective of the A2A interconnection platform (agenthub)
> and summarizes all feature updates in the `agenthub` repository as of
> **2026-10-18**, covering **v0.1.0** (the M1 first release: A2A protocol layer /
> resource registry / adapter layer / Hub orchestration / console), **v0.2.0** (full
> PostgreSQL persistence), **v0.3.0** (build stamping — `version@commit` in the
> console and in `/healthz`), **v0.4.0** (release publishing of the container image)
> and **v0.5.0** (the desktop shell for Windows, macOS and Linux). The server is a
> single Go binary, the console is a monochrome Vue 3 UI, and exactly one standard
> set of A2A endpoints is exposed — that same console also ships as a desktop app.

## Component Version Reference

| Component | Repository | Version |
| --- | --- | --- |
| A2A Server + Console | `agenthub` (agenthub-server / Vue 3 console) | v0.5.0 |
| A2A Protocol Implementation | `agenthub` / `internal/a2a` | 0.2.0 |
| Persistence | PostgreSQL 16 + GORM (`gorm.io/gorm` v1.25.12) | — |
| Desktop Shell | `agenthub` / `desktop` (Tauri v2, Rust) | v0.5.0 |

---

## 1. Platform Overview

`agenthub` registers, discovers, routes and orchestrates five kinds of heterogeneous
assets — model (the brain) / agent (the digital worker) / mcp (the universal adapter) /
skill (the playbook) / cli (the walkie-talkie) — and is callable through standard A2A
endpoints by any agent that speaks the protocol.

### Five-Layer One-Way Dependency

- **Layered dependency**: `a2a` (protocol) → `registry` (resources) → `adapter`
  (adapters) → `hub` (orchestration) → `handlers` (ingress); the direction is strictly
  one-way.
- **Persistence by dependency inversion**: the four ports — `ResourceStore`,
  `TaskRepository`, `EventRepository`, `PushRepository` — are declared by their
  consuming packages, with implementations in `internal/store`, so `hub` and
  `registry` never import `store`.
- **Single binary**: Go 1.27 + the standard library `net/http`, no framework; GORM is
  the only production dependency.
- **Default port 3500**; the compose database publishes host port **5434** (avoiding
  5432 / 5433).

### Five-Kind Asset Map

- **model**: provider registry with a fallback chain, emits a completion.
- **agent**: goal-driven and suspendable, plans steps into the `agent.plan` artifact.
- **mcp**: `tools/list` + `tools/call` protocol translation; `transport` is descriptive
  only.
- **skill**: trigger → playbook → steps → report.
- **cli**: allowlist → timeout → truncation → structured JSON receipt.

---

## 2. A2A Protocol Layer

Exactly one standard set of A2A endpoints is exposed: any agent that speaks the
protocol can discover, invoke, and take over task orchestration.

### Endpoints & Methods

- **Discovery**: `GET /.well-known/agent.json` (also `GET /a2a/agent-card`); each
  resource is rendered as one skill.
- **Single call entry point**: `POST /a2a` with a JSON-RPC 2.0 envelope and
  `Content-Type: application/json`.
- **Seven methods**: `message/send`, `message/stream`, `tasks/get`, `tasks/cancel`,
  `tasks/resume`, `tasks/pushNotification/set`, `tasks/pushNotification/get`.
- **v0.1 aliases**: `tasks/send` / `tasks/sendSubscribe`, toggled by
  `AGENTHUB_A2A_LEGACY_METHODS`.
- **Resource selection**: `metadata.resourceId|resource_id|resource` (exact id → name →
  kind) → `metadata.kind` fallback → with no selector, prefer agent, otherwise the first
  id in lexicographic order.

### Task State Machine

- **Lifecycle**: `submitted → working → input-required → completed / failed / canceled`;
  terminal states cannot be left, and `unknown` is neither a valid source nor target.
- **Suspend & resume**: `input-required` can park **indefinitely** and is resumed by
  `tasks/resume` with the `taskId`; resuming a task that is not parked returns
  `task_not_resumable` (-32008).

### SSE Streaming Contract

- **Frame names**: `task_status_update` / `task_artifact_update` / `message`, with
  `error` as the error frame name.
- **No `[DONE]` sentinel**: termination is expressed by the `completed` / `failed` /
  `canceled` / `input-required` status events; clients must not depend on any sentinel
  frame.
- **Console bypass**: `GET /a2a/stream?task=<id>` serves the same stream without the
  JSON-RPC wrapper.

### Unified Error Model

- **Machine-readable codes**: `bad_request` (400) / `unauthorized` (401) /
  `not_found` (404) / `method_not_allowed` (405) / `payload_too_large` (413) /
  `internal_error` (500).
- **JSON-RPC codes**: standard `-32700` / `-32600` / `-32601` / `-32602` / `-32603`;
  custom `-32001 task_not_found` … `-32008 task_not_resumable`.
- **`error.message` is the code string itself**: no spaces, no natural language —
  localization is 100% the frontend copy layer's job.
- **Adapter codes**: `unknown_kind` / `cli_command_not_allowed` / `adapter_timeout` /
  `resource_unreachable`.

---

## 3. Resource Registry

`registry` records the five kinds of assets with an MCP-Registry-flavoured entry model
and provides discovery, export and aggregation.

### Five Kinds of Entries

- **Registry-level metadata**: `version` / `repository` / `tags` / `env` /
  `auth_schemes`, plus `endpoint` / `transport` (descriptive only — adapters must not
  branch on it).
- **Capabilities & health**: `capabilities` (tool names for MCP, step tools for skill);
  `health` is `healthy` / `degraded` / `unhealthy` / `unknown`.
- **Id rule**: shaped `<kind>-<hex>`, auto-generated when omitted; registering a
  duplicate id returns 400.

### Control Plane & Export

- **CRUD and discovery**: `GET/POST /api/resources`, `GET/PUT/DELETE /api/resources/{id}`,
  `POST /api/resources/{id}/health`, with `?kind=` and `?q=` filtering and counts.
- **MCP export**: `GET /api/export/mcp` emits MCP server descriptions.
- **Aggregated Agent Card**: `GET /api/export/agent-card`, one skill per resource.

---

## 4. Adapter Layer

`adapter` converges the invocation surface of all five kinds behind a single
`Connector` interface. In this release **all five are deterministic mocks**: no network,
no subprocess, fully runnable offline, with real integrations marked `TODO(real)`.

### Five Connectors

- **model**: provider registry with a fallback chain; artifact `model.completion`.
- **agent**: step planning; a prompt containing `approve` / `confirm` / `approval`
  (**word-level match**) parks the task and sets `RequiresInput`.
- **mcp**: `tools/list` + `tools/call` translation; artifacts `mcp.tools` / `mcp.call`.
- **skill**: trigger → playbook → steps → report; artifact `skill.manual`.
- **cli**: allowlist → timeout → truncation → JSON receipt; artifact `cli.receipt`
  (including `truncated`).

### Safety & Reliability

- **Determinism**: same input, same output (derived from an input hash), no network, no
  subprocess, and a deterministic `mockHealth` on health checks.
- **CLI four-step baseline**: allowlist pre-validation → single-shot timeout (15s
  default) → output truncation → structured JSON receipt; none of the four may be
  skipped.
- **Artifact accumulation**: supports `append` / `lastChunk`, returned as the accumulated
  list when the task ends.

---

## 5. Hub Orchestration Layer

`hub` turns one inbound message into a task that is traceable, suspendable and
resumable.

### State Machine & Suspend/Resume

- **Explicit transition validation**: `CanTransition` validates every move; illegal
  transitions return `invalid_state_transition` (-32005) and never degrade silently.
- **Parked state is persisted**: the `input-required` question and its timestamp land in
  `task_pending` and are cleared once the task resumes.

### flow Primitives

- **`then`**: runs one resource in sequence, feeding its output to the next step.
- **`branch`**: picks the **first matching arm** by `when`; with no match the whole arm is
  skipped (span recorded as `skipped`).
- **`parallel`**: runs members concurrently, each reading its own context copy; the branch
  count is capped by `AGENTHUB_MAX_PARALLEL` (default 8), and any member failing fails
  the whole `parallel`.
- **`when` syntax**: `""` / `always` / `true` (default arm), `contains:<text>`,
  `kind:<kind>`, `has:<key>`; if any node asks for input the flow **stops early** and
  parks.

### Span Timeline & Event Fan-out

- **Span timeline**: one span per step with duration, status
  (`ok`/`error`/`input-required`/`skipped`) and error code — the data source of the
  "task trace" screen.
- **Event fan-out**: one subscription channel per task; slow consumers get the **oldest
  frame dropped** rather than blocking the run, bounded by `AGENTHUB_EVENT_BUFFER`
  (default 512).
- **Error codes carried through**: an orchestration failure puts its code on the
  corresponding span so the console can render it directly.

---

## 6. Persistence (PostgreSQL + GORM, v0.2.0)

Resources, tasks, artifacts, spans, parked questions, callback configs, the event stream
and callback records are all durable: they can be read back and replayed after a restart,
while the container itself stays stateless.

### Tables

- **`resources`**: every entry field, with `tags` / `capabilities` / `auth_schemes` /
  `env` / `metadata` stored as `jsonb`.
- **`tasks` plus children**: the task body and state; `task_artifacts`, `task_spans`,
  `task_pending`, `task_push` cascade with the task.
- **`events`**: one row per broker frame, primary key `(task_id, seq)`, powering
  `since=<seq>` replay.
- **`push_records`**: one row per callback (url / state / config snapshot), capped by
  `AGENTHUB_PUSH_RECORD_LIMIT` (default 100 per task).
- **`schema_migrations`**: migration files already applied, skipped at startup.
- **jsonb payloads**: carried by the in-house `model.JSONField` (`driver.Valuer` /
  `sql.Scanner`) — `gorm.io/datatypes` is **not** introduced.

### Migrations & Write Strategy

- **Embedded idempotent migrations**: `migrations/*.sql` is embedded with `//go:embed`,
  executed in filename order, one transaction per file; the SQL itself is idempotent, so
  it is repeatable, auditable and rollback-friendly.
- **Conditional updates prevent lost updates**: task transitions use
  `UPDATE ... WHERE id=? AND state=?` plus a `RowsAffected` check, and zero rows is split
  into "no such task" vs "concurrent conflict".
- **Persistence never blocks a run**: state / artifact / span writes are synchronous,
  while `events` and `push_records` are **best effort** (bounded queue + async writer;
  a full queue or a failed write only warns and **never becomes back pressure**).
- **A client disconnect cannot discard task state**: every persistence call uses
  `context.Background()`.
- **JSON snapshot removed**: `DATABASE_URL` is mandatory and `config.Validate` refuses to
  start without it — there is no database-less fallback path.

### Event Replay & Callback Records

- **Reconnect replay**: `GET /api/tasks/{id}/events?since=<seq>` replays every persisted
  frame after `seq` and then follows the live stream; without `since` it replays spans /
  artifacts / status before subscribing.
- **Sequence continuation**: the counter resumes after the highest persisted seq, so a
  replayed frame and a live one can never share a seq.
- **Callback records**: `MockNotifier` writes one `push_records` row per callback; a
  failure only warns and falls back to memory and **must not affect the task's terminal
  state**. Real HTTP delivery remains M2.

---

## 7. Console

The Vue 3 + Vite (bun) bundle is hosted at `/` by the Go process — both an operations
screen and a visual inspector for the A2A contract.

### Five Screens

- **Registry**: five-kind filtering + search, health indicators and a stats bar, and a
  per-resource "trial run".
- **Task trace**: task list with status filtering, span timeline replay, and copyable
  artifact text.
- **Topology**: five asset lanes with invocation edges, line style distinguishing active
  from failed.
- **Login & registration**: the first account becomes the owner; every `/api/*` route
  is closed without a session.
- **Administration**: users, groups, roles, permissions and resource grants, plus
  database checkpoints (dump / download / restore / schedule) as a fourth tab.

### i18n & Monochrome System

- **13 languages**: en / zh / zh-TW / es / fr / de / it / ru / ja / ko / pt / ar / hi,
  fully native translations with English fallback and RTL for `ar`; switching syncs
  `<html lang>` and `dir` at runtime.
- **No hardcoded natural language**: components always call `t()`; backend enums are
  mapped by `copy/enums-copy.js`.
- **Pure black / white / grey**: every hex satisfies **R=G=B** and any hue is banned;
  state is encoded by fill / stroke / motion, and the five kinds are distinguished with
  triple redundancy (grey level + dedicated icon + text label).
- **Automated guards**: `web/tests/style.spec.js` checks colours; `prefers-contrast` and
  `prefers-reduced-motion` both have degraded modes.

---

## 8. Deployment & Operations

### Delivery

- **Single Go binary**: `bin/agenthub-server` plus the build-time `backend/static`.
- **Three-stage image**: `oven/bun` builds the console → `golang:1.27-alpine` compiles →
  `alpine:3.21` runs; it starts as a **non-root user** (uid 10001) with `/healthz` as the
  healthcheck.
- **docker compose**: one `docker compose up -d` brings up `postgres:16-alpine` (host port
  5434) plus `agenthub`; the server starts only after the database reports healthy and
  then runs migrations and seeding itself. `--profile dev` adds a bun dev server (5173).
- **Health check**: `/healthz` reports version, protocol version, resource and task
  counts, mode, auth mode and **`database` (up / down / unknown)**; it returns 503 when
  the database is unreachable.
- **Desktop builds**: each release also publishes Windows / macOS / Linux assets — a
  native installer, a portable archive and the checksums covering both (see §10). The
  same release workflow owns the image job and the desktop matrix, side by side.

### Configuration & Security

- **Required**: `DATABASE_URL` (startup is refused without it).
- **Port & environment**: `AGENTHUB_HTTP_PORT` (3500), `AGENTHUB_ENV`
  (dev / staging / prod).
- **Auth**: real accounts, sessions and RBAC — the first account becomes the owner and
  every `/api/*` route is closed without a session; `AGENTHUB_COOKIE_SECURE` pins the
  session cookie to HTTPS. **Production red line**: `AGENTHUB_ENV=prod` without
  `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` refuses to start (the shared admin token was
  removed).
- **Limits**: `AGENTHUB_MAX_PARALLEL` (8), `AGENTHUB_CLI_TIMEOUT` (15s),
  `AGENTHUB_CLI_ALLOWLIST` (git / ls / cat / echo / date / aria-memo),
  `AGENTHUB_MAX_OUTPUT_BYTES` (65536), `AGENTHUB_EVENT_BUFFER` (512),
  `AGENTHUB_PUSH_RECORD_LIMIT` (100).
- **Logging**: `slog` JSON; no message bodies, keys or vectors; DSNs are redacted by
  `store.RedactDSN` before they are logged.

---

## 9. Tests & Acceptance

- **`go vet ./...` clean**: `go test -race ./...` green across a2a, config, registry,
  adapter, hub, model, store, handlers and httputil.
- **Pure-logic cases need no external service**: without injected repositories, registry /
  TaskStore / Broker / MockNotifier fall back to in-memory semantics, so the suite is
  green with no database.
- **Database-gated integration cases**: `tests/integration_test.go` covers idempotent
  migrations, resource CRUD with jsonb round trips, the full task lifecycle with cascade
  deletes, event replay, push record persistence and the retention cap, and reading a
  complete A2A run back from the database; **they skip when `DATABASE_URL` is unset and
  never fabricate results**.
- **End-to-end cases**: `tests/e2e_test.go` drives real HTTP through httptest across six
  legs (startup check → discovery → send a task → streaming run → suspend/resume →
  control-plane round trip).
- **Frontend**: `bunx vitest run`, 57 tests passing (languages / copy-fallback /
  enums-copy / api / format / errors / backup-api / style guard).
- **Desktop**: `cargo test` in `desktop/src-tauri`, 34 tests passing — URL and config
  validation, the 13-language contract, the forwarding prefix list and cookie
  rewriting, plus end-to-end cases that start a real proxy against a stub server and
  assert an SSE stream arrives unbuffered.
- **A2A & control-plane smoke**: send (once per kind), stream (no `[DONE]`, terminal frame
  last), `input-required` → `tasks/resume`, `tasks/get` history trimming, v0.1 aliases,
  flow branch and parallel, error-code paths, and the register → invoke → delete round
  trip.

---

## 10. Desktop Shell (v0.5.0)

`desktop/` is a **Tauri v2** shell (Rust, system WebView) for Windows, macOS (arm64)
and Linux. It carries **no backend and no database**: it embeds the same
console the server hosts and opens it against a server the user points it at. Neither
the console nor the Go server was written for the desktop — not a line of either
changed.

### Why a Loopback Proxy

The console is a web app: every request it makes is a relative URL (`/api/…` with
`credentials: 'include'`), so it needs an origin that answers like the server's. The
shell gives it one — `http://127.0.0.1:<random port>`, bound to loopback only — where
a reverse proxy serves the embedded console and forwards upstream:

- **Forwarded prefixes**: `/api`, `/a2a`, `/healthz`, `/.well-known`; anything else
  falls back to `index.html`, the same rule as the server's static hosting.
- **Session cookies**: `Set-Cookie` is rewritten before it reaches the WebView —
  `Secure` and `Domain` are dropped (a plain-http loopback origin cannot keep them),
  `SameSite=None` becomes `Lax`, and the path is pinned to `/`. `HttpOnly` is left as
  the server sent it, so the cookie the page cannot read still rides along.
- **Streaming**: responses are forwarded as a byte stream with no buffering and no
  overall timeout, so `/api/tasks/{id}/events`, `message/stream` and the checkpoint
  download behave exactly as they do in a browser.
- **Redirects**: a `Location` pointing back at the server is rewritten to the loopback
  origin, so an OAuth round trip stays inside the app.

### Connection, Tray and Languages

- **First start** shows the connection page, which probes `/healthz` before it saves
  anything; the address lives in the shell's own `config.json` (0600, per user) and
  "ignore self-signed certificates" is off by default.
- **Tray**: open console / connection settings… / about / quit, in the chosen language;
  closing the window parks the app in the tray instead of ending it.
- **13 languages** — the same list as the console, Arabic mirrored — held by
  `cargo test` to the same contract the console's copy is held to: 13 languages, no
  empty strings, and no language silently repeating English.

### Distribution

Every release publishes three builds (linux-x86_64, macos-aarch64,
windows-x86_64), each with a **native installer** (MSI / DMG / deb), a
**portable archive** (zip / tar.gz) and a **SHA256SUMS.txt** covering both. Linux
ships no AppImage — the `.deb` installs, the portable archive unpacks, and an
AppImage would be a third way to run the same binary. macOS is
**arm64 only**: the Intel cell built on the `macos-13` label, which GitHub has retired,
so that job was accepted and then waited for a runner that never comes — and because
the matrix runs with `fail-fast: false`, the whole release hung instead of failing.
Intel Macs build from source with `make desktop-build`. The installer version is the
release tag. The builds are **not code-signed**, so the first launch is stopped by the
platform:

| Platform | What to do |
| --- | --- |
| macOS | *System Settings → Privacy & Security → Open Anyway*, or `xattr -d com.apple.quarantine /Applications/agenthub.app` |
| Windows | SmartScreen: *More info → Run anyway* |
| Linux | Nothing to bypass: install the `.deb`, or unpack the `.tar.gz` — the binary keeps its executable bit |

---

## Architecture Overview

```
desktop --http://127.0.0.1:<port> (loopback proxy)--> embedded console + /api, /a2a, /healthz, /.well-known
caller --POST /a2a (single JSON-RPC 2.0 entry)--> handlers -> hub -> adapter -> resource
caller --GET /.well-known/agent.json-----------> Agent Card (one skill per resource)
console --GET /api/tasks/{id}/events?since=seq--> SSE replay + live stream
hub ----store (GORM, four port interfaces)------> PostgreSQL 16 (embedded idempotent migrations)
console --GET /api/*---------------------------> Registry / Task trace / Topology
```

- **Frozen contract**: A2A frame shapes are defined only in `internal/a2a` and no layer
  may rewrite them; SSE carries no `[DONE]` sentinel.
- **Language-agnostic backend**: HTTP and JSON-RPC responses carry only fields, enums and
  codes; localization is 100% the frontend copy layer's job.
- **Transport-agnostic**: an MCP entry's `transport` (stdio / streamable-http /
  sse-legacy) is a recorded field, and adapters must not branch on it.
- **No scoring**: eval only keeps a hook — no LLM judge, and no scores are produced.
- **One console, two shells**: the desktop app is the same bundle served from a
  loopback origin, so the console never grows a desktop-only code path — and the Go
  server never learns that a desktop client exists.
