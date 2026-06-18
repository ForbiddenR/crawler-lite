# crawler-lite

A focused crawler platform for Python + Selenium spiders. Go master/worker,
React UI. **Week-2 in progress** — see [`../PLAN.md`](../PLAN.md) for the full
design.

What is here right now:

- ✅ Postgres / Redis / MinIO via Docker Compose
- ✅ Migrations for users, projects, spiders, tasks, items, artifacts
- ✅ Go master: HTTP API (login, spiders + git sync, tasks + items/screenshots/log),
  gRPC server with task dispatcher, log pub/sub fan-out, MinIO log/artifact upload
- ✅ Go worker: subprocess executor (`python -m crawlerkit.runner`) over FD 3 JSONL,
  stdout/stderr forwarded as INFO/ERROR, source zip download from MinIO
- ✅ Python `crawlerkit` SDK: `Spider` base class, `log` / `item` / `screenshot` /
  `captcha` events, real Selenium driver (Chromium via Selenium Manager) with
  `MockDriver` fallback so authoring works without a browser installed
- ✅ Schedules: cron-driven task creation with in-process daemon
- ✅ React + Vite + TanStack Router + Tailwind v4: login, dashboard, spiders list +
  detail, schedules list, tasks list + detail with **live WS log tail**, items, screenshots gallery

What is **not** here yet (later weeks):

- ❌ Proxies, retries, notifications
- ❌ HAR viewer / network tab
- ❌ Production Dockerfiles, TLS reverse proxy

---

## Prerequisites

- Go 1.26+
- Node 20+ and `pnpm`
- Docker + Compose v2
- `protoc` 3.21+ (Homebrew: `brew install protobuf`; Debian: `apt install protobuf-compiler`)

## First-time setup

```sh
# 1. Install Go protoc plugins and goose
make tools

# 2. Generate gRPC stubs from .proto
make gen

# 3. Start Postgres / Redis / MinIO
make up

# 4. Apply migrations
make migrate

# 5. Tidy module
make tidy

# 6. Frontend deps
cd web && pnpm install && cd ..
```

## Run it

In four terminals:

```sh
# 1. infra (already up if you ran `make up`)

# 2. master
cp .env.example .env
make run-master

# 3. worker
make run-worker        # tail will show: "connected, waiting for assignments"

# 4. frontend
make web-dev           # http://localhost:5173
```

## Bootstrap a user

There is no admin UI yet. Generate a hash and insert directly:

```sh
go run ./cmd/master hash-password admin
# prints: $2a$10$....

docker compose exec postgres psql -U crawler -d crawler -c \
  "INSERT INTO users (email, password_hash, role)
   VALUES ('admin@local', '<paste hash here>', 'admin');"
```

Then log in at `http://localhost:5173/login` with `admin@local` / `admin`.

## Layout

```
cmd/{master,worker}        binaries
internal/app/              ◄── master composition root (read this first)
internal/workerapp/        ◄── worker composition root
internal/api/              HTTP handlers
internal/{auth,spider,task,hub,proxy,...}
internal/repository/       Postgres data access (raw pgx for now)
internal/storage/          MinIO client
internal/pb/               generated gRPC stubs (after `make gen`)
proto/worker/v1/           gRPC contract (the only proto)
db/migrations/             goose .sql files
web/                       Vite + React 19 SPA
```

## Tech choices

See [`../PLAN.md`](../PLAN.md) for the full reasoning behind every library
choice. Short version:

- Backend: Go 1.26 stdlib `net/http` (chi for middleware), `pgx/v5`, `slog`,
  `grpc`, `golang-jwt/v5`, `redis/go-redis/v9`, `minio-go/v7`. Manual
  constructor DI. No GORM, no Wire, no Dig.
- Frontend: React 19 + TS 5.6 + Vite 6 + Tailwind v4 + TanStack Router/Query
  + Zustand + a tiny hand-rolled shadcn-style component layer.

## Make targets

```sh
make help
```
