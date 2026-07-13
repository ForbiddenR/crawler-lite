# Bot 自动化任务平台架构设计文档 v2

> 本文是 `bot-task-platform-v1-design.md` 的修正与定稿延续，不是另起炉灶。
>
> v1 已定稿且保留的部分：产品定位、`Bot -> Task -> TaskItem` 核心模型、REST/gRPC/本地 Runtime 边界、主 API 形状、主表清单、Result/Artifact/Log 三分法。
>
> v2 重点修正 v1 中会影响正确实现的缺口：
>
> 1. 执行可靠性（lease / reaper / 对账 / 终态守卫）
> 2. `dispatching` 完整状态机
> 3. Task 终态判定纯函数
> 4. Worker ↔ Master 数据面协议
> 5. 脚本交付与运行环境
> 6. 计数器更新规则
> 7. `retry_failed_items` 输入构造
> 8. Schedule 事务语义
> 9. 取消进程模型
> 10. 最小权限模型
> 11. 错误码目录
> 12. 真实 MVP-0 裁剪
>
> **与仓库内其他设计文档的关系：**
>
> | 文档 | 角色 |
> |---|---|
> | `DESIGN.md` | 当前 crawler-lite **实现向**讨论稿（Spider 模型） |
> | `DESIGN-v2.md` | crawler-lite 无状态控制面演进（lease/claim） |
> | `parts/*` | crawler-lite 部件级设计 |
> | `bot-task-platform-v1-design.md` | 本平台产品规格 v1（保留作历史） |
> | **本文** | **Bot 平台目标产品规格 v2（实现应以本文为准）** |
> | `bot-task-platform-v2-reliability-interfaces.md` | 与 `parts/01–02` 对齐的可靠性接口草图（Transition/claim/proto/reaper） |
> | `bot-task-platform-v2-protocol.md` | Bot 平台 v2 Worker Protocol 定稿（ID 策略、proto 包、消息、幂等、文件传输） |
>
> 本文描述的是目标平台，不是当前 crawler-lite 代码的实现说明书。若复用现有代码，见文末「与 crawler-lite 映射」、可靠性接口草图及 Worker Protocol 文档。

---

## 1. 项目定位

系统定位为：

> 面向多种业务自动化场景的 Bot 任务执行平台。

同时支持：

- 读取 Excel 后模拟人工向主机厂提交审批请求
- 爬取数据
- 查询状态
- 同步数据
- 上传附件
- 导出结果
- 后续发送通知

第一版设计不强绑定“网页爬虫”，而围绕 `Bot`、`Task`、`TaskItem` 建立通用模型。

---

## 2. 核心模型

```text
Bot -> Task -> TaskItem
```

| 对象 | 含义 | 角色 |
|---|---|---|
| `Bot` | 自动化脚本定义 | 配置与版本单元 |
| `Task` | 一次 Python 脚本执行 | **调度单元** |
| `TaskItem` | 脚本运行中动态上报的执行明细 | **观测/统计单元，可选** |

### 2.1 Bot

```text
Bot = 脚本 + 配置 + 输入约束 + 默认运行要求
```

示例：主机厂审批提交 Bot、审批状态查询 Bot、公告爬取 Bot、库存同步 Bot。

### 2.2 Task

```text
Task = 一次 Python 脚本进程执行
```

Master 不拆分脚本内部逻辑，将整个 Task 分配给一个 Worker。

### 2.3 TaskItem

示例：Excel 一行、一个 URL、一个分页、一个查询条件、一个文件、一个处理步骤。

原则：

```text
TaskItem 不是调度单元
TaskItem 可为 0 个（脚本可不报 item）
TaskItem 成功必须显式 success
TaskItem 终态不可修改
TaskItem 不支持单独取消 / 原地重试
失败项重试通过创建新 Task 完成
```

---

## 3. 总体执行链路

```text
Frontend / Admin / External API
        |
        | HTTP REST
        v
      Master
        |
        | gRPC Bidirectional Streaming
        v
      Worker
        |
        | Local HTTP (127.0.0.1) + TASK_TOKEN
        v
 Python Script + bot_sdk
```

上报链路：

```text
Python Script -> bot_sdk -> Worker Runtime -> Worker -> Master
```

前端实时日志：

```text
Browser <-> Master：SSE（优先）/ WebSocket（可选）
```

核心原则：

```text
用户 / 管理 API 使用 REST
Master <-> Worker 使用 gRPC 双向 Streaming
SDK 不直接访问 Master，只访问本机 Worker Runtime
前端实时日志优先 SSE
```

---

## 4. TaskStatus 状态机

### 4.1 状态集合

```text
pending
dispatching
running
canceling
success
partial_success
failed
canceled
timeout
```

| 状态 | 含义 | 终态 |
|---|---|---|
| `pending` | 已创建，等待调度 | 否 |
| `dispatching` | 已选定 Worker 并发出 AssignTask，等待 ack/start | 否 |
| `running` | Worker 正在执行 Python 脚本 | 否 |
| `canceling` | 已请求取消，等待 Worker 停止结果 | 否 |
| `success` | 脚本正常完成且业务成功 | 是 |
| `partial_success` | 脚本正常完成，但部分 TaskItem 业务失败 | 是 |
| `failed` | 脚本异常、Worker 失败、业务全部失败、不可恢复离线等 | 是 |
| `canceled` | 已取消 | 是 |
| `timeout` | 整体 Task 超时 | 是 |

### 4.2 合法转移

