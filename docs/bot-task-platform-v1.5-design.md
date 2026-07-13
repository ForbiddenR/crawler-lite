# Bot 自动化任务平台架构设计文档 v1.5

> 本文是 `bot-task-platform-v1-design.md` 的增强版，参考了 v2 设计中已经比较成熟、且适合提前纳入团队讨论的内容。
>
> v1.5 的定位不是完整 v2，也不是最终实现规格，而是：
>
> ```text
> 在保留 v1 产品模型和 MVP 方向的基础上，补齐实现前最容易出问题的可靠性、协议、文件传输、重试输入、Schedule 事务和 MVP 分层。
> ```
>
> v1.5 适合用于团队第二轮架构讨论：在确认 `Bot -> Task -> TaskItem` 大方向后，进一步确认系统如何可靠落地。

---

## 1. 文档定位

### 1.1 与 v1 的关系

v1 已经定稿的内容继续保留：

```text
Bot -> Task -> TaskItem
Task 是一次 Python 脚本执行，是调度单元
TaskItem 是脚本运行中动态上报的执行明细，是观测单元
Master <-> Worker 使用 gRPC 双向 Streaming
Python SDK 不直接访问 Master，只访问 Worker Runtime
Result / Artifact / Log 三分法
Schedule 到点创建 Task
重试创建新 Task，不修改原 Task
```

### 1.2 与 v2 的关系

v2 中有很多面向高可靠实现的设计，例如 lease、reaper、对账、协议细化、MVP-0 拆分。v1.5 不完整引入 v2 的所有内容，而是选择性吸收这些内容：

```text
assignment_id
 dispatching 完整规则
 lease_expires_at 预留与最低实现
 终态不可覆盖
 迟到消息丢弃
 Task finalize 纯函数
 Worker Protocol 最小子集
 Master local storage + Worker HTTP 文件传输
 retry_failed_items 输入构造
 ScheduleRun 事务语义
 MVP-0 / MVP-1 / MVP-2 / MVP-3 分层
```

### 1.3 v1.5 的目标

v1.5 重点回答：

```text
Task 派发后 Worker 没响应怎么办？
Worker 掉线后 Task 是否直接失败？
Master 重启后 running Task 如何处理？
取消和完成同时发生谁优先？
脚本 exit 0 但部分业务失败如何判定？
失败项重试时新 Task 的输入从哪里来？
local storage 下远程 Worker 如何拿源码和上传文件？
Schedule 到点如何避免重复创建 Task？
MVP 应该如何拆分，避免第一版过大？
```

---

## 2. 核心模型保持不变

第一版核心模型继续保持：

```text
Bot -> Task -> TaskItem
```

| 对象 | 含义 | 第一版角色 |
|---|---|---|
| `Bot` | 自动化脚本定义 | 配置与版本单元 |
| `Task` | 一次 Python 脚本执行 | 调度单元 |
| `TaskItem` | 脚本运行中动态上报的执行明细 | 观测 / 统计单元，可选 |

原则：

```text
TaskItem 不是调度单元
TaskItem 可以为 0 个
Master 不拆分 Python 脚本内部逻辑
失败项重试通过创建新 Task 完成
```

---

## 3. 总体架构边界

总体链路：

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
        | Local HTTP 127.0.0.1 + TASK_TOKEN
        v
 Python Script + bot_sdk
```

上报链路：

```text
Python Script -> bot_sdk -> Worker Runtime -> Worker -> Master
```

文件传输链路：

```text
Bot package / input file:
  Master local storage -> Worker 通过内部 HTTP endpoint 下载

Artifact file:
  Worker -> Master 内部 HTTP endpoint 上传 -> Master local storage

Browser 下载 Artifact:
  Browser -> Master API 鉴权下载
