import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { useEffect, useRef, useState } from "react"

import { type TaskStatus, tasksApi } from "@/api/resources"
import { Button } from "@/components/ui/button"
import { Card, CardBody } from "@/components/ui/card"
import { FoldableMessage } from "@/components/ui/foldable-message"
import { StatusBadge } from "@/components/ui/status-badge"
import { useTaskLogStream } from "@/hooks/useTaskLogStream"
import { fmtDuration, fmtTime } from "@/lib/format"
import { cn } from "@/lib/utils"

export const Route = createFileRoute("/_authed/tasks/$id")({
  component: TaskDetailPage,
})

type Tab = "logs" | "items" | "screenshots"

function TaskDetailPage() {
  const { id } = Route.useParams()
  const taskID = Number(id)
  const qc = useQueryClient()
  const [tab, setTab] = useState<Tab>("logs")

  const task = useQuery({
    queryKey: ["task", taskID],
    queryFn: () => tasksApi.get(taskID),
    // Active tasks transition often; finished ones don't, so back off then.
    refetchInterval: (q) => (isTerminal(q.state.data?.status) ? false : 2_000),
  })

  const cancel = useMutation({
    mutationFn: () => tasksApi.cancel(taskID),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["task", taskID] }),
  })

  if (task.isLoading) return <div className="p-6 text-sm text-zinc-500">Loading...</div>
  if (!task.data) return <div className="p-6 text-sm text-red-600">Task not found.</div>

  const t = task.data
  const cancellable = t.status === "queued" || t.status === "running"

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <div className="flex items-start justify-between">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-semibold">Task #{t.id}</h1>
            <StatusBadge status={t.status} />
            {t.attempt > 1 && (
              <span
                className="rounded-full border border-amber-200 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-800"
                title={`This is attempt ${t.attempt}`}
              >
                Attempt {t.attempt}
              </span>
            )}
          </div>
          <p className="mt-1 text-sm text-zinc-500">
            <Link
              to="/spiders/$id"
              params={{ id: String(t.spider_id) }}
              className="hover:underline"
            >
              spider {t.spider_id}
            </Link>{" "}
            · v{t.spider_version} · {t.trigger}
            {t.worker_id && (
              <span className="ml-1 font-mono text-xs">· {t.worker_id.slice(0, 8)}</span>
            )}
          </p>
          {t.parent_task_id ? (
            <p className="mt-1 text-xs text-zinc-500">
              ↳ retried from{" "}
              <Link
                to="/tasks/$id"
                params={{ id: String(t.parent_task_id) }}
                className="font-mono text-zinc-700 hover:underline"
              >
                #{t.parent_task_id}
              </Link>
            </p>
          ) : null}
        </div>
        {cancellable && (
          <Button
            variant="danger"
            size="sm"
            onClick={() => cancel.mutate()}
            disabled={cancel.isPending}
          >
            Cancel
          </Button>
        )}
      </div>

      <Card>
        <CardBody>
          <dl className="grid grid-cols-2 gap-y-3 text-sm md:grid-cols-4">
            <Row label="Queued">{fmtTime(t.queued_at)}</Row>
            <Row label="Started">{fmtTime(t.started_at) || "—"}</Row>
            <Row label="Finished">{fmtTime(t.finished_at) || "—"}</Row>
            <Row label="Duration">{fmtDuration(t.started_at, t.finished_at) || "—"}</Row>
            {t.not_before && t.status === "queued" && (
              <Row label="Not before">{fmtTime(t.not_before)}</Row>
            )}
          </dl>
          {t.status === "captcha_blocked" ? (
            <div className="mt-4 rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800">
              <div className="flex items-baseline justify-between gap-3">
                <span className="font-medium">Captcha blocked</span>
                <span className="text-[10px] uppercase tracking-wide text-amber-700">
                  Won't be retried
                </span>
              </div>
              <FoldableMessage message={t.error || "(no message provided)"} className="mt-1" />
              <p className="mt-2 text-amber-700/80">
                The screenshot tab may show the page that tripped the challenge.
              </p>
            </div>
          ) : t.error && (t.status === "failed" || t.status === "timeout") ? (
            <div className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700">
              <FoldableMessage message={t.error} label="Error: " />
            </div>
          ) : null}
        </CardBody>
      </Card>

      <Card>
        <div className="flex border-b border-zinc-200">
          <TabBtn active={tab === "logs"} onClick={() => setTab("logs")}>
            Logs
          </TabBtn>
          <TabBtn active={tab === "items"} onClick={() => setTab("items")}>
            Items
          </TabBtn>
          <TabBtn active={tab === "screenshots"} onClick={() => setTab("screenshots")}>
            Screenshots
          </TabBtn>
        </div>
        <CardBody>
          {tab === "logs" && <LogsTab taskID={taskID} />}
          {tab === "items" && <ItemsTab taskID={taskID} />}
          {tab === "screenshots" && <ScreenshotsTab taskID={taskID} />}
        </CardBody>
      </Card>
    </div>
  )
}

function isTerminal(s?: TaskStatus): boolean {
  return (
    s === "succeeded" ||
    s === "failed" ||
    s === "cancelled" ||
    s === "timeout" ||
    s === "captcha_blocked"
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-zinc-500">{label}</dt>
      <dd className="mt-0.5 text-zinc-900">{children}</dd>
    </div>
  )
}

function TabBtn({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "px-6 py-3 text-sm font-medium transition",
        active ? "border-b-2 border-zinc-900 text-zinc-900" : "text-zinc-500 hover:text-zinc-900",
      )}
    >
      {children}
    </button>
  )
}

