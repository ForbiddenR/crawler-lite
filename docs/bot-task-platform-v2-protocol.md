# Bot 平台 v2 Worker Protocol 设计

> 本文定义 Bot 平台 v2 中 Master ↔ Worker 的协议边界、ID 策略、消息结构、幂等规则、文件传输语义与 MVP-0 必须实现的协议子集。
>
> 配套阅读：
>
> - `bot-task-platform-v2-design.md`：产品与总体架构规格
> - `bot-task-platform-v2-reliability-interfaces.md`：可靠性接口草图
> - `bot-task-platform-v1-design.md`：历史规格，仅作参考
>
> 本文是实现 Worker/Master 协议时的直接依据。

---

## 1. 目标与边界

### 1.1 目标

协议层要解决：

```text
Master 如何识别 Worker
Master 如何派发 Task
Worker 如何确认、开始、完成或失败 Task
Worker 如何上报日志、结果、文件元数据
Master 如何校验 assignment_id，拒绝迟到或错误上报
Worker 重连后如何对账
Task package 和 Artifact 文件如何通过 Master-local storage 流转
```

### 1.2 不在本文范围

```text
HTTP REST 用户 API
Bot CRUD 详细字段
Schedule 详细行为
Python SDK 具体函数设计
前端展示
对象存储实现
```

### 1.3 核心原则

```text
所有执行期消息必须携带 task_id + assignment_id
Master 只接受当前 assignment_id 的执行期上报
Worker 不拥有持久文件；持久文件权威在 Master local storage
gRPC 负责控制与事件；大文件内容走 Master HTTP endpoint
重复消息必须幂等
终态不可覆盖
```

---

## 2. ID 策略

### 2.1 定稿决策

Bot 平台对外 ID 统一使用 `string`。

```text
bot_id
bot_version_id
task_id
task_item_id
result_id
artifact_id
assignment_id
worker_id
schedule_id
schedule_run_id
source_file_id
message_id
command_id
```

推荐生成格式：

```text
<kind>_<ULID>
```

示例：

```text
bot_01JZ8S3R8Y9QK9Y6X4MJ1J8YQ2
task_01JZ8S4A5R2E7P9ZJ4X4X10FSC
assign_01JZ8S4B7G2MZ1RBFR9KW05MHH
msg_01JZ8S4C9SNQDQ1JG3EZY6GS2H
```

### 2.2 规则

```text
公共 API / proto / SDK 全部使用 string ID
DB 内部优先使用 string/uuid/ulid
如果复用当前 crawler-lite int64 表，int64 只作为迁移期内部实现，不暴露到 Bot 平台新 API/proto
assignment_id 每次派发生成新的值
message_id 每条 gRPC 消息生成新的值
command_id 用于 CancelTask 等命令幂等
```

### 2.3 为什么不用 int64 作为平台 ID

```text
避免泄露数据库序列
便于多实例/多系统生成
便于导入导出与跨系统引用
避免后续多 Master/分布式创建时迁移 ID 类型
```

---

## 3. Proto 包路径

### 3.1 定稿决策

Bot 平台使用新的 proto 包：

```text
proto/bot/v1/worker.proto
```

Go package 建议：

```text
github.com/yourteam/crawler-lite/internal/pb/bot/v1;botv1
```

### 3.2 与旧协议关系

```text
proto/worker/v1/worker.proto 保留给当前 crawler-lite / Spider 协议兼容
Bot 平台不继续扩展旧 worker.v1 协议
Bot 平台 Master/Worker 使用 bot.v1 新协议
```

原因：

```text
旧协议是 Spider/Crawler 语义
Bot 平台需要 assignment_id、TaskItem、Result、Artifact、Reconcile 等新语义
混用旧 proto 会让兼容逻辑污染新平台协议
```

---

## 4. 协议总览

```proto
service BotWorkerControlService {
  rpc Connect(stream WorkerMessage) returns (stream MasterMessage);
}
```

Worker 主动连接 Master。

```text
Worker -> Master:
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
  TaskItemUpsert          # MVP-1
  TaskItemStatusChange    # MVP-1

Master -> Worker:
  Welcome
  AssignTask
  CancelTask
  DropAssignment
  Ping
```

MVP-0 最小实现子集：

```text
Worker -> Master:
  Hello
  Heartbeat
  TaskAck
  TaskStarted
  TaskFinished
  TaskFailed
  LogBatch
  ResultBatch

Master -> Worker:
  Welcome
  AssignTask
  CancelTask
  DropAssignment
  Ping
```

