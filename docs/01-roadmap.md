# 01 · 路线图与任务分解

> 本文件把重构拆成 **8 个里程碑**。每个里程碑独立可交付、可验证,结束时项目处于可运行状态。

## 里程碑总览

| # | 里程碑 | 交付物 | 依赖 | 预估 |
|---|---|---|---|---|
| M0 | 工程地基 | 仓库骨架、工具链、CI | — | 1 天 |
| M1 | 基础设施 | 配置、日志、DB、迁移、错误体系 | M0 | 2 天 |
| M2 | 账号内核 | 账号、凭据、会话、RBAC、审计 | M1 | 5 天 |
| M3 | OIDC 授权服务 | fosite 集成、SSO、客户端管理 | M2 | 5 天 |
| M4 | MC 认证域 | Yggdrasil 协议、profile、别名 | M2 | 4 天 |
| M5 | 皮肤站 | 上传、存储、去重、头像渲染 | M4 | 4 天 |
| M6 | 前端 | account-web + admin-web | M3,M4 | 6 天 |
| M7 | 部署与加固 | 容器化、集成测试、压测、安全检查 | 全部 | 4 天 |

**合计约 31 人日**。单人全职约 6 周,两人并行约 4 周(前端与后端可重叠)。

---

## M0 · 工程地基

**目标**:能编译、能跑测试、能起一个空服务。

- [x] 初始化 Go module `github.com/yggauth/yggauth`
- [x] 目录骨架:`cmd/`、`internal/`、`db/`、`web/`、`deploy/`
- [x] `Makefile`:`make build` / `make test` / `make lint` / `make dev` / `make sqlc` / `make migrate`
- [x] 工具链版本锁定:Go 1.27、Node 24、pnpm 11(`.tool-versions`)
- [x] lint 配置:`golangci-lint`(含 `gofumpt`、`errcheck`、`staticcheck`)
- [x] GitHub Actions:`build` + `test` + `lint` 三 job
- [x] `cmd/yggauth/main.go` 骨架,`GET /health/live` 返回 200

**验收**:空服务能启动,CI 全绿。

---

## M1 · 基础设施

**目标**:把「与业务无关的通用能力」建成可复用底座。

### 配置
- [x] `internal/config`:环境变量加载 + 类型转换 + 启动即校验(失败直接退出,带清晰错误)
- [x] 配置项分组:`app` / `db` / `auth` / `storage` / `oidc` / `mc` / `mail` / `log`
- [x] 敏感项(数据库密码、`KEY_MASTER_SECRET`)启动日志中脱敏

### 日志与可观测
- [x] `internal/platform/log`:基于 `log/slog` 的 JSON 结构化日志,带 `request_id`、`domain`、`account_id` 上下文
- [x] `/health/live`(存活)、`/health/ready`(就绪,探 DB)、`/metrics`(Prometheus)

### 数据库
- [x] `internal/platform/db`:连接池(`pgx/v5` + `pgxpool`)、事务辅助函数、`Ping` 自检
- [x] `db/migrations/`:goose 迁移目录,首版 baseline
- [x] `db/queries/`:sqlc 查询定义
- [x] `make sqlc` 生成,CI 校验生成物与源码一致

### 错误与响应
- [x] `internal/platform/apperr`:业务错误码枚举 → HTTP 状态码映射
- [x] 统一响应包 `{"code":0,"message":"ok","data":{...}}`
- [x] 全局 error handler:内部错误只回泛化消息,细节进日志
- [x] request ID 中间件 + panic recover 中间件

### 工具
- [x] `internal/platform/uuid`、`internal/platform/clock`(便于测试注入)、`internal/platform/rand`

**验收**:`make migrate` 能建库,`/health/ready` 返回 DB 状态,非法配置启动即失败。

---

## M2 · 账号内核

**目标**:域中立的身份能力。**不含任何 OIDC / MC 语义**。

