# task-security.md — agenthub 安全加固实施记录

> 方案见 `requirements-security.md`，与 serve 五大域同构、按 agenthub 架构适配。

## 1. 传输 / 边缘

1. [x] `internal/middleware/security_headers.go`：中间件 `SecurityHeaders(env)`，输出
   `X-Content-Type-Options` / `Referrer-Policy` / `COOP` / `CORP` / `CSP-Report-Only`；
   HSTS 仅在 `prod` 或已 TLS（`r.TLS` 或 `X-Forwarded-Proto: https`）时发出。
2. [x] `internal/middleware/ratelimit.go`：`Limiter`（内存固定窗口，惰性 GC）+ `RateLimit` 中间件：
   认证端点 `auth:*IP` 严格 10/min；其余 `/api` 与 `/a2a` 为 `api:*IP` 200/min；其余路径不限。
   `clientIP` 信任 `X-Forwarded-For` 最左跳。
3. [x] `internal/middleware/csrf.go`：`CSRF` —— 变更型请求校验 `Origin` 同源；`GET/HEAD/OPTIONS`、
   无 `Origin`、以及环回客户端（桌面代理/本机）放行。
4. [x] `internal/handlers/wrap.go`：`Wrap(h, cfg)` 套链 `Recover → SecurityHeaders → CSRF → RateLimit → 路由`；
   `cmd/agenthub-server/main.go` 用 `handlers.Wrap(handlers.Register(deps), cfg)` 替换原 `Register`。
5. [x] 单测：`ratelimit_test.go`（窗口/限流/strict/跳过静态）、`csrf_test.go`（GET/无 Origin/环回/异源拒绝/同源放行）、
   `security_headers_test.go`（HSTS 仅在 prod/TLS 发出）。

## 2. 认证与限流

6. [x] `migrations/011_login_protection.sql`：`users` 增 `failed_login_attempts`、`locked_until`（IF NOT EXISTS + 索引）。
7. [x] `internal/model/auth.go`：`User` 增 `FailedLoginAttempts` / `LockedUntil` 字段。
8. [x] `internal/auth/auth.go`：扩展 `UserStore` 接口（`RecordLoginFailure` / `ResetLoginAttempts` / `LockedUntil`）；
   `Login` 在密码比对前检查锁定、失败后累加并锁定、成功清零；审计仅记 IP / user_id。
   `ErrAccountLocked` + 常量 `MaxLoginAttempts=5` / `AccountLockMinutes=15`。
9. [x] `internal/store/auth_store.go`：`userStore` 实现三方法（`RecordLoginFailure` 单语句 `CASE` 锁定、`ResetLoginAttempts`、扫描 `LockedUntil`）。
10. [x] `internal/httputil/json.go`：新增 `CodeAccountLocked` → 429；更新 `WriteErr` 映射。
11. [x] 测试桩 `auth/auth_test.go`、`handlers/auth_test.go` 的 `UserStore` 假实现补齐三方法；
    `auth_test.go` 新增 `TestLoginLockout` 与 `lockoutUsers` 假实现（5 次失败→锁定→正确密码仍锁定→解锁后成功→重置）。

## 3. A2A / 出站 SSRF

12. [x] `internal/netutil/ssrf.go`：`SafeClient`（dial 时解析并拒绝危险地址、拒绝重定向、超时 + 体上限）、
    `isDangerous`、`ValidateURL`（https-only，可选 allowHTTP）。
13. [x] `internal/hub/notifier.go`：`HTTPNotifier` 实现 `PushNotifier`（含 `Reset()`）；SSRF 校验 + 可选 HMAC
    签名（`sign`）+ `(taskID, state)` 1h 幂等去重（`markSeen`）；投递失败只告警、落 `push_records`。
14. [x] `internal/auth/auth.go`：`PushNotifier` 接口增加 `Reset()`；`MockNotifier` 已有，`HTTPNotifier` 补齐。
15. [x] `cmd/agenthub-server/main.go`：`AGENTHUB_PUSH_SECRET` 非空时以 `hub.NewHTTPNotifier(pushRepo, secret)`
    替换记录型 `MockNotifier`；`newBackupService` 入参由 `*hub.MockNotifier` 改为接口 `hub.PushNotifier`。
16. [x] `internal/registry/sync_http.go`：`NewHTTPClient` 改用 `netutil.SafeClient`（允许 private，因运营商可信），
    `Fetch` 入口 `ValidateURL`。
