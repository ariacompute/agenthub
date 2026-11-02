# task-isolation.md — 隔离与审计实施记录

> 方案见 `requirements-isolation.md`。本文是**落地记录**：每一步改了什么文件、
> 怎么验证、留了哪些取舍。按依赖顺序排列，前面的步骤是后面的前提。

## 步骤 1 — 迁移（022–027）

| 文件 | 内容 |
| --- | --- |
| `migrations/022_task_ownership.sql` | `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` / `events` 增 `owner_id`；`tasks(owner_id, created_at DESC)`、`events(task_id, seq)` 等复合索引 |
| `migrations/023_knowledge_owner.sql` | `knowledge.owner_id` + `(owner_id, created_at DESC)` |
| `migrations/024_resource_visibility.sql` | `resources.visibility`（`restricted` / `public`，CHECK + 默认 `restricted`），不回填 |
| `migrations/025_audit_log.sql` | `audit_log` 全字段 + 五个索引 + 拒绝 `UPDATE/DELETE/TRUNCATE` 的触发器 |
| `migrations/026_rls_policies.sql` | 八张表 `ENABLE` + `FORCE ROW LEVEL SECURITY`；`agenthub_row_visible()`；`audit_log` 三条独立策略 |
| `migrations/027_audit_permission.sql` | 幂等登记 `audit:read` 并授予 owner 角色 |

验证：`internal/store/store_test.go` 的 `want` 清单已补 022–027，迁移顺序测试通过。

## 步骤 2 — 身份载体与作用域事务

| 文件 | 内容 |
| --- | --- |
| `internal/identity/identity.go`（新） | `Identity{OwnerID, Bypass, System}` + `With`/`From`/`Owner`/`System`/`Request`；零依赖 |
| `internal/store/scope.go`（新） | `scoped[T](ctx, db, fn)`：有身份开短事务并 `set_config(..., true)`，无身份直连 |
| `internal/store/db.go` | `AGENTHUB_RLS=off` → DSN 追加 `options=-c app.system=on`，`Open` 记一条 warn |
| `internal/config/config.go` | `RLSMode` / `RLSAcknowledged` / `MigrateDatabaseURL` / `Audit*` 与 `Validate` 规则 |
| `cmd/agenthub-server/main.go` | 启动期降级告警、DDL 连接分离（`runMigrations`） |
| `scripts/provision-db-roles.sql` | 拆 `migrator` / `app` / `readonly`，`audit_log` 只给 `INSERT, SELECT`，末尾断言八张表策略齐备 |

验证：`config` 单测覆盖 `AGENTHUB_RLS=off` 在 `env=prod` 被拒；`store` 编译与内存路径单测通过。

## 步骤 3 — 归属在两条路径上都生效

| 文件 | 内容 |
| --- | --- |
| `internal/model/model.go` | `Task.OwnerID`、`Knowledge.OwnerID`、`Resource.Visibility`、`AuditLog` |
| `internal/hub/task.go` / `store.go` | `Create` 记录归属、`List`/`Get` 按归属过滤；`TaskRepository` 端口同步扩参 |
| `internal/store/task_store.go` | 全部方法走 `scoped`；`owned()` 在 SQL 里**再过滤一次**（RLS 关掉时仍正确） |
| `internal/store/knowledge_store.go` | `List`/`Add` 增 `owner_id` 维度 |
| `internal/store/resource_store.go` | 读写 `visibility` |
| `internal/store/seed.go` | demo 资源显式 `visibility='public'` |

验证：同一批用例分别跑内存与 GORM 实现，断言「他人任务不可见、管理员可见、系统身份可见」。

## 步骤 4 — 授权 fail-closed 与 `/a2a` 守卫

| 文件 | 内容 |
| --- | --- |
| `internal/authz/authz.go` | `CanAccessResource` / `VisibleResourceIDs` 无 grant 即拒绝；新增 `PermAuditRead` |
| `internal/registry/visibility.go`（新）/ `visibility_test.go` | `Visibility` 常量、默认 `restricted`、取值校验与用例 |
| `internal/handlers/principal.go` | 唯一漏斗：`可读 = public || CanAccessResource`；`canReadTask`/`canWriteTask` 把越权掩码为 404 |
| `internal/handlers/a2a.go` | `mayInvoke` 强制解析 `resourceId`；`tasks/*`、`push/*` 加授权；`ResolveTaskID()` |
| `internal/a2a/jsonrpc.go` | 新增 `-32009 CodeUnauthenticated` |
| `internal/a2a/methods.go` | `TaskQueryParams.TaskID`、`TaskPushNotificationConfigParams.ID` + `ResolveTaskID()` |

