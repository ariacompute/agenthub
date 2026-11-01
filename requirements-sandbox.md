# requirements-sandbox.md — agenthub 执行环境沙盒

> 目标：把「执行」从适配器内部的一次性动作，变成**带租约的一等资源**，并让「有哪些执行后端」
> 从代码分支变成数据目录。整体结构与 `internal/router` 同构：消费方定义端口 + 单写者 +
> 单飞节拍 + 内存/GORM 双实现。
>
> 实施记录见 `task-sandbox.md`；业界方案对比见 `docs/sandbox-landscape.md`。

## 1. 功能边界

### 1.1 做

- 沙盒类型的注册表与目录：内置 `mock` / `docker` / `podman` / `gvisor` / `kata` / `bwrap` /
  `remote`，声明式预设 `nsjail` / `isolate` / `systemd-run` / `local` / `nerdctl`，以及平台层
  预设 `e2b` / `microsandbox` / `daytona` / `cubesandbox` / `openhands`。
- 租约：按 `(owner_id, resource_id, scope)` 复用，`scope` 取 router 的 ticket、其次 A2A 的
  `contextId`、最后 `default`；匿名不复用、用完即回收。
- 生命周期与任务解耦：任务取消/失败/重启不销毁租约；租约丢失时下一次调用透明重建。
- 四个执行面收口：CLI、MCP stdio、computer-use 的 shell、远端 agent 调用。
- 执行审计：只存命令摘要、退出码、耗时、输出 sha256 与截断标记。
- 执行节点：节点注册/心跳/长轮询/回传；桌面端（agentbuddy）可选成为节点。
- 观测：类型目录、统计水位、SSE 事件流。

### 1.2 不做

- 不在 Go 进程里 `fork` 出容器：真实隔离一律通过外部可执行文件或远端协议。
- 不内嵌任何平台 SDK（E2B / microsandbox / Daytona / CubeSandbox / OpenHands）。
- 不新增权限码；不做多租户硬隔离之外的跨实例调度。
- 不做沙盒镜像构建（`docker build`）；镜像由运维提供。

## 2. 配置（`internal/config`）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `AGENTHUB_SANDBOX_PROVIDER` | `docker` | 实例默认类型；`off` 表示拒绝一切执行 |
| `AGENTHUB_SANDBOX_PROVIDERS` | 空 | 声明式提供方 JSON 数组（见 §5） |
| `AGENTHUB_SANDBOX_DEFAULT_IMAGE` | `debian:bookworm-slim` | 容器类提供方的默认镜像 |
| `AGENTHUB_SANDBOX_DOCKER_HOST` | 空 | 覆盖 `DOCKER_HOST`（推荐指向 socket-proxy） |
| `AGENTHUB_SANDBOX_TIMEOUT` | `60s` | 单次执行超时上限 |
| `AGENTHUB_SANDBOX_MAX_OUTPUT_BYTES` | `65536` | 输出截断上限 |
| `AGENTHUB_SANDBOX_IDLE_TTL` | `30m` | 空闲回收 |
| `AGENTHUB_SANDBOX_MAX_TTL` | `2h` | 单租约最长存活（续租也不能超过） |
| `AGENTHUB_SANDBOX_MAX_PER_OWNER` | `4` | 每归属人租约上限 |
| `AGENTHUB_SANDBOX_MAX_TOTAL` | `16` | 全局租约上限，必须 ≥ `AGENTHUB_ROUTER_MAX_TICKETS` |
| `AGENTHUB_SANDBOX_EGRESS_ALLOWLIST` | 空 | 出网白名单（host 后缀匹配） |
| `AGENTHUB_SANDBOX_NODE_POLL_TIMEOUT` | `25s` | 节点长轮询挂起时长 |
| `AGENTHUB_SANDBOX_SWEEP_INTERVAL` | `30s` | 回收节拍 |

校验：`PROVIDER` 属于已注册类型或 `off`；声明式条目加载失败（未知占位符 / 空 argv /
非绝对路径）**拒绝启动**；`MAX_TOTAL ≥ ROUTER_MAX_TICKETS`；所有时长与字节数为正。

## 3. API

权限沿用 `resources:read` / `resources:write` 与 `tasks:write`，不新增权限码。
未接线（`Deps.Sandboxes == nil`）一律 `503 feature_unavailable`；后端不可用是
`409 sandbox_unsupported`（配置/环境问题，重试无用）。

### 3.1 沙盒

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/sandboxes` | resources:read | 当前身份可见的租约 |
| GET | `/api/sandboxes/stats` | resources:read | 五指标水位 |
| GET | `/api/sandboxes/events` | resources:read | SSE，支持 `since` 回放 |
| GET | `/api/sandboxes/{id}` | resources:read | 租约详情 + 最近执行 |
| POST | `/api/sandboxes/{id}/exec` | resources:write | 试跑（绕开 router，审计标 `dry_run`） |
| POST | `/api/sandboxes/{id}/renew` | resources:write | 续租 |
| DELETE | `/api/sandboxes/{id}` | resources:write | 终止 |

### 3.2 类型目录

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/sandbox-providers` | resources:read | 按层分组的类型 + 可用性 + 原因 |

