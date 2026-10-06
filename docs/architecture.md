# 架构设计

> 本文件描述 YggAuth 的分层架构、目录结构、依赖方向与关键约定。**先读这篇**,再读其他文档。

---

## 一、设计原则

1. **依赖单向**:上层依赖下层,反向依赖即错误。编译器帮不上忙,但 `go list` 能查。
2. **域中立内核**:账号内核(`internal/identity`)不得出现 `oidc` / `mc` / `minecraft` / `skin` 等业务词。它只认「身份 + 凭据 + 会话 + 权限」。
3. **业务域可插拔**:新增登录域(SAML / LDAP)只加目录,不动内核。
4. **显式优于隐式**:SQL 显式写、错误显式处理、依赖显式注入,不靠框架魔法。
5. **失败局部化**:一个域挂了,其他域必须照常工作(ADR-003/004)。

---

## 二、分层架构

```
┌─────────────────────────────────────────────────────────┐
│  cmd/yggauth            进程入口、优雅退出                │
├─────────────────────────────────────────────────────────┤
│  internal/transport     HTTP 路由、中间件、handler 装配   │
│  ┌──────────────┬──────────────┬──────────────────┐     │
│  │  identity    │   oidc       │    minecraft     │     │
│  │  (账号内核)   │  (业务域)     │    (游戏域)      │     │
│  └──────────────┴──────────────┴──────────────────┘     │
│  ┌──────────────────────────────────────────────────┐   │
│  │  admin  (后台 RBAC 门面,复用 identity 的权限模型)  │   │
│  └──────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────┤
│  internal/platform      配置/日志/DB/错误/存储/限流/密钥  │
│  internal/domain         跨域共享的值对象与接口           │
├─────────────────────────────────────────────────────────┤
│  db/{migrations,queries}  goose 迁移 + sqlc 查询         │
│  web/                    Vue 源码(构建产物 embed 进二进制) │
└─────────────────────────────────────────────────────────┘
```

**依赖方向**: `transport → 业务域 → platform`,`identity` 不依赖任何业务域(ADR-010)。

与旧版 NestJS 的关键区别:**不再有「域插件注册中心」那套启动期反射校验**()。Go 侧改用编译期装配 —— 在 `cmd/yggauth/main.go` 里显式 import 三个域,少一个域编译就过不了(ADR-011)。

---

## 三、目录结构

```
yggauth/
├── cmd/yggauth/            # 进程入口
│   ├── main.go             # 子命令路由(serve/migrate/admin/healthcheck/version)
│   └── wire.go             # 依赖注入装配
├── internal/
│   ├── config/             # 配置加载与校验(.env.example 是唯一真源)
│   │   ├── config.go       # 全量配置结构体
│   │   ├── settings.go     # Settings 运行时读取接口
│   │   └── keys.go         # 键注册表(新键必须登记)
│   ├── platform/           # 平台能力(与业务无关)
│   │   ├── log/            # slog 结构化日志
│   │   ├── db/             # pgx 连接池、事务、迁移封装
│   │   ├── httpx/          # 中间件、响应封装、错误码映射
│   │   ├── apperr/         # 业务错误码(见 docs/api.md 第二节)
│   │   ├── keys/           # 签名密钥管理(PG 存储)
│   │   ├── mailer/         # 邮件发送(支持热替换)
│   │   ├── ratelimit/      # 限流
│   │   ├── storage/        # 文件存储接口 + 本地实现
│   │   └── health/         # 健康检查端点
│   ├── domain/             # 跨域共享值对象
│   │   └── account.go      # Account、Session、Permission 等
│   ├── identity/           # ★ 账号内核(域中立)
│   │   ├── account/        # 账号与凭据注册/登录
│   │   ├── session/        # 会话签发/吊销
│   │   ├── rbac/           # 角色与权限
│   │   ├── audit/          # 审计日志(只追加)
│   │   └── identity.go     # 内核对外接口定义
│   ├── oidc/               # ★ 业务系统登录域(OIDC/OAuth2.1)
│   │   ├── server.go       # fosite provider 装配
│   │   ├── storage.go      # fosite 存储接口实现
│   │   ├── client.go       # OIDC 客户端管理
│   │   ├── sso.go          # 静默发码
│   │   └── handler.go      # HTTP 端点(/oauth/*)
│   ├── minecraft/          # ★ 游戏登录域(Yggdrasil + 皮肤站)
│   │   ├── protocol.go     # Yggdrasil 协议端点
│   │   ├── profile.go      # 玩家档案与改名
│   │   ├── texture.go      # 皮肤/披风上传
│   │   ├── avatar.go       # 头像渲染
│   │   └── external.go     # 外部皮肤站回源
│   ├── admin/              # 后台管理接口(/api/admin/*)
│   └── transport/          # HTTP 路由装配(唯一知道有哪些业务域的地方)
│       ├── router.go       # 路由注册
│       └── webserver.go    # go:embed 前端产物 + 静态文件托管
├── db/
│   ├── migrations/         # goose SQL 迁移(见 docs/data-model.md)
│   ├── queries/            # sqlc 查询定义
│   └── embed.go            # go:embed 迁移文件进二进制
├── web/                    # Vue 源码
│   ├── apps/account/       # 终端用户 SPA(端口 5173)
│   ├── apps/admin/         # 管理后台 SPA(端口 5174)
│   └── shared/             # 共享类型与工具
├── deploy/                 # Docker、nginx、postgres 初始化
├── scripts/                # check-terms.sh(术语门禁,ADR-010)
└── docs/                   # 本文档目录
```

