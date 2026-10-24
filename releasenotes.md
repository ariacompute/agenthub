# AgentHub Release Notes

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
- **Governed catalogue**: a record belongs to a namespace, carries a curation
  status, and is deduplicated on `(namespace, name, version)`; consumers only ever see
  approved ones.

### Asset Map

- **model**: provider registry with a fallback chain, emits a completion; can point at a
  real OpenAI-compatible gateway through its own router configuration.
- **agent**: goal-driven and suspendable, plans steps into the `agent.plan` artifact.
- **mcp**: `tools/list` + `tools/call` protocol translation; `transport` is descriptive
  only.
- **skill**: trigger → playbook → steps → report.
- **cli**: allowlist → timeout → truncation → structured JSON receipt.
- **custom**: a free-form JSON schema, for records that are none of the five.

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
  id in lexicographic order. The candidate set is `registry.Discoverable`, so
  a record that has not been approved cannot be selected or invoked.

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

`registry` records the five kinds of assets — plus the `custom` record type —
with an MCP-Registry-flavoured entry model, and provides discovery, export and aggregation.

### Entry Model (five kinds + custom)

- **Registry-level metadata**: `version` / `repository` / `tags` / `env` /
  `auth_schemes`, plus `endpoint` / `transport` (descriptive only — adapters must not
  branch on it).
- **Capabilities & health**: `capabilities` (tool names for MCP, step tools for skill);
  `health` is `healthy` / `degraded` / `unhealthy` / `unknown`.
- **Id rule**: shaped `<kind>-<hex>`, auto-generated when omitted; registering a
  duplicate id returns 400.
- **Dedup key**: `(namespace, name, version)`, enforced by a unique index — the
  same name at a new version is a new record, the same name at the same one returns 400.

### Registry Governance

The catalogue is a governed registry rather than a flat list. The item-by-item comparison
against the AWS Agent Registry that drove this work is the *AWS Agent Registry comparison*
section of `README.md`, including what agenthub deliberately does not do (semantic hybrid
search, CloudTrail-equivalent auditing, cross-account RAM sharing, VPC PrivateLink).

- **Per-kind configuration**: each of the six kinds has its own block in the register
  drawer and its own backend validation (`ValidateKind`, `invalid_kind_config`) — a model
  declares a provider mode, an agent its Agent Card URL and capabilities, an MCP server a
  transport that decides whether a command or a URL is asked for, a skill its steps and
  manual, a CLI its command allowlist, and a custom record any JSON schema.
- **Approval lifecycle**: `draft → pending → approved / rejected`, then `deprecated`,
  driven through `POST /api/resources/{id}/status` (`registry:curate`).
  `registry.Discoverable` is the single choke point: the listing, the MCP export, the
  Agent Card and A2A addressing all read through it, so an unapproved record cannot be
  found or invoked. An empty status is treated as `approved`, which is what keeps a
  catalogue that predates the feature visible after an upgrade.
- **Namespaces**: the `registries` table holds namespaces, each with its own approval
  policy (`auto_approve`); `default` is seeded and cannot be deleted.
- **Upstream registry sync**: a source (MCP or Skill) pulls records from an external
  registry on demand or on an interval, merges them on `(name, version)` and records the
  outcome (`last_sync_at` / `status` / `count` / `error`) on the source. Imported records
  go through the same `Upsert` path as a hand-written registration, so an upstream
  catalogue can never bypass validation or curation. The real client is `net/http` with a
  timeout, a size cap and per-source auth; the fixture client is deterministic and
  offline, and `AGENTHUB_REGISTRY_SYNC_FIXTURE` (on by default) pins every source to it —
  enabling synchronisation never silently enables outbound traffic.
- **New error codes**: `invalid_kind_config`, `invalid_status_transition`,
  `invalid_namespace`, `invalid_source` → 400; `sync_in_flight` → 409; an unreachable
  upstream registry → 502 with the upstream code passed through.

### Control Plane & Export

- **CRUD and discovery**: `GET/POST /api/resources`, `GET/PUT/DELETE /api/resources/{id}`,
  `POST /api/resources/{id}/health`, with `?kind=`, `?q=`, `?namespace=` and `?status=`
  filtering and counts.
- **Namespaces and curation**: `GET/POST /api/registries`,
  `PUT/DELETE /api/registries/{id}`, `GET /api/registries/approvals` (the curator's
  queue), `POST /api/resources/{id}/status`.
