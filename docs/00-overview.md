# 00 · 项目总纲

> 本文件是重构计划的**入口**。读完这一篇就该知道项目是什么、为什么重做、做成什么样算完成。

## 一、项目是什么

YggAuth 是一套**全局统一账号系统**:用一份账号与登录能力,同时服务两类完全不同的消费方。

| 消费方 | 协议 | 典型场景 |
|---|---|---|
| **业务系统** | OAuth 2.1 / OIDC(标准,任何语言客户端都能接) | 自研的 Web 管理系统、内部工具 |
| **Minecraft** | Yggdrasil 协议(私有,需 authlib-injector) | 私有 MC 服务器 + 皮肤站 |

用户只注册一次账号,凭同一份凭据既能登录业务系统,也能进 MC 服务器。

## 二、为什么重做

旧版(TypeScript + NestJS + SQLite)能跑,但积累了几个结构性问题:

### 2.1 文档与代码严重脱节

`README.md` 声称项目处于「T01 工程地基阶段」,而实际上 OAuth 域、MC 域、后台、两个前端均已交付。设计文档(`docs/` 下 `01-architecture.md` / `02-isolation.md` / `04-endpoints.md` 等)在新仓库里**根本不存在**,只有一份孤立的 SQLite 初始化 SQL。代码注释里引用「`docs/12-api.md` 第九节」这类指向,全部落空。

文档不可信,等于没有文档。

### 2.2 技术栈偏重且不匹配

- **NestJS 依赖注入体系**为 1.8 万行代码带来了大量装饰器与模块样板,业务逻辑被框架结构稀释;
- **SQLite 强制单实例**。旧版部署文档明确写着「不要给 app 加 replicas」,这不是能接受的扩展性;
- **多库文件拆分**(core/auth/game/audit 四个 `.db`)在 SQLite 下是隔离手段,在 PostgreSQL 下可以直接用 schema 做得更干净。

### 2.3 领域模型存在冗余

盘查旧库表结构时发现三处逻辑不合理:

1. **两套并行 RBAC**:`admin_role` / `admin_permission_point`(后台权限)与 `mc_role` / `oauth_role`(域内角色)是两套完全独立的表,权限点命名空间也不同(`admin:mc:profile:read` vs `mc:profile:read`)。职责边界从未被清晰定义,两套体系都需要维护。
2. **`organization` 表是空壳**:建了带 `parent_id` 的树形组织表,但所有角色都挂在 account 上,没有任何代码消费这张表。真实需求不明。
3. **密码上限 18 位**:`PASSWORD_MAX_LENGTH=18` 偏短,不符合现代实践,疑似随手填写而非安全决策。

本次重构把这三处一并清理,方案见 [02-architecture.md](./02-architecture.md) 与 [03-data-model.md](./03-data-model.md)。

## 三、重构目标

### 3.1 功能等价

**开发内容与旧版一致**,不砍功能、不缩水。完整交付:账号内核、OIDC 授权服务器、SSO 静默发码、Yggdrasil 认证、皮肤站(上传/存储/去重/头像渲染)、管理后台、审计。

> 唯一的行为变更已在 [04-decisions.md](./04-decisions.md) 中逐条列出,其余保持兼容。

### 3.2 技术栈现代化

| 层 | 选型 | 理由 |
|---|---|---|
| 后端语言 | **Go 1.27+** | 单二进制、静态编译、并发模型天然适合认证服务 |
| HTTP 框架 | **chi** | 轻量、零魔法、net/http 兼容,中间件模型清晰 |
| 数据库 | **PostgreSQL 16+** | 解除单实例限制,支持水平扩展 |
| 数据访问 | **sqlc + goose** | SQL 显式可控、编译期类型安全;goose 管版本化迁移 |
| OIDC | **ory/fosite** | OAuth2/OIDC 最成熟的生产级实现,不自研安全敏感逻辑 |
| 密码哈希 | **argon2id** | 与旧版一致,`golang.org/x/crypto/argon2` |
| 前端 | **Vue 3 + TypeScript + Vite** | Composition API + 类型推导 |
| UI 组件库 | **Ant Design Vue 4** | 后台密集型界面组件最全 |
| 前端集成 | **go:embed** | 构建产物编进 Go 二进制,单容器单进程 |
| 日志 | **log/slog** | 标准库结构化日志,零依赖 |
| 容器化 | **Docker + Compose** | 沿用旧版形态 |

