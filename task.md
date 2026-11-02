# task.md — agenthub（A2A 互连平台）实施清单

> 由 `requirements.md` 生成的分步清单（M1 已全部落地）。
> 统一验收基线：`go vet ./...` 无告警、`go test -race ./...` 全绿、`bunx vitest run` 全绿、
> `make build` 产出前端产物与二进制。
>
> 子领域实施记录：安全加固 `task-security.md`、执行沙盒 `task-sandbox.md`、
> **数据隔离与全 API 审计 `task-isolation.md`**（方案 `requirements-isolation.md`）。

## M1 — A2A 互连平台首版（已完成）

### 1. 工程骨架

1. [x] `go.mod`（module `agenthub`；生产依赖仅 GORM + postgres 驱动）、`.gitignore`、
   `.env.example`（端口 3500、`DATABASE_URL` 必填）
2. [x] `Makefile`：`run/build/test/test-integration/vet/fmt/db-up/db-down/db-psql/`
   `web-install/web-dev/web-build/web-test/clean`
3. [x] `internal/config`：`LoadDotEnv`（向上 6 层、只补未设置键）+ `Load()` + `Validate()`
   （`DATABASE_URL` 必填、端口/env/并行数/超时/输出上限/事件缓冲/回调保留上限校验；
   `prod` 无 token 拒绝开放模式）
4. [x] `internal/httputil`：`WriteJSON/WriteError/WriteErr/Decode` + SSE
   （`Stream/WriteSSE/WriteRawSSE/Comment/KeepAlive`）
5. [x] `cmd/agenthub-server`：启动顺序 config → `store.Open` → `Migrate` → `Seed` →
   registry/hub → handlers → `http.Server`（`ReadHeaderTimeout: 30s`）→
   信号优雅退出（15s）→ `Hub.Close()` → `store.Close`；全程 `slog` JSON
6. [x] `README_cn.md` / `README.md`：概念地图、持久化表清单、Docker 部署、A2A 端点、冒烟 curl、
   flow 原语、错误码与范围边界

### 2. 协议层（`internal/a2a`，冻结契约）

7. [x] `types.go`：`TaskState`/`States`/`IsTerminal`/`Valid`、`Part`（text/file/data）、
   `Message`、`TaskStatus`/`Artifact`/`Task`/`NewTask`、流式信封、AgentCard 相关类型
8. [x] `jsonrpc.go`：JSON-RPC 2.0 信封、标准码 + A2A 自定义码（`-32001`…`-32008`），
   `error.message` 即 code
9. [x] `methods.go`：方法名常量 + `Canonical(method, legacy)` v0.1 别名映射 + 各 Params 类型

### 3. 资源层（`internal/registry`）

10. [x] `registry.go`：`Kind`/`Health`/`Transport` 枚举、`Entry`（Registry 级字段）、
    `Upsert/Register/Get/Delete/List/SetHealth` + 计数 + `Snapshot`（`RWMutex`）；
    新增 `ResourceStore` 端口，注入后全部方法委托 PostgreSQL（`NewWithStore`），
    未注入时保持内存语义；公开签名零变更
11. [x] `export.go`：聚合 Agent Card（每资源一个 skill）+ MCP server 描述导出

### 4. 适配层（`internal/adapter`）

13. [x] `connector.go`：`Connector` 接口 + `Options` + `For(kind)` 工厂 + 适配器错误码
14. [x] `mock.go`：确定性基元（`digest`/`pick`/`truncate`/`artifact`/`boundedContext`/`metaString`）
    + `MockProviderName` 溯源戳（离线产物一律标注 `provider: mock`）；原 `mockHealth` 已删除
15. [x] `model.go`：`ModelProvider` + `ModelRouter`（provider 注册表 + 回退链）
16. [x] `agent.go`：规划步骤 + **词级**审批判定（`approve`/`confirm`/`approval`；`approved` 不重挂）
17. [x] `mcp.go`：传输无关的 `tools/list` + `tools/call` 协议转换
18. [x] `skill.go`：触发 → 手册 → 步骤 → 报告
19. [x] `cli.go`：白名单 → 超时 → 截断 → 结构化 JSON 回执（含 `truncated` 标记）
19.1 [x] `probe.go`：健康实测——`probeHTTP`（2xx healthy / 401·403 degraded / 其它 unhealthy /
     连不上报 error）+ 六类 `Health`（model `/models` → 必要时 1 token completion；agent 抓 Agent Card，
     200 但非 card 记 degraded；mcp 发 `initialize`，stdio 记 unknown；skill/custom 取 endpoint；
     cli 只做 `PATH` 查找，缺失 unhealthy、不在 allowlist degraded）；`orchestrator.go` 的 invoker
     按调用结果落健康；无可访问目标一律 `unknown`

### 5. 编排层（`internal/hub`）

20. [x] `task.go`：`TaskStore`（Create/Get/List/SetState/AddArtifact/AppendHistory/
    SetPending/Pending/SetPush/Push/Spans）+ `CanTransition` 合法迁移校验
21. [x] `trace.go`：span 时间线（`StartSpan`/`FinishSpan`，含耗时、状态、错误 code）
22. [x] `broker.go`：订阅/channel/退订 + 扇出；慢消费者**丢弃最旧帧**不阻塞运行
23. [x] `flow.go`：`then/branch/parallel` + `MatchCondition`（`""`/`always`/`contains:`/`kind:`/`has:`）
    + `ParseSteps` + `DefaultSteps`；并行数受 `MaxParallel` 截断
24. [x] `orchestrator.go`：`Send/Stream/Get/Cancel/Resume/SetPush/GetPush` + 资源选择 +
    artifact 累积 + 事件发布 + `MockNotifier`（`Repo` 注入时落 `push_records`，失败回退内存；
    `Records()` 优先读库；`Prune` ticker 按 `PushRecordLimit` 保留）+ `Hub.Events`/`Hub.Close`

### 6. 接入层（`internal/handlers`）

25. [x] `handlers.go`：路由注册中心（well-known、`/a2a/agent-card`、`POST /a2a`、
    `/a2a/stream`、`/api/*`、`/healthz`、静态回退）+ `requireAdmin`
26. [x] `a2a.go`：Agent Card、JSON-RPC 分派（七方法 + 别名）、`streamTask`（SSE，无 `[DONE]`）
27. [x] `api.go`：资源 CRUD/health、任务列表/详情/spans/事件回放/cancel/resume、导出端点
28. [x] `writeDomainErr`：领域码 → HTTP 状态码映射（400/404，避免退化为 500）

### 7. 控制台（`web/`）

29. [x] 脚手架：Vite + Vue 3 + Pinia，`vite.config.js` 代理到 3500，产物输出到 `backend/static`
30. [x] `style.css`：单色 token（背景三层 / 描边三级 / 文字三级 / `--grade-*` / 银白 `--sheen`）
    + light 主题 + RTL + `prefers-reduced-motion`
31. [x] `api.js` / `store.js` / `router.js` / `App.vue`（5s 轮询任务）+ 组件
    （`Sidebar`/`Topbar`/`LanguageSwitcher`/`ThemeToggle`/`StateDot`）
32. [x] 三屏：`RegistryView`（筛选 + 卡片 + 抽屉 + **试跑** 跳任务追踪）、
    `TaskTraceView`（列表 + span 时间线 + artifact 原文 + 恢复框）、
    `TopologyView`（五泳道 + 调用边 + 图例 + 详情）
33. [x] `errors.js` + `copy/errors-copy.js`：机器可读 code → 本地化文案，缺失 key 显示 code 本身
33.1 [x] **文档屏 `/docs`**（侧边栏「管理」之后，无权限门控）：
     `lib/docs-content.js`（语言中立：`/a2a` 七方法 + v0.1 别名 + Message/Task 形状、
     **132 条 HTTP 路由**（17 个分组）的方法/路径/权限/请求体/响应体、错误码按状态码分组、适配器码）+
     `lib/copy/docs-copy.js`（仅散文，13 语 × 120 键）+ `views/DocsView.vue`
     （sticky 章节索引 + 滚动高亮、概念/任务状态机/健康图例、端点表就地展开示例、
     鉴权与错误码、页头 `version@commit`）；`use-copy.js` 登记 `docs` 命名空间、
     `shell-copy.js` 补 13 语 `navDocs`、`router.js` 加无 `meta.permissions` 的 `/docs`、
     `copy-fallback.spec.js` 的 `PROSE` 纳入 `docs`
     —— 机器数据进 `docs-content.js` 而非 copy 表，因为该测试断言每个键的非英译文
     异于英文；CRUD 行用「动词模板 + 名词」，HTTP 原因短语原样呈现

### 8. 多语言（13 语）

34. [x] `languages.js`：13 语清单 + `isRTL`/`matchLang`/`hreflangFor`/`ogLocaleFor`（对齐 ariacompute.cn）
35. [x] `use-copy.js`：`resolve` 浅合并 + 英文兜底、语言持久化（`?lang=`/localStorage/`matchLang`）、
    `dir`/`lang` 同步、`enumLabel`
36. [x] 五份 copy 表 ×13 语：`shell` / `registry` / `trace` / `topology` / `enums`（+ `errors`）
37. [x] `LanguageSwitcher.vue` + 字体回退（ar/hi/ru/ja/ko）

### 9. 持久化（PostgreSQL + GORM）

38. [x] `internal/model`：GORM 模型 `Resource`/`Task`/`TaskArtifact`/`TaskSpan`/`TaskPending`/
    `TaskPush`/`PushRecord`/`Event`/`SchemaMigration` + 自实现 `JSONField`（`jsonb`）
39. [x] `migrations/`：`001_resources.sql` / `002_tasks.sql` / `003_events.sql` /
    `004_push_records.sql`（幂等）+ `embed.go`（`//go:embed *.sql`）
40. [x] `internal/store`：`db.go`（`Open`/`Ping`/`Close`/`RedactDSN` + 连接池）、
    `migrate.go`（`schema_migrations` 按序单事务执行）、`seed.go`（`ON CONFLICT DO NOTHING`）、
    `resource_store.go` / `task_store.go`（条件更新）/ `event_store.go` / `push_store.go`（含 `Prune`）
41. [x] `internal/hub/store.go`：`TaskRepository`/`EventRepository`/`PushRepository` 端口 +
    `Persistence`；`task.go`/`trace.go`/`broker.go`/`orchestrator.go` 委托，
    `broker.Start` 有界队列 + 异步 writer（失败只告警、绝不回压）
42. [x] `internal/config`：`DataFile` → `DatabaseURL`（**必填**，缺失即拒绝启动）+ `PushRecordLimit`
43. [x] 部署：`Dockerfile`（`COPY go.sum` + `COPY migrations`、删数据目录）、
    `docker-compose.yml`（`postgres:16-alpine` 宿主 **5434** + healthcheck + `depends_on`）、
    `.env.example`、`Makefile`（`db-up`/`db-down`/`db-psql`/`test-integration`）
