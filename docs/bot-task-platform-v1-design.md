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

## 7. 对外 REST API 通用约定

Task / Bot / Schedule / Result / Artifact / Log API 面向前端、管理后台和外部系统。第一版 API 不只列路径，还需要明确字段语义，避免后续前后端和 SDK 对接时各自理解不一致。

### 7.1 API 基本约定

| 项 | 约定 |
|---|---|
| 协议 | HTTP + JSON |
| 鉴权 | Bearer Token / Session，具体实现可沿用现有用户体系 |
| 时间格式 | RFC3339 字符串，例如 `2026-07-14T10:30:00Z` |
| ID 类型 | 对外统一按 string 处理，例如 `bot_xxx`、`task_xxx`、`item_xxx`；内部数据库可以自行选择 bigint 或 uuid |
| JSON 字段命名 | snake_case |
| 空值 | 不存在或无值用 `null`，不要用空字符串表达未设置 |
| 大文件上传 | 通过 multipart 或预签名/内部上传接口；普通 JSON API 不直接传大文件内容 |
| 敏感字段 | password、token、secret、cookie、authorization 等字段必须脱敏或禁止返回 |

成功响应建议统一包一层：

```json
{
  "data": {},
  "request_id": "req_xxx"
}
```

列表响应：

```json
{
  "data": [],
  "pagination": {
    "page": 1,
    "page_size": 20,
    "total": 100
  },
  "request_id": "req_xxx"
}
```

错误响应：

```json
{
  "error": {
    "code": "INVALID_INPUT",
    "message": "bot_id is required",
    "detail": {
      "field": "bot_id"
    }
  },
  "request_id": "req_xxx"
}
```

常见 HTTP 状态码：

| 状态码 | 含义 | 示例 |
|---|---|---|
| `200` | 查询或操作成功 | 获取 Task 详情成功 |
| `201` | 创建成功 | 创建 Bot / Task / Schedule 成功 |
| `400` | 请求格式错误 | JSON 解析失败、字段类型不对 |
| `401` | 未认证 | 未登录或 token 失效 |
| `403` | 无权限 | 没有运行 Bot 或取消 Task 的权限 |
| `404` | 资源不存在 | task_id 不存在 |
| `409` | 状态冲突 | 终态 Task 不能取消、重复 code |
| `422` | 业务校验失败 | cron 非法、input_source 与输入字段不匹配 |
| `500` | 系统内部错误 | 数据库或存储异常 |

### 7.2 通用分页与过滤字段

列表接口统一支持：

| Query 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `page` | integer | 否 | `1` | 页码，从 1 开始 |
| `page_size` | integer | 否 | `20` | 每页数量，建议最大 `100` |
| `sort` | string | 否 | `-created_at` | 排序字段，前缀 `-` 表示倒序 |
| `created_from` | string | 否 | `null` | 创建时间起始，RFC3339 |
| `created_to` | string | 否 | `null` | 创建时间结束，RFC3339 |
| `q` | string | 否 | `null` | 模糊搜索关键字，具体匹配字段由资源决定 |

---

## 8. Task API 定稿

Task API 面向前端、管理后台和外部调用方。Task 是一次 Python 脚本执行，也是第一版调度单元。

### 8.1 接口索引

本节只列出接口路径和用途。每个接口的请求字段、查询参数、响应内容和业务规则在后续独立小节中说明；下面的 `Task` 字段表是多个查询接口共用的返回对象定义，不属于某一个单独接口。

| 方法 | 路径 | 用途 | 详细说明 |
|---|---|---|---|
| `POST` | `/api/tasks` | 创建一次 Task 执行 | `8.5 创建 Task` |
| `GET` | `/api/tasks` | 分页查询 Task 列表 | `8.6 查询 Task 列表` |
| `GET` | `/api/tasks/{task_id}` | 查询 Task 详情 | `8.7 查询 Task 详情` |
| `POST` | `/api/tasks/{task_id}/cancel` | 请求取消 Task | `8.8 取消 Task` |
| `POST` | `/api/tasks/{task_id}/retry` | 从历史 Task 创建新的重试 Task | `8.9 重试 Task` |
| `GET` | `/api/tasks/{task_id}/items` | 查询 TaskItem 列表 | `9.4 查询 TaskItem 列表` |
| `GET` | `/api/tasks/{task_id}/logs` | 查询 Task 日志 | `14.3 Log API` |
| `GET` | `/api/tasks/{task_id}/results` | 查询 Task 级 Result | `14.1 Result API` |
| `GET` | `/api/tasks/{task_id}/artifacts` | 查询 Task 级 Artifact | `14.2 Artifact API` |

### 8.2 共享返回对象：Task

以下字段表定义 `GET /api/tasks` 和 `GET /api/tasks/{task_id}` 返回的 Task 对象。创建、取消、重试接口只返回其中的部分字段，具体以对应接口小节为准。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | Task ID，对外唯一 |
| `bot_id` | string | 是 | 所属 Bot ID |
| `bot_code` | string | 是 | Bot 稳定编码，冗余保存用于列表展示和历史追溯 |
| `bot_version_id` | string | 否 | 本次执行使用的 BotVersion；为空表示使用创建时的 current version 快照 |
| `bot_snapshot` | object | 是 | Task 创建时复制的 Bot 关键字段，保证历史 Task 不受 Bot 后续修改影响 |
| `source_task_id` | string | 否 | 来源 Task ID；重试、重新执行时填写 |
| `schedule_id` | string | 否 | 来源 Schedule ID；手动/API 创建为空 |
| `schedule_run_id` | string | 否 | 来源 ScheduleRun ID |
| `worker_id` | string | 否 | 当前或最后执行该 Task 的 Worker ID |
| `assignment_id` | string | 否 | 本次派发 ID，用于 Worker 上报 fencing |
| `run_type` | string | 是 | 运行类型，见 `8.3` |
| `status` | string | 是 | TaskStatus |
| `priority` | integer | 是 | 调度优先级，数值越大越优先；默认 `0` |
| `entrypoint` | string | 是 | 入口文件，例如 `main.py` |
| `input_source` | string | 是 | 输入来源，见 `8.4` |
| `input_params` | object | 否 | 小体积 JSON 输入参数 |
| `input_file_id` | string | 否 | 输入文件 ID，例如 Excel 文件；`input_source=file` 时使用 |
| `config` | object | 否 | 本次 Task 覆盖 Bot 默认配置的运行配置 |
| `requirements` | object | 否 | 本次运行要求，例如 runtime、image、capabilities、labels |
| `timeout_seconds` | integer | 否 | Task 整体超时时间，未设置时使用 Bot 默认值或系统默认值 |
| `cancel_requested_at` | string | 否 | 用户请求取消时间 |
| `cancel_reason` | string | 否 | 取消原因 |
| `cancel_grace_period_seconds` | integer | 是 | 取消宽限期，默认 `30` |
| `dispatching_at` | string | 否 | 进入 dispatching 的时间 |
| `dispatch_deadline_at` | string | 否 | Worker ack/start 截止时间 |
| `started_at` | string | 否 | Worker 开始执行脚本时间 |
| `finished_at` | string | 否 | Task 进入终态时间 |
| `timeout_at` | string | 否 | 系统判定整体超时的时间 |
| `created_by` | string | 是 | 创建人用户 ID 或 API token 主体 |
| `created_at` | string | 是 | 创建时间 |
| `updated_at` | string | 是 | 更新时间 |
| `summary` | object | 否 | 脚本或系统生成的摘要信息，用于详情页概览 |
| `error_code` | string | 否 | 失败错误码，例如 `SCRIPT_EXIT_NONZERO` |
| `error_message` | string | 否 | 面向用户的简短错误说明 |
| `error_detail` | object | 否 | 调试详情；返回给普通用户前必须脱敏 |