```text
创建：
  -> pending

调度：
  pending -> dispatching          # Master 发出 AssignTask，写入 assignment_id + lease
  dispatching -> running          # TaskAck 或 TaskStarted（以先到且合法者为准）
  dispatching -> pending          # 派发超时未 ack；可再次调度
  dispatching -> failed           # 连续派发失败超过上限（可选策略）

执行完成（仅 running，由 finalize 决定目标终态）：
  running -> success
  running -> partial_success
  running -> failed
  running -> timeout

取消：
  pending -> canceled
  dispatching -> canceling -> canceled
  running -> canceling -> canceled
  canceling -> canceled
  canceling -> failed             # 取消过程中 Worker 异常且无法确认已停（少见）

可靠性回收：
  dispatching -> pending          # assignment 过期且从未 running
  running -> pending              # lease 过期 reclaim（默认同 attempt 重派）
  running -> failed               # lease 过期且策略选择直接失败（可配）

禁止：
  任意终态 -> 其他状态（重试是建新 Task，不是改旧 Task）
  无当前 assignment_id 的上报覆盖状态
  终态被迟到消息覆盖
```

### 4.3 `dispatching` 细则（v2 补全）

进入 `dispatching` 时必须写入：

```text
worker_id
assignment_id          # 本次派发唯一 ID
dispatching_at
dispatch_deadline_at   # 默认 now + 30s
lease_expires_at       # 派发即开始租约，避免空窗
assign_sent            # true 表示本 Master 已推送 AssignTask
```

规则：

| 事件 | 行为 |
|---|---|
| Worker `TaskAck` 且 `assignment_id` 匹配 | `dispatching -> running`（若尚未 running）；记录 `acked_at` |
| Worker `TaskStarted` 且匹配 | 若仍 `dispatching` 则转 `running`；写 `started_at` |
| 超过 `dispatch_deadline_at` 仍无 ack | `dispatching -> pending`，清空 worker/assignment，`dispatch_attempt++` |
| `dispatch_attempt >= max_dispatch_attempts`（默认 5） | `pending` 可被策略直接 `failed`，`error_code=DISPATCH_EXHAUSTED` |
| 取消 | `dispatching -> canceling`；若 Worker 尚未 ack，Master 可本地直接 `canceled` |
| 重复 Assign（同 assignment_id） | Worker 幂等忽略 |
| 旧 assignment_id 的 ack/finish | Master 拒绝，不改状态 |

### 4.4 取消

可取消：`pending` / `dispatching` / `running` / `canceling`（幂等）。  
不可取消：所有终态。

运行中取消：

```text
1. Master: running -> canceling，记录 cancel_requested_at / cancel_reason
2. Master -> Worker: CancelTask(assignment_id, command_id)
3. Worker: 通知 Runtime 置取消标志；向进程发 SIGTERM
4. 等待 cancel_grace_period（默认 30s）
5. 仍未退出：SIGKILL，并尽量清理子进程组
6. Worker -> Master: CancelResult
7. Master: 未终态 TaskItem -> canceled；Task -> canceled
```

默认：

```text
cancel_grace_period = 30s
```

### 4.5 超时

双轨：

| 路径 | 条件 | 结果 |
|---|---|---|
| Worker 活着 | 进程执行超过 `timeout_s` | Worker 停进程，上报 `timeout` |
| Worker 失联 | lease 过期 | reclaim 或按策略 failed（见第 5 章） |

```text
running -> timeout   # 仅当权威判定为“执行超时”时
```

### 4.6 重试

```text
原 Task 不变（保持终态）
创建新 Task
新 Task.status = pending
新 Task.source_task_id = 原 Task ID
新 Task.run_type = retry_all | retry_failed_items
```

可重试终态：`failed` / `partial_success` / `timeout` / `canceled`。  
成功任务再次执行用 `rerun`，不叫 retry。

### 4.7 `partial_success` 语义

只表示：

```text
脚本进程正常结束（exit code = 0，且无 Runtime 判定的执行失败）后，
按 TaskItem 统计得到的业务部分失败。
```

不用于：脚本异常退出、用户取消、整体超时、Worker 崩溃不可恢复。

---

## 5. 执行可靠性（v2 新增定稿）

> 本章是 v1 最大缺口的补齐。目标：Master 单实例崩溃可恢复；Worker 静默死亡不永久卡 `running`；迟到消息不能污染终态。

### 5.1 核心对象

```text
assignment_id     一次派发的唯一 ID；所有执行期上报必须携带
lease_expires_at  任务租约到期时间
session_id        Worker 当前连接会话
```

租约含义：

```text
Master 授权该 Worker 在 lease_expires_at 前执行该 assignment。
到期后任意恢复逻辑都可认为该 assignment 已失效。
```

### 5.2 租约生命周期

```text
claim/assign:
  lease_expires_at = now + timeout_s + lease_grace_s

heartbeat（Worker 健康）:
  若 task.assignment_id 仍在 running_tasks 中：
    lease_expires_at = now + timeout_s + lease_grace_s

终态:
  清空 lease_expires_at
```

默认：

```text
timeout_s        来自 Task/Bot requirements，默认 600
lease_grace_s    系统级，默认 60
heartbeat_interval  默认 10s
reaper_interval     默认 10s
```

语义：只要 Worker 持续心跳，任务不会因 wall-clock 长跑被 reclaim；真正执行超时仍由 Worker 侧 `timeout_s` 触发。

### 5.3 Reaper

周期性扫描：

```sql
-- 派发超时
status = 'dispatching' AND dispatch_deadline_at < now()
  -> pending（清空 worker/assignment）或 failed（超过派发次数）

-- 运行租约过期
status = 'running' AND lease_expires_at < now()
  -> 默认 pending（reclaim，同 attempt 重派）
  -> 可配置 failed (error_code=WORKER_OFFLINE 或 LEASE_EXPIRED)

-- 取消卡住
status = 'canceling' AND cancel_requested_at + cancel_grace + extra < now()
  -> canceled（强制收口）
```

