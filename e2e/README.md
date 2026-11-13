# agenthub E2E suites

Two independent, user-facing suites for an **already running** agenthub. They
only test: nothing is started, stopped or migrated. Everything they create is
removed again, and every case prints `PASS`, `FAIL` or `SKIP` with a reason.

- **API** (`api/`): a declarative case table covering every route documented in
  `docs/docs.md`. Normal and error paths for discovery, identity, resources,
  governance, A2A (including HITL park → resume / cancel), streaming and
  `since` replay, tasks, RBAC, channels/connectors, threads/speech, files,
  sandboxes, schedules/computers/knowledge, router, audit and checkpoints.
- **UI** (`ui/`): six Playwright journeys through the real console — login,
  register a resource, playground trial run, task trace, governance approvals
  and administration.

Both share `lib/` (config, HTTP client, identity, assertions, runner, cleanup,
report, coverage guard) and the same login state. They are isolated from the Go
unit tests and from `web/`'s Vitest baseline.

## Prerequisites

| requirement | detail |
| --- | --- |
| running instance | `BASE_URL` must answer `/healthz`. Recommended: `AGENTHUB_SEED_DEMO=true make up` or `make run` |
| bun | >= 1.4.4. No `sh`, `curl` or `python3` is needed, so Windows works too |
| identity | an **owner-equivalent** account or an API key with all scopes (the run writes to every domain) |
| first run | `cd e2e && bun install` — installs only `@playwright/test`, isolated in this directory |
| UI only | `cd e2e && bun x playwright install chromium` |

`AGENTHUB_SEED_DEMO=true` is recommended but no longer required: the fixtures
prefer `demo-*` resources and otherwise create their own equivalents.

## Run

From the repository root:

```bash
make e2e-api        # API suite
make e2e-ui         # UI journeys
make e2e            # both
make e2e-coverage   # static endpoint coverage guard, sends no requests
```

Or from this directory:

```bash
bun run api                     # API suite
bun run ui                      # UI journeys
bun run all                     # both
bun run api/index.js --coverage-only
```

`./scripts/test-api.sh` is kept as a thin compatibility shim that forwards here.

## Identity and environment

| variable | default | purpose |
| --- | --- | --- |
| `BASE_URL` | `http://127.0.0.1:$AGENTHUB_HTTP_PORT` (port 3500) | target instance |
| `AGENTHUB_HTTP_PORT` | `3500` | used when `BASE_URL` is unset |
| `AGENTHUB_TEST_EMAIL` | `admin@example.com` | login |
| `AGENTHUB_TEST_PASSWORD` | `agenthub-admin-pass` | login |
| `AGENTHUB_SESSION_TOKEN` | — | pass an existing session straight through (no login) |
| `AGENTHUB_API_KEY` | — | bearer `ahk_…` instead of a session (no login) |
| `MAX_TIME` | `20` | per-request timeout, seconds |
| `READY_TIMEOUT` | `30` | `/healthz` readiness wait, seconds |
| `AGENTHUB_E2E_LANG` | `en` | fixed console language for UI selectors |
| `AGENTHUB_E2E_STRICT_COVERAGE` | `1` | fail the run when a documented route has no case |

If login fails, the suite tries self-registration. On an instance that already
has users this only yields `role-viewer` **and leaves that account behind**, so
always supply the correct bootstrap credentials explicitly instead of relying
on the fallback. A preflight check stops the run with **exit code 2** and one
actionable message instead of dozens of 403 failures. Exit codes: `0` all
green, `1` at least one FAIL (or coverage guard failed / cleanup warning), `2`
preflight failed (service unreachable or read-only identity).

With `AGENTHUB_API_KEY` or `AGENTHUB_SESSION_TOKEN` set, the auth cases still
exercise `POST /api/auth/login`, but the client deliberately does **not** absorb
that response's `Set-Cookie`: only a login-driven run owns a session cookie.
Absorbing it would silently replace the supplied, owner-equivalent credential
with a viewer session for every later request and turn all writes into 403.

## Opt-in switches

Anything that needs a third party, rewinds the database, dials an external URL
or executes real code is **SKIP by default**. Set the switch to `1`/`true` to
run it; the case prepares its own data and cleans up in `finally`.

| switch | what it enables | server-side preparation |
| --- | --- | --- |
| `AGENTHUB_E2E_SESSION_LOGOUT` | a real logout, then a fresh login to restore the session | none |
| `AGENTHUB_E2E_OAUTH` | a real OAuth start redirect | a configured OAuth provider |
| `AGENTHUB_E2E_BACKUP_RESTORE` / `AGENTHUB_TEST_BACKUP_RESTORE` | a real checkpoint restore | backups enabled + a backup directory |
| `AGENTHUB_E2E_S3` | asserts files are served from S3 | S3 credentials |
| `AGENTHUB_E2E_WEBHOOK` | a signed channel webhook | `AGENTHUB_E2E_SIGNING_KEY` set in the **server** environment to the same value as here |
| `AGENTHUB_E2E_DELIVERY` | a real outbound channel delivery | a reachable `target_url` |
| `AGENTHUB_E2E_PUSH_CALLBACK` | registers a real push callback | an HTTPS receiver in `AGENTHUB_E2E_PUSH_URL` |
| `AGENTHUB_E2E_SANDBOX_EXEC` | runs a real command in a live sandbox lease | a container runtime |

