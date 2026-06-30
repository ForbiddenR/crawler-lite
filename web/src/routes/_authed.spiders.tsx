import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router"
import { type FormEvent, useEffect, useRef, useState } from "react"

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
  const [selectedIDs, setSelectedIDs] = useState<number[]>([])
  const [bulkResult, setBulkResult] = useState<{
    tone: "success" | "error"
    message: string
  } | null>(null)

  const spiders = useQuery({
    queryKey: ["spiders"],
    queryFn: () => spidersApi.list(),
  })

  const items = spiders.data?.items ?? []
  const selectedSet = new Set(selectedIDs)
  const allSelected = items.length > 0 && items.every((s) => selectedSet.has(s.id))
  const someSelected = items.some((s) => selectedSet.has(s.id)) && !allSelected
  const showToolbar = items.length > 0 || bulkResult !== null

  useEffect(() => {
    const visibleIDs = new Set(items.map((s) => s.id))
    setSelectedIDs((prev) => {
      const next = prev.filter((id) => visibleIDs.has(id))
      return next.length === prev.length ? prev : next
    })
  }, [items])

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

  const bulkRemove = useMutation({
    mutationFn: async (ids: number[]) => {
      const results = await Promise.all(
        ids.map(async (id) => {
          try {
            await spidersApi.remove(id)
            return { id, ok: true as const }
          } catch (err) {
            return { id, ok: false as const, message: getErrorMessage(err) }
          }
        }),
      )

      const deletedIDs = results.filter((r) => r.ok).map((r) => r.id)
      const failed = results.filter(
        (r): r is { id: number; ok: false; message: string } => !r.ok,
      )
      return {
        deletedIDs,
        failedCount: failed.length,
        firstError: failed[0]?.message,
      }
    },
    onSuccess: async ({ deletedIDs, failedCount, firstError }) => {
      setSelectedIDs((prev) => prev.filter((id) => !deletedIDs.includes(id)))
      await qc.invalidateQueries({ queryKey: ["spiders"] })
      setBulkResult(buildBulkDeleteResult("spider", deletedIDs.length, failedCount, firstError))
    },
  })

  function toggleSelected(id: number) {
    setBulkResult(null)
    setSelectedIDs((prev) =>
      prev.includes(id) ? prev.filter((value) => value !== id) : [...prev, id],
    )
  }

  function toggleAll(checked: boolean) {
    setBulkResult(null)
    setSelectedIDs(checked ? items.map((s) => s.id) : [])
  }

  function onDeleteSelected() {
    const ids = selectedIDs.slice()
    if (ids.length === 0) {
      return
    }
    const noun = ids.length === 1 ? "spider" : "spiders"
    if (confirm(`Delete ${ids.length} ${noun}?`)) {
      bulkRemove.mutate(ids)
    }
  }

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
        {showToolbar && (
          <CardHeader className="space-y-3">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-sm text-zinc-600">
                {selectedIDs.length} selected
              </p>
              <Button
                variant="danger"
                size="sm"
                disabled={selectedIDs.length === 0 || bulkRemove.isPending}
                onClick={onDeleteSelected}
              >
                {bulkRemove.isPending
                  ? "Deleting..."
                  : `Delete selected${selectedIDs.length > 0 ? ` (${selectedIDs.length})` : ""}`}
              </Button>
            </div>
            {bulkResult && (
              <output
                className={`block text-sm ${
                  bulkResult.tone === "error" ? "text-red-600" : "text-emerald-600"
                }`}
              >
                {bulkResult.message}
              </output>
            )}
          </CardHeader>
        )}
        <CardBody className="p-0">
          {spiders.isLoading ? (
            <p className="p-6 text-sm text-zinc-500">Loading...</p>
          ) : items.length === 0 ? (
            <p className="p-6 text-sm text-zinc-500">
              No spiders yet. Click <strong>New spider</strong> to add one.
            </p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-left text-zinc-500">
                <tr className="border-b border-zinc-200">
                  <th className="w-12 px-6 py-3">
                    <SelectAllCheckbox
                      checked={allSelected}
                      indeterminate={someSelected}
                      disabled={bulkRemove.isPending}
                      onChange={toggleAll}
                    />
                  </th>
                  <th className="px-6 py-3 font-medium">Name</th>
                  <th className="px-6 py-3 font-medium">Entry</th>
                  <th className="px-6 py-3 font-medium">Source</th>
                  <th className="px-6 py-3 font-medium">Updated</th>
                  <th className="px-6 py-3" />
                </tr>
              </thead>
              <tbody>
                {items.map((s) => (
                  <tr key={s.id} className="border-b border-zinc-100 last:border-b-0">
                    <td className="px-6 py-3 align-top">
                      <input
                        type="checkbox"
                        checked={selectedSet.has(s.id)}
                        disabled={bulkRemove.isPending}
                        onChange={() => toggleSelected(s.id)}
                        className="h-4 w-4 rounded border-zinc-300 text-zinc-900 focus:ring-zinc-400"
                        aria-label={`Select spider ${s.name}`}
                      />
                    </td>
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

function SelectAllCheckbox({
  checked,
  indeterminate,
  disabled,
  onChange,
}: {
  checked: boolean
  indeterminate: boolean
  disabled?: boolean
  onChange: (checked: boolean) => void
}) {
  const ref = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (ref.current) {
      ref.current.indeterminate = indeterminate
    }
  }, [indeterminate])

  return (
    <input
      ref={ref}
      type="checkbox"
      checked={checked}
      disabled={disabled}
      onChange={(e) => onChange(e.target.checked)}
      className="h-4 w-4 rounded border-zinc-300 text-zinc-900 focus:ring-zinc-400"
      aria-label="Select all spiders"
    />
  )
}

function buildBulkDeleteResult(
  singular: string,
  deletedCount: number,
  failedCount: number,
  firstError?: string,
) {
  const plural = `${singular}s`
  if (failedCount === 0) {
    return {
      tone: "success" as const,
      message: `Deleted ${deletedCount} ${deletedCount === 1 ? singular : plural}.`,
    }
  }

  let message = `Deleted ${deletedCount} of ${deletedCount + failedCount} selected ${plural}.`
  if (deletedCount === 0) {
    message = `Failed to delete ${failedCount} ${failedCount === 1 ? singular : plural}.`
  }
  if (firstError) {
    message += ` ${firstError}`
  }

  return { tone: "error" as const, message }
}

function getErrorMessage(err: unknown) {
  if (err instanceof ApiError) {
    return err.message
  }
  if (err instanceof Error) {
    return err.message
  }
  return "Delete failed"
}