Artifact 若进入 MVP-0，则文件上传以 HTTP 为准，`ArtifactMeta` 可同步实现，也可 MVP-2 实现。

---

## 5. 公共 Envelope

每条 gRPC 消息都带 `message_id`。

```proto
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
    ResultBatch result_batch = 31;
    ArtifactMeta artifact_meta = 32;
    TaskItemUpsert task_item_upsert = 33;
    TaskItemStatusChange task_item_status = 34;
  }
}

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
```

`message_id` 规则：

```text
发送方生成
接收方可用于短期去重、日志关联、排错
不作为业务幂等主键
业务幂等仍以 assignment_id / command_id / idempotency_key 为准
```

---

## 6. Worker 注册与心跳

### 6.1 Hello

首帧必须是 `Hello`。

```proto
message Hello {
  string worker_id = 1;
  string version = 2;
  int32 max_concurrency = 3;
  repeated string runtimes = 4;
  repeated string capabilities = 5;
  map<string, string> labels = 6;
  string shared_secret = 7;
}
```

规则：

```text
worker_id 是稳定 ID，可来自 WORKER_ID 或 hostname
shared_secret / worker_token 用于 Worker 鉴权
Hello 校验失败则 Master 关闭 stream
Hello 成功后 Master upsert workers 表并返回 Welcome
```

### 6.2 Welcome

```proto
message Welcome {
  string session_id = 1;
  int32 heartbeat_interval_s = 2;
}
```

`session_id` 表示本次连接会话，不等同于 `worker_id`。

### 6.3 Heartbeat

```proto
message Heartbeat {
  int32 observed_running = 1;
  int32 observed_free_slots = 2;
  double cpu_pct = 3;
  double mem_pct = 4;
  repeated AssignmentRef running = 5;
}

message AssignmentRef {
  string task_id = 1;
  string assignment_id = 2;
}
```

规则：

```text
Heartbeat 更新 workers.last_heartbeat_at
Heartbeat.running 用于续租当前 assignment
observed_free_slots 只作 metrics/校准，不直接覆盖 DB 中的权威 free_slots
Master 对 running[] 中仍有效的 assignment 续租
Master 对无效 assignment 可发送 DropAssignment
```

续租条件：

```text
task_id 匹配
assignment_id 匹配
Task.status in (dispatching, running, canceling)
```

---

## 7. Master -> Worker 消息

### 7.1 AssignTask

```proto
message AssignTask {
  string assignment_id = 1;
  string task_id = 2;

  string bot_id = 3;
  string bot_code = 4;
  string bot_version_id = 5;

  string entrypoint = 6;

  string package_uri = 7;       // Master 内部鉴权下载 URL
  string package_checksum = 8;  // sha256:<hex>
  string package_token = 9;     // 短期下载 token；也可复用 TASK_TOKEN

  string input_source = 10;     // params | file | task_items | none
  bytes input_params_json = 11;
  string input_file_uri = 12;

  bytes task_config_json = 13;

  int32 timeout_s = 14;
  int32 cancel_grace_period_s = 15;

  string runtime = 16;
  repeated string required_capabilities = 17;
  map<string, string> labels = 18;

  map<string, string> env = 19;
}
```

`env` 至少包含：

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

规则：

```text
AssignTask 必须携带 assignment_id
Worker 收到重复 assignment_id 的 AssignTask，应幂等忽略或返回 TaskAck
Worker 下载 package 后必须校验 package_checksum
Worker 不得把 package_uri/package_token 写入日志
Worker local disk 只作临时目录
```

### 7.2 CancelTask

```proto
message CancelTask {
  string task_id = 1;
  string assignment_id = 2;
  string command_id = 3;
  int32 grace_period_s = 4;
}
```

规则：

```text
command_id 用于取消命令幂等
Worker 只取消当前 assignment_id 匹配的执行
若 assignment_id 不匹配，Worker 返回 CancelResult(success=false 或 ignored=true)，Master 不改终态
```

### 7.3 DropAssignment

```proto
message DropAssignment {
  string task_id = 1;
  string assignment_id = 2;
  string reason = 3;
}
```

常见 reason：

```text
assignment_mismatch
task_terminal
reclaimed
canceled
unknown_assignment
```

Worker 收到后：

```text
如果本地仍在跑该 assignment，应停止/丢弃结果
后续该 assignment 的上报不应再发送
```