// ---------------------------------------------------------------------------
// Logs tab
// ---------------------------------------------------------------------------

function LogsTab({ taskID }: { taskID: number }) {
  const { rows, connected, error } = useTaskLogStream(taskID)
  const [autoscroll, setAutoscroll] = useState(true)
  const scrollerRef = useRef<HTMLDivElement>(null)

  // Auto-scroll on new lines unless the user has scrolled up.
  useEffect(() => {
    if (!autoscroll) return
    const el = scrollerRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [rows, autoscroll])

  return (
    <div>
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2 text-xs">
          <span
            className={cn(
              "inline-block h-2 w-2 rounded-full",
              connected ? "bg-emerald-500" : "bg-zinc-400",
            )}
          />
          <span className="text-zinc-500">
            {connected ? "live" : "disconnected"} · {rows.length} lines
          </span>
          {error && <span className="text-red-600">{error}</span>}
        </div>
        <label className="flex items-center gap-2 text-xs text-zinc-600">
          <input
            type="checkbox"
            checked={autoscroll}
            onChange={(e) => setAutoscroll(e.target.checked)}
          />
          Auto-scroll
        </label>
      </div>

      <div
        ref={scrollerRef}
        className="h-[28rem] overflow-y-auto rounded-md border border-zinc-200 bg-zinc-950 p-3 font-mono text-xs text-zinc-100"
      >
        {rows.length === 0 ? (
          <p className="text-zinc-500">Waiting for output…</p>
        ) : (
          rows.map((r) => (
            <div key={r.seq} className="flex gap-3 py-px">
              <span className="shrink-0 text-zinc-500">{tsOf(r.ts_ns)}</span>
              <span className={cn("w-12 shrink-0 font-medium", levelClass(r.level))}>
                {r.level}
              </span>
              <FoldableMessage message={r.message} className="min-w-0 flex-1" />
            </div>
          ))
        )}
      </div>
    </div>
  )
}

function tsOf(ns: number): string {
  const d = new Date(Math.floor(ns / 1_000_000))
  if (Number.isNaN(d.getTime())) return ""
  return d.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  })
}

function levelClass(level: string): string {
  switch (level.toUpperCase()) {
    case "ERROR":
      return "text-red-400"
    case "WARN":
      return "text-amber-300"
    case "DEBUG":
      return "text-zinc-500"
    default:
      return "text-emerald-300"
  }
}

// ---------------------------------------------------------------------------
// Items tab
// ---------------------------------------------------------------------------

function ItemsTab({ taskID }: { taskID: number }) {
  const items = useQuery({
    queryKey: ["task-items", taskID],
    queryFn: () => tasksApi.items(taskID, { limit: 200 }),
    refetchInterval: 5_000,
  })

  if (items.isLoading) return <p className="text-sm text-zinc-500">Loading items...</p>
  const rows = items.data?.items ?? []
  if (rows.length === 0)
    return (
      <p className="text-sm text-zinc-500">
        No items emitted yet. Use <code>self.item(...)</code> in your spider.
      </p>
    )

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="text-left text-zinc-500">
          <tr className="border-b border-zinc-200">
            <th className="py-2 pr-4 font-medium">#</th>
            <th className="py-2 pr-4 font-medium">Emitted</th>
            <th className="py-2 font-medium">Payload</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((it) => (
            <tr key={it.id} className="border-b border-zinc-100 last:border-b-0 align-top">
              <td className="py-2 pr-4 font-mono text-xs text-zinc-500">{it.id}</td>
              <td className="py-2 pr-4 text-xs text-zinc-500 whitespace-nowrap">
                {fmtTime(it.created_at)}
              </td>
              <td className="py-2">
                <pre className="overflow-x-auto rounded bg-zinc-50 p-2 text-xs">
                  {JSON.stringify(it.payload, null, 2)}
                </pre>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Screenshots tab
// ---------------------------------------------------------------------------

function ScreenshotsTab({ taskID }: { taskID: number }) {
  const shots = useQuery({
    queryKey: ["task-shots", taskID],
    queryFn: () => tasksApi.screenshots(taskID),
    refetchInterval: 5_000,
  })
  const [zoom, setZoom] = useState<string | null>(null)

  if (shots.isLoading) return <p className="text-sm text-zinc-500">Loading screenshots...</p>
  const rows = shots.data?.items ?? []
  if (rows.length === 0)
    return (
      <p className="text-sm text-zinc-500">
        No screenshots captured yet. Use <code>self.screenshot(...)</code> in your spider.
      </p>
    )

  return (
    <>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-4">
        {rows.map((s) => (
          <button
            key={s.id}
            type="button"
            className="group overflow-hidden rounded-md border border-zinc-200 text-left transition hover:border-zinc-400"
            onClick={() => s.url && setZoom(s.url)}
          >
            {s.url ? (
              <img
                src={s.url}
                alt={s.name}
                className="aspect-video w-full object-cover bg-zinc-100"
                loading="lazy"
              />
            ) : (
              <div className="aspect-video w-full bg-zinc-100" />
            )}
            <div className="p-2">
              <div className="truncate text-sm font-medium">{s.name}</div>
              <div className="text-xs text-zinc-500">
                {s.width}×{s.height} · {fmtTime(s.taken_at)}
              </div>
            </div>
          </button>
        ))}
      </div>
      {zoom && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
          onClick={() => setZoom(null)}
          onKeyDown={(e) => e.key === "Escape" && setZoom(null)}
          role="dialog"
          aria-modal="true"
          tabIndex={-1}
        >
          <img src={zoom} alt="screenshot" className="max-h-full max-w-full" />
        </div>
      )}
    </>
  )
}
