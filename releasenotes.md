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
  custom `-32001 task_not_found` … `-32008 task_not_resumable`, plus `-32009
  unauthenticated` for an anonymous caller that used a method requiring an identity.
- **`error.message` is the code string itself**: no spaces, no natural language —
  localization is 100% the frontend copy layer's job.
- **Adapter codes**: `unknown_kind` / `cli_command_not_allowed` / `adapter_timeout` /
  `resource_unreachable`.
- **Exhaustive status mapping**: `WriteErr` is the single place a code becomes a status, and
  a test parses `json.go` to prove every declared `Code` has a pinned status — a forgotten
  mapping fails the build instead of answering 500 at runtime.

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
- **Connector approvals**: `GET /api/connectors/approvals` plus
  `POST /api/connectors/approvals/{id}/approve|reject` turn the parked `ask_first` tasks into a
  queue a person can actually decide (§16).
- **Shared files**: `GET/POST /api/files`, `GET /api/files/{id}`,
  `GET /api/files/{id}/content` (Range-capable attachment download) and `DELETE /api/files/{id}`
  (§16). Off by default; `files_disabled` when the deployment has no backend.

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
  `task_pending`, `task_push` cascade with the task. Every one of them carries
  `owner_id` (and so does `events`), which is what the row-level policies in §15
  read.
- **`events`**: one row per broker frame, primary key `(task_id, seq)`, powering
  `since=<seq>` replay.
- **`push_records`**: one row per callback (url / state / config snapshot), capped by
  `AGENTHUB_PUSH_RECORD_LIMIT` (default 100 per task).
- **`audit_log`**: append-only, one row per `/api` and `/a2a` call, hash-chained so
  a deleted or edited row breaks every later link (§15). No request or response
  body is stored, and the paths are redacted.
- **`api_keys` / `api_key_scopes`**: long-lived bearer credentials. Only the `sha256`
  of the token (`token_hash`) and a display `prefix` (`ahk_xxxxxxxx`) are stored — never
  the secret — alongside an optional `expires_at` and a soft `revoked_at` (a revoked
  key keeps its row for the audit trail but stops authenticating); `api_key_scopes` is
  the key → permission edge.
- **`threads` / `thread_messages`**: collaboration threads and their turns. Both carry `owner_id`
  and are under row-level security; a message written by the delegated agent inherits the
  thread's owner rather than inventing one.
- **`channel_messages`**: channel deliveries. `owner_id` is inherited from the parent channel, so
  the inbound webhook (which runs before any session exists) still writes a row that belongs to
  the channel's owner instead of to nobody.
- **`shared_files`**: file metadata only — `name` / `mime` / `size` / `sha256` / `backend` /
  `object_key` / `owner_id`. The bytes live on disk or in S3-compatible storage (§16).
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
  proactive schedules / mock RAG chunks), `020_router.sql` / `021_sandboxes.sql` (the scheduler and
  the sandbox control plane), `022`–`024` (task and knowledge ownership, explicit resource
  visibility), `025_audit_log.sql`, `026`–`028` (row-level policies), and
  `029_channel_messages_owner.sql` / `030_threads_rls.sql` / `031_shared_files.sql` — each of the
  last three backfills the rows it inherits **inside the same transaction** that turns `FORCE` on,
  so there is no window where a policy exists and the historical rows do not satisfy it.
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

### Nine Screens

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
- **Audit** (new, `audit:read`): range presets and filters, four water-level counters,
  the row table (time to the millisecond, request id, actor, action, resource, status and
  a latency bar) and a forensics drawer showing `prev_hash` and `hash` side by side, plus
  a verifier that reports the first row whose hash does not link. Empty state and the
  denied state are both real screens, not a blank table.
- **Documentation** (`/docs`): the console explains itself, offline and shipped with the
  build. Five sections — concepts, A2A methods, HTTP API, authentication, errors — with a
  sticky index that follows the reader. The endpoint table covers the full route table
  (141 routes) with method, path, required permission and request/response samples taken
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
- **Shared files**: `AGENTHUB_FILES_STORE` (`off` default / `local` / `s3`),
  `AGENTHUB_FILES_DIR`, `AGENTHUB_FILES_MAX_BYTES` (32 MiB), and for the S3 backend
  `AGENTHUB_FILES_S3_ENDPOINT` / `_BUCKET` / `_REGION` / `_PATH_STYLE` / `_ACCESS_KEY_ENV` /
  `_SECRET_KEY_ENV`. Choosing `s3` without an endpoint or a bucket fails `config.Validate()` at
  startup; the credentials themselves are only ever read from the named environment variables.
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
  any user — a key can act but never issue more keys), and `audit:read` (read the trail;
  its own permission, so reviewing what happened never implies administering users). The
  owner holds all of them.
