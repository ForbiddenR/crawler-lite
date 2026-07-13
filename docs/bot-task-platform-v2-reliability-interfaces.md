# Bot 平台 v2 × crawler-lite parts/01–02：可靠性接口草图

> 配套阅读：
>
> - `docs/bot-task-platform-v2-design.md`（产品规格，实现以 v2 为准）
> - `docs/parts/01-task-state-machine.md`（状态机 / lease / reclaim / 单一入口）
> - `docs/parts/02-dispatch-worker-selection.md`（claim / least-loaded / slot / hub 降级）
> - `bot-task-platform-v2-protocol.md`（Worker Protocol 定稿；ID、proto、消息字段、幂等、文件传输以该文档为准）
> - 现状代码：`internal/task/service.go`、`internal/hub/hub.go`、`proto/worker/v1/worker.proto`
>
> 本文只定 **可靠性重叠区的接口与对齐决策**，不展开 Bot/TaskItem 业务 API、UI、Schedule 全量细节；协议字段细节以 `bot-task-platform-v2-protocol.md` 为准。
> 目标：若基于现有 crawler-lite 演进，或按 Bot 平台重做控制面，两边不再各说各话。

---

## 0. 结论先行

| 主题 | 对齐结果 |
|---|---|
| 单一状态推进入口 | **采纳 parts/01**：`Transition`/`OnUpdate` 唯一入口，顺序铁律不变 |
| 显式转移表 + 终态守卫 | **采纳 parts/01** |
| lease + reaper + reclaim | **采纳 parts/01**，语义映射到 Bot 状态名 |
| reclaim 次数封顶 | **采纳 parts/01 Q2**：独立 `reclaim_count`，默认 3 |
| 时间常量 | **采纳 parts/01 Q3**：心跳 5s / grace 60s / reaper 10s |
| claim 事务 + least-loaded + caps | **采纳 parts/02** |
| hub 降级为传输层 | **采纳 parts/02**：`HasHandle` / `PushAssign` / `PushCancel` |
| slot 双释放防护 | **采纳 parts/02** |
| 状态枚举命名 | **Bot 平台对外名**；内部可兼容旧名（见 §1） |
| `dispatching` | **Bot 平台新增**；parts 的 `queued→running` claim 拆成两段（见 §2） |
| captcha | **Bot 平台 v2 MVP 不做**；接口预留，不实现转移 |
| Python 桥 | **Bot 平台用 Runtime HTTP + bot_sdk**；不等价于 FD3 `crawlerkit` |
| finalize | **Bot 平台新增纯函数**；parts 的 succeeded/failed 由 worker 直接报，Bot 侧常由 Master 汇总 item 后判定 |

一句话：

```text
可靠性骨架 = parts/01 + parts/02
产品状态与执行模型 = bot-task-platform-v2
重叠区以本文接口为准接线
```

---

## 1. 概念与状态映射

### 1.1 对象映射

| crawler-lite（现状/parts） | Bot 平台 v2 | 备注 |
|---|---|---|
| Spider | Bot | 定义单元 |
| spider source version | BotVersion + package | 交付主路径 upload |
| Task | Task | 一次执行 |
| ItemEmitted | Result（为主）/ 可选 TaskItem | 现有 item ≈ 结构化结果 |
| `task.OnUpdate` | `task.Service.Transition` | 可保留方法名 `OnUpdate` |
| `hub.Assign`（现状） | 删除 → claim + `PushAssign` | 对齐 parts/02 |
| FD3 crawlerkit | Worker Runtime HTTP + bot_sdk | 协议替换 |
| captcha_blocked | （暂无） | MVP 不做 |
| parent_task_id / attempt | source_task_id + run_type | 重试建新 Task；attempt 可保留内部 |

### 1.2 状态名映射

| parts/现状 | Bot 平台 v2 | 说明 |
|---|---|---|
| `queued` | `pending` | 等待调度 |
| （无独立态） | `dispatching` | **新增**：已 claim/已发 Assign，等 ack |
| `running` | `running` | 执行中 |
| （无） | `canceling` | **新增**：取消中 |
| `succeeded` | `success` | 对外 success |
| （无） | `partial_success` | **新增**：exit0 + 部分 item 失败 |
| `failed` | `failed` | |
| `cancelled` | `canceled` | 美式拼写以 Bot API 为准 |
| `timeout` | `timeout` | |
| `captcha_blocked` | — | v2 MVP 不暴露 |

