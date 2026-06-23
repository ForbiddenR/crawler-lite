.DEFAULT_GOAL := help
SHELL := /bin/bash

# ---------------------------------------------------------------------------
# Tooling versions
# ---------------------------------------------------------------------------
PROTOC_GEN_GO_VERSION      := v1.36.0
PROTOC_GEN_GO_GRPC_VERSION := v1.5.1
GOOSE_VERSION              := v3.22.1
PROTOC_VERSION             := 27.3

GOBIN ?= $(shell go env GOPATH)/bin

# protoc release asset: map `uname -m` to the names protobuf publishes.
PROTOC_ARCH := $(shell uname -m | sed -e 's/x86_64/x86_64/' -e 's/aarch64/aarch_64/' -e 's/arm64/aarch_64/')
PROTOC_ZIP  := protoc-$(PROTOC_VERSION)-linux-$(PROTOC_ARCH).zip
PROTOC_URL  := https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/$(PROTOC_ZIP)

# ---------------------------------------------------------------------------
# Generated code paths
# ---------------------------------------------------------------------------
PROTO_DIR := proto
PB_OUT    := internal/pb

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------
DATABASE_DSN ?= postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable
MIGRATIONS_DIR := db/migrations

# ---------------------------------------------------------------------------
# Production deployment
# ---------------------------------------------------------------------------
# Image tag defaults to the git describe output (tag, or commit+dirty).
# Override with `make prod-build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE_REGISTRY ?= crawler-lite
COMPOSE_PROD := docker compose -f docker-compose.yml -f docker-compose.prod.yml

