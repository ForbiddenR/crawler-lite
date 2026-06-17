import { createFileRoute, redirect } from "@tanstack/react-router"

import { useAuthStore } from "@/stores/auth"

/**
 * Index route is just a redirector. Logged in → /dashboard; otherwise → /login.
 */
export const Route = createFileRoute("/")({
  beforeLoad: () => {
    const token = useAuthStore.getState().token
    if (token) throw redirect({ to: "/dashboard" })
    throw redirect({ to: "/login" })
  },
})