### 3.3 文档即契约

每份文档在**写代码之前**定稿,写代码时同步更新。文档与代码同仓、同 PR 评审。文档目录即本计划。

## 四、文档地图

| 文档 | 内容 | 读者 |
|---|---|---|
| [00-overview.md](./00-overview.md) | 项目总纲、为什么重做、目标 | 所有人,**先读这篇** |
| [01-roadmap.md](./01-roadmap.md) | 里程碑与任务分解、验收标准 | 开发者、排期 |
| [02-architecture.md](./02-architecture.md) | 分层架构、包结构、领域模型清理 | 开发者 |
| [03-data-model.md](./03-data-model.md) | PostgreSQL schema 设计、迁移策略 | 开发者、DBA |
| [04-decisions.md](./04-decisions.md) | 关键决策记录(ADR)与行为变更清单 | 所有人 |
| [05-api.md](./05-api.md) | HTTP API 设计、认证流程 | 开发者、前端 |
| [06-mc-protocol.md](./06-mc-protocol.md) | Yggdrasil 协议适配、皮肤站 | 开发者 |
| [07-frontend.md](./07-frontend.md) | 前端架构、路由、状态管理 | 前端 |
| [08-deployment.md](./08-deployment.md) | 容器化、配置项、运维排障 | 运维、部署 |
| [09-security.md](./09-security.md) | 安全设计、威胁模型、审计 | 所有人 |

## 五、成功标准

重构完成的判定条件:

- [ ] 全部里程碑交付,`docs/01-roadmap.md` 中所有任务勾选完毕
- [ ] 旧版全部功能点在新版有对应实现(逐条对照 [05-api.md](./05-api.md))
- [ ] 单元测试覆盖核心域(账号、会话、OIDC、MC 认证)关键路径
- [ ] 集成测试覆盖两条完整链路:OIDC 授权码流程、Yggdrasil 认证流程
- [ ] `docker compose up` 即可跑起完整服务
- [ ] 文档无悬空链接,代码注释无失效的文档引用

## 六、明确不做的事

明确划出边界,避免范围蔓延:

- **不做多租户 SaaS**。旧版的 `organization` 空壳表直接删除,不做企业组织树。
- **不做社交登录**(微信/GitHub/Google)。账号体系只保留邮箱 + 密码。
- **不做 S3/对象存储**。皮肤文件存本地磁盘,存储层留接口但本期不实现 S3 后端。
- **不做移动端 App**。只做响应式 Web。
- **不做国际化(i18n)**。界面文案先用中文硬编码,结构上预留不强制。

## 七、术语表

| 术语 | 含义 |
|---|---|
| 账号内核 (identity) | 域中立的账号/凭据/会话/RBAC 能力 |
| 业务域 (OIDC 域) | 为业务系统提供 OAuth 2.1 / OIDC 授权服务 |
| 游戏域 (MC 域) | 为 Minecraft 提供 Yggdrasil 认证 + 皮肤站 |
| SSO | 静默发码登录,用户在已登录状态下免密跳转 |
| 权限点 (permission) | 形如 `mc:profile:read` 的原子操作标识 |
| 角色 (role) | 权限点的集合,可授予账号 |
| 皮肤 (skin) | Minecraft 玩家角色皮肤 PNG |
| 披风 (cape) | Minecraft 玩家披风 PNG |
| Yggdrasil | Mojang 的 MC 认证服务协议 |

---

**下一篇**:[01-roadmap.md](./01-roadmap.md) —— 里程碑与任务分解