44. [x] `handlers`：`/healthz` 增加 `database` 字段（库不可达 503）；
    `/api/tasks/{id}/events?since=<seq>` 走持久化回放

### 10. 测试与文档收口

45. [x] 包级单测（正常 + 异常）：`a2a`、`config`、`registry`、`adapter`、`hub`、`model`、
    `store`、`handlers`、`httputil`；`hub/persistence_test.go` 用 stub 覆盖
    seq 续接 / 队列满丢弃 / Close drain / 回调落库与内存回退 / 持久层失败不静默
46. [x] `tests/e2e_test.go`：httptest 真实 HTTP 端到端（内存模式）——Agent Card + healthz、
    五类适配器各跑通、get/cancel、`input-required` → `tasks/resume` 恢复、流式终态帧与错误帧、
    flow 分支 + 并行、v0.1 别名、pushNotification 计数、控制面注册 → 调用 → 删除
47. [x] `tests/integration_test.go`（`DATABASE_URL` 门控，未设置则 skip）：迁移幂等、
    资源 CRUD 与 jsonb 往返、任务全生命周期 + 级联删除、事件回放、
    push 留痕 + 保留上限、完整 A2A run 落库回读
48. [x] 前端 `web/tests/`：`languages` / `copy-fallback` / `enums-copy` / `api` / `style`（5 文件 22 用例）
49. [x] 验收：`go vet ./...` 通过、`go test -race ./...` 全绿（含真实 PostgreSQL 跑通的集成用例）、
    `bunx vitest run` 22 passed、`make build` 产出 `backend/static` 与 `bin/agenthub-server`
50. [x] 文档同步：`AGENTS.md` / `requirements.md` / 本 `task.md` / `README.md` / `README_cn.md`

## M2 — 目录治理与注册中心互连（已完成）

对照 AWS Agent Registry（对比落在 README 的「AWS Agent Registry 特性对比」章）补齐目录侧能力：
per-kind 注册配置、Model router、上游 MCP/Skill Registry 同步、审批生命周期、
多命名空间、`name + version` 去重启用键与 `custom` 类型。

### 1. 对比文档

51. [x] README / README_cn 的「AWS Agent Registry 特性对比」章：两级模型 / recordType / 协议 schema 校验 /
     `name + recordVersion` 唯一键 / 四角色与审批生命周期 / 发现方式 / 授权 / tags /
     EventBridge·CloudTrail·Organizations·RAM·PrivateLink 逐项对比；三张清单
     （已有 / 本次补齐 / 明确不做）+ recordType ↔ Kind 字段级映射 + 剩余差距优先级

### 2. 领域模型（`internal/registry`，零外部依赖）

52. [x] `kinds.go`：五类 + `custom` 的 per-kind 配置（`ModelConfig`/`AgentConfig`/`MCPConfig`/
     `SkillConfig`/`CLIConfig`/`CustomConfig`）与 `KindConfig` 编解码
53. [x] `validate.go`：`ValidateKind(e)` 按 Kind 分发校验，产出 `invalid_kind_config` 等机器可读码
54. [x] `lifecycle.go`：`Status`（draft/pending/approved/rejected/deprecated）+ `CanTransition`
     合法迁移表 + `Discoverable`（消费方可见性唯一出口，只放行 approved）
55. [x] `namespace.go`：命名空间实体 + `(namespace, name, version)` 去重启用键规则；
     `Entry` 增量扩展（`Namespace`/`Status`/`DisplayName`/`SourceID`/`KindConfig`，全部 `omitempty`）
56. [x] `source.go` / `sync.go`：`RegistrySource` + `Client`/`SourceStore` 端口 + 同步编排
     （单飞 + 拉取 + 解析为 `Entry` + 去重 upsert + `last_sync_*` 回写）
57. [x] `sync_fixture.go`（离线确定性）/ `sync_http.go`（真实 `net/http`：超时 + 限长 +
     鉴权头 + 条目上限，`TODO(real)` 标注协议适配点）
58. [x] `export.go`：MCP server 描述与 Agent Card 只导出 approved，带 namespace 与 kind_config 摘要

### 3. 持久化（迁移 + GORM）

59. [x] `008_registry_governance.sql`：`resources` 治理列（空值回填 approved/default）+ 唯一索引
     `idx_resources_dedup` + `registries` 表（含 default 种子）+ 三个新权限与 admin 关联
60. [x] `009_registry_sync.sql`：`registry_sources`（含 `last_sync_at/status/count/error`）；
     全部 `IF NOT EXISTS` / `ON CONFLICT DO NOTHING`
61. [x] `internal/model`：`Resource` 增列 + `Registry` / `RegistrySource` 模型（复用 `JSONField` jsonb）
62. [x] `store/resource_store.go`：新列往返 + 唯一键冲突返回 `ErrDuplicate` + namespace/status 过滤
63. [x] `store/registry_store.go`：`NamespaceStore` / `SourceStore` 的 GORM 实现（`MarkSync` 只改同步列）

### 4. Model router（`internal/adapter`）

64. [x] `model_openai.go`：OpenAI 兼容 provider（chat completions，`api_key_env` 只取环境变量名，
     限长 + 状态码分类错误码），`TODO(real)` 标注 streaming 边界
65. [x] `model.go`：按 `KindConfig.ModelConfig` 装配 `ModelRouter`（主 provider + 有序回退链 + 权重 +
     超时 + 可选 mock 兜底）；`GenerateWith` 返回命中 provider 名并写进 artifact
66. [x] `model_router_test.go`：URL 拼装、mock 离线、回退链顺序、全失败不伪造、401/畸形响应、凭据取 env

### 5. 控制面（`internal/handlers` + `authz` + `config`）

67. [x] `authz`：`registries:manage` / `registry:curate` / `sources:manage` 进 `AllPermissions`
68. [x] `api_registry.go`：命名空间 CRUD + `POST /api/resources/{id}/status` 审批动作 + 待审批队列
69. [x] `api_sync.go`：上游源 CRUD + `POST /api/registry-sources/{id}/sync`（上游失败 502 透传上游 code）
70. [x] `api.go`：`ListFiltered(Filter{Kind,Query,Namespace,Status})`；`writeDomainErr` 补齐
     `invalid_kind_config` / `invalid_status_transition` / `invalid_namespace` / `invalid_source` → 400、
     `sync_in_flight` → 409
71. [x] `hub/orchestrator.go`：A2A 寻址走 `registry.Discoverable`，未批准记录不可被调用
72. [x] `config`：`AGENTHUB_REGISTRY_SYNC_ENABLED` / `_INTERVAL` / `_FIXTURE` /
     `AGENTHUB_MODEL_ROUTER_MODE`（`mock`|`openai_compatible`）+ `Validate`；`main.go` 装配同步 ticker

### 6. 控制台（`web/`）

73. [x] `ResourceKindForm.vue` + `EnvEditor.vue`：六类专属配置区块（Model router 模式 / Agent Card /
     MCP transport 分流 / Skill 步骤与文档 / CLI 白名单 / custom JSON schema）
74. [x] `RegistryView.vue`：kind 分段选择 + 命名空间与状态筛选 + 审批状态徽标与状态机动作
75. [x] `RegistriesView.vue`（命名空间 + 待审批队列）/ `SourcesView.vue`（上游源 + 手动同步与结果计数）
76. [x] copy 与枚举：`registry-copy.js` 58 key、`registries-copy.js` 23 key、`sources-copy.js` 25 key，
     13 语齐全；`enums-copy.js` 增 `custom` / `status` / `providerMode` / `sourceAuth` / `syncStatus`
77. [x] `api.js` / `store.js` / `router.js` / `Sidebar.vue`：新接口、新 state 与 action、新路由与导航入口

### 7. 测试与文档收口

78. [x] `tests/registry_governance_integration_test.go`（`DATABASE_URL` 门控）：命名空间 CRUD、
     治理列 jsonb 往返、去重启用键冲突、源 CRUD 与同步留痕、facade 与 store 行为一致
79. [x] `internal/handlers/api_governance_test.go`：命名空间、审批两步行走、草稿不泄漏到导出、
     待审批队列、源创建/同步/二次同步/上游失败 502、列表按 namespace 与 status 过滤
80. [x] 验收：`go vet ./...` 通过、`go test -race ./...` 全绿（含真实 PostgreSQL 集成用例）、
     `bunx vitest run` 57 passed、`bun run build` 通过
81. [x] 文档同步：`AGENTS.md` / `requirements.md` / 本 `task.md` / `README.md` / `README_cn.md`

### 8. 覆盖率补齐（一轮专项）

82. [x] `internal/registry/kinds_test.go`：六类 per-kind 校验的**完整非法矩阵**（每类每个字段一条）、
     空/缺失区块仍合法、effective 字段回退链（endpoint / model id / transport / provider mode）、
     `KindConfigSummary` 日志安全（不打印凭据名）
83. [x] `internal/registry/store_mode_test.go`：包内假 store（resource / namespace / source）
     驱动**双模分支**——去重启用键、审批状态机、`Register` 重名 id、`FindByKey`、
     持久化错误归一（`ErrNotFound`/`ErrDuplicate`/其他 → 内部错误）、计数与列表兜底、
     命名空间与源走 store 的增删改与 `MarkSync`
84. [x] `internal/registry/sync_http_test.go`：上游客户端的**不可信输入**分支（非 JSON / 未知形状 /
     非 2xx / 超长体 / 畸形 URL / 上下文取消 / 缺凭据不出请求）、`decodeRecords` 全部已知包裹键、
     `entryFromRecord` 的 MCP 与 Skill 两种形态与嵌套 repository / 工具对象
85. [x] `internal/handlers/api_governance_more_test.go`：命名空间与源的改删路径、
     无同步服务时全部 404、列表过滤非法参数 400、三个新权限**互不蕴含**（逐个 403/200 断言）、
     审批接口的畸形体与不存在的边、`deprecated` 移出可发现目录
86. [x] `internal/store/registry_governance_test.go`（`DATABASE_URL` 门控）：治理列与 `kind_config`
     jsonb 真正落库往返、唯一索引冲突、命名空间 CRUD（含 `auto_approve=false` 的零值陷阱）、
     源 CRUD 与同步结果留痕
87. [x] `internal/a2a/jsonrpc_test.go`：`NewResponse` / `WriteResult` / `WriteRPCError` 的状态映射
     （解析与无效请求 400、未知方法 404、其余 200）/ `WriteRPCErr` 归一 / `NewDataPart` / 推送配置别名
88. [x] `internal/auth/oauth_test.go`：仅提供已配置且启用的提供方、`BeginOAuth` 携带 PKCE 与 state
     且不泄露密钥、未配置提供方的回调拒绝、cookie 标志（HttpOnly / Lax / Secure）、
     无 store 时服务保持惰性