实现建议：

```text
DB enum 直接用 Bot 平台名（新库/新迁移）
若在旧库演进：可先保留旧 enum，API 层映射；中期统一迁移到 Bot 名
```

### 1.3 重试 vs reclaim（两边一致）

| | 重试 retry | reclaim |
|---|---|---|
| 触发 | 业务/执行终态后 | `running`/`dispatching` 租约或派发超时 |
| 动作 | **新建** Task | **同一** Task 回 `pending` |
| attempt | +1（新 Task） | 不变 |
| 通知 | 可通知 | 不通知“失败” |
| 入口 | `Transition` 终态分支后 `maybeScheduleRetry` | `Reclaim` → `Transition` 的 pending 分支 |

---

## 2. 与 parts 的关键差异：`dispatching` / `canceling`

### 2.1 parts/01 原模型

```text
queued -> running   # claim 事务内一步完成
running -> terminal | queued(reclaim)
```

### 2.2 Bot v2 模型（采用）

```text
pending -> dispatching -> running -> terminal
                \-> pending (dispatch timeout)
running -> pending (lease reclaim)
running|dispatching -> canceling -> canceled
```

### 2.3 对齐后的 claim 语义（修正 parts/02 一步到位）

parts/02 的 claim 把任务直接写成 `running`。  
Bot 平台改为 claim 进入 **`dispatching`**，真正 `running` 由 Worker `TaskAck`/`TaskStarted` 推进。

```text
ClaimOne 事务：
  pending -> dispatching
  写 worker_id, assignment_id, dispatching_at, dispatch_deadline_at
  写 lease_expires_at = now + timeout_s + grace
  assign_sent = false
  free_slots - 1

PushAssign 成功：
  assign_sent = true

TaskAck / TaskStarted（assignment 匹配）：
  dispatching -> running
  acked_at / started_at

dispatch_deadline 过期：
  dispatching -> pending
  清 worker/assignment/lease
  free_slots + 1
  dispatch_attempt + 1
```

这样：

- 保留 parts 的 **原子 claim + slot 计量**
- 补上 v1 文档里缺失的 **派发中态**
- 避免“DB 已 running 但 Worker 从未收到 Assign”的假运行窗口被当成真实执行

`started_at` 策略对齐 parts/01 Q1 取向 A：

```text
first_started_at  首次进入 running 时打，reclaim 不清
started_at        当前 attempt 进入 running 时打；reclaim 时置 NULL
```

---

## 3. 状态机接口（对齐 parts/01）

### 3.1 包与类型

建议路径（若在 crawler-lite 内演进）：

```text
internal/task/status.go      # 转移表
internal/task/finalize.go    # Bot 终态纯函数
internal/task/service.go     # Transition / Reclaim / Cancel / Dispatch
internal/task/claim.go       # ClaimOne 编排（或放 repository）
internal/repository/tasks.go
internal/repository/workers.go
```

```go
package task

type Status string

const (
    StatusPending         Status = "pending"
    StatusDispatching     Status = "dispatching"
    StatusRunning         Status = "running"
    StatusCanceling       Status = "canceling"
    StatusSuccess         Status = "success"
    StatusPartialSuccess  Status = "partial_success"
    StatusFailed          Status = "failed"
    StatusCanceled        Status = "canceled"
    StatusTimeout         Status = "timeout"
)

func IsTerminal(s Status) bool { /* success/partial_success/failed/canceled/timeout */ }

var ErrIllegalTransition = errors.New("illegal status transition")
var ErrAssignmentMismatch = errors.New("assignment mismatch")
var ErrNothingToRetry     = errors.New("nothing to retry")
```

### 3.2 合法转移表

