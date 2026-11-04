import { Link, useLocation, useNavigate, useParams } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { approvalsQuery, logout, type Me } from '~/api/queries'
import { setCsrfToken } from '~/api/client'
import { can, describeRoles } from '~/lib/roles'
import { initials } from '~/lib/format'
import { Mark, Slash } from './Icons'
import { ThemeToggle } from './ThemeToggle'

export const DOCS_URL = 'https://github.com/aupv9/cistern'

type TabKey = 'overview' | 'jobs' | 'approvals' | 'audit' | 'config'

function activeTab(pathname: string): TabKey | null {
  if (pathname === '/') return 'overview'
  if (pathname.startsWith('/jobs') || pathname.startsWith('/runs')) return 'jobs'
  if (pathname.startsWith('/approvals')) return 'approvals'
  if (pathname.startsWith('/audit')) return 'audit'
  if (pathname.startsWith('/config')) return 'config'
  return null
}

function useCrumb(): string | null {
  const params = useParams({ strict: false }) as { job?: string; run?: string; id?: string }
  if (params.job) return params.job
  if (params.run) return `Run ${params.run}`
  if (params.id) return params.id
  return null
}

export function Header({ me }: { me: Me }) {
  const { pathname } = useLocation()
  const tab = activeTab(pathname)
  const crumb = useCrumb()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const pending = useQuery({ ...approvalsQuery('pending'), select: (list) => list.length })
  const daemon = me.deployment.daemon
  const daemonOk = daemon.configured && daemon.reachable
  const daemonText = !daemon.configured ? 'no daemon configured' : daemon.reachable ? 'daemon reachable' : 'daemon not reachable'

  const signOut = useMutation({
    mutationFn: logout,
    onSettled: async () => {
      setCsrfToken(null)
      await navigate({ to: '/signin', search: { signed_out: true } })
      queryClient.clear()
    },
  })

  const tabs: { key: TabKey; to: '/' | '/jobs' | '/approvals' | '/audit' | '/config'; label: string }[] = [
    { key: 'overview', to: '/', label: 'Overview' },
    { key: 'jobs', to: '/jobs', label: 'Jobs' },
    { key: 'approvals', to: '/approvals', label: 'Approvals' },
    { key: 'audit', to: '/audit', label: 'Audit log' },
  ]
  if (can(me.roles, 'config')) tabs.push({ key: 'config', to: '/config', label: 'Configuration' })

  return (
    <header className="hdr">
      <div className="hdr-top">
        <div className="hdr-left">
          <Link to="/" className="hdr-brand">
            <Mark />
            Cistern
          </Link>
          <Slash />
          <span className="hdr-deploy" title={`Version ${me.deployment.version}, ${daemonText}`}>
            <span className={`dot ${daemonOk ? 'ok' : 'warn'}`} aria-hidden="true" />
            {me.deployment.name}
            <span className="sr">, {daemonText}</span>
          </span>
          {crumb ? (
            <>
              <Slash />
              <span className="hdr-crumb">{crumb}</span>
            </>
          ) : null}
        </div>
        <div className="hdr-right">
          <a href={DOCS_URL} className="hdr-link" target="_blank" rel="noreferrer">
            Docs
          </a>
          <div className="hdr-user">
            <span className="hdr-who">
              <span className="hdr-name">{me.user.name}</span>
              <span className="hdr-roles">{describeRoles(me.roles)}</span>
            </span>
            <span className="avatar" aria-hidden="true">
              {initials(me.user.name)}
            </span>
          </div>
          <ThemeToggle />
          <button type="button" className="hdr-link" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
            {signOut.isPending ? 'Signing out…' : 'Sign out'}
          </button>
        </div>
      </div>
      <nav aria-label="Main" className="hdr-nav">
        <ul>
          {tabs.map((t) => (
            <li key={t.key}>
              <Link to={t.to} className="tab" aria-current={tab === t.key ? 'page' : undefined}>
                {t.label}
                {t.key === 'approvals' && pending.data ? (
                  <>
                    <span className="badge" aria-hidden="true">
                      {pending.data}
                    </span>
                    <span className="sr">, {pending.data} pending</span>
                  </>
                ) : null}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
    </header>
  )
}