89. [x] `internal/authz/principal_test.go`：dev 与匿名两个内置 principal 的权限边界、
     权限词表封闭且无重复、无 store 的惰性服务与可见性
90. [x] `internal/model/model_test.go`：24 张表的 `TableName` 全量断言（含新增两张）+
     `Resource` 治理列与 jsonb 载荷
91. [x] `internal/config/config_test.go`：同步开关与 router 模式的默认值与校验边界、
     环境解析失败回落、`BackupSchedule` 透传
92. [x] `internal/backup/service_accessors_test.go`：`Enabled`/`EngineName`/`Dir`/`Quiesce`、
     下载前校验 checksum（篡改 → 不匹配）、恢复确认、越界路径拒绝、调度只播种一次
93. [x] `make test-cover`：`go test -coverprofile` + `go tool cover -func` 的一键覆盖率入口，
     `coverage.out` 进 `.gitignore` 与 `make clean`

## M2.1 — agentbuddy 协作会话端点（已完成）

为 agentbuddy 桌面客户端（独立仓库，自身无后端，指向 agenthub 服务端）新增多 agent 协作能力。
沿用既有 `resources:read` / `tasks:read` / `tasks:write` 权限，不新增角色或用户组；线程归单一主体
所有，消息中的 `@mention` 经 `Hub.Send` 走 A2A 委派给对应 agent，并以任务 id 关联，实时回复经 SSE 透传。

### 1. 端点（`internal/handlers`）
101. [x] `handlers.go`：`Deps` 新增 `Threads threads.Store`；追加 7 条路由（workspace 用 `resources:read`，
    threads 用 `tasks:read` / `tasks:write`），沿用 `requirePermission` 包装器，不改既有路由
102. [x] `workspace.go`：聚合视图，`visibleEntries(r, Reg.List(agent, ""))` 取可见 agent + `Hub.Store().List()` 近期任务
103. [x] `threads.go`：`handleListThreads` / `handleCreateThread` / `handleGetThread` /
    `handlePostThreadMessage`（解析 `@mention` → `Hub.Send` 建任务并 `UpsertAgentMessage` 占位）/
    `handleThreadEventStream`（SSE：回访历史 + 订阅 `Broker` 跟随被委派任务，经 `UpsertAgentMessage` 落 agent 回复）
104. [x] `threads_test.go`：`taskIDsOf` 单测

### 2. 领域与持久化（依赖倒置）
105. [x] `internal/threads/types.go`：`Thread` / `Message` 类型 + `Store` 端口 + `ErrNotFound`（端口定义在领域包，
    与 `registry.ResourceStore` 同款模式，`internal/threads` 不 import GORM）
106. [x] `internal/store/threads_store.go`：`NewThreadsStore(db)` 的 GORM 实现；`UpsertAgentMessage` 以
    `task_id` 为键 upsert，agent 消息首建以 `resource_id` 写入
107. [x] `migrations/012_threads.sql`：幂等 `threads` + `thread_messages`（`CREATE TABLE IF NOT EXISTS` + `ON DELETE CASCADE`）

### 3. 装配与文档
108. [x] `cmd/agenthub-server/main.go`：`threadsStore := store.NewThreadsStore(db)` 注入 `deps.Threads`
109. [x] `AGENTS.md`：「agentbuddy 接入」章记录端点、权限复用、迁移与桌面壳前缀白名单
    （`/api/workspace`、`/api/threads` 已被 `is_remote_path` 覆盖，新增服务端端点无需改桌面端代码）

### 4. 验证
110. [x] `go build ./...`、`go vet`、全量 `go test ./...` 绿；`store_test.go` 迁移顺序测试补 `012_threads.sql`

## M2.2 — agentbuddy 通道 / 连接器 / 语音 / 形象（已完成）

为 agentbuddy 桌面客户端补齐四类服务端能力：外部通道入站（Slack/Teams/Telegram/SMS）、
外部应用连接器审批网关、语音转写与合成、资源形象（`Persona`）。全部复用既有权限
（`tasks:read`/`tasks:write`/`resources:read`/`resources:write`），不新增角色或用户组。

### 1. 迁移（幂等，全量重放）
111. [x] `migrations/013_persona.sql`：`ALTER TABLE resources ADD COLUMN IF NOT EXISTS persona JSONB;`
112. [x] `migrations/014_channels.sql`：`channels` + `channel_messages`（`external_ref` 唯一键幂等去重），全部 `IF NOT EXISTS` 单行 `;` 结尾
113. [x] `migrations/015_thread_origin.sql`：`ALTER TABLE thread_messages ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT '';`（通道与语音共用）
114. [x] `store_test.go` 迁移顺序测试追加 `013/014/015`

### 2. Persona（编号 183–374）
115. [x] `internal/registry/registry.go`：`Entry.Persona *Persona` + `Persona{DisplayName,Title,Tagline,AvatarURL}`；
    `normalizePersona`（trim + 长度上限 + `AvatarURL` 仅收 http/https，非法丢弃不报错），`Upsert` 值拷贝自动透传
116. [x] `internal/model/model.go`：`Resource.Persona JSONField`（jsonb）
117. [x] `internal/store/resource_store.go`：3 处（`resourceUpdateColumns`、`resourceFromEntry`、`entryFromResource`）映射 persona

### 3. Channels
118. [x] `internal/channels/types.go`：领域类型 + `Store` 端口（`List/Get/Create/Update/Delete/RecordDelivery/ListDeliveries`），`SigningKeyEnv` 仅存环境变量名
119. [x] `internal/channels/verify.go`：`Verify(kind, body, r, signingKeyEnv)` 按 kind 选 Slack(`X-Slack-Signature` v0 HMAC)、HMAC(`v0:ts:body`)、Telegram(`X-Telegram-Bot-Api-Secret-Token`)；5 分钟重放窗；常量时间比较；错误码 `CodeChannel*`
120. [x] `internal/store/channels_store.go`：`NewChannelsStore(db)` 实现 `channels.Store`；`RecordDelivery` 用 `OnConflict{DoNothing}` 返回 `(false,nil)` 去重
121. [x] `internal/handlers/channels.go`：`GET/POST /api/channels`、`GET/PUT/DELETE /api/channels/{id}`、`GET /api/channels/{id}/deliveries`（`tasks:read`/`tasks:write`）；`POST /api/channels/{id}/webhook` **public**，走签名校验 + `external_ref` 去重 + `mentionsOf` → `Hub.Send` 委派进 thread；`channelsReady` 在禁用/无 store 时返 503 `CodeChannelDisabled`
122. [x] `internal/handlers/listen.go` + `listen_test.go`：`Listeners()` 与 `probes` 同步新端点（含 public webhook）

### 4. Connectors 审批网关
123. [x] `internal/hub/orchestrator.go`：`Hub.ParkConnector(ctx, resourceID, action)` 建任务并置 `input-required` + `SetPending(task.ID, "approved")`
124. [x] `internal/handlers/connectors.go`：`GET /api/connectors`（`resources:read`）、`PUT /api/connectors/{id}/policy`、`POST /api/connectors/{id}/actions`、`GET /api/connectors/{id}/actions`（`resources:write`/`read`）；off→403 `CodeConnectorPolicyOff`、ask-first→`ParkConnector` 202、allowed→`Hub.Send` 200

### 5. Speech / 语音
125. [x] `internal/adapter/speech.go`：确定性 mock STT/TTS（无网络/子进程），`Transcribe`(crc32 派生文本)/`Synthesize`(8kHz 单声道 16-bit WAV)；真实对接标 `TODO(real)`
126. [x] `internal/config/config.go`：新增 `ChannelsEnabled`/`SpeechMode`/`AudioMaxBytes`（`Load` 默认 `16<<20`，`Validate` 对 0 兜底为默认）；`AGENTHUB_CHANNELS_ENABLED`/`AGENTHUB_SPEECH_MODE`/`AGENTHUB_AUDIO_MAX_BYTES`
127. [x] `internal/handlers/speech.go`：`POST /api/threads/{id}/audio`（`tasks:write`）；自建 `http.MaxBytesReader` + `multipart.Reader`(`8<<20`)；STT→`Threads.AddMessage`(`origin='voice'`)→委派→TTS 音频作首个 `PartTypeFile` artifact（`a2a.FileContent.Bytes` 内联 base64）
128. [x] `internal/store/threads_store.go`：`thread_messages.origin` 列读写（添加消息时透传 `m.Origin`）

### 6. 装配与验证
129. [x] `internal/handlers/handlers.go`：`Deps` 增 `Channels channels.Store`；注册全部新路由
130. [x] `cmd/agenthub-server/main.go`：`channelsStore := store.NewChannelsStore(db)` 注入 `deps.Channels`
131. [x] `go build ./...`、`go vet`、`go test -race ./...` 全绿；`gofmt` 无差异

132. [x] `internal/channels/deliver.go`：`Deliver(ctx, ch, body)` 出站投递——经 `netutil.ValidateURL`(仅 https) + `netutil.SafeClient`(SSRF 安全、不跟随重定向、限长) 把消息 POST 到 `config.target_url`，回显 Provider 原始响应（状态码 + ≤512B 截断体），不重试、不缓冲；失败映射 `CodeDeliveryFailed`。
133. [x] `internal/handlers/channels.go`：新增 `POST /api/channels/{id}/deliver`（`tasks:write`，通道须 `Enabled`），调用 `channels.Deliver` 并把出站结果记为 `direction=outbound` 的 `ChannelMessage`；`handlers.go` 注册该路由，`listen.go`/`listen_test.go` 同步条目与 probe。
134. [x] `internal/handlers/handlers.go` + `speech.go` + `cmd/agenthub-server/main.go`：将语音引擎以 `Deps.Speech adapter.Speech` 注入（修正此前「直接引用 `adapter.DefaultSpeech` 包级变量」的设计偏差）；`main` 注入 `adapter.DefaultSpeech`，handler 改用 `d.Speech`，单测仍可经零值 `Speech{}`（确定性 mock）运行。

## M4 — eMudhra / emSign 证书私钥泄露响应（2026-10）

背景：eMudhra(emSign) 8 张中间 CA 私钥极弱（P-384 私钥标量 <256），可被反推伪造任意域名证书；
Go/Python/Node/Bun/Rust(webpki-roots) 等自带信任库需升级。站点证书（如部署用 Let's Encrypt）不受影响，无需重签。