```

核心边界：

```text
REST API 面向用户、前端和外部系统
gRPC Streaming 面向 Master / Worker 控制面与事件流
Worker Runtime HTTP 只监听 127.0.0.1，仅供本机 Python SDK 使用
gRPC 不传大文件，只传控制消息、状态、日志批次、结果批次和文件元数据
持久文件权威在 Master storage，不在 Worker 本地磁盘
```

---

## 4. 存储与中间件基线

v1.5 先明确第一版讨论基线：

```text
主数据库：PostgreSQL
迁移工具：goose
持久文件：Master local storage
实时日志：第一版优先数据库 + SSE；后续可接 Redis Streams / Loki / OpenSearch
对象存储：不作为 MVP 必需；后续可把 local storage 替换为 S3 / OSS / MinIO / COS
Master：单实例优先，但数据模型预留重启恢复能力
```

说明：

```text
storage_backend = local 时，指 Master 本地文件系统，不指 Worker 本地磁盘，也不指共享目录。
Worker 本地磁盘只用于执行时临时工作目录，Task 结束后可清理。
```

第一版不要求：

```text
Redis
MinIO
多 Master
动态容器
分布式对象存储
```

但接口设计需要保持可替换：

```text
Worker 始终通过 Master endpoint 下载 package / 上传 artifact。
未来替换 storage backend 时，不改变 Worker 与 API 的语义。
```

---

## 5. TaskStatus 状态机 v1.5

### 5.1 状态集合

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
| `failed` | 脚本异常、Worker 执行失败、业务全部失败或不可恢复错误 | 是 |
| `canceled` | 已取消 | 是 |
| `timeout` | 整体 Task 执行超时 | 是 |

### 5.2 合法转移

```text
创建：
  -> pending

调度：
  pending -> dispatching
  dispatching -> running
  dispatching -> pending      # 派发超时未 ack，可重新派发
  dispatching -> failed       # 派发多次失败超过上限

执行完成：
  running -> success
  running -> partial_success
  running -> failed
  running -> timeout

取消：
  pending -> canceled
  dispatching -> canceling -> canceled
  running -> canceling -> canceled
  canceling -> canceled

可靠性回收：
  dispatching -> pending      # dispatch_deadline_at 过期
  running -> pending          # lease 过期后 reclaim，v1.5 可选
  running -> failed           # lease 过期后直接失败，v1.5 默认简化策略

禁止：
  任意终态 -> 其他状态
  旧 assignment_id 的上报覆盖当前状态
  迟到终态消息覆盖已有终态
```

---

## 6. dispatching 细则

`dispatching` 不再只是一个过渡名词，而是一等状态。

进入 `dispatching` 时必须写入：

```text
worker_id
assignment_id
assign_sent
dispatch_attempt
dispatching_at
dispatch_deadline_at
lease_expires_at
```

默认值：

```text
dispatch_deadline = now + 30s
max_dispatch_attempts = 5
```

事件规则：

| 事件 | 行为 |
|---|---|
| Worker `TaskAck` 且 `assignment_id` 匹配 | `dispatching -> running`，记录 `acked_at` |
| Worker `TaskStarted` 且匹配 | 若仍为 `dispatching`，转为 `running`，记录 `started_at` |
| 超过 `dispatch_deadline_at` 仍未 ack | `dispatching -> pending`，清空 worker / assignment，`dispatch_attempt + 1` |
| `dispatch_attempt >= max_dispatch_attempts` | 可转 `failed`，`error_code = DISPATCH_EXHAUSTED` |
| 取消 | `dispatching -> canceling`；若 Worker 尚未 ack，可本地直接 `canceled` |
| 旧 assignment_id 的 ack / finish | 拒绝，不改状态，记录事件 |
| 重复 Assign 同一个 assignment_id | Worker 幂等忽略 |

核心原则：

```text
assignment_id 是一次派发的唯一凭证。
Worker 的执行期上报必须携带 assignment_id。
Master 只接受当前 assignment_id 的上报。
```

---

## 7. 执行可靠性最低线

v1.5 不要求完整多 Master，但要求单 Master 重启后不永久卡死 Task。

### 7.1 核心字段

```text
assignment_id       当前派发 ID
lease_expires_at    当前派发/执行租约到期时间
reclaim_count       被回收次数
session_id          Worker 当前连接会话，可选
```

租约含义：

```text
Master 授权该 Worker 在 lease_expires_at 之前执行该 assignment。
租约过期后，恢复逻辑可以认为该 assignment 已失效。
```

### 7.2 租约生命周期

```text
claim / assign:
  lease_expires_at = now + timeout_s + lease_grace_s

