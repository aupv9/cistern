import type { ReactNode } from 'react'
import { render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RouterProvider, createMemoryHistory } from '@tanstack/react-router'
import { QueryClient } from '@tanstack/react-query'
import { Route as RootRoute } from '~/routes/__root'
import { createAppRouter } from '~/router'

Object.assign(RootRoute.options, { shellComponent: ({ children }: { children: ReactNode }) => <>{children}</> })

export function renderApp(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false, staleTime: Infinity }, mutations: { retry: false } },
  })
  const history = createMemoryHistory({ initialEntries: [path] })
  const router = createAppRouter({ queryClient, history })
  const user = userEvent.setup()
  const utils = render(<RouterProvider router={router} />)
  return { ...utils, router, queryClient, user }
}