Reclaim 原则：

```text
reclaim 不是业务失败
reclaim 不增加用户可见 retry attempt（与“失败后建新 Task”不同）
reclaim 清空 worker_id / assignment_id / lease
reclaim 后唤醒调度
reclaim 不发“任务失败”通知
```

可选封顶：

```text
reclaim_count >= max_reclaims (默认 3) -> failed (LEASE_RECLAIM_EXHAUSTED)
```

### 5.4 Worker 离线与重连对账

Worker 心跳超时或 stream 断开超过阈值：

```text
workers.status = offline
```

**不等于立刻失败所有 running Task。**

正确顺序：

```text
1. Worker 标记 offline
2. 其名下 running Task 保持 running，直到 lease 过期
3. 若 Worker 在 lease 内重连：
   - Hello/对账携带 running_tasks[{task_id, assignment_id}]
   - Master 校验 assignment_id
   - 匹配：恢复 online，继续续租
   - 不匹配/Master 侧已非 running：指示 Worker 丢弃本地执行
4. lease 过期仍无有效续租：走 reaper
```

### 5.5 终态守卫与迟到消息

规则：

```text
Task 一旦进入终态，状态不可覆盖
只接受当前 assignment_id 的执行期事件
非法转移返回错误但不回滚历史终态
重复终态消息：返回当前状态，无副作用
```

Master 状态推进必须只有一个入口（概念上 `TaskService.Transition` / `OnUpdate`）：

```text
校验 assignment_id（执行期事件）
校验合法转移
落库（带 WHERE status 守卫）
写 task_events
若终态：清理 lease、释放 worker slot、触发通知（副作用在状态之后）
```

### 5.6 单 Master 假设与重启

v2 产品规格仍允许 **Master 单实例**，但必须 **重启安全**：

```text
Worker 注册、Task 归属、lease、assignment 全部落库
Master 重启后：
  - 从 DB 恢复 pending/dispatching/running/canceling
  - 等待 Worker 重连对账
  - reaper 继续回收过期 lease
不要求多 Master；但数据模型不阻止后续扩展
```

---

## 6. Task 终态判定（finalize 纯函数）

Task 从 `running`/`canceling` 收口时，Master 使用以下纯函数（实现必须与此一致）。

### 6.1 输入

```text
cancel_requested: bool
timed_out: bool                 # Worker/Master 权威超时
process_exit_code: int | null   # null 表示未拿到退出码（崩溃/强杀未收集）
runtime_error_code: string | null
item_stats: { total, success, failed, skipped, canceled, timeout, pending, running }
```

### 6.2 算法

```text
1) if cancel_requested:
     return canceled

2) if timed_out:
     return timeout

3) if runtime_error_code in FATAL_RUNTIME_ERRORS:
     # 如 WORKER_CRASH, ASSIGN_MISMATCH, RUNTIME_ABORTED
     return failed

4) if process_exit_code is null:
     return failed          # error_code=EXIT_CODE_UNKNOWN

5) if process_exit_code != 0:
     return failed          # 进程失败优先于 item 统计

6) # exit_code == 0，按 item 统计
   open_items = pending + running
   if open_items > 0:
     # SDK/Runtime 应在进程结束前把未收口 item 标 failed(ITEM_NOT_FINALIZED)
     # 若仍有 open，Master 强制把 open 记入 failed 后再判定
     failed += open_items
     pending = 0
     running = 0
     total 保持不变（或 = 各终态之和，若 total 曾动态增长则取 max）

   if total == 0:
     return success         # 允许无 TaskItem 的脚本

   if failed + timeout == 0:
     return success         # 全 success，或 success+skipped

   if success == 0 and skipped == total - (failed + timeout + canceled) and (failed + timeout) > 0 and success == 0:
     return failed          # 没有任何成功

   if success == 0 and (failed + timeout) > 0:
     return failed

   if (failed + timeout) > 0 and success > 0:
     return partial_success

   return success
```

简化后的可实现版本：

```text
if cancel: canceled
else if timed_out: timeout
else if exit_code != 0 or fatal_runtime: failed
else if total_items == 0: success
else if hard_fail_items == 0: success          # hard_fail = failed + timeout
else if success_items == 0: failed
else: partial_success
```

其中 `skipped` 计入完成，不计入 hard_fail；`canceled` item 通常随 Task cancel 出现，Task 已在步骤 1 返回。

### 6.3 例子

| 场景 | 结果 |
|---|---|
| exit 0，无 item | `success` |
| exit 0，10 success | `success` |
| exit 0，8 success + 2 skipped | `success` |
| exit 0，7 success + 3 failed | `partial_success` |
| exit 0，0 success + 5 failed | `failed` |
| exit 0，0 success + 5 skipped | `success` |
| exit 1，即便 9 success + 1 failed | `failed` |
| 用户取消 | `canceled` |
| 执行超时 | `timeout` |
| exit 0，仍有 2 个未收口 item | 先强制 failed，再按上表 |

### 6.4 优先级（与 v1 对齐，更精确）

```text
用户取消 > 整体超时 > Runtime/进程失败 > TaskItem 统计
```

---

## 7. TaskItemStatus 状态机

```text
pending
running
success
failed
skipped
canceled
timeout
```

合法转移：

```text
pending -> running -> success|failed|skipped|timeout|canceled
pending -> skipped
pending -> canceled
running -> success|failed|skipped|timeout|canceled
```

禁止终态回滚。

SDK `task_item.run(...)`：

```text
进入上下文：创建 item，状态 running
item.success() / failed() / skipped() / timeout() -> 对应终态
上下文抛异常 -> SDK 自动 failed，再继续抛出
上下文退出未设终态且无异常 -> 自动 failed (ITEM_NOT_FINALIZED)
```

