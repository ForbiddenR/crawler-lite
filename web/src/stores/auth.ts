import { create } from "zustand"
import { persist } from "zustand/middleware"

/**
 * Auth store. The token is persisted to localStorage so a page refresh keeps
 * you logged in. The user object is fetched fresh from /api/auth/me on
 * the protected layout, so it doesn't need to live here.
 */
interface AuthState {
  token: string | null
  setToken: (t: string | null) => void
  logout: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      setToken: (token) => set({ token }),
      logout: () => set({ token: null }),
    }),
    { name: "crawler-auth" },
  ),
)
