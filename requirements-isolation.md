# requirements-isolation.md — 数据隔离（RLS）与全 API 审计

> 对应评估文档 5.3 的 #1–#4、#9，以及第四节「API 一致性」。
> 实施记录见 `task-isolation.md`。既有安全加固（传输 / 认证 / SSRF / 密钥）见
> `requirements-security.md`，本文只覆盖**按归属隔离**与**调用留痕**两件事。

## 0. 一句话目标

把「谁能看见哪一行」从**应用层约定**下沉为**数据库承诺**，并把「谁在什么时候调用了什么」
从**日志文本**升级为**可校验的只追加账本**。

## 1. 归属维度（ownership）

| 表 | 新增列 | 语义 |
| --- | --- | --- |
| `tasks` / `task_artifacts` / `task_spans` / `task_pending` / `task_push` / `events` | `owner_id TEXT NOT NULL DEFAULT ''` | 提交该任务的用户 ID；`''` 表示系统产生（仅系统身份 / 管理员可见） |
| `knowledge` | `owner_id TEXT NOT NULL DEFAULT ''` | 知识块归属；`''` 表示系统写入 |
| `resources` | 不改归属（见 §3） | 可见性改由显式 `visibility` 表达 |

- 子表**冗余** `owner_id`，策略里不做 JOIN：一条 `USING` 里回表会让每次全表扫描都变成
  nested loop。
- 复合索引保证策略过滤后仍走索引：`tasks(owner_id, created_at DESC)`、
  `events(task_id, seq)`、`audit_log(ts DESC)`、`audit_log(actor_id, ts DESC)`、
  `audit_log(action, ts DESC)`。
- **不做存量回填**：迁移把存量行的 `owner_id` 留在 `''`，即「系统产生」。上线后由管理员
  手工补 grant 或重新提交，绝不猜测历史数据的归属。

## 2. 行级安全（RLS）

### 2.1 身份载体

`internal/identity`（零依赖，`hub` 与 `store` 均可 import）把三个事实放进 `context`：

```go
type Identity struct {
    OwnerID string // 归属 ID；匿名请求为空
    Bypass  bool   // 管理员：跨归属读写
    System  bool   // 进程内路径：状态机、事件扇出、seed、迁移、备份、审计写入
}

func Anonymous() Identity
func With(ctx context.Context, id Identity) context.Context
func From(ctx context.Context) (Identity, bool)
func Owner(ctx context.Context) string
func System() context.Context            // 进程内后台路径的 ctx
func SystemWith(id Identity) context.Context // 系统身份 + 一个归属（后台代某用户写）
func IsSystem(ctx context.Context) bool
```

### 2.2 身份如何进入 SQL

RLS 的会话变量必须与事务同生命周期，因此**不能**用连接池级 `SET`。`internal/store`
提供泛型辅助 `scoped[T](ctx, db, fn)`：

- `ctx` 携带身份 → 开一个**短事务**，先执行
  `SELECT set_config('app.owner_id', ?, true), set_config('app.bypass', ?, true),
  set_config('app.system', ?, true)`，再跑仓储方法；
- `ctx` 不携带身份 → 直接复用 `db`（内存语义与既有单测不受影响）。

取舍：每次仓储调用多一次 `BEGIN/COMMIT`（约 0.1–0.3 ms），换来「请求不持连接」。
SSE（`/api/tasks/{id}/events`、`/a2a/stream`）会长时间占用连接，若在中间件层开事务，
25 个连接会被少量长连接吃光，因此选短事务。

### 2.3 策略与失败方向

`agenthub_row_visible(owner_id)`：

```sql
current_setting('app.system', true) = 'on'
OR current_setting('app.bypass', true) = 'on'
OR (owner_id <> '' AND owner_id = current_setting('app.owner_id', true))
```

未设置时 `current_setting` 返回 NULL，比较结果为 NULL，策略视为 false → **fail-closed**：
忘记注入身份就是拒绝，而不是放行。

覆盖表：`tasks`、`task_artifacts`、`task_spans`、`task_pending`、`task_push`、`events`、
`knowledge`、`audit_log`，全部 `ENABLE` + `FORCE ROW LEVEL SECURITY`（`FORCE` 让表属主也受约束）。

`audit_log` 单独三条策略：`SELECT` 需 `app.system` / `app.bypass` / `app.audit_read`；
`INSERT` 只允许 `app.system` / `app.bypass`；`DELETE` 只允许 `app.retention`。

### 2.4 角色分离

| 角色 | 权限 | 用途 |
| --- | --- | --- |
| `agenthub_migrator` | `public` schema 属主、DDL | 仅 `AGENTHUB_MIGRATE_DATABASE_URL` |
| `agenthub_app` | DML（`audit_log` 仅 `INSERT, SELECT`）、`NOBYPASSRLS`、**非表属主** | `DATABASE_URL` |
| `agenthub_readonly` | `SELECT`、`NOBYPASSRLS` | 运维 / 分析 / 恢复校验 |

