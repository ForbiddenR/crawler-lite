import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { type FormEvent, useEffect, useRef, useState } from "react"

import { ApiError } from "@/api/client"
import {
  type Schedule,
  type Spider,
  schedulesApi,
  spidersApi,
} from "@/api/resources"
import { Button } from "@/components/ui/button"
import { Card, CardBody, CardHeader } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { fmtTime } from "@/lib/format"

export const Route = createFileRoute("/_authed/schedules")({
  component: SchedulesPage,
})

function SchedulesPage() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [selectedIDs, setSelectedIDs] = useState<number[]>([])
  const [bulkResult, setBulkResult] = useState<{
    tone: "success" | "error"
    message: string
  } | null>(null)

  const schedules = useQuery({
    queryKey: ["schedules"],
    queryFn: () => schedulesApi.list(),
    // Cron-driven values (last_run_at, next_run_at) drift continuously while
    // the page is open; refresh on the same cadence as tasks.
    refetchInterval: 5_000,
  })
  const spiders = useQuery({
    queryKey: ["spiders"],
    queryFn: () => spidersApi.list(),
  })

  const items = schedules.data?.items ?? []
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

  const spiderName = (id: number) =>
    spiders.data?.items?.find((s) => s.id === id)?.name ?? `#${id}`

  const toggle = useMutation({
    mutationFn: (sch: Schedule) =>
      schedulesApi.update(sch.id, {
        name: sch.name,
        cron_expr: sch.cron_expr,
        args: sch.args,
        enabled: !sch.enabled,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["schedules"] }),
  })

  const bulkRemove = useMutation({
    mutationFn: async (ids: number[]) => {
      const results = await Promise.all(
        ids.map(async (id) => {
          try {
            await schedulesApi.remove(id)
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
      await qc.invalidateQueries({ queryKey: ["schedules"] })
      setBulkResult(buildBulkDeleteResult("schedule", deletedIDs.length, failedCount, firstError))
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
    const noun = ids.length === 1 ? "schedule" : "schedules"
    if (confirm(`Delete ${ids.length} ${noun}?`)) {
      bulkRemove.mutate(ids)
    }
  }

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Schedules</h1>
        <Button
          onClick={() => setShowCreate((v) => !v)}
          disabled={(spiders.data?.items?.length ?? 0) === 0}
        >
          {showCreate ? "Cancel" : "New schedule"}
        </Button>
      </div>

      {showCreate && (
        <CreateForm
          spiders={spiders.data?.items ?? []}
          onDone={() => setShowCreate(false)}
        />
      )}

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
          {schedules.isLoading ? (
            <p className="p-6 text-sm text-zinc-500">Loading...</p>
          ) : items.length === 0 ? (
            <p className="p-6 text-sm text-zinc-500">
              No schedules yet. Create one to run a spider on a cadence.
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
                  <th className="px-6 py-3 font-medium">Spider</th>
                  <th className="px-6 py-3 font-medium">Cron</th>
                  <th className="px-6 py-3 font-medium">Next run</th>
                  <th className="px-6 py-3 font-medium">Last run</th>
                  <th className="px-6 py-3 font-medium">Enabled</th>
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
                        aria-label={`Select schedule ${s.name}`}
                      />
                    </td>
                    <td className="px-6 py-3 font-medium text-zinc-900">{s.name}</td>
                    <td className="px-6 py-3">
                      <Link
                        to="/spiders/$id"
                        params={{ id: String(s.spider_id) }}
                        className="text-zinc-700 hover:underline"
                      >
                        {spiderName(s.spider_id)}
                      </Link>
                    </td>
                    <td className="px-6 py-3 font-mono text-xs text-zinc-600">
                      {s.cron_expr}
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-600">
                      {s.enabled ? fmtTime(s.next_run_at) : "—"}
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-600">
                      {s.last_task_id ? (
                        <Link
                          to="/tasks/$id"
                          params={{ id: String(s.last_task_id) }}
                          className="hover:underline"
                        >
                          {fmtTime(s.last_run_at)}
                        </Link>
                      ) : (
                        fmtTime(s.last_run_at)
                      )}
                    </td>
                    <td className="px-6 py-3">
                      <button
                        type="button"
                        onClick={() => toggle.mutate(s)}
                        disabled={toggle.isPending}
                        className={
                          "rounded-full px-2 py-0.5 text-xs font-medium transition-colors " +
                          (s.enabled
                            ? "bg-emerald-100 text-emerald-700 hover:bg-emerald-200"
                            : "bg-zinc-200 text-zinc-600 hover:bg-zinc-300")
                        }
                      >
                        {s.enabled ? "on" : "off"}
                      </button>
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

function CreateForm({
  spiders,
  onDone,
}: {
  spiders: Spider[]
  onDone: () => void
}) {
  const qc = useQueryClient()
  const [spiderID, setSpiderID] = useState<number>(spiders[0]?.id ?? 0)
  const [name, setName] = useState("")
  const [cron, setCron] = useState("*/5 * * * *")
  const [argsJSON, setArgsJSON] = useState("")
  const [error, setError] = useState<string | null>(null)

  const create = useMutation({
    mutationFn: () => {
      let args: Record<string, unknown> | undefined
      if (argsJSON.trim() !== "") {
        try {
          const parsed = JSON.parse(argsJSON) as unknown
          if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
            throw new Error("args must be a JSON object")
          }
          args = parsed as Record<string, unknown>
        } catch (err) {
          throw new Error(
            err instanceof Error ? `args: ${err.message}` : "args: invalid JSON",
          )
        }
      }
      return schedulesApi.create({
        spider_id: spiderID,
        name,
        cron_expr: cron,
        args,
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["schedules"] })
      onDone()
    },
    onError: (err) => {
      setError(
        err instanceof ApiError
          ? err.message
          : err instanceof Error
            ? err.message
            : "Failed to create schedule",
      )
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
        <h2 className="text-base font-medium">New schedule</h2>
      </CardHeader>
      <CardBody>
        <form className="grid grid-cols-1 gap-4 md:grid-cols-2" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <Label htmlFor="spider">Spider</Label>
            <select
              id="spider"
              value={spiderID}
              onChange={(e) => setSpiderID(Number(e.target.value))}
              className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-zinc-400"
              required
            >
              {spiders.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <div className="space-y-1.5 md:col-span-2">
            <Label htmlFor="cron">
              Cron expression{" "}
              <span className="font-normal text-zinc-500">
                (5 fields: min hour dom mon dow)
              </span>
            </Label>
            <Input
              id="cron"
              value={cron}
              onChange={(e) => setCron(e.target.value)}
              className="font-mono"
              required
            />
          </div>
          <div className="space-y-1.5 md:col-span-2">
            <Label htmlFor="args">
              Args <span className="font-normal text-zinc-500">(JSON object, optional)</span>
            </Label>
            <textarea
              id="args"
              value={argsJSON}
              onChange={(e) => setArgsJSON(e.target.value)}
              placeholder='{"region": "us-west"}'
              rows={3}
              className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 font-mono text-xs focus:outline-none focus:ring-2 focus:ring-zinc-400"
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
            <Button type="submit" disabled={create.isPending || spiderID === 0}>
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
      aria-label="Select all schedules"
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
