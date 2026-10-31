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
- **Measured health probe**: `POST /api/resources/{id}/probe` asks the resource's own
  connector how it is actually doing (an agent's Agent Card is fetched, an MCP server's
  `initialize` is pinged) and stores the answer; a probe that cannot be answered records
  `unhealthy` rather than keeping a stale value — distinct from `…/health`, which records an
  operator's judgement.
- **Identity & sessions**: `GET /api/auth/config` reports the enabled OAuth providers;
  `GET /api/auth/sessions` and `DELETE /api/auth/sessions/{id}` let a user list and revoke
  their own sessions, while `DELETE /api/users/{id}/sessions` (`sessions:manage`) revokes
  every session for another user. Sessions are DB-stored random tokens, so revocation takes
  effect immediately and a revoked session cannot be replayed.
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
- **speech**: a sixth adapter, deterministic offline STT/TTS for the voice endpoint
  (`POST /api/threads/{id}/audio`); like the others it is a mock, marked `TODO(real)`.

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
  `approved`. Later migrations extend the same contract: `010_api_keys.sql` (API-key
  tables), `011_login_protection.sql` (account lockout columns), `012_threads.sql`
  (collaboration threads), `013_persona.sql` / `014_channels.sql` / `015_thread_origin.sql`
  (Persona / Channels / voice origin), and `016_wechat_login.sql` (nullable `users.email`
  so a WeChat account keyed only by its OAuth binding can be stored), and
  `017_computers.sql` / `018_schedules.sql` / `019_knowledge.sql` (agent sandboxes /
  proactive schedules / mock RAG chunks).
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
  sticky index that follows the reader. The endpoint table covers the full route table
  (~100 routes) with method, path, required permission and request/response samples taken
  from the handlers themselves — now including the agent-capability, channel, connector,
  speech and API-key endpoints. It sits after Administration in the sidebar and is
  deliberately **not**
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

### Configuration & Security

- **Required**: `DATABASE_URL` (startup is refused without it).
- **Port & environment**: `AGENTHUB_HTTP_PORT` (3500), `AGENTHUB_ENV`
  (dev / staging / prod).
- **Auth**: real accounts, sessions and RBAC — the first account becomes the owner and
  every `/api/*` route is closed without a session; `AGENTHUB_COOKIE_SECURE` pins the
  session cookie to HTTPS. **Production red line**: `AGENTHUB_ENV=prod` without
  `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` refuses to start (the shared admin token was
  removed). OAuth supports Google, GitHub, generic OIDC and **WeChat** web login
  (`snsapi_login`); a WeChat account is keyed only by its `(provider, subject)` binding,
  so `users.email` is nullable (migration `016_wechat_login.sql`).
- **Limits**: `AGENTHUB_MAX_PARALLEL` (8), `AGENTHUB_CLI_TIMEOUT` (15s),
  `AGENTHUB_CLI_ALLOWLIST` (git / ls / cat / echo / date / aria-memo),
  `AGENTHUB_MAX_OUTPUT_BYTES` (65536), `AGENTHUB_EVENT_BUFFER` (512),
  `AGENTHUB_PUSH_RECORD_LIMIT` (100).
- **Security middleware** (see §13): per-IP rate limiting (auth 10/min/IP, the rest 200/
  min/IP), CSRF origin checks on state-changing requests, and security response headers
  (including HSTS only on TLS). Account brute-force lockout is **5 failures → 15-minute
  lock** (`migrations/011_login_protection.sql`).
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
- **Encryption & database hardening**: `AGENTHUB_ENCRYPTION_KEY` (≥ 32 bytes in
  production, enforced by `config.Validate`; missing → session-key fallback with a warning)
  isolates static encryption at rest and encrypts backup archives. `AGENTHUB_PUSH_SECRET`
  (≥ 16 bytes when set) enables HMAC-signed A2A push callbacks. The connection pool is
  tunable via `AGENTHUB_DB_MAX_OPEN_CONNS` (25) / `AGENTHUB_DB_MAX_IDLE_CONNS` (10) /
  `AGENTHUB_DB_CONN_MAX_LIFETIME` (30m); run with a least-privilege role from
  `scripts/provision-db-roles.sql`.
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

## 10. Example Catalogue

`example/` ships a catalogue that can be loaded into a running instance straight from the
repository: 41 complete `registry.Entry` payloads plus a re-runnable loader, so a fresh
deployment is not limited to the five `demo-*` seeds.

