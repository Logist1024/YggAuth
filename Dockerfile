# syntax=docker/dockerfile:1
#
# 多阶段构建:Node 构建前端 → Go 编译二进制 → distroless 运行。
#
# 三阶段而非两阶段:pnpm 的依赖树有几百 MB,与最终镜像毫无关系。
# 直接在 Go 构建镜像里装 Node 会让镜像大出一个数量级。

# ---------------------------------------------------------------- 前端
FROM node:24-alpine AS web

# pnpm 单独一层。package.json / lockfile 很少变,而依赖安装很慢 ——
# 不分层的话每次改一行前端代码都要重装一遍全部依赖。
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate

WORKDIR /src/web

COPY web/package.json web/pnpm-workspace.yaml web/pnpm-lock.yaml ./
COPY web/shared/package.json ./shared/
COPY web/apps/account/package.json ./apps/account/
COPY web/apps/admin/package.json ./apps/admin/

RUN --mount=type=cache,id=pnpm,target=/pnpm/store pnpm install --frozen-lockfile

COPY web/ ./
COPY web/tsconfig.base.json ./tsconfig.base.json

RUN pnpm build

# ---------------------------------------------------------------- 后端
FROM golang:1.27-alpine AS build

WORKDIR /src

# Go 模块缓存同理:依赖下载是最慢的一步,而它很少变。
COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod go mod download

COPY . .

# 前端产物在 web 阶段构建,这里拷进来后由 go:embed 打进二进制。
COPY --from=web /src/internal/webserver/dist ./internal/webserver/dist

ARG VERSION=dev
RUN --mount=type=cache,id=gocache,target=/root/.cache/go-build \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/yggauth ./cmd/yggauth

# 给运行阶段准备一个属主正确的空目录(原因见运行阶段的 /data 说明)。
# distroless 里没有 shell,建不了目录,只能在这里建好再拷过去。
RUN mkdir -p /out/data

# 静态嵌入产物不需要再单独 COPY:它已经在二进制里。

# ---------------------------------------------------------------- 运行
FROM gcr.io/distroless/static-debian12:nonroot

# 非 root 运行。皮肤文件由本进程写入,所以需要一个可写的 data 目录。
COPY --from=build --chown=nonroot:nonroot /out/yggauth /usr/local/bin/yggauth

# 皮肤与头像写在 /data。compose 挂的是**命名卷**,而命名卷首次创建时会
# 继承镜像里该路径的内容与属主 —— 镜像里没有 /data 的话,卷就是 root:root,
# nonroot 进程启动即 panic: mkdir /data/textures: permission denied。
# distroless 没有 shell,没法在运行阶段 mkdir/chown,所以目录在 build 阶段建好,
# 这里整目录拷过来并改属主。
COPY --from=build --chown=nonroot:nonroot /out/data /data

# 时区数据:邮件模板与审计日志都要按本地时区渲染。
# distroless 刻意不带这些,缺了会让时间戳全变成 UTC。
#
# golang:alpine 里只有 /usr/local/go/lib/time/zoneinfo.zip,并没有 zoneinfo 目录,
# 而运行镜像(distroless static)也没有 /usr/share/zoneinfo。
# 所以拷 zip,再让 Go 通过 ZONEINFO 定位 —— 这是 time 包查找时区的第一优先级,
# 不依赖系统目录,也不需要往运行镜像里塞整个 tzdata。
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /zoneinfo.zip

ENV ZONEINFO=/zoneinfo.zip \
    TZ=Asia/Shanghai \
    DATA_DIR=/data \
    APP_HOST=0.0.0.0 \
    APP_PORT=3000

VOLUME ["/data"]
EXPOSE 3000

# 就绪探针查的是依赖而不是进程存活:数据库连不上时
# 服务进程还在,但它处理不了任何请求,这时候算「活着」是误导。
# distroless 没有 shell,所以用可执行文件自身做探针。
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 \
  CMD ["/usr/local/bin/yggauth", "healthcheck"]

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/yggauth"]
CMD ["serve"]