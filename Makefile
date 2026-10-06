# YggAuth —— 构建与开发入口
#
# 约定:所有目标都能在本机跑,不依赖容器运行时(数据库用 embedded-postgres)。

SHELL := /bin/bash
.DEFAULT_GOAL := help

GO      ?= go
GOFLAGS ?=
BIN_DIR := bin
BINARY  := $(BIN_DIR)/yggauth
PKG     := ./...

# 前端产物由 vite 直接输出到 internal/webserver/dist,再由 //go:embed all:dist 打进二进制。
# 该目录是入库文件(漏掉它,新克隆的仓库 go build 会失败),所以不参与 clean。

.PHONY: help
help: ## 显示所有可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- M0 工程地基

.PHONY: build
build: web ## 构建二进制(含前端产物 embed)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags="-s -w" -o $(BINARY) ./cmd/yggauth
	@echo "built $(BINARY)"

.PHONY: build-go
build-go: ## 只构建 Go 二进制(前端产物已存在时)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags="-s -w" -o $(BINARY) ./cmd/yggauth

.PHONY: test
test: ## 跑单元测试(不含集成测试)
	$(GO) test -count=1 $(PKG)

.PHONY: test-integration
test-integration: ## 跑集成测试(需要 embedded-postgres,自动拉起真实 PostgreSQL 16)
	$(GO) test -count=1 -tags=integration -timeout=20m $(PKG)

.PHONY: test-all
test-all: test test-integration ## 跑全部测试

.PHONY: lint
lint: ## golangci-lint(含 gofumpt / errcheck / staticcheck)
	golangci-lint run ./...

.PHONY: fmt
fmt: ## gofumpt 格式化 + goimports
	gofumpt -l -w .
	goimports -w .

.PHONY: verify
verify: fmt lint test ## 提交前完整校验:格式化 + lint + 单测
	@echo "verify ok"

## ---------------------------------------------------------------- 运行时

.PHONY: dev
dev: ## 本地开发运行(迁移由 DB_AUTO_MIGRATE 控制,默认开,启动即执行)
	$(GO) run ./cmd/yggauth serve

# 迁移不用外部 goose CLI —— 二进制自带 migrate 子命令,迁移文件已 go:embed 进去,
# 读的配置与 serve 完全同一份(.env 由调用方 source),少一个隐藏的工具依赖。
.PHONY: migrate
migrate: ## 执行数据库迁移(等价于 yggauth migrate,默认 up)
	$(GO) run ./cmd/yggauth migrate

.PHONY: migrate-down
migrate-down: ## 回滚一次迁移
	$(GO) run ./cmd/yggauth migrate -to down

.PHONY: migrate-status
migrate-status: ## 查看迁移状态
	$(GO) run ./cmd/yggauth migrate -to status

.PHONY: sqlc
sqlc: ## 根据 db/queries 生成类型安全的 Go 代码
	sqlc generate

.PHONY: sqlc-diff
sqlc-diff: sqlc ## 校验生成物与查询定义一致(CI 用)
	@git diff --exit-code

## ---------------------------------------------------------------- 前端

.PHONY: web
web: ## 构建两个前端应用(account-web + admin-web)
	cd web && pnpm install --frozen-lockfile && pnpm build

.PHONY: web-dev
web-dev: ## 前端开发服务器(Vite:账号站 5173、后台 5174,代理到后端 3000)
	cd web && pnpm install && pnpm dev

.PHONY: web-lint
web-lint: ## 前端 lint
	cd web && pnpm lint

## ---------------------------------------------------------------- 其它

.PHONY: tidy
tidy: ## 整理依赖
	$(GO) mod tidy

.PHONY: vuln
vuln: ## Go 依赖漏洞扫描
	govulncheck ./...

.PHONY: check-terms
check-terms: ## 术语门禁(ADR-010):内核与平台层不得出现业务词
	@bash scripts/check-terms.sh

.PHONY: golden-path
golden-path: ## 黄金路径验收(docs/configuration.md 8);真容器 1/2/7 步需要 Docker
	@bash deploy/test/golden-path.sh

.PHONY: clean
clean: ## 清理构建产物(只清二进制;前端 embed 产物必须保留)
	rm -rf $(BIN_DIR)