# Plan: v2 Stateless Control Plane — Implementation

## Purpose

Implement the v2 redesign documented in `docs/DESIGN-v2.md`: move master coordination state out of process memory into Postgres/Redis so the master becomes crash-recoverable and horizontally scalable. Four pillars:

1. **Durable worker registry + running-task lease** (replaces `hub.sessions`/`hub.tasks` maps).
2. **Atomic `FOR UPDATE SKIP LOCKED` claim dispatch** (replaces poll `ListQueued` + first-fit `Assign`).
3. **N master replicas behind LB; scheduler/dispatch leader-gated by Postgres advisory lock** (replaces single master).
4. **Redis Streams for logs + reconsidered item sink** (replaces Redis pubsub + MinIO RMW; item NDJSON direct-to-MinIO).

This plan **absorbs** `plans/task-control-feature-plan.md` (its concerns are subsumed by lease + claim — see DESIGN-v2 §12) and is **orthogonal to** `plans/private-python-dependencies-plan.md`.

All file paths are relative to `crawler-lite/`. Module path is still the placeholder `github.com/yourteam/crawler-lite`.

## Guiding invariants

- **Single-replica v2 must behave like v1.** With one master, advisory lock is uncontended, claim has no SKIP LOCKED contention, the `workers` table mirrors what the in-memory map held. This is the property that keeps dev experience and risk low.
- **Don't add coordination points.** `task.OnUpdate` stays the single status-advance chokepoint; `readLoop` stays the single worker→master dispatch point; per-session `outbox` stays. Only the *backing store* of these moves.
- **Order in `OnUpdate` is sacred:** persist terminal status → maybe retry → notify. Untouched.
- **Migrations are forward-only goose** (`db/migrations/`). Each new file is `000NN_*.sql`.

## Phasing

The plan is ordered so each phase is independently shippable and revertible. Phases 1–2 are pure additions (no behavior change); phase 3 swaps dispatch; phase 4 adds HA; phase 5 swaps the log/item sinks. Each phase ends with `make test` + `make build` green and a manual smoke run.

---

## Phase 0 — Scaffolding & decision log

- [ ] Add `docs/DESIGN-v2.md` (done) and a short pointer from `CLAUDE.md` "Design rationale" block: "v2 redesign: see `docs/DESIGN-v2.md` (stateless control plane; HA + elastic master)."
- [ ] Create `internal/leader/` (empty, just doc.go) for the advisory-lock leader gate used in Phase 4.
- [ ] Add a `WORKER_*` / `MASTER_*` config TODO list in `internal/app/config.go` and `internal/workerapp/config.go` for the new knobs (lease grace, reaper interval, leader key, stream maxlen) — wired in later phases, declared now so Phase 1–5 don't re-touch config.

---

## Phase 1 — Durable worker registry (additive)

**Goal:** `workers` table exists and is written by hub on connect/heartbeat/disconnect, but dispatch still reads the in-memory map. Nothing depends on the table yet — it's a shadow write.

### Migration
- [ ] `db/migrations/00010_workers.sql`:
  ```sql
  CREATE TYPE worker_status AS ENUM ('online','draining','offline');
  CREATE TABLE workers (
    id            TEXT PRIMARY KEY,
    version       TEXT,
    concurrency   INTEGER NOT NULL,
    capabilities  JSONB NOT NULL DEFAULT '[]',
    status        worker_status NOT NULL DEFAULT 'online',
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    free_slots    INTEGER NOT NULL,
    running_tasks INTEGER NOT NULL DEFAULT 0,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  ```
  Down: drop table + type.

### Repository
- [ ] `internal/repository/workers.go`: `WorkerRepo{pool}`, following the per-repo conventions (package-level column const, `scanOne`, `ErrNotFound`). Methods:
  - `UpsertOnConnect(ctx, in) error` — `INSERT ... ON CONFLICT (id) DO UPDATE SET version=..., concurrency=..., capabilities=..., status='online', free_slots=concurrency, last_seen=now()`.
  - `Heartbeat(ctx, id string, freeSlots, running int) error` — update `last_seen`, `free_slots`, `running_tasks`.
  - `MarkOffline(ctx, id string) error`.
  - `ListOnline(ctx) ([]*Worker, error)` — `WHERE status='online'` (used in Phase 3 claim query).