heartbeat:
  Worker 上报 running_tasks[{task_id, assignment_id}]
  若 assignment_id 匹配，续租 lease_expires_at

终态:
  清空 lease_expires_at
```

默认：

```text
timeout_s = 600
lease_grace_s = 60
heartbeat_interval = 10s
reaper_interval = 10s
```

### 7.3 v1.5 Reaper 简化策略

周期性扫描：

```text
status = dispatching AND dispatch_deadline_at < now()
  -> pending 或 failed(DISPATCH_EXHAUSTED)

status = running AND lease_expires_at < now()
  -> failed(error_code = LEASE_EXPIRED)    # v1.5 默认简化
  -> pending(reclaim)                      # 可作为后续增强

status = canceling AND cancel_requested_at + cancel_grace + extra < now()
  -> canceled
```

v1.5 默认建议：

```text
running lease 过期先标 failed，而不是自动重派。
```

原因：

```text
很多 Bot 可能已经对外部系统产生副作用，例如提交审批请求。
在没有业务幂等保证前，自动重派可能导致重复提交。
```

后续如果 Bot 声明可安全重派，可以再启用：

```text
reclaim -> pending
```

### 7.4 Worker 离线处理

Worker 心跳超时或 stream 断开：

```text
workers.status = offline
```

但不立即失败其名下 running Task。

正确顺序：

```text
1. Worker 标记 offline
2. running Task 保持 running，等待 lease 到期
3. Worker 在 lease 内重连：通过 ReconcileReport 对账
4. assignment_id 匹配：继续执行并续租
5. assignment_id 不匹配或 Task 已终态：Master 下发 DropAssignment
6. lease 到期仍无有效续租：由 reaper 收口
```

---

## 8. 终态守卫与迟到消息

必须遵守：

```text
Task 一旦进入终态，状态不可覆盖
只接受当前 assignment_id 的执行期事件
非法状态转移不回滚历史状态
重复终态消息无副作用
所有状态推进通过一个入口完成
状态落库先于通知、重试、后续副作用
```

概念上的状态推进入口：

```text
TaskService.Transition / OnUpdate
```

处理顺序：

```text
1. 校验 task_id
2. 校验 assignment_id（执行期事件）
3. 校验合法状态转移
4. 条件更新任务状态
5. 写 task_events
6. 若进入终态：清理 lease、释放 worker slot、收口未完成 item
7. 状态提交后再触发通知、重试等副作用
```

条件更新示例：

```sql
UPDATE tasks
   SET status = :next_status,
       updated_at = now()
 WHERE id = :task_id
   AND status IN (:allowed_current_statuses)
   AND assignment_id = :assignment_id;
```

---

## 9. Task 终态判定纯函数

Task 从 `running` / `canceling` 收口时，Master 使用确定性规则判定终态。

### 9.1 输入

```text
cancel_requested: bool
timed_out: bool
process_exit_code: int | null
runtime_error_code: string | null
item_stats:
  total
  success
  failed
  skipped
  canceled
  timeout
  pending
  running
```

### 9.2 判定优先级

```text
用户取消 > 整体超时 > Runtime/进程失败 > TaskItem 统计
```

### 9.3 简化算法

```text
if cancel_requested:
  return canceled

if timed_out:
  return timeout

if runtime_error_code in FATAL_RUNTIME_ERRORS:
  return failed

if process_exit_code is null:
  return failed  # EXIT_CODE_UNKNOWN

if process_exit_code != 0:
  return failed  # SCRIPT_EXIT_NONZERO

# exit_code == 0
if pending_items + running_items > 0:
  将未收口 item 标记 failed(ITEM_NOT_FINALIZED)，再统计

if total_items == 0:
  return success

hard_fail_items = failed_items + timeout_items

if hard_fail_items == 0:
  return success

if success_items == 0:
  return failed