```go
var legalTransitions = map[Status]map[Status]struct{}{
    StatusPending: {
        StatusDispatching: {},
        StatusCanceled:    {},
    },
    StatusDispatching: {
        StatusRunning:  {},
        StatusPending:  {}, // dispatch timeout / 主动回收
        StatusCanceling:{},
        StatusCanceled: {}, // 未 ack 前可本地收口
        StatusFailed:   {}, // dispatch exhausted
    },
    StatusRunning: {
        StatusSuccess:        {},
        StatusPartialSuccess: {},
        StatusFailed:         {},
        StatusTimeout:        {},
        StatusCanceling:      {},
        StatusPending:        {}, // lease reclaim
    },
    StatusCanceling: {
        StatusCanceled: {},
        StatusFailed:   {}, // 少见：取消过程失控
    },
    // terminals: no outbound
}
```

### 3.3 唯一推进入口

```go
type TransitionInput struct {
    TaskID       string // or int64 — 新平台可用 string ULID；演进期可 int64
    To           Status
    AssignmentID string // 执行期事件必填；Master 内部动作可空
    WorkerID     string
    ErrCode      string
    ErrMessage   string
    ErrDetail    string
    ExitCode     *int
    Stats        map[string]any // 可选快照
    Reason       string         // task_events.reason
    ActorUserID  string         // 人工动作
    // Finalize 用（Worker 报完成时）
    CancelRequested bool
    TimedOut        bool
    RuntimeErrCode  string
    ItemStats       *ItemStats // nil = 不据此改终态，由 To 直接指定
}

type ItemStats struct {
    Total, Pending, Running int
    Success, Failed, Skipped, Canceled, Timeout int
}

// Transition 是状态推进的唯一入口。
// 顺序铁律（对齐 parts/01）：
//  1) 读当前行 / 校验 assignment（如需要）
//  2) 校验 legalTransitions
//  3) 落库（WHERE 状态守卫 / assignment 守卫）→ 0 行则静默成功（迟到更新）
//  4) 写 task_events
//  5) 分支：
//     - pending（reclaim/dispatch 回收）：releaseSlot(若从 worker 持有态回收) + wakeup；不重试不失败通知
//     - 终态：ClearLease + releaseSlot(一次) + maybeScheduleRetry + Notify(detached)
//     - canceling：PushCancel
//  6) 副作用永不先于状态落库
func (s *Service) Transition(ctx context.Context, in TransitionInput) error
```

兼容别名（便于从现有 hub 迁移）：

```go
// OnUpdate 保留旧名，内部转 Transition。
func (s *Service) OnUpdate(ctx context.Context, in TransitionInput) error {
    return s.Transition(ctx, in)
}
```

### 3.4 Reclaim / 派发超时 / 取消

```go
// Reclaim 扫描器调用：lease 过期的 running，或 deadline 过期的 dispatching。
// 成功路径：
//  - running/dispatching -> pending（未超 max_reclaims / max_dispatch_attempts）
//  - 超限 -> failed (LEASE_RECLAIM_EXHAUSTED / DISPATCH_EXHAUSTED)
// 必须带 SQL 自守卫；rowsAffected=0 则 no-op。
func (s *Service) ReclaimExpired(ctx context.Context, now time.Time) (int, error)

// Cancel 用户/API：
//  pending -> canceled
//  dispatching/running -> canceling，再 PushCancel
//  已终态 -> ErrIllegalTransition（API 映射 409）
func (s *Service) Cancel(ctx context.Context, taskID, actorUserID, reason string) error
```

### 3.5 Finalize（Bot 平台新增，parts 无对等物）

Worker 报“进程结束”时，**不要**让 Worker 擅自决定 `partial_success`。  
Worker 上报事实，Master 调纯函数：

```go
type FinalizeFacts struct {
    CancelRequested bool
    TimedOut        bool
    ExitCode        *int
    RuntimeErrCode  string
    Items           ItemStats
}

// DecideTerminal 对应 bot-task-platform-v2-design.md §6。
func DecideTerminal(f FinalizeFacts) (Status, string /*error_code*/)
```

```text
cancel > timeout > runtime/exit 失败 > item 统计
exit==0 && total==0 -> success
exit==0 && hard_fail==0 -> success
exit==0 && success==0 && hard_fail>0 -> failed
exit==0 && success>0 && hard_fail>0 -> partial_success
```

Worker 侧消息示例：

```text
TaskFinished {
  assignment_id, task_id,
  exit_code,
  timed_out,
  cancel_requested,   # 或由 Master 已有 cancel 标记覆盖
  error_code?,
  item_stats_snapshot?  # 可选；Master 仍以 DB 计数为准校准
}
```

