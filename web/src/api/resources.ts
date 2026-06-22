import { useAuthStore } from "@/stores/auth"
import { api } from "./client"

// ---------------------------------------------------------------------------
// Spiders
// ---------------------------------------------------------------------------

export interface Spider {
  id: number
  project_id: number
  name: string
  description?: string
  status: "active" | "paused" | "archived"
  entry_module: string
  source_version: number
  config: Record<string, unknown>
  git_url?: string
  git_branch?: string
  last_synced_at?: string
  last_sync_commit?: string
  last_sync_error?: string
  created_at: string
  updated_at: string
}

export interface SpiderCreateInput {
  name: string
  description?: string
  entry_module: string
  config?: Record<string, unknown>
  git_url?: string
  git_branch?: string
}

export const spidersApi = {
  list: () => api<{ items: Spider[] }>("/api/spiders"),
  get: (id: number) => api<Spider>(`/api/spiders/${id}`),
  create: (input: SpiderCreateInput) =>
    api<Spider>("/api/spiders", { method: "POST", json: input }),
  sync: (id: number) => api<Spider>(`/api/spiders/${id}/sync`, { method: "POST" }),
  remove: (id: number) => api<void>(`/api/spiders/${id}`, { method: "DELETE" }),
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

export type TaskStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "timeout"
  | "captcha_blocked"

export interface Task {
  id: number
  spider_id: number
  parent_task_id?: number
  trigger: "manual" | "schedule" | "retry" | "api"
  status: TaskStatus
  spider_version: number
  worker_id?: string
  queued_at: string
  started_at?: string
  finished_at?: string
  error?: string
  error_class?: string
  stats: Record<string, unknown>
  attempt: number
  not_before?: string
}

export interface TaskItem {
  id: number
  task_id: number
  spider_id: number
  payload: unknown
  payload_hash: string
  created_at: string
}

export interface TaskScreenshot {
  id: number
  task_id: number
  taken_at: string
  name: string
  url: string // presigned MinIO URL
  page_url: string
  width: number
  height: number
  bytes: number
}

export const tasksApi = {
  list: () => api<{ items: Task[] }>("/api/tasks"),
  get: (id: number) => api<Task>(`/api/tasks/${id}`),
  create: (spiderId: number, args?: Record<string, unknown>) =>
    api<Task>("/api/tasks", {
      method: "POST",
      json: { spider_id: spiderId, args },
    }),
  cancel: (id: number) => api<void>(`/api/tasks/${id}/cancel`, { method: "POST" }),
  items: (id: number, params?: { limit?: number; offset?: number }) =>
    api<{ items: TaskItem[] }>(`/api/tasks/${id}/items`, { params }),
  screenshots: (id: number) =>
    api<{ items: TaskScreenshot[] }>(`/api/tasks/${id}/screenshots`),
  /**
   * Build a WebSocket URL for live log tail. The token comes in as a query
   * param because browsers can't set Authorization headers on WS upgrades.
   * In dev, vite proxies /api → backend with ws:true so this works seamlessly.
   */
  logStreamURL: (id: number): string => {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
    const token = useAuthStore.getState().token ?? ""
    return `${proto}//${window.location.host}/api/tasks/${id}/log/stream?token=${encodeURIComponent(token)}`
  },
}

// ---------------------------------------------------------------------------
// Workers
// ---------------------------------------------------------------------------

export interface Worker {
  worker_id: string
  session_id: string
  capabilities: string[]
  concurrency: number
  free_slots: number
  running: number
}

export const workersApi = {
  list: () => api<{ items: Worker[] }>("/api/workers"),
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

export interface Schedule {
  id: number
  spider_id: number
  name: string
  cron_expr: string
  args: Record<string, unknown>
  enabled: boolean
  last_run_at?: string
  last_task_id?: number
  next_run_at?: string
  created_at: string
  updated_at: string
}

export interface ScheduleCreateInput {
  spider_id: number
  name: string
  cron_expr: string
  args?: Record<string, unknown>
  enabled?: boolean
}

export interface ScheduleUpdateInput {
  name: string
  cron_expr: string
  args?: Record<string, unknown>
  enabled?: boolean
}

export const schedulesApi = {
  list: () => api<{ items: Schedule[] }>("/api/schedules"),
  get: (id: number) => api<Schedule>(`/api/schedules/${id}`),
  create: (input: ScheduleCreateInput) =>
    api<Schedule>("/api/schedules", { method: "POST", json: input }),
  update: (id: number, input: ScheduleUpdateInput) =>
    api<Schedule>(`/api/schedules/${id}`, { method: "PATCH", json: input }),
  remove: (id: number) =>
    api<void>(`/api/schedules/${id}`, { method: "DELETE" }),
}