### 7.4 Ping

```proto
message Ping {
  int64 nonce = 1;
}
```

用于 stream liveness，可选。

---

## 8. Worker -> Master 执行期消息

所有执行期消息必须携带：

```text
task_id
assignment_id
```

Master 校验：

```text
当前 Task.assignment_id == 消息 assignment_id
Task.status 允许接收该事件
否则拒绝/忽略，记录 ASSIGNMENT_MISMATCH 或 stale_assignment
```

### 8.1 TaskAck

```proto
message TaskAck {
  string task_id = 1;
  string assignment_id = 2;
}
```

含义：Worker 已收到并接受 AssignTask。

状态转移：

```text
dispatching -> running
```

若后续还有 `TaskStarted`，`TaskAck` 可只表示 accepted；实现可选择：

```text
MVP-0 简化：TaskAck 即可转 running
生产增强：TaskAck 只记录 acked_at，TaskStarted 再转 running
```

Bot v2 默认采用 MVP-0 简化：`TaskAck` 可使 Task 进入 `running`，`TaskStarted` 补充 `started_at`。

### 8.2 TaskStarted

```proto
message TaskStarted {
  string task_id = 1;
  string assignment_id = 2;
  int64 started_at_unix_ms = 3;
}
```

规则：

```text
若 Task 仍 dispatching，则 dispatching -> running
若已 running，则只补 started_at（幂等）
非法/迟到则忽略
```

### 8.3 TaskFinished

```proto
message TaskFinished {
  string task_id = 1;
  string assignment_id = 2;
  int32 exit_code = 3;
  bool timed_out = 4;
  bool cancel_requested = 5;
  string error_code = 6;
  string error_message = 7;
  ItemStats item_stats = 8;
}

message ItemStats {
  int32 total = 1;
  int32 pending = 2;
  int32 running = 3;
  int32 success = 4;
  int32 failed = 5;
  int32 skipped = 6;
  int32 canceled = 7;
  int32 timeout = 8;
}
```

规则：

```text
Worker 上报事实，不直接决定 partial_success
Master 使用 DecideTerminal 计算最终 Task 状态
Master 以 DB 中的 TaskItem 计数为准；item_stats 仅作快照/校验
exit_code=0 且无 item 默认 success
```

### 8.4 TaskFailed

```proto
message TaskFailed {
  string task_id = 1;
  string assignment_id = 2;
  string error_code = 3;
  string error_message = 4;
  int32 exit_code = 5;
}
```

用于无法正常进入 `TaskFinished` 的执行失败，例如：

```text
PACKAGE_DOWNLOAD_FAILED
PACKAGE_CHECKSUM_MISMATCH
DEPENDENCY_INSTALL_FAILED
RUNTIME_ABORTED
SCRIPT_EXIT_NONZERO
```

状态：

```text
running/dispatching -> failed
```

### 8.5 CancelResult

```proto
message CancelResult {
  string task_id = 1;
  string assignment_id = 2;
  string command_id = 3;
  bool canceled = 4;
  bool force_killed = 5;
  int32 exit_code = 6;
  string error_code = 7;
  string error_message = 8;
}
```

规则：

```text
command_id 必须匹配最近 CancelTask 或可被幂等接受
取消成功：canceling -> canceled
取消失败且无法确认进程停止：canceling -> failed（少见）
```

---

## 9. 日志、结果、Artifact、TaskItem 消息

### 9.1 LogBatch

```proto
message LogBatch {
  string task_id = 1;
  string assignment_id = 2;
  repeated LogLine lines = 3;
}

message LogLine {
  int64 ts_unix_ms = 1;
  string level = 2;    // debug|info|warning|error
  string source = 3;   // script|worker|runtime|master|system
  string message = 4;
  bytes fields_json = 5;
  string task_item_id = 6;
}
```

规则：

```text
日志必须脱敏
debug 日志可在背压时优先丢弃
info/warning/error 不应静默丢弃
```

### 9.2 ResultBatch

```proto
message ResultBatch {
  string task_id = 1;
  string assignment_id = 2;
  repeated ResultRecord results = 3;
}

message ResultRecord {
  string result_id = 1;          // 可空，Master 可生成
  string task_item_id = 2;
  string type = 3;
  string key = 4;
  bytes data_json = 5;
  string idempotency_key = 6;
}
```

规则：