- **Isolation & audit** (see §15): `AGENTHUB_RLS` (`enforce` default, `off` for local work
  and refused under `AGENTHUB_ENV=prod` without `AGENTHUB_RLS_ACK=1`),
  `AGENTHUB_MIGRATE_DATABASE_URL` (DDL connection; falls back to `DATABASE_URL` with a
  warning), `AGENTHUB_AUDIT_ENABLED` (true), `AGENTHUB_AUDIT_RETENTION` (2160h),
  `AGENTHUB_AUDIT_QUEUE` (8192) and `AGENTHUB_AUDIT_BATCH` (64).
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
  `go tool cover -func`. With a database attached: `identity` and `schedules` 100%,
  `sandbox` 98.2%, `a2a` 98%, `netutil` 97.4%, `model` 95.7%, `adapter` 95.4%, `audit`
  95.1%, `config` 94.4%, `httputil` 92.1%, `authz` 91.8%, `middleware` 91%, `registry`
  87.9%, `router` 82.2%, `hub` 82.1%, `auth` 81.4%, `secure` 80.5%, `handlers` 73.1%,
  `channels` 71.4%, `backup` 57.3%, `store` 3% (its cases skip without `DATABASE_URL`,
  so that number says nothing until one is set).
- **Isolation & audit cases**: `internal/identity` covers the four shapes of "no
  identity"; `internal/audit` covers chain continuation across a restart, a failed
  insert that is counted rather than fatal, a full queue that drops instead of
  blocking, `Verify` locating the break, and the retention branches;
  `internal/handlers` drives every audit filter, the 400s for a bad cursor, limit or
  window, the 503 when no store is wired, and asserts that reading the trail does not
  escalate to the system identity. `model_test.go` pins all 45 table names against the
  migrations, and `auth-store.spec.js` reads `authz.AllPermissions` out of the Go
  source so the console vocabulary cannot drift.
- **End-to-end cases**: `tests/e2e_test.go` drives real HTTP through httptest across six
  legs (startup check → discovery → send a task → streaming run → suspend/resume →
  control-plane round trip).
- **Frontend**: `bunx vitest run`, 417 tests passing across 21 files (languages /
  copy-fallback / enums-copy / api / api-endpoints / audit / auth-store / format /
  errors / backup-api / docs-content / playground / store / user-form / use-copy /
  use-dismiss / style guard, plus the sandbox, router and files surfaces).
  `bun run test:coverage` (v8) reports 100% of statements, 95.8% of branches and 100% of
  functions; the uncovered branches are the defensive arms a screen never reaches.
- **The suite runs as the production role too**: a second CI job provisions
  `agenthub_migrator` / `agenthub_app` from `scripts/provision-db-roles.sql`, migrates as the
  migrator and then runs the whole `go test ./...` as `agenthub_app` — DML only, no
  `BYPASSRLS`, no DDL. Cases that need to create tables go through `internal/testdb`:
  `TempSchema` builds an `it_<random>` schema and points `search_path` at it, and `RequireDDL`
  turns "this role cannot create tables" into a skip that names the reason
  (`production role: no DDL, use a migrator DSN`) instead of a failure. Without that, the
  first production-shaped run would have reported a permission error as a defect.
- **A2A & control-plane smoke**: send (once per kind), stream (no `[DONE]`, terminal frame
  last), `input-required` → `tasks/resume`, `tasks/get` history trimming, v0.1 aliases,
  flow branch and parallel, error-code paths, and the register → invoke → delete round
  trip.
- **API key coverage**: `internal/handlers/api_apikeys_test.go` exercises listing (own vs
  all), creation scope-clamping, manage guards (403 / 404 / 400), update + revoke and
  bearer-key authentication against an in-memory key store; `tests/apikey_integration_test.go`
  gates the PostgreSQL path (migration, token-hash lookup, scopes round trip, revoke +
  audit row) on `DATABASE_URL`.
