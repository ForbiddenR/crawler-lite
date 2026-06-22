import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { type FormEvent, useState } from "react"

import { ApiError } from "@/api/client"
import {
  type NotificationChannel,
  type NotificationEvent,
  type NotificationKind,
  notificationsApi,
} from "@/api/resources"
import { Button } from "@/components/ui/button"
import { Card, CardBody, CardHeader } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export const Route = createFileRoute("/_authed/notifications")({
  component: NotificationsPage,
})

// Single source of truth for the event filter UI. Keeping this list in
// step with backend/notify.validEvents is on the engineer; the backend
// rejects unknown tokens at write time so a typo here is loud.
const ALL_EVENTS: { value: NotificationEvent; label: string; default: boolean }[] = [
  { value: "failed", label: "Failed", default: true },
  { value: "timeout", label: "Timeout", default: true },
  { value: "captcha_blocked", label: "Captcha blocked", default: true },
  { value: "succeeded", label: "Succeeded", default: false },
]

// URL placeholders per kind — these are the cheat sheet a first-time
// operator needs. Real validation happens server-side via shoutrrr.
const KIND_PLACEHOLDERS: Record<NotificationKind, string> = {
  slack: "slack://hook/T00000000/B00000000/XXXXXXXXXXXXXXXX",
  telegram: "telegram://<token>@telegram?chats=@my_channel",
  discord: "discord://<token>@<webhook-id>",
  smtp: "smtp://user:pass@host:587/?from=alerts@x.com&to=ops@x.com",
  generic: "generic://example.com/webhook",
}

