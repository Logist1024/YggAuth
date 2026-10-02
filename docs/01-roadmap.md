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
- [ ] `internal/config`:环境变量加载 + 类型转换 + 启动即校验(失败直接退出,带清晰错误)
- [ ] 配置项分组:`app` / `db` / `auth` / `storage` / `oidc` / `mc` / `mail` / `log`
- [ ] 敏感项(数据库密码、`KEY_MASTER_SECRET`)启动日志中脱敏

### 日志与可观测
- [ ] `internal/platform/log`:基于 `log/slog` 的 JSON 结构化日志,带 `request_id`、`domain`、`account_id` 上下文
- [ ] `/health/live`(存活)、`/health/ready`(就绪,探 DB)、`/metrics`(Prometheus)

### 数据库
- [ ] `internal/platform/db`:连接池(`pgx/v5` + `pgxpool`)、事务辅助函数、`Ping` 自检
- [ ] `db/migrations/`:goose 迁移目录,首版 baseline
- [ ] `db/queries/`:sqlc 查询定义
- [ ] `make sqlc` 生成,CI 校验生成物与源码一致

### 错误与响应
- [ ] `internal/platform/apperr`:业务错误码枚举 → HTTP 状态码映射
- [ ] 统一响应包 `{"code":0,"message":"ok","data":{...}}`
- [ ] 全局 error handler:内部错误只回泛化消息,细节进日志
- [ ] request ID 中间件 + panic recover 中间件

### 工具
- [ ] `internal/platform/uuid`、`internal/platform/clock`(便于测试注入)、`internal/platform/rand`

**验收**:`make migrate` 能建库,`/health/ready` 返回 DB 状态,非法配置启动即失败。

---

## M2 · 账号内核

**目标**:域中立的身份能力。**不含任何 OIDC / MC 语义**。

### 数据模型
- [ ] `account` 表(含 `email`、`username`、`email_verified_at`、`status`、`mc_login_enabled`)
- [ ] `credential` 表(argon2id hash,支持未来多种算法)
- [ ] `session` 表(**统一模型**,见 [04-decisions.md](./04-decisions.md) ADR-004)
- [ ] `role` / `permission` / `role_permission` / `account_role`(**单一 RBAC**,ADR-005)
- [ ] `audit_event` 表

### 功能
- [ ] 注册:邮箱格式校验、用户名唯一性、密码策略(8–128)、邮箱验证令牌
- [ ] 登录:argon2id 校验、失败锁定(N 次 / M 分钟)、会话签发
- [ ] 会话:滑动过期、绝对过期、登出、列出活跃会话、踢下线
- [ ] 密码重置:请求(不泄露账号是否存在)→ 邮件令牌 → 重置
- [ ] 邮件验证:令牌 + 重发冷却
- [ ] RBAC:角色 CRUD、授权 / 撤权、权限点求值
- [ ] 审计:关键事件落库,支持按主体/动作/时间检索

### 单元测试
- [ ] 密码策略边界(7 位 / 8 位 / 128 位 / 129 位)
- [ ] 账号锁定与解锁
- [ ] 会话过期逻辑(用 `clock` mock)
- [ ] RBAC 权限求值(含通配符)

**验收**:注册 → 登录 → 拿到会话 → 访问受保护接口全链路通,单测覆盖关键路径。

---

## M3 · OIDC 授权服务

**目标**:业务系统接入的授权服务器。

### fosite 集成
- [ ] 接入 `ory/fosite`,配置存储实现
- [ ] `oauth_client` 表(redirect_uris / grant_types / scopes / PKCE 强制)
- [ ] 支持 flow:授权码 + PKCE(强制)、refresh token、client_credentials
- [ ] 密钥管理:`KEY_MASTER_SECRET` 加密私钥存储,`kid` 形如 `oidc-<random>`,支持轮换
- [ ] JWKS 端点,`kid` 与私钥一一对应

