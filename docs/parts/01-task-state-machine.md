# 部件 1：任务对象与状态机 — 设计

> 这是 crawler-lite 拆分后的第一块。讨论范围限定在「任务是什么、状态怎么转、谁有权转、重试怎么决定、终态顺序」，不碰派发、worker 会话、日志、调度、API。那些是后续部件。
>
> 配套阅读：`docs/DESIGN.md` 第 4 章（核心抽象）、`docs/DESIGN-v2.md` 第 3 章（registry/lease）、`internal/task/service.go`、`internal/task/retry.go`、`db/migrations/00003_tasks.sql`、`00008_task_retry.sql`。

## 0. 本部件的边界

**包含：**
- `Task` 对象的字段与语义。
- 状态枚举与**显式状态转移图**（含非法转移校验）。
- 状态推进的**唯一入口** `OnUpdate` 及其顺序铁律。
- 重试策略（纯函数，已存在，本部件只确认其与状态机的关系）。
- 租约（`lease_expires_at`）与 reaper 带来的**时间驱动终态**：定义 `running→queued` 的 reclaim 转移。
- captcha 的人工解除转移 `captcha_blocked→queued`。
- `dispatchOnce` 里 `build_assign` 失败写入的**收拢**进 `OnUpdate`。

**不包含（留给后续部件）：**
- 派发怎么挑 worker、claim 事务怎么做 → 部件 2。
- worker 怎么连、心跳怎么续租、reaper goroutine 放哪、谁跑 → 部件 3（reaper 的执行体）+ 部件 2（claim 写 lease）。
- `OnUpdate` 之外的日志/结果流 → 部件 5。
- 调度产任务 → 部件 6。
- API 怎么暴露这些转移 → 部件 7。

部件 1 只定义状态机的**规则与入口**；租约的「写入」在部件 2 的 claim 里，租约的「续/回收执行」在部件 3。但转移 `running→queued(reclaim)` 的**合法性**归部件 1 定义。

---

## 1. 现状回顾

- 状态枚举（DB `task_status`）：`queued, running, succeeded, failed, cancelled, timeout, captcha_blocked`。前两个非终态，后五个终态。
- 推进入口：`task.Service.OnUpdate(ctx, taskID, status, errMsg, errClass, workerID)`。顺序：落库 →（终态）重试 →（终态）通知。
- `SetStatus`（`repository/tasks.go:147`）**无条件写入**：`UPDATE tasks SET status=$2 ... WHERE id=$1`，无 `WHERE status NOT IN (terminal)` 守卫。`started_at`/`finished_at` 由 SQL 的 `CASE` 自动打。
- 重试：`retry.go` 纯函数，`PolicyFromSpiderConfig` + `Decide(attempt, errClass)`。captcha 硬排除，`errClass=""` 也不重试。
- 第二个推进点：`dispatchOnce` 在 `buildAssign` 失败时直接 `SetStatus(..., StatusFailed, ..., "build_assign")` + `maybeScheduleRetry`，绕过 `OnUpdate`（无通知）。

## 2. 上限（本部件要解的）

1. **状态转移图隐式** — `OnUpdate`/`SetStatus` 接受任何 status 就写，无非法转移概念。
2. **终态可被覆盖** — `SetStatus` 无条件，迟到 worker 更新能覆盖已定终态。
3. **第二推进点** — `dispatchOnce` 的 build_assign 写入绕过 `OnUpdate` 顺序保证。
4. **无时间驱动终态** — worker 静默死亡 / master 崩溃时任务卡 `running`，状态机没有自救路径。
5. **captcha 是死路** — 终态无出路，无人工解除入口。

---

## 3. 目标设计

### 3.1 显式状态转移图

在 `internal/task` 里建一张显式合法转移表，`OnUpdate` 与 `SetStatus` 都经它校验。

```go
// legalTransitions: from-status → set of allowed to-statuses.
var legalTransitions = map[Status]map[Status]struct{}{
    StatusQueued: {
        StatusRunning:           {}, // 派发 claim（部件2）
        StatusFailed:            {}, // build_assign 失败（收拢进 OnUpdate）
        StatusCancelled:         {}, // 排队中取消
    },
    StatusRunning: {
        StatusSucceeded:         {},
        StatusFailed:            {},
        StatusCancelled:         {}, // 用户取消（master 权威）
        StatusTimeout:           {}, // worker 报超时
        StatusCaptchaBlocked:    {},
        StatusQueued:            {}, // reclaim：租约到期回收重派（部件2/3 写，部件1 定合法性）
    },
    StatusCaptchaBlocked: {
        StatusQueued:            {}, // 人工解除（操作员标记后重排）
    },
    // succeeded / failed / cancelled / timeout：终态，无合法转出。
    // 任何对终态任务的 status 写入都是 ErrIllegalTransition，
    // 除非走显式的「重新入队」路径（captcha 解除 / reclaim），那是从终态/running 出发的合法边。
}
```

