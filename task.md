# task.md — agenthub（A2A 互连平台）实施清单

> 由 `requirements.md` 生成的分步清单（M1 已全部落地）。
> 统一验收基线：`go vet ./...` 无告警、`go test -race ./...` 全绿、`bunx vitest run` 全绿、
> `make build` 产出前端产物与二进制。

## M1 — A2A 互连平台首版（已完成）

### 1. 工程骨架

1. [x] `go.mod`（module `agenthub`，零第三方 require）、`.gitignore`、`.env.example`（端口 3500）
2. [x] `Makefile`：`run/build/test/vet/fmt/web-install/web-dev/web-build/web-test/clean`
3. [x] `internal/config`：`LoadDotEnv`（向上 6 层、只补未设置键）+ `Load()` + `Validate()`
   （端口/env/并行数/超时/输出上限/事件缓冲校验；`prod` 无 token 拒绝开放模式）
4. [x] `internal/httputil`：`WriteJSON/WriteError/WriteErr/Decode` + SSE
   （`Stream/WriteSSE/WriteRawSSE/Comment/KeepAlive`）
5. [x] `cmd/agenthub-server`：启动顺序 config → registry 载入 snapshot → 种子 → hub → handlers →
   `http.Server`（`ReadHeaderTimeout: 30s`）→ 信号优雅退出（15s）+ snapshot 落盘；全程 `slog` JSON
6. [x] `README_cn.md`：概念地图、A2A 端点、冒烟 curl、flow 原语、错误码、mastra 关系与边界

### 2. 协议层（`internal/a2a`，冻结契约）

7. [x] `types.go`：`TaskState`/`States`/`IsTerminal`/`Valid`、`Part`（text/file/data）、
   `Message`、`TaskStatus`/`Artifact`/`Task`/`NewTask`、流式信封、AgentCard 相关类型
8. [x] `jsonrpc.go`：JSON-RPC 2.0 信封、标准码 + A2A 自定义码（`-32001`…`-32008`），
   `error.message` 即 code
9. [x] `methods.go`：方法名常量 + `Canonical(method, legacy)` v0.1 别名映射 + 各 Params 类型

### 3. 资源层（`internal/registry`）

10. [x] `registry.go`：`Kind`/`Health`/`Transport` 枚举、`Entry`（Registry 级字段）、
    `Upsert/Register/Get/Delete/List/SetHealth` + 计数 + `Snapshot`（`RWMutex`）
11. [x] `snapshot.go`：JSON 快照（版本化、临时文件 + rename 原子写）、`LoadInto`
12. [x] `export.go`：聚合 Agent Card（每资源一个 skill）+ MCP server 描述导出

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
    artifact 累积 + 事件发布 + `MockNotifier`（内存记录，含 `Records()`）

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

### 9. 测试与文档收口

38. [x] 包级单测（正常 + 异常）：`a2a`、`config`、`registry`、`adapter`、`hub`、`handlers`、`httputil`
39. [x] `tests/e2e_test.go`：httptest 真实 HTTP 端到端——Agent Card + healthz、五类适配器各跑通、
    get/cancel、`input-required` → `tasks/resume` 恢复、流式终态帧与错误帧、flow 分支 + 并行、
    v0.1 别名、pushNotification 计数、控制面注册 → 调用 → 删除
40. [x] 前端 `web/tests/`：`languages` / `copy-fallback` / `enums-copy` / `api` / `style`（5 文件 22 用例）
41. [x] 验收：`go vet ./...` 通过、`go test -race ./...` 全绿、`bunx vitest run` 22 passed、
    `make build` 产出 `backend/static` 与 `bin/agenthub-server`
42. [x] 文档同步：`AGENTS.md` / `requirements.md` / 本 `task.md` / `README_cn.md`

## M2 — 后续项（未开始）

43. [ ] 真实适配器落地：model（OpenAI 兼容端点 + 回退链）、agent（`aria-agent-cloud` SSE → A2A）、
      mcp（stdio / Streamable HTTP 子进程与 HTTP 客户端）、skill（本地/远端手册加载）、
      cli（`exec.CommandContext`，沿用白名单/超时/截断）
44. [ ] 持久化换 Postgres（`ResourceStore`/`TaskStore` 接口预留；任务与 span 落库）
45. [ ] pushNotification 真实回调（HTTP POST + 鉴权 + 重试）
46. [ ] 多实例联邦：Agent Network（跨 agenthub 的 Agent Card 聚合与路由）
47. [ ] eval 钩子接评测编排（**仍不产出分数**；缺凭据 skip 并写 reason）
48. [ ] 与 mastra agent 的 A2A 互通验证（形态已对齐，仅验证）
49. [ ] 控制台增强：拓扑时间窗口持久化、任务筛选与分页、artifact 下载