### 3.3 执行节点

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/sandbox-nodes` | resources:read | 节点列表（在线/离线） |
| POST | `/api/sandbox-nodes` | resources:write | 注册节点（返回节点 id） |
| DELETE | `/api/sandbox-nodes/{id}` | resources:write | 撤销节点 |
| POST | `/api/sandbox-node/heartbeat` | 节点凭证 | 心跳 |
| GET | `/api/sandbox-node/jobs` | 节点凭证 | 长轮询领取任务 |
| POST | `/api/sandbox-node/jobs/{id}/result` | 节点凭证 | 回传结果 |

节点凭证复用 API key（`token_hash`），节点记录绑定 `owner_id` 与 `api_key_id`，
撤销 key 即失效。

### 3.4 computers 扩展

- `POST /api/computers` 创建时真正申请租约，写回 `provider` / `leaseId` / `nodeId` / `expiresAt`；
- `DELETE /api/computers/{id}` 终止并销毁租约；
- `POST /api/computers/{id}/exec` 一次性试跑。

## 4. 表结构（`migrations/021_sandboxes.sql`）

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `sandbox_leases` | `id`, `owner_id`, `resource_id`, `scope`, `provider`, `runtime`, `state`, `ref`, `profile` jsonb, `created_at`, `renewed_at`, `expires_at` | 唯一索引 `(owner_id, resource_id, scope, provider)` 支撑复用键查找 |
| `sandbox_execs` | `id`, `lease_id`, `owner_id`, `command`, `exit_code`, `duration_ms`, `truncated`, `stdout_sha256`, `stderr_sha256`, `dry_run`, `created_at` | 只存摘要，不存原始输出 |
| `sandbox_nodes` | `id`, `owner_id`, `name`, `api_key_id`, `capabilities` jsonb, `status`, `last_seen_at`, `created_at` | 心跳超时自动 `offline` |
| `sandbox_jobs` | `id`, `node_id`, `lease_id`, `provider`, `state`, `command`, `args` jsonb, `exit_code`, `error`, `created_at`, `finished_at` | 只在完成后落库做审计；队列本身在进程内 |
| `sandbox_events` | `seq` BIGSERIAL, `subject`, `kind`, `payload` jsonb, `at` | SSE `since` 回放 |

`computers` 增列：`provider` / `lease_id` / `node_id` / `expires_at`。
全部 `IF NOT EXISTS`；`store_test.go` 的 `want` 追加 `021_sandboxes.sql`。

## 5. 声明式提供方

```json
[{
  "name": "nsjail",
  "layer": "process",
  "isolation": "namespace",
  "binary": "/usr/bin/nsjail",
  "probe_args": [["--help"]],
  "create_args": [],
  "exec_args": [["-Mo", "--user", "65534", "--cwd", "{workdir}", "--time_limit", "{timeout_s}", "--", "{command}"], ["{arg}"]],
  "destroy_args": []
}]
```

- 占位符白名单：`{workdir}` `{image}` `{cpu}` `{memory_mb}` `{pids}` `{network}`
  `{timeout_s}` `{command}` `{arg}` `{env:NAME}` `{key}` `{lease_id}` `{owner_id}`；
  未知占位符、空 argv、非绝对路径二进制在**加载期**即失败。
- 展开是**逐 argv 元素**的，永不拼 shell、永不经 `/bin/sh`。
- `exec_args` 是「前缀段 + 尾段」两段：第一段在 `{command}` 处插入命令，第二段逐参数展开
  `{arg}` 一次一个。这样参数永远是独立 argv 元素。
- 模板只能来自运营者配置（环境变量 / 启动配置），永不来自请求内容。
- stdout 若为 JSON `{"exit_code":N,...}` 则解析为结构化回执，否则按退出码语义并标
  `receipt=unparsed`。

## 6. 异常与错误码

| 错误码 | HTTP | 场景 |
| --- | --- | --- |
| `feature_unavailable` | 503 | 沙盒未接线（`Deps.Sandboxes == nil`） |
| `sandbox_disabled` | 403 | `AGENTHUB_SANDBOX_PROVIDER=off` |
| `sandbox_unsupported` | 409 | 类型已注册但探测不可用（未安装 / socket 不可达） |
| `sandbox_unknown_provider` | 400 | 名字未注册 |
| `sandbox_policy_denied` | 403 | 命令 / 参数 / 出网目标被策略拒绝 |
| `sandbox_timeout` | 504 | 执行超时 |
| `sandbox_lease_lost` | 409 | 租约已回收或不存在，调用方应重试（会透明重建） |
| `sandbox_limit_reached` | 429 | 触达每人/全局租约上限，**可重试** |
| `sandbox_node_offline` | 503 | 远端节点离线，**可重试** |
| `sandbox_not_found` | 404 | 租约不存在 |

`limit_reached` 与 `node_offline` 供 router 的重试阶梯（`MaxAttempts` / `RetryBase`）消化，
不得占着 ticket 阻塞。

## 7. 验收标准

1. `AGENTHUB_SANDBOX_PROVIDER=off` 时四个执行面全部拒绝，且返回 `sandbox_disabled`。
2. `docker` 缺失时返回 `sandbox_unsupported`，**绝不**执行到宿主进程，也不降级到 mock/local。
3. CLI 连接器：prompt 中的首个词**不会**变成命令；命令必须等于资源登记的命令；
   参数不合策略时 `sandbox_policy_denied`。
4. 同一 `(owner, resource, ticket)` 的连续执行命中同一租约（执行审计里 `lease_id` 相同）；
   不同 ticket 不复用。
5. 租约被 `DELETE` 后，下一次执行透明重建而不是失败。
6. `Sweep` 单飞：并发触发只跑一次；过期租约被回收并写入 `expired` 状态。
7. 声明式模板：未知占位符启动即失败；`exec_args` 展开后参数仍是独立 argv 元素（单测断言）。
8. 节点离线时 `GET /api/sandbox-node/jobs` 长轮询超时返回空；结果回传幂等。
9. `GET /api/sandboxes/events?since=N` 能回放 `N` 之后的全部帧，`maxSeq` 告知是否追平。
10. `go build` / `go vet` / `go test -race ./...` 全绿；`bun run build` / `bun run test` 全绿。