### 1. Go 后端 / operator
- `go.mod`：`go 1.27` → `go 1.27.3`（强制构建工具链携带 `crypto/x509` 对 8 张 emSign 中间证书的硬编码封禁）。
- `operator/go.mod`：`go 1.27.0` → `go 1.27.3`。
- `Dockerfile`：构建镜像 `golang:1.27-alpine` → `golang:1.27.3-alpine`；运行时 `alpine:3.21` 已自带 `ca-certificates`（出向 TLS 依赖，更新后移除 emSign 链）。
- `operator/Dockerfile`：构建镜像 `golang:1.27-alpine` → `golang:1.27.3-alpine`（运行时 distroless 自带 CA 包，由 Google 维护）。

### 2. 前端 / 开发镜像（Bun）
- `Dockerfile`：`FROM oven/bun:1-alpine AS web-build` → `oven/bun:1.4.4-alpine`（Bun 1.4.4 修复其 TLS 信任库）。
- `docker-compose.yml`：dev `web` 服务 `image: oven/bun:1-alpine` → `oven/bun:1.4.4-alpine`。
- `web/package.json`：新增 `engines.bun >=1.4.4`，约束本地开发运行时。

### 3. 桌面端（Rust / Tauri，reqwest + rustls）
- `desktop/src-tauri/Cargo.toml`：显式固定 `webpki-roots = "1.0.10"`（Rust 信任库在 1.0.10 移除 emSign 根；桌面环回代理 SSE/HTTP 出向校验依赖它）。本仓此前无 `Cargo.lock`，首次构建须解析到 ≥1.0.10。

### 4. 检测与验证
- 新增 `scripts/check-emudhra.sh`：离线按 emSign 根（信任锚）指纹扫描 CA bundle，规避前向/拦截代理造成的在线探测假阳性。
- 构建后核验：`docker run --rm <agenthub-image> scripts/check-emudhra.sh /etc/ssl/certs/ca-certificates.crt`。

## M5 — Agent 能力端点（F10–F16 服务端脚手架，已落地）

背景：配合 agentbuddy `requirements.md` 的 F10–F16 标杆特性分析，agenthub 侧落地服务端契约
（迁移 `017/018/019`、机器码、computers/schedules/knowledge 端点、Listeners/probes/`store_test` 同步）；
真实计算机控制 / 调度引擎 / 向量检索统一标 `TODO(real)`，首版为确定性 mock。不新增权限码
（复用 `resources:*` / `tasks:*`），新端点全部落在 `/api` 前缀下（桌面壳环回代理自动覆盖）。

### 1. 迁移（幂等，接 `016` 之后）
- [x] `migrations/017_computers.sql`：`computers`（agent_id, kind∈browser/shell/screen, session_ref, status, last_action, created_at），`IF NOT EXISTS`。
- [x] `migrations/018_schedules.sql`：`schedules`（goal, trigger, cron_expr, enabled, next_run, owner, last_run_at, last_status）。
- [x] `migrations/019_knowledge.sql`：`knowledge`（agent_id, chunk_text, source_ref, created_at），mock RAG。
- [x] `internal/store/store_test.go` 迁移顺序测试（`want` 切片）追加 `017/018/019`。

### 2. 领域与持久化（依赖倒置）
- [x] `internal/model/computers.go` / `schedules.go` / `knowledge.go`：GORM 模型 + `JSONField`。
- [x] `internal/computers` / `internal/schedules` / `internal/knowledge`：类型 + `Store` 端口（镜像 channels 模式）。
- [x] `internal/store/computers_store.go` / `schedules_store.go` / `knowledge_store.go`：GORM 双模实现（`NewComputersStore` 等）。

### 3. 服务层与端点
- [x] `internal/httputil/json.go`：新增 6 个 `Code*`（computer/schedule/knowledge/skill）；`WriteErr` 映射状态（404/403/400/503）。
- [x] `internal/handlers/computers.go`：`GET/POST /api/computers`、`GET /api/computers/{id}`（mock 适配器）。
- [x] `internal/handlers/schedules.go`：`GET/POST /api/schedules`、`PUT/DELETE /api/schedules/{id}`（mock 调度器复用 hub）。
- [x] `internal/handlers/knowledge.go`：`GET /api/knowledge`（mock RAG 检索 + 技能蒸馏触发点记录）。
- [x] `internal/handlers/handlers.go`：`Deps` 增 `Computers/Schedules/Knowledge`，注册 9 条 `/api` 路由。

### 4. 路由发现同步
- [x] `internal/handlers/listen.go` `Listeners()` 追加 9 个端点；`listen_test.go` `probes` 同步（被 `TestListenersCoverRoutes` 兜底）。

### 5. 装配与验证
- [x] `cmd/agenthub-server/main.go`：注入三个新 store 到 `Deps`。
- [x] `go build ./...`、`go vet`、`go test -race ./...` 全绿（含 `TestEmbeddedMigrationsAreOrderedAndIdempotent`、`TestListenersCoverRoutes`）。

## M6 — 调度器（Router 层，已落地）

背景：把 F13 的 mock 调度器换成真正的双层调度（Router 层判定 + Worker 层执行）。新建 `internal/router`
包，遵守「单一写者 / 状态在事务内、外部动作在提交后 / 命令与事实分离」三条纪律；不实现 Effects 持久队列
与 Outbox（Passes 提交后同步驱动 + 单飞 + 幂等重放替代）；cron 复用 `backup.ParseCron` 而非新增依赖；
开关默认关闭，`Deps.Router == nil` 时全线 503 且线程入口保持原直发路径。不新增权限码。

### 1. 领域与端口（零外部依赖）
- [x] `internal/router/types.go`：`Trigger`/`Disposition`/`Thread`/`Subscription`/`Ticket`/`Assignment`/`Decision`/`Event`/`Stats` + 枚举校验 + `TriggerQuery`/`TicketQuery` + 6 个哨兵错误。
- [x] `internal/router/ports.go`：`Store`/`Assessor`/`Executor`/`Clock` 端口（由消费方定义）。
- [x] `internal/router/memory.go`：内存 `Store`（与 GORM 实现同语义，单测零外部依赖）。

### 2. 持久化
- [x] `internal/model/router.go`：8 张表的 GORM 模型（jsonb 列复用既有 `JSONField`）。
- [x] `migrations/020_router.sql`：`router_triggers`/`threads`/`subscriptions`/`tickets`/`assignments`/`decisions`/`policies`/`events`，全 `IF NOT EXISTS` + `(source, dedupe_key)` 与 `(disposition, next_due)` 索引。
- [x] `internal/store/router_store.go`：`NewRouterStore` 实现 `router.Store`；`store_test.go` 的 `want` 切片追加 `020_router.sql`。

### 3. Engine（单写者）
- [x] `internal/router/engine.go`：`Intake`（去重 + 过滤 + 扇出）/ `EvaluateDue` / `Route` / `Bind` / `Unsubscribe` / `Handback` / `CloseTicket` / `Prune`，只写库并返回待执行意图。
- [x] `internal/router/filter.go`：程序层四来源 × `off|candidate|request_only` × 白名单 × `@提及` × 关键词 × 静默时段 × 自回显抑制。
- [x] `internal/router/assess.go`：`RuleAssessor` + `LLMAssessor`（`adapter.ModelRouter`，严格枚举解析，逐条回退规则）。
- [x] `internal/router/compose.go`：工单简报拼装（正文截断 + 最近 N 条 + 关联 ID）。

### 4. 节拍与执行
- [x] `internal/router/passes.go`：节拍单飞 + `Kick()` 请求合并 + panic 隔离 + 健康计数。
- [x] `internal/router/executor.go`：`HubExecutor`（`hub.Send`/`hub.Resume` + 槽位信号量 + `input-required → waiting` + 退避重试）。
- [x] `internal/router/bus.go`：有界事件扇出（慢订阅者丢最旧帧）+ `since` 回放。
- [x] `internal/router/service.go`：对外门面（`Run`/`Stop`/`Stats`/列表/`Cancel`/`Handback`/`Unsubscribe`/`Kick`/`Policies`/`Inject`/`Subscribe`）。

### 5. 控制面与入口
- [x] `internal/httputil/json.go`：新增 `router_ticket_not_found`/`router_trigger_not_found`/`router_thread_not_found`/`router_ticket_closed`/`router_handback_exhausted`/`router_policy_invalid`/`router_resource_unset` 与状态映射。
- [x] `internal/handlers/router.go`：12 个 `/api/router/*` 端点 + SSE；`routerReady` 闸门返回 503 `feature_unavailable`。
- [x] `internal/handlers/handlers.go`：`Deps.Router` + 注册 12 条路由（`tasks:read`/`tasks:write`）。
- [x] `internal/handlers/listen.go` / `listen_test.go`：`Listeners()` 与 `probes` 同步 12 条（不得 404）。
- [x] `internal/handlers/threads.go` / `channels.go`：`Deps.Router != nil` 时改走 `Intake`，否则保留直发 `Hub.Send`。
- [x] `internal/handlers/schedules.go`：`POST /api/schedules/{id}/run` 立即触发；cron 非法返回 `schedule_invalid`，mock 注释删除。
- [x] `internal/schedules/cron.go`：`NextRun`/`ValidateCron`/`Advance`（复用 `backup.ParseCron`）。

### 6. 装配、配置与验证
- [x] `internal/config/config.go`：10 个 `AGENTHUB_ROUTER_*` 配置项 + `Validate()` 边界（interval>0、batch 1..100、tickets 1..64、attempts 1..10、ttl/prune>0、assess mode 枚举）。
- [x] `cmd/agenthub-server/main.go`：建 Store → `router.NewService` → 条件 `Run(ctx)`；关闭时序为 `Service.Stop()` → `Hub.Close()` → `store.Close`。
- [x] 单测：`engine_test.go`/`filter_test.go`/`assess_test.go`/`passes_test.go`/`executor_test.go`/`cron_test.go`/`router_test.go` + `tests/router_e2e_test.go`（内存端到端）。
- [x] `go build ./...`、`go vet ./...`、`go test ./...` 全绿。

### 7. 文档
- [x] `docs/docs-content.js` + `docs-copy.js`（13 语）：新增「调度器」分组与 14 条端点行、路由错误码；`bun scripts/gen-docs-md.mjs` 重生成 `docs/docs.md` / `docs/docs_cn.md`（**不手改生成物**）。
- [x] `README.md` / `README_cn.md`：「调度器（router）」章（双层职责、两级过滤、订阅直通、节拍、开关与配置）。
- [x] `AGENTS.md`：`internal/router`、`internal/schedules`、`handlers/router.go`、`020_router.sql`、持久化表与错误码映射。
- [x] `requirements.md`：§1.5 范围、§4.2 端点、§5.1 错误码、§10 调度器契约（数据模型/行为/安全/配置/验收）。

## M7 — 收敛项（错误码映射守卫与语音体积上限，已落地）

背景：审查 `internal/httputil` 覆盖率（76.0%）时发现两处隐患——`WriteErr` 的状态映射表没有任何兜底测试，
语音端点又把「超限」与「格式不支持」混答成 400。行为变更仅一处：超限上传由 400 `audio_unsupported`
改为 413 `audio_too_large`。不新增错误码，不新增依赖。