统计字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `total_items` | integer | TaskItem 总数，可运行中动态增长 |
| `pending_items` | integer | 未开始的 TaskItem 数 |
| `running_items` | integer | 正在处理的 TaskItem 数 |
| `success_items` | integer | 成功 TaskItem 数 |
| `failed_items` | integer | 失败 TaskItem 数 |
| `skipped_items` | integer | 跳过 TaskItem 数 |
| `canceled_items` | integer | 取消 TaskItem 数 |
| `timeout_items` | integer | 超时 TaskItem 数 |
| `completed_items` | integer | 已完成 TaskItem 数，等于 success + failed + skipped + canceled + timeout |
| `total_results` | integer | 结构化 Result 数量 |
| `artifact_count` | integer | Artifact 数量 |
| `log_count` | integer | 日志行数 |
| `error_log_count` | integer | error 级别日志行数 |
| `progress_rate` | number/null | `completed_items / total_items`；`total_items=0` 时返回 `null` |
| `success_rate` | number/null | `success_items / total_items` |
| `failed_rate` | number/null | `failed_items / total_items` |
| `timeout_rate` | number/null | `timeout_items / total_items` |
| `error_rate` | number/null | `(failed_items + timeout_items) / total_items` |

### 8.3 run_type

第一版支持：

| 值 | 含义 | 创建来源 |
|---|---|---|
| `manual` | 人工在后台点击运行 | Bot 详情页 / Task 创建页 |
| `schedule` | Schedule 到点触发 | Schedule Runner |
| `retry_all` | 对原 Task 全量重试 | Retry API |
| `retry_failed_items` | 只重试失败/超时 TaskItem | Retry API |
| `rerun` | 成功或任意历史 Task 重新执行 | Rerun 操作 |
| `api` | 外部系统调用 API 创建 | External API |

### 8.4 input_source

第一版支持：

| 值 | 含义 | 相关字段 |
|---|---|---|
| `file` | 输入来自上传文件，例如 Excel | `input_file_id` |
| `params` | 输入来自 JSON 参数 | `input_params` |
| `task_items` | 输入来自旧 Task 的部分 TaskItem | `source_task_id`、系统生成的 retry input |
| `none` | 无显式输入 | 无 |

约束：

```text
input_source = file       时，input_file_id 必填
input_source = params     时，input_params 必填或默认为 {}
input_source = task_items 时，source_task_id 必填，且只能由 retry_failed_items 创建
input_source = none       时，不应传 input_file_id
```

### 8.5 创建 Task

```http
POST /api/tasks
```

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `bot_id` | string | 是 | - | 要运行的 Bot |
| `bot_version_id` | string | 否 | 当前版本 | 指定版本；不传则使用 Bot 当前启用版本 |
| `run_type` | string | 否 | `manual` 或 `api` | 后台手动创建为 `manual`，外部 token 创建为 `api` |
| `input_source` | string | 是 | - | 输入来源 |
| `input_file_id` | string | 否 | `null` | 已上传输入文件 ID |
| `input_params` | object | 否 | `{}` | JSON 参数 |
| `config` | object | 否 | `{}` | 本次运行配置覆盖项 |
| `requirements` | object | 否 | `{}` | 本次运行要求覆盖项 |
| `priority` | integer | 否 | `0` | 调度优先级 |
| `timeout_seconds` | integer | 否 | Bot 默认值 | 整体超时 |
| `idempotency_key` | string | 否 | `null` | 外部系统防重复创建；同一创建主体下唯一 |

请求示例：

```json
{
  "bot_id": "bot_oem_approval",
  "input_source": "file",
  "input_file_id": "file_20260714_excel",
  "config": {
    "dry_run": false
  },
  "requirements": {
    "runtime": "python3.12",
    "capabilities": ["selenium", "chromium"],
    "labels": {
      "region": "cn-east"
    }
  },
  "timeout_seconds": 3600,
  "priority": 0,
  "idempotency_key": "external-order-20260714-001"
}
```

响应：

```json
{
  "data": {
    "id": "task_xxx",
    "bot_id": "bot_oem_approval",
    "run_type": "manual",
    "status": "pending",
    "input_source": "file",
    "created_at": "2026-07-14T10:30:00Z"
  }
}
```

创建成功只表示 Task 进入调度队列：

```text
Task.status = pending
```

不表示 Worker 已经开始执行。

### 8.6 查询 Task 列表

```http
GET /api/tasks
```

Query 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `bot_id` | string | 按 Bot 过滤 |
| `status` | string | 按单个状态过滤 |
| `statuses` | string | 多状态过滤，逗号分隔，例如 `pending,running` |
| `run_type` | string | 按运行类型过滤 |
| `worker_id` | string | 按 Worker 过滤 |
| `schedule_id` | string | 按 Schedule 过滤 |
| `source_task_id` | string | 查询某个 Task 派生出的重试/重跑 Task |
| `created_by` | string | 按创建人过滤 |
| `q` | string | 搜索 task id、bot code、summary |