注意 `failed`/`timeout`/`cancelled`/`succeeded` 是**真终态**：没有合法转出。「重试」不是状态转移——它是 `OnUpdate` 在落终态后**创建一个 attempt+1 的子任务**（已有逻辑），父任务留在终态。这保持不变。

`ErrIllegalTransition = errors.New("illegal status transition")` 作为新 sentinel，在 handler 层映射到 409 Conflict（部件 7）。

### 3.2 `OnUpdate` 成为唯一推进入口（含收拢）

`dispatchOnce` 里 `build_assign` 失败的处理改为调用 `OnUpdate(ctx, t.ID, StatusFailed, err.Error(), "build_assign", "")`，而不是直接 `SetStatus` + `maybeScheduleRetry`。这样：

- build_assign 失败也走「落库 → 重试 → 通知」三步。
- 重试策略里 `build_assign` 已在 `retryableClasses` 白名单内（见 `retry.go`），所以「源码没同步好、下一次同步后自愈」的重试语义自动生效。
- **消灭第二推进点**，恢复单一入口原则。

`OnUpdate` 自身加转移校验：在 `SetStatus` 前查 `legalTransitions`。非法转移返回 `ErrIllegalTransition`，**不落库**。但有一个例外见 3.4。

### 3.3 终态不可覆盖（`SetStatus` 加守卫）

`repository/tasks.go` 的 `SetStatus` 加 `WHERE` 守卫，让 DB 层也挡一次（进程内校验 + DB 守卫双保险，跨副本时 DB 守卫是唯一防线）：

```sql
UPDATE tasks
SET status = $2::task_status, ...
WHERE id = $1
  AND (status NOT IN ('succeeded','failed','cancelled','timeout','captcha_blocked')
       OR $2::task_status = 'queued')  -- 仅 reclaim/解除允许从终态/running 回 queued
```

返回 `rowsAffected`，0 行表示「目标已不在可转移状态」（迟到更新或竞态）。`OnUpdate` 据此判断：若终态写入 0 行 → 说明任务已被别人定性，**静默接受**（迟到 worker 更新被丢弃，正是想要的行为），不报错、不重试、不通知。

### 3.4 时间驱动终态：reclaim 转移

**reclaim 是什么**：master 把一个卡在 `running`、但实际已经没人管的「孤儿」任务，回收回 `queued` 的动作。一句话——「我以为这个 worker 在跑，但它没动静了，我把任务拿回来重新排队。」

**要解决的问题**：v1 里任务进入 `running` 后只能靠 worker 主动发 `TaskUpdate` 推进。worker 崩了 / 被 OOM / 卡死 / 断网 / master 自己崩了重启 —— 没人发 `TaskUpdate`，任务永远卡 `running`，成为孤儿。timeout 是 worker 侧 `context.WithTimeout` 执行的，worker 得活着才能报 timeout；worker 死了，timeout 也没人报。reclaim 就是处理孤儿的兜底机制。

**判定依据——租约（lease）**：派发时 master 给任务一个到期时间 `lease_expires_at = now() + timeout_s + grace`，含义是「授权这个 worker 跑到 T 时刻，T 后就认为没人管」。worker 健康运行期间周期性心跳，master 收到心跳就**续租**（把到期时间往后推）。只要 worker 在跑、在心跳，租约一直在未来，归属不变。worker 一旦不再心跳 → 没人续租 → 租约自然到期 → 成为孤儿。失联判定靠「没人续租」，不靠任何「worker 死了」的显式事件。

**执行体（部件 3 实现，部件 1 只定规则）**：reaper goroutine 周期性扫 `status='running' AND lease_expires_at < now()`，对每个孤儿执行 `Reclaim`：

```sql
UPDATE tasks
   SET status = 'queued', worker_id = NULL, lease_expires_at = NULL, assign_sent = false
 WHERE id = $taskID AND status = 'running' AND lease_expires_at < now()   -- 自守卫
```

