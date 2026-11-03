import { useEffect, useId, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { auditExportUrl, auditQuery, overviewQuery, type AuditAction, type AuditEntry, type AuditFilters, type AuditPeriod, type AuditResult } from '~/api/queries'
import { describeEntry } from '~/components/ActivityList'
import { DownloadIcon } from '~/components/Icons'
import { TableSkeleton } from '~/components/Skeleton'
import { EmptyState, ErrorState } from '~/components/States'
import { Status } from '~/components/Status'
import { useDebounced } from '~/hooks/useDebounced'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { formatWhen } from '~/lib/format'
import { describeRoles } from '~/lib/roles'
import { AUDIT_ACTIONS, AUDIT_RESULT, AUDIT_RESULTS, auditActionLabel } from '~/lib/status'

type AuditSearch = { q?: string; action?: AuditAction; job?: string; result?: AuditResult; period?: AuditPeriod }

const PERIODS: { value: AuditPeriod; label: string }[] = [
  { value: '1', label: 'Last 24 hours' },
  { value: '7', label: 'Last 7 days' },
  { value: '30', label: 'Last 30 days' },
  { value: '90', label: 'Last 90 days' },
]

const ACTION_VALUES = new Set<string>(AUDIT_ACTIONS.map((a) => a.value))
const RESULT_VALUES = new Set<string>(AUDIT_RESULTS.map((r) => r.value))
const PERIOD_VALUES = new Set<string>(PERIODS.map((p) => p.value))

export const Route = createFileRoute('/_app/audit')({
  validateSearch: (s: Record<string, unknown>): AuditSearch => ({
    q: typeof s.q === 'string' && s.q ? s.q : undefined,
    action: typeof s.action === 'string' && ACTION_VALUES.has(s.action) ? (s.action as AuditAction) : undefined,
    job: typeof s.job === 'string' && s.job ? s.job : undefined,
    result: typeof s.result === 'string' && RESULT_VALUES.has(s.result) ? (s.result as AuditResult) : undefined,
    period: typeof s.period === 'string' && PERIOD_VALUES.has(s.period) ? (s.period as AuditPeriod) : undefined,
  }),
  head: () => ({ meta: [{ title: 'Audit log · Cistern' }] }),
  component: AuditPage,
})

const SYSTEM_ACTORS = new Set(['scheduler', 'cli', 'cistern'])

function who(e: AuditEntry) {
  if (SYSTEM_ACTORS.has(e.actor.id)) return { name: e.actor.name, sub: 'System' }
  return { name: e.actor.name, sub: e.roles?.length ? describeRoles(e.roles) : (e.actor.email ?? 'No role') }
}

function AuditPage() {
  const me = useMe()
  const tz = me.deployment.timezone
  const now = useNow(60_000)
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const ids = { q: useId(), action: useId(), job: useId(), result: useId(), period: useId() }
  const [text, setText] = useState(search.q ?? '')
  const debounced = useDebounced(text, 300)
  const [pager, setPager] = useState<{ key: string; cursors: string[] }>({ key: '', cursors: [] })
  const filters: AuditFilters = {
    q: search.q,
    action: search.action,
    job: search.job,
    result: search.result,
    period: search.period ?? '7',
  }
  const filterKey = JSON.stringify(filters)
  const cursors = pager.key === filterKey ? pager.cursors : []
  const cursor = cursors.at(-1)
  const setCursors = (update: (c: string[]) => string[]) => setPager({ key: filterKey, cursors: update(cursors) })
  const q = useQuery(auditQuery({ ...filters, cursor }))
  const jobs = useQuery({ ...overviewQuery, refetchInterval: false, select: (o) => o.jobs.map((j) => j.name) })

  useEffect(() => {
    if ((debounced || undefined) !== search.q) {
      void navigate({ search: (prev) => ({ ...prev, q: debounced || undefined }), replace: true })
    }
  }, [debounced, search.q, navigate])

  const set = (patch: Partial<AuditSearch>) => void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true })
  const entries = q.data?.entries ?? []
  const hasFilters = !!(search.q || search.action || search.job || search.result || (search.period && search.period !== '7'))

  return (
    <div className="page">
      <div className="page-head">
        <div className="page-title">
          <h1 className="h1">Audit log</h1>
          <p>Who did what, when, and how it ended. Entries cannot be edited or deleted.</p>
        </div>
        <a className="btn" href={auditExportUrl(filters)} download>
          <DownloadIcon />
          Export CSV
        </a>
      </div>

      <section aria-label="Entries" className="card card-clip">
        <form aria-label="Filters" className="filters" onSubmit={(e) => e.preventDefault()} role="search">
          <label className="f" style={{ flex: '1 1 220px' }} htmlFor={ids.q}>
            Search
            <input id={ids.q} className="input" type="search" placeholder="Person, job or snapshot" value={text} onChange={(e) => setText(e.target.value)} />
          </label>
          <label className="f" style={{ flex: '0 1 190px' }} htmlFor={ids.action}>
            Action
            <select id={ids.action} className="select" value={search.action ?? ''} onChange={(e) => set({ action: (e.target.value || undefined) as AuditAction | undefined })}>
              <option value="">All actions</option>
              {AUDIT_ACTIONS.map((a) => (
                <option key={a.value} value={a.value}>
                  {a.label}
                </option>
              ))}
            </select>
          </label>
          <label className="f" style={{ flex: '0 1 150px' }} htmlFor={ids.job}>
            Job
            <select id={ids.job} className="select" value={search.job ?? ''} onChange={(e) => set({ job: e.target.value || undefined })}>
              <option value="">All jobs</option>
              {(jobs.data ?? (search.job ? [search.job] : [])).map((j) => (
                <option key={j} value={j}>
                  {j}
                </option>
              ))}
            </select>
          </label>
          <label className="f" style={{ flex: '0 1 150px' }} htmlFor={ids.result}>
            Result
            <select id={ids.result} className="select" value={search.result ?? ''} onChange={(e) => set({ result: (e.target.value || undefined) as AuditResult | undefined })}>
              <option value="">Any result</option>
              {AUDIT_RESULTS.map((r) => (
                <option key={r.value} value={r.value}>
                  {r.label}
                </option>
              ))}
            </select>
          </label>
          <label className="f" style={{ flex: '0 1 150px' }} htmlFor={ids.period}>
            Period
            <select id={ids.period} className="select" value={filters.period} onChange={(e) => set({ period: e.target.value === '7' ? undefined : (e.target.value as AuditPeriod) })}>
              {PERIODS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </label>
        </form>

        {q.isPending ? (
          <TableSkeleton rows={8} cols={5} label="Loading audit entries" />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load the audit log" />
        ) : entries.length === 0 ? (
          <EmptyState title={hasFilters ? 'No entries match these filters' : 'No entries in this period'}>
            {hasFilters ? (
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => {
                  setText('')
                  void navigate({ search: {}, replace: true })
                }}
              >
                Clear filters
              </button>
            ) : null}
          </EmptyState>
        ) : (
          <div className="scroll-x" aria-busy={q.isFetching}>
            <table className="t top">
              <thead>
                <tr>
                  <th scope="col">Time</th>
                  <th scope="col">Who</th>
                  <th scope="col">Action</th>
                  <th scope="col">Details</th>
                  <th scope="col">Result</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => {
                  const w = who(e)
                  return (
                    <tr key={e.id}>
                      <td className="muted">
                        <time dateTime={e.time}>{formatWhen(e.time, tz, now)}</time>
                      </td>
                      <td>
                        <div className="cell-name">
                          <span style={{ fontWeight: 500 }}>{w.name}</span>
                          <span className="sub">{w.sub}</span>
                        </div>
                      </td>
                      <td>{auditActionLabel(e.action)}</td>
                      <td className="wrap" style={{ minWidth: 340, lineHeight: 1.5 }}>
                        {describeEntry(e)}
                        {e.reason ? <span className="muted"> Reason: {e.reason}</span> : null}
                        {e.error && !e.summary?.includes(e.error) ? (
                          <span className="mono" style={{ display: 'block', color: 'var(--fail-fg)', fontSize: 12, overflowWrap: 'anywhere' }}>
                            {e.error}
                          </span>
                        ) : null}
                        {e.run_id || e.approval_id ? (
                          <span className="row sub" style={{ gap: 12 }}>
                            {e.run_id ? (
                              <Link to="/runs/$run" params={{ run: e.run_id }}>
                                Run {e.run_id}
                              </Link>
                            ) : null}
                            {e.approval_id ? (
                              <Link to="/approvals/$id" params={{ id: e.approval_id }}>
                                {e.approval_id}
                              </Link>
                            ) : null}
                          </span>
                        ) : null}
                      </td>
                      <td>
                        <Status info={AUDIT_RESULT[e.result]} />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        <div className="pager">
          <span className="sub num" aria-live="polite">
            {q.data ? `Page ${cursors.length + 1} · ${entries.length} ${entries.length === 1 ? 'entry' : 'entries'}${q.data.total != null ? ` of ${q.data.total.toLocaleString('en-US')}` : ''}` : ''}
          </span>
          <div className="row">
            <button type="button" className="btn btn-sm" disabled={cursors.length === 0 || q.isFetching} onClick={() => setCursors((c) => c.slice(0, -1))}>
              Newer
            </button>
            <button
              type="button"
              className="btn btn-sm"
              disabled={!q.data?.next_cursor || q.isFetching}
              onClick={() => {
                const next = q.data?.next_cursor
                if (next) setCursors((c) => [...c, next])
              }}
            >
              Older
            </button>
          </div>
        </div>
      </section>
    </div>
  )
}