return partial_success
```

### 9.4 示例

| 场景 | 结果 |
|---|---|
| exit 0，无 TaskItem | `success` |
| exit 0，10 success | `success` |
| exit 0，8 success + 2 skipped | `success` |
| exit 0，7 success + 3 failed | `partial_success` |
| exit 0，0 success + 5 failed | `failed` |
| exit 0，0 success + 5 skipped | `success` |
| exit 1，即便已有部分 TaskItem 成功 | `failed` |
| 用户取消 | `canceled` |
| 整体执行超时 | `timeout` |
| exit 0，但仍有 pending / running item | 未收口 item 标 failed，再按统计判定 |

`partial_success` 只表示：

```text
脚本进程正常结束，且业务项部分失败。
```

不用于：

```text
脚本异常退出
用户取消
整体超时
Worker 崩溃不可恢复
```

---

## 10. TaskItem 状态与计数器规则

### 10.1 TaskItemStatus

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

禁止：

```text
终态回滚
终态覆盖
Task 终态后继续创建/修改 TaskItem
```

### 10.2 计数字段

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
```

计算：

```text
completed_items = success + failed + skipped + canceled + timeout
progress_rate = completed_items / total_items
success_rate  = success_items / total_items
failed_rate   = failed_items / total_items
timeout_rate  = timeout_items / total_items
error_rate    = (failed_items + timeout_items) / total_items
```

规则：

```text
skipped 计入 completed，不计入失败
timeout 单独统计，不合并进 failed
total_items 可动态增长，进度回退是正常现象
total_items = 0 时，rate API 返回 null 或 0，建议返回 null
```

### 10.3 并发更新规则

```text
Master 是 Task 计数器唯一写者
Worker / SDK 不直接改 Task 计数器
TaskItem 状态每次合法转移时，Master 在同一事务中更新 Task 计数器
Task 进入终态前，Master 可按 task_items 表重算一次计数器进行校准
rate 字段是派生值，可缓存，但真相是计数字段和 task_items 明细
```

`idempotency_key`：

```text
同一 Task 内唯一
unique(task_id, idempotency_key)
重复创建返回已有 item，不覆盖终态
```

---

## 11. Worker Protocol v1.5

### 11.1 控制面

```text
Master <-> Worker：gRPC 双向 Streaming
Worker 主动 Connect()
Master 不主动连接 Worker
```

建议 proto 服务：

```proto
service BotWorkerControlService {
  rpc Connect(stream WorkerMessage) returns (stream MasterMessage);
}
```

### 11.2 Worker -> Master

MVP 最小子集：

```text
Hello
Heartbeat
ReconcileReport
TaskAck
TaskStarted
TaskFinished
TaskFailed
CancelResult
LogBatch
ResultBatch
ArtifactMeta
```

后续增强：

```text
TaskItemUpsert
TaskItemStatusChange
```

### 11.3 Master -> Worker

```text
Welcome
AssignTask
CancelTask
DropAssignment
Ping
```

### 11.4 公共字段

每条消息建议带：

```text
message_id
```

所有执行期消息必须带：

```text
task_id
assignment_id
```

取消命令带：

```text
command_id
```

规则：

```text
message_id 用于消息去重
assignment_id 用于派发 fencing
command_id 用于取消命令幂等
Master 只接受当前 assignment_id 的执行期消息
重复消息无副作用
```

### 11.5 AssignTask 载荷