Master：

```text
facts := from(DB item counters, TaskFinished, task.cancel_requested_at)
to, code := DecideTerminal(facts)
Transition(To=to, ...)
```

---

## 4. 派发与 Worker 选择接口（对齐 parts/02）

### 4.1 Hub 传输层（降级后）

现状 `Hub.Assign` / 内存 `tasks` map **删除**（按 parts/02）。

```go
package task

// Hub 是 consumer-declared 接口，由 internal/hub 实现。
type Hub interface {
    HasHandle(workerID string) bool
    PushAssign(ctx context.Context, workerID string, a *AssignTask) error
    PushCancel(ctx context.Context, workerID string, c *CancelTask) error
    // 可选：对账后丢弃
    PushDropAssignment(ctx context.Context, workerID string, assignmentID, taskID string) error
}
```

```go
// hub 内部维护 workerID -> *Session（parts/02 §3.6）
// 不再维护 taskID -> worker 的权威映射；权威在 DB tasks.worker_id/assignment_id
```

### 4.2 Claim 仓库接口

```go
type ClaimOpts struct {
    RequiredCaps []string
    TimeoutS     int
    LeaseGraceS  int
    DispatchDeadlineS int // 默认 30
}

type ClaimedTask struct {
    TaskID       string
    BotID        string
    BotVersionID string
    WorkerID     string
    AssignmentID string
    TimeoutS     int
    // 构建 AssignTask 所需快照字段…
}

type TaskRepository interface {
    ListClaimable(ctx context.Context, limit int) ([]ClaimableRow, error)
    // 事务内：锁指定 pending 任务 + 选 worker + pending->dispatching + 扣 slot
    ClaimOne(ctx context.Context, taskID string, opts ClaimOpts) (*ClaimedTask, error)
    MarkAssignSent(ctx context.Context, taskID, assignmentID string) error
    ListAssignPendingForWorkers(ctx context.Context, workerIDs []string) ([]*Task, error)

    // 状态机落库
    TransitionRow(ctx context.Context, in TransitionRowInput) (rowsAffected int64, err error)
    ReclaimRunningExpired(ctx context.Context, now time.Time, maxReclaims int) ([]ReclaimResult, error)
    ReclaimDispatchExpired(ctx context.Context, now time.Time, maxDispatchAttempts int) ([]ReclaimResult, error)
    ClearLease(ctx context.Context, taskID string) error
}

type WorkerRepository interface {
    UpsertHello(ctx context.Context, w WorkerHello) error
    TouchHeartbeat(ctx context.Context, workerID string, observed FreeSlotsReport) error
    AdjustSlots(ctx context.Context, workerID string, delta int) error // +1/-1，带上下界
    MarkOffline(ctx context.Context, workerID string) error
}
```

### 4.3 Claim SQL 草图（Bot 状态版）

```sql
BEGIN;

SELECT id, bot_id, bot_version_id, requirements, priority
  FROM tasks
 WHERE id = $task_id AND status = 'pending'
 FOR UPDATE;   -- 应用层选 id；被抢走则 0 行

SELECT id, free_slots
  FROM workers
 WHERE status = 'online'
   AND free_slots > 0
   AND $required_caps <@ capabilities
 ORDER BY free_slots DESC
 LIMIT 1
 FOR UPDATE;   -- 非 SKIP LOCKED，保 least-loaded

UPDATE tasks SET
   status = 'dispatching',
   worker_id = $w,
   assignment_id = $assignment_id,
   dispatching_at = now(),
   dispatch_deadline_at = now() + make_interval(secs => $dispatch_deadline_s),
   lease_expires_at = now() + make_interval(secs => $timeout_s + $grace_s),
   assign_sent = false,
   dispatch_attempt = dispatch_attempt + 1,
   updated_at = now()
 WHERE id = $task_id AND status = 'pending';

UPDATE workers
   SET free_slots = free_slots - 1
 WHERE id = $w AND free_slots > 0;

COMMIT;
```

无 worker：提交空事务/回滚任务锁，返回 `ErrNoWorker`（任务仍 pending）。

