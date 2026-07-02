# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

crawler-lite: a crawler platform for Python + Selenium spiders. Go master/worker over gRPC, React UI, Postgres/Redis/MinIO backing. The master schedules tasks and serves the API + embedded SPA; workers run spider Python via a subprocess and stream logs/items/screenshots back. The Python SDK is `crawlerkit-py/`.

Module path is the placeholder `github.com/yourteam/crawler-lite` — not yet renamed.

> **Standalone project.** This directory has its own `go.mod` and is self-contained. It happens to be nested at `/workspace/crawlab/crawler-lite`, but the parent `/workspace/crawlab` is a **different, unrelated project** (the upstream CrawLab repo) — do not mix the two. Run all Go/frontend commands from this directory.

> **Design rationale.** `docs/DESIGN.md` is the discussion-oriented design document (architecture intent, storage layering, end-to-end flows). Read it for the "why". It describes the **current-stage preliminary design**: simplified mechanisms that will be refined later are marked inline as 「（现阶段简化：…，后续开发…）」 — these are intentional, not bugs to fix. Code comments carry the matching "v1 / week N / for production" markers.

## Commands

**First-time setup:** `make tools && make gen && make up && make migrate && make tidy` (Go side); `cd web && pnpm install` (frontend); `make py-install` (Python SDK, editable).