### 数据模型
- [x] `account` 表(含 `email`、`username`、`email_verified_at`、`status`、`mc_login_enabled`)
- [x] `credential` 表(argon2id hash,支持未来多种算法)
- [x] `session` 表(**统一模型**,见 [04-decisions.md](./04-decisions.md) ADR-004)
- [x] `role` / `permission` / `role_permission` / `account_role`(**单一 RBAC**,ADR-005)
- [x] `audit_event` 表

### 功能
- [x] 注册:邮箱格式校验、用户名唯一性、密码策略(8–128)、邮箱验证令牌
- [x] 登录:argon2id 校验、失败锁定(N 次 / M 分钟)、会话签发
- [x] 会话:滑动过期、绝对过期、登出、列出活跃会话、踢下线
- [x] 密码重置:请求(不泄露账号是否存在)→ 邮件令牌 → 重置
- [x] 邮件验证:令牌 + 重发冷却
- [x] RBAC:角色 CRUD、授权 / 撤权、权限点求值
- [x] 审计:关键事件落库,支持按主体/动作/时间检索

### 单元测试
- [x] 密码策略边界(7 位 / 8 位 / 128 位 / 129 位)
- [x] 账号锁定与解锁
- [x] 会话过期逻辑(用 `clock` mock)
- [x] RBAC 权限求值(含通配符)

**验收**:注册 → 登录 → 拿到会话 → 访问受保护接口全链路通,单测覆盖关键路径。

---

## M3 · OIDC 授权服务

**目标**:业务系统接入的授权服务器。

### fosite 集成
- [x] 接入 `ory/fosite`,配置存储实现(`internal/oidc/storage.go` 是唯一适配点)
- [x] `oidc.client` 表(redirect_uris / grant_types / scopes / PKCE 强制)
- [x] 支持 flow:授权码 + PKCE(强制,S256 only)、refresh token、client_credentials
- [x] 密钥管理:`KEY_MASTER_SECRET` 加密私钥存储,`kid` 形如 `oidc-<random>`,支持轮换
- [x] JWKS 端点,`kid` 与私钥一一对应

> **实现偏差(已定稿)**:
> - 未装配 ROPC 与 RFC 7523 断言流程。OAuth 2.1 移除了前者;后者需要「可信任的对等服务端」,
>   本项目没有。两者对应的 `grant_type` 会按 RFC 6749 返回错误,而不是留一段永远失败的代码。
> - 客户端密钥用 bcrypt 而非 argon2id:密钥是 256 位随机串,没有字典可爆破,内存硬 KDF
>   不带来额外收益;且 fosite 的 `SecretsHasher` 默认实现就是 bcrypt。
> - PKCE 挑战存在独立的 `oidc.pkce_request` 表,`oidc.authorization_code` 上的两列
>   只是冗余快照(见迁移 00006 / 00007)。

### 端点
- [x] `GET /oauth/.well-known/openid-configuration`(discovery)
- [x] `GET /oauth/.well-known/jwks.json`
- [x] `GET /oauth/authorize`、`POST /oauth/authorize/decision`(同意页)
- [x] `POST /oauth/token`
- [x] `GET|POST /oauth/userinfo`
- [x] `POST /oauth/introspect`、`POST /oauth/revoke`
- [x] `GET|POST /oauth/endsession`(RP-initiated logout)
- [x] `POST /oauth/par`(Pushed Authorization Request,RFC 9126)
- [x] `POST /oauth/device/auth` + `GET /api/device` / `POST /api/device/decision`(设备码,RFC 8628)

### SSO
- [x] `GET /api/sso/status`:已登录直接发码,未登录由 `/oauth/authorize` 跳登录页
- [x] `POST /api/sso/decision`、`POST /api/sso/logout`
- [x] Cookie 域按 `APP_PUBLIC_DOMAIN` 自动推导父域

### 限流
- [x] `authorize` / `token` / `device` 独立限流维度(IP)

**验收**:用标准 OIDC 客户端能完整走通授权码流程。
**实测**:`internal/oidc` 集成测试覆盖 discovery、JWKS、授权码 + PKCE 全链路、
授权码重放拒绝、PKCE 校验、回调地址精确匹配、内省客户端认证、设备码四态、PAR。

