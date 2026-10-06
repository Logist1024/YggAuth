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

三阶段构建:Node 构建前端 → Go 编译二进制 → distroless 运行。

```dockerfile
# ---------- 前端 ----------
FROM node:24-alpine AS web
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate
WORKDIR /src/web
COPY web/package.json web/pnpm-workspace.yaml web/pnpm-lock.yaml ./
COPY web/shared/package.json ./shared/
COPY web/apps/account/package.json ./apps/account/
COPY web/apps/admin/package.json ./apps/admin/
RUN --mount=type=cache,id=pnpm,target=/pnpm/store pnpm install --frozen-lockfile
COPY web/ ./
COPY tsconfig.base.json ./tsconfig.base.json
RUN pnpm build

# ---------- Go ----------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/internal/webserver/dist ./internal/webserver/dist
RUN --mount=type=cache,id=gocache,target=/root/.cache/go-build \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" -o /out/yggauth ./cmd/yggauth

# ---------- 运行 ----------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=nonroot:nonroot /out/yggauth /usr/local/bin/yggauth
COPY --from=build /usr/local/go/lib/time/zoneinfo /usr/share/zoneinfo
ENV TZ=Asia/Shanghai DATA_DIR=/data APP_HOST=0.0.0.0 APP_PORT=3000
VOLUME ["/data"]
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 \
  CMD ["/usr/local/bin/yggauth", "healthcheck"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/yggauth"]
CMD ["serve"]
```

**关键点**:
- **三个阶段**:pnpm 的依赖树有几百 MB,与最终镜像无关。装进 Go 阶段会让镜像大出一个数量级。
- **产物直接构建到 `internal/webserver/dist/`**:`go:embed` 只能读取包目录内的文件。
  Vite 的 `outDir` 用相对 URL 计算(`../../../internal/webserver/dist/<name>`),不写死绝对路径。
- `CGO_ENABLED=0` —— 静态编译,镜像无需 libc。
- distroless —— 无 shell、无包管理器,攻击面小。
- **时区数据单独 COPY**:distroless 刻意不带这些,缺了会让邮件时间戳与审计日志全变成 UTC。
- **健康检查用二进制自身**(`/usr/local/bin/yggauth healthcheck`):
  distroless 没有 shell 或 curl,探针必须是可执行文件本身。
  它查的是 `/health/ready` 而不是「进程还在」—— 数据库连不上时
  进程还活着,但处理不了任何请求,那时候算「健康」是误导。
- `USER nonroot:nonroot` —— 皮肤文件由本进程写入,所以 `/data` 要可写。


## 四、docker-compose.yml

三个服务:`app`、`postgres`、`nginx`。完整内容见仓库根的 `docker-compose.yml`,
这里只说明几个不显然的决定:

```yaml
services:
  postgres:
    healthcheck:
      # pg_isready 只说明进程活着,不代表能接受认证连接。
      # 数据库起来但还没跑完初始化脚本时,pg_isready 已成功,
      # 而应用连上去会立刻失败。这里用真实的 SELECT 1。
      test: ["CMD-SHELL", "pg_isready -U ${DB_USER} -d ${DB_NAME} && psql -U ${DB_USER} -d ${DB_NAME} -c 'SELECT 1'"]
    # 刻意不映射端口:把它发布到 0.0.0.0 等于把整个数据面暴露在公网。
    networks: [internal]

  app:
    depends_on:
      postgres: { condition: service_healthy }
    volumes:
      - appdata:/data      # 命名卷,不是绑定挂载
      - ./db/migrations:/migrations:ro   # 只读:应用执行它,但从不写入
    networks: [internal, edge]

  nginx:
    depends_on:
      app:
        # 等**就绪**而不是容器启动:nginx 早于应用就绪收到第一个请求时,
        # 用户拿到 502,而此时应用其实没问题。
        condition: service_healthy
    networks: [edge]
```

**网络分段**:数据库只在 `internal` 网络上,不对外;
`nginx` 在 `edge` 上做 TLS 终止与应用只连不通数据面。

**为什么不用多副本**:皮肤存储是本地磁盘(ADR-007),
多副本会让 `/mc/textures/:hash` 在不同实例上返回不一致的结果。
要横向扩展必须先把存储换成对象存储。

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
|| `MC_SKIN_EXTERNAL` | `false` | | 外部皮肤站回源 |
| `MC_SKIN_EXTERNAL_BASE_URL` | — | | 外部皮肤站地址,默认取 `PUBLIC_BASE_URL` |
| `MC_NAME_RETENTION_DAYS` | `90` | | 改名后旧名保留天数 |
| `HTTP_PORT` / `HTTPS_PORT` | `80` / `443` | | 仅 compose 使用:nginx 的对外端口 |
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
| `/metrics` | 监控 | Prometheus 指标,**不对公网开放** |

二进制自带 `healthcheck` 子命令,直接查 `/health/ready`:

```bash
yggauth healthcheck            # 用 APP_HOST / APP_PORT
yggauth healthcheck --port 3000
```

**为什么不用 curl/wget**:distroless 镜像既没有 shell 也没有这些工具。
常见的绕法是 `COPY --from=build /bin/busybox /busybox` 再用 busybox 的 wget,
但那等于把一个带大量已知 CVE 的静态二进制塞进生产镜像 —— 为了做一次
HTTP GET 完全不值得。让进程自己查自己是最省事也最安全的做法。

### compose 里必须**重复**声明 app 的 healthcheck

这是 M7 实际踩到的坑。nginx 依赖 app 就绪:

```yaml
nginx:
  depends_on:
    app: { condition: service_healthy }
```

但 **compose 只读本文件的 `healthcheck` 段,不看 Dockerfile 里的 `HEALTHCHECK`**。
少写这一段的话依赖条件永远无法满足,`docker compose up` 直接报错。
所以同一份探针要在 Dockerfile 与 compose 里各写一次:

```yaml
app:
  healthcheck:
    test: ["CMD", "/usr/local/bin/yggauth", "healthcheck"]
    interval: 30s
    timeout: 5s
    retries: 3
    start_period: 40s
```

`start_period` 要留够:启动时要跑数据库迁移,冷启动可能十几秒,
期间探针失败是正常的,不该触发重启。

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

开发时前端跑 Vite dev server —— 账号站 `http://localhost:5173`、后台 `http://localhost:5174/admin/`（端口以 `web/apps/*/vite.config.ts` 为准），代理 `/api` `/oauth` `/mc` 到后端 3000，免去每改前端就重建二进制。

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

> **要搭测试环境**:本文讲的是生产形态(公网域名、Let's Encrypt、Secure cookie)。
> 没有域名、想要可丢弃可重建的环境,走 [10-test-deployment.md](./10-test-deployment.md)。

---

**上一篇**:[07-frontend.md](./07-frontend.md) —— 前端设计
**下一篇**:[09-security.md](./09-security.md) —— 安全设计
