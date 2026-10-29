# 🚀 AgentHub Operator 发布说明

> 面向 Kubernetes 的**声明式部署控制器**。你只需提交一份 `AgentHub` 自定义资源（CR），
> operator 就会把运行 agenthub 所需的全部工作负载与配置拉起、调谐、并随 CR 一起回收。
> 本文件是 operator 的**独立说明文档**，与平台整体的 `releasenotes_cn.md` 互补。

---

## ✨ 核心特性

### 🧩 声明式 CRD：`AgentHub`
- API 组 `agenthub.ai/v1alpha1`，资源名 `AgentHub`。
- 一份 YAML 描述「要跑一个怎样的 agenthub 实例」，`kubectl apply` 后即交给控制器。
- operator 把调谐结果写回 CR 的 `status.conditions`，部署状态一目了然。

### 📦 一键拉起全栈工作负载
operator 为每个实例调和（reconcile）出以下**被管对象**：
- ⚙️ `ConfigMap`（`<实例>-config`）—— 非敏感配置
- 🔑 `Secret`（`<实例>-secrets`）—— 托管写入的敏感字段
- 👤 `ServiceAccount` —— 应用自身身份
- 🛡️ 最小权限 `Role` + `RoleBinding` —— 仅授予应用所需权限
- 🚀 `Deployment` —— agenthub 主进程（含就绪/存活探针）
- 🌐 `Service`（ClusterIP）—— 暴露 `AGENTHUB_HTTP_PORT`（默认 3500）
- 💾 `PersistentVolumeClaim`（可选）—— 开启持久化时挂载备份卷

### ♻️ 级联回收（Garbage Collection）
- 上述子资源都带 **owner 引用（ownerReference）**，指向 `AgentHub` CR。
- 删除 CR 时，所有子资源随之一并回收，**不留孤儿对象**。
- 控制器 `watch` 全部被管类型，任何漂移都会被拉回期望状态。

### 🔐 密钥：托管值 vs. 外部 Secret
CR 内联的敏感字段（数据库 URL、引导管理员密码/邮箱、加密密钥、推送密钥、OAuth 客户端密钥）
**默认写入 operator 托管的 `<实例>-secrets`**。
某字段也可以改设 `ExistingSecret`：
- 此时 operator **不再托管其值**，而是渲染一个 `SecretKeyRef`，
  指向你自己掌控的外部 Secret —— 部署引用的是「你拥有的 Secret」，凭据不进 operator 的托管 Secret。

### 🔄 安全调谐：upsert + merge patch
- 新增对象用 `upsert` 创建；已存在的对象用 **merge patch**（而非整体 `Update`）更新。
- 这样应用侧并发写 `status` 不会触发乐观锁冲突（optimistic-concurrency conflict）。
- 被管的有类型对象在调用客户端前会**显式盖回 GVK**，避免 controller-runtime
  因 `TypeMeta` 为空而无法推断资源、拼不出 API URL。

### 🛡️ 最小权限 RBAC
- 应用侧 `Role` + `RoleBinding` 仅授予 agenthub 运行所需权限。
- operator **自身**的 `Role` 也已补齐：除 `events.k8s.io` 外，显式授予 core
  `events` 的 `create`/`patch`，使 operator 能正常记录事件（修复了早期的
  `events is forbidden`）。
- 复用既有 `resources:read` / `tasks:read` / `tasks:write` 权限，**不新增角色或用户组**。

### 🩺 健康探针与自愈
- `Deployment` 内置 **就绪（readiness）与存活（liveness）探针**，均打 `/healthz`。
- 任意被管对象漂移会被 reconcile 拉回；Pod 异常由 Deployment 控制器自愈重启。

### 📈 状态与事件可观测
- CR `status.conditions` 记录 `Ready` / 校验失败（如 `MissingDatabase`、
  `MissingAdminEmail`、`WeakEncryptionKey`）等状态与原因。
- operator 通过 Kubernetes `Event` 播报调谐关键节点，可用 `kubectl describe` 查看。

### ⚙️ 丰富配置项（通过 CR 字段映射为 `AGENTHUB_*` 环境变量）
| 类别 | 字段 / 环境变量 |
| --- | --- |
| 数据库 | `spec.database.url` → `DATABASE_URL`（或 `spec.database.existingSecret`） |
| 运行环境 | `spec.env` → `AGENTHUB_ENV`（dev / staging / prod） |
| 镜像 | `spec.image.repository` / `tag` / `pullPolicy` |
| 副本 | `spec.replicas` |
| 端口 | `spec.httpPort` → `AGENTHUB_HTTP_PORT`（默认 3500） |
| 持久化 | `spec.persistence.enabled` / `size`（默认 10Gi） |
| 资源 | `spec.resources`（容器 requests/limits） |
| 引导管理员 | `spec.bootstrapAdmin.email` / `name` → `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL/NAME` |
| 加密密钥 | `spec.encryptionKey.value`（≥32 字节）或 `existingSecret` |
| 备份 | `AGENTHUB_BACKUP_DIR` / `MODE` / `INTERVAL` / `CRON` / `KEEP_LAST` / `MAX_AGE_DAYS` 等 |
| 其它 | 会话 TTL、Cookie Secure、CLI 超时/白名单、事件缓冲、推送、注册中心同步等 |

### 🚢 Helm 一键安装
```bash
helm install agenthub-operator operator/deploy/agenthub-operator \
  -n agenthub-operator --create-namespace
```
一条命令铺好 operator 的 `Deployment`、自身的 `Role`/`RoleBinding`/`ServiceAccount` 与 CRD。

---

## 🏁 快速开始

```bash
# 1) 安装 operator（含 CRD）
helm install agenthub-operator operator/deploy/agenthub-operator \
  -n agenthub-operator --create-namespace

# 2) 启动一个 in-cluster PostgreSQL（仅本地冒烟用，生产请用 Bitnami/托管 PG）
kubectl apply -f operator/examples/postgres.yaml

# 3) 提交 AgentHub 实例
kubectl apply -f operator/examples/agenthub-sample.yaml

# 4) 观察
kubectl -n agenthub-operator get pods,svc,agenthub
```

---

## 🗺️ 部署架构

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

## ⚠️ 注意事项

- 🗄️ **外部 PostgreSQL 必填**：operator **不负责创建数据库**。CR 必须提供
  `spec.database.url` 或 `spec.database.existingSecret`，否则校验失败
  （`MissingDatabase`），Deployment 不会创建。
- 🔒 **生产红线**：`spec.env: prod` 时强制要求
  `spec.bootstrapAdmin.email` 与 `spec.encryptionKey`（≥32 字节或 `existingSecret`），
  否则 `MissingAdminEmail` / `WeakEncryptionKey`。
- 💾 **示例 PG 无持久卷**：`operator/examples/postgres.yaml` 不带 PVC，
  Pod 重建会**重新初始化空库**（user/db 由 `POSTGRES_*` 环境变量重建），生产请用
  Bitnami chart 或自带 PVC 的托管 PG。
- 🧹 **删除即回收**：`kubectl delete agenthub <实例>` 会级联删除全部子资源。

---

## 🔗 相关文档
- 平台整体发布说明：`releasenotes_cn.md`（含「十三、Kubernetes Operator」一节）
- operator 使用与本地集群演练：`operator/README_cn.md`