```text
assignment_id
task_id
bot_id
bot_code
bot_version_id
entrypoint
package_uri
package_checksum
package_token
timeout_s
cancel_grace_period_s
requirements:
  runtime
  capabilities
  labels
  image(optional)
input:
  input_source
  input_file_uri(optional)
  input_params(optional)
env:
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

---

## 12. 脚本交付与运行环境

### 12.1 Bot package 格式

```text
zip
根目录包含 entrypoint，例如 main.py
可选 requirements.txt
可选 bot.yaml / 配置文件
禁止 zip slip
禁止解压路径逃逸工作目录
```

### 12.2 Master local storage

```text
Client 上传 Bot package 到 Master
Master 保存到 local storage
source_files 表保存元数据
Task 创建时记录 bot_version_id 和 bot_snapshot
Worker 通过内部鉴权 endpoint 下载 package
Worker 下载后校验 checksum
```

### 12.3 Worker 执行步骤

```text
1. 接收 AssignTask
2. 校验 assignment_id 幂等
3. TaskAck
4. 下载 package
5. 校验 checksum
6. 解压到 Task 工作目录
7. 准备 Python 环境
8. 启动 Worker Runtime，监听 127.0.0.1 随机端口
9. 注入环境变量与 TASK_TOKEN
10. TaskStarted
11. 启动进程组：python entrypoint
12. 泵送 stdout / stderr、Runtime 事件
13. 进程退出后收集 exit_code
14. TaskFinished 或 TaskFailed
15. 清理工作目录，可配置保留失败现场
```

### 12.4 依赖安装策略

```text
若存在 requirements.txt：
  按文件内容 hash 复用 venv 缓存
  miss 则创建 venv 并使用 pip / uv 安装

若无 requirements.txt：
  使用 Worker 基础 Python 环境

安装失败：
  TaskFailed(error_code = DEPENDENCY_INSTALL_FAILED)
```

v1.5 不做：

```text
动态拉镜像
动态创建容器
TaskItem 级调度
```

---

## 13. Worker Runtime API / Python SDK

Worker Runtime 是 Python SDK 与 Worker 之间的本地代理。

```text
SDK -> Runtime：本地 HTTP
Runtime 只监听 127.0.0.1
TASK_TOKEN 鉴权
TASK_TOKEN 只允许访问当前 Task
Task 终态后 TASK_TOKEN 失效
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

Runtime HTTP：

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

Master 内部文件 endpoint：

```http
GET  /internal/runtime/tasks/{task_id}/package
GET  /internal/runtime/tasks/{task_id}/input-file
POST /internal/runtime/tasks/{task_id}/artifacts/file
```

规则：

```text
内部 endpoint 不直接暴露给 Browser
package 下载只允许当前 task / assignment 使用
artifact 上传后由 Master 写入 local storage 和 artifacts 表
Browser 下载走 /api/artifacts/{artifact_id}/download
```

SDK 模块：

```text
context
input
task
task_item
logger
result
artifact
```

预留：

```text
notify
secrets
```

---

## 14. retry_failed_items 输入构造

重试永远创建新 Task，不修改原 Task。

模式：

```text
retry_all
retry_failed_items
```

### 14.1 retry_failed_items

新 Task 字段：

```text
run_type = retry_failed_items
source_task_id = old_task.id
input_source = task_items
```

新 Task 输入：

```json
{
  "source_task_id": "task_xxx",
  "include_statuses": ["failed", "timeout"],
  "items": [
    {
      "source_item_id": "item_xxx",
      "type": "record",
      "key": "row-18",
      "input_data": {}
    }
  ]
}
```

规则：

```text
只包含旧 Task 中 failed / timeout 的 TaskItem
不包含 success / skipped / canceled
input_data 使用深拷贝
新 Task 运行时 SDK 从 input 中读取待重试列表
新 Task 创建新的 TaskItem，不复用旧 TaskItem ID
新 TaskItem 建议 idempotency_key = retry:{source_item_id} 或业务 key
若过滤后 items 为空，拒绝创建，400 NOTHING_TO_RETRY
```

### 14.2 retry_all / rerun

```text
复制原 Task 的 input_source / input_params / config / requirements / bot_version 快照
不复制旧 TaskItem 明细
原 Task 和原 TaskItem 不修改
```

建议新增字段：

```text
tasks.retry_mode
tasks.retry_root_task_id
tasks.retry_attempt
task_items.source_task_item_id
```

---

## 15. ScheduleRun 事务语义

关系：

```text
Bot -> Schedule -> ScheduleRun -> Task
```

Schedule 到点不是直接“跑脚本”，而是在事务中决定是否创建 Task。

### 15.1 active Task 判定

同一 `schedule_id` 下，以下状态视为 active：

