# 02 · 架构设计

## 一、设计原则

1. **依赖单向**:上层依赖下层,反向依赖即错误。编译器帮不上忙,但评审和 `go list` 能查。
2. **域中立内核**:账号内核不得出现 `oidc` / `mc` / `skin` 任何字样。它只认「身份 + 凭据 + 会话 + 权限」。
3. **业务域可插拔**:新增登录域(SAML / LDAP / 企业微信)只加目录,不动内核。
4. **显式优于隐式**:SQL 显式写、错误显式处理、依赖显式注入,不靠框架魔法。
5. **失败要局部化**:一个域挂了,其他域必须照常工作。

## 二、分层架构

```
┌─────────────────────────────────────────────────────────┐
│  cmd/yggauth            进程入口、优雅退出                │
├─────────────────────────────────────────────────────────┤
│  internal/transport     HTTP 路由、中间件、handler 装配    │
│  ┌──────────────┬──────────────┬──────────────────┐     │
│  │  identity    │   oidc       │    minecraft     │     │
│  │  (账号内核)   │  (业务域)     │    (游戏域)      │     │
│  └──────────────┴──────────────┴──────────────────┘     │
│  ┌──────────────────────────────────────────────────┐   │
│  │  admin  (后台 RBAC 门面,复用 identity 的权限模型)  │   │
│  └──────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────┤
│  internal/platform      配置/日志/DB/错误/存储/限流/密钥   │
│  internal/domain         跨域共享的值对象与接口            │
├─────────────────────────────────────────────────────────┤
│  db/{migrations,queries}  goose 迁移 + sqlc 查询         │
│  web/                    Vue 源码(构建产物 embed 进二进制) │
└─────────────────────────────────────────────────────────┘
```

**依赖方向**:`transport → 业务域 → platform`,`identity` 不依赖任何业务域。

与旧版 NestJS 的关键区别:**不再有「域插件注册中心」那套启动期反射校验**。Go 侧改用编译期装配 —— 在 `cmd/yggauth/main.go` 里显式 import 三个域,少一个编译就过不了。这是用类型系统替代运行时校验,更简单也更可靠。

## 三、目录结构

```
yggauth/
├── cmd/
│   └── yggauth/
│       └── main.go              进程入口
├── internal/
│   ├── config/                  配置加载与校验
│   ├── platform/                平台能力(与业务无关)
│   │   ├── log/                 slog 结构化日志
│   │   ├── db/                  pgx 连接池、事务
│   │   ├── apperr/              错误码与 HTTP 映射
│   │   ├── httpx/               中间件、响应封装
│   │   ├── storage/             文件存储接口 + 本地实现
│   │   ├── keys/                签名密钥管理
│   │   ├── ratelimit/           限流
│   │   ├── mailer/              邮件发送
│   │   └── health/              健康检查
│   ├── domain/                  跨域值对象
│   │   └── account.go           Account、Session、Permission 等
│   ├── identity/                ★ 账号内核(域中立)
│   │   ├── account/             账号与凭据
│   │   ├── session/             会话管理
│   │   ├── rbac/                角色与权限
│   │   ├── audit/               审计
│   │   └── identity.go          内核对外接口定义
│   ├── oidc/                    ★ 业务系统登录域
│   │   ├── server.go            fosite provider
│   │   ├── storage.go           fosite 存储实现
│   │   ├── client.go            OIDC 客户端管理
│   │   ├── sso.go               静默发码
│   │   └── handler.go           HTTP 端点
│   ├── minecraft/               ★ 游戏登录域
│   │   ├── yggdrasil.go         协议端点
│   │   ├── profile.go           档案与改名
│   │   ├── texture.go           皮肤站
│   │   ├── avatar.go            头像渲染
│   │   └── external.go          外部皮肤站回源
│   ├── admin/                   后台接口
│   │   └── handler.go
│   └── transport/               路由装配
│       ├── router.go
│       └── static.go            embed 前端产物
├── db/
│   ├── migrations/              goose SQL
│   └── queries/                 sqlc 查询定义
├── web/
│   ├── account/                 终端用户 SPA
│   └── admin/                   管理后台 SPA
├── deploy/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   └── nginx.conf
├── docs/                        本文档目录
└── Makefile
```

## 四、领域模型清理

旧版三处冗余的处理方案,详见 [04-decisions.md](./04-decisions.md)。

### 4.1 统一 RBAC(替代两套并行体系)

旧版:
```
admin_role / admin_permission_point / admin_role_permission / admin_account_role
mc_role    / mc_permission_point    / mc_role_permission    / mc_account_role
oauth_role / oauth_permission_point / oauth_role_permission / oauth_account_role
```
六张表、两个命名空间(`admin:mc:profile:read` 与 `minecraft:profile:read`),两套都要维护,且职责边界从未定义。