---

## M4 · MC 认证域

**目标**:Minecraft 服务端能通过 authlib-injector 认证。

### Yggdrasil 协议
- [x] `POST /mc/authenticate`(用户名 + 密码 → accessToken)
- [x] `POST /mc/refresh`、`POST /mc/validate`、`POST /mc/invalidate`
- [x] `POST /mc/signout`
- [x] `GET /mc/`(metadata,含 skinDomains)
- [x] `GET /mc/hasJoined?username=&serverId=`(**HMAC-SHA1 五生效点校验**)
- [x] `POST /mc/join`(用 serverId 生成 clientToken)
- [x] `GET /mc/profile/:uuid`、`POST /mc/profiles/minecraft`

### 数据模型
- [x] `mc_profile`:uuid ↔ account 一对一映射
- [x] `mc_name_history`:改名历史,旧名保留一段时间
- [x] `mc_access_token`:与 OAuth token 完全隔离的独立表
- [x] `mc_signing_key`:MC 域独立签名密钥(ADR-003)

### 独立性
- [x] 签发的 token 与 OAuth token **类型、存储、签名密钥全不相同**
- [x] `mc_login_enabled=false` 的账号在 `authenticate` 明确拒绝
- [x] 域故障(签名密钥丢失 / DB 不可用)不影响 OIDC 链路

**验收**:通过协议级测试验证签名可验证、防重放、离线服务器正确拒绝。

**实现偏差说明**:

1. 表名以基线迁移 `00003_init_minecraft.sql` 为准:`minecraft.profile`、
   `minecraft.name_history`、`minecraft.access_token`、`minecraft.signing_key`。
2. `/mc/join` **不生成** clientToken,而是登记一份带时间窗的进服会话
   (`minecraft.server_session`)。clientToken 由客户端提供且在刷新时保持不变——
   MC 客户端用它标识「哪台设备在登录」,换掉它会让客户端要求重新选档案。
3. `hasJoined` 收到的是**签名**(serverIdHash)而非原始 serverId,因此
   实现改为:在候选会话里逐个用 `serverId + secret + uuid` 重算比对。
   这也是协议的真实语义,而不是把签名和 serverId 自身比较。
4. `/mc/skin/:uuidOrName`、`/mc/avatar/:uuidOrName`、`/mc/textures/:hash` 
   属于皮肤站,留到 M5 实现,已在本文档末尾标注。
5. 账号侧的 MC 端点(改名、开关、档案查询)挂在 `/api/account/mc/*`,
   与协议端点共用同一个服务但走统一响应包。

**未实测部分**:真实 MC 服务端 + authlib-injector 的端到端进服需要外部 
Java 运行环境,本环境不具备。当前为**协议级验证通过,端到端未实测**。

---

## M6 · 前端

**目标**:两个 Vue 应用,构建产物 `go:embed` 进二进制。

### account-web(终端用户)
- [ ] 技术栈:Vue 3 + TS + Vite + Vue Router + Pinia + Ant Design Vue 4
- [ ] 页面:登录、注册、忘记密码、重置密码、邮箱验证、账号概览、安全设置、皮肤管理
- [ ] 品牌主题:按 `?client_id=app|mc` 切换主色与标题
- [ ] 校验规则**与后端共用同一份定义**(由 OpenAPI 生成或手写共享 TS)

### admin-web(管理后台)
- [ ] 页面:仪表盘、账号管理、角色权限、邀请、审计日志、OIDC 客户端、密钥轮换、MC 档案、材质库、设置
- [ ] 菜单由后端下发,按权限点过滤
- [ ] 无权限路由直接输 URL 也被前端 `RequirePermission` 拦下

### 构建集成
- [ ] `web:embed` 构建目标:pnpm build → `go:embed` 进二进制
- [ ] 静态资源路由:未匹配路径回退 `index.html`(SPA 模式)
- [ ] 前端 API 层统一错误处理与 token 刷新