## SKIP semantics

A case is SKIPped, not failed, when a module is not wired up on the target
instance (`feature_unavailable`, `files_disabled`, `speech_disabled`,
`channel_disabled`, `skill_distill_disabled`, `backups_disabled`,
sandbox/router disabled), when a prerequisite object was not produced by an
earlier case (every dependent case reads it through `requireState()`), when demo
resources are absent, when an SSE stream stays quiet until its read budget
expires, or when its opt-in switch is off. Each SKIP line carries the reason.
This follows `requirements.md` §8: a missing capability is reported, never
faked.

## Cleanup

Every created object carries a timestamped `e2e-*` id and is pushed onto a LIFO
cleanup stack. The runner drains it in a `finally` block (also on failure or
preflight abort); deletes are idempotent, so a `404` counts as already clean.
UI journeys delete their objects through the same session in `finally`.
Cleanup warnings appear in the report and make the run exit non-zero.

## Reports

- `e2e/reports/api.json` and `e2e/reports/api.md` — per-group PASS/FAIL/SKIP,
  skip reasons, timings, summary, switch states and cleanup warnings.
- `e2e/playwright-report/` — HTML report for the UI journeys.

All output passes through a redactor: passwords, session cookies, bearer tokens,
API keys, signing secrets and PII are replaced, and failure bodies are
truncated to 512 bytes. The console prints a single `N passed, N failed, N
skipped` summary line.

## Endpoint coverage guard

`lib/coverage.js` parses the generated endpoint tables under `docs/` read-only
and diffs them against the `route` field of every declared case. A newly
documented endpoint without a case — or a case pointing at an undocumented
route — fails the run. `make e2e-coverage` runs only this guard, so it works
without a service. It mirrors the Go-side `tests/docs_endpoints_test.go`
(handlers ↔ docs) without modifying either.

## Adding a case

Cases are plain objects; add one to the relevant file in `api/cases/` (or create
a new file and add it to `api/cases/index.js`):

```js
const c = (id, route, run, extra = {}) => ({ id, group: 'my group', route, ...extra, run })

export default [
  c('my endpoint answers 200', 'GET /api/example/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/example/1')
    expectAvailable(res, 'example')
    expectStatus(res, 200, 'example')
  }, { enabled: (ctx) => ctx.config.optin.mySwitch ? true : 'needs my switch' }),
]
```

`ctx` exposes `api` / `anon` / `key` / `cookie` / `rpc` / `stream` request
helpers, shared `state`, `cleanup(label, fn)`, `config` and `reporter`. Use
`route: null` for cases that do not map to a documented endpoint (for example
the console bundle). Cases within a group run in declared order and share
state; a failing case does not abort the rest.

## Layout

```
e2e/
  e2e.config.js          environment, credentials, switches, redacted view
  playwright.config.js   isolated Playwright config (only ui/journeys/*.spec.js)
  lib/                   client, auth, assertions, runner, cleanup, report, coverage
  api/index.js           suite entry: guard -> ready -> identity -> cases -> cleanup -> report
  api/fixtures.js        payload builders, demo/self-created resources, FormData helper
  api/cases/*.js         declarative case table (00 .. 99, ordered by dependency)
  ui/helpers.js          real DOM selectors, language freeze, confirm handling
  ui/global-setup.js     one shared login -> .auth/state.json
  ui/journeys/*.spec.js  six Playwright journeys
```

## Troubleshooting

| symptom | cause / fix |
| --- | --- |
| exit 2, "service never answered" | start the instance; check `BASE_URL` and `READY_TIMEOUT` |
| exit 2, "read-only" | use the bootstrap administrator or an owner-scope API key; self-registration only grants `role-viewer` on a populated instance |
| many 401s | the session was invalidated; provide `AGENTHUB_API_KEY` instead |
| resource writes answer `404 not_found` | an all-scopes API key is not the owner: `resource_grants` are checked after scopes. The preflight now proves a created record can be written back and exits 2 with a hint; check `resource_grants` for the creator or use the bootstrap owner session |
| `create a computer card` times out | the sandbox default image is not local yet; `docker pull debian:bookworm-slim`, or set `AGENTHUB_SANDBOX_ENABLED=false` (the sandbox group then SKIPs) |
| `the audit chain is broken at <first event id>` | older builds hashed nanosecond timestamps and could not verify a trail whose oldest rows had been pruned. Upgrade and restart: verify now seeds from the anchor recorded by retention, or from the head's own `prev_hash`. No data reset is needed |
| many SKIPs | optional modules are off; that is expected unless you need those features |
| coverage guard fails | a new route was added to `docs/docs.md`; declare a case for it |
| Windows | use `bun run api` / `bun x playwright test`; `scripts/test-api.sh` requires a POSIX shell |
