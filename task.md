# task.md — agenthub（A2A 互连平台）实施清单

> 由 `requirements.md` 生成的分步清单（M1 已全部落地）。
> 统一验收基线：`go vet ./...` 无告警、`go test -race ./...` 全绿、`bunx vitest run` 全绿、
> `make build` 产出前端产物与二进制。

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
14. [x] `mock.go`：确定性基元（`digest`/`pick`/`truncate`/`artifact`/`boundedContext`/`mockHealth`）
15. [x] `model.go`：`ModelProvider` + `ModelRouter`（provider 注册表 + 回退链）
16. [x] `agent.go`：规划步骤 + **词级**审批判定（`approve`/`confirm`/`approval`；`approved` 不重挂）
17. [x] `mcp.go`：传输无关的 `tools/list` + `tools/call` 协议转换
18. [x] `skill.go`：触发 → 手册 → 步骤 → 报告
19. [x] `cli.go`：白名单 → 超时 → 截断 → 结构化 JSON 回执（含 `truncated` 标记）

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

## M3 — 后续项（未开始）

94. [ ] 真实适配器落地：~~model（OpenAI 兼容端点 + 回退链，M2-64/65 已落地）~~、agent（`aria-agent-cloud`
      SSE → A2A）、mcp（stdio / Streamable HTTP 子进程与 HTTP 客户端）、skill（本地/远端手册加载）、
      cli（`exec.CommandContext`，沿用白名单/超时/截断）
95. [ ] pushNotification 真实回调（HTTP POST + 鉴权 + 重试；留痕表已就位）
96. [ ] 多实例联邦：Agent Network（跨 agenthub 的 Agent Card 聚合与路由）
97. [ ] eval 钩子接评测编排（**仍不产出分数**；缺凭据 skip 并写 reason）
98. [ ] 控制台增强：拓扑时间窗口持久化、任务筛选与分页、artifact 下载
99. [ ] 目录侧剩余项（对照 README「AWS Agent Registry 特性对比」的「剩余差距」）：批量读接口与列表分页、
     语义检索走外部检索源接入、全量 API 调用审计留痕
100. [ ] 覆盖率后续项：`internal/auth` 的 OAuth `Exchange`（端点为常量，需可注入才能离线断言）、
     `internal/backup` 的 `copy.go` / `pgdump.go`（需真实 PostgreSQL 与 `pg_dump` 客户端）
