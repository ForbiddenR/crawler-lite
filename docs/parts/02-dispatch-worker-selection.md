# 部件 2：派发与 worker 选择 — 设计

> crawler-lite 拆分后的第二块。讨论范围限定在「queued 任务怎么变成 running、挑哪个 worker、slot 怎么算、AssignTask 怎么真正下发到 worker」，不碰 worker 连接/心跳/reaper 的执行体（部件 3）、Python 执行（部件 4）、日志/结果（部件 5）。
>
> 配套阅读：`docs/DESIGN-v2.md` 第 5 章（原子 claim）、`docs/parts/01-task-state-machine.md`（部件 1，claim 依赖其 lease/`workers` 表/`OnUpdate`）、`internal/task/service.go`（`RunDispatcher`/`dispatchOnce`/`buildAssign`）、`internal/hub/hub.go`（`Assign`）。

## 0. 本部件的边界

**包含：**
- claim 事务（逐个、事务内锁 worker 行、写 lease、扣 slot）。
- worker 选择策略（least-loaded + 能力感知）。
- `required_caps` 的来源（spider 配置）与匹配。
- `buildAssign` 的位置与 spider 批量预查优化。
- `AssignTask` 真正下发：本副本有句柄则推 outbox，否则置 `assign_sent=false`。
- 扫描补发：发现 `assign_sent=false` 的 running 任务，由持句柄副本补推。
- 终态/reclaim 时的 slot 释放（`workers.free_slots+1`），含**双释放防护**。
- hub 从「选 worker + 推消息」降级为「只推消息」（`Assign`→`PushAssign`/`PushCancel`/`HasHandle`）。

**不包含（留给后续部件）：**
- `workers` 表的建表迁移 → 部件 1 已认领 `00010`（Phase 1）。
- `tasks.lease_expires_at`/`assign_sent` 列 → 部件 1 已认领 `00011`。
- worker 怎么连、Hello/Heartbeat、reaper goroutine、续租执行体 → 部件 3。
- `OnUpdate`/`Reclaim`/`ResolveCaptcha` 的状态机规则 → 部件 1（部件 2 只**调用**它们）。
- AssignTask 到达 worker 后怎么执行 → 部件 4。

部件 2 是部件 1 状态机的**第一个消费者**：claim 写 lease、终态/reclaim 释放 slot，都经部件 1 定的入口或其镜像。

## 1. 现状回顾

- `RunDispatcher`：5s tick + wakeup，每次 `dispatchOnce`。
- `dispatchOnce`：`ListQueued` 全量 → 逐个 `buildAssign` → `Hub.Assign` → `ok=false` 则 break。
- `Hub.Assign`：遍历 `sessions` map，**first-fit**（第一个 `FreeSlots>0`），本地 `FreeSlots--`，推 outbox，记 `h.tasks[taskID]`。
- `buildAssign`：逐个 `SpiderRepo.Get`，拼 `AssignTask`（`ProxyUrl=""` 留空）。

## 2. 上限（本部件要解的）

1. **first-fit，非公平非最优** — 不挑最空闲、不挑能力匹配。
2. **`FreeSlots` 是心跳猜测值，多副本各扣各的** — slot 计量失准，可过派。
3. **`ListQueued` 不原子** — 多副本双派同一任务。
4. **派发与选 worker 跨两处无事务边界** — `ListQueued` 到 `Assign` 间状态可变。
5. **`Hub.Assign` 耦合「选 worker」与「推消息」** — v2 要 hub 降级为传输层。
6. **`buildAssign` 逐个查 spider** — N 任务 N 次 DB 往返。

## 3. 目标设计

### 3.1 claim 事务（逐个，事务内锁 worker 行）

`dispatchOnce` 改为循环调用 `Repo.ClaimOne(ctx, ClaimOpts)`，每次返回一个 claimed 任务或「无任务/无 worker」哨兵。`ClaimOne` 在**单个 `pgx.Tx`** 内完成：