成功后：旧 worker 的 `free_slots++`（释放幽灵槽位）、若 `workers.last_seen` 也过期则标该 worker `offline`、`notify()` 唤醒派发。**reclaim 不重试、不通知**——任务没结束，告诉用户「失败了」是错的。

**状态机入口**：`Reclaim` 走 `OnUpdate` 的 `queued` 特例分支——只落库 + 唤醒派发，不重试不通知。仍是单一入口，入口内按目标状态分流。

**为什么 reclaim 回 `queued` 而非 `timeout`**：master 看到租约到期时无法确知 worker 是真超时、还是早崩了、还是只是网络抖一下马上会续租。回 `timeout`（终态）会误杀一个其实马上要报 `succeeded` 的任务；回 `queued` 更安全——重新排队让重试策略决定下一步，真崩就攒够失败次数落 `failed`，抖动就重派成功 `succeeded`，不误杀。

**reclaim vs 重试的区别**：

| | 重试 (retry) | reclaim |
|---|---|---|
| 触发 | 任务落了失败终态（`failed`/`timeout`）后 | 任务卡在 `running`、租约到期 |
| 做什么 | 创建 `attempt+1` 的新子任务，父任务留终态 | 把同一个任务从 `running` 改回 `queued` |
| 父任务状态 | 终态不变 | 变回 `queued`（非终态） |
| 次数推进 | `attempt++` | `attempt` 不变（同一次 attempt 换 worker 重跑） |
| OnUpdate 分支 | 终态分支：重试 + 通知 | `queued` 分支：只唤醒派发 |

关键：reclaim **不算一次失败**，不推进 `attempt`，不触发重试策略，不通知用户。它只是「换个 worker 把同一次 attempt 跑完」。

**timeout 双轨**：worker 活着、任务真跑超时 → worker 自己 `context.WithTimeout` 触发报 `timeout`（终态），走重试（正常超时路径）；worker 死了/卡了、没人报 → 租约到期 reclaim 回 `queued`（兜底路径）。两条不冲突：前者是「我知道超时了，判终态」；后者是「没人告诉我，先重排试试」。reclaim 不抢 timeout 的活，只在 timeout 没人报时兜底。

**续租的时间常量**：派发时租约初值 = `now() + timeout_s + grace`；每次心跳续租都推到 `now() + timeout_s + grace`（重置完整窗口，语义是「worker 还活着，任务超时窗口重新计」——只要 worker 在心跳，任务就不会因超时被 reclaim，不管跑多久）。`timeout_s` 是任务级（spider 配，默认 600s）；心跳间隔和 grace 是系统级。

**reaper 扫描**：reaper 间隔独立于心跳（建议 ~10s）。`Reclaim` 的 SQL 带 `WHERE status='running' AND lease_expires_at < now()` 自守卫——多副本同时跑 reaper 时天然互斥，只有一个 `UPDATE` 能匹配到 `running` 行。reclaim 成功后给旧 worker `free_slots++`，并检查 `workers.last_seen` 是否也过期（整个 worker 失联则标 `offline`，后续别再派给它——worker 级回收与任务级 reclaim 互补）。

**竞态与状态机的价值**：任务正常完成（报 `succeeded`）与 reaper 扫到要 reclaim 可能竞态。两者都带自守卫改同一行，谁先提交谁赢。关键坑：若 reclaim 先赢（行变 `queued`），迟到的 `succeeded` 报上来时，DB 终态守卫挡不住（`queued` 不是终态），会把 `queued` 改回 `succeeded`。**这正是显式状态机比自由 enum 的价值**——`OnUpdate` 进程内查 `legalTransitions`，`queued→succeeded` 不在合法表里，返回 `ErrIllegalTransition` 丢弃迟到更新。自由 enum + DB 守卫挡不住这条，因为 `queued` 不是终态。

（现阶段简化：续租的 grace 和 reclaim 后的目标状态是固定值；后续开发按 spider 配置的 retry 策略决定，并把「被回收」作为一种可重试的 err_class。reclaim 后 `started_at` 是否重置、reclaim 次数是否封顶——见 §6 待选。）

### 3.5 captcha 人工解除

新增转移 `captcha_blocked→queued`。这是一个**显式的人为动作**，不是自动的：