- **Guards beyond the happy path**: the tests now pin the failure modes that used to be
  silent. `internal/httputil/httputil_test.go` asserts the status of **every** declared
  error code and parses `json.go` with `go/parser`, so a code added without a pinned status
  fails the suite instead of degrading to 500; `internal/handlers/wrap_test.go` pins the
  panic → JSON error path plus the HSTS / security headers; `internal/auth/apikey_test.go`
  covers token minting, the active window, scope clamping, principal precedence over the
  session and the secure-cookie warning; `internal/handlers/api_admin_lists_test.go` covers
  the admin list endpoints and the grant resource filter;
  `internal/router/bus_test.go` + `internal/router/service_test.go` drive the scheduler
  facade (inject / run / cancel / hand back / bind / policies / stream + replay) and the
  event bus (subject filtering, slow subscribers, close, nil safety);
  `internal/netutil/ssrf_test.go` pins the SSRF guards (loopback, trusted private hosts,
  redirect refusal); `internal/handlers/speech_test.go` separates an empty clip (400) from
  an oversized one (413).
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
  A clip over the cap answers **413 `audio_too_large`** rather than `400 audio_unsupported`,
  so the console can tell "record something shorter" from "that format will never work";
  the cap is enforced both while the multipart reader fills its buffer and while the file
  part is read, which is where `http.MaxBytesReader` actually trips.
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
- **Database**: run with a least-privilege role that is **not** the table owner and has no
  `BYPASSRLS` — either would skip the row-level policies in §15.
  `scripts/provision-db-roles.sql` provisions `agenthub_migrator` (schema owner, runs DDL),
  `agenthub_app` (DML only) and `agenthub_readonly`. The connection pool is tunable via
  `AGENTHUB_DB_MAX_OPEN_CONNS` (25) / `AGENTHUB_DB_MAX_IDLE_CONNS` (10) /
  `AGENTHUB_DB_CONN_MAX_LIFETIME` (30m). Whether the DB connection uses TLS is left to the
  DSN (`sslmode=…`).
- **Backup encryption**: when `AGENTHUB_ENCRYPTION_KEY` is set, checkpoint archives are
  encrypted at rest (AES-256-GCM) to `<file>.tar.gz.enc` with a `<file>.enc.sha256` manifest;
  verify recoverability with `scripts/backup-restore-drill.sh`.

---

## 14. Execution Sandboxes

The brain and the hands are now separate. `internal/sandbox` is a control plane that leases an
execution environment per `(owner, resource, scope)`; nothing the platform does on behalf of an
agent runs as the host process any more.

### Kernel

- **Catalogue, not branches**: `sandbox.Registry` maps a name to a `Provider` plus a `Descriptor`
  (layer, isolation strength, required binaries, source). Availability is probed, cached for 30s
  and single-flighted — never spawned on a request path.
- **One writer**: `Manager` resolves a key, reuses or creates, executes, records the audit row and
  reclaims on TTL. `Service` is the facade and owns the only goroutine (a single-flight sweep with a
  merge-one kick), the same shape `internal/router` already uses.
- **Four layers plus platform**: container (`docker`, `podman`), user-space kernel (`runsc`),
  microVM (`kata`), process (`bwrap`, plus declarative `nsjail` / `isolate` / `systemd-run` /
  `local`), and hosted runtimes (`e2b`, `microsandbox`, `daytona`, `cubesandbox`, `openhands`) as
  configuration templates. `mock` is deterministic and offline; `off` refuses everything.
- **Three extension paths**: compile-time provider, declarative argv template, node protocol. Adding
  a backend changes no Go code above the registry.
- **Capabilities are declared, then enforced**: each `Descriptor` now names only what that backend
  can actually do. `local` advertises `platform` alone (a bare child process has no network
  namespace); `bwrap` dropped `bridge`, because `--share-net` is the host namespace, not a NAT
  interface; `docker` advertises `none|bridge|platform` and `createArgs` matches it, passing the
  daemon's own default for `platform` instead of the literal `bridge`; remote nodes, hosted shims
  and undeclared templates advertise **`delegated`**, because this process constrains nothing and
  the node does. `Descriptor` also gained `EnvKeys` (environment-variable names a shim needs) and
  the new `SourcePlatform`, so a shim compiled into the binary no longer pretends to be a
  declarative template with a binary in `Requires`.