---

## 8. 统计字段与更新规则

### 8.1 字段

```text
total_items
pending_items
running_items
success_items
failed_items
skipped_items
canceled_items
timeout_items
completed_items
progress_rate
success_rate
failed_rate
timeout_rate
error_rate
```

### 8.2 计算

```text
completed_items =
  success + failed + skipped + canceled + timeout

progress_rate = completed_items / total_items   # total=0 时为 0 或 null（API 定 null）
success_rate  = success_items / total_items
failed_rate   = failed_items / total_items
timeout_rate  = timeout_items / total_items
error_rate    = (failed_items + timeout_items) / total_items
```

规则：

```text
skipped 计入 completed，不计入失败
timeout 单独统计，不合并进 failed
total_items 支持动态增长（进度回退是正常现象）
```

### 8.3 并发更新（v2 定稿）

```text
Master 是计数器唯一写者
Worker/SDK 不直接改 Task 计数
item 状态每次合法转移时，Master 原子增减：
  UPDATE tasks SET
    running_items = running_items + :dr,
    success_items = success_items + :ds,
    ...
  WHERE id = :task_id
item 创建时 total_items + 1，并 +pending 或 +running
Task 进入终态前可 recompute 一次校准（以 task_items 表为准）
rate 字段为派生值：可落库缓存，但真相是计数字段/明细表
```

---

## 9. Task API

```http
POST   /api/tasks
GET    /api/tasks
GET    /api/tasks/{task_id}
POST   /api/tasks/{task_id}/cancel
POST   /api/tasks/{task_id}/retry
GET    /api/tasks/{task_id}/items
GET    /api/tasks/{task_id}/logs
GET    /api/tasks/{task_id}/logs/stream
GET    /api/tasks/{task_id}/results
GET    /api/tasks/{task_id}/artifacts
```

### 9.1 run_type

```text
manual
schedule
retry_all
retry_failed_items
rerun
api
```

### 9.2 input_source

```text
file
params
task_items
none
```

### 9.3 创建

成功后 `status=pending`，仅表示进入调度队列。

### 9.4 取消

见 4.4。对终态返回 409。

### 9.5 重试

永远创建新 Task。

模式：

```text
all
failed_items
```

#### 9.5.1 `retry_failed_items` 输入构造（v2 定稿）

新 Task：

```text
run_type = retry_failed_items
source_task_id = old_task.id
input_source = task_items
input_params = {
  "source_task_id": "...",
  "include_statuses": ["failed", "timeout"],
  "items": [
    {
      "source_item_id": "...",
      "type": "record",
      "key": "row-18",              # 保留原 key，便于业务对齐
      "input_data": { ... }         # 深拷贝旧 item.input_data
    }
  ]
}
```

规则：

```text
范围：旧 Task 中 status in (failed, timeout) 的 item
不包括 success / skipped / canceled
新 Task 运行时 SDK 通过 input 拉取待重试列表，再创建新的 TaskItem（新 ID）
新 item 建议 idempotency_key = "retry:{source_item_id}" 或业务 key
若过滤后 items 为空：拒绝创建，API 400 (NOTHING_TO_RETRY)
```

`retry_all` / `rerun`：复制原 Task 的 `input_source/input_params/config/requirements/bot_version` 快照，不复制 item 明细。

---

## 10. TaskItem API

```http
GET /api/tasks/{task_id}/items
GET /api/tasks/{task_id}/items/{item_id}
GET /api/tasks/{task_id}/items/{item_id}/logs
GET /api/tasks/{task_id}/items/{item_id}/results
GET /api/tasks/{task_id}/items/{item_id}/artifacts
```

第一版不提供 item 级 cancel/retry/patch。

字段：

```text
id, task_id, type, key, index, status,
input_data, output_data,
error_code, error_message, error_detail, summary,
idempotency_key,
result_count, artifact_count, log_count,
created_at, started_at, finished_at, duration_ms, updated_at
```

`type` 推荐值：`record|url|page|query|file|step|custom`（不强枚举）。  
`index` 从 0 开始，前端展示 `index+1`。

### 10.1 key 与 idempotency_key

| 字段 | 含义 |
|---|---|
| `key` | 业务可读键（如订单号、URL），可重复仅作展示/检索 |
| `idempotency_key` | 同一 Task 内幂等键，`unique(task_id, idempotency_key)` |

创建语义：

```text
若带 idempotency_key 且已存在：返回已有 item（create-or-get），不覆盖终态
若已存在且请求试图改变已终态：拒绝（409）
未带 idempotency_key：每次创建新 item
```

---

## 11. Bot API

```http
POST   /api/bots
GET    /api/bots
GET    /api/bots/{bot_id}
PATCH  /api/bots/{bot_id}
POST   /api/bots/{bot_id}/run
POST   /api/bots/{bot_id}/enable
POST   /api/bots/{bot_id}/disable
GET    /api/bots/{bot_id}/versions
POST   /api/bots/{bot_id}/versions
GET    /api/bots/{bot_id}/versions/{version_id}
```

BotStatus：`draft|enabled|disabled|archived`

脚本源：

```text
实际实现：upload
预留：git|image|inline
```

`entrypoint` 相对路径，如 `main.py`。  
Worker Runtime 固定：

```text
python main.py
```

（工作目录为解压后的脚本根目录。）

BotVersion 原则：

```text
影响执行行为的字段变化时创建 BotVersion
Task 创建时记录 bot_version_id 并复制 bot_snapshot
Bot 后续更新不影响历史 Task
```

### 11.1 bot_snapshot 最小字段