---

## 四、关键约定

### 4.1 请求生命周期

```
HTTP request
  → transport 路由装配 (internal/transport/router.go)
  → 中间件链 (httpx 中间件:日志、CORS、CSRF、认证)
  → 业务 handler (identity/oidc/minecraft/admin)
  → 平台能力 (config, db, keys, mailer, storage, ratelimit)
  → 响应封装 (apperr → httpx.Response)
```

### 4.2 错误码与响应

- 所有 JSON 接口返回统一信封 `{"code":N,"message":"...","data":...}` (ADR-008)
- 错误码按区间划分:
  - `1xxx`:系统级错误
  - `2xxx`:账号相关(注册/登录/密码)
  - `3xxx`:权限/认证相关
  - `4xxx`:OIDC 相关
  - `5xxx`:MC 相关
- HTTP 状态码与业务 code 同时正确设置(ADR-008)
- 内部错误 Detail 只进日志,**绝不**返回给调用方(见 docs/security.md 第十节)

### 4.3 日志约定

- 使用 `log/slog` 结构化日志,零依赖
- 令牌、密钥**不**写进日志(见 docs/security.md 6.3)
- 启动日志只打配置摘要,敏感项脱敏

### 4.4 连接池

- `MaxConns` 默认按 CPU 核数 × 4 推断(见 `internal/platform/db/db.go`)
- 可通过 `DB_MAX_CONNS` 覆盖

---

## 五、明确不做的事

避免范围蔓延:

- **不做多租户 SaaS**。旧版的 `organization` 空壳表直接删除,不做企业组织树。
- **不做社交登录**(微信/GitHub/Google)。账号体系只保留邮箱 + 密码。
- **不做 S3/对象存储**。皮肤文件存本地磁盘,存储层留接口但本期不实现 S3 后端(ADR-007)。
- **不做移动端 App**。只做响应式 Web。
- **不做国际化(i18n)**。界面文案先用中文硬编码,结构上预留不强制。

---

## 六、域中立校验

`internal/identity` 与 `internal/platform` 的源码中**禁止**出现以下业务词:

- `oidc`, `oauth`
- `minecraft`, `mc`, `skin`, `cloak`, `yggdrasil`

门禁脚本:`scripts/check-terms.sh`,CI 跑 `make check-terms`(ADR-010)。

**豁免项**:
- `mc_login_enabled`:数据库列名,内核视作通用布尔开关,不承载业务语义(见 docs/data-model.md 第一节)
- 测试文件(`*_test.go`):测试需要能直接引用真实业务名词
- sqlc 生成物(`internal/platform/db/query/`):由 `db/queries` 派生,天然含业务语义

---

## 七、从旧版的重构要点

旧版(TypeScript + NestJS + SQLite)的三个结构性问题:

1. **文档与代码严重脱节** —— README 声称项目处于「T01 阶段」,实际所有功能均已交付;代码注释引用 `docs/12-api.md` 等不存在的文档。
2. **技术栈偏重** —— NestJS DI 体系为 1.8 万行代码带来大量装饰器样板;SQLite 强制单实例部署。
3. **领域模型冗余** —— 两套并行 RBAC、`organization` 空壳表、18 位密码上限。

本次重构全部清理(详见 docs/decisions.md)。
