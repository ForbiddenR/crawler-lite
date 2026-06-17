import { api } from "./client"

export interface User {
  id: number
  email: string
  role: "admin" | "editor" | "viewer"
  display_name?: string
}

interface LoginResponse {
  token: string
  user: User
}

export const authApi = {
  login: (email: string, password: string) =>
    api<LoginResponse>("/api/auth/login", {
      method: "POST",
      json: { email, password },
    }),
  me: () => api<User>("/api/auth/me"),
}
