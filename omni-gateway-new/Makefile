# omni-gateway —— 本地开发入口。
#
# `make` 或 `make help` 会列出全部可用目标。
#
# 验证门禁是 `make test`，它执行 scripts/test.sh —— 与 CI 完全相同的流程。
# 把 TEST_DATABASE_URL 指向一个可访问的空 PostgreSQL 库即可启用
# internal/store 的集成测试；不设置时这些测试会自行跳过，store 的覆盖率
# 随之塌陷。

SHELL := /bin/sh

BACKEND_DIR  := backend
FRONTEND_DIR := frontend
STATIC_DIR   := $(BACKEND_DIR)/internal/httpapi/static

GO      := go
GOFLAGS := -mod=readonly

# 构建产物（已在 .gitignore 中忽略：backend/server）。
BINARY ?= $(BACKEND_DIR)/server

# `make run` 使用的本地服务端设置。
LISTEN ?= :8080
DB_URL ?=

# 默认留空，这样 scripts/test.sh 会打印警告而不是静默跳过。可在单次调用时
# 覆盖：
#   make test TEST_DATABASE_URL='postgres://user@localhost:5432/postgres?sslmode=disable'
TEST_DATABASE_URL ?=
export TEST_DATABASE_URL

.DEFAULT_GOAL := help

.PHONY: help fmt vet webui build test test-backend test-frontend run clean

help: ## 显示本帮助
	@printf 'Usage: make <target>\n\nTargets:\n'
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

fmt: ## 若有 Go 文件未通过 gofmt 格式化则失败
	@cd $(BACKEND_DIR) && unformatted=$$(gofmt -l .); \
	  if [ -n "$$unformatted" ]; then \
	    printf '%s\n' "$$unformatted" >&2; \
	    printf '%s\n' "unformatted files listed above; run: (cd $(BACKEND_DIR) && gofmt -w .)" >&2; \
	    exit 1; \
	  fi; \
	  echo "gofmt: clean"

vet: ## 对后端执行 go vet
	cd $(BACKEND_DIR) && $(GO) vet $(GOFLAGS) ./...

webui: ## 构建 Vue 控制台并复制到 Go 嵌入目录
	cd $(FRONTEND_DIR) && npm ci && npm run build
	rm -rf $(STATIC_DIR)/assets
	cp -R $(FRONTEND_DIR)/dist/. $(STATIC_DIR)/
	@echo "embedded console assets refreshed in $(STATIC_DIR)"

build: ## 构建把真实控制台嵌入其中的服务端二进制
	cd $(BACKEND_DIR) && $(GO) build $(GOFLAGS) -o server ./cmd/server
	@echo "built $(BINARY)"

test: ## 完整验证门禁：gofmt、vet、build、go test -race、前端构建 + 单元测试
	scripts/test.sh

test-backend: ## 只运行 Go 测试
	cd $(BACKEND_DIR) && $(GO) test $(GOFLAGS) -race -count=1 ./...

test-frontend: ## 只运行前端单元测试
	cd $(FRONTEND_DIR) && npm run test

run: ## 本地运行服务端，加载 .env（可用 LISTEN= DB_URL= 覆盖）
	@if [ ! -f .env ]; then \
	  echo "no .env found; create one first:  cp .env.example .env" >&2; \
	  exit 1; \
	fi
	set -a; . ./.env; set +a; \
	  cd $(BACKEND_DIR) && \
	  GATEWAY_LISTEN_ADDR="$(LISTEN)" \
	  $(if $(DB_URL),GATEWAY_DB_URL='$(DB_URL)',) \
	  $(GO) run ./cmd/server

clean: ## 清理构建产物（二进制、内嵌控制台资源、前端 dist）
	rm -f $(BINARY)
	rm -rf $(FRONTEND_DIR)/dist
	find $(STATIC_DIR) -mindepth 1 ! -name .keep -delete
	@echo "cleaned"