- **Upstream sources**: `GET/POST /api/registry-sources`,
  `PUT/DELETE /api/registry-sources/{id}`, `POST /api/registry-sources/{id}/sync`.
- **MCP export**: `GET /api/export/mcp` emits MCP server descriptions — approved records
  only.
- **Aggregated Agent Card**: `GET /api/export/agent-card`, one skill per resource —
  approved records only.
- **Collaboration (agentbuddy)**: `GET /api/workspace` aggregates visible agents + recent task
  activity; `GET/POST /api/threads`, `GET /api/threads/{id}`, `POST /api/threads/{id}/messages`
  (each `@mention` delegates to that agent over A2A via `Hub.Send`, keyed by task id) and
  `GET /api/threads/{id}/events` (SSE replay + live delegated-task replies). Reuses
  `resources:read` / `tasks:read` / `tasks:write`; no new roles or groups.

---

## 4. Adapter Layer

`adapter` converges the invocation surface of all five kinds behind a single
`Connector` interface. Four of them are **deterministic mocks**: no network, no
subprocess, fully runnable offline, with real integrations marked `TODO(real)`. The
model connector can also run a **real OpenAI-compatible provider** when its
entry asks for one.

### Five Connectors

- **model**: provider registry with a fallback chain; artifact `model.completion`, which
  now carries the name of the provider that answered.
- **agent**: step planning; a prompt containing `approve` / `confirm` / `approval`
  (**word-level match**) parks the task and sets `RequiresInput`.
- **mcp**: `tools/list` + `tools/call` translation; artifacts `mcp.tools` / `mcp.call`.
- **skill**: trigger → playbook → steps → report; artifact `skill.manual`.
- **cli**: allowlist → timeout → truncation → JSON receipt; artifact `cli.receipt`
  (including `truncated`).

### Safety & Reliability

- **Determinism**: same input, same output (derived from an input hash), no network, no
  subprocess, and every offline result stamped `provider: mock`.
- **Measured health**: health is no longer declared by a mock (`mockHealth` is gone). Each
  connector probes what it will actually call — the model's `/models` (falling back to a one
  token completion), an agent's Agent Card, MCP's `initialize`, a skill's or custom record's
  endpoint, a CLI's `PATH` lookup — and a run records what it proved. Anything with nothing to
  reach answers `unknown` instead of a value nobody measured.
- **CLI four-step baseline**: allowlist pre-validation → single-shot timeout (15s
  default) → output truncation → structured JSON receipt; none of the four may be
  skipped.
- **Artifact accumulation**: supports `append` / `lastChunk`, returned as the accumulated
  list when the task ends.

### Model Router

An entry with `provider_mode=openai_compatible` points `endpoint` at any
OpenAI-compatible gateway — LiteLLM, OpenRouter or a self-hosted router — and is served
by a real chat-completions provider.

- **Configuration**: `model_id`, `api_key_env`, an ordered fallback chain with weights, a
  timeout, and an optional mock fallback. The router is built per entry from its
  `KindConfig`, so two model resources can route to two gateways.
- **Credentials**: `api_key_env` holds the **name of an environment variable** only. The
  key itself is never stored in the database and never logged.
- **Failure is never faked**: when every provider fails the connector returns an error.
  Only an explicit `fallback_mock` answers from the deterministic mock, and the artifact
  says `provider: mock` so nobody mistakes it for a real completion.
- **Boundaries**: streaming and the real agent / mcp / skill / cli adapters remain
  `TODO(real)`; those four stay offline.

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

## 6. Persistence (PostgreSQL + GORM)

Resources, tasks, artifacts, spans, parked questions, callback configs, the event stream
and callback records are all durable: they can be read back and replayed after a restart,
while the container itself stays stateless.

### Tables

- **`resources`**: every entry field plus the governance columns — `namespace`, `status`,
  `display_name`, `source_id` and the per-kind `kind_config` — with `tags` /
  `capabilities` / `auth_schemes` / `env` / `metadata` / `kind_config` stored as `jsonb`;
  unique on `(namespace, name, version)`.
- **`registries`**: namespaces, each with its own approval policy.
- **`registry_sources`**: upstream registry sources and the outcome of their last
  sync (`last_sync_at` / `status` / `count` / `error`).
