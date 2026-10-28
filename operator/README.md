# agenthub-operator

A Kubernetes **operator** (controller-runtime) that deploys and manages
[agenthub](https://github.com/agenthub/agenthub) from a declarative custom
resource. It follows the same shape as
[rustfs/operator](https://github.com/rustfs/operator): the chart installs the
operator controller **and** the `AgentHub` CRD, then you create an `AgentHub`
custom resource and the controller reconciles it into a running
agenthub-server Deployment/Service/ConfigMap/Secret/ServiceAccount/Role set.

agenthub itself is unchanged — its `/healthz` probe and `AGENTHUB_*`
environment-driven configuration (see `internal/config/config.go`) are exactly
what the operator feeds it.

## Architecture

```
helm install  ──►  operator Deployment (+ CRD via crds/)
kubectl apply  ──►  AgentHub CR  ──►  controller reconciles  ──►
   Deployment / Service / ConfigMap / Secret / SA / Role  ──►  agenthub-server
```

The operator is a **real controller**: it owns the child resources (deleting
an `AgentHub` cascades the workload), and it patches `status` with
`ObservedGeneration`, `ReadyReplicas` and `Available`/`Progressing`
conditions.

## Prerequisites

- Kubernetes **≥ 1.25** and a `kubectl` / `helm` ≥ 3.
- An **external PostgreSQL** (the operator never provisions a database).
- Two container images:
  - `agenthub-operator` — built from `operator/Dockerfile`.
  - `agenthub` — the application image (your existing build, e.g. `docker
    build -f Dockerfile -t agenthub .`).

## Build & install

```bash
# 1. Build the operator image (static, distroless).
cd operator
make docker-build IMG=agenthub-operator:latest
# push it to your registry, or load it into kind/k3d.

# 2. Install the operator + CRD into a dedicated namespace.
helm install agenthub-operator ./deploy/agenthub-operator \
  --namespace agenthub-operator --create-namespace

# 3. Create an AgentHub CR (the operator only watches its own namespace).
kubectl apply -f examples/agenthub-sample.yaml -n agenthub-operator

# 4. Watch it come up.
kubectl -n agenthub-operator get agenthub
kubectl -n agenthub-operator get pods,svc,deploy
```

## The `AgentHub` resource

Every field of `spec` maps to an `AGENTHUB_*` environment variable of the
server (defaults mirror `config.go`):

| spec field | env var | notes |
|---|---|---|
| `image` | — | agenthub container image |
| `replicas` | — | default **1** (see migration warning) |
| `env` | `AGENTHUB_ENV` | `dev` / `staging` / `prod` |
| `httpPort` | `AGENTHUB_HTTP_PORT` | default 3500 |
| `staticDir` | `AGENTHUB_STATIC_DIR` | default `/app/backend/static` |
| `bootstrapAdmin.email` | `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` | **prod required** |
| `bootstrapAdmin.password` | `AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD` | secret |
| `sessionTTL` | `AGENTHUB_SESSION_TTL` | e.g. `12h` |
| `cookieSecure` | `AGENTHUB_COOKIE_SECURE` | |
| `publicBaseURL` | `AGENTHUB_PUBLIC_BASE_URL` | |
| `database.url` | `DATABASE_URL` | **mandatory** |
| `database.existingSecret` | `DATABASE_URL` | preferred over `url` |
| `encryptionKey` | `AGENTHUB_ENCRYPTION_KEY` | **prod requires ≥32 bytes** |
| `pushSecret` | `AGENTHUB_PUSH_SECRET` | secret |
| `oauth.*` | `AGENTHUB_OAUTH_*` | client IDs / secrets |
| `seedDemo` | `AGENTHUB_SEED_DEMO` | |
| `maxParallel` | `AGENTHUB_MAX_PARALLEL` | |
| `cliTimeout` / `cliAllowlist` | `AGENTHUB_CLI_*` | |
| `maxOutputBytes` / `eventBuffer` | `AGENTHUB_*` | |
| `pushEnabled` / `pushRecordLimit` | `AGENTHUB_PUSH_*` | |
| `modelRouterMode` / `speechMode` | `AGENTHUB_*` | |
| `channelsEnabled` | `AGENTHUB_CHANNELS_ENABLED` | |
| `audioMaxBytes` | `AGENTHUB_AUDIO_MAX_BYTES` | |
| `registrySync.*` | `AGENTHUB_REGISTRY_SYNC_*` | |
| `backup.*` | `AGENTHUB_BACKUP_*` | |
| `persistence.enabled` | — | mounts a PVC at `/var/lib/agenthub/backups` and sets `AGENTHUB_BACKUP_DIR` |

Non-secret values land in a ConfigMap; inline secret values (database DSN,
bootstrap password, encryption/push keys, OAuth secrets) land in a
controller-managed Secret. Any field backed by `existingSecret` is injected
straight from your pre-created Secret via `secretKeyRef` and is **never**
copied into the managed Secret.

## Constraints you must honour

### External PostgreSQL is mandatory
agenthub refuses to start without `DATABASE_URL`. The controller validates this
*before* creating any workload: if neither `spec.database.url` nor
`spec.database.existingSecret` is set, reconciliation stops with
`status.conditions[Available]=False, reason=MissingDatabase`. Always provide a
real database — the operator does not deploy one.

### Production requires two things
With `spec.env: prod` (`config.go` enforces this):
- `spec.bootstrapAdmin.email` must be set, and
- `spec.encryptionKey` must be ≥ 32 bytes (or reference an `existingSecret`).

A prod CR missing either fails fast with `reason=MissingAdminEmail` or
`reason=WeakEncryptionKey` — the Pod is never created, so you get a clear
status condition instead of a silent CrashLoop.

### Multi-replica migration race
agenthub runs its embedded schema migration at startup. With `replicas > 1`
several Pods race on DDL. **Keep `replicas: 1`** for the initial schema
creation, then scale up once the database is migrated. (A dedicated migration
Job is out of scope here.)

### Namespace scoping
The operator is granted a **namespaced `Role`** (not a `ClusterRole`) limited
to its release namespace, and `WATCH_NAMESPACE` is set to that namespace.
Create your `AgentHub` CR **in the same namespace** as the operator. To watch
multiple namespaces, edit the chart to a `ClusterRole`/`ClusterRoleBinding`
and drop `WATCH_NAMESPACE`.

## Status & observability

```bash
kubectl -n agenthub-operator get agenthub agenthub -o yaml
```

`status.conditions` carries `Available` and `Progressing`;
`status.readyReplicas` reflects the Deployment. The operator itself exposes
`/healthz` and `/readyz` on `:8081` and Prometheus metrics on `:8080`.

## Layout

```
operator/
  api/v1alpha1/        AgentHub CRD Go types + generated deepcopy
  internal/controller/ AgentHubReconciler (tests included)
  cmd/agenthub-operator/main.go  manager entrypoint
  config/crd, config/rbac        generated CRD + operator RBAC
  deploy/agenthub-operator/      Helm chart (operator + CRD install)
  examples/agenthub-sample.yaml  sample AgentHub CR
  Dockerfile, Makefile           build + codegen
```

`operator/` is an **independent Go module** (`github.com/agenthub/operator`)
with its own `go.mod`; the parent agenthub module and its zero-extra-dependency
`go.mod` are untouched.

## Regenerate manifests

```bash
cd operator && make manifests   # requires ./bin/controller-gen (downloaded by go install)
```