```text
pending
dispatching
running
canceling
```

### 15.2 overlap_policy

```text
skip
queue
replace
parallel
```

v1.5 建议：

```text
MVP 只实现 skip
parallel 可作为后续增强
queue / replace 仅保留枚举，不实现完整行为
```

默认：

```text
skip
```

### 15.3 missed_run_policy

```text
skip
run_once
```

默认：

```text
run_once
```

Master 宕机追赶：

```text
skip：只推进 next_run_at
run_once：最多补触发 1 次，再推进 next_run_at
```

### 15.4 事务流程

```text
BEGIN
  1. 锁定 schedule 行
  2. 插入 schedule_runs(schedule_id, scheduled_at)
     建议 unique(schedule_id, scheduled_at)
  3. 评估 overlap_policy / max_parallel_runs
  4. 如果跳过：
       schedule_runs.status = skipped
       reason = previous_task_running / max_parallel_runs_reached
  5. 如果执行：
       插入 tasks(status=pending)
       schedule_runs.status = task_created
       schedule_runs.task_id = task.id
  6. 更新 schedule.last_run_at / next_run_at
COMMIT

提交后再唤醒调度器。
```

ScheduleRunStatus：

```text
task_created
skipped
failed
```

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

## 16. Result / Artifact / Log v1.5

### 16.1 Result

Result 是结构化业务结果。

接口：

```http
GET  /api/results
GET  /api/results/{result_id}
GET  /api/tasks/{task_id}/results
GET  /api/tasks/{task_id}/items/{item_id}/results
POST /api/tasks/{task_id}/results/export
```

规则：

```text
unique(task_id, idempotency_key)
重复上报 create-or-return
导出 csv / xlsx / json
导出产物记录为 Artifact
MVP 可先同步导出小结果，大结果后续异步化
```

### 16.2 Artifact

Artifact 是文件型产物。

接口：

```http
GET /api/artifacts
GET /api/artifacts/{artifact_id}
GET /api/artifacts/{artifact_id}/download
GET /api/tasks/{task_id}/artifacts
GET /api/tasks/{task_id}/items/{item_id}/artifacts
```

MVP 存储路径：

```text
Worker -> Master 内部鉴权上传 endpoint -> Master local storage -> artifacts 表
Browser/API Client -> Master 鉴权 download endpoint -> 文件内容
```

规则：

```text
所有 Artifact 持久文件由 Master local storage 管理
Worker local disk 只保存临时执行文件
artifacts.storage_backend = local 时，storage_key 是 Master local storage 下的相对 key
```

### 16.3 Log

Log 是运行过程和排错信息。

接口：

```http
GET /api/logs
GET /api/tasks/{task_id}/logs
GET /api/tasks/{task_id}/items/{item_id}/logs
GET /api/tasks/{task_id}/logs/stream
```

level：

```text
debug
info
warning
error
```

source：

```text
script
worker
runtime
master
system
```

保留策略最低要求：

```text
第一版可先入库
必须配置保留天数或每个 Task 最大日志条数
默认建议：10_000 条 / Task 或 7 天
超限时优先丢弃 debug
后续可迁移 Redis Streams / Loki / OpenSearch
```

脱敏字段：

```text
password
passwd
pwd
token
secret
authorization
cookie
set-cookie
access_token
refresh_token
api_key
client_secret
private_key
```

---

## 17. 取消进程模型

运行中取消流程：

```text
1. Master: running -> canceling
2. Master 记录 cancel_requested_at / cancel_reason
3. Master -> Worker: CancelTask(task_id, assignment_id, command_id)
4. Worker Runtime 设置 cancellation flag
5. SDK 可轮询 /runtime/cancellation
6. Worker 向进程组发送 SIGTERM
7. 等待 cancel_grace_period，默认 30s
8. 仍未退出：SIGKILL 进程组
9. 尽量清理浏览器 / chromedriver 等子进程
10. Worker -> Master: CancelResult
11. Master 将未终态 TaskItem 标记 canceled
12. Task -> canceled
```

原则：