### 端点
- [ ] `GET /oauth/.well-known/openid-configuration`(discovery)
- [ ] `GET /oauth/.well-known/jwks.json`
- [ ] `GET /oauth/authorize`、`POST /oauth/token`
- [ ] `GET|POST /oauth/userinfo`
- [ ] `POST /oauth/introspect`、`POST /oauth/revoke`
- [ ] `GET|POST /oauth/endsession`(RP-initiated logout)
- [ ] `POST /oauth/par`(Pushed Authorization Request)、`POST /oauth/device`(可选,视时间)

### SSO
- [ ] `GET /api/sso/authorize`:已登录则静默发码,未登录则跳登录页
- [ ] `POST /api/sso/logout`
- [ ] Cookie 域按 `APP_PUBLIC_DOMAIN` 自动推导父域

### 限流
- [ ] `authorize` / `token` / `userinfo` 独立限流维度(IP / 账号)

**验收**:用标准 OIDC 客户端(如 `oauth2-proxy` / `keycloak-gatekeeper`)能完整走通授权码流程。

---

## M4 · MC 认证域

**目标**:Minecraft 服务端能通过 authlib-injector 认证。

### Yggdrasil 协议
- [ ] `POST /mc/authenticate`(用户名 + 密码 → accessToken)
- [ ] `POST /mc/refresh`、`POST /mc/validate`、`POST /mc/invalidate`
- [ ] `POST /mc/signout`
- [ ] `GET /mc/`(metadata,含 skinDomains)
- [ ] `GET /mc/hasJoined?username=&serverId=`(**HMAC-SHA1 五生效点校验**)
- [ ] `POST /mc/join`(用 serverId 生成 clientToken)
- [ ] `GET /mc/profile/:uuid`、`POST /mc/profiles/minecraft`

### 数据模型
- [ ] `mc_profile`:uuid ↔ account 一对一映射
- [ ] `mc_name_history`:改名历史,旧名保留一段时间
- [ ] `mc_access_token`:与 OAuth token 完全隔离的独立表
- [ ] `mc_signing_key`:MC 域独立签名密钥(ADR-003)

### 独立性
- [ ] 签发的 token 与 OAuth token **类型、存储、签名密钥全不相同**
- [ ] `mc_login_enabled=false` 的账号在 `authenticate` 明确拒绝
- [ ] 域故障(签名密钥丢失 / DB 不可用)不影响 OIDC 链路

**验收**:通过协议级测试验证签名可验证、防重放、离线服务器正确拒绝。

---

## M5 · 皮肤站

**目标**:玩家上传皮肤/披风,获取材质 URL 与头像。

### 存储
- [ ] `internal/platform/storage`:接口定义 `Put` / `Get` / `Delete` / `Exists`
- [ ] 本地磁盘实现:`data/textures/<sha256前2位>/<sha256>.png`
- [ ] 文件权限 0644,目录 0755

### 材质
- [ ] `mc_texture` 表:`hash`(sha256 唯一)、`type`(skin/cape)、`size`、`mime`
- [ ] 上传校验:PNG 魔数、尺寸合法(skin 64×64/64×32/64×64,cape 64×32)、大小上限 2MB
- [ ] **sha256 去重**:相同内容只存一份,数据库记引用
- [ ] 上传接口:`PUT /api/account/mc/texture`

### 头像
- [ ] 渲染:skin → 64×64 头像(头部区域裁切)
- [ ] **异步渲染**:有界任务队列,渲染慢不阻塞 HTTP 请求
- [ ] `mc_avatar` 缓存表,渲染失败降级为默认头像
- [ ] `GET /mc/avatar/:uuidOrName`

### 外部回源
- [ ] `MC_SKIN_EXTERNAL=true` 时,材质缺失且账号有外部绑定时回源
- [ ] 独立 HTTP 客户端 + 超时 + 熔断 + 失败降级默认皮肤
- [ ] 回源结果入缓存,供下次直接命中

### 只读模式
- [ ] `MC_READONLY=true` 时禁止上传,下载不受影响

**验收**:上传 → 去重 → 下载 → 头像生成全链路通,大文件不拖垮请求处理。

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
