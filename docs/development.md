# 开发指南

> 本地开发环境、测试分层、质量门禁、CI 流水线、贡献约定。

---

## 一、环境清单

| 组件 | 版本 | 位置 / 说明 |
|---|---|---|
| Go | 1.27.1 | `/data/dsh/home/.toolchains/go/bin/go` |
| PostgreSQL | 16.15 | 集成测试用 embedded-postgres;本地开发可用 `$HOME/pg16` |
| Node / pnpm | 24.21.0 / 11.7.0 | 系统自带 |
| golangci-lint | v2.14.0 | `/data/dsh/home/go/bin/golangci-lint`(Go 1.27 构建) |
| sqlc | v1.30.0 | `/data/dsh/home/go/bin/sqlc` |
| gofumpt / goimports | 最新 | `/data/dsh/home/go/bin/` |

版本约束见 [.tool-versions](../.tool-versions)。

---

## 二、常用命令

```bash
# 后端
make build              # 构建二进制(含前端产物 embed)
make build-go           # 只构建 Go 二进制(前端产物已存在时)
make dev                # 本地开发运行(迁移默认开启)
make migrate            # 执行数据库迁移(等价于 yggauth migrate)
make migrate-down       # 回滚一次迁移
make migrate-status     # 查看迁移状态

# 测试
make test               # 跑单元测试
make test-integration   # 跑集成测试(embedded-postgres)
make test-all           # 跑全部测试

# 代码质量
make lint               # golangci-lint(含 gofumpt / errcheck / staticcheck)
make fmt                # gofumpt 格式化 + goimports
make verify             # fmt + lint + test
make check-terms        # 术语门禁(ADR-010)

# 数据库
make sqlc               # 根据 db/queries 生成类型安全的 Go 代码
make sqlc-diff          # 校验生成物与查询定义一致(CI 用)

# 前端
make web                # 构建两个前端应用
make web-dev            # 前端开发服务器(账号站 5173、后台 5174)
make web-lint           # 前端 lint

# 其它
make tidy               # go mod tidy
make vuln               # govulncheck
make golden-path        # 黄金路径验收(端到端冒烟测试)
make clean              # 清理构建产物(只清二进制)
```

---

## 三、测试分层

### 3.1 单元测试

```bash
make test
```

- 默认标签,不跑集成测试
- 不需要数据库
- 速度快,CI 必跑

### 3.2 集成测试

```bash
make test-integration
```

- 标签:`-tags=integration`
- 使用 `fergusstrange/embedded-postgres` 自动拉起真实 PostgreSQL 16
- 每个测试实例隔离提取目录(避免并发冲突)
- 超时 20m

### 3.3 测试数据库

`internal/platform/db/testdb/testdb.go` 提供测试脚手架:
- 自动创建四个 schema:identity、oidc、minecraft、app
- 执行所有迁移
- 测试后清理

---

## 四、质量门禁

### 4.1 golangci-lint

**必须用 Go 1.27 构建的版本**。原因:go.mod 声明 Go 1.27,而 golangci-lint 拒绝分析目标版本高于自身构建版本的代码。

```bash
golangci-lint version   # 输出里有 "built with go1.27.x"
```

v2.13.0(2026-08-19)起改用 Go 1.27 构建。升级时先确认版本号。

### 4.2 术语门禁(ADR-010)

```bash
make check-terms
```

检查 `internal/identity`、`internal/platform`、`internal/domain` 是否含业务词(`oidc`/`minecraft`/`skin` 等)。

豁免项见 `scripts/check-terms.sh`。

### 4.3 sqlc 生成物

```bash
make sqlc-diff
```

确保 `internal/platform/db/query/*.sql.go` 与 `db/queries/*.sql` 一致。

### 4.4 漏洞扫描

```bash
make vuln
```

`govulncheck ./...`。

---

## 五、前端开发

```bash
cd web && pnpm dev
```

启动两个独立 Vite 服务器:
- 账号站: http://localhost:5173
- 管理后台: http://localhost:5174/admin/

代理到后端 3000。

**改前端后必须重建 embed 产物**:
```bash
pnpm build          # 重新生成 internal/webserver/dist
make build-go       # 重新构建 Go 二进制
```

---

## 六、CI 流水线

`.github/workflows/ci.yml` 四个 job:

| Job | 内容 |
|---|---|
| build | 前端构建 + Go 构建 + 上传二进制制品 |
| test | 单元测试 + 集成测试 |
| lint | golangci-lint + 前端 lint + sqlc-diff + 术语门禁 |
| security | govulncheck |

---

## 七、提交约定

从 `git log --oneline -20` 归纳的真实提交前缀:

- `feat:` 新功能
- `fix:` 修复
- `test:` 测试相关
- `lint:` 代码风格
- `docs:` 文档
- `ci:` CI 配置
- `M0`~`M7` 里程碑标记

示例:
```
feat: polish both consoles and ship the MC admin handlers
fix: let public endpoints survive an invalid session cookie
docs: record the small defects found while testing the deployment
```

---

## 八、开发环境注意

本环境(`/tmp` noexec)的特殊设置:

```bash
export PATH=/data/dsh/home/.toolchains/go/bin:$PATH
export PATH=/data/dsh/home/go/bin:$PATH   # golangci-lint, sqlc, gofumpt
export TMPDIR=/data/dsh/home/tmp
export GOTMPDIR=/data/dsh/home/tmp/gotmp
```

缺了 `GOTMPDIR`,`go run` 会报 permission denied。

---

## 九、贡献流程

1. 分支开发:`git checkout -b feat/your-feature`
2. 写代码 + 写文档(文档与代码同 PR)
3. 跑 `make verify` + `make test-integration`
4. 提交:`git commit -m "feat: your feature"`
5. Push + PR

**文档与代码同 PR 评审**,不允许「代码先上、文档后补」。
