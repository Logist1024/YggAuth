# 08 · 容器化部署

## 一、部署形态

三个容器:

```
                    ┌─────────────────────┐
                    │  nginx              │
   443/80 ─────────>│  终结 HTTPS          │
                    │  静态资源(可选)      │
                    │  反代 API            │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  app (yggauth)       │
                    │  Go 二进制           │
                    │  内嵌前端产物         │
                    │  :3000               │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  postgres:16         │
                    │  持久化卷            │
                    └─────────────────────┘

                    ┌─────────────────────┐
                    │  yggauth (volume)    │  皮肤文件
                    └─────────────────────┘
```

**相比旧版的两点变化**:
1. nginx 从**必需**变**可选**(ADR-006)—— Go 已能托管前端;生产仍建议用它终结 HTTPS;
2. SQLite 换成 PostgreSQL,**支持多副本**(注意:皮肤文件是本地磁盘,多副本需共享卷或粘性会话,见 ADR-007)。

## 二、快速开始

```bash
cp .env.example .env
# 至少修改三项:
#   APP_PUBLIC_DOMAIN
#   PUBLIC_BASE_URL
#   KEY_MASTER_SECRET   (openssl rand -hex 32)
docker compose up -d --build
```

启动后:
- `https://auth.example.com/` 终端用户站
- `https://admin.example.com/` 管理后台

## 三、Dockerfile

多阶段构建,运行镜像用 distroless:

```dockerfile
# ---------- 前端构建 ----------
FROM node:22-alpine AS web
WORKDIR /web
RUN corepack enable && corepack prepare pnpm@10 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---------- Go 构建 ----------
FROM golang:1.24-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" -o /out/yggauth ./cmd/yggauth

# ---------- 运行 ----------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/yggauth /app/yggauth
COPY --from=build /src/db/migrations /app/db/migrations
USER nonroot:nonroot
EXPOSE 3000
ENTRYPOINT ["/app/yggauth"]
```

**关键点**:
- `CGO_ENABLED=0` —— 静态编译,镜像无需 libc
- distroless 基础镜像 —— 无 shell、无包管理器,攻击面小
- 迁移 SQL 随镜像分发,启动时自动执行
- 前端产物从 `web` 阶段拷贝,**必须重建 Go** 才能更新(ADR-006 的代价)

## 四、docker-compose.yml

```yaml
services:
  app:
    build: .
    image: yggauth:latest
    restart: unless-stopped
    env_file: .env
    environment:
      APP_HOST: 0.0.0.0        # 容器内必须监听所有网卡
      DB_HOST: postgres
    depends_on:
      postgres: { condition: service_healthy }
    volumes:
      - yggauth-data:/app/data
    expose: ["3000"]
    networks: [yggauth]

  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: yggauth
      POSTGRES_USER: yggauth
      POSTGRES_PASSWORD: ${DB_PASSWORD:?必须设置}
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U yggauth"]
      interval: 5s
      retries: 10
    networks: [yggauth]

  nginx:
    build: { context: ., dockerfile: deploy/Dockerfile.nginx }
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./deploy/certs:/etc/nginx/certs:ro
    depends_on: [app]
    networks: [yggauth]

volumes:
  pgdata:
  yggauth-data:

networks:
  yggauth: { driver: bridge }
```

> **不使用 `replicas` 或 swarm 多副本**,除非皮肤存储已改为共享卷(ADR-007 的限制)。

## 五、配置项

### 5.1 环境变量

