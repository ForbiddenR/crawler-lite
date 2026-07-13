# Bot 自动化任务平台第一版架构设计文档

## 1. 项目定位

第一版系统定位为：

> 面向多种业务自动化场景的 Bot 任务执行平台。

它不再单纯定义为“爬虫系统”，而是同时支持：

- 读取 Excel 后模拟人工向主机厂提交审批请求；
- 爬取数据；
- 查询状态；
- 同步数据；
- 上传附件；
- 导出结果；
- 后续发送通知。

因此第一版设计不强绑定“网页爬虫”概念，而是围绕 `Bot`、`Task` 和 `TaskItem` 建立通用模型。

---

## 2. 核心模型

第一版核心模型定稿为：

```text
Bot -> Task -> TaskItem
```

含义如下：

```text
Bot       自动化脚本定义
Task      一次 Python 脚本执行，第一版调度单元
TaskItem  脚本运行过程中动态上报的执行明细，第一版观测单元，可选
```

### 2.1 Bot

`Bot` 是自动化脚本定义，不是一次执行。

示例：

```text
主机厂审批提交 Bot
审批状态查询 Bot
公告数据爬取 Bot
库存同步 Bot
价格监控 Bot
文件解析 Bot
```

Bot 可以理解为：

```text
Bot = 脚本 + 配置 + 输入约束 + 默认运行要求
```

### 2.2 Task

`Task` 表示 Bot 的一次运行。

第一版中：

```text
Task = 一次 Python 脚本执行
```

Task 是第一版调度单元。Master 不拆分 Python 脚本内部逻辑，而是将整个 Task 分配给一个 Worker 执行。

### 2.3 TaskItem

`TaskItem` 表示脚本运行过程中动态上报的执行明细。

示例：

```text
Excel 中的一行审批记录
一个 URL
一个分页
一个查询条件
一个文件
一个处理步骤
```

TaskItem 是观测单元和统计单元，不是第一版调度单元。

---

## 3. 总体执行链路

整体链路定稿为：

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
        | Local HTTP / Local gRPC
        v
 Python Script + bot_sdk
```

运行时数据上报链路：

```text
Python Script -> bot_sdk -> Worker Runtime -> Worker -> Master
```

前端实时日志 / 进度：

```text
Browser <-> Master：SSE 或 WebSocket
```

第一版核心原则：

```text
用户 / 管理 API 使用 REST
Master <-> Worker 使用 gRPC 双向 Streaming
SDK 不直接访问 Master，只访问本机 Worker Runtime
前端实时日志优先使用 SSE，WebSocket 可选
```

---

## 4. TaskStatus 状态机

TaskStatus 第一版定稿为：

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

说明：

| 状态 | 含义 |
|---|---|
| `pending` | 已创建，等待调度 |
| `dispatching` | 正在派发给 Worker，等待 Worker ack |
| `running` | Worker 正在执行 Python 脚本 |
| `canceling` | 用户已请求取消，Worker 正在停止脚本 |
| `success` | 脚本正常完成且业务成功 |
| `partial_success` | 脚本正常完成，但部分 TaskItem 业务失败 |
| `failed` | 脚本异常退出、Worker 执行失败或业务全部失败 |
| `canceled` | 已取消 |
| `timeout` | 整体 Task 超时 |

### 4.1 状态变化

创建：

```text
pending
```

调度：

```text
pending -> dispatching -> running
```

正常完成：

```text
running -> success
running -> partial_success
running -> failed
```

取消：

```text
pending -> canceled
dispatching -> canceling -> canceled
running -> canceling -> canceled
```

超时：

```text
running -> timeout
```

重试：

```text
原 Task 不变
创建新 Task
新 Task.status = pending
新 Task.source_task_id = 原 Task ID
```

### 4.2 partial_success 语义

`partial_success` 只表示：

```text
脚本正常完成后的业务部分失败
```

不用于：

```text
脚本异常退出
用户取消
整体超时
Worker 离线
```

最终状态判断优先级：

```text
用户取消 > 整体超时 > Worker/脚本异常 > TaskItem 统计
```

---

## 5. TaskItemStatus 状态机

TaskItemStatus 第一版定稿为：

```text
pending
running
success
failed
skipped
canceled
timeout
```

说明：

| 状态 | 含义 |
|---|---|
| `pending` | 已创建但尚未开始处理 |
| `running` | 正在处理 |
| `success` | 处理成功 |
| `failed` | 处理失败 |
| `skipped` | 被脚本主动跳过 |
| `canceled` | 因 Task 取消而取消 |
| `timeout` | 单项处理超时 |

允许状态变化：

```text
pending -> running -> success
pending -> running -> failed
pending -> running -> skipped
pending -> skipped
running -> success
running -> failed
running -> skipped
running -> timeout
pending -> canceled
running -> canceled
```

不允许终态回滚：

```text
success -> failed
failed -> success
canceled -> success
timeout -> success
```

第一版原则：

```text
TaskItem 成功必须显式调用 success
TaskItem 终态不可修改
TaskItem 不支持单独取消
TaskItem 不支持原地重试
失败项重试通过 Task retry 创建新 Task
```

---

## 6. 统计字段与百分比

Task 冗余保存 TaskItem 统计字段：

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

计算规则：

```text
completed_items =
  success_items
  + failed_items
  + skipped_items
  + canceled_items
  + timeout_items