- **Admission is fail-closed**: `Manager.Acquire` describes the resolved provider and refuses a
  profile whose network mode the backend never declared — `sandbox_capability_unavailable`, with
  the backend never called. Asking bubblewrap for `bridge` used to receive the host's network
  namespace; now it is an answer.

### Guarantees

- **Docker by default, refusal by design**: `AGENTHUB_SANDBOX_PROVIDER=docker`. An unreachable
  socket yields `sandbox_unsupported` with the reason shown in the catalogue and `/healthz`; there
  is no fallback to host execution.
- **Leases outlive tasks**: cancelling a task does not destroy its environment, and a vanished
  environment does not fail a task — the next call rebuilds one transparently.
- **Ticket-scoped reuse**: the scope resolves `metadata.ticket` → `metadata.contextId` → `default`,
  so the steps of one router ticket share an environment. `Hub.SendAs` carries the owner through
  so those leases are attributed instead of being treated as anonymous.
- **Concurrency is coordinated**: `SANDBOX_MAX_TOTAL ≥ ROUTER_MAX_TICKETS` is checked at startup,
  and a refused lease is a retryable `sandbox_limit_reached` for the scheduler's back-off.
- **Credentials stay out of the model**: remote agent calls inject `credential_env` server-side;
  task metadata and artifacts keep only the variable name.

### Closed execution surfaces

- CLI no longer derives a command from the prompt's first word: the command must equal the
  registered one, arguments are checked against `args_policy`, and a prompt can at most become
  stdin. The receipt is real (`exitCode`, `durationMs`, `truncated`, `provider`).
- MCP stdio starts inside the lease, so the tool allow list is enforced where the process runs.
- Computers resolve to real leases (`provider` / `lease_id` / `node_id` / `expires_at`) and can be
  probed once from the console.
- Remote agents egress through the server, with an allow list.

### Persistence, API and console

- `021_sandboxes.sql` adds `sandbox_leases` / `sandbox_execs` / `sandbox_nodes` / `sandbox_jobs` /
  `sandbox_events` plus the new `computers` columns; the audit table stores digests, never output.
- `/api/sandboxes`, `/{id}`, `/stats`, `/events` (SSE with `since`), `/api/sandbox-providers` (grouped
  by layer, with reasons) and `/api/sandbox-nodes` (enroll, heartbeat, long-poll, report).
- An instance without the control plane answers `503 feature_unavailable`; a wired instance whose
  backend cannot run answers `sandbox_unsupported`. The two are deliberately different, because one
  is "configure it" and the other is "install it".

### Tests

- `internal/sandbox/sandbox_test.go`: reuse, scope isolation, anonymous non-reuse, owner ceiling,
  audit rows, terminate, renewal ceiling, sweep, catalogue reasons, duplicate registration, stats,
  event replay, and the job queue (single claim, capability match, unknown completion).
- `tests/sandbox_e2e_test.go`: the console endpoints over real HTTP, an empty command refused, an
  unknown lease 404, another owner's lease invisible, `503` when not wired, node enroll → claim →
  report (and a second node refused), and the SSE replay.
- agenthub web: `docs-content.spec.js` pins the sandbox docs group across all 13 languages, and the
  monochrome guard still runs over the new copy.

---

## 15. Data Isolation & Audit Trail (M3)

Two gaps a review left open: isolation lived in the application layer, so forgetting one
filter made a task, its artifacts and its events everybody's; and "who called what" existed
only as log text — unverifiable, and trimmable by anyone with a `DELETE`.

### Row level security

- **Ownership columns**: `tasks` and its children (`task_artifacts`, `task_spans`,
  `task_pending`, `task_push`), plus `events`, `knowledge`, `schedules`, `channels`, `computers`,
  and — since this round — `threads`, `thread_messages`, `channel_messages` and `shared_files`,
  all carry `owner_id TEXT NOT NULL DEFAULT ''`; `''` means the row was produced by the process
  itself. Pre-existing task rows are **not** backfilled — nobody guesses the owner of history —
  but where the owner is *knowable* it is copied rather than invented: a thread message inherits
  its thread's owner, a delivery inherits its channel's, and each of `029` / `030` does that in
  the same transaction that turns `FORCE` on.
