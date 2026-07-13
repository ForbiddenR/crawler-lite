# Plan: Master-Controlled Task Lifecycle

## Purpose

Add a task-control feature that makes the master authoritative over task assignment, cancellation, timeout, worker disconnect recovery, worker capacity, and scheduled-task overlap behavior.

Today the master can queue, dispatch, and cancel tasks, but worker execution is only loosely controlled. Worker updates can overwrite terminal master decisions, running tasks can be orphaned when a worker disconnects, and scheduled tasks can overlap without policy control.

This plan keeps the existing master/worker split, but makes task state transitions explicit and guarded.

## Goals

1. Prevent stale worker updates from overwriting master-owned terminal states.
2. Let the master recover tasks when a worker disconnects or stops reporting.
3. Make cancellation authoritative from the master side.
4. Make timeout enforceable even if a worker dies before reporting timeout.
5. Fix slot accounting so heartbeat timing cannot over-assign tasks.
6. Add scheduler overlap control so a schedule can choose whether to allow, skip, queue, or cancel overlapping runs.
7. Preserve current behavior by default where practical.

## Non-goals

- Do not introduce a distributed queue system in this iteration.
- Do not move task execution into the master.
- Do not implement capability-aware worker selection yet, except keeping room for it.
- Do not redesign the Python `crawlerkit` event protocol beyond adding task ownership metadata where needed.

---

## Current behavior summary

### Dispatch

- `task.Service.RunDispatcher` scans queued tasks.
- `hub.WorkerHub.Assign` picks the first worker with `FreeSlots > 0`.
- The worker starts the task and reports `ACCEPTED`, `RUNNING`, then a terminal state.

### Cancellation

- `task.Service.Cancel` immediately writes `cancelled` to the DB.
- The master sends `CancelTask` if the task is known in the in-memory hub map.
- A late worker update can still overwrite the cancelled status because `SetStatus` is unconditional.

### Timeout

- The master sends `timeout_s` in `AssignTask`.
- The worker enforces timeout with `context.WithTimeout`.
- If the worker dies before reporting timeout, the master has no independent watchdog.

### Scheduler

- Every cron fire queues a new task.
- It does not check whether the previous scheduled task is still queued or running.
- Overlap is effectively allowed, limited only by worker slots.

---

## Proposed design

## 1. Make task assignment explicit

Add assignment ownership so every worker update can be validated by the master.

### Data model changes

Add fields to `tasks`:

```sql
assignment_id UUID,
assigned_at TIMESTAMPTZ,
accepted_at TIMESTAMPTZ,
lease_expires_at TIMESTAMPTZ,
deadline_at TIMESTAMPTZ,
worker_session_id TEXT,
cancel_requested_at TIMESTAMPTZ,
schedule_id BIGINT REFERENCES schedules(id) ON DELETE SET NULL
```

Add a task status:

```text
assigned
```

Resulting lifecycle:

```text
queued
  -> assigned
  -> running
  -> succeeded | failed | timeout | cancelled | captcha_blocked
```

`assigned` means the master has reserved a worker slot and sent, or is about to send, `AssignTask`, but the worker has not yet confirmed `RUNNING`.

### Repository methods

Replace broad `SetStatus` use in dispatch/update paths with guarded methods:

```go
TryAssignQueued(ctx, taskID, workerID, sessionID, assignmentID, deadlineAt, leaseExpiresAt) (bool, error)
ApplyWorkerUpdate(ctx, input WorkerUpdateInput) (applied bool, current *Task, error)
CancelTask(ctx, taskID int64) (wasRunning bool, assignmentID uuid.UUID, error)
MarkWorkerLost(ctx, sessionID string) ([]LostTask, error)
ExpireTimedOutTasks(ctx, now time.Time) ([]ExpiredTask, error)
ReleaseStaleAssigned(ctx, now time.Time) ([]TaskID, error)
```

Important transition rules:

- Only `queued` can become `assigned`.
- Only matching `assignment_id` can update `assigned` or `running` tasks.
- Terminal statuses cannot be overwritten by worker updates.
- `cancelled` is terminal.
- Worker updates for unknown/stale assignments are ignored and logged.

