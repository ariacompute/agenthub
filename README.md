<div align="center">
<a name="readme-top"></a>

# agenthub

**Register an asset once, and every agent can reach it**

model / agent / mcp / skill / cli are registered, discovered and orchestrated<br/>
behind one standard A2A endpoint that any conforming agent can call.

**English** · [简体中文](./README_cn.md) · [Releases][releases] · [Docs][docs] · [Issues][issues]

[![][badge-release]][releases] [![][badge-ci]][ci] [![][badge-license]][license]
<br/>
[![][badge-go]][go] [![][badge-postgres]][postgres] [![][badge-vue]][vue] [![][badge-stars]][stars] [![][badge-forks]][forks]
</div>

<details>
<summary><kbd>Table of contents</kbd></summary>

#### TOC

**Get started**

- [🧠 Concept map](#-concept-map)
- [📸 Snapshot](#-snapshot)
- [🚀 Run](#-run)
  - [🏷 Version stamping](#-version-stamping)
- [📚 Examples](#-examples)

**Deploy**

- [🛳 Deployment](#-deployment)
  - [🔨 Make](#-make)
  - [🐳 Docker compose](#-docker-compose)
  - [☸️ Kubernetes operator](#-kubernetes-operator)

**Runtime surface**

- [🔒 Security hardening](#-security-hardening)
- [🧪 Tests](#-tests)
- [🔗 A2A endpoints](#-a2a-endpoints)
- [🧩 Flow primitives](#-flow-primitives)
- [⏰ Scheduler (router)](#-scheduler-router)
  - [🆚 Compared with the dsh superagent router](#-compared-with-the-dsh-superagent-router)
- [🧱 Execution sandboxes](#-execution-sandboxes)
  - [🔍 What runs inside a sandbox](#-what-runs-inside-a-sandbox)
  - [🐋 Default is Docker, and a missing Docker is an answer](#-default-is-docker-and-a-missing-docker-is-an-answer)
  - [🆚 Compared with the hosted runtimes](#-compared-with-the-hosted-runtimes)
- [🚨 Error contract](#-error-contract)
- [🔑 API keys](#-api-keys)

**Data surface**

- [🗄 Persistence](#-persistence)
- [💾 Database checkpoints](#-database-checkpoints)
- [📁 Shared files](#-shared-files)

**Compare & governance**

- [🏛 AWS Agent Registry comparison](#-aws-agent-registry-comparison)
  - [🧾 Record types and validation](#-record-types-and-validation)
  - [♻️ Lifecycle and governance](#-lifecycle-and-governance)
  - [🔎 Discovery](#-discovery)
  - [🔐 Authorization and synchronisation](#-authorization-and-synchronisation)
  - [📊 Operations and observability](#-operations-and-observability)
  - [📋 What agenthub had, what it added, what it refuses](#-what-agenthub-had-what-it-added-what-it-refuses)
  - [🔁 recordType ↔ Kind](#-recordtype--kind)
  - [🧭 Remaining gaps](#-remaining-gaps)
- [📝 License](#-license)
- [🤝 Engineering conventions](#-engineering-conventions)

</details>

An interconnection platform built on the **A2A (Agent2Agent) protocol**: it registers, discovers,
routes and orchestrates five heterogeneous asset classes — **model / agent / mcp / skill / cli** —
and exposes them as standard A2A endpoints that any protocol-conforming agent can call.

- Backend: Go 1.27 + `net/http`, single binary (production dependencies: GORM + pgx)
- Frontend: Vue 3 + Vite (bun), **13 languages**, pure black / white / grey monochrome UI
- Persistence: **PostgreSQL + GORM** — resources, tasks, artifacts, spans, the event stream and
  push callback records are all durable
- All five adapters run **offline by default**: no network, no subprocess, fully runnable
  without credentials — a model entry can opt into a real OpenAI-compatible gateway

## 🧠 Concept map

| Concept | Role | In agenthub |
| --- | --- | --- |
| Model | The brain | `kind=model`, one `Generate/Stream` interface + provider fallback chain |
| Agent | Digital coworker | `kind=agent`, goal driven, can park for human input |
| MCP | Universal adapter | `kind=mcp`, protocol translation only, no business logic; `transport` is descriptive |
| Skill | Job manual | `kind=skill`, versioned process asset: trigger + steps + tool list |
| CLI | Walkie-talkie | `kind=cli`, allowlist → timeout → truncation → structured JSON receipt |

One call: user → Agent → Model plans → Skill loaded → MCP reaches the legacy system → the result
travels back the same path.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📸 Snapshot

![agenthub console — Registry screen](registry.png)

The Registry screen: ten screens in one console (registry, registries, sources, task trace,
topology, playground, sign-in, administration, audit and a built-in documentation page).

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🚀 Run

```bash
cp .env.example .env                        # local development: copy the env template, then edit values as needed
# cp .env.sec.example .env                  # hardened production: copy the security template, then replace every <...> placeholder with a real secret
make db-up                                  # start PostgreSQL (host port 5434) and wait until it is ready
make run                                    # defaults to :3500 (Open → Migrate → Seed)
curl localhost:3500/healthz
curl localhost:3500/.well-known/agent.json
```

`DATABASE_URL` is **mandatory**: `config.Validate` refuses to start without it (there is no
database-less fallback). Two different hosts are involved:

- `.env` (copied from `.env.example`) holds the **in-container** value
  `postgres://agenthub:agenthub@postgres:5434/agenthub`: compose injects it into the
  `agenthub` container, where `localhost` is the container itself, not the database.
- `make run` overrides it inline with `postgres://agenthub:agenthub@localhost:5434/agenthub`
  for the host-run server. `make db-up` waits for both the container's healthcheck **and**
  the published host port, so a missing port publish fails early with an explicit message
  instead of surfacing as `connection refused` from `go run`.

For a hardened production deployment rather than local development, start from the
security-hardened template instead: `cp .env.sec.example .env` (see
[Security hardening](#-security-hardening)). It pre-applies every production safeguard below and
ships with placeholder secrets you must replace before the instance can boot.

Console: `make web-dev` (development) or `make web-build && make run` (the bundle is hosted at `/`
by the Go process).

Documentation: the console documents itself on a **`/docs`** screen — concepts, the seven A2A
methods and all 144 HTTP routes. The same content is rendered to
[`docs/docs.md`](docs/docs.md) by `make docs-md`; that file is **generated** from the two modules
the screen reads (`web/src/lib/docs-content.js` and `web/src/lib/copy/docs-copy.js`), so edit those
and re-run rather than editing the markdown.

The registry starts **empty** — nothing is seeded. Set `AGENTHUB_SEED_DEMO=true` to get the five
`demo-*` resources the examples below talk to, or load [`example/`](example/README.md) to fill it
with a realistic catalogue.

### 🏷 Version stamping

`make build` and `make run` stamp `config.Version` (the git tag, e.g. `v0.2.0`),
`config.Commit` (the seven character revision), `config.Distance` (commits since the tag)
and `config.Dirty` via `-ldflags -X`. `/healthz` returns them, the Agent Card carries the
version, and the console shows **`version@commit`** (for example `v0.2.0@1a2b3c4`) in the
bottom-left corner of the sidebar, with a second line such as `+3 dirty` whenever the
build is not exactly a clean tag. An unstamped build reports the `dev` sentinel instead
of pretending to be a release. Images take the same values through the `VERSION` /
`COMMIT` / `DISTANCE` / `DIRTY` build args (`make docker-build`).

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📚 Examples

[`example/`](example/README.md) registers a realistic catalogue straight from the repository: 41
complete `registry.Entry` payloads plus a re-runnable loader, covering all five kinds in both a
Windows and an Ubuntu shape. Four `deploy-*.sh` scripts go one step further and materialise one
asset each on WSL2 / Ubuntu: an A2A agent, a stdio MCP server, a skill directory or a CLI command.

```bash
cp .env.example .env     # then set AGENTHUB_EXAMPLE_ENABLED=true and
                         # AGENTHUB_EXAMPLE_MODEL_ID / EMAIL / PASSWORD
./example/load.sh        # picks the platform from uname -s
./example/load.sh --all  # both platforms

./example/deploy-agent.sh google-adk start      # stand one agent up so its card_url answers
./example/deploy-agent.sh google-adk smoke      # print its Agent Card, then send one message/send
./example/deploy-mcp.sh filesystem start        # serve one stdio MCP server in the background
./example/deploy-skill.sh anthropics-pdf start  # acquire the skill directory at its file:// endpoint
./example/deploy-cli.sh gh start                # PATH + AGENTHUB_CLI_ALLOWLIST preflight
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
per platform, what each of the four deployment scripts does, and the two settings people trip over:
`AGENTHUB_EXAMPLE_API_KEY` for the gateway and `AGENTHUB_CLI_ALLOWLIST` for the CLI commands.

The deployment scripts are deliberately separate from the loader: none of them logs in, touches the
database or calls `/probe`. `deploy-agent.sh` installs, serves and verifies one agent, which is what
`Try run` and a health probe need before they can report anything measured; `deploy-mcp.sh` holds a
stdio server open over a FIFO and checks its tool list, `deploy-skill.sh` acquires the `file://`
directory and reads `SKILL.md`, and `deploy-cli.sh` checks PATH plus `AGENTHUB_CLI_ALLOWLIST` before
running a read-only probe. The server-side mcp / skill / cli connectors are still `TODO(real)`, so
those three verify the local asset only.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🛳 Deployment

Both flows below bring **database + service** up — the console is baked into the image and served
at `/` by the Go process. Use `make` when you want the port map afterwards; plain compose needs
nothing but Docker and Compose.

### 🔨 Make

```bash
cp .env.example .env  # local development: copy the env template, then edit values as needed
# cp .env.sec.example .env  # hardened production: copy the security template, then replace every <...> placeholder with a real secret
make up               # docker compose up -d --build (rebuilds the image from source), then prints the port map
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
  postgres           5434 -> 5434     healthy    the only state: resources, tasks, spans, events, push records

  ENDPOINT                                            USE
  http://127.0.0.1:3500/                              Vue 3 console (bundle baked into the image)
  http://127.0.0.1:3500/.well-known/agent.json        A2A Agent Card (discovery)
  POST http://127.0.0.1:3500/a2a                      A2A JSON-RPC: message/send, message/stream, tasks/*
  http://127.0.0.1:3500/api/*                         control plane: resources, tasks, events, push records
  http://127.0.0.1:3500/healthz                       health probe, also the container healthcheck
  postgres://agenthub@localhost:5434/agenthub         host DSN (inside compose it is postgres:5434)
```

Everything above is one listener inside the container (always 3500); the published host port is
compose-only knowledge, hence the table. The server itself logs the same endpoint list on boot, so
an attached `docker compose up` or `docker compose logs -f agenthub` spells it out too.

The database also joins a dedicated `hostpg` network: Docker does not publish a host port for a
container attached only to an `internal` network, so without it `ports: 5434:5434` silently does
nothing and `make run` cannot connect. Only postgres joins `hostpg` and masquerade is disabled
there, so the console side still cannot reach the database and the database gains no outbound
route.

### 🐳 Docker compose

```bash
docker compose up -d --build                # rebuild from source, then start postgres + agenthub, :3500 / :5434
docker compose up -d --force-recreate       # re-create containers even when compose detects no change — e.g. after editing .env / env_file, so new env vars reach the container
docker compose ps                           # STATUS should be Up (healthy)
docker compose logs -f agenthub
docker compose --profile dev up -d --build  # rebuild from source; also starts the web dev server on :5173
docker compose down                         # stop; data survives in the agenthub-pgdata volume
```

`--force-recreate` tears down and re-creates the containers unconditionally. Use it
after editing `.env` (the stack reads your config through `env_file`): a plain
`up -d` may skip recreation when only the file's contents changed, leaving the old
environment inside the container — a common cause of "I changed `.env` but the app
still sees the old value".

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
# AGENTHUB_PUSH_SECRET=<16+ byte random secret>           # optional: signed, SSRF-guarded A2A push callbacks
# AGENTHUB_DB_MAX_OPEN_CONNS=25 AGENTHUB_DB_MAX_IDLE_CONNS=10 AGENTHUB_DB_CONN_MAX_LIFETIME=30m  # pool tuning
```

With `AGENTHUB_ENV=prod` and no `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL`, `config.Validate` **refuses to
start**: a production instance without an administrator is unrecoverable. The account is upserted at
every boot and never has its password overwritten, so a password changed in the console survives a
restart. Configuration is passed through the compose `.env` file or environment variables;
`AGENTHUB_HTTP_PORT` changes the published host port.

### ☸️ Kubernetes operator

For a declarative, GitOps-friendly deployment on Kubernetes, `operator/` ships a
controller-runtime based operator. Install the operator + the `AgentHub` CRD via Helm,
then apply a declarative `AgentHub` custom resource and the controller reconciles it into a running
agenthub-server Deployment. PostgreSQL stays external; the connection string is injected through the CR.

- `operator/README.md` — install steps, the full `AgentHub` CRD field list, the external-DB
  requirement, the prod prerequisites (`BOOTSTRAP_ADMIN_EMAIL` + `ENCRYPTION_KEY` ≥ 32 bytes)
  and the multi-replica migration note (startup migrations are serialised by an advisory lock,
  so scaling up no longer races on DDL).
- `operator/deploy/agenthub-operator/` — the Helm chart (operator Deployment + bundled CRD +
  namespace-scoped RBAC).
- `operator/examples/agenthub-sample.yaml` — a ready-to-apply `AgentHub` CR.

```bash
helm install agenthub-operator ./operator/deploy/agenthub-operator --namespace agenthub-operator --create-namespace
kubectl apply -f operator/examples/agenthub-sample.yaml -n agenthub-operator
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔒 Security hardening

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
  `/a2a` at **200 req/min/IP** (`429` + `Retry-After`). **Loopback clients** (the agentbuddy desktop
  shell and local dev, both seen as `127.0.0.1`/`::1`) are exempt, since their traffic collapses into a
  single shared bucket and the credential-stuffing defence does not apply to host-only addresses.
- CSRF: state-changing requests must carry a same-origin `Origin` (loopback and non-browser clients
  such as the desktop client and scripts are exempt), else `403`. The console uses same-origin
  fetch with the session cookie, so it is unaffected.
- CLI resources run the command **the record declares**, never one taken from the prompt, and the
  allowlist match is an exact command name (no prefixes, no paths). Caller arguments are refused
  unless the record's `args_policy` allows them. `internal/adapter/cli_test.go` pins both rules as
  regression tests — an allowlisted binary must not become a shell again.
- Audit rows carry the caller, the IP, the HTTP method, a redacted path, the outcome and a hash —
  never passwords, tokens, cookies or request/response bodies. See **API audit trail** above.

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

**Database.** Run-time connections should use a least-privilege role, and **that role must not own a
table and must not have `BYPASSRLS`** — either of those silently skips the isolation policies.
`scripts/provision-db-roles.sql` provisions three roles (`migrator` owns the schema and runs DDL,
`app` is DML-only, `readonly` is SELECT-only) and refuses to finish while any guarded table lacks
`ENABLE`+`FORCE ROW LEVEL SECURITY`. Point DDL at the migrator with
`AGENTHUB_MIGRATE_DATABASE_URL`; when it is unset, migrations fall back to `DATABASE_URL` and the
startup log warns, because a single-role deployment weakens the boundary. The connection pool is
tunable (`AGENTHUB_DB_MAX_OPEN_CONNS` / `_MAX_IDLE_CONNS` / `_CONN_MAX_LIFETIME`).

**Data isolation (row level security).** Tasks, their artifacts/spans/pending/push rows and their
events, plus knowledge chunks, schedules, channels, computers, threads and their messages, channel
deliveries and shared files all carry `owner_id`. PostgreSQL policies (`migrations/026` for the task
pipeline, `028` for the three per-owner surfaces, `029` for deliveries, `030` for threads and their
messages, `031` for shared files — **fifteen tables** in total) decide visibility from three
per-transaction settings — `app.owner_id` (the signed-in user), `app.bypass` (administrator) and
`app.system` (in-process paths: the task state
machine, the event broker, the cron scan, seed, migrations, backups, the audit writer). An unset
setting reads as NULL and every comparison is false, so **a request that forgets to scope itself sees
nothing** rather than everything. Each repository call opens a short transaction
(`internal/store.scoped`) and injects the identity with `set_config(..., true)`; a request never holds
a connection.

The application layer states the same rule in its port signatures: by-id reads are
`Get(ctx, id, ownerID)` (or `Delete`/`Update` with the owner), and a row belonging to somebody else
answers `404`, never `403` — existence is not leaked. The single unnamed-owner read is
`channels.GetUnscoped`, used by the inbound webhook, which has to read the row to learn which secret
verifies the body before any session exists; it runs under the process identity. That endpoint's
signature check, replay window and de-duplication are unchanged.

`AGENTHUB_RLS=enforce` is the default. `AGENTHUB_RLS=off` makes every policy pass (it appends
`options=-c app.system=on` to the DSN) and logs a loud warning; with `AGENTHUB_ENV=prod` it is refused
unless `AGENTHUB_RLS_ACK=1` also says the operator meant it.

Resource visibility is **explicit**: `resources.visibility` is `restricted` by default and `public`
on request. Access is `public || a grant`, so a resource with no grant and no `public` flag is
invisible *and* uncallable, including to anonymous A2A peers. **This is a breaking change** — upgrade
steps, and the three ways to restore an existing record, are in `requirements-isolation.md` §3.1.
Demo records (`AGENTHUB_SEED_DEMO=true`) are seeded as `public`.

**API audit trail.** Every `/api` and `/a2a` call appends one row to `audit_log`, using CloudTrail's
vocabulary: `event_id`/`ts`/`source`/`action`/`read_only`/`actor_*`/`ip`/`method`/`path`/
`resource_*`/`status`/`error_code`/`latency_ms`/`user_agent`. Paths are redacted (query values become
`REDACTED`) and no request or response body is ever stored. The table is append-only twice over: a
trigger rejects `UPDATE`/`DELETE`/`TRUNCATE` for every role including the owner, and the runtime role
is granted only `INSERT`/`SELECT` on it. Rows are hash-chained
(`hash = sha256(prev_hash ‖ canonical(event))`), so editing or removing one breaks every later link
and `GET /api/audit/verify` points at the first broken row. A single writer serialises the chain with
a bounded queue and batched inserts (8192 / 64 / 500 ms by default); a full queue drops frames and
warns — it never blocks a request.

| Variable | Default | Meaning |
| --- | --- | --- |
| `AGENTHUB_AUDIT_ENABLED` | `true` | Record the trail at all |
| `AGENTHUB_AUDIT_RETENTION` | `2160h` | How long a row may live; expiry itself is audited |
| `AGENTHUB_AUDIT_QUEUE` | `8192` | Bounded queue depth (frames beyond it are counted and dropped) |
| `AGENTHUB_AUDIT_BATCH` | `64` | Rows per insert (`1..AGENTHUB_AUDIT_QUEUE`) |
| `AGENTHUB_RLS` | `enforce` | `off` relaxes every policy (dev only; refused in prod without `AGENTHUB_RLS_ACK=1`) |
| `AGENTHUB_MIGRATE_DATABASE_URL` | unset | DDL connection; falls back to `DATABASE_URL` with a warning |

Reading the trail needs the dedicated `audit:read` permission: `GET /api/audit` (filters `from`, `to`,
`actor`, `action`, `resource`, `source`, `outcome`, `q`, `before`, `limit`), `GET /api/audit/{id}`, and
`GET /api/audit/verify?from=&to=`. The console shows them on the **Audit** screen.

**Backups.** When `AGENTHUB_ENCRYPTION_KEY` is set, checkpoint archives are encrypted at rest
(AES-256-GCM) into `<file>.tar.gz.enc` with a `<file>.enc.sha256` manifest — plaintext never touches
disk. Verify recoverability periodically with `scripts/backup-restore-drill.sh`.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧪 Tests

Unit and integration suites:

```bash
make db-up             # integration cases need a real PostgreSQL
make db-migrate        # create the schema; both test targets run this first
make test              # go test -race ./... (integration cases skip without DATABASE_URL)
make test-integration  # only integration_test.go
make test-cover        # -coverprofile + go tool cover -func, per package and in total
make web-test          # cd web && bunx vitest run
```

Coverage is measured per package. Two things are worth knowing before reading the numbers: the
governance rules are driven through in-package fakes, so the store-backed branches of `registry`
are covered without a database, while `internal/store` only measures once `DATABASE_URL` is set —
its cases skip rather than fabricate.

End-to-end test against a running instance — two independent user-facing suites in `e2e/`
(see [`e2e/README.md`](e2e/README.md) for the full switch list and report format). The API suite is
a declarative case table covering **every documented route** (144): discovery, auth/session,
resources and governance, the A2A methods with HITL park → resume / cancel, streaming and `since`
replay, tasks, RBAC and API keys, channels/connectors, threads/speech, files, sandboxes,
schedules/computers/knowledge, router, audit and checkpoints. The UI suite drives six real console
journeys (login, register a resource, playground trial run, task trace, governance approvals,
administration) with Playwright. Both point at `BASE_URL`, never start or stop the service, clean
up everything they create, and write a JSON + Markdown report.

```bash
# 1. Server side: every AGENTHUB_* switch is read once at startup. Set them before
#    starting/restarting the instance, never in front of `make e2e`.
AGENTHUB_SEED_DEMO=true \
AGENTHUB_CHANNELS_ENABLED=true \
AGENTHUB_ROUTER_ENABLED=true AGENTHUB_ROUTER_RESOURCE=demo-mcp \
AGENTHUB_CLI_ALLOWLIST=git,ls,cat,echo,date,aria-memo,gh \
AGENTHUB_BACKUP_DIR=/var/lib/agenthub/backups AGENTHUB_BACKUP_MODE=off \
make run  # host-run Go server (WSL2 / local dev)
# or: `make up` for the compose stack; both targets also read the same keys from .env

# 2. Client side: the suites only talk to BASE_URL.
cd e2e && bun install && cd ..  # first run only; isolated dev dependency
make e2e-api                    # API suite (./scripts/test-api.sh is a shim)
make e2e-ui                     # UI journeys (bun x playwright install chromium)
make e2e                        # both
make e2e-coverage               # endpoint coverage guard only, no requests
```

`AGENTHUB_SEED_DEMO=true` is the pattern to follow for the other optional modules: they are
**server startup** settings, not switches of the test runner. Prefixing `make e2e` with one of them
does not change the server. `make run` (host-run Go server, e.g. WSL2) reads `.env` through
`config.LoadDotEnv()` — it only fills keys the process does not already have, so a `VAR=… make run`
prefix wins; edit `.env` and restart that Go process. `make up` takes the same keys from compose's
`environment:` block and `--force-recreate`. `docker-compose.yml`, `.env.example` and
`.env.sec.example` list every service-side variable.

| server variable | default | why set it for a full run |
| --- | --- | --- |
| `AGENTHUB_SEED_DEMO=true` | `false` | seeds the five `demo-*` resources used as A2A / router targets; without them the suite creates its own equivalents |
| `AGENTHUB_CHANNELS_ENABLED=true` | `false` | otherwise `/api/channels*` answers `503 channel_disabled` and the whole group SKIPs |
| `AGENTHUB_ROUTER_ENABLED=true` | `false` | otherwise `/api/router/*` and `/api/schedules/{id}/run` answer `503 feature_unavailable` |
| `AGENTHUB_ROUTER_RESOURCE=demo-mcp` | empty | fallback worker; without one a ticket can answer `409 router_resource_unset` |
| `AGENTHUB_CLI_ALLOWLIST=…,gh` | `git,ls,cat,echo,date,aria-memo` | whole-string `EqualFold` match; the seeded `demo-cli` uses `git` (already allowed), but the `example/` CLI resources are not — append `gh` (and `hf` / `modelscope` for the rest) or `deploy-cli.sh gh start` refuses and a CLI probe answers `degraded` |
| `AGENTHUB_BACKUP_DIR=/var/lib/agenthub/backups` | empty | **this** is the checkpoint on/off switch (compose mounts a volume here; under `make run` use a writable host path instead) |
| `AGENTHUB_BACKUP_MODE=off\|daily\|interval\|cron` | `off` | schedule mode only — `off` still allows the manual checkpoints the suite creates |
| `AGENTHUB_FILES_STORE=local` (+ `AGENTHUB_FILES_DIR`) | `off` | shared-file routes otherwise answer `503 files_disabled`; compose needs a writable volume mounted at that path |
| `AGENTHUB_SPEECH_MODE=mock` | `mock` | the only shipping voice backend; any other value makes thread audio SKIP |
| `AGENTHUB_SANDBOX_DEFAULT_IMAGE=debian:bookworm-slim` | `debian:bookworm-slim` | `POST /api/computers` leases a container with `docker run --pull missing`; pre-pull the image (`docker pull debian:bookworm-slim`) or the call can exceed `MAX_TIME`. `AGENTHUB_SANDBOX_ENABLED=false` skips leases altogether (the sandbox group then SKIPs) |

Credentials come from `AGENTHUB_TEST_EMAIL` / `AGENTHUB_TEST_PASSWORD` (bootstrap administrator) or
`AGENTHUB_API_KEY=ahk_…` (long-lived bearer key), and `BASE_URL` defaults to
`http://127.0.0.1:3500`. `MAX_TIME` / `READY_TIMEOUT` bound per-request and readiness waits.

Capabilities not wired up on the instance are reported as SKIP, never faked. Anything needing a
third party, a real database restore, S3, a signed webhook, a push receiver or a real container
exec stays SKIP until its `AGENTHUB_E2E_*` switch is set. A built-in guard diffs the case table
against `docs/docs.md`, so a newly documented endpoint without a case fails the run. The suites are
isolated from the Go unit tests and the web Vitest baseline.

`./scripts/test-api.sh` remains as a compatibility shim that forwards to the API suite.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔗 A2A endpoints

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

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧩 Flow primitives

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

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## ⏰ Scheduler (router)

`internal/router` turns the platform from a directory that answers when called into a two-layer
scheduler: a **router** layer decides *whether* an event deserves work, a **worker** layer runs the
work through the ordinary task state machine. Nothing in `hub` changes — a ticket becomes a
`message/send` (or a `tasks/resume`), so spans, events, push callbacks and HITL parking all apply
unchanged.

Four sources feed one queue: a cron expression coming due, a message posted into a collaboration
thread, an inbound channel webhook, and a manual injection (`POST /api/router/triggers`). Every
trigger carries a dedupe key, so a redelivered webhook is answered `200 {"duplicate":true}` instead
of opening a second ticket.

Filtering has two stages:

- **Program layer** — a per-source policy (`off | candidate | request_only`) plus an allow list of
  principals, `@mention`, keywords, quiet hours and self-echo suppression. It is offline and
  deterministic, and it settles most of the traffic.
- **Semantic layer** — everything one beat admits is assessed **in a single batch call**
  (`accept / attach / stop / ignore`), against a model resource when
  `AGENTHUB_ROUTER_ASSESS_MODE=model`, and by the rules alone otherwise. Trigger text is treated as
  untrusted data, the answer is parsed against a closed vocabulary, and an unusable answer falls
  back to the rule verdict per trigger — never to "let it through".

A trigger that lands on a thread somebody already subscribed to skips the decision and goes
straight to the subscriber, and a human message outranks an assessment. A worker may hand a trigger
back to the queue exactly once, and when a ticket ends its unread events return to evaluation
instead of being dropped.

The beat is fixed (`AGENTHUB_ROUTER_INTERVAL`, default `2s`) and single flight: a request that
arrives mid-beat is merged into the one that starts right after it. Tickets beyond
`AGENTHUB_ROUTER_MAX_TICKETS` (default `8`) stay queued for a later beat instead of opening a
goroutine, and a failed run retries with back-off up to `AGENTHUB_ROUTER_MAX_ATTEMPTS` (default `3`).

The scheduler is **off by default** — `AGENTHUB_ROUTER_ENABLED=true` turns it on, because an
instance that has not decided which worker a trigger should run on must not start opening tasks by
itself. While it is off, `/api/router/*` and `/api/schedules/{id}/run` answer
`503 feature_unavailable`, and thread messages keep the direct `Hub.Send` path they had before.
`AGENTHUB_ROUTER_RESOURCE` names the fallback worker; cron expressions reuse the five-field parser
the checkpoint scheduler already ships, so "next run" is computed on save and again after every
fire.

### 🆚 Compared with the dsh superagent router

| Capability | agenthub |
| --- | --- |
| Unified intake with an idempotency key | yes — four sources, dedupe on `(source, dedupe_key)`, a redelivery answers `200 {duplicate:true}` |
| Fast path rules (source mode, allow list, `@mention`, keywords, quiet hours) | yes — `internal/router/filter.go`, offline and deterministic |
| Semantic layer as one batched call with a closed verdict vocabulary | yes — `accept / attach / stop / ignore`, with a per-trigger fallback to the rule verdict, never a pass-through |
| Thread subscriptions with direct pass-through | yes — human messages outrank an assessment |
| One hand-back per trigger | yes — a second attempt answers `409 router_handback_exhausted` |
| Unread events return to evaluation when a ticket closes | yes |
| Fixed beat, single flight, request merging | yes — `2s`, one beat at a time, a mid-beat request merges into the next |
| Worker isolation, concurrency slots, back-off | yes — `hub.Send`/`hub.Resume` under a slot semaphore, retries up to `AGENTHUB_ROUTER_MAX_ATTEMPTS` |
| Durable effects queue / transactional outbox | **not built, by choice** — a committed intent is driven synchronously after the transaction, and single writer + idempotent replay cover the failure window |
| Persona voice, reply composition, long-term memory | **not in scope** — a worker answers through the ordinary task and thread surface |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧱 Execution sandboxes

The platform does not execute anything as the host process. `internal/sandbox` is a control plane:
it leases an environment per `(owner, resource, scope)`, runs work inside it, audits every run and
reclaims it when it goes idle. `hub`, `router` and the adapters never learn what a container is —
they ask `sandbox.Service` for an execution, and only the `Manager` knows where it happens.

The shape is the scheduler's, deliberately: consumer-defined ports (`store.go`), one writer
(`Manager`), a single-flight beat (`Sweeper`), a replayable event bus, and the same
"memory or PostgreSQL" pair behind one interface.

| | |
| --- | --- |
| Default backend | `docker` (`AGENTHUB_SANDBOX_PROVIDER`) |
| Refusal | a missing runtime answers `sandbox_unsupported`, never a quiet fallback to the host |
| Layers | container · user-space kernel (gVisor) · microVM (Kata/Firecracker) · process (bubblewrap / nsjail / isolate) · platform (E2B / Daytona / CubeSandbox / OpenHands) · remote node · off |
| Lease key | `(owner, resource, scope)`; the scope resolves `metadata.ticket` → `metadata.contextId` → `default`, so one ticket's steps share one environment |
| Reuse | renewed on every hit, reclaimed after `AGENTHUB_SANDBOX_IDLE_TTL`; anonymous callers never reuse |
| Ceiling | `AGENTHUB_SANDBOX_MAX_PER_OWNER` / `_MAX_TOTAL`, cross-checked against `AGENTHUB_ROUTER_MAX_TICKETS`; a refused lease is a **retryable** `sandbox_limit_reached`, consumed by the scheduler's back-off |
| Audit | command digest, exit code, duration and the output's sha256 — never the output itself |
| API | `/api/sandboxes`, `/api/sandboxes/{id}`, `/api/sandboxes/stats`, `/api/sandbox-providers`, `/api/sandbox-nodes`, `/api/sandboxes/events` (SSE with `since`) |
| Console | the interactive sandbox screen lives in **agentbuddy** (`ComputersView`); this repository ships the API and the `/docs` table only |
| Network | `none` / `egress_allowlist` cut the network on **both** backends, `bridge` / `platform` grant it; the allowlist itself still needs an egress sidecar, so it is read as `none` (fail-closed) |

What a backend actually honours is declared per backend and checked **before** a lease is handed out:
bubblewrap has no NAT interface, so asking it for `bridge` answers `sandbox_capability_unavailable`
instead of quietly receiving the host's network namespace, and remote nodes and hosted shims declare
`delegated` because this process cannot constrain them — the node does, and its own audit records it.

A backend is a catalogue entry, not a code path. Three ways to add one without touching the control
plane:

1. **Compile-time** — implement `sandbox.Provider` and register it (`docker`, `podman`, `runsc`,
   `bwrap`, `mock`, `remote`).
2. **Declarative** — hand the operator a template through `AGENTHUB_SANDBOX_PROVIDERS`. Argv is
   expanded element by element, never through a shell; an unknown placeholder, an empty argv or a
   relative binary refuses to load at startup.
3. **Node protocol** — enroll a machine and it pulls jobs over a long poll.

`off` refuses everything; `mock` is deterministic and offline, and stays an explicit choice so an
offline demo can never become the default posture.

### 🔍 What runs inside a sandbox

Four execution surfaces go through the same `Manager`:

- **CLI** — the command is the one registered on the resource; arguments are checked against
  `args_policy` (`none | prefix | passthrough`), and a prompt can at most become standard input.
  The "first word of the prompt is the command" path is gone.
- **MCP stdio** — the server starts inside the lease, so the tool allow list is enforced where the
  process actually runs.
- **Computers** — `shell / browser / screen` sessions reference a real lease and can be probed once
  from the console (`dry_run` in the audit).
- **Remote agents** — egress goes through the server; credentials are injected from
  `credential_env` and never enter the model context or an artifact.

### 🐋 Default is Docker, and a missing Docker is an answer

`AGENTHUB_SANDBOX_PROVIDER` defaults to `docker`. When the socket is unreachable — no daemon, no
`/var/run/docker.sock` inside the container, a socket proxy that is not running — the catalogue
reports `available: false` with the reason, `/healthz` says so, and execution is refused. The
console shows the same sentence. An instance that cannot isolate does not execute on the host
instead.

Deploying inside a container: mount nothing by default, or point `AGENTHUB_SANDBOX_DOCKER_HOST` at
a socket-proxy sidecar with only `CONTAINERS`, `IMAGES` and `POST` enabled. Mounting
`/var/run/docker.sock` directly is equivalent to host root; `docker-compose.yml` ships the proxy as
an optional profile for exactly that reason.

### 🆚 Compared with the hosted runtimes

Full comparison, licences included: [`docs/sandbox-landscape.md`](docs/sandbox-landscape.md).

| | agenthub |
| --- | --- |
| Boundary | container by default, upgradable per resource to a user-space kernel, a microVM or a node you enrolled |
| Vendor SDK | none — a hosted runtime is a declarative template |
| Credentials for remote agents | injected server-side; the model only ever sees the variable name |
| Lifetime tied to the task? | no — a cancelled task keeps its environment, and a vanished environment rebuilds on the next call |
| Audit | digests, exit codes and durations in `sandbox_execs` |
| Offline mode | explicit `mock`, never a hidden fallback |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🚨 Error contract

The backend returns **machine readable codes only**, never prose; the console maps each code to
localised copy on the client.

- HTTP: `{"code":"..."}` (`bad_request` / `unauthorized` / `not_found` / …)
- JSON-RPC: standard codes plus the A2A specific `-32001 task_not_found`,
  `-32002 task_not_cancelable`, `-32003 push_notification_not_supported`,
  `-32004 unsupported_operation`, `-32005 invalid_state_transition`,
  `-32006 resource_not_found`, `-32007 resource_unreachable`, `-32008 task_not_resumable`

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔑 API keys

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

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🗄 Persistence

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
| `audit_log` | The append-only ledger (see **API audit trail**). Also **excluded** from every dump and restore: `025` refuses `TRUNCATE` for every role, and rewinding the trail is exactly what its hash chain exists to prevent |
| `api_keys` / `api_key_scopes` | Long-lived bearer credentials; only the `sha256` of the token (`token_hash`) and a display `prefix` are stored (never the secret), with an optional `expires_at` and a soft `revoked_at`; `api_key_scopes` is the key → permission edge |

- Migrations: `migrations/*.sql` embedded with `//go:embed`, executed in filename order, one
  transaction per file; the SQL itself is idempotent. Each file (and the `schema_migrations`
  bootstrap) is taken under a `pg_advisory_xact_lock` and re-checks the ledger inside it, so
  several instances starting at once — or two `go test` packages sharing one database — serialise
  instead of colliding in the system catalogs.
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

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 💾 Database checkpoints

The console's **Backups** tab (`/admin`, `backups:manage`) takes a complete dump of the agenthub
schema into one `tar.gz` on disk and can rewind the database to any of them.

- **Archive**: `manifest.json` (tables, row counts, engine, the migration set it was taken at) plus
  the payload, in one file. The file's sha256 is stored on the catalogue row and re-checked before a
  download and before a restore, so an archive that rotted on its volume is refused rather than
  handed over.
- **Engines**: `pg_dump -Fc` when the PostgreSQL client is installed in the runtime image, otherwise
  a pure-Go engine that streams `COPY … TO STDOUT` / `COPY … FROM STDIN`. The engine travels with the
  archive; an archive can only be loaded by the engine that wrote it.
- **What a checkpoint carries**: application tables only. The catalogue (`backup_checkpoints` /
  `backup_schedules`) and the append-only ledger (`audit_log`) are excluded, so a rewind neither
  erases the archive's own record nor rewinds evidence — the hash chain stays continuous across a
  restore. `pg_dump` archives are therefore built from a positive `--table` list, not by exclusion:
  excluding a table by name would still leave its sequence and its trigger functions in the archive,
  and `pg_restore --clean` would then try to drop them out from under the table that still uses them.
- **Guarded tables**: the pure-Go engine empties the schema with one `TRUNCATE`, so it refuses — at
  dump *and* at restore, by table name, with `backup_table_guarded` — when a table it would carry has
  a `BEFORE TRUNCATE` trigger. A checkpoint that could not be loaded back is worse than none, and the
  failure should surface while a `pg_dump`-capable image can still be deployed, not on the day the
  archive is needed. Only `BEFORE TRUNCATE` counts: `TRUNCATE` fires no row trigger.
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

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📁 Shared files

The workbench can upload a document, a screenshot or a recording and hand it to an agent from a
thread. The bytes live outside the database; only their metadata does.

- **Switch**: `AGENTHUB_FILES_STORE=off|local|s3` (default `off`). With it off, every `/api/files*`
  route answers `503 files_disabled` and the screen says so instead of showing an empty list.
- **Endpoints**: `GET /api/files` (list + `maxBytes` + backend), `POST /api/files` (multipart, first
  file part), `GET /api/files/{id}`, `GET /api/files/{id}/content` (attachment download, `Range`
  supported, so a large upload can be resumed), `DELETE /api/files/{id}`.
  Permissions reuse `tasks:read` / `tasks:write`; no new permission code.
- **Limits**: `AGENTHUB_FILES_MAX_BYTES` (default 32 MiB) is enforced while the bytes stream to a
  temporary file, so an oversized upload never reaches the store. Every row carries the sha256 and
  the sniffed MIME type; the claimed type is kept only when sniffing cannot do better.
- **Local backend**: `AGENTHUB_FILES_DIR`. Written to a temporary file, fsynced by rename into a
  shard directory (`<first two hex>/<digest>`), mode `0600`.
- **S3-compatible backend** (RustFS, MinIO, AWS): `AGENTHUB_FILES_S3_ENDPOINT`, `..._BUCKET`,
  `..._REGION`, `..._PATH_STYLE` (must stay `true` for RustFS/MinIO), and the *names* of the
  environment variables holding the credentials (`..._ACCESS_KEY_ENV`, `..._SECRET_KEY_ENV`).
  The client is written against the standard library (SigV4), so the module adds no dependency.
  Selecting `s3` without an endpoint or a bucket fails `config.Validate()` at startup.
- **Ownership**: rows carry `owner_id` and the table is under `FORCE` row level security, so a
  second account cannot list, read or delete somebody else's file even by guessing the id.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🏛 AWS Agent Registry comparison

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

### 🧾 Record types and validation

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

### ♻️ Lifecycle and governance

| AWS Agent Registry | agenthub |
| --- | --- |
| Draft → submit → curator **approve / reject** (with feedback) → **deprecate** | `draft → pending → approved / rejected → deprecated`; an illegal transition is a 400 |
| Consumers only ever see approved records | `registry.Discoverable` is the single filter the listing, the MCP export, the Agent Card and A2A addressing share |
| Four roles: Administrator / Publisher / Curator / Consumer | RBAC (owner / groups / roles / permissions) plus resource grants, with `registries:manage`, `registry:curate` and `sources:manage` for the catalogue |
| Namespaces split by type, environment or team | Namespaces, each with its own approval policy and visibility boundary |

### 🔎 Discovery

| AWS Agent Registry | agenthub |
| --- | --- |
| **Hybrid search**: semantic + keyword | Keyword search only — see *deliberately not* below |
| **Catalogue browsing**: paginated list, filter by recordType, `Get` / `BatchGet` | `GET /api/resources?kind=&q=&namespace=&status=`; no pagination and no batch read yet |
| **Native MCP endpoint** (`InvokeRegistryMcp`) any MCP client can call | `GET /api/export/mcp` **exports** MCP server descriptions; agenthub does not run an MCP server |
| Agent Card | `GET /api/export/agent-card` and `/.well-known/agent.json`, one advertised skill per approved record |

### 🔐 Authorization and synchronisation

| AWS Agent Registry | agenthub |
| --- | --- |
| Inbound: IAM or JWT | Session cookie → `Principal`; `/api/*` behind `requirePermission`, A2A behind `identify` |
| Outbound: a **credential provider** (OAuth credential or IAM role) for reaching remote endpoints | `AuthScheme` (none / bearer) plus `auth_token_env`, which stores the environment variable **name** only |
| **URL-based synchronisation**, keeping records in step with remote MCP / A2A endpoints | A source (mcp or skill) pulls on demand or on an interval, merges on `(name, version)` and records the outcome; the fixture client keeps it offline until an operator opts out |

### 📊 Operations and observability

| AWS Agent Registry | agenthub |
| --- | --- |
| EventBridge notifications (a record submitted for approval) | SSE broker plus the `events` table, replayable with `since=<seq>` — equivalent in kind, agenthub's own implementation |
| CloudTrail auditing of every API call | `slog` structured logs (no secrets, no user content, no vectors); a weaker audit surface, deliberately left alone |
| Organizations, cross-account RAM sharing | One instance, multi-tenant by principal and resource grants; AWS-only constructs that are not replicated |
| VPC / PrivateLink, service-linked role | Not applicable to a self-hosted binary |
| Key-value tags (cost attribution, access control) | `tags []string` plus a `metadata` map — enough for the use |
| — | Database checkpoints with scheduled dumps and in-place restore (`internal/backup`): no AWS counterpart, agenthub is ahead here |

### 📋 What agenthub had, what it added, what it refuses

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

### 🔁 recordType ↔ Kind

| AWS recordType | Validated against | agenthub kind | Per-kind configuration |
| --- | --- | --- | --- |
| `MCP` | MCP protocol schema | `mcp` | transport, command+args or URL, tool allowlist, auth schemes, env |
| `AGENT` | A2A Agent Card schema | `agent` | card URL, protocol version, capabilities, skills, auth |
| `SKILL` | descriptor (name / description / package / repository / markdown) | `skill` | version, repository / package, triggers, steps, tool list, markdown |
| `CUSTOM` | any valid JSON | `custom` | JSON schema |
| — | — | `model` | provider mode, endpoint, model id, api key env, fallback chain with weights, timeout |
| — | — | `cli` | command, allowlist, timeout, output truncation, working directory, env |

### 🧭 Remaining gaps

1. **Semantic retrieval** (medium): keyword search recalls poorly once a catalogue runs to thousands
   of records; the fix would be an external search source rather than an embedded model.
2. **Batch read and pagination** (medium): the listing has neither yet, which will slow the console
   down as the catalogue grows.
3. **Audit surface** (low): no record of every API call, which compliance scenarios ask for.
4. **Schedule expressiveness** (low): a sync schedule is a hand-rolled five-field cron, less
   expressive than EventBridge rules but enough for a self-hosted instance.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📝 License

MIT.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🤝 Engineering conventions

This repository follows Harness Engineering:

- [`AGENTS.md`](AGENTS.md): entry point and directory index for agent engineering context
- [`requirements.md`](requirements.md): requirement spec (scope / errors / acceptance, reviewed by
  a human before coding)
- [`task.md`](task.md): implementation checklist

<div align="right">

[![][back-to-top]](#readme-top)

</div>

---

<div align="center">

Copyright © 2026 [ariacompute][org]. <br/>
This project is [MIT](./LICENSE) licensed.

</div>

<!-- LINK GROUP -->

[org]: https://github.com/ariacompute
[releases]: https://github.com/ariacompute/agenthub/releases
[issues]: https://github.com/ariacompute/agenthub/issues
[ci]: https://github.com/ariacompute/agenthub/actions/workflows/ci.yml
[stars]: https://github.com/ariacompute/agenthub/stargazers
[forks]: https://github.com/ariacompute/agenthub/network/members
[license]: ./LICENSE
[docs]: ./docs/docs.md
[go]: https://go.dev
[postgres]: https://www.postgresql.org
[vue]: https://vuejs.org
[badge-go]: https://img.shields.io/badge/Go-1.27-151515?labelColor=black&style=flat-square&logo=go
[badge-postgres]: https://img.shields.io/badge/PostgreSQL-persistence-151515?labelColor=black&style=flat-square&logo=postgresql
[badge-vue]: https://img.shields.io/badge/Vue-3-151515?labelColor=black&style=flat-square&logo=vuedotjs
[badge-release]: https://img.shields.io/github/v/release/ariacompute/agenthub?labelColor=black&style=flat-square
[badge-ci]: https://img.shields.io/github/actions/workflow/status/ariacompute/agenthub/ci.yml?labelColor=black&style=flat-square
[badge-license]: https://img.shields.io/github/license/ariacompute/agenthub?labelColor=black&style=flat-square
[badge-stars]: https://img.shields.io/github/stars/ariacompute/agenthub?labelColor=black&style=flat-square
[badge-forks]: https://img.shields.io/github/forks/ariacompute/agenthub?labelColor=black&style=flat-square
[back-to-top]: https://img.shields.io/badge/-BACK_TO_TOP-151515?style=flat-square
