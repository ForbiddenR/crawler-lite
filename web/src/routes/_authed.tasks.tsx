import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"

import { spidersApi, tasksApi } from "@/api/resources"
import { Card, CardBody } from "@/components/ui/card"
import { FoldableMessage } from "@/components/ui/foldable-message"
import { StatusBadge } from "@/components/ui/status-badge"
import { fmtDuration, fmtTime } from "@/lib/format"

export const Route = createFileRoute("/_authed/tasks")({
  component: TasksPage,
})

function TasksPage() {
  // Lightweight polling — running tasks transition often enough that 3s is
  // a sensible default before SSE/WS push for the list.
  const tasks = useQuery({
    queryKey: ["tasks"],
    queryFn: () => tasksApi.list(),
    refetchInterval: 3_000,
  })
  const spiders = useQuery({
    queryKey: ["spiders"],
    queryFn: () => spidersApi.list(),
  })

  // Build a lookup so we can show the spider name per row without N+1 fetches.
  const spiderByID = new Map((spiders.data?.items ?? []).map((s) => [s.id, s]))

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <h1 className="text-xl font-semibold">Tasks</h1>

      <Card>
        <CardBody className="p-0">
          {tasks.isLoading ? (
            <p className="p-6 text-sm text-zinc-500">Loading...</p>
          ) : (tasks.data?.items?.length ?? 0) === 0 ? (
            <p className="p-6 text-sm text-zinc-500">
              No tasks yet. Run a spider from the spiders page.
            </p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-left text-zinc-500">
                <tr className="border-b border-zinc-200">
                  <th className="px-6 py-3 font-medium">ID</th>
                  <th className="px-6 py-3 font-medium">Spider</th>
                  <th className="px-6 py-3 font-medium">Status</th>
                  <th className="px-6 py-3 font-medium">Trigger</th>
                  <th className="px-6 py-3 font-medium">Queued</th>
                  <th className="px-6 py-3 font-medium">Duration</th>
                </tr>
              </thead>
              <tbody>
                {(tasks.data?.items ?? []).map((t) => {
                  const s = spiderByID.get(t.spider_id)
                  return (
                    <tr key={t.id} className="border-b border-zinc-100 last:border-b-0">
                      <td className="px-6 py-3 font-mono text-xs">
                        <Link
                          to="/tasks/$id"
                          params={{ id: String(t.id) }}
                          className="text-zinc-900 hover:underline"
                        >
                          #{t.id}
                        </Link>
                      </td>
                      <td className="px-6 py-3">
                        {s ? (
                          <Link
                            to="/spiders/$id"
                            params={{ id: String(t.spider_id) }}
                            className="hover:underline"
                          >
                            {s.name}
                          </Link>
                        ) : (
                          <span className="text-zinc-400">spider {t.spider_id}</span>
                        )}
                      </td>
                      <td className="px-6 py-3 align-top">
                        <StatusBadge status={t.status} />
                        {t.error &&
                        (t.status === "failed" ||
                          t.status === "timeout" ||
                          t.status === "captcha_blocked") ? (
                          <FoldableMessage
                            message={t.error}
                            className="mt-1 max-w-[420px] text-xs text-red-600"
                          />
                        ) : null}
                      </td>
                      <td className="px-6 py-3 text-xs text-zinc-600">{t.trigger}</td>
                      <td className="px-6 py-3 text-xs text-zinc-500">{fmtTime(t.queued_at)}</td>
                      <td className="px-6 py-3 text-xs text-zinc-500">
                        {fmtDuration(t.started_at, t.finished_at)}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </CardBody>
      </Card>
    </div>
  )
}