```json
{
  "bot_id": "...",
  "bot_code": "...",
  "bot_version_id": "...",
  "version": 3,
  "entrypoint": "main.py",
  "script_source": "upload",
  "package": {
    "source_file_id": "...",
    "checksum": "sha256:...",
    "storage_backend": "local",
    "storage_key": "bots/.../pkg.zip"
  },
  "input_params_schema": {},
  "default_input_source": "params",
  "default_config": {},
  "default_requirements": {
    "runtime": "python3.12",
    "capabilities": [],
    "timeout_s": 600
  }
}
```

---

## 12. 脚本交付与运行环境（v2 新增定稿）

### 12.1 上传包格式

```text
zip
根目录包含 entrypoint（如 main.py）
可选 requirements.txt
可选 bot.yaml / 配置文件
禁止：zip slip（解压路径必须落在工作目录内）
```

存储权威（MVP-0 定稿）：

```text
所有持久文件均由 Master local storage 管理。

Bot package:
  Client -> Master -> Master local storage
  source_files 表只保存元数据与 storage_key

Worker package download:
  Worker -> Master 内部鉴权下载 endpoint
  Worker 不直接读 Master 文件系统，也不依赖共享目录

Worker local disk:
  仅作为执行时临时工作目录；任务结束后可清理
  不是权威存储，不写入可被 API 下载的持久文件

后续扩展:
  s3/oss/minio/cos 替换 Master local storage 的后端，
  不改变 Worker 通过 Master endpoint 下载/上传的语义
```

`storage_backend=local` 在 MVP-0 中**特指 Master 本地文件系统**，不指 Worker 本地磁盘，也不指 NFS/共享目录。

### 12.2 AssignTask 载荷

```text
assignment_id
task_id
bot_id
bot_code
bot_version_id
entrypoint
package_uri          # Master 内部鉴权下载 URL
package_checksum      # sha256
package_token         # 短期下载 token；也可复用 TASK_TOKEN
env:
  BOT_ID, BOT_CODE, TASK_ID, WORKER_ID, TASK_TOKEN,
  BOT_RUNTIME_ADDR, INPUT_FILE_PATH, INPUT_PARAMS_JSON, TASK_CONFIG_JSON
timeout_s
cancel_grace_period_s
requirements:
  runtime, capabilities, labels, image(optional)
input:
  input_source, input_file_uri?, input_params?
```

### 12.3 Worker 执行步骤

```text
1. 校验 assignment_id 幂等（已在跑则忽略）
2. 下载 package，校验 checksum
3. 解压到 task 工作目录
4. 准备 Python 环境：
   - v1 简化：使用 Worker 预装 runtime + 可选 per-requirements hash venv 缓存
   - 不在 v1 做动态拉镜像/动态建容器
5. 启动本地 Worker Runtime（127.0.0.1:随机端口）
6. 注入环境变量与 TASK_TOKEN
7. 启动进程组：python entrypoint
8. 泵送 Runtime 事件与 stdout/stderr
9. Wait 退出码 -> classify -> TaskFinished/TaskFailed/timeout
10. 清理工作目录（可配置保留失败现场）
```

### 12.4 依赖安装策略（简化）

```text
若存在 requirements.txt：
  按内容 hash 复用 venv 缓存；miss 则创建并 pip/uv install
若无 requirements.txt：
  使用 Worker 基础环境
安装失败：TaskFailed (error_code=DEPENDENCY_INSTALL_FAILED)
```

---

## 13. Worker 通信与数据面协议

### 13.1 控制面

```text
Master <-> Worker：gRPC 双向 Streaming
Worker 主动 Connect()
Master 不主动连 Worker
```

```proto
service WorkerControlService {
  rpc Connect(stream WorkerMessage) returns (stream MasterMessage);
}
```

#### Worker -> Master

```text
Hello
Heartbeat
TaskAck
TaskStarted
TaskFinished
TaskFailed
CancelResult
LogBatch
TaskItemUpsert          # v2 明确
TaskItemStatusChange    # v2 明确
ResultBatch             # v2 明确
ArtifactMeta            # v2 明确（文件本体可走独立上传通道）
ReconcileReport         # 重连对账
```

#### Master -> Worker

```text
Welcome
AssignTask
CancelTask
Ping
DropAssignment          # 对账后要求 Worker 丢弃无效 assignment
```

### 13.2 数据面约定

```text
SDK -> Runtime : 本地 HTTP + TASK_TOKEN
Runtime -> Worker 进程内 : 内存队列 / localhost
Worker -> Master : gRPC 消息（可批量）
大文件 Artifact 内容：
  v1 简化：Worker 经 Master 上传本地存储，或 Worker 写共享 local path 后仅传 meta
  后续：直传对象存储 + 回传 meta
```

批处理与背压：

```text
Log/Result/Item 事件允许 batch
Worker outbox 有界队列；满时阻塞 SDK 上报或丢弃 debug 日志（info+ 不丢，策略可配）
Master 写库失败应可重试；不得在未落库时推进终态依赖数据丢失
```

### 13.3 幂等字段

```text
message_id
assignment_id
command_id
```

规则：

```text
AssignTask 必须带 assignment_id
所有执行期上报必须带 assignment_id
Master 只接受当前 assignment_id
终态不可覆盖
重复消息返回当前结果，无副作用
重连必须 ReconcileReport(running_tasks)
```

### 13.4 Worker 选择

必须满足：

```text
worker.status == online
worker.free_slots > 0
worker.runtimes 包含 task.requirements.runtime
worker.images 包含 task.requirements.image（若指定）
worker.capabilities 包含 requirements.capabilities 全部元素
worker.labels 满足 requirements.labels
heartbeat 未超时
```

排序：