**验收**:两个 SPA 构建产物嵌入后,单进程启动即可访问全部页面。

---

## M7 · 部署与加固

**目标**:能上生产的完整交付。

### 容器化
- [ ] 多阶段 `Dockerfile`:Go 构建 → distroless 运行镜像
- [ ] 前端构建阶段:Node 构建 → 产物拷贝进 Go 构建上下文
- [ ] `docker-compose.yml`:`app` + `postgres` + `nginx`
- [ ] 健康检查与依赖顺序

### 配置与密钥
- [ ] `.env.example` 完整列出,必填项标注
- [ ] 启动时校验必填项,缺失直接退出并指出缺哪个

### 测试
- [ ] 集成测试:PostgreSQL testcontainer 跑 OIDC + MC 全链路
- [ ] 故障注入测试:DB 连接池耗尽 / 磁盘满 / 外部皮肤站宕机
- [ ] 域隔离测试:MC 域 5xx 时 OIDC 与 admin 全部 200

### 安全
- [ ] 依赖漏洞扫描(Trivy / `govulncheck`)
- [ ] 密钥不落日志、不入 Git
- [ ] CORS / CSRF / 限流复核
- [ ] 见 [09-security.md](./09-security.md)

### 文档
- [ ] 全部文档与代码对齐,无悬空链接
- [ ] 部署文档走查一遍(照着文档实际部署一次)

**验收**:在一台干净机器上 `cp .env.example .env && docker compose up -d` 即可跑起。

---

## 关键路径与风险

### 关键路径
```
M0 → M1 → M2 → M3 → M6 → M7
                ↘ M4 → M5 ↗
```
M2(账号内核)是所有业务的共同前置,是最大瓶颈,应优先投入。

### 风险清单

| 风险 | 影响 | 应对 |
|---|---|---|
| fosite 学习曲线陡 | M3 延期 | 先用官方 example 跑通,再集成 |
| Yggdrasil 协议文档缺失 | M4 延期 | 以 authlib-injector 源码和 Mojang 服务端行为为参照,协议级测试兜底 |
| 头像渲染性能 | M5 延期 | 有界队列 + 预生成 + 缓存,不追求实时 |
| 前端工作量被低估 | M6 延期 | 组件复用优先,不做过度设计 |
| 无真实 MC 服务端环境 | M4/M5 无法端到端验证 | 明确记录为协议级验证,交付时如实说明 |

---

**上一篇**:[00-overview.md](./00-overview.md) —— 项目总纲
**下一篇**:[02-architecture.md](./02-architecture.md) —— 架构设计
# 01 · 路线图与任务分解

> 本文件把重构拆成 **8 个里程碑**。每个里程碑独立可交付、可验证,结束时项目处于可运行状态。

## 里程碑总览

| # | 里程碑 | 交付物 | 依赖 | 预估 |
|---|---|---|---|---|
| M0 | 工程地基 | 仓库骨架、工具链、CI | — | 1 天 |
| M1 | 基础设施 | 配置、日志、DB、迁移、错误体系 | M0 | 2 天 |
| M2 | 账号内核 | 账号、凭据、会话、RBAC、审计 | M1 | 5 天 |
| M3 | OIDC 授权服务 | fosite 集成、SSO、客户端管理 | M2 | 5 天 |
| M4 | MC 认证域 | Yggdrasil 协议、profile、别名 | M2 | 4 天 |
| M5 | 皮肤站 | 上传、存储、去重、头像渲染 | M4 | 4 天 |
| M6 | 前端 | account-web + admin-web | M3,M4 | 6 天 |
| M7 | 部署与加固 | 容器化、集成测试、压测、安全检查 | 全部 | 4 天 |

**合计约 31 人日**。单人全职约 6 周,两人并行约 4 周(前端与后端可重叠)。

---

## M0 · 工程地基

**目标**:能编译、能跑测试、能起一个空服务。

