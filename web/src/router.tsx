import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRouter, type RouterHistory } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { ApiError } from './api/client'
import { NotFound } from './components/NotFound'
import { routeTree } from './routeTree.gen'

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (count, error) => {
          if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false
          return count < 2
        },
        refetchOnWindowFocus: true,
        staleTime: 5_000,
      },
      mutations: {
        retry: false,
      },
    },
  })
}

export function createAppRouter(options: { queryClient?: QueryClient; history?: RouterHistory } = {}) {
  const queryClient = options.queryClient ?? createQueryClient()
  return createRouter({
    routeTree,
    history: options.history,
    context: { queryClient },
    scrollRestoration: true,
    defaultPreload: 'intent',
    defaultPreloadStaleTime: 0,
    defaultNotFoundComponent: NotFound,
    Wrap: ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
  })
}

export function getRouter() {
  return createAppRouter()
}