新版:
```
role
permission
role_permission
account_role
```

**唯一命名空间**,权限点用 `域:资源:动作` 三段式:

| 权限点 | 含义 |
|---|---|
| `account:read` / `account:write` | 账号查看与管理 |
| `rbac:read` / `rbac:write` | 角色权限管理 |
| `audit:read` | 审计日志查看 |
| `oidc:client:read` / `oidc:client:write` | OIDC 客户端管理 |
| `oidc:token:revoke` | 吊销令牌 |
|  `minecraft:profile:read` / `minecraft:profile:write` | 玩家档案 |
|  `minecraft:texture:read` / `minecraft:texture:write` | 材质库 |
|  `minecraft:server:read` / `minecraft:server:write` | 服务器白名单 |

区分「域管理员」与「平台管理员」不再靠两套表,而是靠**角色绑定的权限点集合**。后台菜单按角色实际持有的权限点过滤,行为不变但模型简单得多。

### 4.2 删除 organization 表

旧版 `organization` 表建了树形结构(`parent_id`),但**没有任何代码消费它** —— 所有角色都直接挂在 account 上。本项目不做多租户(见 [00-overview.md](./00-overview.md) 第六节),表直接删除。

如果将来真有企业组织需求,再基于账号分组重新设计,而不是复活一张没人用的表。

### 4.3 统一会话模型

旧版 `session` 表有 `domain` 字段区分 `sso` / `oauth` / `mc` / `admin`,四种会话混在一张表,语义模糊。

新版按**用途**拆分:

| 表 | 用途 | 生命周期 |
|---|---|---|
| `session` | 终端用户登录态(账号中心) | 滑动过期,7 天 / 绝对 30 天 |
| `oidc_refresh_token` | OIDC 刷新令牌 | 30 天,可轮换可吊销 |
| `mc_access_token` | MC 认证令牌 | 短期,与 OIDC 完全隔离 |

`session` 是**唯一**的用户登录态。OIDC 和 MC 各自用独立的令牌表,不混用。`domain` 字段删除。

## 五、请求生命周期

```
HTTP 请求
  │
  ├─ RequestID 中间件        生成/透传 request_id
  ├─ Logger 中间件           注入 logger(ctx)
  ├─ Recover 中间件          panic → 500 + 日志
  ├─ CORS 中间件
  ├─ 认证中间件               解析 session cookie / Bearer token
  ├─ 授权中间件               校验权限点(rbac.Can())
  │
  └─ Handler → Service → Repository(sqlc) → PostgreSQL
        │
        └─ 响应经 httpx.OK() / httpx.Fail() 统一包装
```

错误处理链:业务错误 `apperr.New(code, msg)` → handler 返回 → `httpx.Fail` 映射 HTTP 状态 → 未预期错误统一 500,细节只进日志。

## 六、并发与超时

- **DB 连接池**:`pgxpool`,默认 `MaxConns = NumCPU * 4`,可通过环境变量调
- **HTTP 超时**:读 15s / 写 30s / 空闲 60s
- **外部调用**:HTTP 客户端统一 5s 超时,皮肤站回源另配熔断
- **头像渲染**:异步任务队列,**不占用请求处理时间**

## 七、测试策略

| 层级 | 范围 | 工具 |
|---|---|---|
| 单元测试 | 值逻辑:密码策略、权限求值、会话过期、PNG 校验 | `testify` + `clock` 接口注入 |
| 集成测试 | 需要 DB 的服务逻辑 | testcontainers 起真实 PostgreSQL |
| 端到端测试 | 完整 HTTP 链路 | `httptest` + 真实 DB |
| 协议测试 | Yggdrasil 签名、防重放 | 针对 `hasJoined` 的 HMAC 校验 |

原则:**依赖用接口注入**,时间用 `clock.Clock` 接口而非直接调 `time.Now`,这样过期逻辑可测。

## 八、与旧版的对应关系

| 旧版 | 新版 |
|---|---|
| `libs/kernel-contract`(纯类型契约) | 消失 —— Go 用接口 + 编译期装配替代运行时注册 |
| `libs/platform` | `internal/platform` |
| `libs/core` | `internal/identity` |
| `libs/domain-oauth` | `internal/oidc` |
| `libs/domain-mc` | `internal/minecraft` |
| `libs/admin` | `internal/admin` |
| `libs/domain-registry`(启动期校验) | 消失 —— 编译期保证 |
| `scripts/check-terms.mjs`(术语门禁) | 保留思想,用 lint 规则或 CI grep 实现(见 M7) |
| 4 个 SQLite 文件 | 1 个 PostgreSQL,4 个 schema 隔离 |

---

**上一篇**:[01-roadmap.md](./01-roadmap.md) —— 路线图
**下一篇**:[03-data-model.md](./03-data-model.md) —— 数据模型