- [x] 初始化 Go module `github.com/yggauth/yggauth`
- [x] 目录骨架:`cmd/`、`internal/`、`db/`、`web/`、`deploy/`
- [x] `Makefile`:`make build` / `make test` / `make lint` / `make dev` / `make sqlc` / `make migrate`
- [x] 工具链版本锁定:Go 1.27、Node 24、pnpm 11(`.tool-versions`)
- [x] lint 配置:`golangci-lint`(含 `gofumpt`、`errcheck`、`staticcheck`)
- [x] GitHub Actions:`build` + `test` + `lint` 三 job
- [x] `cmd/yggauth/main.go` 骨架,`GET /health/live` 返回 200

**验收**:空服务能启动,CI 全绿。

---

## M1 · 基础设施

**目标**:把「与业务无关的通用能力」建成可复用底座。

### 配置
- [x] `internal/config`:环境变量加载 + 类型转换 + 启动即校验(失败直接退出,带清晰错误)
- [x] 配置项分组:`app` / `db` / `auth` / `storage` / `oidc` / `mc` / `mail` / `log`
- [x] 敏感项(数据库密码、`KEY_MASTER_SECRET`)启动日志中脱敏

### 日志与可观测
- [x] `internal/platform/log`:基于 `log/slog` 的 JSON 结构化日志,带 `request_id`、`domain`、`account_id` 上下文
- [x] `/health/live`(存活)、`/health/ready`(就绪,探 DB)、`/metrics`(Prometheus)

### 数据库
- [x] `internal/platform/db`:连接池(`pgx/v5` + `pgxpool`)、事务辅助函数、`Ping` 自检
- [x] `db/migrations/`:goose 迁移目录,首版 baseline
- [x] `db/queries/`:sqlc 查询定义
- [x] `make sqlc` 生成,CI 校验生成物与源码一致

### 错误与响应
- [x] `internal/platform/apperr`:业务错误码枚举 → HTTP 状态码映射
- [x] 统一响应包 `{"code":0,"message":"ok","data":{...}}`
- [x] 全局 error handler:内部错误只回泛化消息,细节进日志
- [x] request ID 中间件 + panic recover 中间件

### 工具
- [x] `internal/platform/uuid`、`internal/platform/clock`(便于测试注入)、`internal/platform/rand`

**验收**:`make migrate` 能建库,`/health/ready` 返回 DB 状态,非法配置启动即失败。

---

## M2 · 账号内核

**目标**:域中立的身份能力。**不含任何 OIDC / MC 语义**。

### 数据模型
- [x] `account` 表(含 `email`、`username`、`email_verified_at`、`status`、`mc_login_enabled`)
- [x] `credential` 表(argon2id hash,支持未来多种算法)
- [x] `session` 表(**统一模型**,见 [04-decisions.md](./04-decisions.md) ADR-004)
- [x] `role` / `permission` / `role_permission` / `account_role`(**单一 RBAC**,ADR-005)
- [x] `audit_event` 表

### 功能
- [x] 注册:邮箱格式校验、用户名唯一性、密码策略(8–128)、邮箱验证令牌
- [x] 登录:argon2id 校验、失败锁定(N 次 / M 分钟)、会话签发
- [x] 会话:滑动过期、绝对过期、登出、列出活跃会话、踢下线
- [x] 密码重置:请求(不泄露账号是否存在)→ 邮件令牌 → 重置
- [x] 邮件验证:令牌 + 重发冷却
- [x] RBAC:角色 CRUD、授权 / 撤权、权限点求值
- [x] 审计:关键事件落库,支持按主体/动作/时间检索

### 单元测试
- [x] 密码策略边界(7 位 / 8 位 / 128 位 / 129 位)
- [x] 账号锁定与解锁
- [x] 会话过期逻辑(用 `clock` mock)
- [x] RBAC 权限求值(含通配符)

**验收**:注册 → 登录 → 拿到会话 → 访问受保护接口全链路通,单测覆盖关键路径。

---

## M3 · OIDC 授权服务

**目标**:业务系统接入的授权服务器。