列表项必须返回足够支撑列表页的信息：

```text
id, bot_id, bot_code, run_type, status, worker_id,
created_at, started_at, finished_at,
total_items, completed_items, success_items, failed_items, timeout_items,
progress_rate, success_rate, error_rate,
error_code, error_message
```

### 8.7 查询 Task 详情

```http
GET /api/tasks/{task_id}
```

详情页响应应包含：

| 字段组 | 说明 |
|---|---|
| 基础信息 | Task 对象完整字段 |
| Bot 快照 | `bot_snapshot`，展示当时执行的 Bot 名称、版本、入口文件 |
| 输入摘要 | `input_source`、`input_file_id`、`input_params` 摘要，敏感字段脱敏 |
| 运行配置 | `config`、`requirements` |
| 调度信息 | `worker_id`、`assignment_id`、dispatch 时间、start/finish 时间 |
| 统计信息 | 所有计数和百分比字段 |
| 错误信息 | `error_code`、`error_message`、`error_detail` |
| 操作能力 | `can_cancel`、`can_retry`、`can_rerun`，由后端根据状态和权限计算 |

### 8.8 取消 Task

```http
POST /api/tasks/{task_id}/cancel
```

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `reason` | string | 否 | `user_requested` | 取消原因 |
| `cancel_grace_period_seconds` | integer | 否 | `30` | 优雅退出等待时间 |

可取消状态：