- 新方法 `ResolveCaptcha(ctx, taskID)`（在 `task.Service`），由操作员/API 触发（部件 7 暴露端点）。
- 它走 `OnUpdate` 的 `queued` 特例分支：落库 `captcha_blocked→queued`（清 error/error_class）→ 唤醒派发 → 不通知。
- 不自动重试——操作员解除意味着「 captcha 已处理（换了代理/补了 cookie/人工过了），现在重跑」。下一次跑若再遇 captcha，照样落 `captcha_blocked` 等再次解除。

这给 captcha 一条出路，但保持「自动重试硬排除 captcha」不变（`retry.go` 的 `Decide` 对 `captcha` 返回 false 仍然成立——自动路径碰 captcha 落终态；只有人能把它救回 queued）。

### 3.6 字段语义补全

- `exit_code`：worker 终态时通过 `TaskUpdate` 带回（proto 需加字段，部件 4 落地；部件 1 只在 `OnUpdate` 签名里预留 `exitCode *int` 参数，0 值/nil 不覆盖）。
- `stats`：worker 在终态 `TaskUpdate` 里带回 `{items, logs, screenshots, duration_s}` 等（部件 4/5 填充）；`OnUpdate` 把它 merge 进 `stats` 列。部件 1 定义 `OnUpdate` 接受一个 `stats map[string]any` 参数（nil 表示不更新）。
- `OnUpdate` 新签名：
  ```go
  OnUpdate(ctx, taskID int64, status Status, errMsg, errClass, workerID string, exitCode *int, stats map[string]any) error
  ```
  hub 的 `readLoop`（部件 3）适配新签名。

### 3.7 顺序铁律（不变，仅明确 reclaim/解除的位置）

```
OnUpdate(taskID, status, ...):
  1. 校验转移合法性（legalTransitions）→ 非法返回 ErrIllegalTransition
  2. SetStatus（带 DB 终态守卫）→ 0 行 = 已被定性，静默返回 nil
  3. 按 status 分流：
     - queued (reclaim/解除/cancel-from-queued): 唤醒派发，return   ← 不重试不通知
     - 终态 succeeded/cancelled:                                ← 不重试，走通知
     - 终态 failed/timeout: maybeScheduleRetry，再通知
     - 终态 captcha_blocked:                                    ← 不重试，走通知
  4. 通知（若有）在 detached goroutine + context.Background()
```

reclaim 和 captcha 解除是「回 queued」，天然跳过重试/通知；失败/超时/captcha 是终态，走原有重试+通知。succeeded/cancelled 走通知不重试。**单一入口内分流，不新增入口。**

---

## 4. 落到哪些文件

| 文件 | 改动 |
|---|---|
| `internal/task/status.go`（新） | `legalTransitions` 表、`IsTerminal()`、`CanTransition(from,to)`、`ErrIllegalTransition`。从 `service.go` 抽出，集中。 |
| `internal/task/service.go` | `OnUpdate` 加转移校验 + 终态守卫 0 行处理 + `queued` 分流；签名加 `exitCode *int, stats map[string]any`；新增 `Reclaim`、`ResolveCaptcha`（都走 `OnUpdate`）；`dispatchOnce` 的 build_assign 失败改调 `OnUpdate`。 |
| `internal/repository/tasks.go` | `SetStatus` 加终态守卫 `WHERE` + 返回 `rowsAffected`；`Reclaim` 的 SQL（`running→queued` 原子，带 `lease_expires_at < now()` 自守卫，清 worker_id/lease/assign_sent）；`ClearLease`（终态时清租约）。 |
| `db/migrations/00011_task_lease.sql` | 加 `lease_expires_at`、`assign_sent` 列 + `idx_tasks_running_lease`。（与 v2 计划 Phase 2 同一份迁移，部件1先把它认领。） |
| `internal/task/retry.go` | 不动。`build_assign` 已在白名单，收拢后自动适配。 |
| `internal/task/service_test.go` + `status_test.go` | 转移图全覆盖：合法/非法转移、终态覆盖被挡、reclaim、captcha 解除、build_assign 收拢走通知。 |

> proto 的 `TaskUpdate` 加 `exit_code`/`stats` 字段属于部件 4 的 IPC 变更，部件 1 只在 `OnUpdate` 签名预留参数，暂传 nil/空，不阻塞部件 1 落地。

## 5. 时间常量（关系固定，取值待选）

reclaim 机制依赖三个时间常量，它们之间有一个**硬约束**，取值可后选：

```
grace  >=  2 × heartbeat_interval
```