```text
权威空闲量使用 free_slots（claim 时扣减，终态/reclaim 时释放）
load_score = (max_concurrency - free_slots) / max_concurrency
选择 free_slots 最大（least-loaded）的 Worker
```

注意：

```text
free_slots 是调度权威
current_running 仅作展示/metrics
禁止心跳“猜测值”覆盖权威 free_slots 而不做对账
心跳可上报 observed_running；若与 DB 偏差，以对账流程修正
```

### 13.5 Slot 释放

```text
Task 进终态：free_slots + 1（仅一次，双释放防护）
reclaim：free_slots + 1（旧 worker）
重复终态消息不得再次 +1
```

---

## 14. Worker Runtime API / Python SDK

链路：

```text
Python Script -> bot_sdk -> Worker Runtime -> Worker -> Master
```

```text
SDK -> Runtime：本地 HTTP
Runtime 只监听 127.0.0.1
TASK_TOKEN 鉴权，仅限当前 Task
Task 终态后 token 失效
```

环境变量：

```text
BOT_ID
BOT_CODE
TASK_ID
WORKER_ID
TASK_TOKEN
BOT_RUNTIME_ADDR
INPUT_FILE_PATH
INPUT_PARAMS_JSON
TASK_CONFIG_JSON
```

Runtime HTTP（Python SDK -> Worker Runtime，本机 127.0.0.1）：

```http
GET  /runtime/context
GET  /runtime/cancellation

POST /runtime/task-items
POST /runtime/task-items/{item_id}/start
POST /runtime/task-items/{item_id}/success
POST /runtime/task-items/{item_id}/failed
POST /runtime/task-items/{item_id}/skipped
POST /runtime/task-items/{item_id}/timeout

POST /runtime/logs
POST /runtime/logs/batch
POST /runtime/results
POST /runtime/artifacts
POST /runtime/artifacts/file
```

Master 内部文件传输 endpoint（Worker -> Master，TASK_TOKEN/package_token 鉴权）：

```http
GET  /internal/runtime/tasks/{task_id}/package
POST /internal/runtime/tasks/{task_id}/artifacts/file
```

```text
package 下载只允许当前 assignment/task 使用
artifact 上传后由 Master 写入 local storage 与 artifacts 表
这些 endpoint 不直接暴露给 Browser；Browser 下载走 /api/artifacts/{artifact_id}/download
```

SDK 模块：

```text
context, input, task, task_item, logger, result, artifact
预留：notify, secrets
```

---

## 15. Schedule API

关系：

```text
Bot -> Schedule -> ScheduleRun -> Task
```

```http
POST   /api/schedules
GET    /api/schedules
GET    /api/schedules/{schedule_id}
PATCH  /api/schedules/{schedule_id}
POST   /api/schedules/{schedule_id}/enable
POST   /api/schedules/{schedule_id}/disable
POST   /api/schedules/{schedule_id}/trigger
GET    /api/schedules/{schedule_id}/runs
GET    /api/schedule-runs/{run_id}
```

cron：标准 5 段，按 `timezone` 解释。

ScheduleStatus：`enabled|disabled|archived`

### 15.1 overlap / missed（v2 收紧）

overlap_policy：

```text
skip | queue | replace | parallel
```

第一版实现：

```text
skip（默认）
parallel
```

判定“仍在跑”的范围：

```text
同一 schedule_id 下，存在 Task.status in (pending, dispatching, running, canceling)
```

`max_parallel_runs`：

```text
parallel 时生效，默认 1
达到上限：ScheduleRun.status=skipped, reason=max_parallel_runs_reached
skip 策略下只要有任一 active Task：跳过
```

missed_run_policy：

```text
skip | run_once（默认）
```

Master 宕机追赶：

```text
启动时计算 last_run_at/next_run_at 与 now 之间的 missed ticks
skip：只推进 next_run_at
run_once：最多补触发 1 次，再推进 next_run_at
```

### 15.2 ScheduleRun 事务语义

```text
在同一 DB 事务中：
  1. 锁定 schedule 行
  2. 评估 overlap / max_parallel
  3. 插入 schedule_runs
  4. 若应执行：插入 tasks(status=pending)
  5. 更新 schedule.last_run_at/next_run_at
提交后再唤醒调度器

若 Task 创建失败：
  schedule_runs.status = failed
  reason = create_task_failed
```

ScheduleRunStatus：`task_created|skipped|failed`

常见 reason：

```text
previous_task_running
bot_disabled
invalid_input
create_task_failed
missed_run_skipped
max_parallel_runs_reached
```

---

## 16. Result / Artifact / Log

### 16.1 Result

结构化业务结果。

```http
GET  /api/results
GET  /api/results/{result_id}
GET  /api/tasks/{task_id}/results
GET  /api/tasks/{task_id}/items/{item_id}/results
POST /api/tasks/{task_id}/results/export
```

`unique(task_id, idempotency_key)`：重复上报 create-or-return。

导出：`csv|xlsx|json`，导出产物记为 Artifact。  
v1 简化：同步导出小结果；大导出后续异步化。

### 16.2 Artifact

```http
GET /api/artifacts
GET /api/artifacts/{artifact_id}
GET /api/artifacts/{artifact_id}/download
GET /api/tasks/{task_id}/artifacts
GET /api/tasks/{task_id}/items/{item_id}/artifacts
```

MVP-0 Artifact 存储路径：

```text
Worker -> Master 内部鉴权上传 endpoint -> Master local storage -> artifacts 表
Browser/API Client -> Master 鉴权 download endpoint -> 文件内容
```

规则：