function NotificationsPage() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [testFeedback, setTestFeedback] = useState<Record<number, string>>({})

  const channels = useQuery({
    queryKey: ["notifications"],
    queryFn: () => notificationsApi.list(),
  })

  const toggle = useMutation({
    mutationFn: (ch: NotificationChannel) =>
      notificationsApi.update(ch.id, {
        name: ch.name,
        kind: ch.kind,
        url: ch.url,
        events: ch.events,
        enabled: !ch.enabled,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["notifications"] }),
  })

  const remove = useMutation({
    mutationFn: (id: number) => notificationsApi.remove(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["notifications"] }),
  })

  const test = useMutation({
    mutationFn: (id: number) => notificationsApi.test(id),
    onMutate: (id) =>
      setTestFeedback((m) => ({ ...m, [id]: "sending…" })),
    onSuccess: (_, id) =>
      setTestFeedback((m) => ({ ...m, [id]: "✓ sent" })),
    onError: (err, id) =>
      setTestFeedback((m) => ({
        ...m,
        [id]:
          err instanceof ApiError
            ? `✗ ${err.message}`
            : err instanceof Error
              ? `✗ ${err.message}`
              : "✗ failed",
      })),
  })

  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Notifications</h1>
        <Button onClick={() => setShowCreate((v) => !v)}>
          {showCreate ? "Cancel" : "New channel"}
        </Button>
      </div>

      <p className="text-sm text-zinc-500">
        Notification channels fan out terminal task events (failed,
        timeout, captcha) to Slack, Telegram, Discord, email, or any
        generic webhook. URLs follow{" "}
        <a
          href="https://containrrr.dev/shoutrrr/services/overview/"
          target="_blank"
          rel="noreferrer"
          className="text-zinc-700 underline hover:text-zinc-900"
        >
          shoutrrr's format
        </a>
        .
      </p>

      {showCreate && <CreateForm onDone={() => setShowCreate(false)} />}

      <Card>
        <CardBody className="p-0">
          {channels.isLoading ? (
            <p className="p-6 text-sm text-zinc-500">Loading...</p>
          ) : (channels.data?.items?.length ?? 0) === 0 ? (
            <p className="p-6 text-sm text-zinc-500">
              No notification channels yet. Create one to start receiving
              alerts about failed tasks.
            </p>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-left text-zinc-500">
                <tr className="border-b border-zinc-200">
                  <th className="px-6 py-3 font-medium">Name</th>
                  <th className="px-6 py-3 font-medium">Kind</th>
                  <th className="px-6 py-3 font-medium">Events</th>
                  <th className="px-6 py-3 font-medium">Enabled</th>
                  <th className="px-6 py-3 font-medium">Test</th>
                  <th className="px-6 py-3" />
                </tr>
              </thead>
              <tbody>
                {(channels.data?.items ?? []).map((ch) => (
                  <tr key={ch.id} className="border-b border-zinc-100 last:border-b-0">
                    <td className="px-6 py-3 font-medium text-zinc-900">{ch.name}</td>
                    <td className="px-6 py-3">
                      <span className="rounded-full bg-zinc-100 px-2 py-0.5 text-xs font-medium text-zinc-700">
                        {ch.kind}
                      </span>
                    </td>
                    <td className="px-6 py-3">
                      <div className="flex flex-wrap gap-1">
                        {ch.events.map((e) => (
                          <span
                            key={e}
                            className="rounded-full bg-zinc-100 px-2 py-0.5 text-[11px] font-medium text-zinc-700"
                          >
                            {e}
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="px-6 py-3">
                      <button
                        type="button"
                        onClick={() => toggle.mutate(ch)}
                        disabled={toggle.isPending}
                        className={
                          "rounded-full px-2 py-0.5 text-xs font-medium transition-colors " +
                          (ch.enabled
                            ? "bg-emerald-100 text-emerald-700 hover:bg-emerald-200"
                            : "bg-zinc-200 text-zinc-600 hover:bg-zinc-300")
                        }
                      >
                        {ch.enabled ? "on" : "off"}
                      </button>
                    </td>
                    <td className="px-6 py-3 text-xs text-zinc-600">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => test.mutate(ch.id)}
                        disabled={test.isPending}
                      >
                        Send test
                      </Button>
                      {testFeedback[ch.id] && (
                        <span
                          className={
                            "ml-2 " +
                            (testFeedback[ch.id].startsWith("✓")
                              ? "text-emerald-700"
                              : testFeedback[ch.id].startsWith("✗")
                                ? "text-red-600"
                                : "text-zinc-500")
                          }
                        >
                          {testFeedback[ch.id]}
                        </span>
                      )}
                    </td>
                    <td className="px-6 py-3 text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          if (confirm(`Delete channel "${ch.name}"?`)) {
                            remove.mutate(ch.id)
                          }
                        }}
                      >
                        Delete
                      </Button>
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
  const [kind, setKind] = useState<NotificationKind>("slack")
  const [url, setUrl] = useState("")
  const [events, setEvents] = useState<NotificationEvent[]>(
    ALL_EVENTS.filter((e) => e.default).map((e) => e.value),
  )
  const [error, setError] = useState<string | null>(null)

  const create = useMutation({
    mutationFn: () =>
      notificationsApi.create({
        name,
        kind,
        url,
        events,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["notifications"] })
      onDone()
    },
    onError: (err) => {
      setError(
        err instanceof ApiError
          ? err.message
          : err instanceof Error
            ? err.message
            : "Failed to create channel",
      )
    },
  })

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (events.length === 0) {
      setError("Pick at least one event to listen for.")
      return
    }
    create.mutate()
  }

  function toggleEvent(ev: NotificationEvent) {
    setEvents((current) =>
      current.includes(ev) ? current.filter((c) => c !== ev) : [...current, ev],
    )
  }

  return (
    <Card>
      <CardHeader>
        <h2 className="text-base font-medium">New channel</h2>
      </CardHeader>
      <CardBody>
        <form className="grid grid-cols-1 gap-4 md:grid-cols-2" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <Label htmlFor="name">Name</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Ops Slack"
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="kind">Kind</Label>
            <select
              id="kind"
              value={kind}
              onChange={(e) => setKind(e.target.value as NotificationKind)}
              className="w-full rounded-md border border-zinc-300 bg-white px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-zinc-400"
            >
              <option value="slack">Slack</option>
              <option value="telegram">Telegram</option>
              <option value="discord">Discord</option>
              <option value="smtp">Email (SMTP)</option>
              <option value="generic">Generic webhook</option>
            </select>
          </div>
          <div className="space-y-1.5 md:col-span-2">
            <Label htmlFor="url">URL</Label>
            <Input
              id="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder={KIND_PLACEHOLDERS[kind]}
              className="font-mono text-xs"
              required
            />
            <p className="text-xs text-zinc-500">
              shoutrrr-format URL. The token is stored verbatim in the
              database — rotate it there if it leaks.
            </p>
          </div>
          <div className="space-y-1.5 md:col-span-2">
            <Label>Events</Label>
            <div className="flex flex-wrap gap-3">
              {ALL_EVENTS.map((e) => (
                <label
                  key={e.value}
                  className="flex items-center gap-2 text-sm text-zinc-700"
                >
                  <input
                    type="checkbox"
                    checked={events.includes(e.value)}
                    onChange={() => toggleEvent(e.value)}
                  />
                  {e.label}
                </label>
              ))}
            </div>
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
