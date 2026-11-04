import { useEffect, type MouseEvent } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { configQuery, type ConfigView } from '~/api/queries'
import { CopyButton } from '~/components/CopyButton'
import { InfoIcon, WarningIcon } from '~/components/Icons'
import { PageSkeleton } from '~/components/Skeleton'
import { EmptyState, ErrorState } from '~/components/States'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { formatWhenLower, plural } from '~/lib/format'
import { can } from '~/lib/roles'

export const Route = createFileRoute('/_app/config')({
  head: () => ({ meta: [{ title: 'Configuration · Cistern' }] }),
  component: ConfigPage,
})

const REDACTED = '<redacted>'

export function splitRedacted(line: string): { text: string; redacted: boolean }[] {
  const out: { text: string; redacted: boolean }[] = []
  let rest = line
  while (rest.length) {
    const i = rest.indexOf(REDACTED)
    if (i < 0) {
      out.push({ text: rest, redacted: false })
      break
    }
    if (i > 0) out.push({ text: rest.slice(0, i), redacted: false })
    out.push({ text: REDACTED, redacted: true })
    rest = rest.slice(i + REDACTED.length)
  }
  return out
}

function lineId(n: number) {
  return `L${n}`
}

function jumpTo(e: MouseEvent<HTMLAnchorElement>, n: number) {
  const el = document.getElementById(lineId(n))
  if (!el) return
  e.preventDefault()
  el.scrollIntoView({ block: 'center' })
  el.focus({ preventScroll: true })
  window.history.replaceState(window.history.state, '', `#${lineId(n)}`)
}

function ConfigPage() {
  const me = useMe()
  const allowed = can(me.roles, 'config')
  const q = useQuery({ ...configQuery, enabled: allowed })

  if (!allowed) {
    return (
      <div className="page">
        <h1 className="h1">Configuration</h1>
        <div className="card">
          <EmptyState title="You cannot view the configuration">
            <p className="muted">Viewing the server configuration needs the Operator role. Ask an administrator if you need it.</p>
            <Link to="/" className="btn btn-sm">
              Go to overview
            </Link>
          </EmptyState>
        </div>
      </div>
    )
  }
  if (q.isPending) return <PageSkeleton title="Loading the configuration" />
  if (q.isError) {
    return (
      <div className="page">
        <h1 className="h1">Configuration</h1>
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load the configuration" />
        </div>
      </div>
    )
  }
  return <ConfigBody view={q.data} tz={me.deployment.timezone} />
}

export function ConfigBody({ view, tz }: { view: ConfigView; tz: string }) {
  const now = useNow(60_000)
  const lines = view.yaml.replace(/\n$/, '').split('\n')
  const secretLines = new Set(view.literal_secrets.map((s) => s.line))
  const width = String(lines.length).length

  useEffect(() => {
    const hash = window.location.hash
    if (!/^#L\d+$/.test(hash)) return
    const el = document.getElementById(hash.slice(1))
    el?.scrollIntoView({ block: 'center' })
  }, [])

  return (
    <div className="page">
      <div className="page-title">
        <h1 className="h1">Configuration</h1>
        <p>
          Read-only view of the file the server loaded {formatWhenLower(view.loaded_at, tz, now)}. Secret values are replaced by{' '}
          <span className="redacted">{REDACTED}</span>.
        </p>
      </div>

      <div className="callout info">
        <InfoIcon />
        <span className="text">Every view of this page is recorded in the audit log with your name.</span>
      </div>

      {view.literal_secrets.length ? (
        <section aria-labelledby="secrets-h" className="callout wrn">
          <WarningIcon />
          <div className="text stack" style={{ gap: 6 }}>
            <span id="secrets-h">
              <strong>
                {view.literal_secrets.length === 1 ? 'A secret is' : `${plural(view.literal_secrets.length, 'secret')} are`} written into the file.
              </strong>{' '}
              Anyone who can read the file or its backups can read {view.literal_secrets.length === 1 ? 'it' : 'them'}. Reference an environment variable
              instead, for example <span className="mono">password: ${'{'}DB_PASSWORD{'}'}</span>.
            </span>
            <ul className="secret-list">
              {view.literal_secrets.map((s) => (
                <li key={`${s.path}-${s.line}`}>
                  <span className="mono">{s.path}</span>
                  {' · '}
                  <a href={`#${lineId(s.line)}`} onClick={(e) => jumpTo(e, s.line)}>
                    line {s.line}
                  </a>
                </li>
              ))}
            </ul>
          </div>
        </section>
      ) : null}

      <section aria-labelledby="file-h" className="card card-clip">
        <div className="hd">
          <h2 id="file-h" className="h2 mono" style={{ fontWeight: 500, overflowWrap: 'anywhere' }}>
            {view.path}
          </h2>
          <div className="row">
            <span className="sub num">{plural(lines.length, 'line')}</span>
            <CopyButton text={view.yaml} />
          </div>
        </div>
        <div className="cfg" tabIndex={0} role="region" aria-label={`Contents of ${view.path}`}>
          <div className="cfg-code" style={{ ['--ln-w' as string]: `${width}ch` }}>
            {lines.map((line, i) => {
              const n = i + 1
              const secret = secretLines.has(n)
              return (
                <div key={n} id={lineId(n)} tabIndex={-1} className={secret ? 'cfg-line secret' : 'cfg-line'}>
                  <span className="ln" aria-hidden="true">
                    {n}
                  </span>
                  <span className="lc">
                    {splitRedacted(line).map((part, j) =>
                      part.redacted ? (
                        <span key={j} className="redacted" title="Redacted literal secret">
                          {part.text}
                        </span>
                      ) : (
                        <span key={j}>{part.text}</span>
                      ),
                    )}
                    {line === '' ? '​' : null}
                  </span>
                </div>
              )
            })}
          </div>
        </div>
      </section>
    </div>
  )
}