```sql
BEGIN;
-- 1. 锁一个就绪的 queued 任务（SKIP LOCKED：跨副本不阻塞，各锁不同行）
SELECT id, spider_id, spider_version, triggered_args
  FROM tasks
 WHERE status = 'queued'
   AND (not_before IS NULL OR not_before <= now())
 ORDER BY queued_at
 LIMIT 1
 FOR UPDATE SKIP LOCKED;
-- 无行 → COMMIT; 返回 ErrNoClaimableTask

-- 2. 读 spider 的 required_caps 与 timeout_s（应用层用 spider_id 查，见 3.4 批量预查）
--    required_caps 来自 spider.config["required_caps"]，缺省 []

-- 3. 选 worker：锁 worker 行（FOR UPDATE 非 SKIP —— 保 least-loaded，见 3.2）
SELECT id, free_slots
  FROM workers
 WHERE status = 'online'
   AND free_slots > 0
   AND $required_caps <@ capabilities
 ORDER BY free_slots DESC
 LIMIT 1
 FOR UPDATE;
-- 无行 → COMMIT; 返回 ErrNoWorker（任务仍 queued，下一轮再试）

-- 4. 占用：任务转 running + 写租约；worker 扣 slot
UPDATE tasks
   SET status='running', worker_id=$w,
       lease_expires_at = now() + make_interval(secs => $timeout_s + $grace),
       started_at = CASE WHEN started_at IS NULL THEN now() ELSE started_at END,  -- 见部件1 Q1
       assign_sent = false
 WHERE id = $t;
UPDATE workers SET free_slots = free_slots - 1 WHERE id = $w;
COMMIT;
-- 返回 ClaimedTask{TaskID, SpiderID, SpiderVersion, WorkerID, TriggeredArgs, TimeoutS}
```

要点：

- **`FOR UPDATE SKIP LOCKED`（任务行）+ `FOR UPDATE`（worker 行，非 SKIP）**。任务行 SKIP LOCKED 让多副本不阻塞、各锁不同任务；worker 行用普通 `FOR UPDATE`，第二个副本选到同一最空闲 worker 时**短阻塞等第一个提交**，然后看到 `free_slots` 已减、重新评估——保证 least-loaded 正确性。worker 行不用 SKIP LOCKED，否则会跳过最空闲 worker 选次空闲，破坏 least-loaded。
- **`<@`（JSONB 数组包含）做能力过滤**。`$required_caps <@ capabilities` 意为「worker 的 capabilities 包含任务所有 required_caps」。`required_caps=[]` 时恒真，所有 online worker 候选。
- **claim 事务不 `buildAssign`**。事务只改状态；`buildAssign` 在事务外（见 3.4）。这是与部件 1 `OnUpdate` 顺序铁律一致的原则：**事务只管状态，副作用在事务外**。
- **claim 写 `assign_sent=false`**。真正推到 outbox 后才置 true（见 3.5）。
- **`ErrNoClaimableTask` / `ErrNoWorker` 是哨兵不是错误**。`dispatchOnce` 收到 `ErrNoClaimableTask` → 派完，结束本轮；收到 `ErrNoWorker` → 所有 worker 忙，结束本轮（任务仍 queued，下 tick 再试）。

### 3.2 least-loaded 与能力感知

- **排序 `ORDER BY free_slots DESC`**：选 `free_slots` 最大的 worker（最空闲）。`free_slots` 由 claim 原子扣、终态/reclaim 原子加，是权威值（不是 v1 的心跳猜测）。
- **能力过滤 `required_caps <@ capabilities`**：`required_caps` 来自 spider 配置（见 3.3），`capabilities` 来自 worker Hello（部件 3 写入 `workers` 表）。
- **为什么 worker 行用 `FOR UPDATE` 而非 `SKIP LOCKED`**：least-loaded 要求「选当前最空闲的」。若两副本同时看到 W1 `free_slots=5` 最空闲，第二个副本的 `SELECT...FOR UPDATE` 会等第一个提交（W1 变 4），然后重新排序——可能 W1 仍最空闲（4≥其他）就选 W1，或选到别的。这保证了「选的是扣减后的真实最空闲」，代价是 worker 行毫秒级阻塞。可接受。

### 3.3 `required_caps` 的来源

`required_caps` 存在 `spiders.config` JSONB 里（`config` 列已存在，无需迁移），键 `required_caps`：

```json
{
  "entry_module": "spiders.amazon:PriceSpider",
  "timeout_s": 600,
  "required_caps": ["chromium", "python3.12"],
  "retry": { "max_attempts": 3, ... }
}
```