### fosite 集成
- [x] 接入 `ory/fosite`,配置存储实现(`internal/oidc/storage.go` 是唯一适配点)
- [x] `oidc.client` 表(redirect_uris / grant_types / scopes / PKCE 强制)
- [x] 支持 flow:授权码 + PKCE(强制,S256 only)、refresh token、client_credentials
- [x] 密钥管理:`KEY_MASTER_SECRET` 加密私钥存储,`kid` 形如 `oidc-<random>`,支持轮换
- [x] JWKS 端点,`kid` 与私钥一一对应

> **实现偏差(已定稿)**:
> - 未装配 ROPC 与 RFC 7523 断言流程。OAuth 2.1 移除了前者;后者需要「可信任的对等服务端」,
>   本项目没有。两者对应的 `grant_type` 会按 RFC 6749 返回错误,而不是留一段永远失败的代码。
> - 客户端密钥用 bcrypt 而非 argon2id:密钥是 256 位随机串,没有字典可爆破,内存硬 KDF
>   不带来额外收益;且 fosite 的 `SecretsHasher` 默认实现就是 bcrypt。
> - PKCE 挑战存在独立的 `oidc.pkce_request` 表,`oidc.authorization_code` 上的两列
>   只是冗余快照(见迁移 00006 / 00007)。

### 端点
- [x] `GET /oauth/.well-known/openid-configuration`(discovery)
- [x] `GET /oauth/.well-known/jwks.json`
- [x] `GET /oauth/authorize`、`POST /oauth/authorize/decision`(同意页)
- [x] `POST /oauth/token`
- [x] `GET|POST /oauth/userinfo`
- [x] `POST /oauth/introspect`、`POST /oauth/revoke`
- [x] `GET|POST /oauth/endsession`(RP-initiated logout)
- [x] `POST /oauth/par`(Pushed Authorization Request,RFC 9126)
- [x] `POST /oauth/device/auth` + `GET /api/device` / `POST /api/device/decision`(设备码,RFC 8628)

### SSO
- [x] `GET /api/sso/status`:已登录直接发码,未登录由 `/oauth/authorize` 跳登录页
- [x] `POST /api/sso/decision`、`POST /api/sso/logout`
- [x] Cookie 域按 `APP_PUBLIC_DOMAIN` 自动推导父域

### 限流
- [x] `authorize` / `token` / `device` 独立限流维度(IP)

**验收**:用标准 OIDC 客户端能完整走通授权码流程。
**实测**:`internal/oidc` 集成测试覆盖 discovery、JWKS、授权码 + PKCE 全链路、
授权码重放拒绝、PKCE 校验、回调地址精确匹配、内省客户端认证、设备码四态、PAR。

---

## M4 · MC 认证域

**目标**:Minecraft 服务端能通过 authlib-injector 认证。

### Yggdrasil 协议
- [x] `POST /mc/authenticate`(用户名 + 密码 → accessToken)
- [x] `POST /mc/refresh`、`POST /mc/validate`、`POST /mc/invalidate`
- [x] `POST /mc/signout`
- [x] `GET /mc/`(metadata,含 skinDomains)
- [x] `GET /mc/hasJoined?username=&serverId=`(**HMAC-SHA1 五生效点校验**)
- [x] `POST /mc/join`(用 serverId 生成 clientToken)
- [x] `GET /mc/profile/:uuid`、`POST /mc/profiles/minecraft`

### 数据模型
- [x] `mc_profile`:uuid ↔ account 一对一映射
- [x] `mc_name_history`:改名历史,旧名保留一段时间
- [x] `mc_access_token`:与 OAuth token 完全隔离的独立表
- [x] `mc_signing_key`:MC 域独立签名密钥(ADR-003)

### 独立性
- [x] 签发的 token 与 OAuth token **类型、存储、签名密钥全不相同**
- [x] `mc_login_enabled=false` 的账号在 `authenticate` 明确拒绝
- [x] 域故障(签名密钥丢失 / DB 不可用)不影响 OIDC 链路