```bash
cp .env.example .env     # then set AGENTHUB_EXAMPLE_ENABLED=true and
                         # AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh        # picks the platform from uname -s
./example/load.sh --all  # both platforms
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

## 11. Persona, Agent Capabilities, Channels, Connectors & Speech (M2)

On top of M1's registry and A2A orchestration, agenthub now ships the collaboration
surface that agentbuddy consumes: a resource **Persona**, external **Channels**, a
policy-gated **Connectors** gateway, and **Speech** (offline STT/TTS). All new endpoints
reuse the existing `resources:read` / `tasks:read` / `tasks:write` permissions — no new
roles or groups.

- **Persona (F10)**: `Entry.Persona` (`DisplayName` / `Title` / `Tagline` / `AvatarURL`)
  is normalised by `normalizePersona` — every text field is trimmed and capped at 256
  chars, `AvatarURL` is restricted to http(s) and dropped otherwise, and the whole struct
  coalesces to nil when empty — and stored in `resources.persona` as JSONB (migration
  `013_persona.sql`).
- **Channels**: Slack / Teams / Telegram / SMS surfaces bound to a thread. Inbound runs
  through a signed, public webhook (`POST /api/channels/{id}/webhook`, secret held only as
  an env-var name) that forwards into the thread; outbound runs through an SSRF-safe
  `Deliver` (`POST /api/channels/{id}/deliver`, `netutil.ValidateURL` + `SafeClient`, echoes
  the provider's raw response ≤512B, never retries) to `config.target_url`. Stored in
  `channels` + `channel_messages` (migration `014_channels.sql`).
- **Connectors**: a policy gateway over `kind=mcp` resources — `GET /api/connectors` lists,
  `PUT /api/connectors/{id}/policy` sets `off` / `ask_first` / `allowed`; `ask_first` parks
  an `input-required` task via `Hub.ParkConnector` for human approval. `GET/POST
  /api/connectors/{id}/actions` invoke an action.
- **Speech**: `POST /api/threads/{id}/audio` uploads a clip; the server transcribes it (STT
  mock), delegates it into the thread as an `origin=voice` message, and attaches the agent's
  reply as a TTS audio artifact. Gated by `AGENTHUB_SPEECH_MODE` / `AGENTHUB_AUDIO_MAX_BYTES`;
  migration `015_thread_origin.sql` adds the `origin` column to `thread_messages`.
- **Agent capability endpoints (F10–F16 scaffold)**: beyond Persona, agenthub now ships the
  first slice of the agent-capability surface — agent sandboxes, proactive schedules and
  mock RAG. All are deterministic scaffolds: the deep behaviours (real browser / shell /
  screen control, a real scheduler engine, vector retrieval) are marked `TODO(real)` and
  deferred to a later milestone. No new roles or permission codes were introduced; the
  endpoints reuse `resources:*` and `tasks:*`.
  - **Computers (F11)**: per-agent sandboxes of kind `browser` / `shell` / `screen` an agent
    may drive. `GET/POST /api/computers` and `GET /api/computers/{id}` create and list
    sandboxes and read one back. The first release records the session the console may
    attach to (`session_ref` / `status` / `last_action`); a real sandboxed driver is
    `TODO(real)`. Backed by `computers` (migration `017_computers.sql`).
  - **Schedules (F13)**: proactive goals the hub works on, on a cron or an external trigger.
    `GET/POST /api/schedules`, `GET/PUT/DELETE /api/schedules/{id}` manage goals; the first
    release stores the intent (`goal` / `trigger` / `cron_expr` / `enabled` / `next_run`)
    and the mock scheduler reuses `Hub` to create a task when invoked — registering the
    expression with a real scheduler is `TODO(real)`. Backed by `schedules` (migration
    `018_schedules.sql`).
  - **Knowledge (F14)**: mock RAG over stored chunks. `GET /api/knowledge?agentId=…` returns
    the `chunk_text` / `source_ref` stored for an agent; real embedding and similarity
    ranking are `TODO(real)`. Backed by `knowledge` (migration `019_knowledge.sql`).
  - **Permissions & errors**: Computers hang off `resources:read` / `resources:write`;
    Schedules off `tasks:read` / `tasks:write`; Knowledge off `resources:read`. An unknown
    id returns `computer_not_found` / `schedule_not_found` (404), a goal-less schedule
    returns `schedule_invalid` (400), and when a store is not wired in the endpoint answers
    `feature_unavailable` (503) rather than 404 — so a deployment that does not provision
    these tables degrades cleanly.
- **Docs**: the `/docs` console now documents every new endpoint in its Collaboration group,
  in 13 languages, including the agent-capability, channel, connector and speech endpoints
  (see `web/src/lib/docs-content.js` + `web/src/lib/copy/docs-copy.js`).

## 12. Kubernetes Operator

For Kubernetes-native deployment, `agenthub-operator` reconciles the `AgentHub`
CRD (`agenthub.ai/v1alpha1`) and provisions the full workload for each instance:
a ConfigMap (`<instance>-config`), a Secret (`<instance>-secrets`), a
ServiceAccount, a least-privilege Role + RoleBinding for the app, a Deployment,
a Service, and an optional PersistentVolumeClaim (when persistence is enabled).
Children carry an owner reference so they are garbage-collected with the CR, and
the controller watches every owned kind.

- **Values vs. external secrets**: inline CR fields (database URL, bootstrap
  admin password/email, encryption key, push secret, OAuth client secrets) are
  written into the managed `<instance>-secrets`. A field may instead set
  `ExistingSecret` — the operator then renders a `SecretKeyRef` to that
  externally-provided Secret rather than managing its value, so the deployment
  references a Secret you own.
- **Helm install**: `helm install agenthub-operator operator/deploy/agenthub-operator
  -n agenthub-operator --create-namespace` lays down the Deployment, the
  operator's own Role/RoleBinding/ServiceAccount, and the CRD.
- **Reconcile strategy**: `upsert` creates owned objects and patches them with a
  merge patch (not a full `Update`) so a concurrent status write from the app
  cannot trip the optimistic-concurrency conflict.

---

## 13. Security Hardening

agenthub now ships the safeguards it needs to sit on a public network: security response
headers, per-IP rate limiting, CSRF defence, account lockout, SSRF-guarded outbound calls,
signed A2A push callbacks, encryption at rest and least-privilege database roles. No new
production dependency was introduced — every behaviour is the standard library plus GORM.
The design mirrors `ariacompute/serve`'s five-domain hardening, adapted to agenthub's single
Go-binary, DB-backed-session architecture (full spec in `requirements-security.md`,
implementation log in `task-security.md`).

### Transport & edge

- **Security headers** (`internal/middleware`): `X-Content-Type-Options`, `Referrer-Policy`,
  `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy` and a grace-period
  `Content-Security-Policy-Report-Only`. HSTS is emitted only when `AGENTHUB_ENV=prod` or the
  request is already TLS-terminated (`r.TLS` / `X-Forwarded-Proto: https`). The binary does
  not terminate TLS — front it with a TLS-terminating reverse proxy (Caddy / nginx) in
  production.

### Authentication & rate limiting

- **Account lockout** (`migrations/011_login_protection.sql`): **five** consecutive failed
  logins lock an account for **15 minutes**; a successful login clears the counter and the
  lock. Unknown accounts return the same error, so they cannot be enumerated; the new
  `account_locked` (429) code is returned while locked.
- **Per-IP rate limiting**: auth endpoints are capped at **10 req/min/IP**; everything under
  `/api` and `/a2a` shares **200 req/min/IP** (`429` + `Retry-After`). Static, `/healthz`
  and discovery paths are unlimited.
- **CSRF**: state-changing requests must carry a same-origin `Origin`; `GET/HEAD/OPTIONS`,
  requests without an `Origin`, and non-browser clients (such as scripts) are exempt —
  the same-origin console fetch is unaffected.
- **Audit hygiene**: logs record only IP and user id — never passwords, tokens or PII.
- Sessions are DB-stored random tokens (no JWT / JTI), so revocation is immediate and a
  revoked session cannot be replayed.

### A2A callbacks & upstream sync (SSRF)

- **Real push notifications**: `HTTPNotifier` replaces the record-only mock and POSTs to the
  caller-supplied `URL`. The destination is https-only and never resolves to a loopback /
  private / link-local / multicast address (`internal/netutil.SafeClient`); when
  `AGENTHUB_PUSH_SECRET` is set the payload is HMAC-SHA256 signed (timestamp + nonce + body)
  so the receiver can verify authenticity, freshness (±5 min) and replay; `(taskId, state)`
  is de-duplicated for one hour. A failed delivery only warns and never back-pressures the
  task.
- **Guarded sync client**: the upstream registry sync client uses the same `SafeClient` and
  validates the URL before fetching.

### Secrets, data & database

- **Encryption at rest**: `AGENTHUB_ENCRYPTION_KEY` (**≥ 32 bytes in production**, else the
  server refuses to start) isolates any future static encryption; a missing key falls back to
  the session key with a startup warning and supports key rotation with old-key fallback.
- API keys are stored hashed (`token_hash`); OAuth client secrets are environment-variable
  references, never rows. An injection audit confirmed every SQL statement is parameterised.
- **Database**: run with a least-privilege role; `scripts/provision-db-roles.sql` provisions
  `app` (runs migrations) and `readonly` roles. The connection pool is tunable via
  `AGENTHUB_DB_MAX_OPEN_CONNS` (25) / `AGENTHUB_DB_MAX_IDLE_CONNS` (10) /
  `AGENTHUB_DB_CONN_MAX_LIFETIME` (30m). Whether the DB connection uses TLS is left to the
  DSN (`sslmode=…`).
- **Backup encryption**: when `AGENTHUB_ENCRYPTION_KEY` is set, checkpoint archives are
  encrypted at rest (AES-256-GCM) to `<file>.tar.gz.enc` with a `<file>.enc.sha256` manifest;
  verify recoverability with `scripts/backup-restore-drill.sh`.
