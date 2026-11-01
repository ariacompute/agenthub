# task-sandbox.md — 实施记录

配套 `requirements-sandbox.md`（契约）与 `docs/sandbox-landscape.md`（选型）。
按 `internal/router` 已交付的范式推进：消费方定义端口 → 单写者 → 单飞节拍 → 双实现。

## S1 执行入口清单与选型文档

- [x] 穷举可绕开沙盒的执行入口（`adapter.For` 两处、`exec` 四处、出站七处、`computers.session_ref`、CLI 命令推导三路）
- [x] `docs/sandbox-landscape.md` / `_cn.md`：四层 + 平台层对比与选型规则
- [x] `requirements-sandbox.md`：配置 / API / 表 / 声明式模板 / 错误码 / 验收

## S2 领域内核 `internal/sandbox`

- [x] `types.go` 类型、分层、错误码
- [x] `provider.go` Provider 端口（Describe/Probe/Create/Exec/Attach/Destroy/Status）
- [x] `policy.go` 实例默认 + 资源策略合并与校验
- [x] `manager.go` 租约复用键 + 单飞 + 上限 + 审计
- [x] `sweeper.go` 单飞节拍 + kick 合并（对标 `router/passes.go`）
- [x] `bus.go` 事件扇出 + since 回放（对标 `router/bus.go`）
- [x] `service.go` 门面（Run/Stop/Kick/Live/Stats/Catalog/Subscribe/EventsSince/MaxSeq）
- [x] `store.go` + `memory.go` 端口与内存实现

## S3 提供方与目录

- [x] `registry.go` 注册表 + 30s 探测缓存 + 单飞探测
- [x] `mock.go` 确定性离线（不执行，回执标注）
- [x] `docker.go` docker/podman 提供方，`Runtime` 参数承载 gvisor / kata
- [x] `bwrap.go` bubblewrap 参数装配
- [x] `local.go` 受限本地执行（无隔离，如实标注）
- [x] `declarative.go` 模板解析 / 占位符白名单 / 逐 argv 展开 / JSON 回执
- [x] `presets.go` nsjail / isolate / systemd-run / nerdctl / 平台层五种
- [x] `remote.go` 远端节点提供方（投递 job、等待结果、Attach 帧收发）
- [x] `exec.go` 外部进程执行基元（超时 / 截断 / 环境白名单）

## S4 持久化

- [x] `migrations/021_sandboxes.sql`（五表 + computers 新列，幂等）
- [x] `internal/model/sandbox.go`
- [x] `internal/store/sandbox_store.go`（GORM）
- [x] `internal/store/computers_store.go` 新列 + Update / Terminate
- [x] `store_test.go` 的 `want` 追加 `021_sandboxes.sql`

## S5 执行面收口

- [x] `adapter.Options.Sandbox` 注入；`For` 传递
- [x] CLI：删除 prompt 推导；命令来自登记；参数按策略；沙盒内执行；真实回执
- [x] MCP stdio：`Attach` 在沙盒内起进程，`initialize` / `tools/list` / `tools/call`，白名单生效
- [x] agent：出网走 `netutil.SafeClient`，卡片返回的 url 二次校验，`credential_env` 注入
- [x] `hub.SendAs` + `FlowContext.OwnerID/Scope`；`router.HubExecutor` 改用 `SendAs`

## S6 控制面

- [x] `handlers/sandboxes.go` / `sandbox_providers.go` / `sandbox_nodes.go` / `sandbox_events.go`
- [x] `handlers/computers.go` 申请租约 / 终止 / 试跑
- [x] `handlers.go` Deps + 路由；`listen.go` + `listen_test.go` 同步
- [x] `config.go` 十三个开关 + 交叉校验
- [x] `main.go` 装配、清扫、停止顺序

## S7 桌面端执行节点（agentbuddy）

- [x] Rust：`executor/{mod,policy,spawn,jobs,tests}.rs`
- [x] 配置项、启动接线、`/__shell/executor` 状态端点
- [x] 长轮询 + 心跳 + 退避 + 并发上限

## S8 控制台

- [x] agentbuddy `ComputersView` 升级为执行环境控制台（目录 / 水位 / 租约 / 审计）
- [x] 设置页执行节点区块
- [x] `sandbox-copy.js` 13 语；`api.js` / `errors.js` 登记
- [x] agenthub `ResourceKindForm` 沙盒策略区块 + 错误码 13 语
- [x] `/docs` 沙盒分组（13 语，`docs-content.js` 生成）

## S9 测试与文档

- [x] Go：`internal/sandbox/sandbox_test.go`（复用 / 作用域 / 匿名不复用 / 上限 / 审计 / 终止 / 续租上限 /
  清扫 / 目录原因 / 重复注册 / 统计 / 事件回放 / 任务队列）
- [x] Go：`tests/sandbox_e2e_test.go`（控制台端点、空命令拒绝、未知租约 404、他人租约不可见、未接线 503、
  节点注册→领取→回报、跨节点回报 404、SSE 回放）
- [x] Vitest：`views.spec.js` 沙盒屏 9 条（水位 / 目录 / 空态 / `feature_unavailable` / 终止确认 / 试跑与
  拒绝 / 申请租约与拒绝）+ 设置页执行节点 4 条；`docs-content.spec.js` 钉住 13 语文档分组；单色守卫
- [x] cargo：策略拒绝 / 越界路径 / 超时 / 截断
- [x] README / README_cn / releasenotes / releasenotes_cn / AGENTS 同步；`docs/docs*.md` 由 `make docs-md` 重生成

## 取舍记录

1. **远端队列在进程内**：`sandbox_jobs` 只在完成后落库做审计，活跃队列不落库。理由是节点连接本身是
   易失的，跨重启的排队会带来「幽灵任务」；单二进制部署下进程内队列语义更诚实。
2. **gvisor / kata 不做独立实现**：它们是 docker 的 `--runtime` 变体，独立实现会分叉行为。
   目录里仍作为独立条目出现，因为运维是按名字选的。
3. **`local` 保留**：没有容器运行时也没有 bwrap 的开发机需要一个可跑的选项，但它把
   `isolation` 标为 `none`，绝不假装自己是沙盒。
4. **匿名不复用租约**：避免评估报告点名的「不点名资源即匿名可用」被放大成「匿名长驻容器」。
5. **终止后的租约仍可寻址**：`GET /api/sandboxes/{id}` 返回 `state=terminated` 而不是 404。控制台的审计
   抽屉正停在这条租约上，「刚才那次执行干了什么」必须还能回答；只有从未存在过的 id 才是 404。
6. **空命令在 handler 即拒**：`" "` 这类命令在 HTTP 层 `TrimSpace` 后为空就返回
   `sandbox_policy_denied`，不让 provider 收到一个「看起来合法」的请求。
7. **`JobQueue.Enqueue` 默认置为 queued**：调用方忘记写 `State` 会让任务永远躺在一个 `Claim` 看不见的
   位置，节点一直长轮询而工作不动。「入队即排队」由队列自己保证。
8. **`/__shell/executor` 的失败同时返回 `code` 与 `reason`**：工作台的 `ApiError` 只读 `code`，桌面外壳
   必须遵守同一条「不返回散文、只返回机器码」的约定，否则拒绝原因会在前端丢掉。