- **`tasks` plus children**: the task body and state; `task_artifacts`, `task_spans`,
  `task_pending`, `task_push` cascade with the task.
- **`events`**: one row per broker frame, primary key `(task_id, seq)`, powering
  `since=<seq>` replay.
- **`push_records`**: one row per callback (url / state / config snapshot), capped by
  `AGENTHUB_PUSH_RECORD_LIMIT` (default 100 per task).
- **`api_keys` / `api_key_scopes`**: long-lived bearer credentials. Only the `sha256`
  of the token (`token_hash`) and a display `prefix` (`ahk_xxxxxxxx`) are stored — never
  the secret — alongside an optional `expires_at` and a soft `revoked_at` (a revoked
  key keeps its row for the audit trail but stops authenticating); `api_key_scopes` is
  the key → permission edge.
- **`schema_migrations`**: migration files already applied, skipped at startup.
- **jsonb payloads**: carried by the in-house `model.JSONField` (`driver.Valuer` /
  `sql.Scanner`) — `gorm.io/datatypes` is **not** introduced.

### Migrations & Write Strategy

- **Embedded idempotent migrations**: `migrations/*.sql` is embedded with `//go:embed`,
  executed in filename order, one transaction per file; the SQL itself is idempotent, so
  it is repeatable, auditable and rollback-friendly. `008_registry_governance.sql` and
  `009_registry_sync.sql` add the governance columns, the unique dedup index, the
  namespaces and the upstream sources — every statement is `IF NOT EXISTS` /
  `ON CONFLICT DO NOTHING`, and rows written before the upgrade keep their status as
  `approved`.
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

### Eight Screens

- **Registry**: six-kind (including `custom`) filtering + search, namespace and status
  filters, health indicators and a stats bar, and a per-resource "trial run". The register
  drawer now switches per kind — a model shows its router, an MCP server its transport, a
  skill its steps and manual — and every card carries a curation badge that offers exactly
  the transitions the status allows.
- **Registries**: namespaces with their approval policy, plus the curator's
  pending-review queue with approve / reject in place.
- **Sources**: upstream registry sources, a manual "sync now", and the imported /
  updated / skipped counts of the last run.
- **Task trace**: task list with status filtering, span timeline replay, and copyable
  artifact text.
- **Topology**: five asset lanes with invocation edges, line style distinguishing active
  from failed.
- **Login & registration**: the first account becomes the owner; every `/api/*` route
  is closed without a session.
- **Administration**: users, groups, roles, permissions and resource grants, plus
  database checkpoints (dump / download / restore / schedule) as a fourth tab, and
  now an **API keys** tab for minting, scoping, rotating and revoking long-lived
  bearer credentials for non-interactive access.
- **Documentation** (`/docs`): the console explains itself, offline and shipped with the
  build. Five sections — concepts, A2A methods, HTTP API, authentication, errors — with a
  sticky index that follows the reader. The endpoint table covers all 66 routes with
  method, path, required permission and request/response samples taken from the handlers
  themselves. It sits after Administration in the sidebar and is deliberately **not**
  permission-gated: anyone who can sign in may read it, while Administration itself still
  appears and disappears with its permissions.
  Content and copy are split in two: `lib/docs-content.js` holds the machine-shaped half
  (methods, paths, permission codes, field names, JSON samples, HTTP reason phrases) and
  renders it verbatim in every locale, while `lib/copy/docs-copy.js` holds only prose in
  13 languages. The boundary is enforced by `web/tests/copy-fallback.spec.js`, which
  asserts that every non-English translation differs from English — a string that is
  identical everywhere cannot live in the copy table.

### i18n & Monochrome System

- **13 languages**: en / zh / zh-TW / es / fr / de / it / ru / ja / ko / pt / ar / hi,
  fully native translations with English fallback and RTL for `ar`; switching syncs
  `<html lang>` and `dir` at runtime.
- **No hardcoded natural language**: components always call `t()`; backend enums are
  mapped by `copy/enums-copy.js`.