```text
idempotency_key 在同一 Task 内唯一
重复 ResultRecord create-or-return，不覆盖已有结果
MVP-0 至少支持无 task_item_id 的 Task 级 Result
```

### 9.3 ArtifactMeta

```proto
message ArtifactMeta {
  string artifact_id = 1;        // 可空，Master 可生成
  string task_id = 2;
  string assignment_id = 3;
  string task_item_id = 4;
  string name = 5;
  string type = 6;
  string content_type = 7;
  int64 size = 8;
  string checksum = 9;
  string storage_backend = 10;   // MVP-0: local
  string storage_key = 11;       // Master local storage 相对 key
  string idempotency_key = 12;
}
```

MVP-0 推荐：Artifact 文件上传 HTTP 成功时，Master 直接写 artifacts 表；`ArtifactMeta` 可作为事件通知，不作为唯一落库路径。

### 9.4 TaskItemUpsert（MVP-1）

```proto
message TaskItemUpsert {
  string task_id = 1;
  string assignment_id = 2;
  string task_item_id = 3;       // 可空，Master 可生成
  string type = 4;
  string key = 5;
  int32 index = 6;
  bytes input_data_json = 7;
  string idempotency_key = 8;
}
```

### 9.5 TaskItemStatusChange（MVP-1）

```proto
message TaskItemStatusChange {
  string task_id = 1;
  string assignment_id = 2;
  string task_item_id = 3;
  string status = 4;             // running|success|failed|skipped|canceled|timeout
  bytes output_data_json = 5;
  string error_code = 6;
  string error_message = 7;
  string error_detail = 8;
}
```

---

## 10. 文件传输协议

MVP-0 所有持久文件由 Master local storage 管理。

### 10.1 Package 下载

AssignTask 下发：

```text
package_uri
package_checksum
package_token
```

Worker 下载：

```http
GET /internal/runtime/tasks/{task_id}/package
Authorization: Bearer <package_token 或 TASK_TOKEN>
X-Assignment-ID: <assignment_id>
```

Master 校验：

```text
token 有效
task_id 匹配
assignment_id 是当前 assignment
Task.status in (dispatching, running)
```

Worker 下载后：

```text
校验 sha256
防 zip slip 解压到临时工作目录
不得把 package token 写日志
任务结束后可清理临时目录
```

### 10.2 Artifact 上传

```http
POST /internal/runtime/tasks/{task_id}/artifacts/file
Authorization: Bearer <TASK_TOKEN>
X-Assignment-ID: <assignment_id>
Content-Type: multipart/form-data
```

字段：

```text
file
name
type
content_type
checksum
idempotency_key
task_item_id optional
```

Master 行为：

```text
校验 TASK_TOKEN + assignment_id
保存文件到 Master local storage
写 artifacts 表
返回 artifact_id / storage_key
```

Browser 下载：

```http
GET /api/artifacts/{artifact_id}/download
```

规则：

```text
Browser/API Client 永远不直接访问 Worker 文件路径
Worker local disk 不是 Artifact 权威存储
后续对象存储只替换 Master storage backend
```

---

## 11. 幂等规则

| 字段 | 用途 |
|---|---|
| `message_id` | gRPC 消息短期去重、排错关联 |
| `assignment_id` | 执行归属校验 |
| `command_id` | Master 命令幂等，如 CancelTask |
| `idempotency_key` | Result / Artifact / TaskItem 幂等 |

规则：

```text
重复 TaskAck：无副作用，返回/保持当前状态
重复 TaskStarted：只补缺失 started_at，不重复副作用
重复 TaskFinished：若已终态，忽略
重复 TaskFailed：若已终态，忽略
重复 CancelResult：若已 canceled，忽略
重复 ResultBatch：按 idempotency_key create-or-return
重复 Artifact 上传：按 idempotency_key create-or-return；不能重复保存多份文件
重复 TaskItemUpsert：按 idempotency_key create-or-return
```

非法或迟到 assignment：

```text
Master 不改 Task 状态
不释放 slot
不触发 retry/notify
记录 debug/warn 级日志
必要时向 Worker 发送 DropAssignment
```

---

## 12. 重连与对账

### 12.1 ReconcileReport

Worker 连接成功后，除 Hello 外，应尽快发送本地仍在执行的 assignment 列表。

```proto
message ReconcileReport {
  string session_id = 1;
  repeated AssignmentRef running = 2;
}
```

Master 对每个本地 assignment 判断：