### 4.4 `dispatchOnce` 编排

```text
1. rows = ListClaimable(limit)
2. bots/versions 批量预查（对齐 parts/02：每轮 batch，不做 LRU）
3. for each row:
     claimed, err = ClaimOne(row.ID, optsFromBot)
     ErrNoWorker / 0 行 -> continue/break
     assign = BuildAssign(claimed, botSnapshot)
     if build err -> Transition(failed, DEPENDENCY/PACKAGE/build)
     if hub.HasHandle(worker):
        PushAssign
        MarkAssignSent
4. 补发扫描：ListAssignPendingForWorkers(localHandles) -> PushAssign + MarkAssignSent
```

单 Master 时补发几乎立即完成；接口仍按 parts/02 保留 `assign_sent`，避免假运行。

### 4.5 Slot 释放规则（原样采纳 parts/02）

```text
扣减：仅 ClaimOne 成功
释放：仅当 Transition/Reclaim 的 rowsAffected > 0
  - 终态成功写入
  - reclaim 成功写回 pending/failed
上界：free_slots+1 WHERE free_slots < max_concurrency
心跳：不直接信任上报覆盖权威 free_slots；只作校准/metrics
```

```go
func (s *Service) releaseSlot(ctx context.Context, workerID string) {
    if workerID == "" { return }
    _ = s.workers.AdjustSlots(ctx, workerID, +1)
}
```

---

## 5. gRPC 协议草图（在现有 proto 上演进）

现状：`proto/worker/v1/worker.proto`  
Bot 平台需要 **assignment_id**、更细任务事件、对账、数据面消息。

### 5.1 兼容策略

```text
方案 A（推荐若产品切换）：新建 proto/bot/v1/worker.proto，Master/Worker 新服务
方案 B（演进）：在现有 worker.v1 上加字段/消息，旧字段保留
```

下列以 **逻辑消息** 为准（包名可替换）。

### 5.2 Worker → Master

```protobuf
message WorkerMessage {
  string message_id = 1;
  oneof payload {
    Hello hello = 10;
    Heartbeat heartbeat = 11;
    ReconcileReport reconcile = 12;

    TaskAck task_ack = 20;
    TaskStarted task_started = 21;
    TaskFinished task_finished = 22;
    TaskFailed task_failed = 23;
    CancelResult cancel_result = 24;

    LogBatch log_batch = 30;
    TaskItemUpsert task_item_upsert = 31;
    TaskItemStatusChange task_item_status = 32;
    ResultBatch result_batch = 33;
    ArtifactMeta artifact_meta = 34;
  }
}

message Hello {
  string worker_id = 1;
  string version = 2;
  int32 max_concurrency = 3;
  repeated string runtimes = 4;
  repeated string capabilities = 5;
  map<string,string> labels = 6;
  string shared_secret = 7;
}

message Heartbeat {
  int32 observed_running = 1;
  int32 observed_free_slots = 2; // 仅校准，非权威
  double cpu_pct = 3;
  double mem_pct = 4;
  // 续租依据：当前 assignment 列表
  repeated AssignmentRef running = 5;
}

message AssignmentRef {
  string task_id = 1;
  string assignment_id = 2;
}

message ReconcileReport {
  string session_id = 1;
  repeated AssignmentRef running = 2;
}

message TaskAck {
  string task_id = 1;
  string assignment_id = 2;
}

message TaskStarted {
  string task_id = 1;
  string assignment_id = 2;
  int64 started_at_unix_ms = 3;
}

message TaskFinished {
  string task_id = 1;
  string assignment_id = 2;
  int32 exit_code = 3;
  bool timed_out = 4;
  bool cancel_requested = 5;
  string error_code = 6;
  string error_message = 7;
  // 可选；Master 以 DB 为准
  ItemStatsProto item_stats = 8;
}

message TaskFailed {
  string task_id = 1;
  string assignment_id = 2;
  string error_code = 3;
  string error_message = 4;
  int32 exit_code = 5; // optional
}

message CancelResult {
  string task_id = 1;
  string assignment_id = 2;
  string command_id = 3;
  bool force_killed = 4;
  int32 exit_code = 5;
}

message TaskItemUpsert { /* id/key/type/input/... */ }
message TaskItemStatusChange { /* item_id, from, to, error_... */ }
message ResultBatch { /* results[] with idempotency_key */ }
message ArtifactMeta { /* storage_key, checksum, ... */ }
message LogBatch { /* lines[] */ }
```

