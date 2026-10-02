# YggAuth

> **全局统一账号系统** —— 一份账号,登录业务系统(OAuth 2.1 / OIDC)与 Minecraft(Yggdrasil + 皮肤站)。

**当前状态:规划阶段,尚未开始编码。** 全部设计文档已定稿,实施路线图见 [docs/01-roadmap.md](docs/01-roadmap.md)。

---

## 这是什么

一个账号系统同时服务两类完全不同的消费方:

| 消费方 | 协议 | 场景 |
|---|---|---|
| 业务系统 | OAuth 2.1 / OIDC(标准) | 自研 Web 系统、内部工具 |
| Minecraft | Yggdrasil(私有) | 私有 MC 服务器 + 皮肤站 |

用户注册一次,凭同一份凭据既能登业务系统,也能进 MC 服务器。

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | **Go 1.27+** + chi |
| 数据库 | **PostgreSQL 16+**(pgx + **sqlc** + goose) |
| OIDC | **ory/fosite**(不自研安全敏感逻辑) |
| 密码哈希 | argon2id |
| 前端 | **Vue 3** + TypeScript + Vite + **Ant Design Vue 4** |
| 前端集成 | `go:embed` —— 构建产物编进二进制,单进程服务全部 |
| 部署 | Docker + Docker Compose |

## 功能范围

- **账号内核** —— 注册、登录、会话、密码重置、邮箱验证、RBAC、审计
- **OIDC 授权服务** —— 授权码 + PKCE、refresh token 轮换、JWKS、SSO 静默发码
- **Minecraft 域** —— Yggdrasil 认证、玩家档案与改名、服务器白名单
- **皮肤站** —— 皮肤/披风上传、sha256 去重、异步头像渲染、外部皮肤站回源
- **管理后台** —— 账号、角色权限、OIDC 客户端、密钥轮换、材质库、审计

**明确不做**:多租户 SaaS、社交登录、S3 对象存储、移动端 App、国际化。理由见 [docs/00-overview.md](docs/00-overview.md) 第六节。

## 为什么重做

旧版(TypeScript + NestJS + SQLite)存在三个结构性问题:

1. **文档与代码脱节** —— README 声称处于「T01 阶段」,实际 OAuth/MC/后台/前端均已交付;设计文档根本不存在,代码注释里的文档引用全部落空。
2. **技术栈偏重** —— NestJS DI 体系为 1.8 万行代码带来大量样板;SQLite **强制单实例部署**。
3. **领域模型冗余** —— 两套并行 RBAC、`organization` 空壳表、18 位密码上限。

完整分析见 [docs/00-overview.md](docs/00-overview.md) 第二节。

---

## 文档

**先读这一篇** → [docs/00-overview.md](docs/00-overview.md)

| # | 文档 | 内容 |
|---|---|---|
| 00 | [项目总纲](docs/00-overview.md) | 项目定位、重构理由、目标与边界 |
| 01 | [路线图与任务分解](docs/01-roadmap.md) | 8 个里程碑、任务清单、风险 |
| 02 | [架构设计](docs/02-architecture.md) | 分层、目录结构、领域模型清理 |
| 03 | [数据模型](docs/03-data-model.md) | PostgreSQL schema、索引、迁移策略 |
| 04 | [关键决策记录](docs/04-decisions.md) | 11 条 ADR + 10 项行为变更 |
| 05 | [API 设计](docs/05-api.md) | HTTP 接口、错误码、限流 |
| 06 | [Minecraft 协议适配](docs/06-mc-protocol.md) | Yggdrasil 协议、皮肤站设计 |
| 07 | [前端设计](docs/07-frontend.md) | Vue + Ant Design Vue 架构 |
| 08 | [容器化部署](docs/08-deployment.md) | Docker、配置项、排障 |
| 09 | [安全设计](docs/09-security.md) | 威胁模型、上线检查清单 |

## 里程碑

```
M0 工程地基 → M1 基础设施 → M2 账号内核 ⇢ M3 OIDC 服务 ⇢ M6 前端 ⇢ M7 部署加固
                                        ↘ M4 MC 认证 ⇢ M5 皮肤站 ↗
```

预计 31 人日,单人全职约 6 周。详见 [docs/01-roadmap.md](docs/01-roadmap.md)。

## 与旧版的行为变更

重构会改变以下行为(详见 [docs/04-decisions.md](docs/04-decisions.md) 第二节完整清单):

| 变更 | 旧版 | 新版 |
|---|---|---|
| 密码长度 | 6–18 位 | **8–128 位** |
| 密码复杂度 | 强制大小写+数字 | 不强制,查泄露库 |
| RBAC | 两套表、两个命名空间 | **单一表、统一命名空间** |
| `organization` | 建表但未使用 | **删除** |
| 数据库 | 4 个 SQLite 文件 | PostgreSQL,4 个 schema |
| 静态文件 | 必须 nginx 托管 | Go 可直接托管 |

---

## 贡献

文档与代码同 PR 评审,不允许「代码先上、文档后补」。提交前运行 `make verify`(typecheck + lint + test)。
