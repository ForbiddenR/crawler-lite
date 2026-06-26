# syntax=docker/dockerfile:1
#
# Master image — multi-stage. The frontend is built and the result is
# embedded into the Go binary via //go:embed (internal/web/dist), so the
# final image is a single static binary with the whole control plane
# (REST API + WebSocket log stream + SPA) baked in.
#
# Build:
#   docker build -t crawler-lite-master --build-arg VERSION=$(git describe --tags --always --dirty) .
#
# Run: see docker-compose.prod.yml. Needs DATABASE_DSN, REDIS_ADDR,
# MINIO_*, JWT_SECRET, WORKER_SHARED_SECRET at minimum.

ARG GO_VERSION=1.26
ARG NODE_VERSION=22
ARG VERSION=dev

# --- 1. Frontend build ------------------------------------------------------
FROM node:${NODE_VERSION}-alpine AS frontend-build
WORKDIR /web
# Install pnpm via corepack (shipped with node:22-alpine).
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
# Copy the rest of the frontend source and build. Output: /web/dist.
COPY web/ ./
RUN pnpm build

# --- 2. Go build ------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS go-build
ARG VERSION
WORKDIR /src
# Module cache layer: copy only manifests first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Place the built frontend where internal/web expects it for embedding.
COPY --from=frontend-build /web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X github.com/yourteam/crawler-lite/internal/version.Version=${VERSION}" \
      -o /out/master ./cmd/master

# --- 3. Runtime -------------------------------------------------------------
# Alpine runtime: keeps a small image while still providing sh/apk for basic
# container debugging. The binary is statically linked (CGO_ENABLED=0). Runs as
# non-root.
FROM alpine:3.20 AS runtime
RUN adduser -D -H -u 65532 appuser
USER appuser
COPY --from=go-build /out/master /master
# :8000 = HTTP (REST + WS + SPA), :9000 = gRPC (workers).
EXPOSE 8000 9000
ENTRYPOINT ["/master"]
