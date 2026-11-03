import type { ReactNode } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { ClientOnly, HeadContent, Outlet, Scripts, createRootRouteWithContext } from '@tanstack/react-router'
import appCss from '~/styles/app.css?url'
import { THEME_BOOT_SCRIPT } from '~/lib/theme'

const FAVICON =
  "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 28 28' fill='none'%3E%3Crect x='3' y='4' width='22' height='20' rx='6' stroke='%23171717' stroke-width='2.2'/%3E%3Cpath d='M3 15c4 2 8 2 11 0s7-2 11 0' stroke='%23171717' stroke-width='2.2'/%3E%3C/svg%3E"

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  head: () => ({
    meta: [
      { charSet: 'utf-8' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      { name: 'color-scheme', content: 'light dark' },
      { name: 'robots', content: 'noindex' },
      { title: 'Cistern' },
    ],
    links: [
      { rel: 'stylesheet', href: appCss },
      { rel: 'icon', href: FAVICON, type: 'image/svg+xml' },
    ],
  }),
  shellComponent: RootDocument,
  component: RootOutlet,
})

function RootOutlet() {
  return (
    <ClientOnly fallback={null}>
      <Outlet />
    </ClientOnly>
  )
}

function RootDocument({ children }: { children: ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOT_SCRIPT }} />
        <HeadContent />
      </head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  )
}