- 缺省 `[]` → 不要求能力，任何 online worker 候选（向后兼容：现有 spider 无此字段时行为不变）。
- 解析在 `buildAssign` 同处（应用层读 `sp.config["required_caps"]`，转 `[]string` 传进 `ClaimOne`）。复用 `retry.go` 的 `numberAsInt` 式宽松解析：字段缺失/类型错 → `[]`，不报错（「spider 配置笔误不该让 master 崩」的既有原则）。
- worker 侧 `capabilities` 由 `Hello` 上报（proto 已有 `repeated string capabilities`），部件 3 写入 `workers.capabilities` JSONB。

### 3.4 `buildAssign` 与 spider 批量预查

`buildAssign` 仍在 `task.Service`，但调用位置从「`Assign` 前」移到「`ClaimOne` 成功后」：

```
dispatchOnce:
  1. ListClaimableIDs()  // 仅 id 列表，轻量（或直接让 ClaimOne 内部处理，无需此步）
  2. loop:
     claimed, err := Repo.ClaimOne(ctx, opts)   // 事务内选任务+worker
     if ErrNoClaimableTask: break
     if ErrNoWorker: break
     assign := buildAssign(claimed)              // 事务外，用预查的 spider
     if err: OnUpdate(claimed.TaskID, failed, "build_assign", ...)  // 部件1 收拢
        continue
     pushAssign(claimed.WorkerID, assign)        // 见 3.5
```

**spider 批量预查优化**（解上限 #6）：`dispatchOnce` 进入循环前，先查「这一轮可能 claim 的 spider」——但 claim 是逐个的，事先不知道会 claim 哪些 spider。两种做法：

- **a) 不预查，逐个查 + LRU 缓存**：`task.Service` 持一个进程内 `spiderLRU`（容量 ~100），`buildAssign` 先查缓存再查 DB。同一 spider 高频派发时命中缓存。简单，覆盖 99% 场景。
- **b) 预查 queued 任务涉及的 spider**：`SELECT DISTINCT spider_id FROM tasks WHERE status='queued'` → 批量 `WHERE id=ANY(...)` 查 spider 进 map → 循环里从 map 取。多一次扫描，但 spider 查询彻底批量化。

**建议 a（LRU 缓存）**。理由：spider 数量有限且定义稳定，LRU 命中率极高；b 的「预查 queued 涉及的 spider」要在 claim 前扫一次 `tasks`，而 claim 本身已逐个锁任务行，预查的 spider 集合未必等于最终 claim 到的（有些任务可能被别的副本抢走、`not_before` 未到），预查有浪费。LRU 既省往返又不浪费。

`buildAssign` 现有逻辑（拼 `entry_module`+`config` 进 `config_json`、`args_json`、`timeout_s`）不变，只加从 `config["required_caps"]` 取出传 `ClaimOne`（其实 `ClaimOne` 在 `buildAssign` 之前调，所以 `required_caps` 要在 claim 前就知道——见下面的顺序问题）。

**顺序问题**：claim 需要 `required_caps`（选 worker），而 `required_caps` 在 spider 配置里，`buildAssign` 也需要 spider。所以 spider 查询得在 claim **之前**。调整：

```
dispatchOnce:
  queuedIDs := Repo.ListClaimableIDs(ctx)         // 轻 id 列表
  spiders := batchGetSpiders(queuedIDs) via LRU   // 预查/缓存
  loop over queuedIDs:
     sp := spiders[task.spider_id]
     opts := ClaimOpts{RequiredCaps: sp.config["required_caps"], TimeoutS: sp.timeout_s}
     claimed, err := Repo.ClaimOne(ctx, opts)     // 事务内：锁这个任务行+选 worker
     ...
     assign := buildAssign(claimed, sp)           // 已有 spider，不重查
```

这里 `ClaimOne` 需要「锁指定任务行」而非「锁任意就绪行」——把 claim 拆成「先选任务 id（应用层 `ListClaimableIDs`）→ 再事务内锁该 id + 选 worker」。`ListClaimableIDs` 是 `SELECT id, spider_id FROM tasks WHERE queued AND ready ORDER BY queued_at`（不加锁，快照），循环里 `ClaimOne(taskID, opts)` 事务内 `SELECT ... WHERE id=$taskID AND status='queued' FOR UPDATE`（锁指定行，若已被别人抢走则 0 行 → 跳过下一个）。这样 spider 预查和 claim 都干净。**这是对 3.1 的微调**：3.1 的「`ClaimOne` 内部 `LIMIT 1` 选任务」改为「应用层选 id + `ClaimOne(taskID)` 锁指定行」。

