import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { drillsQuery, isActive, type DrillCheck, type DrillResult, type DrillTable, type JobDetail, type Me } from '~/api/queries'
import { useRunDrill } from '~/hooks/useRunActions'
import {
  CHECK_KIND_LABEL,
  CHECK_STATUS,
  checkSummary,
  drillBlockReason,
  drillEvidence,
  drillPassed,
  drillScheduleText,
  drillState,
  maxAgeText,
} from '~/lib/drill'
import { formatBytes, formatCount, formatDuration, formatWhen, plural } from '~/lib/format'
import { can } from '~/lib/roles'
import { DRILL_STATE } from '~/lib/status'
import { CheckCircleIcon, ErrorIcon } from './Icons'
import { BlockSkeleton } from './Skeleton'
import { ErrorState, InlineError } from './States'
import { Status } from './Status'

export function DrillEvidence({ result, now }: { result: DrillResult; now: Date }) {
  const running = isActive(result.state)
  const ok = drillPassed(result)
  const tone = running ? 'run' : ok ? 'ok' : 'fail'
  return (
    <p className={`evidence ${tone}`} data-testid="drill-evidence">
      {running ? null : ok ? <CheckCircleIcon /> : <ErrorIcon />}
      <span>{drillEvidence(result, now)}</span>
    </p>
  )
}