### 1. `WriteErr` 映射穷举守卫
- [x] `internal/httputil/httputil_test.go`：新增 `declaredCodes`，用 `go/parser` 解析 `json.go` 收集全部
  `Code*` 常量；`TestWriteErrMapsCodesToStatus` 的期望表必须与常量集**双向**一致——新增码未登记、
  表格残留已删码、重复登记、期望状态不符，四种情况均失败（已用临时常量验证守卫确实会红）。
- [x] 期望表按 400/401/403/404/405/413/429/500/502/503 分组，并显式把 11 个「由 handler 直接
  `WriteError` 写出、`WriteErr` 不产出」的码钉成 500，将这一降级行为写成文档而非隐患。
- [x] `internal/httputil/json.go`：`Decode` 删除与尾随 `return` 完全相同的 `io.EOF` 分支（连带去掉 `io` 导入）。

### 2. 语音体积上限语义
- [x] `internal/handlers/speech.go`：新增哨兵 `errClipTooLarge` 与 `isBodyTooLarge`；`readFirstFile`
  在 `NextPart` 与 `ReadAll` 两处把 `*http.MaxBytesError` 归一化为超限（`MaxBytesReader` 常在
  multipart 填缓冲时就触发，只判 `ReadAll` 会漏掉整类请求），handler 回 413 `audio_too_large`。
- [x] `internal/handlers/speech_test.go`：拆成 `TestThreadAudioRejectsAnEmptyClip`（400）与
  `TestThreadAudioRejectsAnOversizedClip`（413 + 机器码断言）；上限取 `4<<10`、片段 `8<<10`，
  避免旧用例的 `8` 字节上限把任何 multipart 信封都算作超限。

### 3. 验证
- [x] `go build ./...`、`go vet ./...`、`go test ./...` 全绿。
- [x] `internal/httputil` 覆盖率 76.0% → 91.5%；余下未覆盖仅 `WriteJSON` 的编码失败分支与
  `sse.go` 的 KeepAlive 写错误分支，两者都需构造失败 writer 才能触发。

## M8 — /docs 屏与 docs/*.md 补齐沙盒端点（文档同步，2026-11）

背景：`/docs` 屏已有「执行沙盒」概念章节（`sectionSandbox` + 7 层隔离表），但 HTTP 端点表里
**没有任何 `sandbox*` 行**，通道投递重试 `POST /api/channels/{id}/deliveries/{did}/retry` 也缺失；
错误码表同样停在调度器那一轮，`channel_*` / `connector_*` / `audio_*` / `sandbox_*` 全部未登记。
两者都由 `docs-content.js` + `docs-copy.js` 渲染，因此一次修复即可同时更新 `/docs` 屏与
`docs/docs.md` / `docs/docs_cn.md`。

### 1. 机器数据（`web/src/lib/docs-content.js`）
- [x] `endpointGroups`：`capabilities` 与 `router` 之间新增 `sandbox` 分组（`groupKey: 'groupSandbox'`）。
- [x] `httpEndpoints`：新增 15 条 —— `GET /api/sandboxes`、`GET /api/sandboxes/{id}`、
  `GET /api/sandboxes/stats`、`GET /api/sandboxes/events`、`POST /api/sandboxes/{id}/exec`、
  `POST /api/sandboxes/{id}/renew`、`DELETE /api/sandboxes/{id}`、`GET /api/sandbox-providers`、
  `POST /api/sandbox-providers/refresh`、`GET /api/sandbox-nodes`、`POST /api/sandbox-nodes`、
  `DELETE /api/sandbox-nodes/{id}`、`POST /api/sandbox-nodes/{id}/heartbeat`、
  `GET /api/sandbox-nodes/{id}/jobs`、`POST /api/sandbox-nodes/{id}/jobs/{job}`；权限与请求/响应
  形状逐条对齐 `handlers.go` 与各 handler 的实际读写（`GET|POST jobs` 用 `tasks:write`，
  其余 lease 路由 `resources:read|write`）。
- [x] `httpEndpoints`：补 `POST /api/channels/{id}/deliveries/{did}/retry`（`tasks:write`，无请求体，
  502 `delivery_failed` 后回执仍可重试）。
- [x] `errorGroups`：补齐 `channel_payload_invalid` / `connector_action_denied` / `audio_unsupported` /
  `sandbox_unknown_provider`（400），`channel_signature_*` / `channel_timestamp_expired`（401），
  `channel_disabled` / `connector_policy_off` / `sandbox_disabled` / `sandbox_policy_denied`（403），
  `channel_not_found` / `delivery_not_found` / `connector_not_found` / `sandbox_not_found`（404），
  `delivery_not_retryable` / `sandbox_unsupported` / `sandbox_lease_lost`（409），`audio_too_large`（413），
  `delivery_failed`（502），`speech_disabled` / `sandbox_node_offline`（503）；新增 `415`
  （`audio_unsupported`）、`422`（`audio_transcribe_failed`）、`429`（`account_locked`、
  `sandbox_limit_reached`）、`504`（`sandbox_timeout`）四行。

### 2. 文案（`web/src/lib/copy/docs-copy.js`，13 语）
- [x] 新增 `groupSandbox` / `nounSandboxes` / `nounSandboxProviders` / `nounSandboxNodes`、
  12 个 `epSandbox*` 与 `epChannelRetry`，共 14 键 × 13 语；可复用 `verbList`/`verbGet`/`verbDelete`
  的端点只加名词，避免同一句话译 13 遍。

### 3. 守卫与生成
- [x] `web/tests/docs-content.spec.js`：新增两条 —— 每条 `httpEndpoints` 的 `descKey`/`nounKey`
  在 13 语均可解析；每个 `endpointGroups` 都有标题且至少一条端点、每条端点的 `group` 都已声明。
  缺失端点正是此前静默漏掉 15 行的原因，故把「键能解析」变成断言。
- [x] `bun scripts/gen-docs-md.mjs` 重生成 `docs/docs.md`（en）与 `docs/docs_cn.md`（zh），
  两份文件均出现 `### Execution sandboxes` / `### 执行沙盒` 与 16 条新行；生成物不手改。
- [x] 法语 `epSandboxProviderRefresh` 初稿含 `redémarrage`，被 `style.spec.js` 的色相词守卫
  判为独立单词 `red`（`é` 构成词边界），改写为「sans avoir à relancer le serveur」。
- [x] `bun run test` 17 文件 / 303 用例全绿。

## M3 — 后续项（未开始）

94. [x] 真实适配器落地：model（OpenAI 兼容端点 + 回退链，M2-64/65）、agent（Agent Card + `message/send`）、
     mcp（Streamable HTTP JSON-RPC）、skill（本地手册加载）、cli（租约沙箱内 `exec`，沿用白名单/超时/截断）。
     仍留 `TODO(real)`：MCP **stdio 子进程**、agent 与模型的**流式**输出、speech / custom 的 mock
95. [x] pushNotification 真实回调（`hub.HTTPNotifier`：https-only + SSRF 白名单 + HMAC-SHA256 签名 +
     `(taskId, state)` 一小时去重 + 留痕落 `push_records`；未配置 `AGENTHUB_PUSH_SECRET` 时仍是只留痕的 mock）
96. [ ] 多实例联邦：Agent Network（跨 agenthub 的 Agent Card 聚合与路由）
97. [ ] eval 钩子接评测编排（**仍不产出分数**；缺凭据 skip 并写 reason）
98. [ ] 控制台增强：拓扑时间窗口持久化、任务筛选与分页、artifact 下载
99. [ ] 目录侧剩余项（对照 README「AWS Agent Registry 特性对比」的「剩余差距」）：批量读接口与列表分页、
     语义检索走外部检索源接入、全量 API 调用审计留痕
100. [ ] 覆盖率后续项：`internal/auth` 的 OAuth `Exchange`（端点为常量，需可注入才能离线断言）、
     `internal/backup` 的 `copy.go` / `pgdump.go`（需真实 PostgreSQL 与 `pg_dump` 客户端）

## M4 — 完整度补齐（2026-11-02，对照《agenthub + agentbuddy 完整度评估》§6）

### 1. 沙盒出网语义（D1）
- [x] `sandbox.GrantsNetwork(profile)` 成为「给不给网」的唯一判定：只有 `bridge` / `platform` 返回 true，
      `none` 与 **`egress_allowlist` 都返回 false**。
- [x] `bwrap.go`：`--share-net` 只在 `GrantsNetwork` 为真时出现（此前 `egress_allowlist` 落进给网分支，
      等于把宿主机整个网络交给沙盒）；`Describe().Network` 移除 `NetworkAllowlist`。
- [x] `docker.go`：改用同一判定，`platform` 对齐为守护进程默认网络（此前只有 `bridge` 给网），
      `Describe()` 同样不再宣称 allowlist。
- [x] `types.go` / `requirements-sandbox.md` §2.1：写明词表保留、名单需 egress sidecar（`TODO(real)`）才生效。
- [x] `providers_test.go`：四值 × 两 provider 的参数矩阵 + 「descriptor 不卖自己没有的能力」两条用例；
      `policy_test.go` 补 `GrantsNetwork` 用例。

### 2. RLS 可执行断言（D3）
- [x] 新增 `tests/rls_test.go`：`DATABASE_URL` 未设即 skip；**必须** `store.OpenOptions{RLSEnforce: true}`
      （零值会 `relaxRLS`，否则整份文件假绿）。
- [x] 结构层：8 张表 `ENABLE + FORCE` 与 10 条策略名（`tasks_owner_access` … `audit_log_retention`）逐一断言。
- [x] 行为层：无身份 / 空身份 → 0 行；本人 → 自己的行；他人 → 0 行；`bypass` / `system` → 全量；
      审计表「普通请求不可见、审计员可读不可写、只有 `retention` 能删」。
- [x] `022_task_ownership.sql` 的子表 owner 继承触发器逐表断言（`task_artifacts`/`task_spans`/`task_pending`/
      `task_push`/`events`）；`knowledge` 按 owner 隔离；仓库路径（`identity.With` + `TaskRepository`）闭环。
- [x] `tests/integration_test.go` 的注释从「空承诺」改为指向真实文件与 gating 方式。

### 3. 机器码文案与沙盒策略表单（D5/D6）
- [x] `errors.js` 补 38 个码（channel / delivery / connector / audio / speech / computer / schedule /
      skill / router / sandbox / account_locked / feature_unavailable）；顺带补上此前遗漏的 4 个
      （`invalid_health` / `invalid_transport` / `unknown_kind` / `no_resource_available`），
      现在 **104 个机器码 ↔ 104 个文案键**一一对应，13 语全译。
