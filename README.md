# YggAuth

> **全局统一账号系统** —— 一份账号,登录业务系统(OAuth 2.1 / OIDC)与 Minecraft(Yggdrasil + 皮肤站)。

---

## 这是什么

YggAuth 是一个单二进制交付的账号中心,同时服务两类完全不同的消费方:

| 消费方 | 协议 | 典型场景 |
|---|---|---|
| **业务系统** | OAuth 2.1 / OIDC(标准) | 自研 Web 系统、内部工具 |
| **Minecraft** | Yggdrasil(私有,需 authlib-injector) | 私有 MC 服务器 + 皮肤站 |

用户注册一次,凭同一份凭据既能登业务系统,也能进 MC 服务器。

---

## 为什么重做

旧版(TypeScript + NestJS + SQLite)能跑,但积累了三个结构性问题:

1. **文档与代码严重脱节** —— README 声称项目处于「T01 阶段」,实际所有功能均已交付;代码注释里的文档引用全部落空。
2. **技术栈偏重且不匹配** —— NestJS DI 体系为 1.8 万行代码带来大量样板;SQLite **强制单实例部署**。
3. **领域模型冗余** —— 两套并行 RBAC、`organization` 空壳表、18 位密码上限。

完整分析见 [docs/architecture.md](docs/architecture.md)。

---

## 技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| 后端 | **Go 1.27+** + chi | 单二进制、静态编译、并发模型适合认证服务 |
| 数据库 | **PostgreSQL 16+**(pgx + **sqlc** + goose) | 支持水平扩展、schema 隔离 |
| OIDC | **ory/fosite** | OAuth2/OIDC 最成熟的生产级实现,不自研安全敏感逻辑 |
| 密码哈希 | argon2id | NIST 推荐,抗 GPU/ASIC |
| 前端 | **Vue 3** + TypeScript + Vite + **Ant Design Vue 4** | Composition API + 类型推导 |
| 前端集成 | `go:embed` —— 构建产物编进二进制,单进程服务全部 |
| 部署 | Docker + Docker Compose | 沿用旧版形态 |

---

## 功能范围

- **账号内核** —— 注册、登录、会话、密码重置、邮箱验证、RBAC、审计
- **OIDC 授权服务** —— 授权码 + PKCE、refresh token 轮换、JWKS、SSO 静默发码
- **Minecraft 域** —— Yggdrasil 认证、玩家档案与改名、服务器白名单
- **皮肤站** —— 皮肤/披风上传、sha256 去重、异步头像渲染、外部皮肤站回源
- **管理后台** —— 账号、角色权限、OIDC 客户端、密钥轮换、材质库、审计

**明确不做**:多租户 SaaS、社交登录、S3 对象存储、移动端 App、国际化。详见 [docs/architecture.md](docs/architecture.md)「明确不做的事」。

---

## 快速开始

### 环境要求

- Go 1.27+
- PostgreSQL 16+
- Node 24+ / pnpm 11+

版本约束见 [.tool-versions](.tool-versions)。

### 本地开发

```bash
# 1. 配置环境变量(必填至少四项)
cp .env.example .env
# 编辑 .env:APP_PUBLIC_DOMAIN / PUBLIC_BASE_URL / KEY_MASTER_SECRET / DB_PASSWORD

# 2. 启动后端(迁移默认开启,DB_AUTO_MIGRATE=true)
make dev
# 或: go run ./cmd/yggauth serve

# 3. 启动前端开发服务器(两个 SPA 独立端口)
cd web && pnpm dev
# 账号站: http://localhost:5173
# 管理后台: http://localhost:5174/admin/

# 4. 校验
make verify   # 格式化 + lint + 单测
make test-integration  # 集成测试(embedded-postgres)
```

**注意**:本环境 `/tmp` 为 noexec,跑 `go test` 必须设:
```bash
export TMPDIR=/data/dsh/home/tmp GOTMPDIR=/data/dsh/home/tmp/gotmp
```
详见 [docs/development.md](docs/development.md)。

### 容器部署

```bash
docker compose up -d
# 健康检查:curl http://localhost:3000/health/ready
```

详细部署步骤见 [docs/deployment.md](docs/deployment.md)。

---

## 目录结构