17. [x] 单测：`netutil/ssrf_test.go`（危险地址表、URL 校验、拒绝环回）、`hub/notifier_test.go`（拒绝 http URL、
    签名确定且随密钥/时间戳变化、幂等去重）。

## 4. 输入与数据 / 密钥

18. [x] `internal/secure/crypto.go`：`Configure` / `EncryptDefault` / `DecryptDefault`，密钥源自 `AGENTHUB_ENCRYPTION_KEY`，
    缺失时由（会话）密钥派生并告警；旧密钥回退解密。AES-256-GCM。
19. [x] `internal/config/config.go`：新增 `EncryptionKey` 字段与 `AGENTHUB_ENCRYPTION_KEY` 读取；`Validate` 中生产强制
    `EncryptionKey ≥ 32` 字节、`PushSecret ≥ 16` 字节。
20. [x] `cmd/agenthub-server/main.go`：启动处 `secure.Configure(cfg.EncryptionKey, cfg.EncryptionKey)`。
21. [x] 审计：GORM 全参数化；`Raw`/`Exec` 仅用于迁移、备份 COPY、push 留痕，均基于白名单标识符/占位符，无注入风险；
    API key 已哈希、OAuth 不存令牌，故无查找型 PII 列需加密 —— 加密能力就绪，文档化留待未来字段。
22. [x] 单测：`secure/crypto_test.go`（加解密往返 + 密钥轮换回退）。

## 5. 数据库安全

23. [x] `internal/store/db.go`：`Open` 连接池改由 `dbPoolFromEnv`（默认 25/10/30m，可经环境变量覆盖）。
24. [x] `internal/config/config.go`：新增 `MigratorDatabaseURL`（`AGENTHUB_MIGRATOR_DATABASE_URL`）；
    注：`Validate` 曾于生产强制 `DATABASE_URL` 的 `sslmode`，该要求已移除（见后续配置变更）；连接是否启用 TLS 现由部署方自行决定。
25. [x] `cmd/agenthub-server/main.go`：若设 `MigratorDatabaseURL`，迁移在其专连上跑，运行期仍用 app 角色。
26. [x] `scripts/provision-db-roles.sql`：`app` / `migrator` / `readonly` 最小权限三角色与默认权限。

## 6. 备份加密

27. [x] `internal/backup/crypto.go`：`EncryptFile` / `DecryptFile`（AES-256-GCM，nonce‖密文，密钥 = sha256(secret)）。
28. [x] `internal/backup/service.go`：`Options.Key`；`checkpointLocked` 落盘后加密为 `<path>.enc`、删明文、写 `<path>.enc.sha256`；
    `verify` / `load` 经 `toPlaintext` 透明解密；`Delete` 一并删 `.sha256`；`removeArchive` 辅助。
29. [x] `cmd/agenthub-server/main.go`：`EncryptionKey` 非空时注入 `opts.Key`。
30. [x] `scripts/backup-restore-drill.sh`：取最新归档（`.enc` 经内联 Go 解密 / `.tar.gz` 直解）→ 解包 → 恢复进空库 → 报告关键表行数。
31. [x] 单测：`backup/crypto_test.go`（加解密往返 + 错密钥失败）。

## 7. 收口

32. [x] `go build ./...` / `go vet ./...` 全绿；`go test` 覆盖上述包全绿（无 DB 依赖的包零外部依赖）。
33. [x] `README.md` / `README_cn.md`：新增「安全加固」章，更新环境变量（加密/Push/Migrator/DB 池）、备份（加密+演练）、生产须知（DB TLS、最小权限、密钥轮换）。
34. [x] 文档：`requirements-security.md`（方案）/ 本 `task-security.md`（记录）。

## 待人工确认 / 后续

- 部署前 `bash -n` 通过的 `scripts/backup-restore-drill.sh` 需在真实环境跑一次；需 `go` 与 `pg_restore/psql`。
- 灰度后视情把 CSP 由 `Report-Only` 切为强制。
- 执行 `provision-db-roles.sql` 并将运行期凭据切换到 `app` 角色、迁移切换到 `migrator`。
- 生产 `AGENTHUB_ENCRYPTION_KEY` 与（若启用）`AGENTHUB_PUSH_SECRET` 须由密钥管理注入，绝不提交入仓。