- [x] `errors.spec.js`：新增「新码可本地化」与「`CODE_KEYS` ↔ `errorsCopy.en` 双向键集一致」两条守卫。
- [x] 新增 `lib/copy/sandbox-copy.js`（13 语，27 键）与 `lib/sandbox-form.js`（纯函数：trim / 数字 / 三态 /
      列表 / 词表校验 / 空对象删除），`ResourceKindForm.vue` 增加「执行沙盒」分区（仅 `agent`/`mcp`/`cli` 渲染；
      `readonly_root` 三态；`network=egress_allowlist` 时直接提示「当前按断网执行」）。
- [x] `docs-content.spec.js` 钉住 132 条端点、`sandbox` 15 条、`router` 14 条、`collaboration` 20 条。
- [x] `sandbox-form.spec.js`：正常路径 + 非法值/清空/词表外取值 + 13 语文案覆盖。

### 4. hub 覆盖率（82.4% → 91.0%）
- [x] `internal/hub/coverage_test.go`：`SendAs`/`StreamAs`、`scopeOf`（ticket/contextID/默认）、
      `ResolveResource`（三种选择键、kind 回退、空注册中心、无注册中心）、`lastUserText`、
      `transitionError`/`isNotFound`、`Events` 仓库失败、`notifyPush` 四重早退、`TaskStore` 读失败降级。
- [x] `HTTPNotifier` 真实投递路径：签名头可由接收端用同一份 body 复算、`(taskId,state)` 去重、
      `Reset` 清缓存、peer 5xx 与传输失败都只留痕、`http://` 目标在拨号前被拒、nonce 随机与
      `markSeen` 的过期清扫。

### 5. Persona 控制台字段（F10 服务端契约）
- [x] `registry.Persona` 增加 `role` / `instruction` / `mbti` / `avatar_ref`（与 agentbuddy 表单字段名一致），
      全部 `omitempty`，老记录序列化不变，无迁移（复用 `resources.persona` jsonb）。
- [x] `normalizePersona`：标签 ≤256、`instruction` ≤4096（`maxInstructionLen`）、MBTI 按 16 型白名单
      （`NormalizeMBTI`，非法值清空而不是入库）、整体为空归 nil；`registry_test.go` 三条用例覆盖。

### 6. 规格回填（D8）
- [x] `requirements.md` §1.1 适配器现状改写成「真实路径 + 回退 mock」；§1.2 范围外移除已落地的
      真实 CLI 与 MCP Streamable HTTP，只留 stdio 子进程、流式输出与 speech/custom 的 mock；
      §1.4 Persona 段落对齐 `avatar_ref` 与归一化规则。
- [x] `task.md`：「66 条 HTTP 路由」→ 132 条（17 分组）；M3 的 94/95 勾选并写清真实边界。
- [x] `internal/adapter/connector.go` 头注释经核对已准确（不再是「only mock」），无需改动；
      `internal/handlers/schedules.go` 的过时 `TODO(real)` 已不存在（现为正常注释）。
- [x] `README.md` 沙盒表把 Console 行拆成 **API** 与 **Console** 两行（交互式控制台在 agentbuddy
      的 `ComputersView`），并补一行 Network 语义；`task-sandbox.md` S8 的两条声称（`sandbox-copy.js`、
      `ResourceKindForm` 沙盒区块 + 错误码 13 语）本轮之后与代码一致。

## M9 — 服务端与 web 覆盖率补强（2026-11-02，测试为主）

### 1. 服务端逐包抬升
- [x] `handlers` 73.7% → 76.2%：新增 `capabilities_test.go`（schedules 五个端点的 CRUD、未知 id、
      无 store 时整组 503；computers 删除的正常路径、不存在、他人卡片按 404 隐藏、带租约且
      `Terminate` 失败仍删卡片、无 store 503）与 `channels_extra_test.go`（投递回执列表、`itoa` 与
      `strconv` 对拍、`delegateThread` 的调度器/扇出/提及过滤/线程缺失、`redirectWithCode` 与
      `authCodeOf` 全分支）。参与者按**资源 id** 命名、`Registry.Get` 亦按 id，此前测试未触及这点。
- [x] `channels` 71.4% → 82.5%：`Deliver` 的无主机、拨号期拒绝私网、死域名、已取消 context、
      非字符串 `target_url` 五条失败路径（`https` 之外的写法在拨号前就被 `ValidateURL` 拦下）。
- [x] `router` 82.2% → 84.5%：门面访问器（`Engine`/`Bus`/`Resource`/`Clock`/`Kick`/`Live`/`GetTicket`）、
      `Disposition.Settled` 与 `DecisionKind.Disposition` 词汇表、`MemoryStore.SeedPolicy` 的写入与覆盖；
      `eval_apply_test.go` 覆盖 `apply` 的 attach（属主仍在则绑定、已关闭则重建工单）、三类不建单裁决、
      词表外裁决，以及 `ownerOf` 只认属主订阅（context-only 不算）。
- [x] `registry` 88.0% → 91.5%：`Kind.Valid`、`fieldError.Error/FieldOf`、`SandboxConfig.Profile`
      （nil 与全字段拷贝）、`EffectiveArgsPolicy` 六种组合、`validateSandbox` 全部拒绝项（含
      「未知 network 归一为 none 而非报错」），`WithSandboxChecker` 四条路径（查/不查/拒绝/未注入）。
- [x] `auth` 81.4% → 84.5%：`OAuthProviders`、`BeginOAuth`（state/verifier 新鲜、PKCE challenge 与
      redirect_uri 断言、未配置与无授权端点都返回 `ErrOAuthNotConfigured`）、`CompleteOAuth` 未配置；
      删除从未被调用的 `ProviderConfig.scopes`（与 `scopeString` 重复）。
- [x] `secure` 80.5% → 87.8%：密文非 base64 / 短于 nonce / 被篡改 / 错密钥，以及 `rand.Reader` 失败时
      加解密返回错误而不是空串（空串会被误读成「这一列本来就是空的」）。

### 2. 顺带修掉的真实缺陷
- [x] `router.MemoryStore.SeedPolicy` 原先只做结构体浅拷贝，`Allow`/`Keywords` 仍与调用方共享底层
      数组，调用方改一个元素就能在引擎不知情时放宽白名单；现改为写入即复制，
      `TestMemoryStoreSeedPolicyCopiesThePolicyIn` 钉住「播种后再改原结构不影响存储」。

### 3. web
- [x] 新增 `tests/api-stream.spec.js`：`streamTask`（正常帧、error 帧解包、坏 JSON 继续读、拒绝时
      `stream_unavailable`、body 缺失、abort 后 cancel）、`subscribeTaskEvents`（四种帧、路径转义、
      dispose 关闭、坏帧只记日志）、`saveBlob`（点击临时链接触发下载并释放 object URL）、
      `request` 的 204 / 非 JSON / 无 code 三条边界。
- [x] 新增 `tests/use-dismiss-dom.spec.js`：用手写 renderer + document 桩驱动 `useEscape` 的挂载/卸载、
      单一全局监听、栈顶优先、`ref`/函数/布尔三种 active、无 document 时不炸。
- [x] 新增 `tests/use-copy-dom.spec.js`：无 navigator 时回退 en、URL 优先于存储、`setLang` 落盘并镜像
      `<html lang|dir>`（ar → rtl）、未收录语言回退 en、无 window 时仍返回规范码。
- [x] `store.spec.js`：部分载荷的空默认值、无 code 的拒绝记为 `internal_error`、13 个动作的失败路径表
      （返回值 + `errorCode`）、`update` 成功路径。
- [x] `auth-store.spec.js`：`revokeSession` / `revokeUserSessions` 的成功与失败，并断言只有前者重载列表。
- [x] 零碎分支：`autoContinue` 遇未知策略返回 false、`hreflangFor` 未知码透传、`applyList` 的
      `undefined`/`null` 读作空列表、`passwordBytes` 的 `undefined` 记 0 字节、`applyText` 保留非字符串。
- [x] 结果：**21 文件 / 408 用例**，语句 99.85%、分支 95.19%、函数 98.51%
      （基线 18 文件 / 335 用例，98.59% / 91.66% / 93%）。

### 4. store：写后重读的失败回传（行为变更）

补用例时暴露的真问题：`update` 等动作在写入成功后调用 `refreshResources()`，而 `refreshX` 把失败
吞进 `errorCode`，动作随后又执行 `this.errorCode = ''` 并返回 `true`——**保存成功但列表没刷新**
会显示为成功、列表还是旧的、错误码还被抹掉。

- [x] `refreshResources` / `refreshTasks` / `refreshHealth` / `refreshRegistries` / `refreshApprovals` /
      `refreshSources` 一律返回布尔（成功 `true`，失败 `false` 且写入机器码）。仍**不抛异常**：
      多数调用点是 fire-and-forget，空白屏幕比过期列表更糟。
- [x] 新增动作内 `write(call)`：把写入的拒绝统一折算成 `errorCode` + `false`，13 处 try/catch 收敛成一处。
- [x] `register` / `update` / `remove` / `probe` / `saveRegistry` / `removeRegistry` / `saveSource` /
      `removeSource` 的返回值改为**「写入落地且列表已刷新」**：写入成功但重读失败时返回 `false`，
      错误码保留，抽屉不关闭（`errorCode` 的横幅给出的正是原因），也绝不重试写入。
- [x] `curate` 顺序读目录与审批队列，两者都要真读；成功的那次读会清空横幅，故在返回前把先失败
      的那次的码恢复（`catalogueCode || this.errorCode`），不会让第二个读把第一个的原因抹掉。
- [x] `syncSource` 的返回值仍是**同步结果本身**：重读失败时若返回 `null`，等于把服务端已报的
      imported/updated/skipped 藏起来，是第二个更大的谎；失败只体现在 `errorCode`，同样恢复被
      后一次成功读清掉的码。
- [x] `store.spec.js` 增补：「写成功但重读失败」的 8 个动作逐条断言（`false` + 码保留 + 写入只发生一次）、
      `curate` 的三种组合、`syncSource` 的结果保留、6 个 `refreshX` 的 true/false 对照。

### 5. 校验与未覆盖项
- [x] `go vet ./...` 干净；`go test ./...` 全绿，新增 53 条 Go 用例。
- [x] 本机 `go test -race` 不可用（Windows 无 cgo/gcc），已在 CI 侧补跑，不伪称已验证。
- [x] `internal/store`（3.0%）与 `internal/backup` 的 `pg_dump`/`COPY` 路径需 `DATABASE_URL`，
      仍按 skip 处理；`backup.writeArchiveHash` 依赖落盘引擎，未强测。

## M10 — CI 首次 `go test -race` 全绿修正（2026-11-02）

首次跑通的 GitHub Actions（`ci.yml`，postgres:16 服务容器 + `DATABASE_URL`）暴露四类
「本机没条件跑、因此从未被验证」的用例问题。产品代码的行为不变，改的是用例对环境的假设。

