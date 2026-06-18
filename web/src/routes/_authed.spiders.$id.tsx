import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router"

import { spidersApi, tasksApi } from "@/api/resources"
import { Button } from "@/components/ui/button"
import { Card, CardBody, CardHeader } from "@/components/ui/card"
import { StatusBadge } from "@/components/ui/status-badge"
import { fmtDuration, fmtTime } from "@/lib/format"

export const Route = createFileRoute("/_authed/spiders/$id")({
  component: SpiderDetailPage,
})

function SpiderDetailPage() {
  const { id } = Route.useParams()
  const spiderID = Number(id)
  const qc = useQueryClient()
  const navigate = useNavigate()

  const spider = useQuery({
    queryKey: ["spider", spiderID],
    queryFn: () => spidersApi.get(spiderID),
  })

  // Filter tasks to this spider client-side. Once the API grows a server-side
  // filter (?spider_id=…) this should switch to that.
  const tasks = useQuery({
    queryKey: ["tasks"],
    queryFn: () => tasksApi.list(),
    refetchInterval: 5_000,
  })

  const sync = useMutation({
    mutationFn: () => spidersApi.sync(spiderID),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["spider", spiderID] }),
  })

  const run = useMutation({
    mutationFn: () => tasksApi.create(spiderID),
    onSuccess: (task) => {
      qc.invalidateQueries({ queryKey: ["tasks"] })
      void navigate({ to: "/tasks/$id", params: { id: String(task.id) } })
    },
  })

  const recent = (tasks.data?.items ?? [])
    .filter((t) => t.spider_id === spiderID)
    .slice(0, 20)

  if (spider.isLoading) return <div className="p-6 text-sm text-zinc-500">Loading...</div>
  if (!spider.data)
    return <div className="p-6 text-sm text-red-600">Spider not found.</div>

  const s = spider.data
  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-xl font-semibold">{s.name}</h1>
          {s.description && (
            <p className="mt-1 text-sm text-zinc-500">{s.description}</p>
          )}
        </div>
        <div className="flex gap-2">
          {s.git_url && (
            <Button
              variant="secondary"
              onClick={() => sync.mutate()}
              disabled={sync.isPending}
            >
              {sync.isPending ? "Syncing..." : "Sync source"}
            </Button>
          )}
          <Button
            onClick={() => run.mutate()}
            disabled={s.source_version === 0 || run.isPending}
          >
            {run.isPending ? "Queueing..." : "Run task"}
          </Button>
        </div>
      </div>

      <Card>
        <CardBody>
          <dl className="grid grid-cols-2 gap-y-3 text-sm md:grid-cols-4">
            <Row label="Entry">
              <span className="font-mono text-xs">{s.entry_module}</span>
            </Row>
            <Row label="Status">{s.status}</Row>
            <Row label="Source version">
              {s.source_version > 0 ? `v${s.source_version}` : "—"}
            </Row>
            <Row label="Last sync">
              {s.last_synced_at ? fmtTime(s.last_synced_at) : "never"}
            </Row>
            <Row label="Git URL">
              <span className="font-mono text-xs">{s.git_url || "—"}</span>
            </Row>
            <Row label="Branch">
              <span className="font-mono text-xs">{s.git_branch || "—"}</span>
            </Row>
            <Row label="Last commit">
              <span className="font-mono text-xs">
                {s.last_sync_commit ? s.last_sync_commit.slice(0, 12) : "—"}
              </span>
            </Row>
            <Row label="Updated">{fmtTime(s.updated_at)}</Row>
          </dl>
          {s.last_sync_error && (
            <p className="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-700">
              <span className="font-medium">Last sync error: </span>
              {s.last_sync_error}
            </p>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="text-base font-medium">Recent tasks</h2>
        </CardHeader>
        <CardBody className="p-0">
          {recent.length === 0 ? (
            <p className="p-6 text-sm text-zinc-500">No tasks yet.</p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-left text-zinc-500">
                <tr className="border-b border-zinc-200">
                  <th className="px-6 py-3 font-medium">ID</th>
                  <th className="px-6 py-3 font-medium">Status</th>
                  <th className="px-6 py-3 font-medium">Queued</th>
                  <th className="px-6 py-3 font-medium">Duration</th>
                </tr>
              </thead>
              <tbody>
                {recent.map((t) => (
                  <tr key={t.id} className="border-b border-zinc-100 last:border-b-0">
                    <td className="px-6 py-3 font-mono text-xs">
                      <Link
                        to="/tasks/$id"
                        params={{ id: String(t.id) }}
                        className="hover:underline"
                      >
                        #{t.id}
                      </Link>
                    </td>
                    <td className="px-6 py-3">
                      <StatusBadge status={t.status} />
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-500">
                      {fmtTime(t.queued_at)}
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-500">
                      {fmtDuration(t.started_at, t.finished_at)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardBody>
      </Card>
    </div>
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