### 5.3 Master → Worker

```protobuf
message MasterMessage {
  string message_id = 1;
  oneof payload {
    Welcome welcome = 10;
    AssignTask assign_task = 11;
    CancelTask cancel_task = 12;
    DropAssignment drop_assignment = 13;
    Ping ping = 14;
  }
}

message AssignTask {
  string assignment_id = 1;
  string task_id = 2;
  string bot_id = 3;
  string bot_code = 4;
  string bot_version_id = 5;
  string entrypoint = 6;
  string package_uri = 7;       // Master 内部鉴权下载 URL
  string package_checksum = 8;  // sha256
  string package_token = 17;    // 短期下载 token；也可复用 TASK_TOKEN
  int32 timeout_s = 9;
  int32 cancel_grace_period_s = 10;
  bytes input_params_json = 11;
  bytes task_config_json = 12;
  string input_file_uri = 13;
  repeated string required_capabilities = 14;
  string runtime = 15;
  // TASK_TOKEN 仅下发 Worker，由 Runtime 本地校验；可放 env map
  map<string,string> env = 16;
}

message CancelTask {
  string task_id = 1;
  string assignment_id = 2;
  string command_id = 3;
  int32 grace_period_s = 4;
}

message DropAssignment {
  string task_id = 1;
  string assignment_id = 2;
  string reason = 3; // assignment_mismatch | task_terminal | reclaim
}
```

### 5.4 MVP-0 文件传输约束

```text
所有持久文件均由 Master local storage 管理。

Bot package:
  Client -> Master -> Master local storage
  AssignTask.package_uri 指向 Master 内部鉴权下载 endpoint

Worker:
  通过 package_uri 下载脚本包，校验 package_checksum
  Worker local disk 只作为临时执行目录，不是权威存储

Artifact:
  Worker -> Master 内部鉴权上传 endpoint -> Master local storage -> artifacts 表
  Browser/API Client -> /api/artifacts/{artifact_id}/download -> Master 鉴权返回文件

后续对象存储:
  只替换 Master storage backend，不改变 Worker 下载/上传与 API 下载语义
```

### 5.5 与现有 proto 字段对应

| 现有 | Bot 草图 |
|---|---|
| `Hello.concurrency` | `max_concurrency` |
| `AssignTask.source_key` | `package_uri` + checksum |
| `AssignTask.config_json/args_json` | `task_config_json` / `input_params_json` |
| `TaskUpdate` 单消息多状态 | 拆成 Ack/Started/Finished/Failed/CancelResult |
| `ItemEmitted` | `ResultBatch`（+ 可选 TaskItem*） |
| `ArtifactRef` | `ArtifactMeta` |
| `CancelTask.task_id` only | + `assignment_id` + `command_id` |
| （无） | `ReconcileReport` / `DropAssignment` / `Heartbeat.running` |

---

## 6. readLoop 分发契约（对齐 parts 的 chokepoint 思想）

```text
hub.readLoop 仍是 Worker→Master 唯一分发点：

Hello            -> workers.UpsertHello + Welcome + 等待/触发 Reconcile
Heartbeat        -> TouchHeartbeat + 对 running[] 续租（按 assignment_id）
ReconcileReport  -> 对比 DB；多余本地执行 DropAssignment；DB 有/Worker 无则等 lease
TaskAck/Started  -> Transition(dispatching|running)
TaskFinished     -> DecideTerminal + Transition(terminal)
TaskFailed       -> Transition(failed)
CancelResult     -> Transition(canceled) + item 未终态刷 canceled
LogBatch         -> log sink
TaskItem*        -> item service（只写 item + 原子计数，不直接改 Task 终态）
ResultBatch      -> result sink（idempotent）
ArtifactMeta     -> artifact sink
```

续租 SQL：

```sql
UPDATE tasks
   SET lease_expires_at = now() + make_interval(secs => $timeout_s + $grace_s),
       updated_at = now()
 WHERE id = $task_id
   AND assignment_id = $assignment_id
   AND status IN ('dispatching', 'running');
```

