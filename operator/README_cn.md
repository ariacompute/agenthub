# agenthub-operator

一个 Kubernetes **operator**（基于 controller-runtime），通过声明式自定义资源部署并管理
[agenthub](https://github.com/agenthub/agenthub)。其形态与
[rustfs/operator](https://github.com/rustfs/operator) 一致：chart 安装 operator 控制器**以及**
`AgentHub` CRD，随后你创建一个 `AgentHub` 自定义资源，控制器将其调和（reconcile）为一个运行中
agenthub-server 的 Deployment / Service / ConfigMap / Secret / ServiceAccount / Role 集合。

agenthub 自身保持不变——它的 `/healthz` 探针与 `AGENTHUB_*` 环境变量驱动的配置
（见 `internal/config/config.go`）正是 operator 喂给它的内容。

## 架构

```
helm install  ──►  operator Deployment (+ CRD via crds/)
kubectl apply  ──►  AgentHub CR  ──►  controller reconciles  ──►
   Deployment / Service / ConfigMap / Secret / SA / Role  ──►  agenthub-server
```

operator 是一个**真正的控制器**：它拥有子资源（删除 `AgentHub` 会级联删除工作负载），并通过
`status` 的 `ObservedGeneration`、`ReadyReplicas` 以及 `Available` / `Progressing` 条件进行补丁更新。

## 前置条件

- Kubernetes **≥ 1.25**，以及 `kubectl` / `helm` ≥ 3。
- 一个**外部 PostgreSQL**（operator 从不创建数据库）。
- 两个容器镜像：
  - `agenthub-operator` —— 由 `operator/Dockerfile` 构建。
  - `agenthub` —— 应用镜像（你已有的构建，例如 `docker build -f Dockerfile -t agenthub .`）。

## 构建与安装

```bash
# 1. 构建 operator 镜像（静态、distroless）。
cd operator
make docker-build IMG=agenthub-operator:latest
# 推送到你的镜像仓库，或 load 进 kind/k3d。

# 2. 将 operator 与 CRD 安装到专用命名空间。
helm install agenthub-operator operator/deploy/agenthub-operator --namespace agenthub-operator --create-namespace

# 3. 创建一个 AgentHub CR（operator 只 watch 自己的命名空间）。
kubectl apply -f operator/examples/agenthub-sample.yaml -n agenthub-operator

# 4. 观察它启动。
kubectl -n agenthub-operator get agenthub
kubectl -n agenthub-operator get pods,svc,deploy
```

## `AgentHub` 资源

`spec` 的每个字段都映射到服务端的某个 `AGENTHUB_*` 环境变量（默认值与 `config.go` 一致）：

| spec 字段 | 环境变量 | 说明 |
|---|---|---|
| `image` | — | agenthub 容器镜像 |
| `replicas` | — | 默认 **1**（见迁移警告） |
| `env` | `AGENTHUB_ENV` | `dev` / `staging` / `prod` |
| `httpPort` | `AGENTHUB_HTTP_PORT` | 默认 3500 |
| `staticDir` | `AGENTHUB_STATIC_DIR` | 默认 `/app/backend/static` |
| `bootstrapAdmin.email` | `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` | **prod 必填** |
| `bootstrapAdmin.password` | `AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD` | 密钥 |
| `sessionTTL` | `AGENTHUB_SESSION_TTL` | 例如 `12h` |
| `cookieSecure` | `AGENTHUB_COOKIE_SECURE` | |
| `publicBaseURL` | `AGENTHUB_PUBLIC_BASE_URL` | |
| `database.url` | `DATABASE_URL` | **必填** |
| `database.existingSecret` | `DATABASE_URL` | 优先级高于 `url` |
| `encryptionKey` | `AGENTHUB_ENCRYPTION_KEY` | **prod 要求 ≥32 字节** |
| `pushSecret` | `AGENTHUB_PUSH_SECRET` | 密钥 |
| `oauth.*` | `AGENTHUB_OAUTH_*` | 客户端 ID / 密钥 |
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
| `persistence.enabled` | — | 在 `/var/lib/agenthub/backups` 挂载 PVC，并设置 `AGENTHUB_BACKUP_DIR` |

非敏感值进入 ConfigMap；内联的敏感值（数据库 DSN、引导管理员密码、加密/推送密钥、OAuth 密钥）
进入控制器托管的 Secret。任何由 `existingSecret` 支撑的字段，会直接通过 `secretKeyRef`
从你预先创建的 Secret 注入，且**绝不**被拷贝进托管 Secret。

## 必须遵守的约束

### 外部 PostgreSQL 为必选项
没有 `DATABASE_URL`，agenthub 拒绝启动。控制器在创建任何工作负载**之前**就校验这一点：
若既未设置 `spec.database.url` 也未设置 `spec.database.existingSecret`，调和会停止，
并置 `status.conditions[Available]=False, reason=MissingDatabase`。务必提供一个真实数据库
——operator 不会替你部署。

### 生产环境需要两样东西
当 `spec.env: prod` 时（`config.go` 强制）：
- `spec.bootstrapAdmin.email` 必须设置，且
- `spec.encryptionKey` 必须 ≥ 32 字节（或引用某个 `existingSecret`）。

缺少任意一项的 prod CR 会快速失败，原因分别为 `reason=MissingAdminEmail` 或
`reason=WeakEncryptionKey`——Pod 永远不会被创建，因此你得到的是清晰的状态条件，
而不是静默的 CrashLoop。

### 多副本迁移竞态
agenthub 在启动时执行内嵌的 schema 迁移。当 `replicas > 1` 时，多个 Pod 会在 DDL 上产生竞态。
**初始建表阶段保持 `replicas: 1`**，等数据库迁移完成后再扩容。（专门的迁移 Job 不在本文范围。）

### 命名空间作用域
operator 被授予一个**命名空间级 `Role`**（而非 `ClusterRole`），权限限定在其发布命名空间内，
且 `WATCH_NAMESPACE` 被设为该命名空间。请在**与 operator 相同的命名空间**中创建你的
`AgentHub` CR。如需 watch 多个命名空间，把 chart 改为 `ClusterRole` / `ClusterRoleBinding`
并去掉 `WATCH_NAMESPACE`。

## 本地集群演练（kind / k3d）

本节在本地集群跑起整套栈——包括集群**内部**的 PostgreSQL。operator 不管理数据库，
但"外部"仅表示"不由 operator 调和"；同一命名空间内的 Postgres 工作负载完全可用，
且可通过集群内 DNS 访问。

### 1. 创建集群

```bash
# kind
kind create cluster --name agenthub
# 或 k3d
k3d cluster create agenthub --wait
```

### 2. 构建并加载镜像

operator 与 agenthub 镜像必须存在于集群中。在本地 Docker daemon 构建并导入：

```bash
# 在 agenthub 仓库根目录
docker build -f Dockerfile -t agenthub:latest .
# 在 operator/ 下
make -C operator docker-build IMG=agenthub-operator:latest

# kind：从本地 daemon 导入
kind load docker-image agenthub:latest agenthub-operator:latest --name agenthub
# k3d：从本地 daemon 导入
k3d image import agenthub:latest agenthub-operator:latest -n agenthub
```

> 这里的镜像 tag 必须与 `helm install` 期望的一致。chart 默认是 `agenthub-operator:latest`
> （见 `values.yaml`），示例 CR（`examples/agenthub-sample.yaml`）使用 `agenthub:latest`。
> 如果你构建了其他 tag，安装时用 `--set operator.image.tag=<tag>` 覆盖，并相应设置
> `AgentHub` CR 的 `spec.image.tag`——否则 operator/agenthub 的 Pod 会停留在 `ImagePullBackOff`。

如果改为把镜像推送到仓库，请在 chart 的 `values.yaml`（或 `AgentHub` 的 spec）中相应设置
`image.repository` / `tag`。

### 3. 在集群内启动 PostgreSQL

应用随 `examples/postgres.yaml` 提供的 Postgres 清单——一个位于 operator 命名空间内的
Namespace 加 Deployment + Service。完整的资源定义都在该文件中：

```bash
kubectl apply -f operator/examples/postgres.yaml
kubectl -n agenthub-operator wait --for=condition=Ready pod -l app=postgres --timeout=120s
```

集群内的 DSN 随后为
`postgres://agenthub:agenthub@postgres.agenthub-operator.svc.cluster.local:5432/agenthub`
（服务 DNS 为 `<name>.<namespace>.svc.cluster.local`）。请将 host / 用户 / 库 / 密码
与你在清单中的设置对齐。

> 若想要更接近生产的 Postgres，可使用 Bitnami chart ——
> `helm install pg oci://registry-1.docker.io/bitnamicharts/postgresql -n agenthub-operator`
> —— 并从其发布的 Secret 中读取生成的密码，而不是硬编码。

### 4. 安装 operator 并创建 `AgentHub`

```bash
helm install agenthub-operator operator/deploy/agenthub-operator --namespace agenthub-operator --create-namespace

kubectl -n agenthub-operator apply -f operator/examples/agenthub-sample.yaml
```

示例文件 `operator/examples/agenthub-sample.yaml` 自带的 `database.url` 指向一个集群内 Postgres
（`postgres://agenthub:changeme@postgres:5432/agenthub?sslmode=disable`）。
如果你的 Postgres 不同，请按第 3 步的 DSN 修改
（例如 `postgres://agenthub:agenthub@postgres.agenthub-operator.svc.cluster.local:5432/agenthub`）。

如果你不想把 DSN 内联，可预先创建一个 Secret，并以它替代 `spec.database.url`：

```bash
kubectl -n agenthub-operator create secret generic agenthub-db \
  --from-literal=DATABASE_URL='postgres://agenthub:agenthub@postgres.agenthub-operator.svc.cluster.local:5432/agenthub'
# 然后设置 spec.database.existingSecret: agenthub-db（可选项
# spec.database.secretKey: DATABASE_URL），并省略 spec.database.url。
```

### 5. 验证

```bash
kubectl -n agenthub-operator get agenthub
kubectl -n agenthub-operator get pods,svc,deploy
kubectl -n agenthub-operator port-forward svc/agenthub 8080:3500 &
curl -s localhost:8080/healthz
```

`status.conditions[Available]=True` 且 `readyReplicas: 1` 表示 operator 已成功调和。
数据库迁移完成后，你可以用 `kubectl -n agenthub-operator scale agenthub agenthub --replicas=3`
（或修改 CR 的 `spec.replicas`）扩容——见『多副本迁移竞态』。

### 清理

```bash
kind delete cluster --name agenthub  # 或：k3d cluster delete agenthub
```

## 状态与可观测性

```bash
kubectl -n agenthub-operator get agenthub agenthub -o yaml
```

`status.conditions` 携带 `Available` 与 `Progressing`；`status.readyReplicas` 反映 Deployment
的状态。operator 自身在 `:8081` 暴露 `/healthz` 与 `/readyz`，并在 `:8080` 暴露 Prometheus 指标。

## 目录结构

```
operator/
  api/v1alpha1/        AgentHub CRD 的 Go 类型 + 生成的 deepcopy
  internal/controller/ AgentHubReconciler（含测试）
  cmd/agenthub-operator/main.go  管理器入口
  config/crd, config/rbac        生成的 CRD + operator RBAC
  deploy/agenthub-operator/      Helm chart（operator + CRD 安装）
  examples/agenthub-sample.yaml  示例 AgentHub CR
  Dockerfile, Makefile           构建 + 代码生成
```

`operator/` 是一个**独立的 Go module**（`github.com/agenthub/operator`），拥有自己的 `go.mod`；
父级 agenthub module 及其零额外依赖的 `go.mod` 不受影响。

## 重新生成清单

```bash
cd operator && make manifests  # 需要 ./bin/controller-gen（由 go install 下载）
```