理由：心跳是「worker 还活着」的证明，但心跳会丢包/延迟。若 grace 只有一个心跳间隔，一次心跳丢了租约就到期，正常任务被误 reclaim。`grace >= 2 × 心跳间隔` 保证至少连续丢 2 次心跳才认定失联，单次抖动不误杀。

| 常量 | 建议取值 | 谁配 | 说明 |
|---|---|---|---|
| 心跳间隔 | ~5s | 系统级（worker config） | worker 每 N 秒发一次 Heartbeat |
| grace | ≥ 2×心跳间隔，建议 60s | 系统级（master config） | 吸收「完成→上报」延迟 + 抗心跳丢包 |
| reaper 扫描间隔 | ~10s | 系统级（master config） | 独立于心跳，决定孤儿被发现的速度 |
| timeout_s | spider 配，默认 600s | 任务级 | spider 配置里已有 |
| 租约初值/续租推到 | `now() + timeout_s + grace` | 派发时算 | 每次 heartbeat 续租都重置到完整窗口 |

这些**取值**留待后续选择（见 §6 Q3）；**关系**（grace ≥ 2×心跳）是设计约束，不可违反。

## 6. 待后续选择的取舍

以下问题**不在本轮定稿**，留待后续逐个选择。每题给出选项与影响，选完即可推进对应实现。文档此处作为登记，避免遗漏。

### Q1 — reclaim 后 `started_at` 要不要重置？

> **现状澄清**：`SetStatus` 的 SQL 是 `CASE WHEN status='running' AND started_at IS NULL THEN now()`——只有**首次**进 running 才打 `started_at`，后续 reclaim 回 queued 再进 running 因 `started_at` 已非 NULL 不会重打。所以 v1 现状实际就是「保留首启时间」，只是 v1 无 reclaim，该行为从未被检验。本质问题：`started_at` 服务于「这次 attempt 何时开始」还是「这条任务链何时开始」——一个列塞两个量必然顾此失彼。

两个选项的真实影响：

- **保留**（不改 `Reclaim` 的 `started_at`）：`started_at` = 任务最早启动时间，跨多次 reclaim 不变。**硬伤**：`duration_s` 若用 `finished_at - started_at` 算，会**包含所有 reclaim 空转时间**——一个实际跑 30s 但被回收耗了 10 分钟的任务，duration 显示 10 分钟，stats 失真。
- **重置**（`Reclaim` 里 `started_at = NULL`，下次 running 重打）：`started_at` = 当前 attempt 启动时间，`duration_s` 干净。代价：丢「链首启时间」历史信息。

**建议取向 A（推荐）：两量分离。** 新增 `first_started_at TIMESTAMPTZ`（`00011`），首次进 running 打一次、reclaim 不重置，承载「链首启」语义；`started_at` 退化为「当前 attempt 启动」，每次 reclaim 重置。`SetStatus` 的 `CASE` 同时维护两列。两量各归其位，不互相污染，且部件 1 自己就把时长算对，不依赖 IPC。

**建议取向 B（退路，零迁移）：保留 `started_at` 首启不重置，`duration_s` 改由 worker 终态报真实执行时长**，不靠 `finished_at - started_at` 算。两全且不加列，但 `duration_s` 依赖部件 4 的 stats 落地（与 Q4 耦合）。

**倾向 A**：让部件 1 自洽，不把时长正确性拴在 IPC 上。若偏好尽量不加列，B 也成立。

**部件 1 本轮做（按 A）**：`00011` 加 `first_started_at`；`Reclaim` 的 SQL `started_at = NULL`；`SetStatus` 的 `CASE` 维护两列；`Task` 结构体加字段。
**留给后续**：UI 用哪个字段展示（部件 7）。

### Q2 — reclaim 次数要不要封顶？

> **现状澄清**：reclaim 不推进 `attempt`，而 `max_attempts` 只管「失败后重试」、不管「reclaim 后重派」。所以 `max_attempts=3` **管不住** reclaim——一个任务可 attempt=1 但被 reclaim 100 次。这是 reclaim 相对重试的**治理盲区**，封顶正是补它。least-loaded 派发（部件 2）能部分缓解「无限派给同一坏 worker」，但「所有 worker 都崩」或「spider 本身每次跑超时」时仍需封顶让故障进人视野。

**建议取向：封顶，用独立计数器，不和 `attempt` 混。**