`scripts/provision-db-roles.sql` 幂等，末尾有一条断言：八张表的 `relrowsecurity` 与
`relforcerowsecurity` 必须都为真，否则 `RAISE EXCEPTION` —— 不允许「配了一半」的实例上线。

### 2.5 降级开关

`AGENTHUB_RLS`：

- `enforce`（默认）：策略生效。
- `off`：`store.Open` 在 DSN 追加 `options=-c app.system=on`，让全部策略通过；启动时
  `slog.Warn` 明确写出「每个会话都能看到每一行」。
- `env=prod` 下 `off` 会被 `config.Validate` 拒绝，除非同时设 `AGENTHUB_RLS_ACK=1`
  （即「我知道我在做什么」）。

## 3. 资源可见性（`resources.visibility`）— **破坏性变更**

- 新增 `visibility TEXT NOT NULL DEFAULT 'restricted'`，取值 `public` / `restricted`（CHECK 约束）。
- 语义从「没写就是公开」改为「**没写就是受限**」：

  ```
  可读 = entry.Visibility == public || authz.CanAccessResource(principal, entry)
  ```

- `authz.CanAccessResource` 与 `VisibleResourceIDs` 改为 fail-closed：**无 grant 即不可见**，
  不再默认 `return true`。
- 匿名调用者（没有 `Principal`）只剩显式 `public` 的资源可触达。
- `resources` **不启用 RLS**：其可见性来自 `resource_grants` 的多表半连接，且注册表在
  `reg.Snapshot()` 中有内存快照，DB 层策略对快照路径没有收益、反而显著增加策略复杂度。
  该层仍由应用层唯一漏斗判定，此取舍记录在此。

### 3.1 升级影响（必读）

升级后，**任何未显式标 `public` 且没有 grant 的资源都会从所有人的列表里消失**，
调用返回 `404`（不是 `403`，避免 ID 枚举）。

三种恢复方式，任选：

```sql
-- a) 公开：所有人（含匿名 A2A 对端）可发现、可调用
UPDATE resources SET visibility = 'public' WHERE id = '<id>';

-- b) 精确授权：只给某个用户（走控制台「授权」页更稳妥）
INSERT INTO resource_grants (id, resource_id, subject_kind, subject_id, permission)
VALUES ('<uuid>', '<resource-id>', 'user', '<user-id>', 'invoke');

-- c) 重新提交资源时在表单里选「公开 / 受限」
```

演示数据（`AGENTHUB_SEED_DEMO=true`）已在 `seed.go` 里显式写入 `visibility='public'`，
因此开箱演示不受影响。

## 4. 全 API 审计

### 4.1 语义（对齐 CloudTrail）

| CloudTrail | `audit_log` 列 | 说明 |
| --- | --- | --- |
| `eventID` | `event_id` | 随机 32 字节 hex，唯一索引 |
| `eventTime` | `ts` | 服务端时钟 |
| `eventSource` | `source` | `api` / `a2a` |
| `eventName` | `action` | A2A 方法名（`tasks/get`）或路由（`GET /api/tasks`） |
| `readOnly` | `read_only` | `GET/HEAD/OPTIONS` 为真 |
| `userIdentity` | `actor_kind` / `actor_id` / `actor_label` / `owner_id` | 会话用户、API key、匿名 |
| `sourceIPAddress` | `ip` | 复用 `X-Forwarded-For` 最左跳（与限流一致） |
| — | `request_id` | 请求 ID（透传或生成），同一次调用的所有行可串起来 |
| — | `method` / `path` / `user_agent` | 路径**脱敏**：query 值一律 `REDACTED` |
| `resources[]` | `resource_id` / `resource_kind` | 本次触达的资源 |
| `responseElements` / `errorCode` | `status` / `error_code` | 状态码与机器码（不含响应体） |
| — | `latency_ms` / `read_only` | 耗时、读写标记 |
| — | `prev_hash` / `hash` | 逐行链接 |

### 4.2 只追加

- 应用从不 `UPDATE` / `DELETE`。
- 触发器 `audit_log_no_update`（`BEFORE UPDATE OR DELETE`）与 `audit_log_no_truncate`
  （`BEFORE TRUNCATE`）无条件拒绝，**包括表属主与超级用户**；唯一例外是
  `app.retention='on'` 事务里的 `DELETE`（保留期清理）。
- 角色层再加一道：`REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM agenthub_app`。

### 4.3 哈希链

```
hash[i] = sha256( hash[i-1] || canonical(event[i]) )
```

- `canonical` 为固定字段序的 pipe 连接，不含 `hash` 本身，也不含 `id`/`seq`（数据库分配的
  序号不可从行内容复现）。
- 启动时读末行 `hash` 续链，因此重启不会另起一条链。
- 写入由**单写者 goroutine** 串行完成，天然保序：有界队列（默认 8192）+ 批量插入
  （64 条或 500 ms 触发）。队列满则丢弃并计数告警，**绝不阻塞业务请求**；关停时
  `Flush(timeout)`。
