import type { TaskStatus } from "@/api/resources"

/**
 * Format a timestamp as a short, locale-aware datetime. Empty string when
 * the input is missing — handlers return undefined for not-yet-set fields
 * (started_at/finished_at).
 */
export function fmtTime(s?: string | null): string {
  if (!s) return ""
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ""
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

/** Human-readable duration, e.g. "1m 23s", "742ms", "2h 5m". */
export function fmtDuration(startISO?: string | null, endISO?: string | null): string {
  if (!startISO) return ""
  const start = new Date(startISO).getTime()
  const end = endISO ? new Date(endISO).getTime() : Date.now()
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return ""
  let ms = end - start
  if (ms < 1_000) return `${ms}ms`
  const s = Math.floor(ms / 1_000) % 60
  ms = Math.floor(ms / 1_000)
  const m = Math.floor(ms / 60) % 60
  const h = Math.floor(ms / 3_600)
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

/** Tailwind classes for a status badge. */
export function statusClass(status: TaskStatus): string {
  switch (status) {
    case "running":
      return "bg-blue-100 text-blue-700"
    case "succeeded":
      return "bg-emerald-100 text-emerald-700"
    case "failed":
    case "timeout":
      return "bg-red-100 text-red-700"
    case "cancelled":
      return "bg-zinc-200 text-zinc-700"
    case "captcha_blocked":
      return "bg-amber-100 text-amber-800"
    default:
      return "bg-zinc-100 text-zinc-600"
  }
}