```

百分比：

```text
progress_rate = completed_items / total_items
success_rate  = success_items / total_items
failed_rate   = failed_items / total_items
timeout_rate  = timeout_items / total_items
error_rate    = (failed_items + timeout_items) / total_items
```

规则：

```text
skipped 计入 completed_items，但不计入失败
 timeout 单独统计，不合并进 failed
 total_items 支持动态增长
```

动态增长示例：

```text
80 / 100 = 80%
运行中发现新任务后：80 / 200 = 40%
```

这是正常现象。

---

## 7. Task API 定稿

Task API 面向前端、管理后台和外部调用方。

核心接口：

```http
POST   /api/tasks
GET    /api/tasks
GET    /api/tasks/{task_id}
POST   /api/tasks/{task_id}/cancel
POST   /api/tasks/{task_id}/retry
```

详情相关接口：

```http
GET    /api/tasks/{task_id}/items
GET    /api/tasks/{task_id}/logs
GET    /api/tasks/{task_id}/results
GET    /api/tasks/{task_id}/artifacts
```

### 7.1 run_type

第一版支持：

```text
manual
schedule
retry_all
retry_failed_items
rerun
api
```

### 7.2 input_source

第一版支持：

```text
file
params
task_items
none
```

### 7.3 创建 Task

创建成功后：

```text
Task.status = pending
```

创建成功只表示进入调度队列，不表示已经开始或完成。

### 7.4 取消 Task

可取消状态：

```text
pending
dispatching
running
```

不可取消状态：

```text
success
partial_success
failed
canceled
timeout
```

取消运行中 Task：

```text
Worker 通知 Python 进程优雅退出
等待 cancel_grace_period
超时后强制终止
Task 最终变为 canceled
已终态 TaskItem 保持原状态
未终态 TaskItem 标记为 canceled
```

默认：

```text
cancel_grace_period = 30s
```

### 7.5 重试 Task

重试永远创建新 Task，不修改原 Task。

支持模式：

```text
all
failed_items
```

可重试状态：

```text
failed
partial_success
timeout
canceled
```

失败项重试范围：

```text
failed
timeout
```

不包括：

```text
success
skipped
canceled
```

成功任务重新执行使用 `rerun`，不叫 retry。

---

## 8. TaskItem API 定稿

核心查询接口：

```http
GET /api/tasks/{task_id}/items
GET /api/tasks/{task_id}/items/{item_id}
```

关联查询接口：

```http
GET /api/tasks/{task_id}/items/{item_id}/logs
GET /api/tasks/{task_id}/items/{item_id}/results
GET /api/tasks/{task_id}/items/{item_id}/artifacts
```

第一版不提供：

```http
PATCH /api/task-items/{item_id}
POST  /api/task-items/{item_id}/cancel
POST  /api/task-items/{item_id}/retry
```

TaskItem 字段：

```text
id
task_id
type
key
index
status
input_data
output_data
error_code
error_message
error_detail
summary
result_count
artifact_count
log_count
created_at
started_at
finished_at
duration_ms
updated_at
```

`type` 不强枚举，推荐值：

```text
record
url
page
query
file
step
custom
```

`index` 从 0 开始，前端展示为 `index + 1`。

---

## 9. Bot API 定稿

Bot 是自动化脚本定义。

核心接口：

```http
POST   /api/bots
GET    /api/bots
GET    /api/bots/{bot_id}
PATCH  /api/bots/{bot_id}
POST   /api/bots/{bot_id}/run
POST   /api/bots/{bot_id}/enable
POST   /api/bots/{bot_id}/disable
```

版本接口：

```http
GET    /api/bots/{bot_id}/versions
POST   /api/bots/{bot_id}/versions
GET    /api/bots/{bot_id}/versions/{version_id}
```

Bot 字段：

```text
id
name
code
description
category
tags
status
entrypoint
script_source
current_version_id
default_input_source
input_params_schema
default_config
default_requirements
created_by
created_at
updated_at
```

BotStatus：

```text
draft
enabled
disabled
archived
```

第一版脚本来源实际实现：

```text
upload
```

预留：

```text
git
image
inline
```

`entrypoint` 第一版保存相对路径，例如：

```text
main.py
```

Worker Runtime 负责使用固定命令运行：

```text
python main.py
```

BotVersion 原则：

```text
影响执行行为的字段变化时创建 BotVersion
Task 创建时记录 bot_version_id
Task 创建时复制 bot_snapshot
Bot 更新不影响历史 Task
```

---

## 10. Worker 通信方案定稿

第一版采用：

```text
Master <-> Worker：gRPC 双向 Streaming
```

Worker 主动连接 Master：

```text
Worker -> Master: Connect()
```

Master 不主动连接 Worker，Worker 不需要暴露调度端口。

核心服务：

```proto
service WorkerControlService {
  rpc Connect(stream WorkerMessage) returns (stream MasterMessage);
}
```

### 10.1 Worker -> Master 消息

第一版消息：

```text
Hello
Heartbeat
TaskAck
TaskStarted
TaskFinished
TaskFailed
CancelResult
LogBatch
```

### 10.2 Master -> Worker 消息

第一版消息：

```text
Welcome
AssignTask
CancelTask
Ping
```

### 10.3 Worker 调度匹配规则

Worker 必须满足：

```text
worker.status == online
worker.free_slots > 0
worker.runtimes 包含 task.requirements.runtime
worker.images 包含 task.requirements.image
worker.capabilities 包含 task.requirements.capabilities 全部元素
worker.labels 满足 task.requirements.labels
worker heartbeat 未超时
```

排序规则：

```text
load_score = current_running / max_concurrency
```

选择 `load_score` 最小的 Worker。

### 10.4 幂等字段

关键字段：

```text
message_id
assignment_id
command_id
```

规则：

```text
AssignTask 必须携带 assignment_id
TaskAck / TaskStarted / TaskFinished / TaskFailed / CancelResult 必须携带 assignment_id
Master 只接受当前 assignment_id 的上报
Task 终态不可覆盖
重复消息返回当前结果，不重复执行副作用
Worker 断线重连后必须 running_tasks 对账
```

### 10.5 Worker 离线处理

Worker 心跳超时或 stream 断开超过超时时间：

```text
Worker.status = offline
```

第一版：

```text
dispatching 且未 ack -> pending
running 所属 Worker offline -> failed
error_code = WORKER_OFFLINE
```

---

## 11. Worker Runtime API / Python SDK 定稿

Worker Runtime 是 Python 脚本和平台之间的本地代理。

链路：

```text
Python Script -> bot_sdk -> Worker Runtime -> Worker -> Master
```

第一版：

```text
SDK -> Worker Runtime：本地 HTTP
Worker Runtime 只监听 127.0.0.1
SDK 使用 TASK_TOKEN 鉴权
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