- [ ] Add `Workers *WorkerRepo` to `repository.Repos` bag (`repos.go`) and `New`.

### Hub wiring (shadow write)
- [ ] `internal/hub/hub.go`: add a `WorkerRepo`-shaped interface (declared in `hub`, consumer-side) to `WorkerHub` deps, set via constructor or a `BindWorkerRepo` setter (consistent with existing `BindTaskService`). In `Connect`:
  - After successful Hello/Welcome → `workerRepo.UpsertOnConnect(...)` (best-effort; log on error, don't fail the stream).
  - In `readLoop` `Heartbeat` case → `workerRepo.Heartbeat(...)` (best-effort).
  - In `unregister` → `workerRepo.MarkOffline(...)`.
- [ ] `internal/app/app.go`: construct `repos.Workers`, pass into hub. Keep `sessions` map **for now** — dispatch in Phase 3 will stop reading it.

### Tests
- [ ] `internal/repository/workers_test.go`: upsert/heartbeat/offline round-trip against a real Postgres (project already uses integration-style repo tests — match the existing pattern in `tasks_test.go` if present, else `pgx`+testdb).
- [ ] Extend `hub_test.go`: assert a connect/heartbeat/disconnect produces the expected `workers` rows (use a fake `WorkerRepo` recorder).

### Done when
`workers` table tracks reality, dispatch still works off the in-memory map, `make test` + `make build` green, manual `make up && make run-master && make run-worker` shows a `workers` row appearing and going offline.

---

## Phase 2 — Running-task lease (additive columns)

**Goal:** `tasks` gains `lease_expires_at` and `assign_sent`; `OnUpdate` clears the lease on terminal; nothing reads the lease yet.

### Migration
- [ ] `db/migrations/00011_task_lease.sql`:
  ```sql
  ALTER TABLE tasks
    ADD COLUMN lease_expires_at TIMESTAMPTZ,
    ADD COLUMN assign_sent      BOOLEAN NOT NULL DEFAULT false;
  CREATE INDEX idx_tasks_running_lease
    ON tasks (lease_expires_at) WHERE status = 'running';
  ```
  Down drops both columns + index.

### Repository
- [ ] `internal/repository/tasks.go`: add methods, keeping existing `scanOne`/column-const conventions:
  - `Claim(ctx, taskID int64, workerID string, leaseExpires time.Time) error` — `UPDATE tasks SET status='running', worker_id=$w, lease_expires_at=$t, started_at=coalesce(started_at, now()), assign_sent=false WHERE id=$taskID AND status='queued'`. Returns rows-affected so the caller detects a lost race. **This is the atomic primitive; Phase 3 uses it.**
  - `ClearLease(ctx, taskID int64) error` — set `lease_expires_at=NULL` (called on terminal status).
  - `MarkAssignSent(ctx, taskID int64) error`.
  - `ListOrphans(ctx, before time.Time) ([]*Task, error)` — `status='running' AND lease_expires_at < $before` (Phase 4 reaper).
  - `Reclaim(ctx, taskID int64) error` — `UPDATE ... SET status='queued', worker_id=NULL, lease_expires_at=NULL, assign_sent=false WHERE status='running' AND lease_expires_at < now() AND id=$taskID` (atomic self-guard).
- [ ] Update `Task` struct + `scanOne` to read the two new columns; `SetStatus` unchanged for non-terminal, but wire `OnUpdate` to call `ClearLease` on terminal (see below).

### Task service
- [ ] `internal/task/service.go` `OnUpdate`: after `SetStatus` succeeds, if `isTerminalStatus(status)` → `s.deps.Repo.ClearLease(ctx, taskID)` (best-effort, logged; must not roll back the status update — same error-discipline as `maybeScheduleRetry`).
- [ ] Add the new repo methods to the `Repository` interface declared in `task/service.go`.

### Tests
- [ ] `repository/tasks_test.go`: `Claim` returns rows-affected 0 when a second claim races; `ClearLease` nulls the column; `Reclaim` only touches expired rows.
- [ ] `task` service test: terminal `OnUpdate` clears the lease.

### Done when
Lease columns exist and are maintained, but dispatch still uses first-fit + in-memory map. `make test` + `make build` green.

---

## Phase 3 — Atomic claim dispatch (the behavior switch)

**Goal:** `dispatchOnce` no longer reads `hub.sessions`; it runs a SKIP LOCKED claim batch against `tasks`+`workers`, then pushes `AssignTask` to whichever worker's outbox is on this replica. This is the phase that makes multi-dispatch safe and turns first-fit into least-loaded + capability-aware.

### Scheduler/claim
- [ ] `internal/task/service.go` `dispatchOnce`: rewrite to:
  1. `Repo.ClaimBatch(ctx, ClaimBatchOpts{Max, RequiredCaps})` — a single query: `SELECT ... FROM tasks WHERE status='queued' AND (not_before IS NULL OR not_before<=now()) ORDER BY queued_at LIMIT $Max FOR UPDATE SKIP LOCKED`. **Run inside a `pgx.Tx`** so the row locks span the worker selection too.
  2. For each claimed task, within the same tx, pick a worker: `SELECT id, free_slots FROM workers WHERE status='online' AND free_slots>0 AND $caps <@ capabilities ORDER BY free_slots DESC LIMIT 1 FOR UPDATE OF workers SKIP LOCKED` — lock the worker row so its `free_slots` decrement is atomic across replicas.
  3. `UPDATE tasks SET worker_id=$w, lease_expires_at=now()+timeout+grace, assign_sent=false WHERE id=$t` and `UPDATE workers SET free_slots=free_slots-1 WHERE id=$w`. Commit.
  4. Outside the tx, for each (task, worker) claimed on this replica: `buildAssign`, then if `hub.HasHandle(workerID)` → `hub.PushAssign(workerID, assign)` + `MarkAssignSent`; else leave `assign_sent=false` for another replica's补发 (Phase 3 can still use the 5s tick for补发; cross-replica Pub/Sub is Phase 4).
- [ ] Add `ClaimBatch` + worker-selection to `TaskRepo` (one method that does steps 1–3 in a tx, returns `[]ClaimedTask{TaskID, SpiderID, SpiderVersion, WorkerID, TimeoutS}`). Keeps the tx inside the repo layer (matches the project's "repos own SQL" convention).
- [ ] `buildAssign` is unchanged; it already produces `*pb.AssignTask`. `ProxyUrl` stays `""` (private-deps plan / future proxy work).

### Hub changes
- [ ] `internal/hub/hub.go`:
  - `Assign(ctx, *pb.AssignTask)` → **delete** (or deprecate to a thin wrapper that calls the new `PushAssign`). The dispatch loop now owns the claim; the hub only transports.
  - Add `HasHandle(workerID string) bool` and `PushAssign(ctx, workerID string, a *pb.AssignTask) error` — look up session by `WorkerID` (need a `workerID→*Session` index alongside the existing `sessionID` map), push to its outbox. Return an error/sentinel if no handle here.
  - `tasks` map (`taskMeta`) → **delete**. `CancelRunning` reads `tasks.worker_id` from the repo (add `Repo.Get` already exists) then `PushCancel(workerID, taskID)`. Add `PushCancel` mirroring `PushAssign`.
  - `releaseTask` no longer touches a global `tasks` map; it just adjusts the session's local `running` set + `FreeSlots` (the authoritative `free_slots` is the DB column, bumped by the claim/reaper; the session-local counter is now only for *this replica's* outbound pressure, kept best-effort).
- [ ] Update `task.Hub` interface in `service.go` to the new `PushAssign`/`PushCancel`/`HasHandle` shape; update `app.go` wiring.

### Reaper (light, leader-gated in Phase 4)
- [ ] `internal/task/service.go`: add `RunReaper(ctx)` goroutine (started in `app.Run`): every N seconds, `ListOrphans(now())` → for each, `Reclaim` (atomic) → on success, bump `workers.free_slots` for the old `worker_id` and `s.notify()` wakeup. In Phase 3 it runs on every replica (harmless — `Reclaim` is atomic). Phase 4 restricts it to leader.

### Worker `free_slots` reconciliation
- [ ] The `Heartbeat` case still writes `workers.free_slots` from the worker's self-report. Add a guard in `WorkerRepo.Heartbeat`: never increase `free_slots` above `concurrency - running_tasks` and never let it go negative. This is the calibration path; the claim is the authoritative decrement.

### Tests
- [ ] `task` service: two concurrent `dispatchOnce` against the same queued set + 2 workers → each task assigned exactly once, no double-claim (use a real Postgres tx in the test).
- [ ] Capability filter: a task requiring `chromium` skips a worker without it.
- [ ] `hub` `PushAssign` to a missing handle returns the not-here sentinel, dispatch leaves `assign_sent=false`.

### Done when
Dispatch is DB-authoritative, first-fit is gone, slot accounting comes from the claim. Single-replica smoke: queue 3 tasks on 2 workers, observe least-loaded distribution and no orphans. `make test` + `make build` green.

**Note:** after Phase 3, `hub.sessions` is still used as the *outbox index* (which worker's gRPC handle is here) — that's its correct v2 role. The `tasks` map is gone.

---

## Phase 4 — HA master + leader gate

**Goal:** run N master replicas; only the leader runs `schedule.Runner` and the reaper; wakeup is cross-replica via Redis.

### Leader gate
- [ ] `internal/leader/leader.go`: `Gate` struct holding a `pgxpool.Pool`, a key constant, and a renewal interval. `Run(ctx)`:
  - loop: `pg_try_advisory_lock($KEY)`. On success → mark held, renew on a ticker (`pg_advisory_lock` is session-scoped; keep the conn alive via a dedicated `pgx.Conn`, not the pool — advisory locks are per-connection). On failure → sleep and retry.
  - expose `Held() bool` (cheap, atomic bool) and a `<-chan struct{}` that fires when leadership is acquired/lost.
  - On `ctx.Done()` → `pg_advisory_unlock` + close conn.
  - Use a **dedicated `pgx.Conn`** for the lock (pool connections are recycled and the lock would silently transfer/release). Document this.
- [ ] `app.Run`: start `leader.Run(ctx)`; gate `scheduleRunner.Run` and `task.RunReaper` behind `if leader.Held()` (restart them on acquire, stop on loss — use the acquire/loss channel). `RunDispatcher` runs on **all** replicas (claim is SKIP LOCKED safe), so it is **not** gated.
- [ ] Fencing (现阶段简化 — explicit in DESIGN-v2 §6): no fenced epoch token yet. Document the assumption: lease writes in Phase 2/3 use `WHERE ... AND lease_expires_at < now()` self-guards, so a stale leader's reaper writes are no-ops against tasks whose lease was already renewed by a fresh leader. Add a TODO for epoch fencing.

### Cross-replica wakeup
- [ ] `internal/cache/` (Redis wrapper): add `PublishWakeup(ctx)` and a `SubscribeWakeup(ctx) <-chan struct{}`. `task.Queue` → after `Repo.Create`, call both the in-process `s.notify()` **and** `cache.PublishWakeup` (best-effort, logged).
- [ ] `RunDispatcher`: select on in-process `wakeup`, Redis wakeup channel, **and** the 5s tick. Dedupe (all three just trigger `dispatchOnce`).

### Schedule runner under leader
- [ ] `schedule.Runner` currently starts its cron unconditionally in `Run`. Keep `Run` but only call it from `app.Run` when leader is held. On leadership loss, stop the runner (`c.Stop()`); on re-acquire, `Reload`+`Start`. Because `Reload` is already safe to call repeatedly, this is mostly orchestration in `app.Run`.
- [ ] Scheduler overlap control (from the in-progress task-control plan) is schedule-layer policy — implement here if in scope, else leave a TODO. It is orthogonal to v2.

### Config & deploy
- [ ] `internal/app/config.go`: add `LeaderKey int64` (default constant), `LeaseGraceSeconds` (default 60), `ReaperIntervalSeconds` (default 10), `LogStreamMaxLen` (default 10000, used Phase 5).
- [ ] `docker-compose.prod.yml`: `master` service → `deploy.replicas: 2` (or document `--scale master=N`) + an LB note. Worker `WORKER_MASTER_ADDR` → the LB gRPC endpoint instead of a single master host.
- [ ] `deploy/RUNBOOK.md`: add "multi-master" section — LB must support gRPC long-lived streams (nginx `grpc_pass` with keepalive, or Envoy); leader failover window ~seconds; no special handling needed for worker reconnect (they just hit the LB).

### Tests
- [ ] `leader` package: acquire/release/loss cycle with a real Postgres conn; a second `Gate` with the same key blocks until the first releases.
- [ ] `task` service: wakeup via Redis channel triggers `dispatchOnce` on a second replica (simulate with two `Service` instances sharing one Redis).
- [ ] Integration: two `app.Build` instances against one Postgres/Redis, one worker — kill the leader master, observe the other takes scheduling within the renewal window.

### Done when
`--scale master=2` works; killing one master leaves scheduling/dispatch functional; no double-scheduled cron. `make test` + `make build` green.

---

## Phase 5 — Redis Streams logs + item sink reconsideration

**Goal:** replace `LogSinkPubsub` (pubsub + MinIO RMW) with `LogSinkStream` (Redis Streams); switch items to NDJSON direct-to-MinIO + index row.

### Log sink
- [ ] `internal/hub/sinks.go`: add `LogSinkStream` implementing `LogSink`:
  - `Write(ctx, *pb.LogLine)`: `XADD logs:{task_id} MAXLEN ~ {LogStreamMaxLen} * level ... ts_ns ... message ...`. No memory buffering, no flush goroutine.
  - On terminal `OnUpdate` (hook from `task.Service` via a new `LogFinalizer` interface, or the existing `ArtifactsLogIndex` path): dump the whole stream to MinIO `tasks/{id}/log.jsonl` via `XRANGE` → NDJSON write, then `XTRIM`/del the stream, then `repo.UpsertLogIndex` with final counts/bytes/storage_key.
- [ ] `app.go`: swap `hub.NewLogSink` → `hub.NewLogSinkStream`; the `logSink.Run` flush goroutine in `app.Run` is removed (stream has no flush). Keep a `FinalizeLogs` call wired into `OnUpdate`'s terminal branch (best-effort, detached like notify).
- [ ] `internal/api/` WebSocket log handler: change from "SUBSCRIBE Redis channel + replay MinIO" to "`XRANGE logs:{id} - +` (history) then `XREAD BLOCK` (live tail) on the same stream." Reconnect resumes from the last received entry ID.

### Item sink
- [ ] Decision per DESIGN-v2 §8: **path 1 (NDJSON + index)** by default.
- [ ] Worker side (`internal/runner/executor.go`): instead of streaming `ItemEmitted` frames to master, the worker (or the Python SDK) appends each item to `items/{task_id}.ndjson` in MinIO via a presigned PUT-once/append strategy. **现阶段简化:** MinIO lacks true append; either (a) buffer items in the worker and PUT/overwrite the NDJSON object periodically (RMW-free, just overwrite with the full-so-far buffer), or (b) write per-batch numbered objects `items/{task_id}/batch-NN.ndjson` and concatenate at finalize. Pick (b) to avoid unbounded worker memory. The master gets a single `ItemBatch` frame (new proto message) per batch with `{task_id, batch_index, count, storage_key, bytes}` — **no item bytes through master**.
- [ ] `proto/worker/v1/worker.proto`: add `ItemBatch` to the `WorkerMsg` oneof; keep `ItemEmitted` for backward-compat / small-item mode during transition. Regenerate with `make gen`.
- [ ] `internal/hub/sinks.go` `ItemSink`: new `ItemSinkObject` writes an `items` index row per batch (`items(task_id, spider_id, batch_index, count, bytes, storage_key)`). The `items` table schema needs a migration (`00012_items_object.sql`) adding `batch_index`, `storage_key`, `bytes`, and relaxing `payload` to nullable (or a new `item_batches` table — cleaner; recommend a new table to keep the old per-row path optional).
- [ ] Python SDK (`crawlerkit-py/`): the `item()` method writes to the NDJSON batch file (or sends `ItemEmitted` if `CRAWLERKIT_ITEM_MODE=frame` for compat). Default to object mode.

### Tests
- [ ] `LogSinkStream`: `XADD` produces entries; `XRANGE`+`XREAD` replay a stream without gaps; finalize writes MinIO + index and trims the stream.
- [ ] `ItemSinkObject`: index row written per batch; bytes never transit the master (assert via a byte-counting fake store).
- [ ] WebSocket handler: reconnect mid-stream resumes from the right entry ID.

### Done when
Logs are single-system (Streams) with native append + correct replay; items don't transit the master process. `make test`, `make build`, `make py-test` green. Manual: run a spider that emits 1000 items + 5000 log lines, confirm master process memory stays flat and log replay after reconnect is gap-free.

---

## Cross-cutting

### Config knobs (declared Phase 0, wired per-phase)
`LeaderKey`, `LeaseGraceSeconds`, `ReaperIntervalSeconds`, `LogStreamMaxLen`, `ItemMode` (`object`|`frame`), `WorkersReaperStaleSeconds`. All with sensible defaults so single-replica dev needs no new env.

### Production hardening (carry from v1, orthogonal)
CORS tighten, bcrypt cost, module path rename `yourteam`→real, SSH git auth. Not blocked by v2; track separately. The module rename touches every import — do it as its own PR, not folded into a v2 phase.

### Observability
- [ ] Add slog structured fields: `replica_id`, `leader=bool`, `claim_batch_size`, `orphan_reclaimed`. Helps reason about multi-master behavior in prod.
- [ ] Prometheus metrics (if the project has a metrics surface — check `internal/` for an existing `prometheus` import before adding): `tasks_claimed_total`, `tasks_orphaned_total`, `leader_held`, `log_stream_len`.

### Rollout / migration
- Forward-only migrations 00010–00012 are additive (new table/columns), so they can be applied to a running v1 master with no downtime.
- Phase 3 (dispatch switch) is the one behavioral cutover — deploy all replicas together or feature-flag `DispatchMode=claim|legacy` temporarily (recommend a flag with legacy default, flip after soak).
- Phase 4 HA can roll out one extra replica at a time.
- Phase 5 log sink: keep `LogSinkPubsub` behind the same flag until the WebSocket handler is migrated, then remove.

### Test strategy per phase
Every phase ends with: `make gen` (if proto changed) → `make test` → `make build` → `cd web && pnpm lint && pnpm build` (if API surface changed) → `make py-test` (if SDK changed) → manual smoke (`make up && run-master && run-worker && web-dev`). Use the `verify` skill before committing nontrivial phases.

## Out of scope (explicit)
- Distributed queue broker (Temporal/NATS/Kafka) — that's Framing B, rejected.
- Event-sourced task log — Framing 3, rejected.
- Capability-aware scheduling *beyond* the `<@` filter (e.g. affinity, bin-packing) — the claim query leaves room; not in this plan.
- Proxy assignment (`ProxyUrl` wiring) — separate future work, noted in DESIGN-v1 §4.4.
- Captcha human queue — separate future work, noted in DESIGN-v1 §4.4.
- Module path rename — separate PR.

## Sequencing summary
Phase 1 (registry) → Phase 2 (lease) → Phase 3 (claim dispatch, the real switch) → Phase 4 (HA) → Phase 5 (streams + items). Phases 1–2 are safe additive prep; 3 is the riskiest and highest-value; 4 and 5 are independent of each other and can be reordered (recommend 5 before 4 if log volume is the more pressing pain than master HA).