- `GET /api/audit/verify?from=&to=` 按时间片流式重算，返回 `{ok, checked, brokenAt}`。

### 4.4 端点

| 方法 + 路径 | 权限 | 说明 |
| --- | --- | --- |
| `GET /api/audit` | `audit:read` | 列表 + 概览；过滤 `from` `to` `actor` `action` `resource` `source` `outcome` `q` `before` `limit` |
| `GET /api/audit/{id}` | `audit:read` | 单条详情（含 `prev_hash` / `hash`） |
| `GET /api/audit/verify` | `audit:read` | 链校验，按 `from`/`to` 分段 |

- `audit:read` 是**新增权限码**（`authz.PermAuditRead`，登记进 `AllPermissions`，
  由 `027_audit_permission.sql` 幂等授予 owner 角色）。
- 响应里不透出请求体与响应体；脱敏键（`token` / `key` / `secret` / `password` /
  `authorization` / `cookie`）从不落库。
- 未接审计服务时三个端点返回 `503 feature_unavailable`，而不是 404。

### 4.5 保留期

`AGENTHUB_AUDIT_RETENTION`（默认 `2160h` = 90 天）到期由清理 tick 删除，删除本身写一行
`action=audit/retention` 的审计记录 —— 删也要留痕。

## 5. `/a2a` 方法授权

| 方法 | 要求 |
| --- | --- |
| `message/send` / `message/stream` | 必须解析出具体 `resourceId`（`metadata.resourceId`，缺省时由 `hub.ResolveResource` 反推）；解析不到 → `resource_not_found` |
| `tasks/get` / `tasks/cancel` / `tasks/resume` | 必须已认证；任务归属 = 调用者，或 `tasks:write` + 管理员；他人任务一律 `task_not_found` |
| `tasks/pushNotification/set` / `get` | 同上（属主或管理员） |
| 匿名调用者 | 只能触达 `visibility=public` 的资源；需要认证的方法返回 `-32009` |

新增 A2A 错误码：`-32009 CodeUnauthenticated`（只用于「匿名调用需认证的方法」）。

## 6. API 一致性（评估 6.4）

`tasks/pushNotification/set` 用 `taskId`、`get` 用 `id` 的历史不一致，按**向后兼容**修正：

- 参数结构同时接受两个字段；
- 同时出现时以**规范字段**为准（`set` → `taskId`，`get` → `id`）；
- 全部任务维度方法（`tasks/get|cancel|resume`、`push/set|get`）共用同一个解析函数
  `ResolveTaskID()`，行为一致；
- 两者都缺 → `-32602 invalid params`。

## 7. 验收标准

- 迁移幂等、只增不改；`022`–`027` 已登记进 `store_test.go` 的 `want` 清单。
- 新路由已登记 `handlers/listen.go` 的 `Listeners()` 与 `listen_test.go` 的 `probes`。
- 领域层不 import GORM；未注入仓库时保持原内存语义，两条路径行为一致
  （归属过滤在内存与 PostgreSQL 上都有用例）。
- 越权访问统一按「不存在」返回，不泄露存在性。
- 审计写失败不阻断业务；队列满只丢帧并告警。
- `go build` / `go vet` / `go test -race ./...` 全绿；前端 `bun run build` + `bun run test` 全绿。

## 8. 升级操作指引（从无 RLS 版本升上来）

1. **备份**：`scripts/backup-restore-drill.sh` 或一次 `pg_dump -Fc`。
2. **建角色**：以超级用户执行 `scripts/provision-db-roles.sql`（此时第 5 步断言会失败，
   因为策略还没建 —— 先只执行到第 3 步，或忽略最后一段，见第 5 步）。
3. **配 DDL 连接**：`AGENTHUB_MIGRATE_DATABASE_URL=postgres://agenthub_migrator:...`。
4. **配运行连接**：`DATABASE_URL=postgres://agenthub_app:...`（DML only，非属主）。
5. **启动一次**（`AGENTHUB_RLS=enforce`）：`022`–`027` 迁移建列、建策略、装触发器、
   登记 `audit:read`。之后再执行一遍 `provision-db-roles.sql`，末尾断言应当通过。
6. **补可见性**：按 §3.1 给需要用起来的资源标 `public` 或补 grant。管理员登录后
   `/api/audit` 应能看到本次启动产生的审计行。
7. **验证**：`curl -b <admin-cookie> .../api/audit/verify` 返回 `ok: true`；
   `SET ROLE agenthub_app; SELECT count(*) FROM tasks;`（无 `app.*` 设置）应返回 0 行。

降级回退：设 `AGENTHUB_RLS=off`（`env=prod` 还需 `AGENTHUB_RLS_ACK=1`）即可让策略
全部通过，**列与策略本身保留**，无需回滚迁移。