验证：`handlers` 与 `a2a` 单测覆盖「匿名调 tasks/get → -32009」「他人任务 → task_not_found」
「push 用 id / set 用 taskId 均可」「两者都缺 → -32602」。

## 步骤 5 — 审计内核

| 文件 | 内容 |
| --- | --- |
| `internal/audit/audit.go`（新） | `Event`（CloudTrail 字段语义）、`Query`、`Stats`、`Store` 端口、`Tap` 中间件（`statusRecorder` + request-id 生成/透传 + `NotePrincipal`）、脱敏规则 |
| `internal/audit/chain.go`（新） | `Canonical` / `Hash(prev, event)` / `VerifyRange` |
| `internal/audit/recorder.go`（新） | 单写者、有界队列、批量插入、`Flush`、保留期清理、丢帧计数 |
| `internal/store/audit_store.go`（新） | `Append` / `List` / `Get` / `Stats` / `LastHash` / 分段读取 |
| `internal/handlers/audit.go`（新） | `GET /api/audit`、`/api/audit/{id}`、`/api/audit/verify` |
| `internal/handlers/wrap.go` | 链首插入 `audit.Tap`（`Deps.Audit` 为 nil 时跳过） |
| `cmd/agenthub-server/main.go` | 装配 Recorder、启动续链与清理 tick、关停 `Flush` |

验证：`audit` 单测覆盖链确定、篡改后 `VerifyRange` 定位到断点、队列满丢帧但请求不阻塞；
`handlers/audit_test.go` 覆盖权限拒绝与未接线时的 503。

## 步骤 6 — 控制台审计页

| 文件 | 内容 |
| --- | --- |
| `web/src/views/AuditView.vue`（新） | 筛选栏 + 概览 + 明细表 + 取证抽屉 + 链校验 |
| `web/src/lib/audit.js`（新） | 纯逻辑：时间窗、查询参数、结果分类、链状态、游标 |
| `web/src/router.js` / `components/Sidebar.vue` | `/audit` 路由（`meta.permissions=['audit:read']`）与导航项 |
| `web/src/lib/api.js` | `listAudit` / `getAudit` / `verifyAudit` |
| `web/src/lib/copy/audit-copy.js`（新） | 13 语文案 |
| `web/src/views/RegistryView.vue` + `copy/registry-copy.js` | 资源可见性徽标与表单选择（13 语） |
| `web/tests/audit.spec.js`（新） | 22 条用例：时间窗边界、过滤组合、拒绝 vs 失败、链状态、游标 |

验证：`bun run test` 180 条全绿；`bun run build` 通过；单色守卫（`style.spec.js`）不受影响。

## 步骤 7 — compose 加固与文档

| 文件 | 内容 |
| --- | --- |
| `docker-compose.yml` | `agenthub`：`cap_drop: [ALL]` + `no-new-privileges` + `read_only` + `tmpfs /tmp` + `pids_limit` + `mem_limit`；`postgres`：`cap_drop` 高危集合 + `no-new-privileges`；网络拆 `edge` / `data`（`data` 为 `internal`）；补齐 `AGENTHUB_RLS*`、`AGENTHUB_AUDIT_*` |
| `internal/adapter/cli_test.go` | #8 回归用例：命令只来自记录、提示词不得选命令、白名单精确名、参数策略默认拒绝 |
| `README.md` / `README_cn.md` | 新环境变量、破坏性变更与迁移指引入口 |
| `docs/docs.md` / `docs_cn.md` | `/api/audit` 三个端点与 push 参数兼容说明 |

## 遗留与取舍

- `resources` 未启用 RLS（理由见 `requirements-isolation.md` §3）。
- 存量行 `owner_id` 保持 `''`，不做回填；管理员需手工标注。
- 审计 `meta` 列保留 jsonb 位置但当前不写业务载荷（只留 request-id 与路径），
  避免把 PII 带进账本。
- 桌面端环回代理的 bearer token / nonce 兑换、CSP 与外置主题脚本、系统钥匙串会话存储
  属 agentbuddy 仓库，记录在其 `requirements.md` / `task.md`。