---

## 7. Reaper 接口

```go
// 挂在 app.Run 的 errgroup 中（不要放进构造器）
func (s *Service) RunReaper(ctx context.Context) error {
    t := time.NewTicker(s.cfg.ReaperInterval) // 10s
    for {
        select {
        case <-ctx.Done(): return nil
        case <-t.C:
            _, _ = s.ReclaimExpired(ctx, time.Now())
            _ = s.FailStuckCanceling(ctx, time.Now())
            _ = s.MarkStaleWorkersOffline(ctx, time.Now())
        }
    }
}
```

行为摘要：

| 扫描条件 | 动作 |
|---|---|
| `dispatching AND dispatch_deadline_at < now` | -> pending 或 failed(DISPATCH_*)；+slot |
| `running AND lease_expires_at < now` | -> pending 或 failed(LEASE_*)；+slot；reclaim_count++ |
| `canceling AND cancel_requested_at + grace + extra < now` | -> canceled 强制收口 |
| `workers.last_heartbeat 过旧` | worker offline（**不**立即 fail 任务） |

---

## 8. 配置键（对齐 parts/01 Q3）

```go
// master
LeaseGraceSeconds       int `env:"LEASE_GRACE_SECONDS" envDefault:"60"`
ReaperIntervalSeconds   int `env:"REAPER_INTERVAL_SECONDS" envDefault:"10"`
MaxReclaims             int `env:"MAX_RECLAIMS" envDefault:"3"`
MaxDispatchAttempts     int `env:"MAX_DISPATCH_ATTEMPTS" envDefault:"5"`
DispatchDeadlineSeconds int `env:"DISPATCH_DEADLINE_SECONDS" envDefault:"30"`
DefaultTaskTimeoutSeconds int `env:"DEFAULT_TASK_TIMEOUT_SECONDS" envDefault:"600"`

// worker
HeartbeatIntervalSeconds int `env:"HEARTBEAT_INTERVAL_SECONDS" envDefault:"5"`
```

硬约束：

```text
LeaseGraceSeconds >= 2 * HeartbeatIntervalSeconds
```

---

## 9. 错误码与 HTTP 映射（可靠性相关）

| error_code / sentinel | 场景 | HTTP |
|---|---|---|
| `ILLEGAL_TRANSITION` / `ErrIllegalTransition` | 非法状态 | **409** + current_status |
| `ASSIGNMENT_MISMATCH` | 上报 assignment 不符 | gRPC 内拒绝；不改 DB |
| `DISPATCH_TIMEOUT` | 派发 ack 超时回收 | 任务回 pending（事件可见） |
| `DISPATCH_EXHAUSTED` | 派发次数用尽 | Task failed |
| `LEASE_EXPIRED` / `LEASE_RECLAIM_EXHAUSTED` | 租约回收/用尽 | pending 或 failed |
| `WORKER_OFFLINE` | 可选失败策略 | failed |
| `NOTHING_TO_RETRY` | 失败项重试无目标 | 400 |

自动路径（readLoop / reaper / dispatch）遇 `ErrIllegalTransition`：**静默**（Debug 日志）。  
人工路径（API cancel/retry）：**409**。

---

## 10. 数据表最小增量（可靠性）

在 Bot v2 表清单上，可靠性必需列：

```text
tasks
  assignment_id
  assign_sent
  dispatch_attempt
  dispatching_at
  dispatch_deadline_at
  lease_expires_at
  reclaim_count
  first_started_at
  started_at
  acked_at
  cancel_requested_at
  cancel_grace_period_seconds
  worker_id
  source_task_id

workers
  status, session_id
  max_concurrency, free_slots, current_running
  capabilities, runtimes, labels
  last_heartbeat_at

task_events
  task_id, from_status, to_status, reason, message, metadata, created_by, created_at
```

索引：

```text
tasks (status, lease_expires_at)
tasks (status, dispatch_deadline_at)
tasks (assignment_id)
tasks (status, priority, created_at)
workers (status, free_slots)
```

---

## 11. 与现有代码的落地切口