- 新增 `reclaim_count INTEGER NOT NULL DEFAULT 0`（`00011`）。
- `Reclaim` 时 `reclaim_count = reclaim_count + 1`。
- 阈值 `MAX_RECLAIMS` 默认 3（系统级 config，不进 spider 配置——这是平台健壮性参数）。
- `reclaim_count >= MAX_RECLAIMS` 时 `Reclaim` 不回 `queued`，改落 `failed` + `err_class="reclaim_exhausted"`，走 `OnUpdate` 终态分支（触发重试 + 通知）。
- **`reclaim_exhausted` 建议加进 `retryableClasses` 白名单**：reclaim 耗尽多属环境/瞬时问题（worker 池不健康、偶发慢），非 spider 代码错，符合「值得重试」语义；由 `max_attempts` 接管。若偏好「坏任务不反复消耗资源」，可不加白名单，一旦 `reclaim_exhausted` 即 `failed` 不重试——更严格。
- 任务终态（succeeded/failed/timeout）时 `reclaim_count` 清零（终态后不重要）；reclaim 回 queued 时不清零（要累积）。

**退路（不封顶）**：依赖 least-loaded + `workers.last_seen` 过期标 offline 兜底，但「attempt 管不住 reclaim」盲区仍在。不推荐作默认，可作「先观察一阵」过渡。

**部件 1 本轮做**：`00011` 加 `reclaim_count`；`Reclaim` SQL `+1` + 阈值分支（超阈值落 `failed`/`reclaim_exhausted`）；`retry.go` 白名单加 `"reclaim_exhausted"`（建议）。
**留给后续**：`MAX_RECLAIMS` 具体值（随 Q3 定 config 默认）。

### Q3 — 时间常量取值

> 关系已定（`grace >= 2×心跳间隔`，见 §5），此处定取值与 grace 是否自适应。

**建议取向：固定默认值，不做自适应。**

| 常量 | 建议默认 | 理由 |
|---|---|---|
| 心跳间隔 | 5s | v1 量级下足够及时，再短徒增 gRPC 流量 |
| grace | 60s | ≥ 2×5=10s 留足余量；吸收「完成→上报」延迟（正常 <1s，60s 极宽裕） |
| reaper 扫描间隔 | 10s | 孤儿被发现中位延迟 ~5s，可接受 |
| `MAX_RECLAIMS` | 3 | 给「换到健康 worker」足够机会但不无限 |

**不做自适应 grace**：`grace = max(固定, 2×心跳)` 在心跳运行时动态变化时才有意义，本系统心跳间隔是固定 config 不会运行时变——自适应退化为固定值，多此一举。未来心跳可调再加。

**配置落点**：系统级，加 `internal/app/config.go`（`LeaseGraceSeconds=60`、`ReaperIntervalSeconds=10`、`MaxReclaims=3`）和 `internal/workerapp/config.go`（`HeartbeatIntervalSeconds=5`）。**给默认值、不 `,required`**，单副本开发零新 env 即可跑。

**部件 1 本轮做**：config 里声明这些键 + 默认值（声明即可，接线在部件 2/3 真正用到时）；`Reclaim` 用 `MAX_RECLAIMS`（与 Q2 一起）。
**留给后续**：reaper/心跳 goroutine（部件 3）接线时读取这些 config。

### Q4 — `exit_code` / `stats` 字段的填充时机

> **现状澄清**：`exit_code` 和 `stats` 两列在 `00003` 迁移里**已存在**（`exit_code INTEGER` 可空、`stats JSONB DEFAULT '{}'`），但当前 `OnUpdate` 签名没有这两个参数、`SetStatus` SQL 不写它们、`TaskUpdate` proto 也没带——属于「有 schema、无写入路径」，字段一直空着。所以这是「已存在字段谁来填、何时填、怎么传」的问题，不是「要不要加字段」。

三个选项：

- **A — 部件 1 完全不动，留给部件 4**：`OnUpdate` 签名加参数但本阶段所有调用方传 nil/`{}`，`SetStatus` 不写两列，proto 不改。边界最纯，但 `SetStatus` 会被部件 1 和部件 4 改两次，状态机相关 SQL 散在两部件。
- **B — 部件 1 顺手接通 `SetStatus` 写入，proto 暂不改（建议）**：`OnUpdate` 加 `exitCode *int, stats map[string]any`；`SetStatus` SQL 加 `exit_code = COALESCE($exit, exit_code)`、`stats` **整体替换** `stats = $stats::jsonb`（仅在非 nil 时覆盖，nil 不动现有值）；proto 仍不改，本阶段无人传非 nil。部件 4 落地 proto 字段时只需在 `readLoop` 取字段传进 `OnUpdate`，`SetStatus` 已会写。
- **C — 部件 1 连 proto 一起改**：跨部件边界，把 IPC 变更拉进本轮，违背切分初衷。

