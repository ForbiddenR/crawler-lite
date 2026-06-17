.DEFAULT_GOAL := help
SHELL := /bin/bash

# ---------------------------------------------------------------------------
# Tooling versions
# ---------------------------------------------------------------------------
PROTOC_GEN_GO_VERSION      := v1.36.0
PROTOC_GEN_GO_GRPC_VERSION := v1.5.1
GOOSE_VERSION              := v3.22.1

GOBIN ?= $(shell go env GOPATH)/bin

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

# ===========================================================================
.PHONY: help
help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ---------------------------------------------------------------------------
# Dev tools
# ---------------------------------------------------------------------------
.PHONY: tools
tools: ## Install Go protoc plugins and goose locally
	GOBIN=$(GOBIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(GOBIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	GOBIN=$(GOBIN) go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

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
	go run ./cmd/master

.PHONY: run-worker
run-worker: ## Run a worker locally
	go run ./cmd/worker

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: fmt
fmt: ## gofmt + goimports
	gofmt -w .

.PHONY: test
test: ## Run Go tests
	go test ./...

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
