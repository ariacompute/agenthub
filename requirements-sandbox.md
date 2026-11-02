# requirements-sandbox.md — agenthub 执行环境沙盒

> 目标：把「执行」从适配器内部的一次性动作，变成**带租约的一等资源**，并让「有哪些执行后端」
> 从代码分支变成数据目录。整体结构与 `internal/router` 同构：消费方定义端口 + 单写者 +
> 单飞节拍 + 内存/GORM 双实现。
>
> 实施记录见 `task-sandbox.md`；业界方案对比见 `docs/sandbox-landscape.md`。

## 1. 功能边界

### 1.1 做

- 沙盒类型的注册表与目录：内置 `mock` / `docker` / `podman` / `gvisor` / `kata` / `bwrap` /
  `local` / `remote`，声明式预设 `nsjail` / `isolate` / `systemd-run` / `nerdctl`，以及平台层
  预设 `e2b` / `microsandbox` / `daytona` / `cubesandbox` / `openhands`（各经运营者自备的 shim
  接入，二进制内不含厂商 SDK）。
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
| `AGENTHUB_SANDBOX_ENABLED` | `true` | 总开关；`false` 时控制面不接线——端点 503 `feature_unavailable`，适配器保持接入前的行为 |
| `AGENTHUB_SANDBOX_PROVIDER` | `docker` | 实例默认类型；`off` 表示控制面在、但拒绝一切执行（`sandbox_disabled`） |
| `AGENTHUB_SANDBOX_PROVIDERS` | 空 | 声明式提供方 JSON 数组（见 §5） |
| `AGENTHUB_SANDBOX_DEFAULT_IMAGE` | `debian:bookworm-slim` | 容器类提供方的默认镜像 |
| `AGENTHUB_SANDBOX_DOCKER_HOST` | 空 | 覆盖 `DOCKER_HOST`（推荐指向 socket-proxy） |
| `AGENTHUB_SANDBOX_RUNTIME` | 空 | 容器运行时（`runsc` / `kata`），空表示 runtime 默认 |
| `AGENTHUB_SANDBOX_IDLE_TTL` | `30m` | 空闲回收 |
| `AGENTHUB_SANDBOX_MAX_TTL` | `2h` | 单租约最长存活（续租也不能超过，且 ≥ `IDLE_TTL`） |
| `AGENTHUB_SANDBOX_MAX_PER_OWNER` | `4` | 每归属人租约上限（1..64） |
| `AGENTHUB_SANDBOX_MAX_TOTAL` | `16` | 全局租约上限（≥ `MAX_PER_OWNER`，≤ 256，且 ≥ `AGENTHUB_ROUTER_MAX_TICKETS`） |
| `AGENTHUB_SANDBOX_EGRESS_ALLOWLIST` | 空 | 出网白名单（**尚未生效**：当前没有 provider 做 host 过滤，见 §2.1） |
| `AGENTHUB_SANDBOX_NODE_POLL_TIMEOUT` | `25s` | 节点长轮询挂起时长（1s..60s） |
| `AGENTHUB_SANDBOX_SWEEP_INTERVAL` | `30s` | 回收节拍（1s..10m） |

单次执行的超时与输出上限**不单独配置**：它们来自资源 profile 继承的实例默认值——
`AGENTHUB_CLI_TIMEOUT`（默认 `15s`）与 `AGENTHUB_MAX_OUTPUT_BYTES`（默认 `65536`），
资源可经 policy 覆盖，未设置时回落 `64 KiB`。

校验（`AGENTHUB_SANDBOX_ENABLED=false` 时整块跳过）：`PROVIDER` 属于已注册类型或 `off`；
声明式条目加载失败（未知占位符 / 空 argv / 非绝对路径）**拒绝启动**；各上限范围如上；
`MAX_TOTAL ≥ ROUTER_MAX_TICKETS`（沙盒不能悄悄把调度器串行化）；时长与字节数为正。

### 2.1 网络模式的真实语义

profile 的 `network` 接受 `none / egress_allowlist / bridge / platform` 四个取值，但**只有后两个真的给网**：

| 取值 | bubblewrap | docker / podman | 说明 |
| --- | --- | --- | --- |
| `none`（默认） | `--unshare-all`（无 `--share-net`） | `--network none` | 断网 |
| `egress_allowlist` | 同上（断网） | `--network none` | **名单暂不生效**：host 过滤需要 egress sidecar（`TODO(real)`）；在它出现前该取值按 `none` 执行（fail-closed），词表保留以让存量记录仍可解析 |
| `bridge` | `--share-net` | `--network bridge` | 给网 |
| `platform` | `--share-net` | `--network bridge` | 交给平台默认 |

两个 provider 共用 `sandbox.GrantsNetwork` 判定，因此不会再出现「同一个取值在一种后端断网、在另一种后端放全量网络」的分叉；provider 的 `Describe().Network` 也不再宣称支持 `egress_allowlist`。