| 变量 | 默认 | 必填 | 说明 |
|---|---|---|---|
| `APP_HOST` | `0.0.0.0` | | 监听地址,容器内必须为 `0.0.0.0` |
| `APP_PORT` | `3000` | | 监听端口 |
| `APP_PUBLIC_DOMAIN` | — | **是** | 终端用户站域名,用于 SSO cookie 父域与 skinDomains |
| `PUBLIC_BASE_URL` | — | **是** | 对外完整基址,**OIDC issuer 就是它** |
| `DB_HOST` | `127.0.0.1` | | PostgreSQL 主机 |
| `DB_PORT` | `5432` | | |
| `DB_NAME` | `yggauth` | | |
| `DB_USER` | `yggauth` | | |
| `DB_PASSWORD` | — | **是** | |
| `DB_SSLMODE` | `disable` | | 生产外部 PG 用 `require` |
| `KEY_MASTER_SECRET` | — | **是** | 私钥加密主密钥,`openssl rand -hex 32` |
| `DATA_DIR` | `./data` | | 皮肤文件目录 |
| `SSO_COOKIE_DOMAIN` | 留空 | | 留空则由 `APP_PUBLIC_DOMAIN` 推导父域 |
| `SSO_COOKIE_SECURE` | `true` | | 本地 http 测试须设 `false` |
| `SSO_COOKIE_NAME` | `ygg_sso` | | |
| `PASSWORD_MIN_LENGTH` | `8` | | 变更 C-1 |
| `PASSWORD_MAX_LENGTH` | `128` | | 变更 C-1 |
| `LOGIN_MAX_FAILED_ATTEMPTS` | `5` | | |
| `LOGIN_LOCK_SECONDS` | `900` | | |
| `SESSION_IDLE_TTL` | `168h` | | 7 天 |
| `SESSION_MAX_TTL` | `720h` | | 30 天 |
| `REGISTRATION_MODE` | `open` | | `open` / `invite_only` |
| `MC_LOGIN_DEFAULT` | `true` | | 新账号默认是否可登 MC |
| `MC_ENABLED` | `true` | | |
| `MC_READONLY` | `false` | | 只读模式,禁上传 |
| `MC_SKIN_EXTERNAL` | `false` | | 外部皮肤站回源 |
| `MAILER_TRANSPORT` | `console` | | `console` / `smtp` |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASSWORD` | — | | smtp 模式必填 |
| `MAILER_FROM` | — | | 发件人 |
| `LOG_LEVEL` | `info` | | `debug` / `info` / `warn` / `error` |
| `CORS_ALLOWED_ORIGINS` | — | | 逗号分隔,生产建议留空走白名单 |

### 5.2 启动校验

**必填项缺失或格式错误 → 启动即退出**,打印明确错误:

```
FATAL  配置错误: KEY_MASTER_SECRET 未设置(生成方式: openssl rand -hex 32)
FATAL  配置错误: PUBLIC_BASE_URL 必须是绝对 URL,当前值: auth.example.com
FATAL  配置错误: PASSWORD_MIN_LENGTH(8) 不能大于 PASSWORD_MAX_LENGTH(128)
```

**绝不静默使用默认值**处理必填项。

### 5.3 运行时可改配置

`app.setting` 表中的配置(注册开关、密码策略、邮件设置)优先级**高于**环境变量,便于后台调整而无需重启。

## 六、数据库迁移

启动时自动执行 `goose up`:

```go
if err := migrations.Up(ctx, pool, "db/migrations"); err != nil {
    log.Fatal("数据库迁移失败", "error", err)
}
```

**备份策略**:迁移前检测到将执行破坏性变更(`-- +goose Down` 存在且非空)时,先自动 `pg_dump` 到 `data/backups/`,保留最近 10 份。

**回滚**:`goose down` 需谨慎,生产环境建议先在预发验证。

## 七、健康检查

| 端点 | 用途 | 含义 |
|---|---|---|
| `/health/live` | liveness | 进程活着即 200,不查外部依赖 |
| `/health/ready` | readiness | 查 DB 连接,DB 不可用返回 503 |
| `/metrics` | 监控 | Prometheus 指标 |

```yaml
healthcheck:
  test: ["CMD", "/app/yggauth", "healthcheck"]   # 或用 wget/curl(distroless 无 shell,建议用 Go 内置子命令)
  interval: 30s
  timeout: 5s
  retries: 3
```

> distroless 镜像无 shell,不能直接用 `curl`。建议给二进制加 `healthcheck` 子命令,或在 Dockerfile 中 `COPY --from=build /bin/busybox /busybox` 后用 `/busybox wget`。

## 八、监控指标

| 指标 | 类型 | 说明 |
|---|---|---|
| `yggauth_login_total` | counter | 登录成功/失败 |
| `yggauth_session_active` | gauge | 活跃会话数 |
| `yggauth_oidc_token_total` | counter | 令牌签发 |
| `yggauth_mc_authenticate_total` | counter | MC 认证 |
| `yggauth_avatar_render_duration` | histogram | 头像渲染耗时 |
| `yggauth_db_pool_conns` | gauge | 连接池使用情况 |
| `yggauth_http_request_duration` | histogram | 按路由的请求耗时 |

## 九、本地开发

```bash
# 起 PG
docker compose up -d postgres

# 迁移
make migrate

# 后端(热重载)
make dev

# 前端(独立开发服务器,代理到后端)
cd web && pnpm dev
```

开发时前端跑 Vite dev server(3001),代理 `/api` `/oauth` `/mc` 到后端 3000,免去每改前端就重建二进制。

**SSO 本地测试两种做法**:

| 做法 | 配置 | 能验证 |
|---|---|---|
| **A(推荐)** | hosts 加 `auth.test.local` / `admin.test.local`,配自签证书 | 完整 SSO |
| **B** | `localhost` 单域名,`SSO_COOKIE_SECURE=false` | 仅后端 |

> ⚠️ 用 `localhost` 时若 `SSO_COOKIE_SECURE=true`,浏览器会**直接丢弃 cookie**,SSO 静默发码全链路静默失效,排查时极易误判。

## 十、排障

| 症状 | 排查 |
|---|---|
| OIDC 客户端报 `invalid_issuer` | `curl $PUBLIC_BASE_URL/oauth/.well-known/openid-configuration \| grep issuer`,必须与实际域名完全一致 |
| SSO 静默失效 | 检查 `SSO_COOKIE_DOMAIN` 推导是否正确、`SSO_COOKIE_SECURE` 与协议是否匹配 |
| 皮肤上传 500 | 检查 `DATA_DIR` 权限、磁盘空间、PNG 是否合法 |
| MC 进服失败 | 先确认 `/mc/` metadata 可达,再查 authlib-injector 日志中的 `hasJoined` 返回 |
| 头像 404 | 异步渲染未完成,首次请求返回默认头像属正常,重试即可 |
| 迁移失败 | 查看容器日志,`goose status` 确认当前版本 |
| 数据库连不上 | 容器内 `DB_HOST` 须为服务名 `postgres`,不是 `localhost` |

---

**上一篇**:[07-frontend.md](./07-frontend.md) —— 前端设计
**下一篇**:[09-security.md](./09-security.md) —— 安全设计