**Run locally** (needs `.env` copied from `.env.example`, and `docker compose up -d postgres redis minio`): four terminals — `make run-master`, `make run-worker`, `make web-dev` (→ http://localhost:5173). Vite proxies `/api`→`:8000` incl. WebSocket.

| Command | What |
|---|---|
| `make build` | `go build` master+worker into `bin/` |
| `make test` | `go test ./...` |
| `go test -run TestName -count=1 ./internal/task/...` | single test (run from repo root) |
| `make fmt` / `make tidy` | gofmt / `go mod tidy` |
| `make gen` | regenerate gRPC stubs (`make gen-proto`) into `internal/pb/` |
| `make migrate` / `migrate-down` / `migrate-status` | goose against `$DATABASE_DSN` |
| `make web-dev` / `web-build` | vite dev / `tsc -b && vite build` |
| `cd web && pnpm lint` | Biome check |
| `make py-test` | `pytest` in `crawlerkit-py/` |
| `make up` / `down` / `ps` | dev infra (postgres/redis/minio only) |

**Production** (see `deploy/RUNBOOK.md`): `make prod-build VERSION=<tag>`, `make prod-up`, `make prod-migrate`, `make backup`/`restore`, `make load-test`. Prod stack = `docker compose -f docker-compose.yml -f docker-compose.prod.yml`.

**Bootstrap admin** (no UI): `go run ./cmd/master hash-password <pw>` → `INSERT INTO users (email, password_hash, role) VALUES (...)` via `docker compose exec postgres psql ...`.

The Go toolchain is invoked from the repo root. If your shell isn't in `crawler-lite/`, use `go -C /workspace/crawlab/crawler-lite ./...` (a bare `cd` in a compound command may be blocked by the sandbox).

## Architecture

### Composition root — `internal/app/app.go` (master) and `internal/workerapp/app.go` (worker)

No DI container. Manual constructor injection. `app.Build` is a strict 5-section constructor: (1) infra [pg pool, redis, minio], (2) `repository.New(db)` bag, (3) domain services in dependency order, (4) hub + sinks with the task↔hub cycle broken by a post-construction setter `workerHub.BindTaskService(taskSvc)`, (5) network surface [`api.NewRouter`, http server, gRPC server].

`app.Run` runs every long-lived goroutine under one `errgroup.WithContext`. A final goroutine waits on `<-ctx.Done()` for graceful shutdown (15s gRPC `GracefulStop`→`Stop`, http `Shutdown`, db/redis close). **New background goroutines belong in `app.Run`, not in service constructors.**

### Repository layer — `internal/repository/`

`Repos` is a bag struct holding one `*XRepo` per table. Each repo is a one-field `struct{ pool *pgxpool.Pool }`. Conventions:
- Column lists are package-level string consts shared by `INSERT ... RETURNING` and `SELECT`.
- A per-repo `scanOne(pgx.Row)` helper serves both `QueryRow` and `rows.Next()`, maps `pgx.ErrNoRows`→`ErrNotFound`, unmarshals nullable/JSONB columns.
- `ErrNotFound` is a per-repo sentinel, not centralized.
- `nullableInt64`/`nullableStr` coerce `0`/`""`→`NULL` at write time (the project-wide "0 means unset" convention).

### Service layer — `internal/<domain>/service.go`

**Interfaces are declared on the consumer side**, not the implementer. `spider.Service` declares `Repository`, `StorageClient`, `SourceSyncer` locally; `task.Service` declares `Notifier` and the hub's `TaskService` interface lives in `hub`. This makes every service unit-testable with inline mocks next to the service file. Constructors take interfaces + `*slog.Logger`. Defaults (e.g. `ProjectID=1`, `GitBranch="main"`) live in the service, not the repo. Sentinels: `ErrInvalidInput`, `ErrInvalidCron`, `ErrNoGitURL`, `ErrNoSource`.

### HTTP handlers — `internal/api/<domain>/`

Free functions `RegisterRoutes(g gin.IRoutes, d Deps)`; **no `*Handler` structs** unless a route owns mutable in-memory state. `Deps` (or typed args) is the only DI. Each package has its own `pathInt64(c, key)` (writes the 400 itself, returns `bool`). The `render` package (`internal/api/render/`) is the only response path: `render.JSON`, `render.Error` (envelope `{"error":{code,message}}`), `render.Decode` (400 on bad JSON). Error mapping: `errors.Is(svc.ErrInvalid*)`→400, `repo.ErrNotFound`→404, user-input failures (e.g. bad git url)→422, unknown→500 + `slog.Error`.

> The README mentions `chi`; the actual HTTP framework is **gin** (`go.mod`). Trust the code.

`internal/api/router.go`: `NewRouter` builds a `gin.Engine`, applies slog recoverer/logger/cors middleware, mounts `/healthz` (public), `/api/version` (public), the `/api` group with `authMiddleware`, registers each domain, then `r.NoRoute(gin.WrapH(web.Handler()))` — the embedded SPA fallback (`internal/web`, `//go:embed all:dist`). NoRoute only fires for unmatched routes, so `/api` 404s still return JSON; the SPA never shadows the API.

### Task lifecycle chokepoint — `internal/task/service.go`

`OnUpdate(ctx, taskID, status, errMsg, errClass, workerID)` is the **single** entry point the master uses to advance task state. Order is invariant: persist status → (if terminal) `maybeScheduleRetry` → fire `Notifier.Notify` on a **detached `context.Background()` goroutine** (the gRPC read-loop ctx may be cancelled; a slow webhook must never backpressure status persistence). `Notifier` is nil-able. Retry policy (`internal/task/retry.go`) is pure: `PolicyFromSpiderConfig` + `Decide(attempt, errClass)`; captcha is hard-excluded (operator state, not transient). `RunDispatcher` is the 5s-tick queue pump that calls `Hub.Assign`.

### Worker/gRPC hub — `internal/hub/hub.go` (master) + `internal/runner/` (worker)

`WorkerHub.Connect` is the bidi gRPC handler. First frame must be `Hello` (checked against `WORKER_SHARED_SECRET`); master mints a session, sends `Welcome`, runs `readLoop` until EOF. `readLoop` is the worker→master chokepoint: `Heartbeat`→slot counters, `TaskUpdate`→`taskSvc.OnUpdate` (the link into the task chokepoint above), `LogLine`/`Item`/`Artifact`→the respective sinks. `Assign` is first-fit over sessions by free slots.

Worker side (`runner.Worker`): connect-loop with exp backoff; `pumpOutbox` + `heartbeatLoop` goroutines + inbound `Recv` loop. `TaskExecutor.Run` per task: download+unzip source from MinIO → resolve/install per-`requirements.txt`-hash venv via `uv` (cached under `WORKER_VENV_DIR`, serialized by a per-hash mutex) → spawn `python -m crawlerkit.runner` with FD 3 as the event pipe → pump FD3-JSONL + stdout/stderr → `cmd.Wait()` → `classifyOutcome` (pure; captcha overrides even a clean exit). Every event the master sees passes through the worker's single `outbox chan`.

### Python IPC — `crawlerkit-py/`

FD 3 JSONL protocol: one JSON object per line, `{"type":"log"|"item"|"shot"|"captcha","data":{...}}`. Spider entry is `entry_module = "pkg.mod:ClassName"` (runner splits on `:`, `importlib.import_module`, `getattr`); the task workdir is prepended to `sys.path`. `Spider` base class provides `run()` (only required override), `log/item/screenshot/captcha`, and `driver()` (real Selenium Chromium or `MockDriver`; `CRAWLERKIT_DRIVER=mock|selenium` forces it). Env contract: `CRAWLERKIT_TASK_ID`, `CRAWLERKIT_SPIDER_ID`, `CRAWLERKIT_EVENT_FD`, `CRAWLERKIT_CONFIG`, `CRAWLERKIT_ARGS`. Harness: `python -m crawlerkit.runner`; exits 0 success / 1 uncaught / 2 no entry_module / 130 SIGINT.

### Frontend — `web/`

React 19 + Vite 6 + Tailwind v4 + TanStack Router/Query + Zustand. File-based routing: every file in `src/routes/` exports a `Route` via `createFileRoute("<literal>")`; `routeTree.gen.ts` is **auto-generated by the `TanStackRouterVite` plugin — never hand-edit** (regenerated on dev/build). Pathless layout routes use the `_name` convention (`_authed.tsx` guards with `beforeLoad` redirecting to `/login` when no token). Path alias `@/`→`src/`. The single fetch wrapper `api<T>()` in `src/api/client.ts` injects the bearer token from the Zustand `useAuthStore` (persisted to `localStorage` under `crawler-auth`), throws `ApiError` on non-2xx, logs out on 401. WebSocket log URL carries `?token=` because browsers can't set auth headers on WS upgrades.

### Config

`caarlos0/env/v11` struct tags. Master `internal/app/config.go`, worker `internal/workerapp/config.go`. Required secrets are `,required`. `WORKER_ID` is optional — the worker falls back to `os.Hostname()` so `docker compose up --scale worker=N` works without per-replica env.

## Conventions to follow

1. **No DI container; consumer-declared interfaces; cycle resolution via `BindX` setter** (constructor takes nil-able dep, read loops guard `if x != nil`).
2. **One chokepoint per cross-cutting concern** — `task.OnUpdate` for status+retry+notify, `pumpEvents` for Python→Go, `outbox chan` per session, `readLoop` for worker→master. A second place that "advances a task" is a bug.
3. **Commit state before side effects** — `OnUpdate` persists status, then retries, then notifies. Never reorder.
4. **Sentinel errors + `errors.Is`** mapped to HTTP codes in handlers; unknown → 500 + slog.
5. **Free functions + `Deps`** in `internal/api/*`, not handler structs.
6. Migrations are goose, forward-only (`-- +goose Up/Down`), in `db/migrations/`. A rollback crossing a migration boundary needs a manual `goose down`.