```text
所有 Artifact 持久文件由 Master local storage 管理
Worker local disk 只保存临时执行文件，不作为 Artifact 权威存储
artifacts.storage_backend = local 时，storage_key 是 Master local storage 下的相对 key
后续接入对象存储时，仅替换 Master storage backend，不改变 Worker 上传与 API 下载语义
```

### 16.3 Log

```http
GET /api/logs
GET /api/tasks/{task_id}/logs
GET /api/tasks/{task_id}/items/{item_id}/logs
GET /api/tasks/{task_id}/logs/stream
```

level：`debug|info|warning|error`  
source：`script|worker|runtime|master|system`

脱敏字段：

```text
password, passwd, pwd, token, secret, authorization, cookie, set-cookie,
access_token, refresh_token, api_key, client_secret, private_key
```

保留策略（v2 最低要求）：

```text
第一版可先入库
必须配置保留天数或按 Task 上限条数（默认建议 10_000 条/Task 或 7 天）
超限丢弃 debug 优先
后续可迁 Redis Streams / Loki / OpenSearch
```

---

## 17. 取消进程模型（v2 定稿）

```text
1. Runtime 设置 cancellation flag（SDK 可轮询 /runtime/cancellation）
2. 向进程组发送 SIGTERM
3. 等待 cancel_grace_period（默认 30s）
4. SIGKILL 进程组
5. 尽量清理浏览器/chromedriver 等子进程
6. 收集退出信息，回报 CancelResult
```

原则：

```text
SDK 协作取消是 best-effort，不可依赖脚本一定配合
Worker 以进程组强杀为最终手段
取消成功后 Task=canceled；已终态 item 保持原状；未终态 item=canceled
```

---

## 18. 权限模型（v2 最低线）

角色：

```text
admin
user
```

规则：

```text
admin：全部读写
user：
  - 可管理自己创建的 Bot / Task / Schedule
  - 可运行已 enable 的 Bot（若产品需要可再限私有 Bot）
  - 仅 owner 或 admin 可 cancel / retry / 下载 artifact / 看完整日志
Worker：worker_token 或 shared_secret 连接 Master
SDK：TASK_TOKEN 仅当前 Task 的 Runtime API
```

第一版不做完整多租户/RBAC，但 API 必须强制上述 owner/admin 检查。

---

## 19. 数据表结构

正式表：

```text
bots
bot_versions
tasks
task_items
workers
schedules
schedule_runs
results
artifacts
logs
source_files
task_events
```

`worker_events` 第一版不单建，重要事件写 logs 或 task_events。

### 19.1 tasks 关键增量字段（相对 v1 明确化）

```text
assignment_id
assign_sent
dispatch_attempt
dispatching_at
dispatch_deadline_at
lease_expires_at
reclaim_count
acked_at
cancel_requested_at
cancel_reason
cancel_grace_period_seconds
bot_snapshot
source_task_id
...（其余同 v1）
```

索引补充：

```text
index(status, lease_expires_at)
index(status, dispatch_deadline_at)
index(assignment_id)
index(status, priority, created_at)
```

### 19.2 workers

```text
id, name, hostname, status, version, session_id,
runtimes, images, capabilities, labels,
max_concurrency, current_running, free_slots, running_tasks,
metrics, last_heartbeat_at, connected_at, disconnected_at,
created_at, updated_at, disabled_at
```

### 19.3 其余表

bots / bot_versions / task_items / schedules / schedule_runs / results / artifacts / logs / source_files / task_events 字段与 v1 基本一致；以 v1 第 14 章为基线，并加上本章幂等与可靠性字段约束。

---

## 20. 错误码目录（v2 新增）

| error_code | 含义 |
|---|---|
| `DISPATCH_TIMEOUT` | 派发等待 ack 超时 |
| `DISPATCH_EXHAUSTED` | 超过最大派发次数 |
| `WORKER_OFFLINE` | Worker 离线且不可恢复 |
| `LEASE_EXPIRED` | 租约过期 |
| `LEASE_RECLAIM_EXHAUSTED` | reclaim 次数用尽 |
| `ASSIGNMENT_MISMATCH` | 上报 assignment 不匹配 |
| `ILLEGAL_TRANSITION` | 非法状态转移 |
| `DEPENDENCY_INSTALL_FAILED` | 依赖安装失败 |
| `PACKAGE_DOWNLOAD_FAILED` | 脚本包下载失败 |
| `PACKAGE_CHECKSUM_MISMATCH` | 脚本包校验失败 |
| `SCRIPT_EXIT_NONZERO` | 进程非 0 退出 |
| `EXIT_CODE_UNKNOWN` | 未收集到退出码 |
| `TASK_TIMEOUT` | 执行超时 |
| `TASK_CANCELED` | 用户取消 |
| `ITEM_NOT_FINALIZED` | item 未显式收口 |
| `NOTHING_TO_RETRY` | 失败项重试无目标 |
| `BOT_DISABLED` | Bot 不可运行 |
| `INVALID_INPUT` | 输入不合法 |
| `PERMISSION_DENIED` | 无权限 |
| `RUNTIME_ABORTED` | Runtime 异常中止 |

---

## 21. 安全与敏感信息

```text
SDK 不直接访问 Master
脚本不持有 Master 管理权限
TASK_TOKEN 短期、任务级、终态失效
Worker 使用 worker_token/shared_secret
密钥不得出现在日志、TaskItem input_data、Result、Artifact metadata
禁止把密码/Token/Cookie/完整 Authorization 存进 JSON 业务字段
脚本上传防 zip slip
Artifact 下载走鉴权
Worker 应对 Task 设 CPU/内存/时间基础限制（可先 cgroup/超时简化）
```

预留：

```text
secrets 模块读取密钥
notify 使用 channel，不在脚本硬编码 webhook
```