| 现有文件 | 现状 | 目标改动 |
|---|---|---|
| `internal/task/service.go` | `OnUpdate` + first-fit `Assign` | 拆 `status.go`/`finalize.go`；`Transition`；claim 循环 |
| `internal/task/retry.go` | parent/attempt 子任务 | 保留纯函数；err class 增加 reclaim/dispatch exhausted |
| `internal/hub/hub.go` | 内存 session + Assign 选 worker | 删选主逻辑；`Push*` + workerID 索引 |
| `internal/repository/tasks.go` | `SetStatus` 无守卫 | 守卫 + ClaimOne + Reclaim SQL |
| `proto/worker/v1/worker.proto` | 无 assignment | 按 §5 扩展或 bot.v1 新 proto |
| `internal/app/app.go` | 无 reaper | `RunReaper` 进 errgroup |
| `crawlerkit-py` | FD3 | Bot MVP 新 `bot_sdk` + Runtime；可并行存在 |

**不在本接口范围：** UI、Bot CRUD 细节、Schedule 全量、对象存储切换。

---

## 12. MVP-0 接线顺序（只含可靠性闭环）

```text
1. migrations：workers 表 + tasks lease/assignment/dispatch 列
2. status.go 转移表 + Transition 守卫
3. ClaimOne(pending->dispatching) + free_slots
4. Hub.PushAssign + assign_sent
5. TaskAck/Started -> running；Finished -> finalize -> terminal
6. Heartbeat 续租 + Reaper
7. Canceling/Canceled + PushCancel
8. 单测：
   - 非法转移 / 终态覆盖
   - 双 claim 不双派
   - 终态与 reclaim 竞态只释放一次 slot
   - 迟到 Finished 不覆盖 canceled/failed
   - dispatch deadline 回 pending
```

---

## 13. 明确不对齐 / 不采纳的 parts 项

| parts 项 | 决策 |
|---|---|
| `captcha_blocked` 与 `ResolveCaptcha` | Bot v2 MVP **不做**；接口不暴露 |
| 状态名 `queued/succeeded/cancelled` | 对外改用 Bot 名 |
| claim 直接 `running` | **改为** `dispatching` |
| 终态仅 worker 直报 succeeded/failed | Bot 增加 Master **finalize**（item 统计） |
| FD3 item/log 协议 | Bot 数据面走 Runtime→Worker→gRPC |
| 多 Master 补发的完整跨副本 | 单 Master 先做；`assign_sent` 字段保留 |

---

## 14. 决议摘要（可直接当实现验收清单）

1. **单一入口** `Transition`/`OnUpdate`，顺序：校验 → 落库 → 事件 → 副作用  
2. **lease + reaper + reclaim_count**，grace=60s，心跳=5s，reaper=10s  
3. **claim 原子扣 slot**，终态/reclaim 成功才 +slot  
4. **hub 只传输**，不选 worker  
5. **pending → dispatching → running**，dispatch 超时回 pending  
6. **终态由 `DecideTerminal` 判定**，支持 `partial_success`  
7. **所有执行期消息带 `assignment_id`**  
8. **Worker offline ≠ 立即 fail Task**  
9. **captcha 不做**；其余可靠性与 parts/01–02 对齐  
10. **MVP-0 必须包含 reaper**，否则不叫完成执行闭环  

---

## 附录 A. 转移速查图

```text
                    +----------+
         create --> | pending  | <------------------------------+
                    +----+-----+                                 |
                         | ClaimOne                              | reclaim /
                         v                                       | dispatch timeout
                 +-------+--------+                              |
                 | dispatching    | -----------------------------+
                 +--+----------+--+
           ack/start|          | cancel
                    v          v
              +-----+--+   +---+------+
              | running |-->| canceling |
              +--+--+---+   +----+-----+
     finish/fail/timeout|        |
                        v        v
              success / partial_success / failed / timeout / canceled
```

## 附录 B. 与 `bot-task-platform-v2-design.md` 章节索引

| 本文 | v2 设计章节 |
|---|---|
| §1–2 映射与 dispatching | §4 状态机、§5 可靠性 |
| §3 Transition/Finalize | §4、§6 finalize |
| §4 Claim/Hub/Slot | §13 Worker 通信 |
| §5 Proto | §13 数据面 |
| §7 Reaper | §5.3 |
| §12 MVP-0 | §22.1 |