```text
SDK 协作取消是 best-effort
不能依赖脚本一定配合取消
Worker 以进程组强杀作为最终手段
已终态 TaskItem 保持原状态
未终态 TaskItem 标记 canceled
```

---

## 18. Worker 选择与 slot 规则

Worker 必须满足：

```text
worker.status == online
worker.free_slots > 0
worker.runtimes 包含 task.requirements.runtime
worker.images 包含 task.requirements.image（若指定）
worker.capabilities 包含 task.requirements.capabilities 全部元素
worker.labels 满足 task.requirements.labels
heartbeat 未超时
```

排序：

```text
优先选择 free_slots 最大的 Worker
load_score = (max_concurrency - free_slots) / max_concurrency
```

slot 规则：

```text
free_slots 是调度权威
current_running 只用于展示 / metrics
claim / dispatch 时 free_slots - 1
Task 进终态时 free_slots + 1
reclaim 时释放旧 Worker slot
重复终态消息不得重复释放 slot
心跳 observed_running 与 DB 不一致时走对账，不直接覆盖权威值
```

v1.5 简化：

```text
单 Master 下可以先用事务 + 条件更新维护 slot。
后续多 Master 时再引入 FOR UPDATE SKIP LOCKED claim。
```

---

## 19. 权限与安全最低线

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
  - 可运行 enabled Bot
  - 仅 owner 或 admin 可 cancel / retry / 下载 artifact / 查看完整日志
Worker：使用 worker_token 或 shared_secret 连接 Master
SDK：TASK_TOKEN 仅访问当前 Task 的 Runtime API
```

v1.5 最低安全要求：

```text
SDK 不直接访问 Master 管理 API
脚本不持有 Master 管理权限
TASK_TOKEN 短期、任务级、终态失效
密钥不得出现在日志、TaskItem input_data、Result、Artifact metadata
禁止把密码、Token、Cookie、完整 Authorization 存进 JSON 业务字段
脚本上传必须防 zip slip
Artifact 下载必须鉴权
Worker 对 Task 设置时间限制
Worker 尽量设置 CPU / 内存 / 文件大小限制，可后续增强
```

信任模型：

```text
v1.5 默认 Bot 脚本由可信内部用户上传。
不支持运行任意不可信用户代码。
```

预留：

```text
secrets 模块
notify channel
更完整 RBAC
mTLS / TLS Worker 通信
```

---

## 20. 错误码目录

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
| `ITEM_NOT_FINALIZED` | TaskItem 未显式收口 |
| `NOTHING_TO_RETRY` | 失败项重试无目标 |
| `BOT_DISABLED` | Bot 不可运行 |
| `INVALID_INPUT` | 输入不合法 |
| `PERMISSION_DENIED` | 无权限 |
| `RUNTIME_ABORTED` | Worker Runtime 异常中止 |

---

## 21. 数据表增量建议

v1 表清单保持：

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

### 21.1 tasks 增量字段

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
retry_mode
retry_root_task_id
retry_attempt
bot_snapshot
source_task_id
```

索引建议：

```text
index(status, lease_expires_at)
index(status, dispatch_deadline_at)
index(assignment_id)
index(status, priority, created_at)
index(source_task_id)
index(retry_root_task_id)
```

### 21.2 task_items 增量字段

```text
idempotency_key
source_task_item_id
error_log_count
```

约束：

```text
unique(task_id, idempotency_key)
```

### 21.3 schedule_runs 约束

```text
unique(schedule_id, scheduled_at)
```

### 21.4 task_events 增强

建议字段：

```text
actor_type   # user | worker | system | runtime
actor_id
from_status
to_status
reason
message
metadata
created_at
```

---

## 22. MVP 分层

v1 文档中的 MVP 范围偏大。v1.5 建议拆成多个可交付阶段。

### 22.1 MVP-0：最小执行闭环

目标：手动上传并运行一个 `main.py` Bot，能看到 Task 状态、日志和至少一条 Result。

必须实现：