---

## 2. Extend the gRPC task protocol

Add assignment identity to master/worker messages.

### `AssignTask`

Add:

```proto
string assignment_id = 9;
string worker_session_id = 10;
int64 deadline_unix_ns = 11;
```

### `CancelTask`

Add:

```proto
string assignment_id = 2;
```

The worker should cancel only if the assignment ID matches its local running task.

### `TaskUpdate`

Add:

```proto
string assignment_id = 5;
```

The master accepts worker updates only when `assignment_id` matches the active DB row.

---

## 3. Make master dispatch authoritative

Change dispatch flow from:

```text
hub.Assign() reserves in memory -> worker reports RUNNING -> DB changes
```

to:

```text
DB queued -> assigned with assignment_id
  -> hub sends AssignTask
  -> worker reports RUNNING with assignment_id
  -> DB assigned -> running
```

### New dispatch flow

1. Dispatcher lists ready `queued` tasks.
2. Hub selects a worker using master-owned capacity.
3. Service generates `assignment_id` and `deadline_at`.
4. Repository atomically updates `queued -> assigned`.
5. Hub sends `AssignTask` with `assignment_id`.
6. If send fails, service reverts `assigned -> queued` if still assigned to that assignment.
7. Worker accepts and sends `RUNNING` with `assignment_id`.
8. Master applies `assigned -> running` only if assignment matches.

This prevents duplicate assignment and stale update races.

---

## 4. Fix slot accounting

Today heartbeat can overwrite master slot reservation. Instead:

- Master-owned worker capacity should be calculated from session state:

```go
freeSlots = concurrency - len(session.running)
```

- Heartbeat `free_slots` and `running_tasks` should be treated as telemetry, not authority.
- `hub.Assign` should reserve by adding `assignment_id/task_id` to `session.running`.
- `releaseTask` should remove the task from `session.running`.
- Worker heartbeat mismatches should be logged as warnings.

Worker-side guard:

- Before starting a task, the worker should check local capacity with an atomic compare-and-swap or a mutex.
- If the worker is full, it should reject the assignment with a non-terminal rejection message, or send a structured update that the master maps back to `queued`.

Suggested protocol addition:

```proto
TASK_STATE_REJECTED = 8;
```

Master behavior for `REJECTED`:

```text
assigned -> queued
release reserved slot
log reason
```

---

## 5. Make cancellation final

### API behavior

`POST /api/tasks/:id/cancel` should:

1. Mark the task cancelled only if it is not terminal.
2. Record `cancel_requested_at`.
3. Send `CancelTask` with `assignment_id` if the task is assigned/running.
4. Release the hub slot immediately or after worker acknowledgement, depending on chosen policy.

Recommended policy for v1:

- Mark DB status `cancelled` immediately.
- Send cancel to worker best-effort.
- Ignore any later worker update for that assignment.
- Release master slot immediately so the system does not get stuck behind a worker that ignores cancellation.
- If the worker later sends terminal status, log and ignore it.

This makes cancellation authoritative and simple.

---

## 6. Add a master watchdog

Add a long-lived goroutine in `internal/app/app.go`, following the existing rule that background goroutines belong in `app.Run`.

Example:

```go
g.Go(func() error { return a.task.RunWatchdog(ctx) })
```

### Watchdog responsibilities

Every few seconds:

1. Find `assigned` tasks whose `lease_expires_at` passed before the worker accepted them.
   - Requeue them.
   - Release the hub reservation if still present.

2. Find `running` tasks whose `deadline_at` passed.
   - Mark them `timeout` from the master side.
   - Send `CancelTask` best-effort.
   - Release slot.
   - Trigger retry/notification through the same terminal-state path.

3. Find tasks assigned to disconnected sessions.
   - For `assigned`: requeue.
   - For `running`: mark `failed` with `error_class = worker_lost`, then let retry policy decide.

### Important implementation detail

Terminal handling should not be duplicated. Refactor `OnUpdate` so both worker updates and master watchdog terminal updates share the same path:

```go
completeTask(ctx, taskID, status, errMsg, errClass, workerID)
```

That function should preserve the existing order:

```text
persist terminal status -> maybe schedule retry -> notify
```

---

## 7. Add scheduled-task overlap control

Add a schedule-level overlap policy.

### Data model

Add to `schedules`:

```sql
overlap_policy TEXT NOT NULL DEFAULT 'allow'
```

Allowed values:

```text
allow
skip_if_active
queue_once
cancel_previous
```

Add `schedule_id` to tasks so active tasks can be tied back to the schedule that created them.

### Policy behavior

#### `allow`

Current behavior.

Every cron fire creates a new task.

#### `skip_if_active`

If the schedule already has an active task, skip this fire.

Active means:

```text
queued, assigned, running
```

No backlog is created.

#### `queue_once`

If there is already an active task for this schedule, do not create another queued duplicate.

This gives at most one pending/running scheduled task per schedule.

#### `cancel_previous`

If there is an active task for this schedule:

1. Cancel the previous active task.
2. Queue a new task.

This is useful for frequent schedules where only the latest run matters.

### Runner changes

In `schedule.Runner.fire`:

1. Load schedule.
2. Check `overlap_policy`.
3. Query active tasks for `schedule_id`.
4. Apply policy.
5. Queue task with `ScheduleID` set in `task.CreateInput`.
6. Mark run only when a new task is actually queued.
7. Optionally record skipped fires in logs.

### API/UI changes

- Include `overlap_policy` in schedule create/update/list responses.
- Add a select control in the schedule form.
- Show skipped behavior in help text:

```text
Allow overlap: every cron fire creates a task.
Skip if active: do not start a new run while one is queued or running.
Queue once: keep at most one active run for this schedule.
Cancel previous: stop the old run and start a new one.
```

---

## 8. Implementation phases

## Phase 1: Guard state transitions

Files:

- `db/migrations/`
- `internal/repository/tasks.go`
- `internal/task/service.go`
- `internal/hub/hub.go`

Tasks:

1. Add task assignment fields and `assigned` status.
2. Add guarded repository methods.
3. Refactor `OnUpdate` to reject terminal overwrites.
4. Ensure cancellation cannot be overwritten by late worker updates.
5. Add unit tests for state transitions.

Acceptance criteria:

- A cancelled task remains cancelled even if worker later sends `succeeded` or `failed`.
- A terminal task cannot move back to `running`.
- Worker update with wrong assignment ID is ignored.

## Phase 2: Assignment ownership in gRPC

Files:

- `proto/worker/v1/worker.proto`
- `internal/pb/worker/v1/`
- `internal/task/service.go`
- `internal/hub/hub.go`
- `internal/runner/worker.go`

Tasks:

1. Add `assignment_id` to `AssignTask`, `CancelTask`, and `TaskUpdate`.
2. Regenerate protobuf stubs with `make gen`.
3. Make dispatcher generate and persist assignment ID before sending.
4. Make worker store assignment ID per running task.
5. Make all worker updates include assignment ID.

Acceptance criteria:

- A stale worker cannot mutate a task after it has been reassigned.
- Master logs stale updates without changing DB state.

## Phase 3: Slot accounting and worker capacity rejection

Files:

- `internal/hub/hub.go`
- `internal/runner/worker.go`
- `internal/api/workers/workers.go`
- `web/src/routes/_authed.dashboard.tsx`

Tasks:

1. Calculate master free slots from `concurrency - len(running)`.
2. Treat heartbeat slots as telemetry only.
3. Add worker-side capacity guard.
4. Optionally add `TASK_STATE_REJECTED` and requeue rejected tasks.
5. Update worker API response if needed to show authoritative vs reported slots.

Acceptance criteria:

- Heartbeat timing cannot cause over-assignment.
- Worker cannot run more tasks than `WORKER_CONCURRENCY`.

## Phase 4: Master watchdog

Files:

- `internal/task/service.go`
- `internal/repository/tasks.go`
- `internal/app/app.go`
- `internal/hub/hub.go`

Tasks:

1. Add `RunWatchdog(ctx)` to task service.
2. Expire stale `assigned` tasks back to `queued`.
3. Mark overdue `running` tasks as `timeout`.
4. Handle worker disconnect by requeueing assigned tasks and failing/retrying running tasks.
5. Ensure retry and notification behavior is shared with worker terminal updates.

Acceptance criteria:

- A task does not stay `running` forever if the worker dies.
- A task times out even if the worker never reports timeout.
- Worker disconnect is visible through task error state or retry child task.

## Phase 5: Scheduler overlap policy

Files:

- `db/migrations/`
- `internal/schedule/service.go`
- `internal/schedule/runner.go`
- `internal/repository/schedules.go`
- `internal/repository/tasks.go`
- `internal/api/schedules/schedules.go`
- `web/src/routes/_authed.schedules.tsx`
- `web/src/api/resources.ts`

Tasks:

1. Add `overlap_policy` to schedules.
2. Add `schedule_id` to tasks and task create input.
3. Add repository method to list/count active tasks by schedule.
4. Implement policy handling in `Runner.fire`.
5. Expose policy in API and frontend.
6. Add tests for each policy.

Acceptance criteria:

- `allow` preserves current behavior.
- `skip_if_active` skips new fires while a previous scheduled task is active.
- `queue_once` prevents schedule backlog growth beyond one active task.
- `cancel_previous` cancels existing active task before queuing the new one.

---

## 9. Testing plan

### Unit tests

Add/extend tests for:

- `task.Service.Cancel` finality.
- Worker update ignored after cancellation.
- Wrong `assignment_id` ignored.
- `queued -> assigned -> running -> terminal` happy path.
- Terminal status cannot be overwritten.
- Timeout watchdog marks stale running task as timeout.
- Worker disconnect recovery.
- Scheduler overlap policies.

### Integration tests

Add a master/worker integration test that exercises:

1. Queue task.
2. Assign to worker.
3. Cancel before completion.
4. Worker sends late success.
5. DB remains `cancelled`.

Add another test:

1. Queue task.
2. Assign to worker.
3. Simulate worker disconnect.
4. Verify task is requeued or failed/retried according to policy.

### Manual verification

Use local stack:

```sh
make up
make migrate
make run-master
make run-worker
make web-dev
```

Manual checks:

- Start a long-running spider and cancel it from UI.
- Kill the worker during a running task.
- Run a schedule every minute with a task that sleeps for several minutes.
- Verify each overlap policy behaves as documented.

---

## 10. Rollout strategy

1. Keep default schedule policy as `allow` to avoid changing existing behavior unexpectedly.
2. Use migrations with nullable new task fields where possible.
3. Deploy master before workers only if protobuf changes remain backward-compatible.
4. If protobuf changes are not backward-compatible, deploy workers and master together.
5. Add logs for ignored stale updates before making them hard errors.
6. Watch for tasks in `assigned` or `running` longer than expected after rollout.

---

## 11. Open decisions

1. Should worker-lost running tasks be requeued directly, or marked failed and retried through the existing retry policy?

   Recommended: mark `failed` with `error_class = worker_lost` and let retry policy decide. This avoids silently duplicating non-idempotent spider work.

2. Should cancelled tasks release slots immediately or wait for worker acknowledgement?

   Recommended: release immediately in the master and ignore stale worker updates. This avoids stuck capacity when workers are unhealthy.

3. Should `assigned` be a new DB enum value or represented as `queued` plus assignment fields?

   Recommended: add explicit `assigned`. It makes lifecycle, UI, and watchdog behavior clearer.

4. Should schedule overlap consider only `running`, or also `queued` and `assigned`?

   Recommended: consider `queued`, `assigned`, and `running` as active. Otherwise frequent schedules can still build a backlog.

---

## 12. Expected result

After this feature, task ownership will be master-authoritative:

```text
master assigns with assignment_id
worker can only update matching assignment
master cancellation is final
master timeout/watchdog can recover stuck tasks
slots are reserved and released by the master
scheduler overlap is configurable per schedule
```

This directly addresses the current gap where the master dispatches tasks but cannot reliably control their lifecycle once workers begin execution.
