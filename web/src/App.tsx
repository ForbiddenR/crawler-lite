import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider, createRouter } from "@tanstack/react-router"

import { routeTree } from "./routeTree.gen"

// One QueryClient for the whole app. Configured for an internal tool: longer
// stale time, no refetch-on-focus, no infinite retry.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: (count, err) => {
        // Don't retry 401s (would loop the user)
        if ((err as { status?: number })?.status === 401) return false
        return count < 2
      },
      refetchOnWindowFocus: false,
    },
    mutations: { retry: false },
  },
})

const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