**建议取向：B。** 理由：把「`SetStatus` 如何处理 `exit_code`/`stats`」在部件 1 一次定死（`COALESCE` + 整体替换 + nil 不覆盖的测试），部件 4 只管「把 proto 字段取出来传进去」。边界切在 `OnUpdate` 签名上——状态机对两字段的写入规则归部件 1，IPC 传输归部件 4。

**`stats` 合并 vs 整体替换**：建议**整体替换**。stats 是终态时一次性报的快照，不是增量流；合并（`stats || $stats`）引入「谁覆盖谁」的隐式行为，此场景无必要。worker 终态报一次完整 stats，简单可预期。

**迁移成本**：`exit_code`/`stats` 列已存在、约束已合适，选 B **不需要新迁移**，只改 `SetStatus` SQL。零迁移成本也支持选 B。

**部件 1 本轮做**：`OnUpdate` 加两参数；`SetStatus` SQL 加列写入 + `COALESCE`/整体替换；测试「传 nil 不覆盖现有值」。
**留给后续**：部件 4 给 `TaskUpdate` proto 加 `exit_code`/`stats_json` 字段 + `make gen` + hub `readLoop` 取字段传进 `OnUpdate`。

### Q5 — captcha 解除的权限与审计

`ResolveCaptcha` 由操作员触发（§3.5）。与其他转移不同，这是**带业务判断的人为干预入口**：解除前提是操作员在 spider 之外做了事（换代理、补 cookie、人工过验证码、调访问频率），且解除可能被滥用（不合格的解除让任务反复撞 captcha、浪费 worker、甚至触发目标站风控）。因此需要权限与审计治理。

**权限——三个层级：**

- **1 — 仅 admin 角色（建议默认）**：只有管理员能解除，普通用户在 UI 看到任务但「解除」按钮不可用。最克制，逼出一条「captcha 处理流程」（操作员处理完由有权限者解除）。从紧到松容易，从松到紧难。
- **2 — 任务创建者 + admin**：贴近「自己的任务自己管」，减少管理员瓶颈；但创建者未必有动代理池等处理能力，解除后任务仍可能跑不过，等于把「浪费资源」决策权下放。
- **3 — 任意认证用户**：最简单，滥用风险最高，不推荐。

**建议取向：1（仅 admin）默认**。权限判断在部件 7 API handler 做，几乎不影响部件 1——`ResolveCaptcha` 签名加 `actorUserID int64`（无论谁调都传操作者），部件 1 只负责「接受 actor、落转移」。

**审计——要不要记、记什么、记哪：**

- **要记**：captcha 解除是少数「人为改变任务状态」的点之一（另一个是用户取消，但属常规操作），值得留痕。
- **记什么**：`{task_id, actor_user_id, action: "captcha_resolved", at: now()}`（`note` 等自由文本是 UI 层的事，部件 1 审计只需结构化字段）。
- **记哪——三选项：**
  - **a — 复用 `tasks` 表加列**（`captcha_resolved_by`/`captcha_resolved_at`）：查询简单，但把审计塞进事实表，`tasks` 列膨胀，且只覆盖一种人为动作，未来 cancel-by-user/force-fail 要各加两列，不可扩展。
  - **b — 独立 `task_events` 审计表（建议，但不在部件 1 本轮建）**：`task_events(id, task_id, actor_user_id, action, detail, at)`，`action ∈ {captcha_resolved, cancelled_by_user, ...}`。可扩展，天然是「任务状态变更日志」，但需新表 + 新 repo + 查询 JOIN。
  - **c — 部件 1 不做审计，留给部件 7**：`ResolveCaptcha` 只落转移，API handler 自己写审计。边界最纯，但审计逻辑散在 API 层各调用点，易漏。