```
yggauth/
├── cmd/yggauth/            # 进程入口、子命令(serve/migrate/admin/healthcheck/version)
├── internal/
│   ├── config/             # 配置加载与校验(.env.example 是唯一真源)
│   ├── platform/           # 平台能力(与业务无关)
│   │   ├── db/             # pgx 连接池、事务、迁移
│   │   ├── httpx/          # 中间件、响应封装、错误码映射
│   │   ├── apperr/         # 业务错误码(见 docs/api.md)
│   │   ├── log/            # slog 结构化日志
│   │   ├── keys/           # 签名密钥管理
│   │   ├── mailer/         # 邮件发送(支持热替换)
│   │   ├── ratelimit/      # 限流
│   │   ├── storage/        # 文件存储接口 + 本地实现
│   │   └── health/         # 健康检查
│   ├── domain/             # 跨域共享值对象(Account、Session、Permission)
│   ├── identity/           # ★ 账号内核(域中立)
│   │   ├── account/        # 账号与凭据
│   │   ├── session/        # 会话管理
│   │   ├── rbac/           # 角色与权限
│   │   └── audit/          # 审计日志(只追加)
│   ├── oidc/               # ★ 业务系统登录域(OIDC/OAuth2.1)
│   ├── minecraft/          # ★ 游戏登录域(Yggdrasil + 皮肤站)
│   ├── admin/              # 后台管理接口(RBAC 门面)
│   └── transport/          # HTTP 路由装配(唯一知道有哪些业务域的地方)
├── db/
│   ├── migrations/         # goose SQL 迁移(见 docs/data-model.md)
│   └── queries/            # sqlc 查询定义
├── web/                    # Vue 源码(构建产物嵌入 internal/webserver/dist/)
│   ├── apps/account/       # 终端用户 SPA(端口 5173)
│   ├── apps/admin/         # 管理后台 SPA(端口 5174)
│   └── shared/             # 共享类型与工具
├── deploy/                 # Docker、nginx、postgres 初始化
├── scripts/                # check-terms.sh(术语门禁,ADR-010)
└── docs/                   # 本文档目录
```

**依赖方向**: `transport → 业务域 → platform`,`identity` 不依赖任何业务域(ADR-010)。

---

## 文档索引

| 文档 | 内容 | 读者 |
|---|---|---|
| [架构设计](docs/architecture.md) | 分层、目录、依赖方向、关键约定 | 开发者 |
| [数据模型](docs/data-model.md) | schema、迁移、sqlc 流程 | 开发者、DBA |
| [API 设计](docs/api.md) | HTTP 接口、错误码、限流 | 开发者、前端 |
| [安全设计](docs/security.md) | 威胁模型、约束、上线检查清单 | 所有人 |
| [配置方案](docs/configuration.md) | env/setting 双源、首启引导、热更新 | 运维、部署 |
| [部署指南](docs/deployment.md) | Docker/Compose/nginx/测试环境/排障 | 运维、部署 |
| [Minecraft 协议](docs/minecraft.md) | Yggdrasil、皮肤站、SSRF 防护 | 开发者 |
| [前端架构](docs/frontend.md) | Vue 架构、列表页通则、开发约定 | 前端 |
| [开发指南](docs/development.md) | 本地开发、测试分层、CI、环境 | 开发者 |
| [决策记录](docs/decisions.md) | ADR-001 ~ ADR-012 | 所有人 |
| [待办清单](docs/todo.md) | 已知问题与未修缺陷 | 维护者 |

**先读** [架构设计](docs/architecture.md) 了解分层与目录。

---

## 里程碑

```
M0 工程地基 → M1 基础设施 → M2 账号内核 → M3 OIDC 服务 → M4 MC 认证
                                                ↘ M5 皮肤站
                                              ↘ M6 前端 → M7 部署加固
```

全部里程碑已交付。历史决策见 [docs/decisions.md](docs/decisions.md)。

---

## 贡献

- 提交前运行 `make verify`(格式化 + lint + 单测)与 `make test-integration`(集成测试)
- 文档与代码同 PR 评审,不允许「代码先上、文档后补」
- 新增 ADR 追加到 [docs/decisions.md](docs/decisions.md) 第十节
- 改接口同步更新 [docs/api.md](docs/api.md)
- 改 schema 同步迁移并跑 `make sqlc-diff`
