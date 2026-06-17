import { useMutation } from "@tanstack/react-query"
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router"
import { type FormEvent, useState } from "react"

import { authApi } from "@/api/auth"
import { ApiError } from "@/api/client"
import { Button } from "@/components/ui/button"
import { Card, CardBody, CardHeader } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuthStore } from "@/stores/auth"

export const Route = createFileRoute("/login")({
  beforeLoad: () => {
    if (useAuthStore.getState().token) {
      throw redirect({ to: "/dashboard" })
    }
  },
  component: LoginPage,
})

function LoginPage() {
  const setToken = useAuthStore((s) => s.setToken)
  const navigate = useNavigate()

  const [email, setEmail] = useState("admin@local")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)

  const login = useMutation({
    mutationFn: () => authApi.login(email, password),
    onSuccess: (resp) => {
      setToken(resp.token)
      void navigate({ to: "/dashboard" })
    },
    onError: (err) => {
      if (err instanceof ApiError) {
        setError(err.status === 401 ? "Invalid credentials" : err.message)
      } else {
        setError("Login failed")
      }
    },
  })

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    login.mutate()
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-50 px-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <h1 className="text-lg font-semibold">crawler-lite</h1>
          <p className="mt-1 text-sm text-zinc-500">Sign in to continue</p>
        </CardHeader>
        <CardBody>
          <form className="space-y-4" onSubmit={onSubmit}>
            <div className="space-y-1.5">
              <Label htmlFor="email">Email</Label>
              <Input
                id="email"
                type="email"
                autoComplete="username"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {error && (
              <p className="text-sm text-red-600" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" className="w-full" disabled={login.isPending}>
              {login.isPending ? "Signing in..." : "Sign in"}
            </Button>
          </form>
        </CardBody>
      </Card>
    </div>
  )
}
