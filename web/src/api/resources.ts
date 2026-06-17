import { api } from "./client"

export interface Spider {
  id: number
  project_id: number
  name: string
  description?: string
  status: "active" | "paused" | "archived"
  entry_module: string
  source_version: number
  config: Record<string, unknown>
  created_at: string
  updated_at: string
}

export const spidersApi = {
  list: () => api<{ items: Spider[] }>("/api/spiders"),
  get: (id: number) => api<Spider>(`/api/spiders/${id}`),
}

export interface Task {
  id: number
  spider_id: number
  trigger: "manual" | "schedule" | "retry" | "api"
  status:
    | "queued"
    | "running"
    | "succeeded"
    | "failed"
    | "cancelled"
    | "timeout"
    | "captcha_blocked"
  spider_version: number
  worker_id?: string
  queued_at: string
  started_at?: string
  finished_at?: string
  error?: string
  stats: Record<string, unknown>
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
}

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