```text
pending
dispatching
running
canceling  # 幂等返回当前取消中状态
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

响应字段：

| 字段 | 说明 |
|---|---|
| `id` | Task ID |
| `status` | `canceled` 或 `canceling` |
| `cancel_requested_at` | 取消请求时间 |
| `cancel_reason` | 取消原因 |

### 8.9 重试 Task

```http
POST /api/tasks/{task_id}/retry
```

重试永远创建新 Task，不修改原 Task。

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `mode` | string | 是 | - | `all` 或 `failed_items` |
| `reason` | string | 否 | `user_requested` | 重试原因 |
| `config` | object | 否 | 原 Task config | 可覆盖新 Task 的运行配置 |
| `requirements` | object | 否 | 原 Task requirements | 可覆盖新 Task 的运行要求 |
| `priority` | integer | 否 | 原 Task priority | 新 Task 优先级 |
| `idempotency_key` | string | 否 | `null` | 防重复创建 |

支持模式：

| mode | 新 Task run_type | 输入构造 |
|---|---|---|
| `all` | `retry_all` | 复用原 Task 的 input_source、input_file_id、input_params |
| `failed_items` | `retry_failed_items` | 从原 Task 中筛选 `failed`、`timeout` 的 TaskItem 构造输入 |

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

响应返回新 Task：

```json
{
  "data": {
    "id": "task_new",
    "source_task_id": "task_old",
    "run_type": "retry_failed_items",
    "status": "pending"
  }
}
```

---

## 9. TaskItem API 定稿

TaskItem 表示脚本运行过程中动态上报的执行明细，是观测和统计单元，不是第一版调度单元。

### 9.1 接口索引

本节只列出接口路径。TaskItem 的共享字段定义与每个接口的详细说明分开描述。

| 方法 | 路径 | 用途 | 详细说明 |
|---|---|---|---|
| `GET` | `/api/tasks/{task_id}/items` | 查询 TaskItem 列表 | `9.4 查询 TaskItem 列表` |
| `GET` | `/api/tasks/{task_id}/items/{item_id}` | 查询 TaskItem 详情 | `9.5 查询 TaskItem 详情` |
| `GET` | `/api/tasks/{task_id}/items/{item_id}/logs` | 查询 TaskItem 日志 | `14.3 Log API` |
| `GET` | `/api/tasks/{task_id}/items/{item_id}/results` | 查询 TaskItem Result | `14.1 Result API` |
| `GET` | `/api/tasks/{task_id}/items/{item_id}/artifacts` | 查询 TaskItem Artifact | `14.2 Artifact API` |

第一版不提供：

```http
PATCH /api/task-items/{item_id}
POST  /api/task-items/{item_id}/cancel
POST  /api/task-items/{item_id}/retry
```

### 9.2 共享返回对象：TaskItem

以下字段表定义 `GET /api/tasks/{task_id}/items` 和 `GET /api/tasks/{task_id}/items/{item_id}` 返回的 TaskItem 对象。它不是创建或更新接口的请求体定义。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | TaskItem ID |
| `task_id` | string | 是 | 所属 Task ID |
| `bot_id` | string | 是 | 冗余 Bot ID，便于查询 |
| `type` | string | 是 | 明细类型，推荐值见 `9.3` |
| `key` | string | 否 | 业务唯一键，例如 Excel 行号、URL、外部单号 |
| `index` | integer | 否 | 顺序号，从 0 开始，前端展示为 `index + 1` |
| `status` | string | 是 | TaskItemStatus |
| `input_data` | object | 否 | 单项输入数据，敏感字段必须脱敏或避免写入 |
| `output_data` | object | 否 | 单项输出摘要，不保存大文件内容 |
| `error_code` | string | 否 | 单项失败错误码 |
| `error_message` | string | 否 | 单项失败简短说明 |
| `error_detail` | object | 否 | 单项失败详情，必须可脱敏 |
| `summary` | object | 否 | 面向详情页展示的业务摘要 |
| `idempotency_key` | string | 否 | SDK 上报幂等键，同一 Task 内唯一 |
| `result_count` | integer | 是 | 关联 Result 数量 |
| `artifact_count` | integer | 是 | 关联 Artifact 数量 |
| `log_count` | integer | 是 | 关联日志数量 |
| `error_log_count` | integer | 是 | 关联 error 日志数量 |
| `created_at` | string | 是 | 创建时间 |
| `started_at` | string | 否 | 开始处理时间 |
| `finished_at` | string | 否 | 进入终态时间 |
| `duration_ms` | integer | 否 | 执行耗时，毫秒 |
| `updated_at` | string | 是 | 更新时间 |

### 9.3 TaskItem type 推荐值

`type` 不强枚举，推荐值：

| 值 | 含义 | 示例 |
|---|---|---|
| `record` | 一条业务记录 | Excel 中一行审批记录 |
| `url` | 一个 URL | 商品详情页 URL |
| `page` | 一个页面或分页 | 第 3 页列表 |
| `query` | 一个查询条件 | 订单号 / VIN |
| `file` | 一个文件 | 待上传附件 |
| `step` | 一个处理步骤 | 登录、提交、校验 |
| `custom` | 自定义类型 | 业务方自定义 |

### 9.4 查询 TaskItem 列表

```http
GET /api/tasks/{task_id}/items
```

Query 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `status` | string | 单状态过滤 |
| `statuses` | string | 多状态过滤，逗号分隔 |
| `type` | string | 按 TaskItem 类型过滤 |
| `key` | string | 按业务 key 精确查询 |
| `q` | string | 搜索 key、summary、error_message |
| `has_error` | boolean | 是否只看有错误的 item |
| `sort` | string | 默认 `index`，也可 `-created_at`、`-finished_at` |

列表项至少返回：

```text
id, task_id, type, key, index, status,
summary, error_code, error_message,
result_count, artifact_count, log_count,
created_at, started_at, finished_at, duration_ms
```

### 9.5 查询 TaskItem 详情

```http
GET /api/tasks/{task_id}/items/{item_id}
```

详情页响应应包含完整 TaskItem 对象，并可以附带最近日志摘要：

| 字段 | 说明 |
|---|---|
| `data` | TaskItem 完整对象 |
| `recent_logs` | 最近 N 条日志，可选，前端也可以单独调用 logs 接口 |
| `related_results_count` | 关联 Result 数量 |
| `related_artifacts_count` | 关联 Artifact 数量 |

---

## 10. Bot API 定稿

Bot 是自动化脚本定义，不是一次执行。

### 10.1 接口索引

本节只列出接口路径和用途。Bot、BotVersion 的共享返回字段与各写入/查询接口分开定义。

| 方法 | 路径 | 用途 | 详细说明 |
|---|---|---|---|
| `POST` | `/api/bots` | 创建 Bot | `10.3 创建 Bot` |
| `GET` | `/api/bots` | 分页查询 Bot 列表 | `10.7 查询 Bot 列表` |
| `GET` | `/api/bots/{bot_id}` | 查询 Bot 详情 | `10.8 查询 Bot 详情` |
| `PATCH` | `/api/bots/{bot_id}` | 更新 Bot 配置 | `10.4 更新 Bot` |
| `POST` | `/api/bots/{bot_id}/run` | 使用 Bot 创建 Task | `10.5 运行 Bot` |
| `POST` | `/api/bots/{bot_id}/enable` | 启用 Bot | `10.9 启用/禁用 Bot` |
| `POST` | `/api/bots/{bot_id}/disable` | 禁用 Bot | `10.9 启用/禁用 Bot` |
| `GET` | `/api/bots/{bot_id}/versions` | 查询 Bot 版本列表 | `10.10 BotVersion 接口` |
| `POST` | `/api/bots/{bot_id}/versions` | 创建 Bot 版本 | `10.10 BotVersion 接口` |
| `GET` | `/api/bots/{bot_id}/versions/{version_id}` | 查询 BotVersion 详情 | `10.10 BotVersion 接口` |

### 10.2 共享返回对象：Bot

以下字段表定义 Bot 列表和详情接口返回的 Bot 对象。创建和更新接口的请求字段在对应接口小节中单独定义。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | Bot ID |
| `name` | string | 是 | 展示名称，例如“主机厂审批提交 Bot” |
| `code` | string | 是 | 稳定唯一编码，只允许字母、数字、下划线、短横线；创建后不建议修改 |
| `description` | string | 否 | 说明 |
| `category` | string | 否 | 分类，例如 `approval`、`crawler`、`sync` |
| `tags` | array<string> | 否 | 标签 |
| `status` | string | 是 | BotStatus |
| `entrypoint` | string | 是 | 入口文件，例如 `main.py` |
| `script_source` | string | 是 | 脚本来源，第一版实际实现 `upload` |
| `current_version_id` | string | 否 | 当前版本 ID |
| `default_input_source` | string | 是 | 默认输入来源 |
| `input_params_schema` | object | 否 | JSON Schema，用于前端生成参数表单和后端校验 |
| `default_config` | object | 否 | 默认运行配置 |
| `default_requirements` | object | 否 | 默认运行要求 |
| `created_by` | string | 是 | 创建人 |
| `created_at` | string | 是 | 创建时间 |
| `updated_at` | string | 是 | 更新时间 |
| `archived_at` | string | 否 | 归档时间 |

BotStatus：

| 值 | 含义 |
|---|---|
| `draft` | 草稿，不能被 Schedule 自动触发 |
| `enabled` | 启用，可以运行 |
| `disabled` | 禁用，不能创建新 Task |
| `archived` | 已归档，默认列表隐藏 |

### 10.3 创建 Bot

```http
POST /api/bots
```

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `name` | string | 是 | - | Bot 名称 |
| `code` | string | 是 | - | 唯一编码 |
| `description` | string | 否 | `null` | 描述 |
| `category` | string | 否 | `null` | 分类 |
| `tags` | array<string> | 否 | `[]` | 标签 |
| `entrypoint` | string | 是 | - | 入口文件，例如 `main.py` |
| `script_source` | string | 是 | `upload` | 脚本来源 |
| `source_file_id` | string | 否 | `null` | 已上传脚本包 ID；`script_source=upload` 时使用 |
| `default_input_source` | string | 是 | - | 默认输入来源 |
| `input_params_schema` | object | 否 | `{}` | 参数 schema |
| `default_config` | object | 否 | `{}` | 默认配置 |
| `default_requirements` | object | 否 | `{}` | 默认运行要求 |

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

### 10.4 更新 Bot

```http
PATCH /api/bots/{bot_id}
```

允许更新字段：

```text
name
description
category
tags
entrypoint
source_file_id
script_source
default_input_source
input_params_schema
default_config
default_requirements
```

规则：

```text
影响执行行为的字段变化时必须创建新的 BotVersion
Bot 更新不影响历史 Task
已经创建的 Task 使用创建时的 bot_snapshot
code 第一版不建议修改，如允许修改必须保证唯一且记录审计
```

### 10.5 运行 Bot

```http
POST /api/bots/{bot_id}/run
```

这是创建 Task 的便捷接口，语义等价于：

```http
POST /api/tasks
```

请求字段与 `POST /api/tasks` 基本一致，但 `bot_id` 来自 path，不需要在 body 中重复传。

### 10.6 BotVersion 字段

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | BotVersion ID |
| `bot_id` | string | 是 | 所属 Bot |
| `version` | integer | 是 | 版本号，从 1 递增 |
| `script_source` | string | 是 | 脚本来源 |
| `source_file_id` | string | 否 | 脚本包文件 ID |
| `entrypoint` | string | 是 | 入口文件 |
| `input_params_schema` | object | 否 | 参数 schema 快照 |
| `default_input_source` | string | 是 | 默认输入来源快照 |
| `default_config` | object | 否 | 默认配置快照 |
| `default_requirements` | object | 否 | 默认运行要求快照 |
| `change_note` | string | 否 | 版本说明 |
| `created_by` | string | 是 | 创建人 |
| `created_at` | string | 是 | 创建时间 |

BotVersion 原则：

```text
影响执行行为的字段变化时创建 BotVersion
Task 创建时记录 bot_version_id
Task 创建时复制 bot_snapshot
Bot 更新不影响历史 Task
```

### 10.7 查询 Bot 列表

```http
GET /api/bots
```

Query 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `status` | string | 按 `draft`、`enabled`、`disabled`、`archived` 过滤 |
| `category` | string | 按分类过滤 |
| `q` | string | 搜索 name、code、description |
| `include_archived` | boolean | 是否包含归档 Bot，默认 `false` |

列表项返回 Bot 共享对象的摘要字段：

```text
id, name, code, category, status, current_version_id,
created_by, created_at, updated_at
```

### 10.8 查询 Bot 详情

```http
GET /api/bots/{bot_id}
```

响应返回完整 Bot 共享对象，并可附带：

```text
current_version
schedules_count
recent_tasks_count
can_run
can_edit
```

`can_run`、`can_edit` 是后端根据 Bot 状态和当前用户权限计算的操作能力，不是数据库持久字段。

### 10.9 启用 / 禁用 Bot

```http
POST /api/bots/{bot_id}/enable
POST /api/bots/{bot_id}/disable
```

请求体默认为空。响应返回：

| 字段 | 说明 |
|---|---|
| `id` | Bot ID |
| `status` | 操作后的 BotStatus |
| `updated_at` | 状态更新时间 |

规则：

```text
禁用 Bot 不影响已经创建的 Task
禁用 Bot 后不能创建新的手动/API Task
禁用 Bot 后 Schedule 到点应生成 skipped 的 ScheduleRun
启用 Bot 不会自动补跑之前跳过的 ScheduleRun
```

### 10.10 BotVersion 接口

```http
GET  /api/bots/{bot_id}/versions
POST /api/bots/{bot_id}/versions
GET  /api/bots/{bot_id}/versions/{version_id}
```

`POST /api/bots/{bot_id}/versions` 请求字段：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `source_file_id` | string | 是 | 已上传的脚本包文件 ID |
| `entrypoint` | string | 是 | 入口文件，例如 `main.py` |
| `input_params_schema` | object | 否 | 参数 JSON Schema |
| `default_input_source` | string | 是 | 默认输入来源 |
| `default_config` | object | 否 | 默认配置 |
| `default_requirements` | object | 否 | 默认运行要求 |
| `change_note` | string | 否 | 版本说明 |

接口响应返回 `BotVersion` 共享对象。创建版本后，是否自动更新 `current_version_id` 必须由 API 明确提供策略；第一版建议创建版本后显式发布，避免上传中间版本立即被 Schedule 使用。

---

## 11. Worker 通信方案定稿

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

### 11.1 Worker -> Master 消息

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

### 11.2 Master -> Worker 消息

第一版消息：

```text
Welcome
AssignTask
CancelTask
Ping
```

### 11.3 Worker 调度匹配规则

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

### 11.4 幂等字段

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

### 11.5 Worker 离线处理

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

## 12. Worker Runtime API / Python SDK 定稿

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

## 13. Schedule API 定稿

Schedule 是定时触发规则，不是执行记录。

关系：

```text
Bot -> Schedule -> ScheduleRun -> Task
```

### 13.1 接口索引

本节只列出接口路径和用途。`Schedule`、`ScheduleRun` 的共享返回字段与每个接口的请求字段分开定义。

| 方法 | 路径 | 用途 | 详细说明 |
|---|---|---|---|
| `POST` | `/api/schedules` | 创建 Schedule | `13.5 创建 Schedule` |
| `GET` | `/api/schedules` | 分页查询 Schedule | `13.6 查询 Schedule 列表` |
| `GET` | `/api/schedules/{schedule_id}` | 查询 Schedule 详情 | `13.7 查询 Schedule 详情` |
| `PATCH` | `/api/schedules/{schedule_id}` | 更新 Schedule | `13.8 更新 / 启停 Schedule` |
| `POST` | `/api/schedules/{schedule_id}/enable` | 启用 Schedule | `13.8 更新 / 启停 Schedule` |
| `POST` | `/api/schedules/{schedule_id}/disable` | 禁用 Schedule | `13.8 更新 / 启停 Schedule` |
| `POST` | `/api/schedules/{schedule_id}/trigger` | 立即手动触发一次 | `13.9 手动触发 Schedule` |
| `GET` | `/api/schedules/{schedule_id}/runs` | 查询 ScheduleRun 列表 | `13.10 ScheduleRun 接口` |
| `GET` | `/api/schedule-runs/{run_id}` | 查询 ScheduleRun 详情 | `13.10 ScheduleRun 接口` |

### 13.2 共享返回对象：Schedule

以下字段表定义 Schedule 列表和详情接口返回的 Schedule 对象。创建、更新和触发接口只使用其中一部分字段，具体以对应小节为准。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | Schedule ID |
| `name` | string | 是 | 定时任务名称 |
| `description` | string | 否 | 说明 |
| `bot_id` | string | 是 | 被触发的 Bot |
| `bot_version_id` | string | 否 | 固定版本；为空表示触发时使用 Bot 当前启用版本 |
| `cron` | string | 是 | 标准 5 段 cron 表达式 |
| `timezone` | string | 是 | cron 解释时区，例如 `Asia/Shanghai` |
| `input_source` | string | 是 | 每次触发创建 Task 时的输入来源 |
| `input_file_id` | string | 否 | 定时执行固定输入文件时使用；也可为空，由脚本自行拉取数据 |
| `input_params` | object | 否 | 每次触发传给 Task 的 JSON 参数 |
| `config` | object | 否 | 每次触发传给 Task 的运行配置覆盖项 |
| `requirements` | object | 否 | 每次触发传给 Task 的运行要求覆盖项 |
| `overlap_policy` | string | 是 | 到点时上一轮未完成的处理策略 |
| `missed_run_policy` | string | 是 | Master 停机或延迟导致错过触发时间时的处理策略 |
| `max_parallel_runs` | integer | 是 | `overlap_policy=parallel` 时允许的最大并行 Task 数 |
| `status` | string | 是 | ScheduleStatus |
| `last_run_at` | string | 否 | 最近一次触发决策时间，不一定创建了 Task |
| `last_task_id` | string | 否 | 最近一次成功创建的 Task ID |
| `next_run_at` | string | 否 | 下一次计划触发时间，用于前端展示 |
| `created_by` | string | 是 | 创建人 |
| `created_at` | string | 是 | 创建时间 |
| `updated_at` | string | 是 | 更新时间 |
| `archived_at` | string | 否 | 归档时间 |

cron 使用标准 5 段表达式：

```text
minute hour day-of-month month day-of-week
```

cron 按 `timezone` 解释。第一版不建议使用秒级 cron，避免调度频率过高。

ScheduleStatus：

| 值 | 含义 |
|---|---|
| `enabled` | 启用，到点会触发 |
| `disabled` | 禁用，到点不会触发 |
| `archived` | 归档，默认列表隐藏且不可触发 |

### 13.3 overlap_policy

| 值 | 含义 | 第一版建议 |
|---|---|---|
| `skip` | 如果上一轮仍有 active Task，本轮跳过，只记录 ScheduleRun | MVP 默认实现 |
| `queue` | 到点后排队，等上一轮结束再创建 Task | 预留 |
| `replace` | 取消上一轮并创建新 Task | 预留，风险较高 |
| `parallel` | 允许并行创建多个 Task | 可作为后续增强 |

active Task 状态：

```text
pending
dispatching
running
canceling
```

第一版默认：

```text
overlap_policy = skip
```

### 13.4 missed_run_policy

| 值 | 含义 |
|---|---|
| `skip` | 错过的触发不补跑 |
| `run_once` | 如果错过多次，只补跑一次 |

第一版默认建议：

```text
missed_run_policy = skip
```

如果团队更重视“业务不能漏跑”，可以把默认值调整为 `run_once`，但需要在 ScheduleRun 中记录补跑原因。

### 13.5 创建 Schedule

```http
POST /api/schedules
```

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `name` | string | 是 | - | Schedule 名称 |
| `description` | string | 否 | `null` | 描述 |
| `bot_id` | string | 是 | - | 目标 Bot |
| `bot_version_id` | string | 否 | `null` | 固定执行版本 |
| `cron` | string | 是 | - | 5 段 cron |
| `timezone` | string | 否 | `Asia/Shanghai` | 时区 |
| `input_source` | string | 是 | - | 输入来源 |
| `input_file_id` | string | 否 | `null` | 输入文件 |
| `input_params` | object | 否 | `{}` | JSON 输入参数 |
| `config` | object | 否 | `{}` | 运行配置 |
| `requirements` | object | 否 | `{}` | 运行要求 |
| `overlap_policy` | string | 否 | `skip` | 重叠策略 |
| `missed_run_policy` | string | 否 | `skip` | 错过触发策略 |
| `max_parallel_runs` | integer | 否 | `1` | 最大并行数 |
| `enabled` | boolean | 否 | `true` | 是否创建后立即启用 |

请求示例：

```json
{
  "name": "每天 9 点提交审批",
  "bot_id": "bot_oem_approval",
  "cron": "0 9 * * *",
  "timezone": "Asia/Shanghai",
  "input_source": "params",
  "input_params": {
    "business_date": "today"
  },
  "overlap_policy": "skip",
  "missed_run_policy": "skip"
}
```

创建成功后返回 Schedule 对象，并计算 `next_run_at`。

### 13.6 查询 Schedule 列表

```http
GET /api/schedules
```

Query 字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `bot_id` | string | 按 Bot 过滤 |
| `status` | string | `enabled`、`disabled`、`archived` |
| `q` | string | 搜索 name、description |
| `include_archived` | boolean | 是否包含归档数据，默认 false |

列表项至少返回：

```text
id, name, bot_id, cron, timezone, status,
overlap_policy, missed_run_policy,
last_run_at, last_task_id, next_run_at,
created_at, updated_at
```

### 13.7 查询 Schedule 详情

```http
GET /api/schedules/{schedule_id}
```

响应返回完整 `Schedule` 共享对象，并可附带：

```text
current_active_task_count
last_schedule_run
can_edit
can_trigger
can_enable
can_disable
```

这些操作能力字段由后端根据 Schedule 状态、关联 Bot 状态和当前用户权限计算，不是持久化字段。

### 13.8 更新 / 启停 Schedule

```http
PATCH /api/schedules/{schedule_id}
POST  /api/schedules/{schedule_id}/enable
POST  /api/schedules/{schedule_id}/disable
```

允许 PATCH 的字段：

```text
name
description
bot_version_id
cron
timezone
input_source
input_file_id
input_params
config
requirements
overlap_policy
missed_run_policy
max_parallel_runs
```

规则：

```text
修改 cron/timezone 后必须重新计算 next_run_at
禁用 Schedule 不影响已经创建的 Task
归档 Schedule 后不再触发，但历史 ScheduleRun 保留
```

### 13.9 手动触发 Schedule

```http
POST /api/schedules/{schedule_id}/trigger
```

手动触发表示立即按 Schedule 配置创建一次 Task，但 Schedule 本身的 cron 规则不变。

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `reason` | string | 否 | `manual_trigger` | 触发原因 |
| `override_input_params` | object | 否 | `null` | 本次触发覆盖 input_params |
| `override_config` | object | 否 | `null` | 本次触发覆盖 config |

响应应返回本次 ScheduleRun 和新 Task：

```json
{
  "data": {
    "schedule_run": {
      "id": "schrun_xxx",
      "status": "task_created",
      "reason": "manual_trigger"
    },
    "task": {
      "id": "task_xxx",
      "status": "pending"
    }
  }
}
```

### 13.10 ScheduleRun 接口与共享返回对象

```http
GET /api/schedules/{schedule_id}/runs
GET /api/schedule-runs/{run_id}
```

以上两个接口分别返回 ScheduleRun 列表和单个 ScheduleRun 详情。两者共用下面的 ScheduleRun 对象字段定义。

ScheduleRun 记录每一次“触发决策”，不只记录成功创建 Task 的情况。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | ScheduleRun ID |
| `schedule_id` | string | 是 | 所属 Schedule |
| `bot_id` | string | 是 | 冗余 Bot ID |
| `planned_at` | string | 是 | 理论计划触发时间 |
| `triggered_at` | string | 是 | 实际做出触发决策的时间 |
| `status` | string | 是 | ScheduleRunStatus |
| `reason` | string | 否 | skipped/failed 的原因，或手动触发原因 |
| `task_id` | string | 否 | 如果创建了 Task，记录 Task ID |
| `overlap_policy` | string | 是 | 当时采用的 overlap 策略快照 |
| `missed_run_policy` | string | 是 | 当时采用的 missed 策略快照 |
| `error_code` | string | 否 | 创建失败错误码 |
| `error_message` | string | 否 | 创建失败说明 |
| `created_at` | string | 是 | 记录创建时间 |

ScheduleRunStatus：

| 值 | 含义 |
|---|---|
| `task_created` | 已创建 Task |
| `skipped` | 本轮触发被策略跳过 |
| `failed` | 本轮触发尝试失败，例如输入非法或数据库错误 |

常见 reason：

| reason | 含义 |
|---|---|
| `previous_task_running` | 上一轮 active Task 未结束，且 overlap_policy=skip |
| `bot_disabled` | Bot 当前不可运行 |
| `invalid_input` | Schedule 配置的输入非法 |
| `create_task_failed` | 创建 Task 失败 |
| `missed_run_skipped` | 错过触发且策略选择跳过 |
| `max_parallel_runs_reached` | 达到并行上限 |
| `manual_trigger` | 用户手动触发 |

---

## 14. Result / Artifact / Log API 定稿

Result、Artifact、Log 分别解决三个不同问题：

| 类型 | 含义 | 示例 |
|---|---|---|
| `Result` | 结构化业务结果 | 审批提交结果、订单状态、抓取到的数据记录 |
| `Artifact` | 文件或大内容 | 截图、Excel 导出、PDF、上传回执 |
| `Log` | 运行过程和排错信息 | stdout、业务日志、Worker 日志 |

### 14.1 Result API

Result 保存结构化业务结果。

#### 14.1.1 接口索引

| 方法 | 路径 | 用途 | 说明 |
|---|---|---|---|
| `GET` | `/api/results` | 分页查询 Result | 查询字段见 `14.1.3` |
| `GET` | `/api/results/{result_id}` | 查询 Result 详情 | 返回完整 Result 对象 |
| `GET` | `/api/tasks/{task_id}/results` | 查询某个 Task 的 Result | 查询字段见 `14.1.3` |
| `GET` | `/api/tasks/{task_id}/items/{item_id}/results` | 查询某个 TaskItem 的 Result | 查询字段见 `14.1.3` |
| `POST` | `/api/tasks/{task_id}/results/export` | 导出 Task Result | 请求字段见 `14.1.4` |

#### 14.1.2 共享返回对象：Result

以下字段表同时适用于 Result 列表接口、Result 详情接口以及 Task/TaskItem 关联查询接口。

| 字段 | 类型 | 必填 | 说明 |

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | Result ID |
| `task_id` | string | 是 | 所属 Task |
| `task_item_id` | string | 否 | 所属 TaskItem；Task 级结果为空 |
| `bot_id` | string | 是 | 冗余 Bot ID |
| `type` | string | 是 | 结果类型，例如 `approval_result`、`crawl_record`、`status_snapshot` |
| `key` | string | 否 | 业务 key，例如外部订单号、URL、Excel 行号 |
| `data` | object | 是 | 结构化结果数据，禁止存放大文件内容 |
| `idempotency_key` | string | 否 | SDK 上报幂等键，同一 Task 内唯一 |
| `created_at` | string | 是 | 创建时间 |
| `updated_at` | string | 是 | 更新时间 |

#### 14.1.3 查询 Result 列表接口参数

以下 Query 字段适用于 `GET /api/results`、`GET /api/tasks/{task_id}/results` 和 `GET /api/tasks/{task_id}/items/{item_id}/results`。路径参数已经提供的过滤条件不需要重复传入。

查询 Result 列表支持：

| Query 字段 | 类型 | 说明 |
|---|---|---|
| `task_id` | string | 按 Task 过滤 |
| `task_item_id` | string | 按 TaskItem 过滤 |
| `bot_id` | string | 按 Bot 过滤 |
| `type` | string | 按结果类型过滤 |
| `key` | string | 按业务 key 精确查询 |
| `q` | string | 搜索 key 或 data 摘要 |

#### 14.1.4 导出 Result 接口

```http
POST /api/tasks/{task_id}/results/export
```

请求字段：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `format` | string | 是 | - | `csv`、`xlsx`、`json` |
| `type` | string | 否 | `null` | 只导出某类 Result |
| `fields` | array<string> | 否 | 全部字段 | 指定导出字段 |
| `filters` | object | 否 | `{}` | 导出过滤条件 |

导出结果生成 Artifact，响应返回 Artifact：

```json
{
  "data": {
    "artifact_id": "artifact_export_xxx",
    "status": "created"
  }
}
```

### 14.2 Artifact API

Artifact 保存文件和大内容，API 只返回元数据，下载接口返回文件内容。

#### 14.2.1 接口索引

| 方法 | 路径 | 用途 | 说明 |
|---|---|---|---|
| `GET` | `/api/artifacts` | 分页查询 Artifact | 返回 Artifact 元数据 |
| `GET` | `/api/artifacts/{artifact_id}` | 查询 Artifact 详情 | 返回完整 Artifact 元数据 |
| `GET` | `/api/artifacts/{artifact_id}/download` | 下载 Artifact 文件内容 | 返回二进制响应 |
| `GET` | `/api/tasks/{task_id}/artifacts` | 查询 Task 的 Artifact | 返回 Artifact 元数据列表 |
| `GET` | `/api/tasks/{task_id}/items/{item_id}/artifacts` | 查询 TaskItem 的 Artifact | 返回 Artifact 元数据列表 |

#### 14.2.2 共享返回对象：Artifact

以下字段表适用于 Artifact 列表、详情和关联查询接口；下载接口返回文件内容，不返回此对象作为主体。

| 字段 | 类型 | 必填 | 说明 |

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | Artifact ID |
| `task_id` | string | 是 | 所属 Task |
| `task_item_id` | string | 否 | 所属 TaskItem；Task 级文件为空 |
| `bot_id` | string | 是 | 冗余 Bot ID |
| `name` | string | 是 | 文件展示名 |
| `type` | string | 是 | 文件类型，例如 `screenshot`、`export`、`attachment`、`debug_bundle` |
| `content_type` | string | 是 | MIME 类型，例如 `image/png` |
| `size` | integer | 是 | 文件大小，字节 |
| `storage_backend` | string | 是 | 存储后端，第一版为 `local` |
| `storage_key` | string | 是 | 后端内部 key，不应直接暴露可访问 URL |
| `checksum` | string | 否 | 文件校验值，建议 `sha256:<hex>` |
| `idempotency_key` | string | 否 | SDK 上报幂等键 |
| `created_at` | string | 是 | 创建时间 |
| `updated_at` | string | 是 | 更新时间 |

Artifact type 推荐值：

| 值 | 含义 |
|---|---|
| `screenshot` | 截图 |
| `download` | 脚本下载到的业务文件 |
| `upload_receipt` | 外部系统上传/提交回执 |
| `export` | Result 导出文件 |
| `debug_bundle` | 失败现场、HAR、日志包等调试文件 |
| `custom` | 自定义文件 |

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

下载规则：

```text
GET /api/artifacts/{artifact_id}/download 必须鉴权
下载时后端根据权限校验用户是否可访问对应 Task/Bot
响应头设置 Content-Type 和 Content-Disposition
不在 Artifact 元数据中直接返回可长期访问的 storage path
```

### 14.3 Log API

Log 保存运行过程和排错信息。

#### 14.3.1 接口索引

| 方法 | 路径 | 用途 | 说明 |
|---|---|---|---|
| `GET` | `/api/logs` | 分页查询全局日志 | 需要较高权限，默认按时间倒序 |
| `GET` | `/api/tasks/{task_id}/logs` | 查询 Task 日志 | 返回历史日志列表 |
| `GET` | `/api/tasks/{task_id}/items/{item_id}/logs` | 查询 TaskItem 日志 | 返回关联日志列表 |
| `GET` | `/api/tasks/{task_id}/logs/stream` | 订阅 Task 实时日志 | SSE 长连接 |

实时日志第一版优先使用 SSE。

#### 14.3.2 共享返回对象：Log

以下字段表适用于历史日志查询接口和 SSE 的 `log` 事件；SSE 的 `task_status`、`done` 事件使用各自的事件字段。

| 字段 | 类型 | 必填 | 说明 |

| 字段 | 类型 | 必填 | 说明 |
|---|---|---:|---|
| `id` | string | 是 | Log ID |
| `task_id` | string | 是 | 所属 Task |
| `task_item_id` | string | 否 | 所属 TaskItem |
| `bot_id` | string | 是 | 冗余 Bot ID |
| `worker_id` | string | 否 | 产生日志的 Worker |
| `level` | string | 是 | 日志级别 |
| `message` | string | 是 | 日志正文，必须脱敏 |
| `fields` | object | 否 | 结构化字段，必须脱敏 |
| `source` | string | 是 | 日志来源 |
| `seq` | integer | 否 | Task 内单调递增序号，用于前端增量加载 |
| `created_at` | string | 是 | 日志时间 |

level：

| 值 | 含义 |
|---|---|
| `debug` | 调试日志 |
| `info` | 普通信息 |
| `warning` | 警告 |
| `error` | 错误 |

source：

| 值 | 含义 |
|---|---|
| `script` | Python 脚本输出或 SDK 日志 |
| `worker` | Worker 执行器日志 |
| `runtime` | Worker Runtime 日志 |
| `master` | Master 调度和状态日志 |
| `system` | 系统生成日志 |

查询日志支持：

| Query 字段 | 类型 | 说明 |
|---|---|---|
| `level` | string | 按级别过滤 |
| `source` | string | 按来源过滤 |
| `task_item_id` | string | 按 TaskItem 过滤 |
| `since_seq` | integer | 只返回某个 seq 之后的日志 |
| `limit` | integer | 返回条数，默认 200，最大 1000 |
| `q` | string | 搜索 message |

SSE 事件：

```http
GET /api/tasks/{task_id}/logs/stream
```

事件建议：

```text
event: log
data: {"id":"log_xxx","seq":12,"level":"info","message":"started"}

event: task_status
data: {"task_id":"task_xxx","status":"running"}

event: done
data: {"task_id":"task_xxx","status":"success"}
```

日志保留策略建议：

```text
第一版可写数据库
默认每个 Task 最多保留 10,000 行或保留 7 天
超过限制时优先丢弃 debug 日志
error 日志应尽量保留
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

## 15. 数据表结构定稿

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

### 15.1 bots

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

### 15.2 bot_versions

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

### 15.3 tasks

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

### 15.4 task_items

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

### 15.5 workers

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

### 15.6 schedules

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

### 15.7 schedule_runs

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

### 15.8 results

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

### 15.9 artifacts

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

### 15.10 logs

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

### 15.11 source_files

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

### 15.12 task_events

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

## 16. 第一版 MVP 范围裁剪

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

## 17. 安全与敏感信息规则

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

## 18. 第一版最终定稿结论

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