- **Pure black / white / grey**: every hex satisfies **R=G=B** and any hue is banned;
  state is encoded by fill / stroke / motion, and the kinds are distinguished with
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
- **Catalogue**: `AGENTHUB_MODEL_ROUTER_MODE` (`mock` | `openai_compatible`,
  default `mock` — the per-entry configuration always wins over this global default),
  `AGENTHUB_REGISTRY_SYNC_ENABLED` (default **false**),
  `AGENTHUB_REGISTRY_SYNC_INTERVAL` (default `1h`, must be positive once sync is on) and
  `AGENTHUB_REGISTRY_SYNC_FIXTURE` (default **true**, which pins every source to the
  offline client — a deployment must opt out explicitly before agenthub will open a
  connection to an upstream registry).
- **API key authentication**: long-lived bearer credentials (`ahk_…`) for
  non-interactive access — scripts, CI, other languages. A key authenticates as its
  owner but only with the permission subset chosen at creation; scopes are intersected
  with the owner's permissions, so a key can never exceed what its owner can do and can
  never silently mint further keys (`apikeys:manage` is its own permission, not implied
  by any other). The raw token is returned exactly once on `POST /api/api-keys`; only its
  `sha256` (`token_hash`) and a display `prefix` (`ahk_xxxxxxxx`) are stored — never the
  secret. Keys carry an optional `expires_at` and a soft `revoked_at`, so a revoked key
  keeps its row for the audit trail but stops authenticating immediately. Send a key as
  `Authorization: Bearer ahk_…` or the `X-AgentHub-Key` header on any `/api/*` route and
  on the A2A endpoint; an unusable key simply falls back to anonymous, so public endpoints
  stay open.
- **Governance permissions**: `registries:manage` (namespaces),
  `registry:curate` (approve / reject / deprecate), `sources:manage` (upstream sources and
  their synchronisation), `apikeys:manage` (list / create / rotate / revoke API keys for
  any user — a key can act but never issue more keys). The owner holds all of them.
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
- **Governance cases**: the store-backed branches of `registry` are driven by
  in-package fakes (`store_mode_test.go`), so the dedup key, the approval state machine
  and the persistence error mapping are covered with no database at all;
  `internal/store/registry_governance_test.go` and
  `tests/registry_governance_integration_test.go` cover what genuinely needs PostgreSQL —
  the governance columns and the per-kind block surviving a jsonb round trip, the unique
  index rejecting a repeat, namespace CRUD and the sync bookkeeping. Both gate on
  `DATABASE_URL`.
- **Coverage**: `make test-cover` runs `-coverprofile` and prints
  `go tool cover -func`. With a database attached, `a2a` 97.8%, `model` 93.9%, `registry`
  89.7%, `hub` 87.8%, `authz` 87.9%, `adapter` 83.3%, `config` 80.3%, `httputil` 80.2%,
  `handlers` 68.7%, `backup` 58.2%, `auth` 56.9%, `store` 22.6% (its cases skip without
  `DATABASE_URL`, so that number says nothing until one is set).
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
- **API key coverage**: `internal/handlers/api_apikeys_test.go` exercises listing (own vs
  all), creation scope-clamping, manage guards (403 / 404 / 400), update + revoke and
  bearer-key authentication against an in-memory key store; `tests/apikey_integration_test.go`
  gates the PostgreSQL path (migration, token-hash lookup, scopes round trip, revoke +
  audit row) on `DATABASE_URL`.
- **Scripted API test**: `scripts/test-api.sh` grew a governance section —
  creating a namespace, watching a record go `draft → pending → approved` (and proving
  that `draft → approved` is refused and a draft stays out of the discoverable list), the
  dedup key, and a fixture source syncing twice (imported on the first run, refreshed on
  the   second). Against a running instance: **167 passed, 0 failed, 3 skipped**.
- **Example loader**: `example/load.sh` registers all 41 example payloads
  against a running instance — 41 created on the first run, 41 updated on the next two,
  every record `approved` and `healthy`, read back through
  `GET /api/resources?namespace=…`.

---

## 10. Desktop Shell

`desktop/` is a **Tauri v2** shell (Rust, system WebView) for Windows, macOS (arm64)
and Linux. It carries **no backend and no database**: it embeds the same
console the server hosts and opens it against a server the user points it at. Neither
the console nor the Go server was written for the desktop — not a line of either
changed.

**This release carries a rebuilt console, not a rewritten shell.** The two new screens and the
new control-plane endpoints needed no change here, which is the design working as
intended: forwarding is decided by prefix (`/api`, `/a2a`, `/healthz`, `/.well-known`) and
every other path falls back to `index.html`, so Vue's router owns console paths inside the
window exactly as it does on the server. `cargo test` still passes 34/34 unchanged.

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