### 1. `internal/backup`：引擎选择依赖宿主机有没有装 pg_dump
- [x] `pgdump.go` 增加包级 `lookPath`（默认 `exec.LookPath`），`newPgDumpEngine` 走它；用例
      `withoutClient(t)` 把它换成返回 `exec.ErrNotFound`，`TestSelectEngineFallsBackToTheNativeEngine`
      与 `TestNewPgDumpEngineIsHonestAboutAMissingClient` 不再因 CI 镜像自带 PostgreSQL 客户端而失败
      （后者原先靠 `t.Skip` 回避，实际从未验证「没有客户端」这条路）。
- [x] 顺带把「自动选择必须落在 gonative」写死断言（原先只判 `Name() != ""`，选到 pg_dump 也算过）。

### 2. `tests`：RLS 断言连的是超级用户，等于什么都没断言
- [x] `openRLSDB` 先探测会话角色（`pg_roles.rolsuper` / `rolbypassrls`）。受策略约束的角色直接用；
      超级用户（compose 与 CI 服务的默认）则临时建 `agenthub_rls_test`（`NOSUPERUSER`、
      `NOBYPASSRLS`、无 DDL、只授 DML）并改用它重连，`current_user` 不是属主、不是超级用户，
      `026` 的策略才真正生效；建不出来时整组 skip 并提示 `scripts/provision-db-roles.sql`。
- [x] 运行期角色没有 DDL 权限时（生产形态：`DATABASE_URL=agenthub_app`），`Migrate` 失败但
      schema 已在即继续，不再把「无权限建表」当成断言失败。

### 3. 策略真生效后暴露的两条错误断言（原先被超级用户绕过）
- [x] `UPDATE tasks SET owner_id='bob'`：不是「USING 先滤掉、静默 0 行」，而是 USING 放行旧行、
      WITH CHECK 拒绝新属主 → **报错**。改为断言报错 + 行仍属原主。
- [x] `audit_log` 的 DELETE / UPDATE：策略层已把行滤掉（0 行、不报错），只有在策略不生效的连接上
      才轮到触发器抛错。改为断言「改动 0 行且内容未变」（`affectedIn`），两种收场都算通过，
      断言的是结果而不是哪一道闸先响。

### 4. 其余两条集成用例的期望与当前契约不符
- [x] `TestIntegrationResourceGrantRestricts`：仍按旧规则（「无 grant = 所有人可见」）断言，与
      `c2b5826` 的 fail-closed 改造相反。改为：持有 `resources:read` 也看不到无 grant 的记录；
      授权持有者能读能列；撤销后回到 restricted（再公开是 `visibility=public` 的独立动作）。
- [x] `TestIntegrationAPIKeyLifecycle`：用了词表里根本不存在的 `users:read` / `groups:read`，
      且 `svc.Register` 不像 `handleRegister` 那样自动授予 `role-viewer`。改为显式授予 viewer
      后断言 key 的 scope 交集（`resources:read` / `tasks:read` 保留，`backups:manage` 丢掉）；
      另把「实例所有者」显式化（`BootstrapAdmin` 提权），不再依赖「谁先注册谁就是 owner」的
      跨包时序。

### 5. 校验
- [x] `go vet ./...` 干净；`go test ./tests/ ./internal/backup/` 通过（无 `DATABASE_URL` 时集成
      用例 skip）。本机无 Docker/PostgreSQL，RLS 与其余数据库用例只能在 CI 上验证，不伪称已跑过。

## M11 — `go test ./...` 的并发迁移竞态（bug fix，2026-11-02）

第二次 CI run：RLS 与 backup 的用例全绿（M10 生效），但 `tests` 包有 5 条用例在
`openDBReal` 的第一步就挂了，全部是同一种错：

```
migrate: store: migration 020_router.sql failed:
  ERROR: duplicate key value violates unique constraint "pg_type_typname_nsp_index" (23505)
migrate: store: record migration 024_resource_visibility.sql:
  ERROR: duplicate key value violates unique constraint "schema_migrations_pkey" (23505)
migrate: store: migration 026_rls_policies.sql failed:
  ERROR: duplicate key value violates unique constraint "pg_proc_proname_args_nsp_index" (23505)
```

### 1. 根因：`Migrate` 幂等但不并发安全
- `go test ./...` 并行跑包，`internal/store` 与 `tests` 各自 `store.Migrate` 同一库。
- `CREATE TABLE IF NOT EXISTS` 不是原子的：两个会话都判定「不存在」，都去插系统目录，输的一方
  拿到 23505（表撞 `pg_type_typname_nsp_index`，函数撞 `pg_proc_proname_args_nsp_index`），
  整个迁移事务回滚；`schema_migrations_pkey` 那一条则是两边都认定某文件没跑过、抢写同一行。
- 生产同样成立：两个副本同时启动就会撞，不是 CI 专属问题。

### 2. 修复：事务级 advisory lock + 锁内复查
- `internal/store/migrate.go`：新增 `migrationLockKey`（`0x6167656e74687562`，"agenthub" 的
  ASCII 字节）；建 `schema_migrations` 先在一个持有 `pg_advisory_xact_lock` 的事务里完成
  （锁不需要表存在，因此没有先有鸡还是先有蛋的问题）。
- `applyMigration`：进入事务后先取同一把锁，再 `SELECT count(*) FROM schema_migrations` 复查；
  已被别人跑过就回滚返回 nil（不再写入）——把「判定 + 执行 + 记账」变成锁内的一步。
- 用事务级锁（`pg_advisory_xact_lock`）而不是会话级：提交/回滚即释放，不会因连接回池而泄漏成
  幽灵锁。热启动路径不变：`appliedMigrations` 预扫描命中就完全不加锁。

### 3. 校验与未覆盖项
- [x] `go vet ./...` 干净；`go test ./internal/store/ ./tests/` 通过（无 `DATABASE_URL` 时集成用例
      skip；`gofmt -l internal/store` 无输出）。
- [ ] 本机无 PostgreSQL，**并发路径未经本机验证**。该竞态在 CI 里每轮都会由
      `internal/store` 与 `tests` 两个进程真实触发，绿色即证；未另写「两个迁移者并发」的用例——
      它需要临时 schema 与第二个连接池，在无库环境下无法验证，宁可留给 CI 的真实竞态。
- [ ] 上一轮（M10）改写的 `TestIntegrationResourceGrantRestricts` /
      `TestIntegrationAPIKeyLifecycle` 这次都止步于迁移失败，断言仍未被执行过；其期望已逐条比对
      `authz.CanAccessResource` / `VisibleResourceIDs` / `BootstrapAdmin` 与 `006_rbac.sql`
      的 viewer 权限表，需以 CI 下一轮结果为准。

## M12 — 第四轮完整度报告 §8 收口（2026-11-02）

第四轮报告点出的第一类问题是**跨租户越权**：`schedules` / `channels` / `computers` 的列表
按 owner 过滤，但按 id 取行只认 id——任何登录用户改一个 id 就能读、改、删、投递或「立即触发」
别人的资产。第二类是同三张表没有被数据库行级策略覆盖，应用层是唯一一道闸。本轮先立闸，
再把文档与沙盒的「说了但没做」逐条改正。

### 1. 应用层 owner 作用域（8 处越权）

照抄已被验证的 `threads` 范式：**owner 提到端口签名上**，仓储侧统一走 `store.ownedWhere`：

```go
Get(ctx, id, ownerID)         // WHERE id = ? AND owner_id = ?
GetUnscoped(ctx, id)          // 仅 webhook；调用方必须自带系统身份
Update(ctx, c, ownerID)       // 精确 owner
Delete(ctx, id, ownerID)      // 精确 owner
```

语义定死：`ownerID == ""` 只匹配 `owner_id = ''` 的无主行，**不是「任意 owner」**；越权一律
返回 404（不泄露存在性）。改动面：

| 面 | 改法 |
| --- | --- |
| `handleGet/Update/DeleteSchedule` | 传 `callerID(r)` |
| `handleRunSchedule` | `router.RunSchedule(ctx, id, ownerID)`；非本人 → `ErrTicketNotFound` → 既有 404；`ActorID` 仍记该行 owner，`Meta` 增 `requestedBy` |
| `CronSource.Scan` | 列表与回写都在 `identity.With(ctx, {System:true})` 内，`Update` 传行自身的 `OwnerID` |
| `handleGet/Update/DeleteChannel`、`Deliver`、`RetryDelivery` | 传 `callerID(r)` |
| `handleListDeliveries` | **先**按 owner 取 channel 再列回执（此前完全没有归属校验） |
| `handleChannelWebhook` | `GetUnscoped` + `systemScope(r)`；验签、去重、转发不变 |
| `handleGetComputer`、创建回滚的 `Delete` | 传 `callerID(r)` |

新增 `internal/handlers/ownership_test.go` 覆盖「他人 id → 404」的每一条路径。

### 2. 数据库第二道闸：028 把三表纳入 FORCE RLS

`migrations/028_rls_policies_owned_surfaces.sql`：`channels` / `schedules` / `computers` 三表
`ENABLE` + `FORCE ROW LEVEL SECURITY`，策略 `*_owner_access` 的 `USING` 与 `WITH CHECK`
都走 `agenthub_row_visible(owner_id)`——跨 owner 的 `UPDATE`/`INSERT` 也被数据库拒绝。
受保护表因此从 8 张变 **11 张**，`tests/rls_test.go` 的表清单、策略名断言与三组新用例
（无身份看不到 / 他人看不到 / 跨 owner 写入被拒）同步更新。

`channel_messages` 有意不覆盖：回执只能经其 channel 到达，而 `handleListDeliveries` 已先做
owner 作用域的父行取数，父行就是闸门。

### 3. 认证口径对齐

`requireAuth` 改用 `Principal.Anonymous()` 判定，而不是 `PrincipalFrom(r) == nil`：前者还覆盖
「解析出了 principal 但没有 user id」这一形态，而它正是 owner 作用域下的 fail-open 入口。

### 4. 文档端点补全 + 差集守卫

- `web/src/lib/docs-content.js` 补 `DELETE /api/computers/{id}`（此前 `/docs` 屏漏项），
  `bun scripts/gen-docs-md.mjs` 重生成 `docs/docs.md`；`web/tests/docs-content.spec.js`
  的端点数 132 → 133。
- 新增 `tests/docs_endpoints_test.go`：正则抓 `handlers.go` 的 `mux.HandleFunc("METHOD PATH"`，
  归一化 `{id}` 形态后与 `docs/docs.md` 的表格行做**双向差集**——`TestEveryRouteIsDocumented`
  报漏项、`TestNoDocumentedRouteIsDead` 报幽灵条目。此后任何新路由漏文档都会直接红。