**验收**:通过协议级测试验证签名可验证、防重放、离线服务器正确拒绝。

**实现偏差说明**:

1. 表名以基线迁移 `00003_init_minecraft.sql` 为准:`minecraft.profile`、
   `minecraft.name_history`、`minecraft.access_token`、`minecraft.signing_key`。
2. `/mc/join` **不生成** clientToken,而是登记一份带时间窗的进服会话
   (`minecraft.server_session`)。clientToken 由客户端提供且在刷新时保持不变——
   MC 客户端用它标识「哪台设备在登录」,换掉它会让客户端要求重新选档案。
3. `hasJoined` 收到的是**签名**(serverIdHash)而非原始 serverId,因此
   实现改为:在候选会话里逐个用 `serverId + secret + uuid` 重算比对。
   这也是协议的真实语义,而不是把签名和 serverId 自身比较。
4. `/mc/skin/:uuidOrName`、`/mc/avatar/:uuidOrName`、`/mc/textures/:hash` 
   属于皮肤站,留到 M5 实现,已在本文档末尾标注。
5. 账号侧的 MC 端点(改名、开关、档案查询)挂在 `/api/account/mc/*`,
   与协议端点共用同一个服务但走统一响应包。

**未实测部分**:真实 MC 服务端 + authlib-injector 的端到端进服需要外部 
Java 运行环境,本环境不具备。当前为**协议级验证通过,端到端未实测**。

---


**目标**:两个 Vue 应用,构建产物 `go:embed` 进二进制。

### account-web(终端用户)
- [ ] 技术栈:Vue 3 + TS + Vite + Vue Router + Pinia + Ant Design Vue 4
- [ ] 页面:登录、注册、忘记密码、重置密码、邮箱验证、账号概览、安全设置、皮肤管理
- [ ] 品牌主题:按 `?client_id=app|mc` 切换主色与标题
- [ ] 校验规则**与后端共用同一份定义**(由 OpenAPI 生成或手写共享 TS)

### admin-web(管理后台)
- [ ] 页面:仪表盘、账号管理、角色权限、邀请、审计日志、OIDC 客户端、密钥轮换、MC 档案、材质库、设置
- [ ] 菜单由后端下发,按权限点过滤
- [ ] 无权限路由直接输 URL 也被前端 `RequirePermission` 拦下

### 构建集成
- [ ] `web:embed` 构建目标:pnpm build → `go:embed` 进二进制
- [ ] 静态资源路由:未匹配路径回退 `index.html`(SPA 模式)
- [ ] 前端 API 层统一错误处理与 token 刷新

**验收**:两个 SPA 构建产物嵌入后,单进程启动即可访问全部页面。

---

## M7 · 部署与加固

**目标**:能上生产的完整交付。

### 容器化
- [ ] 多阶段 `Dockerfile`:Go 构建 → distroless 运行镜像
- [ ] 前端构建阶段:Node 构建 → 产物拷贝进 Go 构建上下文
- [ ] `docker-compose.yml`:`app` + `postgres` + `nginx`
- [ ] 健康检查与依赖顺序

### 配置与密钥
- [ ] `.env.example` 完整列出,必填项标注
- [ ] 启动时校验必填项,缺失直接退出并指出缺哪个

### 测试
- [ ] 集成测试:PostgreSQL testcontainer 跑 OIDC + MC 全链路
- [ ] 故障注入测试:DB 连接池耗尽 / 磁盘满 / 外部皮肤站宕机
- [ ] 域隔离测试:MC 域 5xx 时 OIDC 与 admin 全部 200

### 安全
- [ ] 依赖漏洞扫描(Trivy / `govulncheck`)
- [ ] 密钥不落日志、不入 Git
- [ ] CORS / CSRF / 限流复核
- [ ] 见 [09-security.md](./09-security.md)

### 文档
- [ ] 全部文档与代码对齐,无悬空链接
- [ ] 部署文档走查一遍(照着文档实际部署一次)

**验收**:在一台干净机器上 `cp .env.example .env && docker compose up -d` 即可跑起。

