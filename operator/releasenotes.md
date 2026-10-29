# 🚀 AgentHub Operator Release Notes

> A declarative deployment controller for Kubernetes. You only submit an `AgentHub`
> custom resource (CR), and the operator brings up, reconciles, and garbage-collects
> (together with the CR) all the workloads and configuration agenthub needs to run.
> This document is a **standalone guide for the operator**, complementing the
> platform-wide `releasenotes.md`.

---

## ✨ Core Features

### 🧩 Declarative CRD: `AgentHub`
- API group `agenthub.ai/v1alpha1`, kind `AgentHub`.
- A single YAML describes "what kind of agenthub instance to run"; `kubectl apply` hands it to the controller.
- The operator writes reconcile results back to the CR's `status.conditions`, making deployment state self-evident.

### 📦 One-shot full-stack workloads
For each instance the operator reconciles these **managed objects**:
- ⚙️ `ConfigMap` (`<instance>-config`) — non-sensitive config
- 🔑 `Secret` (`<instance>-secrets`) — managed sensitive fields
- 👤 `ServiceAccount` — the app's own identity
- 🛡️ least-privilege `Role` + `RoleBinding` — only grants what the app needs
- 🚀 `Deployment` — the agenthub main process (with readiness/liveness probes)
- 🌐 `Service` (ClusterIP) — exposes `AGENTHUB_HTTP_PORT` (default 3500)
- 💾 `PersistentVolumeClaim` (optional) — backup volume mounted when persistence is enabled

### ♻️ Cascading Garbage Collection
- All the above child resources carry an **ownerReference** pointing to the `AgentHub` CR.
- Deleting the CR reclaims all children together — **no orphaned objects** left behind.
- The controller `watch`es every managed type, so any drift is reconciled back to the desired state.

### 🔐 Secrets: managed values vs. external Secret
Sensitive fields inlined in the CR (database URL, bootstrap admin password/email,
encryption key, push secret, OAuth client secrets) are **written into the
operator-managed `<instance>-secrets` by default**.
A field can instead set `ExistingSecret`:
- In that case the operator **no longer manages its value**; instead it renders a
  `SecretKeyRef` pointing to an external Secret you control — the deployment
  references "a Secret you own", and credentials never enter the operator's managed Secret.

### 🔄 Safe reconciliation: upsert + merge patch
- New objects are created via `upsert`; existing objects are updated with a **merge patch** (not a full `Update`).
- This way a concurrent `status` write from the app side does not trip the optimistic-concurrency conflict.
- Typed managed objects get their **GVK stamped back** before the client call, avoiding the controller-runtime failure where an empty `TypeMeta` prevents resource inference and API URL construction.

### 🛡️ Least-privilege RBAC
- The app-side `Role` + `RoleBinding` grant only what agenthub needs to run.
- The operator's **own** `Role` is also completed: besides `events.k8s.io`, it explicitly grants core `events` `create`/`patch` so the operator can record events normally (fixing the earlier `events is forbidden`).
- It reuses the existing `resources:read` / `tasks:read` / `tasks:write` permissions — **no new roles or user groups added**.

### 🩺 Health probes and self-healing
- The `Deployment` has built-in **readiness and liveness probes**, both hitting `/healthz`.
- Any drift of a managed object is reconciled back; Pod anomalies are self-healed by the Deployment controller via restart.

### 📈 Status and event observability
- The CR `status.conditions` records `Ready` / validation failures (e.g. `MissingDatabase`, `MissingAdminEmail`, `WeakEncryptionKey`) with reasons.
- The operator broadcasts key reconciliation milestones via Kubernetes `Event`s, viewable with `kubectl describe`.

### ⚙️ Rich configuration (CR fields mapped to `AGENTHUB_*` env vars)
| Category | Field / env var |
| --- | --- |
| Database | `spec.database.url` → `DATABASE_URL` (or `spec.database.existingSecret`) |
| Environment | `spec.env` → `AGENTHUB_ENV` (dev / staging / prod) |
| Image | `spec.image.repository` / `tag` / `pullPolicy` |
| Replicas | `spec.replicas` |
| Port | `spec.httpPort` → `AGENTHUB_HTTP_PORT` (default 3500) |
| Persistence | `spec.persistence.enabled` / `size` (default 10Gi) |
| Resources | `spec.resources` (container requests/limits) |
| Bootstrap admin | `spec.bootstrapAdmin.email` / `name` → `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL/NAME` |
| Encryption key | `spec.encryptionKey.value` (≥32 bytes) or `existingSecret` |
| Backup | `AGENTHUB_BACKUP_DIR` / `MODE` / `INTERVAL` / `CRON` / `KEEP_LAST` / `MAX_AGE_DAYS`, etc. |
| Other | session TTL, Cookie Secure, CLI timeout/allowlist, event buffer, push, registry sync, etc. |

### 🚢 One-command Helm install
```bash
helm install agenthub-operator operator/deploy/agenthub-operator \
  -n agenthub-operator --create-namespace
```
One command lays down the operator's `Deployment`, its own `Role`/`RoleBinding`/`ServiceAccount`, and the CRD.

---

## 🏁 Quick Start

```bash
# 1) Install the operator (with CRD)
helm install agenthub-operator operator/deploy/agenthub-operator \
  -n agenthub-operator --create-namespace

# 2) Start an in-cluster PostgreSQL (local smoke test only; use Bitnami/managed PG in prod)
kubectl apply -f operator/examples/postgres.yaml

# 3) Submit an AgentHub instance
kubectl apply -f operator/examples/agenthub-sample.yaml

# 4) Observe
kubectl -n agenthub-operator get pods,svc,agenthub
```

---

## 🗺️ Deployment Architecture

```
┌────────────────────────────────────────────────────────────────────────────────────────────┐
│  kubectl apply AgentHub CR  -->  AgentHub Operator                                         │
│                                 (reconcile + watch)                                        │
│        |                                                                                   │
│        +--> ConfigMap  <instance>-config  (non-sensitive config)                           │
│        +--> Secret  <instance>-secrets  (managed values / SecretKeyRef)                    │
│        +--> ServiceAccount + Role + RoleBinding  (least privilege)                         │
│        +--> Deployment  agenthub  (probes / runAsUser 10001)                               │
│        |       +--> Service  (ClusterIP :3500)                                             │
│        +--> PVC  (optional, backup persistence)                                            │
│                                                                                            │
│  External dependency: PostgreSQL (not provisioned by operator; must be supplied externally)│
└────────────────────────────────────────────────────────────────────────────────────────────┘
```

---

## ⚠️ Notes

- 🗄️ **External PostgreSQL is mandatory**: the operator **does not create the database**. The CR must provide `spec.database.url` or `spec.database.existingSecret`, otherwise validation fails (`MissingDatabase`) and the Deployment is not created.
- 🔒 **Production red lines**: with `spec.env: prod`, `spec.bootstrapAdmin.email` and `spec.encryptionKey` (≥32 bytes or `existingSecret`) are required, otherwise `MissingAdminEmail` / `WeakEncryptionKey`.
- 💾 **Example PG has no persistent volume**: `operator/examples/postgres.yaml` ships without a PVC; recreating the Pod **re-initializes an empty database** (user/db recreated from `POSTGRES_*` env vars). Use the Bitnami chart or your own PVC-backed managed PG in production.
- 🧹 **Delete cascades**: `kubectl delete agenthub <instance>` cascades and removes all child resources.

---

## 🔗 Related Documents
- Platform-wide release notes: `releasenotes.md` (includes the "13. Kubernetes Operator" section)
- Operator usage and local-cluster walkthrough: `operator/README.md`