`local` provider（既无容器运行时也无 bubblewrap 时的兜底）**没有自己的网络命名空间**：子进程用的是宿主的路由与 DNS，
因此它的 `Describe().Network` 只报 `platform`，不再宣称能做到 `none`。profile 里的 `network` 对它不构成约束，
这一点写在 `local.go` 的注释里——把"给不出网"写成 capability 才是真正的谎报。
`AGENTHUB_SANDBOX_EGRESS_ALLOWLIST` 同理：值会进 profile 以便回读，但没有任何 provider 做 host 过滤，
启动时打一条 `WARN` 说明它不构成访问控制。

## 3. API

权限沿用 `resources:read` / `resources:write` 与 `tasks:write`，不新增权限码：租约与类型目录走
`resources:*`，节点注册/撤销走 `resources:write`，心跳与作业领取/回传走 `tasks:write`。
未接线（`Deps.Sandboxes == nil`，含 `AGENTHUB_SANDBOX_ENABLED=false`）一律 `503 feature_unavailable`；
后端不可用是 `409 sandbox_unsupported`（配置/环境问题，重试无用）。

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
| POST | `/api/sandbox-providers/refresh` | resources:write | 绕过 30s 探测缓存，重新探测全部类型 |

### 3.3 执行节点

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/sandbox-nodes` | resources:read | 节点列表（在线/离线）+ 等待者数 + 轮询提示 |
| POST | `/api/sandbox-nodes` | resources:write | 注册/刷新节点（同 id 为更新；非法 id → 400 `bad_request`；他人已占用的 id → 404 `sandbox_not_found`） |
| DELETE | `/api/sandbox-nodes/{id}` | resources:write | 撤销节点（不销毁其租约，下次调用透明重建） |
| POST | `/api/sandbox-nodes/{id}/heartbeat` | tasks:write | 心跳，返回节点与 `pollHint` |
| GET | `/api/sandbox-nodes/{id}/jobs[?wait=<s>]` | tasks:write | 长轮询领活；空队列 `204`；轮询本身即心跳 |
| POST | `/api/sandbox-nodes/{id}/jobs/{job}` | tasks:write | 回传结果；非本节点的作业 → 404 `sandbox_not_found` |

认证用普通会话或 API Key（按上表权限码判定），不是独立的节点凭证；节点身份是注册时提交的
稳定 `id`，`owner_id` 防止他人抢占同一 id。`sandbox_nodes.api_key_id` 列已预留，但当前注册流程
不写入——撤销某个 key 只会让持有该 key 的调用失效，不会单独下线节点；下线靠心跳超时或 `DELETE`。

### 3.4 computers 扩展

- `POST /api/computers` 创建时真正申请租约，写回 `provider` / `leaseId` / `nodeId` / `expiresAt`，
  申请失败回滚该行并返回机器码；
- `DELETE /api/computers/{id}` 先终止租约再删行（租约已不存在不算错误）；
- 一次性试跑没有独立端点，走 `POST /api/sandboxes/{id}/exec`。

## 4. 表结构（`migrations/021_sandboxes.sql`）

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `sandbox_leases` | `id`, `owner_id`, `resource_id`, `scope`, `provider`, `runtime`, `node_id`, `state`, `ref`, `profile` jsonb, `execs`, `last_exit`, `last_command`, `created_at`, `renewed_at`, `expires_at` | `idx_sandbox_leases_key (owner_id, resource_id, scope, provider, state)` 支撑复用键查找，`(state, expires_at)` 供回收 |
| `sandbox_execs` | `id`, `lease_id`, `owner_id`, `provider`, `command`, `exit_code`, `duration_ms`, `truncated`, `stdout_sha256`, `stderr_sha256`, `dry_run`, `at` | 只存摘要，不存原始输出 |
| `sandbox_nodes` | `id`, `owner_id`, `name`, `api_key_id`, `capabilities` jsonb, `status`, `last_seen_at`, `created_at` | 心跳超时自动 `offline` |
| `sandbox_jobs` | `id`, `node_id`, `lease_id`, `provider`, `op`, `state`, `command`, `exit_code`, `error`, `created_at`, `finished_at` | 只在完成后落库做审计；队列本身在进程内 |
| `sandbox_events` | `seq` BIGSERIAL, `subject`, `kind`, `lease_id`, `payload` jsonb, `at` | SSE `since` 回放 |

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
  `{timeout_s}` `{command}` `{arg}` `{env:NAME}` `{lease_id}` `{owner_id}` `{resource_id}` `{scope}`；
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
| `sandbox_policy_denied` | 403（空命令等参数校验复用该码时 400） | 命令 / 参数 / 出网目标被策略拒绝 |
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
8. 空队列时 `GET /api/sandbox-nodes/{id}/jobs` 在 `wait` 到期后返回 `204`；重复回传同一作业不报错
   （后写覆盖），回传他节点的作业返回 404。
9. `GET /api/sandboxes/events?since=N` 先回放 `N` 之后的帧再订阅直播，回放与直播同 `seq`、
   不重不漏（服务层 `EventsSince` 的 `maxSeq` 即游标）。
10. `go build` / `go vet` / `go test -race ./...` 全绿；`bun run build` / `bun run test` 全绿。
