import { useEffect } from 'react'
import { Outlet, createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ApiError } from '~/api/client'
import { meQuery } from '~/api/queries'
import { Header } from '~/components/Header'
import { Mark } from '~/components/Icons'
import { ReadOnlyBanner } from '~/components/ReadOnlyBanner'
import { PageSkeleton, Skel } from '~/components/Skeleton'
import { ErrorState } from '~/components/States'
import { MeContext } from '~/hooks/useMe'

export const Route = createFileRoute('/_app')({
  component: AppLayout,
  pendingComponent: () => <AppSkeleton />,
})

function AppSkeleton({ page = true }: { page?: boolean }) {
  return (
    <>
      <header className="hdr">
        <div className="hdr-top">
          <div className="hdr-left">
            <span className="hdr-brand">
              <Mark />
              Cistern
            </span>
          </div>
          <div className="hdr-right">
            <Skel w={120} h={28} />
          </div>
        </div>
        <div className="hdr-nav" style={{ height: 44 }} />
      </header>
      {page ? <PageSkeleton /> : null}
    </>
  )
}

function AppLayout() {
  const me = useQuery(meQuery)
  const navigate = useNavigate()
  const denied =
    (me.error instanceof ApiError && me.error.code === 'forbidden') || (me.data !== undefined && me.data.roles.length === 0)

  useEffect(() => {
    if (denied) void navigate({ to: '/denied', replace: true })
  }, [denied, navigate])

  if (me.isPending || denied) return <AppSkeleton />

  if (me.isError) {
    if (me.error instanceof ApiError && me.error.status === 401) {
      return (
        <div className="page" role="status">
          <p className="muted">Your session has ended. Taking you to sign-in…</p>
        </div>
      )
    }
    return (
      <>
        <AppSkeleton page={false} />
        <div className="page">
          <div className="card">
            <ErrorState error={me.error} onRetry={() => void me.refetch()} title="Could not load your account" />
          </div>
        </div>
      </>
    )
  }

  return (
    <MeContext.Provider value={me.data}>
      <a className="skip" href="#main">
        Skip to content
      </a>
      <Header me={me.data} />
      <ReadOnlyBanner me={me.data} />
      <main id="main" tabIndex={-1}>
        <Outlet />
      </main>
    </MeContext.Provider>
  )
}