核心接口：

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

`task_item.run(...)` 行为：

```text
进入上下文时创建 TaskItem，状态 running
调用 item.success() -> success
调用 item.failed() -> failed
调用 item.skipped() -> skipped
上下文中抛异常 -> SDK 自动 failed，然后继续抛出异常
上下文退出时未设置终态且无异常 -> SDK 自动 failed，error_code = ITEM_NOT_FINALIZED
```

---

## 12. Schedule API 定稿

Schedule 是定时触发规则，不是执行记录。

关系：

```text
Bot -> Schedule -> ScheduleRun -> Task
```

核心接口：

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

Schedule 字段：

```text
id
name
description
bot_id
cron
timezone
input_source
input_params
config
requirements
overlap_policy
missed_run_policy
max_parallel_runs
status
last_run_at
next_run_at
created_by
created_at
updated_at
```

cron 使用标准 5 段表达式：

```text
minute hour day-of-month month day-of-week
```

cron 按 `timezone` 解释。

ScheduleStatus：

```text
enabled
disabled
archived
```

overlap_policy：

```text
skip
queue
replace
parallel
```

第一版重点实现：

```text
skip
parallel
```

默认：

```text
skip
```

missed_run_policy：

```text
skip
run_once
```

默认：

```text
run_once
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

## 13. Result / Artifact / Log API 定稿

### 13.1 Result

Result 保存结构化业务结果。

接口：

```http
GET  /api/results
GET  /api/results/{result_id}
GET  /api/tasks/{task_id}/results
GET  /api/tasks/{task_id}/items/{item_id}/results
POST /api/tasks/{task_id}/results/export
```

Result 字段：

```text
id
task_id
task_item_id
bot_id
type
key
data
idempotency_key
created_at
updated_at
```

导出支持：

```text
csv
xlsx
json
```

导出结果生成 Artifact。

### 13.2 Artifact

Artifact 保存文件和大内容。

接口：

```http
GET /api/artifacts
GET /api/artifacts/{artifact_id}
GET /api/artifacts/{artifact_id}/download
GET /api/tasks/{task_id}/artifacts
GET /api/tasks/{task_id}/items/{item_id}/artifacts
```

Artifact 字段：

```text
id
task_id
task_item_id
bot_id
name
type
content_type
size
storage_backend
storage_key
checksum
idempotency_key
created_at
updated_at
```

第一版存储：

```text
local
```

后续扩展：

```text
s3
oss
minio
cos
```

### 13.3 Log

Log 保存运行过程和排错信息。

接口：

```http
GET /api/logs
GET /api/tasks/{task_id}/logs
GET /api/tasks/{task_id}/items/{item_id}/logs
GET /api/tasks/{task_id}/logs/stream
```

实时日志第一版优先使用 SSE。

Log 字段：

```text
id
task_id
task_item_id
bot_id
worker_id
level
message
fields
source
created_at
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