**建议取向：b，但不本轮建。** 审计表是跨部件公共设施（cancel、force-fail、captcha 解除都要用），不该归部件 1 独占，更像独立的「审计」横切关注点。部件 1 本轮 `ResolveCaptcha` **只落转移 + 接受 `actorUserID`**，不写审计；审计表建立与写入留给后续（部件 7 或单开审计小部件）。部件 1 预留 `actorUserID` 参数即可，后续审计写入只需在调用链加一步，不用回头改部件 1。

**解除后 `attempt` 语义（建议一并确认）：** 建议**保留 `attempt` 不变**。captcha 不是失败，`attempt` 在 captcha 路径上从未推进过（`retry.go` 对 captcha 返回 false，不建 attempt+1 子任务）；captcha 任务停在 `captcha_blocked` 时 `attempt` 就是当初被派出去的值（通常 1）。解除回 queued 后继续用此值，语义一致；重置会引入「解除=新任务」的错觉。`ResolveCaptcha` 不动 `attempt`。

**部件 1 本轮做**：`ResolveCaptcha(ctx, taskID, actorUserID)` 签名加 `actorUserID`；落 `captcha_blocked→queued` 转移，清 error/error_class，不动 `attempt`。
**留给后续**：部件 7 做角色判断（仅 admin）；审计表 `task_events` 的建立与写入（独立小部件或部件 7）。

### Q6 — `ErrIllegalTransition` 的 HTTP 映射

`ErrIllegalTransition` 是部件 1 导出的 sentinel，HTTP 状态码与响应体归部件 7 render 层。

**建议取向：409 Conflict，响应体带「当前实际状态」。**

- **状态码 409 而非 422**：422 = 请求体格式/语义错（缺字段、非法 JSON，「你发的东西本身不对」）；409 = 请求本身没问题但与**服务器当前状态冲突**（「任务现在不是你能转的那个状态」）。非法转移正是 409 典型场景（「我想取消，但它已 succeeded」）。与现有 render 映射同档：`ErrInvalidInput`→400、`ErrNotFound`→404，状态冲突补 409。
- **响应体带当前实际状态**：`{"error":{"code":"illegal_transition","message":"...","current_status":"succeeded"}}`，前端可给精准提示。需 sentinel 携带 `from`/`to`——构造为 `fmt.Errorf("illegal transition %s→%s: %w", from, to, ErrIllegalTransition)`，handler 在 render 层 parse。
- **render 层映射**（部件 7）：`errors.Is(err, task.ErrIllegalTransition)`→409 + 带状态。

**调用方对 `ErrIllegalTransition` 的处理区分（部件 1 本轮在 `dispatchOnce` 收拢处就按此处理，`readLoop` 处属部件 3 但规则在此定）：**

- **自动路径静默**：hub `readLoop`（迟到更新撞已定终态）、`dispatchOnce`（build_assign 期间任务已被定性）收到 `ErrIllegalTransition` → **静默丢弃**，只 `slog.Debug`，不走 `slog.Error`。这是 §3.3 终态守卫 0 行的进程内对应物——迟到更新被丢弃正是想要的行为。
- **人为路径报 409**：API handler（取消/解除等用户动作）收到 `ErrIllegalTransition` → 409 + 带状态，让用户知道为什么操作没生效。

**部件 1 本轮做**：导出 `ErrIllegalTransition` sentinel；构造时带 `from`/`to`；`dispatchOnce` 收拢处对 `ErrIllegalTransition` 静默跳过。
**留给后续**：部件 7 render 层加 409 映射 + 响应体带 `current_status`；部件 3 `readLoop` 对 `ErrIllegalTransition` 静默（按本规则）。

---

以上 6 题均已给出**建议取向**（Q1 倾向 A 两量分离；Q2 封顶 + 独立计数器 + `reclaim_exhausted` 入白名单；Q3 固定默认值 5/60/10/3 不自适应；Q4 选 B 部件 1 接通 `SetStatus` 写入、proto 留部件 4；Q5 仅 admin + 独立 `task_events` 表不本轮建 + `attempt` 保留 + 预留 `actorUserID`；Q6 409 + 带当前状态 + 自动路径静默/人为路径 409）。这些取向待你确认；确认后即可据推进部件 1 的实现计划。

部件 1 的**设计骨架**（显式状态机、单一入口、reclaim/captcha 解除转移、终态守卫、build_assign 收拢）不依赖这些取值，可先按建议取向默认实现，待确认后微调取值层。各题的共同切分原则：**部件 1 定状态机规则与签名，跨部件治理（IPC 传输、权限、审计、HTTP 映射）只预留接口、留给对应部件**。