### 3.5 AssignTask 下发与扫描补发

claim 成功后，`assign_sent=false`。下发逻辑：

```go
func (s *Service) pushAssign(workerID string, assign *pb.AssignTask) {
    if s.hub.HasHandle(workerID) {
        if err := s.hub.PushAssign(workerID, assign); err == nil {
            s.repo.MarkAssignSent(ctx, assign.TaskId)   // 置 true
        }
    }
    // 句柄不在本副本 → 不置 assign_sent，留给持句柄副本补发
}
```

**扫描补发**：`dispatchOnce` 循环末尾加一步——扫本副本持有句柄的 worker 的 `assign_sent=false` 的 running 任务，补推：

```sql
SELECT t.id, t.spider_id, t.spider_version, t.triggered_args, t.worker_id
  FROM tasks t
 WHERE t.status='running' AND t.assign_sent=false
   AND t.worker_id = ANY($worker_ids_with_handle)   -- 本副本持有句柄的 worker id 列表
```

对每条 `buildAssign` + `PushAssign` + `MarkAssignSent`。这一步让「claim 在副本 A、句柄在副本 B」的任务在副本 B 的下一轮派发里被补发，延迟 ≤ 一个派发 tick（5s）。`wakeup` 也覆盖补发（新连入的 worker 会触发 wakeup，持句柄副本立刻补发它名下的未下发任务）。

**为什么不用 Redis Pub/Sub 即时通知**（已选 assign_sent+扫描）：Pub/Sub 即时通知需要「哪个副本持哪个 worker 句柄」的跨副本可见性，等于把 hub 的句柄表也分布式化，复杂度高。assign_sent + 扫描补发用 5s tick 兜底，延迟可接受，且 hub 句柄表保持进程内（部件 3 的正确边界）。后续若延迟不可接受，再加 Pub/Sub 优化层。

### 3.6 hub 降级为传输层

`Hub.Assign(ctx, *pb.AssignTask) (bool, error)` → **删除**。派发决策移到 claim 事务，hub 只推消息。新增：

```go
// HasHandle 报告该 worker 的 gRPC 句柄是否在本副本。
func (h *WorkerHub) HasHandle(workerID string) bool

// PushAssign 把 AssignTask 推到该 worker 的 outbox。句柄不在本副本返回 ErrNoHandle。
func (h *WorkerHub) PushAssign(ctx, workerID string, a *pb.AssignTask) error

// PushCancel 同理推 CancelTask。
func (h *WorkerHub) PushCancel(ctx, workerID string, taskID int64) error
```

为此 hub 需要一个 **`workerID→*Session` 索引**（现在 `sessions` 只按 `sessionID` 键）。部件 3 在 `register`/`unregister` 时维护此索引；部件 2 只**用**它（`HasHandle`/`PushAssign` 查此索引）。索引归部件 3 维护，部件 2 依赖其存在。

`hub.tasks` map（`taskMeta`）→ **删除**。任务归属读 `tasks.worker_id`（DB）。

`task.Hub` 接口（`service.go` 里 consumer-declared）改为：
```go
type Hub interface {
    HasHandle(workerID string) bool
    PushAssign(ctx, workerID string, a *pb.AssignTask) error
    PushCancel(ctx, workerID string, taskID int64) error
}
```
`Cancel`（`task.Service.Cancel`）改为：`OnUpdate(id, cancelled, ...)`（部件 1 终态分支）+ `Hub.PushCancel(workerID, id)`（worker_id 从 DB 读）。取消的真相是 DB 的 `cancelled` 状态，不是消息推没推到。

### 3.7 slot 释放与双释放防护

slot 的成对操作：

| 时刻 | 操作 | 谁负责 |
|---|---|---|
| claim | `workers.free_slots - 1` | 部件 2 claim 事务 |
| 终态（succeeded/failed/timeout/captcha/cancelled） | `workers.free_slots + 1` | 部件 2（`OnUpdate` 终态分支调） |
| reclaim | `workers.free_slots + 1` | 部件 1 `Reclaim` + 部件 2 释放 |