日志必须脱敏，敏感字段包括：

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

## 14. 数据表结构定稿

第一版正式表清单：

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

`worker_events` 第一版暂不单独建表，关键 Worker 事件先写入 logs 表。

### 14.1 bots

字段：

```text
id
name
code
description
category
tags
status
entrypoint
script_source
current_version_id
default_input_source
input_params_schema
default_config
default_requirements
created_by
created_at
updated_at
archived_at
```

索引：

```text
unique(code)
index(status)
index(category)
index(created_by)
index(created_at)
```

### 14.2 bot_versions

字段：

```text
id
bot_id
version
script_source
entrypoint
input_params_schema
default_input_source
default_config
default_requirements
change_note
created_by
created_at
```

索引：

```text
index(bot_id)
unique(bot_id, version)
index(created_at)
```

### 14.3 tasks

字段：

```text
id
bot_id
bot_code
bot_version_id
bot_snapshot
source_task_id
schedule_id
schedule_run_id
worker_id
assignment_id
run_type
status
priority
script_source
entrypoint
input_source
input_params
config
requirements
total_items
pending_items
running_items
success_items
failed_items
skipped_items
canceled_items
timeout_items
completed_items
total_results
artifact_count
log_count
error_log_count
progress_rate
success_rate
failed_rate
timeout_rate
error_rate
summary
error_code
error_message
error_detail
cancel_requested_at
cancel_reason
cancel_grace_period_seconds
dispatching_at
dispatch_deadline_at
started_at
finished_at
timeout_at
created_by
created_at
updated_at
```