| Worker 报告 | Master DB | 行为 |
|---|---|---|
| 存在且 assignment 匹配 | running/dispatching/canceling | 接受，续租 |
| 存在但 assignment 不匹配 | 任意 | DropAssignment |
| 存在但 Task 已终态 | 终态 | DropAssignment |
| DB 有 running，但 Worker 没报 | running | 等 lease 过期 reaper 处理 |
| DB 有 dispatching，Worker 没报 | dispatching | 等 dispatch_deadline 处理 |

### 12.2 不立即失败原则

```text
Worker offline 或重连对账缺失，不立即把 running Task failed
Task 是否回收由 lease/reaper 决定
```

---

## 13. Heartbeat 与 lease renewal

Master 收到 Heartbeat 后：

```text
1. 更新 workers.last_heartbeat_at
2. 记录 observed metrics
3. 对 Heartbeat.running 中合法 assignment 续租
4. 对非法 assignment 可 DropAssignment
```

续租 SQL 语义：

```sql
UPDATE tasks
   SET lease_expires_at = now() + make_interval(secs => $timeout_s + $lease_grace_s),
       updated_at = now()
 WHERE id = $task_id
   AND assignment_id = $assignment_id
   AND status IN ('dispatching', 'running', 'canceling');
```

约束：

```text
lease_grace_s >= 2 * heartbeat_interval_s
默认 heartbeat_interval_s = 5
默认 lease_grace_s = 60
```

---

## 14. MVP-0 协议子集

MVP-0 必须实现：

```text
proto/bot/v1/worker.proto
BotWorkerControlService.Connect
Hello / Welcome
Heartbeat
AssignTask
TaskAck
TaskStarted
TaskFinished
TaskFailed
CancelTask（可以先支持基础取消）
DropAssignment
LogBatch
ResultBatch
Package 下载 HTTP endpoint
Artifact 上传 HTTP endpoint（若 MVP-0 包含文件产物）
```

MVP-0 可以暂缓：

```text
TaskItemUpsert
TaskItemStatusChange
ArtifactMeta gRPC 事件（若 HTTP 上传已直接写 artifacts 表）
ReconcileReport 的完整双向修复（但接口应保留）
高级 Ping/Pong 逻辑
```

---

## 15. MVP-1 / MVP-2 扩展

### MVP-1

```text
TaskItemUpsert
TaskItemStatusChange
完整 ReconcileReport
SSE 日志流优化
Runtime HTTP 与 SDK 完整封装
```

### MVP-2

```text
ArtifactMeta gRPC 事件
retry_failed_items 所需 item 引用
更完整 command_id 追踪
批量上传优化
对象存储后端
```

---

## 16. 实现不变量检查清单

实现不满足以下条件时，不应认为协议闭环完成：

```text
所有执行期 gRPC 消息都带 task_id + assignment_id
Master 每次执行期上报都校验当前 assignment_id
AssignTask 带 package_uri/package_checksum/package_token
Worker package 下载后必须校验 checksum
Worker local disk 只作临时目录
Artifact 持久文件只落 Master local storage
TaskFinished 不直接决定 partial_success，必须走 Master finalize
重复终态消息不能覆盖已有终态
重复 Result/Artifact/TaskItem 按 idempotency_key 幂等
Heartbeat.running 参与 lease renewal
Worker offline 不立即 fail running Task
```

---

## 17. 错误码

协议相关错误码：

```text
ASSIGNMENT_MISMATCH
STALE_ASSIGNMENT
UNKNOWN_ASSIGNMENT
PACKAGE_DOWNLOAD_FAILED
PACKAGE_CHECKSUM_MISMATCH
PACKAGE_TOKEN_INVALID
ARTIFACT_UPLOAD_FAILED
RESULT_WRITE_FAILED
TASK_ACK_TIMEOUT
WORKER_RECONCILE_MISMATCH
```

这些错误码进入 Task `error_code` 或 task_events，具体是否终态由状态机决定。

---

## 18. 最终定稿结论

```text
Bot 平台使用新的 proto/bot/v1 协议
公共 ID 使用 string
Master/Worker 通过一个双向 gRPC stream 通信
执行期消息必须带 task_id + assignment_id
AssignTask 通过 Master package_uri 下发脚本包
持久文件权威在 Master local storage
Worker local disk 仅作临时目录
Task 终态由 Master 根据执行事实与 item/result 统计 finalize
协议层所有重复与迟到消息必须幂等/可丢弃
```
