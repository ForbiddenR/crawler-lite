import { useEffect, useRef, useState } from "react"

import { tasksApi } from "@/api/resources"

/**
 * One log entry as it lands on the Redis channel. Matches the encoder in
 * internal/hub/sinks.go: `{task_id, ts_ns, level, message}`. The worker
 * forwards stdout/stderr through the same shape so a plain `print()`
 * becomes an INFO row.
 */
export interface LogRow {
  /** Sequential index — used as React key. */
  seq: number
  task_id: number
  ts_ns: number
  level: string
  message: string
}

interface State {
  rows: LogRow[]
  connected: boolean
  error: string | null
}

/**
 * Subscribe to the live log stream for a task. Returns the rolling window of
 * rows seen so far (capped at `cap` to bound memory) plus connection state.
 *
 * The endpoint sends one initial frame containing the full historical JSONL
 * (catch-up) followed by one frame per new line. We split each frame on \n.
 */
export function useTaskLogStream(taskID: number, cap = 5_000): State {
  const [rows, setRows] = useState<LogRow[]>([])
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const seqRef = useRef(0)

  useEffect(() => {
    if (!taskID) return

    const url = tasksApi.logStreamURL(taskID)
    const ws = new WebSocket(url)
    let cancelled = false

    ws.onopen = () => {
      if (!cancelled) setConnected(true)
    }
    ws.onclose = () => {
      if (!cancelled) setConnected(false)
    }
    ws.onerror = () => {
      if (!cancelled) setError("connection error")
    }
    ws.onmessage = (ev) => {
      if (cancelled || typeof ev.data !== "string") return
      const lines = ev.data.split("\n").filter((s) => s.length > 0)
      if (lines.length === 0) return
      const parsed: LogRow[] = []
      for (const line of lines) {
        try {
          // Encoder lives in internal/hub/sinks.go (LogSinkPubsub.Write).
          const data = JSON.parse(line) as Omit<LogRow, "seq">
          parsed.push({ seq: ++seqRef.current, ...data })
        } catch {
          // Tolerate malformed lines — surface as INFO so they're not lost.
          parsed.push({
            seq: ++seqRef.current,
            task_id: taskID,
            level: "INFO",
            message: line,
            ts_ns: Date.now() * 1_000_000,
          })
        }
      }
      setRows((cur) => {
        const next = cur.concat(parsed)
        return next.length > cap ? next.slice(next.length - cap) : next
      })
    }

    return () => {
      cancelled = true
      ws.close()
    }
  }, [taskID, cap])

  return { rows, connected, error }
}
