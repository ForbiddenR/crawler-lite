import { useQuery } from "@tanstack/react-query"
import { Link, Outlet, createFileRoute, redirect, useNavigate } from "@tanstack/react-router"
import { LogOut } from "lucide-react"

import { authApi } from "@/api/auth"
import { Button } from "@/components/ui/button"
import { useAuthStore } from "@/stores/auth"

/**
 * Auth-guarded layout. All real pages live as children of this route. The
 * guard runs before the route matches; unauthenticated users are bounced
 * to /login.
 */
export const Route = createFileRoute("/_authed")({
  beforeLoad: () => {
    if (!useAuthStore.getState().token) {
      throw redirect({ to: "/login" })
    }
  },
  component: AuthedLayout,
})

function AuthedLayout() {
  const logout = useAuthStore((s) => s.logout)
  const navigate = useNavigate()

  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => authApi.me(),
  })

  return (
    <div className="flex min-h-screen flex-col">
      <header className="flex items-center justify-between border-b border-zinc-200 bg-white px-6 py-3">
        <div className="flex items-center gap-6">
          <span className="font-semibold">crawler-lite</span>
          <nav className="flex gap-4 text-sm text-zinc-600">
            <NavLink to="/dashboard">Dashboard</NavLink>
            <NavLink to="/spiders">Spiders</NavLink>
            <NavLink to="/schedules">Schedules</NavLink>
            <NavLink to="/tasks">Tasks</NavLink>
          </nav>
        </div>
        <div className="flex items-center gap-3 text-sm text-zinc-600">
          {me.data && <span>{me.data.email}</span>}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              logout()
              void navigate({ to: "/login" })
            }}
          >
            <LogOut className="mr-1.5 h-4 w-4" />
            Sign out
          </Button>
        </div>
      </header>
      <main className="flex-1 bg-zinc-50">
        <Outlet />
      </main>
    </div>
  )
}

function NavLink({ to, children }: { to: string; children: React.ReactNode }) {
  // TanStack <Link> gives us active styling and prefetch — the routes are
  // typed so the to=… literal is checked at compile time.
  return (
    <Link
      to={to}
      className="hover:text-zinc-900"
      activeProps={{ className: "text-zinc-900 font-medium" }}
    >
      {children}
    </Link>
  )
}
