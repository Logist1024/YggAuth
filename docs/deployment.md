# 部署指南

> Docker + Compose 部署、测试环境编排、nginx 反代、黄金路径验收。

---

## 一、三条部署路线

| 路线 | 适用场景 | 是否需要 Docker |
|---|---|---|
| A. 本地进程 | 开发调试 | 否 |
| B. 测试容器 | 本地测试、CI | 是(Docker ≥24 + compose v2) |
| C. 生产 | 正式环境 | 是 |

---

## 二、路线 A:本地进程

```bash
# 1. 配置环境变量
cp .env.example .env
# 编辑 .env:至少改 APP_PUBLIC_DOMAIN / PUBLIC_BASE_URL / KEY_MASTER_SECRET / DB_PASSWORD

# 2. 启动后端
make dev
# 或: go run ./cmd/yggauth serve
# 端口默认 3000,迁移默认开启(DB_AUTO_MIGRATE=true)

# 3. 启动前端(另开终端)
cd web && pnpm dev
# 账号站: http://localhost:5173
# 管理后台: http://localhost:5174/admin/
```

**注意**:本环境 `/tmp` 为 noexec,跑 `go test` 必须设:
```bash
export TMPDIR=/data/dsh/home/tmp GOTMPDIR=/data/dsh/home/tmp/gotmp
```

---

## 三、路线 B:测试容器

测试编排见 `deploy/test/docker-compose.test.yml`,配套 `.env.example` 见 `deploy/test/test.env.example`。

```bash
# 启动测试环境
docker compose -f deploy/test/docker-compose.test.yml up -d
# 健康检查
curl http://localhost:3000/health/ready
```

测试环境特点:
- nginx 反代 `app` 容器端口(直连,不走宿主端口映射)
- 自签证书(见 `deploy/test/certs/`)
- 测试专用数据库(独立容器,测试完可丢弃)

启用测试 profile:
```bash
docker compose --profile test up -d
```

---

## 四、路线 C:生产部署

### 4.1 容器构建

```bash
# 构建镜像
docker build -t yggauth .
# 或用 docker compose
docker compose up -d --build
```

Dockerfile 多阶段构建:
1. `build` 阶段:编译 Go 二进制 + 前端产物
2. `runtime` 阶段:distroless 基础镜像,非 root 用户运行

### 4.2 环境变量(必填四项)

从 `.env.example` 复制,至少修改:
- `APP_PUBLIC_DOMAIN` —— 终端用户站域名
- `PUBLIC_BASE_URL` —— **OIDC issuer**,必须与实际访问域名完全一致(含协议)
- `KEY_MASTER_SECRET` —— 签名密钥包裹密钥(随机 32 字节 hex)
- `DB_PASSWORD` —— PostgreSQL 密码

### 4.3 nginx 反代(可选但推荐)

生产环境建议用 nginx 终结 HTTPS,app 容器监听内网。nginx 配置见 `deploy/nginx/`。

关键配置:
- HTTPS 终结
- HSTS
- 静态文件缓存
- `/admin` 前缀分流

---

## 五、配置分组(5.1/5.2/5.3)

配置项分三组(见 `internal/config/config.go`):

| 分组 | 说明 | 示例 |
|---|---|---|
| App | 应用行为 | `APP_HOST`, `APP_PORT`, `DATA_DIR` |
| DB | 数据库连接 | `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD` |
| Mail | 邮件发送 | `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD` |
| Session | 会话策略 | `SESSION_IDLE_TIMEOUT`, `SESSION_ABS_TIMEOUT` |
| OIDC | OIDC 行为 | `OIDC_ISSUER`, `OIDC_KEY_*` |
| MC | Minecraft 行为 | `MC_LOGIN_DEFAULT`, `MC_SKIN_DOMAIN` |
| Log | 日志级别 | `LOG_LEVEL`, `LOG_FORMAT` |
| CORS | 跨域策略 | `CORS_ORIGINS` |

完整清单以 `.env.example` 为准。

---

## 六、自动迁移

`DB_AUTO_MIGRATE=true`(默认开启)。启动时自动执行 `db/migrations/` 下所有未应用的迁移。

**不要在生产环境用 `false`**,除非你有单独的迁移流水线。

迁移命令:
```bash
# 手动执行
yggauth migrate
yggauth migrate -to down
yggauth migrate -to status
```

---

## 七、指标

Prometheus 指标端点:`GET /metrics`(nginx 下必须拒绝公网访问,见 `deploy/nginx/`)。

指标清单见 `internal/platform/metrics/metrics.go`:
- HTTP 请求数、延迟、状态码分布
- 活跃会话数
- 数据库连接池状态
- 邮件发送成功率

---

## 八、健康检查

```bash
# 存活探针
curl http://localhost:3000/health/live
# → 200 OK

# 就绪探针
curl http://localhost:3000/health/ready
# → 200 OK(数据库连通、迁移完成)
```

compose 自带健康检查:`docker compose ps` 查看状态。

---

## 九、Vite 开发服务器端口

`pnpm dev` 启动两个独立 SPA 开发服务器:
- 账号站: **5173**
- 管理后台: **5174**

代理到后端 3000(见 `web/apps/*/vite.config.ts`)。

**注意**:旧文档曾误记为 3001,实际是 5173/5174。

---

## 十、排障

| 现象 | 可能原因 | 排查 |
|---|---|---|
| 启动即退出 | 缺少必填 env 键 | 看启动日志末行 |
| OIDC 客户端验签失败 | `PUBLIC_BASE_URL` 与 issuer 不一致 | 对比 `/.well-known/openid-configuration` 的 issuer |
| 静态资源 404 | 前端未重建 | `make web && make build` |
| 邮件发不出 | SMTP 配置缺失或错误 | 用 `POST /api/admin/mail/test` 测试 |
| 设置改了不生效 | 缓存未失效 / 键名写错 | 检查 `app.setting` 表 |
| 皮肤上传失败 | `DATA_DIR` 目录无写权限 | `chmod 755 data/` |

---

## 十一、黄金路径验收

```bash
make golden-path
```

执行端到端冒烟测试(见 `deploy/test/golden-path.sh`),覆盖:
1. 容器启动 → 健康检查
2. 首启引导 → 首个管理员
3. 登录 → 获取会话
4. 注册 → 验证邮箱
5. OIDC 授权码流程
6. MC 认证流程
7. 皮肤上传与下载

真容器 1/2/7 步需要 Docker。