---

## 关键路径与风险

### 关键路径
```
M0 → M1 → M2 → M3 → M6 → M7
                ↘ M4 → M5 ↗
```
M2(账号内核)是所有业务的共同前置,是最大瓶颈,应优先投入。

### 风险清单

| 风险 | 影响 | 应对 |
|---|---|---|
| fosite 学习曲线陡 | M3 延期 | 先用官方 example 跑通,再集成 |
| Yggdrasil 协议文档缺失 | M4 延期 | 以 authlib-injector 源码和 Mojang 服务端行为为参照,协议级测试兜底 |
| 头像渲染性能 | M5 延期 | 有界队列 + 预生成 + 缓存,不追求实时 |
| 前端工作量被低估 | M6 延期 | 组件复用优先,不做过度设计 |
| 无真实 MC 服务端环境 | M4/M5 无法端到端验证 | 明确记录为协议级验证,交付时如实说明 |

---

**上一篇**:[00-overview.md](./00-overview.md) —— 项目总纲
**下一篇**:[02-architecture.md](./02-architecture.md) —— 架构设计
## M5 · 皮肤站

**目标**:玩家上传皮肤/披风,获取材质 URL 与头像。

### 存储
- [x] `internal/platform/storage`:接口定义 `Put` / `Get` / `Delete` / `Exists`
- [x] 本地磁盘实现:`data/textures/<sha256前2位>/<sha256>.png`
- [x] 文件权限 0644,目录 0755

### 材质
- [x] `mc_texture` 表:`hash`(sha256 唯一)、`type`(skin/cape)、`size`、`mime`
- [x] 上传校验:PNG 魔数、尺寸合法(skin 64×64/64×32/64×32,cape 64×32)、大小上限 2MB
- [x] **sha256 去重**:相同内容只存一份,数据库记引用
- [x] 上传接口:`PUT /api/account/mc/texture`

### 头像
- [x] 渲染:skin → 头像(头部区域裁切 + 第二层叠加)
- [x] **异步渲染**:有界任务队列,渲染慢不阻塞 HTTP 请求
- [x] `mc_avatar` 缓存表,渲染失败降级为默认头像
- [x] `GET /mc/avatar/:uuidOrName`

### 外部回源
- [x] `MC_SKIN_EXTERNAL=true` 时,材质缺失且账号有外部绑定时回源
- [x] 独立 HTTP 客户端 + 超时 + 熔断 + 失败降级默认皮肤
- [x] 回源结果入缓存,供下次直接命中

### 只读模式
- [x] `MC_READONLY=true` 时禁止上传,下载不受影响

**验收**:上传 → 去重 → 下载 → 头像生成全链路通,大文件不拖垮请求处理。

**实现偏差说明**:

1. `mc_texture` 的实际表名是 `minecraft.texture`,绑定关系在
   `minecraft.profile_texture`。基线迁移 `00003` 已定型,M5 未新增表。
2. 头像缓存表 `minecraft.avatar` 只有 `(profile_id, hash, rendered_at)`,
   **不存图像** —— 图像落在 `storage` 的 `avatars/<uuid>.png`。
   这与基线设计一致:一次 SELECT 不该拖上几 MB 二进制,
   而这里需要的只是「缓存是否失效」这一个判断。
3. `/mc/avatar/:id?size=N` 按请求尺寸渲染。因为基线表没有尺寸列,
   同一玩家在不同尺寸下会各自渲染一次。
4. 外部回源按 `minecraft.external_binding` 的 `provider` + `base_url` 取数据,
   而不是只读配置 —— 换站不需要重启服务。
5. 新增配置 `MC_SKIN_EXTERNAL_BASE_URL`(默认取 `PUBLIC_BASE_URL`)。
6. **修正**:`TEXTURE_DIR` 原先被 `DATA_DIR` 无条件覆盖,配了等于没配。
   现改为仅在未显式设置时推导默认值,`AVATAR_DIR` 补上了同样的默认值。