**双释放隐患**：终态释放和 reclaim 释放都可能对同一个 `worker_id` 执行 `+1`。场景：

1. 任务在 W1 上 running，租约到期 → reaper `Reclaim`：`running→queued`、清 `worker_id`、`W1.free_slots+1`。
2. 同时 W1 终于报来 `succeeded`（迟到更新）→ 部件 1 状态机挡住（`queued→succeeded` 非法，`ErrIllegalTransition`）→ 不落终态 → **不会释放 slot**。✓ 正确，因为 reclaim 已释放过。

但如果终态先到、reclaim 后到呢？
1. W1 报 `succeeded` → `OnUpdate` 终态分支 → `W1.free_slots+1`、清 `lease_expires_at`。
2. reaper 扫到这个任务？此时 `status='succeeded'`，`Reclaim` 的 `WHERE status='running' AND lease_expires_at<now()` 匹配 0 行 → 不 reclaim → **不会重复释放**。✓ 正确。

关键：**释放 slot 的动作必须和「能成功改状态」绑定**。即：

- 终态释放：只在 `SetStatus` 终态写入**成功（rowsAffected>0）**时才 `free_slots+1`。若 0 行（任务已被别人定性），不释放。
- reclaim 释放：只在 `Reclaim` 的 `UPDATE` 成功（rowsAffected>0）时才 `free_slots+1`。

这样两个释放路径各自的自守卫保证「只有真正改了状态的那个才释放」，不会双释放。**这条规则归部件 2 定义**（因为 slot 是部件 2 的镜像），但执行点分散：终态释放挂在 `OnUpdate` 终态分支（部件 1 的入口里调部件 2 的 `releaseSlot`），reclaim 释放挂在 `Reclaim`（部件 1）。部件 2 提供一个 `releaseSlot(ctx, workerID) error`（`UPDATE workers SET free_slots=free_slots+1 WHERE id=$w AND free_slots<concurrency`，带 `free_slots<concurrency` 上界防溢出），部件 1 在自守卫成功后调用它。

**slot 上界守卫**：`free_slots+1` 时 `WHERE free_slots < concurrency`，防止计数漂移导致 `free_slots` 超过 `concurrency`（理论上不该发生，但心跳校准、并发释放的边界情况下可能，加守卫兜底）。

**心跳校准**（部件 3 执行，部件 2 定义规则）：worker `Heartbeat` 上报 `free_slots` 时，master 不直接信任上报值覆盖 DB，而是用作校准：`UPDATE workers SET free_slots = LEAST($reported, concurrency - running_tasks), last_seen=now() WHERE id=$w`。`free_slots` 的权威是 claim/释放的原子计数，上报只纠偏（比如 worker 重启后 running_tasks 归零，上报 `free_slots=concurrency`，校准把它拉回正确）。

## 4. 落到哪些文件

| 文件 | 改动 |
|---|---|
| `internal/repository/tasks.go` | 新增 `ClaimOne(ctx, taskID int64, opts ClaimOpts) (*ClaimedTask, error)`（事务内锁任务行+选 worker 行+改状态+扣 slot）；`MarkAssignSent`；`ListAssignPendingForWorkers(ctx, workerIDs []string) ([]*Task, error)`（补发扫描）；`ListClaimableIDs(ctx) ([]ClaimableRow, error)`。`SetStatus` 返回 `rowsAffected`（部件 1 已定）。 |
| `internal/repository/workers.go` | `releaseSlot` 的 SQL 可放此处（`AdjustSlots(ctx, workerID, delta int)`）；`ListOnlineForClaim` 已在部件 1 Phase 1。 |
| `internal/task/service.go` | `dispatchOnce` 重写为 claim 循环 + spider LRU + 补发扫描；`Hub` 接口改为 `HasHandle`/`PushAssign`/`PushCancel`；`buildAssign` 接受预查的 spider；`Cancel` 改为 `OnUpdate(cancelled)`+`PushCancel`；新增 `releaseSlot` 调用点（终态/reclaim 后，自守卫成功才调）。 |
| `internal/hub/hub.go` | 删 `Assign`、`tasks` map、`releaseTask` 对全局 map 的操作；加 `workerID→*Session` 索引（部件 3 维护，部件 2 用）；加 `HasHandle`/`PushAssign`/`PushCancel`。 |
| `db/migrations/00010_workers.sql`、`00011_task_lease.sql` | 部件 1 已认领；部件 2 依赖其 `workers.free_slots`/`capabilities`、`tasks.lease_expires_at`/`assign_sent`。无新迁移。 |
| `internal/task/service_test.go` + `repository/tasks_test.go` | claim 并发（两 `dispatchOnce` 同一 queued 集 → 无双派）；能力过滤；least-loaded；`ErrNoWorker`/`ErrNoClaimableTask`；补发扫描；双释放防护（终态+reclaim 竞态只释放一次）；slot 上界守卫。 |