- **Policies** (`026_rls_policies.sql`, `028_rls_policies_owned_surfaces.sql`, `029`, `030`,
  `031` — **fifteen tables**): those tables and `audit_log` are `ENABLE` +
  `FORCE ROW LEVEL SECURITY` (`FORCE` binds the table owner too), and
  `agenthub_row_visible(owner_id)` reads three per-transaction settings — `app.owner_id`
  (the signed-in user), `app.bypass` (administrator) and `app.system` (in-process paths:
  the task state machine, the broker, seed, migrations, backups, the audit writer). An
  unset setting reads as NULL, so a request that forgets to scope itself sees **nothing**:
  the failure mode is closed, not open.
- **Short transactions**: `internal/store.scoped` opens one transaction per repository
  call and injects the identity with `set_config(..., true)`. A request never holds a
  connection — the SSE paths would otherwise starve the 25-connection pool.
- **Separated roles**: `scripts/provision-db-roles.sql` provisions `agenthub_migrator`
  (schema owner, runs DDL), `agenthub_app` (DML only, not the table owner, no
  `BYPASSRLS`) and `agenthub_readonly`, and aborts while any guarded table lacks
  `FORCE` RLS. DDL goes through `AGENTHUB_MIGRATE_DATABASE_URL`.
- **`AGENTHUB_RLS=off`** appends `options=-c app.system=on` to the DSN so every policy
  passes. That is for local work and tests: `AGENTHUB_ENV=prod` refuses it without
  `AGENTHUB_RLS_ACK=1`, and either way the startup log says the boundary is relaxed.

### Resource visibility is explicit (breaking)

- `resources.visibility` is `restricted` or `public`, default **`restricted`**.
  Readability is `public || a grant`; `authz` no longer treats "no grant" as "yes".
  A record that is neither published nor granted is invisible *and* uncallable —
  including to anonymous A2A peers.
- Upgrading therefore hides every resource that used to be implicitly public, and there
  is no backfill: an operator marks it `public` or grants access, as described in
  `requirements-isolation.md` §3.1. Demo records are seeded as `public`.

### Audit trail

- **One row per call**: every `/api` and `/a2a` request appends `event_id`, `ts`,
  `source`, `action`, `read_only`, `actor_*`, `ip`, `method`, a redacted `path`,
  `resource_*`, `status`, `error_code`, `latency_ms`, `user_agent` and `request_id`.
  Query *values* become `REDACTED`; no request or response body is ever stored, and the
  sensitive keys (`token` / `key` / `secret` / `password` / `authorization` / `cookie`)
  never reach the table.
- **Append-only twice over**: a trigger rejects `UPDATE` / `DELETE` / `TRUNCATE` for
  every role including the owner (retention deletes run under `app.retention`), and the
  runtime role holds only `INSERT` / `SELECT` on the table.
- **Hash chain**: `hash = sha256(prev_hash ‖ canonical(event))`, with the canonical form
  a fixed field order that excludes the hash itself. Startup continues from the last
  row, so a restart does not fork the chain. A single writer serialises it behind a
  bounded queue with batched inserts (8192 / 64 / 500 ms by default); a full queue drops
  frames and counts them, and never blocks a request.
- **Endpoints**: `GET /api/audit` (filters `from`, `to`, `actor`, `action`, `resource`,
  `source`, `outcome`, `q`, `before`, `limit`), `GET /api/audit/{id}` and
  `GET /api/audit/verify?from=&to=` → `{ ok, checked, brokenAt }`, where `brokenAt` is
  the first row whose hash does not link. All three need the new `audit:read`
  permission, granted to the admin role by `027_audit_permission.sql`.
- **Console**: a tenth screen — range and filters, water-level counters, the row table
  and a forensics drawer that shows `prev_hash` and `hash` side by side.

### A2A authorization and parameter compatibility

- `message/send|stream` must resolve a concrete `resourceId` (from
  `metadata.resourceId`, or inferred from the selection) before anything runs; if it
  cannot, the call is refused with `resource_not_found`.
- `tasks/get|cancel|resume` and `tasks/pushNotification/set|get` require the caller to
  own the task or be an administrator. Somebody else's task answers `task_not_found`
  rather than 403, so ids cannot be enumerated; an anonymous caller that needs one of
  those methods gets `-32009 unauthenticated`.