索引：

```text
index(bot_id, created_at)
index(status, created_at)
index(run_type, created_at)
index(worker_id, status)
index(schedule_id, created_at)
index(schedule_run_id)
index(source_task_id)
index(created_by, created_at)
index(priority, created_at)
index(assignment_id)
index(status, priority, created_at)
```

### 14.4 task_items

字段：

```text
id
task_id
bot_id
type
key
index
status
input_data
output_data
error_code
error_message
error_detail
summary
idempotency_key
result_count
artifact_count
log_count
error_log_count
created_at
started_at
finished_at
duration_ms
updated_at
```

索引：

```text
index(task_id, index)
index(task_id, status)
index(task_id, type)
index(task_id, key)
index(bot_id, created_at)
unique(task_id, idempotency_key)
index(created_at)
index(task_id, status, created_at)
```

### 14.5 workers

字段：

```text
id
name
hostname
status
version
session_id
runtimes
images
capabilities
labels
max_concurrency
current_running
free_slots
running_tasks
metrics
last_heartbeat_at
connected_at
disconnected_at
created_at
updated_at
disabled_at
```

索引：

```text
index(status)
index(last_heartbeat_at)
index(name)
index(hostname)
```

### 14.6 schedules

字段：

```text
id
name
description
bot_id
cron
timezone
input_source
input_params
config
requirements
overlap_policy
missed_run_policy
max_parallel_runs
status
last_run_at
next_run_at
created_by
created_at
updated_at
archived_at
```

索引：

```text
index(bot_id)
index(status, next_run_at)
index(created_by)
index(created_at)
```

### 14.7 schedule_runs

字段：

```text
id
schedule_id
task_id
scheduled_at
triggered_at
status
reason
overlap_policy
missed_run_policy
created_at
updated_at
```

索引：

```text
index(schedule_id, scheduled_at)
index(schedule_id, status)
index(task_id)
index(created_at)
```

### 14.8 results

字段：

```text
id
task_id
task_item_id
bot_id
type
key
data
idempotency_key
created_at
updated_at
```

索引：

```text
index(task_id)
index(task_item_id)
index(bot_id, created_at)
index(type)
index(key)
index(created_at)
unique(task_id, idempotency_key)
```

### 14.9 artifacts

字段：

```text
id
task_id
task_item_id
bot_id
name
type
content_type
size
storage_backend
storage_key
checksum
idempotency_key
created_at
updated_at
```

索引：

```text
index(task_id)
index(task_item_id)
index(bot_id, created_at)
index(type)
index(content_type)
index(created_at)
unique(task_id, idempotency_key)
```

### 14.10 logs

字段：

```text
id
task_id
task_item_id
bot_id
worker_id
level
message
fields
source
created_at
```

索引：

```text
index(task_id, created_at)
index(task_item_id, created_at)
index(bot_id, created_at)
index(worker_id, created_at)
index(level, created_at)
```

### 14.11 source_files

字段：

```text
id
name
original_name
content_type
size
storage_backend
storage_key
checksum
purpose
created_by
created_at
updated_at
```

purpose：

```text
bot_script
task_input
schedule_input
other
```

索引：

```text
index(purpose)
index(created_by)
index(created_at)
index(checksum)
```

### 14.12 task_events

字段：

```text
id
task_id
from_status
to_status
reason
message
metadata
created_by
created_at
```

索引：

```text
index(task_id, created_at)
index(created_at)
index(to_status)
```

---

## 15. 第一版 MVP 范围裁剪

### 15.1 MVP 必须实现

