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
helm install agenthub-operator ./deploy/agenthub-operator --namespace agenthub-operator --create-namespace

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

## Local cluster walkthrough (kind / k3d)

This section runs the whole stack on a local cluster — including PostgreSQL
**inside** the cluster. The operator does not manage a database, but "external"
only means "not reconciled by the operator"; a Postgres workload in the same
namespace works fine and is reachable via in-cluster DNS.

### 1. Create the cluster

```bash
# kind
kind create cluster --name agenthub
# or k3d
k3d cluster create agenthub --wait
```

### 2. Build and load the images

The operator and agenthub images must be present in the cluster. Build them on
the local Docker daemon and import them:

```bash
# from the agenthub repo root
docker build -f Dockerfile -t agenthub:dev .
# from operator/
make -C operator docker-build IMG=agenthub-operator:dev

# kind: import from the local daemon
kind load docker-image agenthub:dev agenthub-operator:dev --name agenthub
# k3d: import from the local daemon
k3d image import agenthub:dev agenthub-operator:dev -n agenthub
```

If you push the images to a registry instead, set `image.repository`/`tag` in
the chart `values.yaml` (or the `AgentHub` spec) accordingly.

### 3. Start PostgreSQL inside the cluster

Apply a simple Postgres Deployment + Service in the operator namespace:

```yaml
# postgres.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: agenthub-operator
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: agenthub-operator
spec:
  replicas: 1
  selector:
    matchLabels: { app: postgres }
  template:
    metadata:
      labels: { app: postgres }
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - { name: POSTGRES_USER, value: agenthub }
            - { name: POSTGRES_PASSWORD, value: agenthub }
            - { name: POSTGRES_DB, value: agenthub }
          ports:
            - { containerPort: 5432 }
          readinessProbe:
            exec:
              command: [ pg_isready, -U, agenthub ]
            initialDelaySeconds: 5
            periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: agenthub-operator
spec:
  selector: { app: postgres }
  ports:
    - { port: 5432, targetPort: 5432 }
```

```bash
kubectl apply -f postgres.yaml
kubectl -n agenthub-operator wait --for=condition=Ready pod -l app=postgres --timeout=120s
```

The in-cluster DSN is then
`postgres://agenthub:agenthub@postgres.agenthub-operator.svc.cluster.local:5432/agenthub`
(service DNS `<name>.<namespace>.svc.cluster.local`). Match the host/user/db/password
to whatever you set in the manifest.

> For a more production-like Postgres use the Bitnami chart —
> `helm install pg oci://registry-1.docker.io/bitnamicharts/postgresql -n agenthub-operator`
> — and read its generated password from the released Secret instead of
> hard-coding it.

### 4. Install the operator and create the `AgentHub`

```bash
helm install agenthub-operator ./operator/deploy/agenthub-operator \
  --namespace agenthub-operator --create-namespace

kubectl -n agenthub-operator apply -f - <<'EOF'
apiVersion: agenthub.ai/v1alpha1
kind: AgentHub
metadata:
  name: agenthub
  namespace: agenthub-operator
spec:
  image:
    repository: agenthub
    tag: dev
    pullPolicy: IfNotPresent
  replicas: 1                 # keep 1 until the schema is migrated
  env: dev
  httpPort: 3500
  database:
    url: postgres://agenthub:agenthub@postgres.agenthub-operator.svc.cluster.local:5432/agenthub
  bootstrapAdmin:
    email: admin@example.com
    password: change-me-please
EOF
```

If you would rather not put the DSN inline, pre-create a Secret and reference
it instead of `spec.database.url`:

```bash
kubectl -n agenthub-operator create secret generic agenthub-db \
  --from-literal=DATABASE_URL='postgres://agenthub:agenthub@postgres.agenthub-operator.svc.cluster.local:5432/agenthub'
# then set spec.database.existingSecret: agenthub-db (optionally
# spec.database.secretKey: DATABASE_URL) and omit spec.database.url.
```

### 5. Verify

```bash
kubectl -n agenthub-operator get agenthub
kubectl -n agenthub-operator get pods,svc,deploy
kubectl -n agenthub-operator port-forward svc/agenthub 8080:3500 &
curl -s localhost:8080/healthz
```

`status.conditions[Available]=True` with `readyReplicas: 1` means the operator
reconciled successfully. Once the database is migrated you may scale up with
`kubectl -n agenthub-operator scale agenthub agenthub --replicas=3` (or edit the
CR's `spec.replicas`) — see [Multi-replica migration race](#multi-replica-migration-race).

### Cleanup

```bash
kind delete cluster --name agenthub   # or: k3d cluster delete agenthub
```

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