> `Hub` 接口改签名会影响 `app.go` 接线和 hub_test.go，但接线改动小（部件 2 一并改）。

## 5. 取舍（已定）

### Q2/Q1 — spider 查询：每轮批量预查（已定）

`dispatchOnce` 每轮 `ListClaimableIDs` 拿 `(task_id, spider_id)` 列表 → 一次 `WHERE id=ANY(...)` 批量查 spider 进 map → 循环里从 map 取。**不做 LRU**（Q1 的 LRU 方案废弃）。

理由：每轮已经是批量一次查询，LRU 无额外收益；批量预查无缓存失效问题、每轮数据最新、实现更简单。spider 编辑后下一轮派发自然查到新值（`ListClaimableIDs` 每轮重查）。

**部件 2 落点**：`dispatchOnce` 开头批量预查 spider；`buildAssign` 接受预查的 spider（不重查）。

### Q3 — `ErrNoWorker` 派发节奏：保持 5s tick + wakeup（已定）

所有 worker 忙时 `dispatchOnce` 收到 `ErrNoWorker` 结束本轮，等下一个 5s tick 或 wakeup。不引入指数退避。

理由：worker 释放 slot（终态/reclaim）时部件 2 已 `notify()` wakeup，所以「worker 空出来」会立刻触发派发，不必等 tick；`ErrNoWorker` 的空跑只在「无 worker 释放」时发生，开销可忽略。退避复杂度不值得。

**部件 2 落点**：`RunDispatcher` 保持现有 5s tick + wakeup 结构不变。

### Q4 — caps 匹配：精确匹配 + 日志区分（已定）

`required_caps` 与 `capabilities` 精确字符串匹配（`python3.12` 只匹配 `python3.12`）。`ErrNoWorker` 不细分原因（保持 sentinel 简单），但 `ClaimOne` 在「有 online worker 但都 caps 不匹配」时 `slog.Info` 一行（区别于「无 online worker」），帮助排查「任务卡 queued」。版本化语义（`python3.12+`）留后续。

理由：现阶段精确匹配足够；可观察性靠日志而非错误细分，避免 `ErrNoWorker` 派生类型膨胀。版本化是未来优化，不预做。

**部件 2 落点**：`ClaimOne` 内选 worker 时，若 `WHERE status='online'` 有行但加 `caps<@` 后无行 → `slog.Info("claim: workers online but none match caps", "task", taskID, "required_caps", opts.RequiredCaps)` 后返回 `ErrNoWorker`。

### Q5 — 单副本退化验证（测试约束，非取舍）

部件 2 所有机制在单副本下退化为「等价于 v1 但更优」：`SKIP LOCKED` 无竞争、worker 行 `FOR UPDATE` 无竞争、`assign_sent` 补发在持句柄副本（=本副本）立即完成。单副本退化等价是设计约束（DESIGN-v2 §11），**测试必须覆盖**单副本场景，确保不引入额外延迟/复杂度。

**部件 2 落点**：测试清单含单副本 claim/补发/释放用例。

---

部件 2 的**设计骨架**（逐个 claim、事务内锁 worker 行、least-loaded+能力感知、spider 批量预查、assign_sent+扫描补发、hub 降级传输层、slot 成对释放+双释放防护）与上述已定取舍一致。共同切分原则同部件 1：**部件 2 定派发决策与 slot 计量规则，跨部件执行体（worker 句柄索引、reaper、心跳校准）留给部件 3**。