### 5. 沙盒诚实化

- `local` provider 的 `Describe().Network` 从 `[none bridge]` 改为 `[platform]`：裸子进程
  没有自己的网络命名空间，它走宿主的路由与 DNS，声明「可无网」是卖没有的能力。
- `Manager.Exec` 逐 arg 调 `sandbox.Argument(lease.Profile, arg)`（`CodePolicyDenied`）。
  CLI 连接器本来在造租约前就问过 `ArgsAllowed`，但 `Exec` 是所有调用方的唯一漏斗——
  包括 `POST /api/sandboxes/{id}/exec`，此前把调用方参数原样透传给 provider。
  默认 `ArgsNone` 下等价于「不接受任何调用方参数」，合法路径零变化。
- `AGENTHUB_SANDBOX_EGRESS_ALLOWLIST` 保留旋钮，但启动时非空即 `slog.Warn`：当前没有任何
  provider 做主机过滤，列表仍按 `none` 执行；要真正生效需要 egress sidecar。

### 6. 备份覆盖率（有库门控）

新增 `internal/backup/pgdump_client_test.go`（子进程注入密码、失败透传、超时；用
`TestMain` 之外的 helper 进程自证）与 `tests/backup_pgdump_test.go`
（`DATABASE_URL` + `pg_dump`/`pg_restore` 双门控）：`SelectEngine` 命中 pg_dump、
`Dump` 产出可读 manifest、`Restore` 往返、garbage archive 被拒。
本机（Windows，无 PostgreSQL）全部 skip，数字以 CI 为准；`internal/backup` 无库时约 57%，
有库 CI 上抬到 85% 以上——两个数字都如实记录，不合并成一个。

### 7. operator RBAC 评估与 CRD 旋钮

结论写在 `operator/README.md` / `README_cn.md`：K8s RBAC（operator 对 API 的权限）与
应用层 RBAC（14 个权限码 + 组/角色/授予）是两层正交体系，CRD 不建模后者是合理边界，
但必须写明「应用层 RBAC 在控制台配置」，否则运维会以为 CR 能声明用户权限。

真 bug 一个：`config/rbac/role.yaml`（controller-gen 产物）缺 `persistentvolumeclaims`
与 core 组 `events`，而 controller 真会 `buildPVC` + `Owns(PVC)` 并用 core events 记录事件
——非 Helm 安装开 `persistence` 就 403。已补 `+kubebuilder:rbac` 标记并对齐清单。

CRD 另补主服务有而 operator 缺的旋钮：`spec.rls{ mode, acknowledged }`、`spec.audit{…}`、
`spec.sandbox{…}`、`spec.router{…}`、`spec.migrateDatabaseURL`、`spec.legacyMethods`、
`spec.agentTimeout`、`spec.oauth.wechat`，渲染为对应 `AGENTHUB_*` 环境变量；
`rls.mode=off` 必须 `acknowledged=true`，与主服务 `Validate()` 同规则。

### 8. 校验与残余

- [x] `go vet ./...` 干净。
- [x] `go test -count=1 ./...` 全绿（本机无 PostgreSQL，集成用例 skip，属预期；
      结论区分「通过」与「跳过」）。
- [ ] CI 上 `go test -race ./...` 需要 cgo；本机 Windows 未装 C 工具链，故只跑无 `-race` 版本。
- [ ] `tests/rls_test.go` 的三组新断言与 `tests/backup_pgdump_test.go` 需要真实数据库与
      `pg_dump`/`pg_restore`，本机（Windows，无 PostgreSQL）全部 skip。WSL2 复验一条命令：

      ```bash
      DATABASE_URL=postgres://agenthub_app:pw@127.0.0.1:5432/agenthub?sslmode=disable \
        go test -race -count=1 -run 'RLS|PgDump' ./tests/ ./internal/backup/
      ```

      （先 `psql -f scripts/provision-db-roles.sql` 建角色并跑过一次迁移。）
- 残余：`channel_messages` 与 `threads` 无 `owner_id` 列，未纳入 RLS；webhook 的验签与去重
  语义未改；operator 的 `buildRole`（受管应用自身 Role）保持最小，不动。

## M13 — 再一轮覆盖率补强（2026-11-02，测试为主）

只加用例，不改生产代码。新增用例顺带钉死两条此前无人断言的语义：**未知 id 读 404、删 204**。

### 1. 服务端：`internal/handlers` 76.3% → **79.4%**

- `store_failures_test.go`：给 threads / channels / computers / schedules / knowledge 五个 Store
  端口各挂一个「每次调用都失败」的假实现，跑遍 **22 条 CRUD 路由**，断言一律 5xx 且带机器码。
  这些分支以前只能靠读代码相信：一个吞掉错误的 handler 会回 200 + 空列表，界面显示「你还没有
  任何线程」，而整个功能其实是断的。
- 同文件另加两组：`TestAnUnknownIdIsANotFoundForEverySurface`（未知 id 读 404，含各面自己的码）
  与 `TestDeletingAMissingRowIsStillASuccess`（schedules / channels 删未知 id 回 204——重试不该被
  告知「你搞错了」）。
- `admin_edges_test.go`：管理员真会撞上的 4xx——未知用户 404、`invalid_email`、`weak_password`、
  不存在的 `status` → `bad_request`，并断言**拒绝后账户仍在原值**；另有「一次请求同时改状态与口令」
  与「删除即禁用（行必须留下）」两条复合路径。
- `resource_edges_test.go`：资源五条路由未知 id 一律 404；非法 `health` / `visibility` → 400
  （`invalid_visibility` 用 registry 常量比对）；PUT 的 body 不能改 id；连接器未知 id 404。
- `admin_store_failures_test.go`：authz 端口整体故障时，permissions / roles / groups / grants
  十二条路由全部 5xx 带码——防的是「以为删了但没删」。

### 2. web：21 文件 / **413 用例**，语句与行 **100%**、分支 **95.1%**、函数 **99.5%**

- `tests/auth-store.spec.js` 补 5 条，把该文件从 95.05% 推到 100% 语句：`loadConfig` 的
  `providers`/`configured` 映射与两种失败（带码 / 无码回落 `internal_error`），`register` 的成功
  与 `email_taken` 失败（同时断言 `loading` 归零）。注册与实例配置此前**一条都没被执行过**。
- 剩余分支集中在 `api.js`（可选链缺省）、`playground.js`（表单缺省值）与 `auth-store` 的
  `?? []` 回退臂，均为不可达或等价分支，未写假用例。

### 3. 刻意不补的部分

- `internal/store`（2.9%）、`internal/backup`（60.4%）需要真实 PostgreSQL 与 `pg_dump`/`pg_restore`，
  本机 skip，数字以 CI 为准（门控命令见 M12 §8）。
- handlers 里仍空着的主要是三类**没有注入点**的错误分支：concrete 服务（router / sandbox / backups）
  不是端口、auth 的用户仓储在测试 helper 内部构造无法替换、`httputil.Stream` 的「writer 不支持
  Flush」分支需要非 `httptest` 的 ResponseWriter。补它们要么改生产代码加端口，要么写绕过入口的
  假用例，两者都比留白更糟。

## M14 — 检查点不得回滚只追加账本（bug fix，2026-11-02）

**现象**：门控套件全绿，但一旦打开 `AGENTHUB_TEST_BACKUP_RESTORE=true`，
`TestIntegrationBackupRestoreRewindsTheDatabase` 必挂：

```
backup: the dump engine failed: truncate: ERROR: audit_log is append-only: TRUNCATE is not allowed
```

**根因**：`internal/backup` 的 `excludedTables` 只排除检查点清单两张表，`audit_log` 仍在范围里；
`copyEngine.Restore` 用一条 `TRUNCATE TABLE … CASCADE` 清空全部表，而 `025_audit_log.sql` 的
`BEFORE TRUNCATE` 触发器对任何角色（含 owner / superuser）一律 `RAISE`。更深一层：`pg_dump`
引擎的 `--clean` 靠 `DROP` 绕过触发器，等于**默认引擎也在回滚审计历史**——恢复后
`audit.Recorder` 的 `prev` 仍指向已不存在的行，`/api/audit/verify` 会一直报断链。

**方案**（A + 类级防线）：账本不进检查点，也不被恢复改写。

- `excludedTables` 增 `audit_log`：dump 不收、`truncateAll` 不清、manifest 不列、`fixSequences` 跳过。
- `copyEngine.Restore` 对旧归档里残留的 `audit_log` 帧**丢弃而非报错**（`frameDisposition`
  返回 `frameDrop`），昨天写的归档今天仍可读。
- **类级预检**（`ensureRewritable` / `guardedTables` / `refusesTruncate`）：被 dump 的表若带
  `BEFORE TRUNCATE` 触发器，dump 与 restore 都按表名以新码 `backup_table_guarded`（409）拒绝，
  并提示改用 `pg_dump` 引擎。只认 `BEFORE TRUNCATE`——`TRUNCATE` 不触发行触发器，把
  `BEFORE DELETE`/`BEFORE UPDATE` 也算进来会误拒本可恢复的表。
- `pgDumpEngine.Dump` 改为**白名单**（`--table <schema>.<name>` + `archiveSequences`），不再用
  `--exclude-table`：按名排除表时，它的序列与触发器函数仍会进归档，`pg_restore --clean` 随后
  试图删掉它们（实测报 `cannot drop sequence public.audit_log_id_seq` 与
  `cannot drop function public.audit_log_append_only()`）。
- 错误码 `backup_table_guarded` 登记三处：`httputil.Code*` + `WriteErr` 状态码 + `codeOf`
  + handler 映射 + 前端 `CODE_KEYS` / `errors-copy.js` 13 语 / `docs-content.js` 409 组。

**验证**（WSL + `postgres:16-alpine`，scratch 库 `agenthub_ci`）：

- `go test -race ./tests/ ./internal/store/ ./internal/backup/`：三包 ok；恢复三例（含破坏性）
  连跑两轮均 PASS，`TestIntegrationBackupRestoreRewindsTheDatabase` 从必挂转绿。
- `TestIntegrationPgDumpEngineRoundTrips` 追加断言：恢复后账本行仍在、`audit_log_no_truncate`
  仍在、`tasks` 的 RLS 策略与 `events` 的 owner 触发器仍在（白名单 dump 不能丢安全对象）。
- `TestIntegrationBackupRefusesAGuardedTable`：建一张带 `BEFORE TRUNCATE` 的探针表，Dump 与
  Restore 都返回 `ErrTableGuarded` 且错误里带表名。
- 两条破坏性用例加前置清理（`DELETE FROM resources WHERE name LIKE 'it-%'`）：恢复会把检查点
  捕获的行写回，否则同一库第二次跑会撞 dedup 唯一索引。
- web：21 文件 / 413 用例全绿。
