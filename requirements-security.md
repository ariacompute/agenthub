# requirements-security.md — agenthub 安全加固方案

> 参考 ariacompute/serve 的五大域安全加固，针对 agenthub 的架构差异做同构适配。
> 实施记录见 `task-security.md`。M1/M2 的产品规格在 `requirements.md` / `task.md`，不在本文范围。

## 0. 架构差异与适配原则

| 维度 | serve | agenthub（本方案） |
| --- | --- | --- |
| 传输边缘 | Nginx + 多站点 | 单 Go `net/http`，无 Nginx。安全响应头改由 Go 中间件发出；生产须前置 TLS 终止反代（Caddy/nginx） |
| 会话 | 有签名 JWT（曾存在 JTI 绕过） | DB 随机会话令牌（无 JTI 问题，吊销天然有效），但改用 cookie → 需 CSRF 防护 |
| 支付 webhook | Stripe/微信/支付宝 | 无支付；但有 **A2A pushNotification** 与 **上游 registry 同步**，均为服务端出站请求 → SSRF + 回调签名/新鲜度/重放 |
| 注入 | pgx 参数化 | GORM 参数化（审计后确认全参数化，无注入风险） |
| 密钥存储 | 环境变量 | API key 已哈希存储；OAuth 密钥来自环境变量（不入库）；提供独立静态加密密钥能力 |
| 备份 | openssl AES-256-GCM | Go 原生 AES-256-GCM（无 openssl 依赖），已具备 SHA-256 清单 |

## 1. 传输 / 边缘

- 新增 `internal/middleware/security_headers.go`：统一输出 `X-Content-Type-Options: nosniff`、`Referrer-Policy: strict-origin-when-cross-origin`、`Cross-Origin-Opener-Policy: same-origin`、`Cross-Origin-Resource-Policy: same-origin`、`Content-Security-Policy-Report-Only`（严格默认，先灰度）。
- HSTS 仅在 `env=prod` 或请求已走 TLS（`X-Forwarded-Proto: https` 或 `r.TLS!=nil`）时发出，避免明文开发环境被拒绝。
- 生产部署文档化：必须在 agenthub 之前前置 TLS 终止反代；桌面 Tauri 仅做环回代理，不受影响。

## 2. 认证与限流

- `internal/middleware/ratelimit.go`：内存固定窗口限流，按 `X-Forwarded-For` 最左跳取客户端 IP。认证端点（`/api/auth/*`）严格 **10 次/分钟/IP**，其余 `/api` 与 `/a2a` 共享受限 **200 次/分钟/IP**；非 `/api`、`/a2a` 路径（静态、healthz、发现）不限。
- 账户暴力破解防护（`migrations/011_login_protection.sql` + `auth.Login` + `store/auth_store.go`）：连续失败 **5 次锁定 15 分钟**；登录成功清零失败计数与锁定；不存在的账户一律返回 `invalid_credentials`，不泄露账户存在性。
- 审计日志仅记 **IP 与 user_id**，绝不记密码/令牌/PII。
- `internal/middleware/csrf.go`：变更型请求要求 `Origin` 同源（请求 Host 或显式允许源）；`GET/HEAD/OPTIONS` 与无 `Origin`（脚本/桌面代理/非浏览器）放行。由于 cookie 为 `SameSite=Lax` 且控制台同源，既有交互不被破坏。
- `handlers.Wrap` 在 `cmd/agenthub-server` 启动处套用链：`Recover → SecurityHeaders → CSRF → RateLimit → 路由`；不进入 `Register`，单测不受影响。

## 3. A2A / 出站 SSRF（对应 serve 的 webhook 域）

