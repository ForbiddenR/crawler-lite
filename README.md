# crawler-lite

A focused crawler platform for Python + Selenium spiders. Go master/worker,
React UI. The core loop and operational surface are implemented; see
[`../PLAN.md`](../PLAN.md) for the original design.

What is here right now:

- ✅ Postgres / Redis / MinIO via Docker Compose
- ✅ Migrations for users, projects, spiders, tasks, items, artifacts, schedules,
  notification channels
- ✅ Go master: HTTP API (login, spiders + git sync, tasks + items/screenshots/log,
  schedules, notifications), gRPC server with task dispatcher, log pub/sub fan-out,
  MinIO log/artifact upload
- ✅ Go worker: subprocess executor (`python -m crawlerkit.runner`) over FD 3 JSONL,
  stdout/stderr forwarded as INFO/ERROR, source zip download from MinIO
- ✅ Python `crawlerkit` SDK: `Spider` base class, `log` / `item` / `screenshot` /
  `captcha` events, real Selenium driver (Chromium via Selenium Manager) with
  `MockDriver` fallback so authoring works without a browser installed
- ✅ Schedules: cron-driven task creation with in-process daemon
- ✅ Retries + captcha path: configurable retry policy with backoff, captcha
  classified as a distinct non-retryable terminal state
- ✅ Notifications: configurable channels (Slack/Telegram/Discord/email/webhook via
  shoutrrr) fanned out on terminal task events
- ✅ Deployment: production Dockerfiles (master embeds the SPA via `go:embed`,
  worker bundles Chromium + uv + tini), prod Compose overlay for app services,
  external reverse proxy/TLS, backup/restore scripts, runbook, k6 + queue-burst
  load tests
- ✅ React + Vite + TanStack Router + Tailwind v4: login, dashboard, spiders list +
  detail, schedules list, tasks list + detail with **live WS log tail**, items,
  screenshots gallery, notifications management

What is **not** here yet:

- ❌ Proxies (rotating proxy pool, health checks, per-task selection — a
  `ProxyUrl` field is threaded through dispatch but always empty today)
- ❌ HAR viewer / network tab in the task detail UI

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

## Spider dependencies

A spider repo may include a `requirements.txt` at its root. On the first task
for that spider, the worker hashes the file and runs `uv pip install -r
requirements.txt crawlerkit[selenium]` into a venv under `WORKER_VENV_DIR`
keyed by the hash. Subsequent tasks reuse the cached venv (one `stat` call)
and the spider's Python subprocess runs against the venv's interpreter. Install
output streams into the task log prefixed `[deps] …` so authors see progress
live, and install failures terminate the task with `error_class=deps`.

Install `uv` once on each worker (`make tools-uv`). If `uv` isn't on PATH at
worker startup, the worker logs a warning and falls back to the system
`python3` — dep-free spiders keep working, spiders with a `requirements.txt`
will see an `ImportError` at runtime.

Worker env knobs:

| Var | Default | Meaning |
|---|---|---|
| `WORKER_VENV_DIR` | `/var/lib/crawler-lite/venvs` | parent dir for cached venvs; persists across reboots |
| `UV_PATH` | _(empty)_ | absolute path to `uv`; empty → auto-detect on `PATH` |

## Layout

```
cmd/{master,worker}        binaries
internal/app/              ◄── master composition root (read this first)
internal/workerapp/        ◄── worker composition root
internal/api/              HTTP handlers
internal/{auth,spider,task,hub,notify,schedule,...}
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

- Backend: Go 1.26, `gin` (HTTP + middleware), `pgx/v5`, `slog`,
  `grpc`, `golang-jwt/v5`, `redis/go-redis/v9`, `minio-go/v7`. Manual
  constructor DI. No GORM, no Wire, no Dig.
- Frontend: React 19 + TS 5.6 + Vite 6 + Tailwind v4 + TanStack Router/Query
  + Zustand + a tiny hand-rolled shadcn-style component layer.

## Make targets

```sh
make help
```

Production deploy, backup/restore, and load testing live behind `make prod-*`,
`make backup` / `make restore`, and `make load-test`. See
[`deploy/RUNBOOK.md`](deploy/RUNBOOK.md) for the full operational walkthrough.