# ===========================================================================
.PHONY: help
help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---------------------------------------------------------------------------
# Dev tools
# ---------------------------------------------------------------------------
.PHONY: tools
tools: install-protoc tools-uv ## Install protoc, Go protoc plugins, goose, and uv locally
	GOBIN=$(GOBIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(GOBIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	GOBIN=$(GOBIN) go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

.PHONY: tools-uv
tools-uv: ## Install uv (Astral) — the worker uses it to install per-spider requirements.txt
	@command -v uv >/dev/null 2>&1 && { echo "uv already installed: $$(uv --version)"; exit 0; } || true
	@echo "installing uv via https://astral.sh/uv/install.sh"
	@curl -LsSf https://astral.sh/uv/install.sh | sh
	@echo "(if uv is not on PATH yet, add ~/.local/bin to PATH or set UV_PATH in .env)"

.PHONY: install-protoc
install-protoc: ## Install the latest protoc into /usr/local (Linux only)
	@command -v protoc >/dev/null 2>&1 && { echo "protoc already installed: $$(protoc --version)"; exit 0; } || true
	@command -v unzip >/dev/null 2>&1 || { echo "error: 'unzip' is required (apt install -y unzip)"; exit 1; }
	@echo "installing protoc $(PROTOC_VERSION) for linux-$(PROTOC_ARCH)"
	@tmp=$$(mktemp -d) && \
		curl -fLo "$$tmp/$(PROTOC_ZIP)" "$(PROTOC_URL)" && \
		sudo unzip -o "$$tmp/$(PROTOC_ZIP)" -d /usr/local 'bin/protoc' 'include/*' && \
		rm -rf "$$tmp" && \
		protoc --version

# ---------------------------------------------------------------------------
# Code generation
# ---------------------------------------------------------------------------
.PHONY: gen
gen: gen-proto ## Run all generators (proto only for now)

.PHONY: gen-proto
gen-proto: ## Generate Go stubs from .proto files
	@mkdir -p $(PB_OUT)
	protoc \
		--go_out=$(PB_OUT) --go_opt=paths=source_relative \
		--go-grpc_out=$(PB_OUT) --go-grpc_opt=paths=source_relative \
		--proto_path=$(PROTO_DIR) \
		$(shell find $(PROTO_DIR) -name '*.proto')

# ---------------------------------------------------------------------------
# Database migrations (goose)
# ---------------------------------------------------------------------------
.PHONY: migrate
migrate: ## Apply all pending migrations
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(DATABASE_DSN)" \
		goose -dir $(MIGRATIONS_DIR) up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(DATABASE_DSN)" \
		goose -dir $(MIGRATIONS_DIR) down

.PHONY: migrate-status
migrate-status: ## Show migration status
	GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(DATABASE_DSN)" \
		goose -dir $(MIGRATIONS_DIR) status

# ---------------------------------------------------------------------------
# Build / run
# ---------------------------------------------------------------------------
.PHONY: build
build: ## Build master and worker binaries into ./bin
	@mkdir -p bin
	go build -o bin/master ./cmd/master
	go build -o bin/worker ./cmd/worker

.PHONY: run-master
run-master: ## Run the master locally (reads .env if present)
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/master

.PHONY: run-worker
run-worker: ## Run a worker locally (reads .env if present)
	set -a; [ -f .env ] && . ./.env; set +a; go run ./cmd/worker

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: fmt
fmt: ## gofmt + goimports
	gofmt -w .

.PHONY: test
test: ## Run Go tests
	go test ./...

.PHONY: py-install
py-install: ## Install crawlerkit (editable, with selenium + test extras)
	python3 -m pip install -e crawlerkit-py[selenium,test]

.PHONY: py-test
py-test: ## Run crawlerkit-py tests
	cd crawlerkit-py && python3 -m pytest -q

# ---------------------------------------------------------------------------
# Docker
# ---------------------------------------------------------------------------
.PHONY: up
up: ## Start Postgres / Redis / MinIO
	docker compose up -d postgres redis minio

.PHONY: down
down: ## Stop docker compose stack
	docker compose down

.PHONY: ps
ps:
	docker compose ps

# ---------------------------------------------------------------------------
# Frontend
# ---------------------------------------------------------------------------
.PHONY: web-dev
web-dev: ## Run the React dev server
	cd web && pnpm dev

.PHONY: web-build
web-build: ## Build the React app
	cd web && pnpm build

# ---------------------------------------------------------------------------
# Production deployment (see deploy/RUNBOOK.md)
# ---------------------------------------------------------------------------
.PHONY: prod-build
prod-build: ## Build the master + worker images, tagged $(IMAGE_REGISTRY)/…:$(VERSION)
	$(COMPOSE_PROD) build --build-arg VERSION=$(VERSION)

.PHONY: prod-push
prod-push: ## Push the master + worker images to $(IMAGE_REGISTRY)
	$(COMPOSE_PROD) push

.PHONY: prod-up
prod-up: ## Start the full production stack (data + master + workers + Caddy)
	$(COMPOSE_PROD) up -d

.PHONY: prod-down
prod-down: ## Stop the production stack (keeps volumes)
	$(COMPOSE_PROD) down

.PHONY: prod-logs
prod-logs: ## Tail production stack logs
	$(COMPOSE_PROD) logs -f --tail=200

.PHONY: prod-ps
prod-ps: ## Show production stack status
	$(COMPOSE_PROD) ps

.PHONY: prod-migrate
prod-migrate: ## Apply pending migrations to the prod database (one-shot goose container)
	@set -a; [ -f .env ] && . ./.env; set +a; \
	: "${DATABASE_DSN:?set DATABASE_DSN in .env (host should be 'postgres')}"; \
	$(COMPOSE_PROD) run --rm --no-deps \
		-v "$(PWD)/db/migrations:/migrations:ro" \
		-e GOOSE_DRIVER=postgres \
		-e GOOSE_DBSTRING="$$DATABASE_DSN" \
		--entrypoint sh \
		golang:1.26-alpine -c '\
			set -e; \
			echo "installing goose $(GOOSE_VERSION)…"; \
			go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION); \
			/root/go/bin/goose -dir /migrations status; \
			/root/go/bin/goose -dir /migrations up'

.PHONY: backup
backup: ## Back up Postgres + MinIO to ./backups/<ts>/ (see deploy/backup.sh)
	./deploy/backup.sh

.PHONY: restore
restore: ## Restore from a backup dir: make restore BACKUP=./backups/<ts>
	./deploy/restore.sh $(BACKUP)

.PHONY: load-test
load-test: ## Run k6 HTTP perf + queue-burst pipeline throughput tests
	@command -v k6 >/dev/null 2>&1 || { echo "error: k6 not installed (https://k6.io)"; exit 1; }
	@mkdir -p loadtest/results
	@echo "==> k6 HTTP perf (set LOGIN_EMAIL/LOGIN_PASSWORD/SPIDER_ID via env)"
	k6 run --summary-export loadtest/results/api-results.json loadtest/api.js || true
	@echo "==> queue burst"
	./loadtest/queue_burst.sh || true
	@echo "==> results in loadtest/results/"