```text
Bot upload / enable / disable
Task create / list / detail / cancel / retry
TaskItem create / update / query
Worker gRPC 双向 Streaming
Worker Runtime 本地 HTTP
Python SDK 基础模块
Log 查询 + SSE 实时日志
Result 保存 / 查询
Artifact 保存 / 下载
Schedule 基础版
数据表和 task_events
```

### 15.2 简化实现

```text
Master 单实例
script_source 只支持 upload
storage_backend 只支持 local
Schedule overlap_policy 重点实现 skip
missed_run_policy 默认 run_once
BotVersion 轻量实现
权限先做基础用户 / 管理员
日志先入库，不接 Loki / OpenSearch
SDK notify / secrets 只预留
```

### 15.3 暂不实现

```text
TaskItem 级调度
动态拉镜像
动态创建容器
多 Master 高可用
对象存储
完整通知系统
完整密钥系统
DAG 工作流
复杂权限系统
Schedule queue / replace / parallel 的完整行为
Result 高级导出
```

### 15.4 推荐里程碑

#### 里程碑 1：基础模型和 API

```text
bots
bot_versions
tasks
source_files
Bot API
Task API
脚本上传
Task 创建
Task 列表 / 详情
```

#### 里程碑 2：Worker 执行闭环

```text
workers
gRPC 双向 Streaming
Hello / Heartbeat
AssignTask / TaskAck
TaskStarted / TaskFinished / TaskFailed
Worker 执行 Python 脚本
stdout / stderr 基础日志
```

#### 里程碑 3：SDK 和 TaskItem

```text
Worker Runtime 本地 HTTP
Python SDK
task_items
results
logs
TaskItem 统计
实时日志 SSE
```

#### 里程碑 4：取消和重试

```text
CancelTask / CancelResult
Task cancel API
未终态 TaskItem 标记 canceled
retry_all
retry_failed_items
retry input
```

#### 里程碑 5：定时任务和附件

```text
schedules
schedule_runs
cron + timezone
overlap_policy skip
missed_run_policy run_once
artifacts
Artifact 下载
local storage
```

---

## 16. 安全与敏感信息规则

第一版安全边界：

```text
SDK 不直接访问 Master
脚本不需要 Master 地址
脚本不持有 Master 管理权限
SDK / Worker Runtime 使用短期任务级 TASK_TOKEN
TASK_TOKEN 只能访问当前 Task
Task 完成 / 取消 / 超时后 TASK_TOKEN 失效
Worker 使用 worker_token 或 shared_secret 连接 Master
```

敏感信息规则：

```text
通知不应在脚本中硬编码 webhook，应使用 channel
secrets 模块用于读取密钥，脚本不直接读取敏感环境变量
密钥不能出现在日志、TaskItem input_data、Result、Artifact metadata 中
SDK logger 和 Worker Runtime 应尽量做脱敏
```

禁止保存到 JSON 字段中的内容：

```text
密码
Token
Cookie
密钥
完整 Authorization header
```

---

## 17. 第一版最终定稿结论

第一版平台定稿为：

```text
Bot 自动化任务平台
Bot 是脚本定义
Task 是一次 Python 脚本执行，是调度单元
TaskItem 是脚本动态上报的执行明细，是观测单元
Master 通过 gRPC 双向 Streaming 管理 Worker
Worker 运行 Python 脚本并提供本地 Worker Runtime
Python SDK 只访问 Worker Runtime
前端 / 管理 API 使用 REST
实时日志优先使用 SSE
Schedule 到点创建 Task，并通过 ScheduleRun 记录触发决策
Result 保存结构化业务结果
Artifact 保存文件型产物
Log 保存运行日志
数据表以 Task 为执行主表，TaskItem 为明细表，BotVersion 和 bot_snapshot 保证历史可追溯
```

第一版实现重点：

```text
先完成 Bot -> Task -> Worker -> Python Script -> SDK -> TaskItem / Log / Result / Artifact 的闭环
再完成取消、重试、基础 Schedule
暂不做 TaskItem 级调度、动态容器、多 Master、高级工作流、完整通知和密钥系统
```