export function DrillChecks({ checks }: { checks: readonly DrillCheck[] }) {
  if (checks.length === 0) return <p className="muted">No checks ran.</p>
  return (
    <div className="inset scroll-x">
      <table className="t compact">
        <caption className="sr">Drill checks with expected and actual values</caption>
        <thead>
          <tr>
            <th scope="col">Check</th>
            <th scope="col">Result</th>
            <th scope="col">Expected</th>
            <th scope="col">Actual</th>
          </tr>
        </thead>
        <tbody>
          {checks.map((c, i) => (
            <tr key={`${c.kind}-${c.name}-${i}`} className={c.status === 'failed' ? 'row-fail' : undefined}>
              <td className="wrap">
                <div className="cell-name">
                  <span>{c.name}</span>
                  <span className="sub">
                    {CHECK_KIND_LABEL[c.kind]}
                    {c.message ? ` · ${c.message}` : ''}
                  </span>
                </div>
              </td>
              <td>
                <Status info={CHECK_STATUS[c.status]} small />
              </td>
              <td className="wrap num">{c.expected ?? '–'}</td>
              <td className={`wrap num${c.status === 'failed' ? ' fail-text' : ''}`}>{c.actual ?? '–'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

const TOP_TABLES = 8

export function DrillTables({ tables }: { tables: readonly DrillTable[] }) {
  const sorted = [...tables].sort((a, b) => b.rows - a.rows)
  const top = sorted.slice(0, TOP_TABLES)
  const rest = sorted.slice(TOP_TABLES)
  const restRows = rest.reduce((a, t) => a + t.rows, 0)
  return (
    <div className="inset">
      <table className="t compact">
        <caption className="sr">Largest restored tables by row count</caption>
        <thead>
          <tr>
            <th scope="col">Table</th>
            <th scope="col" className="right">
              Rows
            </th>
          </tr>
        </thead>
        <tbody>
          {top.map((t) => (
            <tr key={`${t.database}.${t.name}`}>
              <td className="wrap">
                <span className="mono">
                  <span className="muted">{t.database}.</span>
                  {t.name}
                </span>
              </td>
              <td className="right">{formatCount(t.rows)}</td>
            </tr>
          ))}
          {rest.length ? (
            <tr>
              <td className="muted">and {plural(rest.length, 'more table')}</td>
              <td className="right muted">{formatCount(restRows)}</td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  )
}

export function DrillResultView({ result, tz, now, showRunLink = true }: { result: DrillResult; tz: string; now: Date; showRunLink?: boolean }) {
  const tables = result.tables ?? []
  const failedChecks = result.checks.some((c) => c.status === 'failed')
  const meta: string[] = []
  if (result.duration_ms != null) meta.push(`took ${formatDuration(result.duration_ms)}`)
  if (result.restored_bytes != null) meta.push(`${formatBytes(result.restored_bytes)} restored`)
  if (result.finished_at) meta.push(`finished ${formatWhen(result.finished_at, tz, now)}`)
  if (result.cleaned_up === false) meta.push('drill databases kept')
  return (
    <div className="drill">
      <DrillEvidence result={result} now={now} />
      <p className="sub drill-meta">
        Snapshot{' '}
        <Link to="/jobs/$job" params={{ job: result.job }} search={{ snapshot: result.snapshot }} className="mono">
          {result.snapshot}
        </Link>{' '}
        into <span style={{ color: 'var(--fg)' }}>{result.target}</span>
        {meta.length ? ` · ${meta.join(' · ')}` : ''}
        {showRunLink ? (
          <>
            {' · '}
            <Link to="/runs/$run" params={{ run: result.run_id }}>
              Open run
            </Link>
          </>
        ) : null}
      </p>
      {result.error && !failedChecks && result.state === 'failed' ? (
        <div className="callout err box" role="note">
          <ErrorIcon />
          <span className="text mono" style={{ fontSize: 13 }}>
            {result.error}
          </span>
        </div>
      ) : null}
      <div className="split drill-split">
        <div className="main stack" style={{ gap: 10 }}>
          <h3 className="h3">Checks</h3>
          <DrillChecks checks={result.checks} />
        </div>
        {tables.length ? (
          <div className="side stack" style={{ gap: 10 }}>
            <h3 className="h3">Largest tables</h3>
            <DrillTables tables={tables} />
          </div>
        ) : null}
      </div>
    </div>
  )
}

function DrillHistory({ job, tz, now }: { job: string; tz: string; now: Date }) {
  const q = useQuery(drillsQuery(job, 10))
  if (q.isSuccess && q.data.length === 0) return null
  return (
    <div className="drill-hist">
      <h3 className="h3" style={{ padding: '14px 16px 4px' }}>
        Recent drills
      </h3>
      {q.isPending ? (
        <BlockSkeleton lines={3} label="Loading drill history" />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load the drill history" />
      ) : (
        <div className="scroll-x">
          <table className="t compact">
            <caption className="sr">Recent restore drills of {job}, newest first</caption>
            <thead>
              <tr>
                <th scope="col">Drill</th>
                <th scope="col">Result</th>
                <th scope="col">Snapshot</th>
                <th scope="col">Checks</th>
                <th scope="col" className="right">
                  Duration
                </th>
              </tr>
            </thead>
            <tbody>
              {q.data.map((d) => (
                <tr key={d.run_id}>
                  <td>
                    <Link to="/runs/$run" params={{ run: d.run_id }}>
                      {formatWhen(d.finished_at ?? d.started_at, tz, now)}
                    </Link>
                  </td>
                  <td>
                    <Status info={DRILL_STATE[drillState(d)]} small />
                  </td>
                  <td className="mono">{d.snapshot}</td>
                  <td>{checkSummary(d)}</td>
                  <td className="right">{formatDuration(d.duration_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export function DrillCard({ job, me, now }: { job: JobDetail; me: Me; now: Date }) {
  const tz = me.deployment.timezone
  const drill = useRunDrill()
  const result = job.last_drill_result
  const block = drillBlockReason(me, job)
  const canDrill = can(me.roles, 'drill')

  return (
    <section aria-labelledby="drill-h" className="card card-clip">
      <div className="hd">
        <div className="row" style={{ gap: '8px 14px' }}>
          <h2 id="drill-h" className="h2">
            Restore drill
          </h2>
          {result ? <Status info={DRILL_STATE[drillState(result)]} /> : null}
        </div>
        {canDrill ? (
          <button
            type="button"
            className="btn btn-sm"
            disabled={block !== null || drill.isPending}
            title={block ?? undefined}
            aria-describedby={block ? 'drill-block' : undefined}
            onClick={() => drill.mutate({ job: job.name })}
          >
            {drill.isPending ? 'Starting…' : 'Run drill now'}
          </button>
        ) : null}
      </div>
      <div className="body" style={{ gap: 16 }}>
        {block && canDrill ? (
          <span id="drill-block" className="sr">
            {block}
          </span>
        ) : null}
        {!job.drill ? (
          <div className="stack" style={{ gap: 8 }}>
            <p className="evidence none">Restores of {job.name} are not tested automatically.</p>
            <p className="muted" style={{ margin: 0 }}>
              Add a <span className="chip">drill</span> section to the job in the server configuration. Cistern then restores the latest snapshot into a
              non-production target on a schedule, checks tables, row counts and your SQL checks, and drops the copy again.
            </p>
          </div>
        ) : result ? (
          <DrillResultView result={result} tz={tz} now={now} />
        ) : (
          <div className="stack" style={{ gap: 8 }}>
            <p className="evidence none">No restore drill has run yet.</p>
            <p className="muted" style={{ margin: 0 }}>
              {job.drill.schedule ? 'The first scheduled drill will appear here.' : 'Drills of this job run only on demand.'}
            </p>
          </div>
        )}
        {drill.isError ? <InlineError error={drill.error} /> : null}
      </div>
      {job.drill ? (
        <>
          <div className="cells drill-cfg">
            <div className="cell">
              <span className="sub">Schedule</span>
              <span>{drillScheduleText(job.drill)}</span>
            </div>
            <div className="cell">
              <span className="sub">Target</span>
              <span>
                {job.drill.target}, suffix <span className="mono">{job.drill.restore_suffix}</span>
              </span>
            </div>
            <div className="cell">
              <span className="sub">Max age</span>
              <span>{maxAgeText(job.drill.max_age_days)}</span>
            </div>
            <div className="cell">
              <span className="sub">After the checks</span>
              <span>
                {job.drill.keep ? 'Drill databases are kept' : 'Drill databases are dropped'}
                {job.drill.checks ? `, ${plural(job.drill.checks, 'SQL check')}` : ''}
              </span>
            </div>
          </div>
          <DrillHistory job={job.name} tz={tz} now={now} />
        </>
      ) : null}
    </section>
  )
}