- `pushNotification/set` used `taskId` while `get` used `id`. Both are accepted now, the
  canonical field wins when they disagree, and every task-scoped method shares one
  resolver — so one client implementation works across all of them.

---

## 16. Shared Files & Connector Approvals (M4)

Two capabilities that existed only as an idea: a file a person can hand to an agent, and a
`ask_first` decision somebody can actually answer.

### Shared files

The bytes live outside the database; only their metadata does.

- **Switch**: `AGENTHUB_FILES_STORE=off|local|s3`, default **off**. With it off every
  `/api/files*` route answers `503 files_disabled`, and the console says so rather than
  showing an empty list.
- **Endpoints**: `GET /api/files` (`{ files, maxBytes, backend }`), `POST /api/files`
  (multipart, first file part), `GET /api/files/{id}`,
  `GET /api/files/{id}/content` (attachment download through `http.ServeContent`, so `Range`
  works and a large upload can be resumed) and `DELETE /api/files/{id}`. Permissions reuse
  `tasks:read` / `tasks:write`; no new permission code.
- **Limits and integrity**: `AGENTHUB_FILES_MAX_BYTES` (32 MiB) is enforced *while* the bytes
  stream to a temporary file, so an oversized upload never reaches the store (`413
  file_too_large`). Every row carries the sha256 and the sniffed MIME type; the claimed type is
  kept only when sniffing cannot do better. A name is reduced to its base name, and an empty one
  falls back to `upload.bin`.
- **Local backend**: written to a temporary file and moved into place with `rename` (never a
  partial file under the final key), mode `0600`, sharded by the first two hex digits of the
  digest. A key that is not `<2 hex>/<64 hex>` — including anything with `..` — is refused
  before it touches the filesystem.
- **S3-compatible backend** (RustFS, MinIO, AWS): SigV4 written against the standard library, so
  the module adds **no dependency**. Path-style addressing is required (RustFS/MinIO), a single
  `PUT` carries `x-amz-content-sha256` (no multipart, no chunked signing — a file already has a
  size cap), and the credentials are read from the environment variables *named* by
  `_ACCESS_KEY_ENV` / `_SECRET_KEY_ENV`. Logs carry the backend, the object key prefix and the
  byte count — never the signature, the key or an endpoint with userinfo.
- **Ownership**: `shared_files.owner_id` plus `FORCE` row-level security, so a second account
  cannot list, read or delete somebody else's file even with the id. A row that is not yours
  answers `404 file_not_found`, not `403`.
- **Tests**: `internal/files` (limits, hashing, name handling, backend failure, key traversal,
  cancellation) at 86.4% of statements, `s3_test.go` against an `httptest` server (signature
  headers, escaping, no redirect following, transport failure, size mismatch) and
  `internal/handlers/files_test.go` for the HTTP surface — upload → list → metadata → download
  with `Range` → delete, plus 413 / 415 / 404 / 502 / 503.

### Connector approvals

`ask_first` already parked a task; it now parks everything a person needs to decide, and the
queue is derived from those tasks, so there is **no approval table**.

- **What is parked**: `Hub.ParkConnector` writes `metadata.connectorApproval =
  { action, args, requestedBy, resourceId }` onto the `input-required` task. The console can
  therefore show the connector, the action name and the arguments instead of a task id.
- **Queue**: `GET /api/connectors/approvals` (`tasks:read`) reads the parked tasks directly.
  `hub.TaskStateLister` is an **optional port** — the GORM repository filters by state (index +
  row-level security) and the in-memory store filters a slice; the existing `TaskRepository`
  interface is untouched, so no test double broke.
- **Decide**: `POST /api/connectors/approvals/{id}/approve` resumes the task with the parked
  arguments, so the connector runs **exactly once**; `POST .../reject` cancels it and nothing
  runs. A task that is not parked, already decided, or somebody else's answers `404
  connector_approval_not_found`; deciding twice answers `409 connector_approval_closed`.
- **Tests**: `handlers/connectors_approval_test.go` covers the derived queue, approve-runs-once,
  the duplicate-approval conflict, reject-then-approve conflict, unknown and unrelated tasks, and
  the `503` when the hub is not wired; `hub/orchestrator_park_test.go` pins the parked metadata.