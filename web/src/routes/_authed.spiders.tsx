import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router"
import { type FormEvent, useState } from "react"

import { ApiError } from "@/api/client"
import { spidersApi, tasksApi } from "@/api/resources"
import { Button } from "@/components/ui/button"
import { Card, CardBody, CardHeader } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { fmtTime } from "@/lib/format"

export const Route = createFileRoute("/_authed/spiders")({
  component: SpidersPage,
})

function SpidersPage() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [showCreate, setShowCreate] = useState(false)

  const spiders = useQuery({
    queryKey: ["spiders"],
    queryFn: () => spidersApi.list(),
  })

  const sync = useMutation({
    mutationFn: (id: number) => spidersApi.sync(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["spiders"] }),
  })

  const run = useMutation({
    mutationFn: (id: number) => tasksApi.create(id),
    onSuccess: (task) => {
      qc.invalidateQueries({ queryKey: ["tasks"] })
      void navigate({ to: "/tasks/$id", params: { id: String(task.id) } })
    },
  })

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Spiders</h1>
        <Button onClick={() => setShowCreate((v) => !v)}>
          {showCreate ? "Cancel" : "New spider"}
        </Button>
      </div>

      {showCreate && <CreateForm onDone={() => setShowCreate(false)} />}

      <Card>
        <CardBody className="p-0">
          {spiders.isLoading ? (
            <p className="p-6 text-sm text-zinc-500">Loading...</p>
          ) : (spiders.data?.items?.length ?? 0) === 0 ? (
            <p className="p-6 text-sm text-zinc-500">
              No spiders yet. Click <strong>New spider</strong> to add one.
            </p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-left text-zinc-500">
                <tr className="border-b border-zinc-200">
                  <th className="px-6 py-3 font-medium">Name</th>
                  <th className="px-6 py-3 font-medium">Entry</th>
                  <th className="px-6 py-3 font-medium">Source</th>
                  <th className="px-6 py-3 font-medium">Updated</th>
                  <th className="px-6 py-3" />
                </tr>
              </thead>
              <tbody>
                {(spiders.data?.items ?? []).map((s) => (
                  <tr key={s.id} className="border-b border-zinc-100 last:border-b-0">
                    <td className="px-6 py-3">
                      <Link
                        to="/spiders/$id"
                        params={{ id: String(s.id) }}
                        className="font-medium text-zinc-900 hover:underline"
                      >
                        {s.name}
                      </Link>
                      {s.description && (
                        <div className="text-xs text-zinc-500">{s.description}</div>
                      )}
                    </td>
                    <td className="px-6 py-3 font-mono text-xs text-zinc-600">
                      {s.entry_module}
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-600">
                      {s.source_version > 0 ? (
                        <>
                          v{s.source_version}{" "}
                          {s.last_sync_commit && (
                            <span className="text-zinc-400">
                              · {s.last_sync_commit.slice(0, 7)}
                            </span>
                          )}
                        </>
                      ) : (
                        <span className="text-amber-600">not synced</span>
                      )}
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-500">
                      {fmtTime(s.updated_at)}
                    </td>
                    <td className="px-6 py-3 text-right">
                      <div className="flex justify-end gap-2">
                        {s.git_url && (
                          <Button
                            variant="secondary"
                            size="sm"
                            disabled={sync.isPending && sync.variables === s.id}
                            onClick={() => sync.mutate(s.id)}
                          >
                            Sync
                          </Button>
                        )}
                        <Button
                          size="sm"
                          disabled={s.source_version === 0 || run.isPending}
                          onClick={() => run.mutate(s.id)}
                        >
                          Run
                        </Button>
                      </div>
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

// ---------------------------------------------------------------------------
// Create form
// ---------------------------------------------------------------------------

function CreateForm({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState("")
  const [entry, setEntry] = useState("")
  const [gitURL, setGitURL] = useState("")
  const [gitBranch, setGitBranch] = useState("main")
  const [error, setError] = useState<string | null>(null)

  const create = useMutation({
    mutationFn: () =>
      spidersApi.create({
        name,
        entry_module: entry,
        git_url: gitURL || undefined,
        git_branch: gitBranch || undefined,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["spiders"] })
      onDone()
    },
    onError: (err) => {
      setError(err instanceof ApiError ? err.message : "Failed to create spider")
    },
  })

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    create.mutate()
  }

  return (
    <Card>
      <CardHeader>
        <h2 className="text-base font-medium">New spider</h2>
      </CardHeader>
      <CardBody>
        <form className="grid grid-cols-1 gap-4 md:grid-cols-2" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="entry">Entry module</Label>
            <Input
              id="entry"
              placeholder="spiders.amazon:PriceSpider"
              value={entry}
              onChange={(e) => setEntry(e.target.value)}
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="git">Git URL</Label>
            <Input
              id="git"
              placeholder="https://github.com/you/spider-repo.git"
              value={gitURL}
              onChange={(e) => setGitURL(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="branch">Branch</Label>
            <Input
              id="branch"
              value={gitBranch}
              onChange={(e) => setGitBranch(e.target.value)}
            />
          </div>
          {error && (
            <p className="md:col-span-2 text-sm text-red-600" role="alert">
              {error}
            </p>
          )}
          <div className="md:col-span-2 flex justify-end gap-2">
            <Button variant="ghost" type="button" onClick={onDone}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating..." : "Create"}
            </Button>
          </div>
        </form>
      </CardBody>
    </Card>
  )
}