- 新增 `internal/netutil/ssrf.go`：`SafeClient` 在 dial 时解析目标并拒绝 **loopback / private / link-local / multicast / unspecified** 地址（防 DNS 重绑），拒绝跟随重定向，限定超时与响应体上限。`ValidateURL` 仅允许 https（或显式允许的 http）。
- `internal/hub/notifier.go` 的 `HTTPNotifier` 替换 `MockNotifier` 的 `TODO(real)`：向 `cfg.URL` 发 A2A 终态回调，调用前先做 SSRF 校验；当 `AGENTHUB_PUSH_SECRET` 配置时，对载荷做 **HMAC-SHA256（时间戳 + nonce + body）** 签名并附带 `X-Agenthub-Timestamp/Nonce/Signature`，供接收方验证真实性与新鲜度（±5 分钟）、去重重放；按 `(taskID, state)` 做 **1 小时幂等**去重，重复回调不影响任务终态。未配置 secret 时退化为仅 Bearer 鉴权（与既有 `cfg.Token` 一致）。失败投递只告警，绝不回压任务。
- `internal/registry/sync_http.go`：上游源同步客户端改用 `SafeClient`（运营商可信，允许 private 但仍有超时/限长/不跟随重定向），并在 `Fetch` 入口 `ValidateURL`。

## 4. 输入与数据 / 密钥

- 审计结论（见 `task-security.md`）：GORM 全部参数化；唯一 `Raw`/`Exec` 用于迁移、备份 COPY 与 push 留痕，均基于白名单标识符或占位符，无用户数据拼接，无注入风险。
- 提供 `internal/secure`：`Configure` / `EncryptDefault` / `DecryptDefault`，密钥来自 `AGENTHUB_ENCRYPTION_KEY`（缺失时由会话密钥派生并告警）。支持旧密钥回退，便于无停机轮换。生产强制 `AGENTHUB_ENCRYPTION_KEY ≥ 32` 字节。
- API key 已哈希存储（`token_hash`），OAuth 客户端密钥来自环境变量、不入库；`oauth_accounts` 仅存 provider/subject/email/profile，不存 access/refresh token。因此当前无需要加密的查找型列；加密能力就绪，留待未来敏感字段使用。
- 请求体大小上限沿用 `httputil.Decode` 的 1MB 限制；无需额外全局限流。

## 5. 数据库安全

- `DATABASE_URL` 不再强制 `sslmode`：是否对数据库连接启用 TLS 由部署方自行决定（如需传输加密，可在 DSN 中设置 `sslmode=require|verify-ca|verify-full`）。
- 连接池可配置：`AGENTHUB_DB_MAX_OPEN_CONNS`（默认 25）/`AGENTHUB_DB_MAX_IDLE_CONNS`（10）/`AGENTHUB_DB_CONN_MAX_LIFETIME`（30m），见 `store/db.go`。
- 最小权限角色：运行期 `DATABASE_URL` 使用 `app` 角色，迁移在启动期的同一连接上执行（因此该角色需可建表/改表）。`scripts/provision-db-roles.sql` 生成 `app` / `readonly` 两角色。
- 备份静态加密：`internal/backup/crypto.go`（AES-256-GCM，nonce‖密文）+ `service.go` 在落盘时加密为 `*.tar.gz.enc` 并写 `<file>.sha256` 清单；恢复时透明解密。`scripts/backup-restore-drill.sh` 定期演练。

## 6. 验收标准

- `go vet ./...` / `go test -race ./...`（含 DB 门控集成用例）全绿，无新增外部依赖。
- 新增单测：中间件（限流/CSR/安全头）、netutil（SSRF 拒绝环回、URL 校验）、secure（加解密往返 + 密钥轮换回退）、netutil/hub notifier（签名确定 + 幂等）、config（sslmode 解析）、backup（加解密往返）。
- 部署前人工确认：前置 TLS 反代；`AGENTHUB_ENCRYPTION_KEY` 与 `AGENTHUB_PUSH_SECRET`（若启用）已配置且 ≥ 阈值；执行 `provision-db-roles.sql` 并切换凭据到最小权限角色；定期运行 `backup-restore-drill.sh`。