```text
Bot upload / enable / disable
Master local storage 保存 Bot package
创建 Task（params 输入）
Worker gRPC Connect / Hello / Heartbeat
AssignTask + TaskAck / TaskStarted / TaskFinished / TaskFailed
Worker 通过 Master 内部 endpoint 下载 package 并校验 checksum
Worker 执行 python entrypoint
stdout / stderr -> Log
ResultBatch 上报与查询
Task list / detail
assignment_id
终态守卫
lease + reaper 最低实现，避免永久卡 running
```

验收：

```text
手动运行一个 Bot。
脚本成功时 Task=success。
脚本失败时 Task=failed 并有错误码。
页面/API 可看到日志和 Result。
Master 重启或 Worker 断开后 Task 不永久卡住。
```

### 22.2 MVP-1：SDK / TaskItem / 取消 / 实时日志

```text
Worker Runtime 本地 HTTP
Python SDK 基础模块
TaskItem 创建 / 状态 / 查询
Task 计数器与 finalize
SSE 实时日志
Task cancel
取消进程组模型
```

### 22.3 MVP-2：重试 / Artifact / 权限

```text
retry_all
retry_failed_items
Artifact 上传 / 下载
基础 owner/admin 权限
Artifact 鉴权下载
task_events
```

### 22.4 MVP-3：Schedule / 版本快照

```text
Schedule
ScheduleRun
cron + timezone
overlap_policy = skip
missed_run_policy = run_once
BotVersion 轻量实现
bot_snapshot
```

### 22.5 暂不实现

```text
TaskItem 级调度
动态拉镜像
动态创建容器
多 Master 高可用
完整通知系统
完整密钥系统
DAG 工作流
复杂 RBAC
Schedule queue / replace 完整行为
Result 高级异步导出
captcha 人工介入
```

---

## 23. 与 crawler-lite 现有代码的映射

如果复用当前 crawler-lite 代码，可参考：

| 当前概念 | Bot 平台概念 | 说明 |
|---|---|---|
| Spider | Bot | 定义单元改名与泛化 |
| Spider source/version | BotVersion / package | upload 为主，git 预留 |
| Task | Task | 仍是一次执行 |
| Item | Result 或 TaskItem + Result | 当前 item 更接近输出结果 |
| FD3 JSONL | Worker Runtime HTTP + SDK | v1.5 目标协议会替换当前 IPC |
| WorkerHub sessions map | workers 表 + assignment / lease | 连接是传输，不是系统真相 |
| task.OnUpdate | Task Transition / finalize | 保持单一状态入口思想 |
| MinIO | Master local storage | 后续可替换对象存储 |

原则：

```text
新平台对外 API 不继续暴露 Spider 命名。
现有代码可作为实现参考，但产品模型以 Bot 平台文档为准。
状态推进仍应保持单一入口。
```

---

## 24. v1.5 最终结论

v1.5 相比 v1 的核心变化：

```text
保留 Bot -> Task -> TaskItem 产品模型
保留 Master/Worker/SDK 边界
补齐 dispatching 的 assignment_id 与超时规则
引入 lease_expires_at 和 reaper 最低线
明确 Worker 离线不立即失败 running Task
明确终态不可覆盖和迟到消息丢弃
用 finalize 纯函数判定 success / partial_success / failed
明确 gRPC 只走控制和事件，大文件通过 Master HTTP endpoint
明确 Master local storage 是持久文件权威
补齐 retry_failed_items 输入构造
补齐 ScheduleRun 事务语义
补齐取消进程组模型、错误码、权限最低线
把 MVP 拆成 MVP-0 到 MVP-3，先闭环再扩展
```

v1.5 推荐实施顺序：

```text
MVP-0：Bot package -> Task -> Worker -> Python -> Log/Result -> Task 终态
MVP-1：Worker Runtime + SDK + TaskItem + SSE + cancel
MVP-2：retry + Artifact + 权限 + task_events
MVP-3：Schedule + BotVersion + bot_snapshot
```

最终目标：

```text
先做一个能可靠运行 Python Bot 的最小平台，
再逐步补齐 TaskItem、取消、重试、文件产物、定时任务和更高级的可靠性能力。
```