## 11. Example Catalogue

`example/` ships a catalogue that can be loaded into a running instance straight from the
repository: 41 complete `registry.Entry` payloads plus a re-runnable loader, so a fresh
deployment is not limited to the five `demo-*` seeds.

```bash
cp .env.example .env          # then set AGENTHUB_EXAMPLE_ENABLED=true and
                              # AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh             # picks the platform from uname -s
./example/load.sh --all       # both platforms
```

Loading is opt-in: the loader refuses to run until `AGENTHUB_EXAMPLE_ENABLED=true`, and every
example setting carries the `AGENTHUB_EXAMPLE_` prefix so it cannot collide with the server's own
configuration.

| Kind | What it registers |
| --- | --- |
| model | an OpenAI-compatible gateway; `kind_config.model.api_key_env` names an environment variable, so the credential reaches neither the JSON nor the database |
| agent | Google ADK, LangGraph, CrewAI, Pi and DeepSeek Harness |
| mcp | the official filesystem server plus GitHub, Google Drive, Google Docs, Gmail, Slack, Stripe, WeChat, DingTalk and Feishu |
| skill | `anthropics/skills` and [skills.sh](https://www.skills.sh) |
| cli | `gh`, `hf` (HuggingFace) and `modelscope` |

- **Two namespaces**: `example-ubuntu` and `example-windows`, because the dedup key is
  `(namespace, name, version)`; the model record is shared in `example`.
- **Idempotent loader**: POST first (201) and fall back to PUT when the answer is
  `resource_already_exists` — `PUT /api/resources/{id}` is not an upsert and answers 404
  for an unknown id.
- **Credential discipline**: the gateway key stays in the server process environment
  (`AGENTHUB_EXAMPLE_API_KEY`); a CLI command only runs once it is listed in
  `AGENTHUB_CLI_ALLOWLIST`, which matches whole strings case-insensitively.
- **Intent, not execution**: only the model connector is real. The agent / mcp / skill /
  cli adapters remain `TODO(real)`, so those payloads describe a configuration rather
  than a live integration.

`example/README.md` carries the payload contract (the 21 accepted keys, the `kind` /
`transport` / `health` / `status` enums, and the per-kind `kind_config` with its
validation) plus the upstream install and start commands for each platform.

---

## Architecture Overview

```
desktop --http://127.0.0.1:<port> (loopback proxy)--> embedded console + /api, /a2a, /healthz, /.well-known
caller --POST /a2a (single JSON-RPC 2.0 entry)--> handlers -> hub -> adapter -> resource
caller --GET /.well-known/agent.json-----------> Agent Card (one skill per resource)
console --GET /api/tasks/{id}/events?since=seq--> SSE replay + live stream
agentbuddy --GET /api/threads/{id}/events------> SSE: replay thread + follow delegated tasks (Hub.Send over A2A)
hub ----store (GORM, four port interfaces)------> PostgreSQL 16 (embedded idempotent migrations)
console --GET /api/*---------------------------> Registry / Registries / Sources / Task trace / Topology
upstream registry --HTTPS----------------------> registry source -> dedup on (name, version) -> Upsert
```

- **Frozen contract**: A2A frame shapes are defined only in `internal/a2a` and no layer
  may rewrite them; SSE carries no `[DONE]` sentinel.
- **Language-agnostic backend**: HTTP and JSON-RPC responses carry only fields, enums and
  codes; localization is 100% the frontend copy layer's job.
- **Transport-agnostic**: an MCP entry's `transport` (stdio / streamable-http /
  sse-legacy) is a recorded field, and adapters must not branch on it.
- **Consumers only see approved records**: `registry.Discoverable` is the single
  filter the listing, the MCP export, the Agent Card and A2A addressing share, so a draft
  cannot leak through one path that another path guards.
- **Offline by default**: every adapter except an explicitly configured model
  router stays deterministic, and upstream synchronisation is both off and pinned to the
  fixture client until an operator turns both of those off.
- **No scoring**: eval only keeps a hook — no LLM judge, and no scores are produced.
- **One console, two shells**: the desktop app is the same bundle served from a
  loopback origin, so the console never grows a desktop-only code path — and the Go
  server never learns that a desktop client exists.