v1 **不做**完整密钥系统与通知系统；也不做 captcha 人工介入（若业务需要，后续单列）。

---

## 22. MVP 范围（v2 重切）

### 22.1 MVP-0（最先闭环，必须先做完）

```text
Bot upload + enable/disable
Master local storage 保存 Bot package（持久文件权威）
创建 Task（params）
Worker gRPC 连接 / Hello / Heartbeat
AssignTask + TaskAck/Started/Finished/Failed
Worker 通过 Master 内部 endpoint 下载脚本包并校验 checksum
python 执行（Worker local disk 仅作临时工作目录）
stdout/stderr -> Log
至少 1 条 Result 上报与查询
Artifact 上传到 Master local storage（若 MVP-0 包含文件产物）
Task 列表/详情
lease + reaper 最低实现（避免卡 running）
终态守卫
```

验收：手动跑通一个 `main.py` Bot，看到 success/failed 与日志/结果；若产生文件，文件必须由 Master 鉴权下载，不能依赖 Worker 本地路径。

### 22.2 MVP-1

```text
Worker Runtime 本地 HTTP
Python SDK 基础模块
TaskItem 创建/状态/查询
计数器与 finalize
SSE 实时日志
cancel
```

### 22.3 MVP-2

```text
retry_all / retry_failed_items
artifacts 上传下载
task_events
基础权限 owner/admin
```

### 22.4 MVP-3

```text
Schedule + ScheduleRun
overlap skip
missed run_once
BotVersion 轻量 + bot_snapshot
```

### 22.5 简化实现

```text
Master 单实例（但 DB 重启安全）
script_source 仅 upload
storage 仅 local
Schedule 仅 skip + parallel（parallel 可后置）
日志先入库 + 保留策略
notify/secrets 仅预留
不做动态容器、多 Master、DAG、完整 RBAC、对象存储
```

### 22.6 暂不实现

```text
TaskItem 级调度
动态拉镜像 / 动态创建容器
多 Master 高可用
完整通知 / 密钥系统
DAG 工作流
captcha 人工介入
Result 高级异步导出
Schedule queue/replace 完整行为
```

---

## 23. 与 crawler-lite 现有设计/代码的映射

若选择复用现有 crawler-lite 代码，可参考：

| 现有概念 | Bot 平台概念 | 说明 |
|---|---|---|
| Spider | Bot | 定义单元 |
| Spider version / source | BotVersion / package | 交付形态从 git 主路径转为 upload 主路径 |
| Task | Task | 仍是一次执行 |
| Item（crawl item） | Result 或 TaskItem+Result | 现有 item 更接近 Result |
| crawlerkit FD3 JSONL | bot_sdk + 本地 Runtime HTTP | 协议替换点 |
| hub sessions 内存 | workers 表 + assignment/lease | 应对齐 DESIGN-v2 思路 |
| task.OnUpdate | Task Transition/finalize | 保持单一入口 |
| MinIO | local storage 先顶上 | 接口预留对象存储 |

原则：

```text
产品模型以本文为准
可靠性机制应吸收 DESIGN-v2 / parts 的 lease、终态守卫、单一入口思想
不要把旧 Spider API 名称直接暴露为新平台对外 API
```

---

## 24. v2 最终定稿结论

```text
Bot 自动化任务平台
Bot 是脚本定义；Task 是一次 Python 进程执行（调度单元）
TaskItem 是可选观测明细；Result/Artifact/Log 三分法保留
Master REST 对外；Worker gRPC 双流；SDK 只打本地 Runtime
调度与执行必须有 assignment_id + lease + reaper + 终态守卫
dispatching 是一等状态，有超时回队列规则
Task 终态由 finalize 纯函数决定
Worker 离线不立即杀任务，以 lease/对账为准
脚本 upload 包经 AssignTask 下发，Worker 负责下载/解压/依赖/执行
MVP 先做 MVP-0 闭环，再 Item/取消/重试/Schedule
单 Master 可接受，但必须重启安全；多 Master 不在本版范围
```

实现顺序建议：

```text
MVP-0 执行闭环 + 可靠性底座
-> MVP-1 SDK/TaskItem/SSE/cancel
-> MVP-2 retry/artifact/权限
-> MVP-3 schedule/version snapshot
```

---

## 附录 A. 相对 v1 的变更摘要

| 主题 | v1 | v2 |
|---|---|---|
| Worker 离线 | running 直接 failed | lease 过期再 reclaim/fail；支持重连对账 |
| dispatching | 有状态、规则不全 | 完整超时/重试/取消规则 |
| 终态判定 | 优先级描述 | finalize 纯函数 + 例子表 |
| 数据面协议 | 仅 LogBatch 等 | 明确 Item/Result/Artifact 消息 |
| 脚本交付 | 仅 upload 字样 | 包格式、Assign 载荷、依赖、执行步骤 |
| 计数器 | 字段列表 | Master 唯一写者 + 原子增减 + 终态校准 |
| 失败重试输入 | 模式名 | failed_items 显式 payload |
| Schedule | 策略枚举 | 事务、active 判定、missed 追赶 |
| 取消 | 优雅+强杀 | 信号顺序与进程组清理 |
| 权限 | 一笔带过 | owner/admin 最低线 |
| 错误码 | 分散 | 集中目录 |
| MVP | 偏大 | MVP-0 先闭环 |
| 与 crawler-lite 关系 | 未说明 | 映射表 + 文档角色 |

## 附录 B. 待实现阶段可再开放的配置

```text
reclaim 默认回 pending 还是 failed
max_dispatch_attempts / max_reclaims
日志保留精确策略
Artifact 直传对象存储
Schedule parallel 完整公平性
多 Master claim 事务（可直接借鉴 DESIGN-v2）
```
