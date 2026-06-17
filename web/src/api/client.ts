import { useAuthStore } from "@/stores/auth"

/**
 * ApiError carries the HTTP status alongside the message so callers can
 * distinguish 401 from 4xx from 5xx without parsing strings.
 */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

interface ApiInit extends Omit<RequestInit, "body"> {
  params?: Record<string, string | number | undefined>
  json?: unknown
}

/**
 * Thin fetch wrapper:
 *   - Injects Bearer token from the auth store.
 *   - JSON-encodes `json` payloads.
 *   - Throws ApiError on non-2xx so TanStack Query handles errors uniformly.
 *   - Returns parsed JSON, or undefined on 204.
 */
export async function api<T = unknown>(path: string, init: ApiInit = {}): Promise<T> {
  const url = new URL(path, window.location.origin)
  for (const [k, v] of Object.entries(init.params ?? {})) {
    if (v !== undefined) url.searchParams.set(k, String(v))
  }

  const token = useAuthStore.getState().token
  const headers = new Headers(init.headers)
  headers.set("Accept", "application/json")
  if (token) headers.set("Authorization", `Bearer ${token}`)

  let body: BodyInit | undefined
  if (init.json !== undefined) {
    headers.set("Content-Type", "application/json")
    body = JSON.stringify(init.json)
  }

  const res = await fetch(url, { ...init, headers, body })

  if (res.status === 204) return undefined as T

  if (!res.ok) {
    let payload: { error?: { code?: string; message?: string } } = {}
    try {
      payload = await res.json()
    } catch {
      // ignore
    }
    if (res.status === 401) useAuthStore.getState().logout()
    throw new ApiError(
      res.status,
      payload.error?.code ?? res.statusText,
      payload.error?.message ?? res.statusText,
    )
  }

  return (await res.json()) as T
}
