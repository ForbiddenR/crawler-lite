import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { spidersApi, tasksApi, workersApi } from "@/api/resources"
import { Card, CardBody, CardHeader } from "@/components/ui/card"

export const Route = createFileRoute("/_authed/dashboard")({
  component: Dashboard,
})

function Dashboard() {
  const spiders = useQuery({ queryKey: ["spiders"], queryFn: () => spidersApi.list() })
  const tasks = useQuery({ queryKey: ["tasks"], queryFn: () => tasksApi.list() })
  const workers = useQuery({
    queryKey: ["workers"],
    queryFn: () => workersApi.list(),
    refetchInterval: 5_000,
  })

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <h1 className="text-xl font-semibold">Dashboard</h1>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <StatCard label="Spiders" value={spiders.data?.items.length ?? 0} />
        <StatCard label="Tasks" value={tasks.data?.items.length ?? 0} />
        <StatCard label="Connected workers" value={workers.data?.items.length ?? 0} />
      </div>

      <Card>
        <CardHeader>
          <h2 className="text-base font-medium">Workers</h2>
        </CardHeader>
        <CardBody>
          {workers.isLoading ? (
            <p className="text-sm text-zinc-500">Loading...</p>
          ) : workers.data && workers.data.items.length > 0 ? (
            <table className="w-full text-sm">
              <thead className="text-left text-zinc-500">
                <tr>
                  <th className="pb-2 font-medium">Worker</th>
                  <th className="pb-2 font-medium">Capabilities</th>
                  <th className="pb-2 font-medium">Slots</th>
                </tr>
              </thead>
              <tbody>
                {workers.data.items.map((w) => (
                  <tr key={w.session_id} className="border-t border-zinc-100">
                    <td className="py-2 font-mono text-xs">{w.worker_id}</td>
                    <td className="py-2 text-zinc-600">{w.capabilities.join(", ")}</td>
                    <td className="py-2">
                      {w.free_slots} / {w.concurrency} free · {w.running} running
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <p className="text-sm text-zinc-500">No workers connected.</p>
          )}
        </CardBody>
      </Card>
    </div>
  )
}

function StatCard({ label, value }: { label: string; value: number }) {
  return (
    <Card>
      <CardBody>
        <p className="text-sm text-zinc-500">{label}</p>
        <p className="mt-1 text-2xl font-semibold">{value}</p>
      </CardBody>
    </Card>
  )
}
