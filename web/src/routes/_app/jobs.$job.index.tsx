import { useId, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { isActive, jobQuery, manifestQuery, snapshotsQuery, type JobDetail, type Me, type Run, type SnapshotSummary } from '~/api/queries'
import { DrillCard } from '~/components/Drill'
import { HistorySection } from '~/components/HistoryCharts'
import { RunStrip, stripSummary } from '~/components/RunStrip'
import { RunProgress } from '~/components/RunProgress'
import { BlockSkeleton, PageSkeleton, TableSkeleton } from '~/components/Skeleton'
import { EmptyState, ErrorState, InlineError } from '~/components/States'
import { Status } from '~/components/Status'
import { ErrorIcon } from '~/components/Icons'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { useRunBackup, useRunVerify } from '~/hooks/useRunActions'
import { formatBytes, formatDay, formatDuration, formatTime, formatWhen, formatWhenLower, median, plural, shortHash } from '~/lib/format'
import { encryptionText, keptByText, objectLockText, retentionText, scheduleLines, sourceLine } from '~/lib/job'
import { blockReason, can } from '~/lib/roles'
import { HEALTH, OP_LABEL, RUN_STATE, VERIFY_STATE } from '~/lib/status'

type JobSearch = { snapshot?: string }

export const Route = createFileRoute('/_app/jobs/$job/')({
  validateSearch: (search: Record<string, unknown>): JobSearch => ({
    snapshot: typeof search.snapshot === 'string' && search.snapshot ? search.snapshot : undefined,
  }),
  head: ({ params }) => ({ meta: [{ title: `${params.job} · Cistern` }] }),
  component: JobPage,
})

function JobPage() {
  const { job } = Route.useParams()
  const me = useMe()
  const now = useNow(5_000)
  const q = useQuery(jobQuery(job))

  if (q.isPending) return <PageSkeleton title={`Loading ${job}`} />
  if (q.isError) {
    return (
      <div className="page">
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title={`Could not load ${job}`} />
        </div>
      </div>
    )
  }
  return <JobView job={q.data} me={me} now={now} />
}

function JobActions({ job, me }: { job: JobDetail; me: Me }) {
  const backup = useRunBackup()
  const verify = useRunVerify()
  const running = isActive(job.current_run?.state)
  const backupBlock = blockReason(me, 'backup')
  const restoreBlock = blockReason(me, 'restore')
  const busyTitle = running ? `A ${job.current_run?.op ?? 'run'} is in progress` : undefined
  const err = backup.error ?? verify.error
  return (
    <div className="stack" style={{ gap: 8, alignItems: 'flex-end' }}>
      <div className="row">
        {can(me.roles, 'verify') ? (
          <button
            type="button"
            className="btn"
            disabled={backupBlock !== null || running || verify.isPending || job.snapshots === 0}
            title={backupBlock ?? busyTitle}
            onClick={() => verify.mutate({ job: job.name, snapshot: 'latest' })}
          >
            {verify.isPending ? 'Starting…' : 'Verify latest'}
          </button>
        ) : null}
        {can(me.roles, 'restore') ? (
          restoreBlock || job.snapshots === 0 ? (
            <button type="button" className="btn" disabled title={restoreBlock ?? 'There is no snapshot to restore yet'}>
              Restore
            </button>
          ) : (
            <Link to="/jobs/$job/restore" params={{ job: job.name }} className="btn">
              Restore
            </Link>
          )
        ) : null}
        {can(me.roles, 'backup') ? (
          <button
            type="button"
            className="btn btn-p"
            disabled={backupBlock !== null || running || backup.isPending}
            title={backupBlock ?? busyTitle}
            onClick={() => backup.mutate(job.name)}
          >
            {backup.isPending ? 'Starting…' : 'Run backup'}
          </button>
        ) : null}
      </div>
      {err ? <InlineError error={err} /> : null}
    </div>
  )
}

function JobView({ job, me, now }: { job: JobDetail; me: Me; now: Date }) {
  const tz = me.deployment.timezone
  const [schedule, verifySchedule] = scheduleLines(job)
  const [encMain, encSub] = encryptionText(job)
  const points = job.recent_backups
  const summary = stripSummary(points)
  const durations = points.filter((p) => p.status === 'succeeded' && p.duration_ms != null).map((p) => p.duration_ms as number)
  const med = median(durations)
  const first = points[0]
  const last = points.at(-1)
  const range = first && last ? `${formatDay(first.started_at, tz, now)} to ${formatDay(last.started_at, tz, now)}` : null
  const stripLine = [range, `${summary.succeeded} of ${summary.total} succeeded`, med != null ? `median ${formatDuration(med)}` : null]
    .filter(Boolean)
    .join(' · ')

  return (
    <div className="page">
      <div className="page-head">
        <div className="page-title">
          <div className="row" style={{ gap: 14 }}>
            <h1 className="h1">{job.name}</h1>
            <Status info={HEALTH[job.health]} />
          </div>
          <p>{sourceLine(job)}</p>
        </div>
        <JobActions job={job} me={me} />
      </div>

      {job.health === 'failing' && job.last_failure_error ? (
        <section aria-label="Last failure" className="callout err">
          <ErrorIcon />
          <span className="text">
            <strong>The last backup failed {job.last_failure ? formatWhenLower(job.last_failure, tz, now) : ''}.</strong>{' '}
            <span className="mono" style={{ fontSize: 13 }}>
              {job.last_failure_error}
            </span>
          </span>
        </section>
      ) : null}

      <section aria-label="Job summary" className="card">
        <div className="cells">
          <div className="cell">
            <span className="sub">Schedule</span>
            <span>
              {schedule}
              <br />
              {verifySchedule}
            </span>
          </div>
          <div className="cell">
            <span className="sub">Retention</span>
            <span>
              {retentionText(job.retention)}
              <br />
              {plural(job.snapshots, 'snapshot')} kept, {formatBytes(job.stored_bytes)}
              {can(me.roles, 'prune_preview') ? (
                <>
                  <br />
                  <Link to="/jobs/$job/prune" params={{ job: job.name }}>
                    Preview prune
                  </Link>
                </>
              ) : null}
            </span>
          </div>
          <div className="cell">
            <span className="sub">Repository</span>
            <span>
              {job.repository}
              <br />
              {objectLockText(job.object_lock)}
            </span>
          </div>
          <div className="cell">
            <span className="sub">Encryption</span>
            <span>
              {encMain}
              <br />
              {encSub}
            </span>
          </div>
        </div>
        <div className="row" style={{ alignItems: 'flex-end', justifyContent: 'space-between', gap: '12px 24px', padding: '16px 20px' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, minWidth: 0 }}>
            <span className="sub">Last {points.length || 30} backups</span>
            <RunStrip points={points} size="lg" noun="backups" timeZone={tz} />
          </div>
          {points.length ? <span className="sub num">{stripLine}</span> : null}
        </div>
      </section>

      {job.current_run ? <CurrentRun run={job.current_run} tz={tz} now={now} /> : null}

      <DrillCard job={job} me={me} now={now} />

      <Snapshots job={job} me={me} now={now} />

      <HistorySection job={job.name} tz={tz} now={now} />
    </div>
  )
}

function CurrentRun({ run, tz, now }: { run: Run; tz: string; now: Date }) {
  const [open, setOpen] = useState(false)
  const logId = useId()
  const by = run.trigger === 'schedule' ? 'schedule' : (run.requested_by?.name ?? run.trigger)
  const at = run.started_at ?? run.queued_at
  return (
    <section aria-labelledby="run-h" className="card" style={{ gap: 12, padding: '16px 20px' }}>
      <div className="row" style={{ justifyContent: 'space-between', gap: '8px 16px' }}>
        <div className="row" style={{ gap: '8px 14px' }}>
          <h2 id="run-h" className="h2">
            <span className={`st ${RUN_STATE[run.state].tone}`} style={{ fontSize: 16 }}>
              {OP_LABEL[run.op]} {RUN_STATE[run.state].text.toLowerCase()}
            </span>
          </h2>
          <span className="sub">
            {run.snapshot ? (
              <>
                snapshot <span className="mono" style={{ color: 'var(--fg)' }}>{run.snapshot}</span> ·{' '}
              </>
            ) : null}
            started by {by}
            {at ? ` at ${formatTime(at, tz)}` : ''}
          </span>
        </div>
        <div className="row">
          <button type="button" className="btn btn-sm" aria-expanded={open} aria-controls={logId} onClick={() => setOpen((v) => !v)}>
            {open ? 'Hide log' : 'Show log'}
          </button>
          <Link to="/runs/$run" params={{ run: run.id }} className="btn btn-sm">
            Open run
          </Link>
        </div>
      </div>
      <RunProgress run={run} now={now} label={`${OP_LABEL[run.op]} progress`} />
      <div id={logId} hidden={!open}>
        {open ? <pre className="log">{(run.log_tail ?? []).join('\n') || 'No log lines yet.'}</pre> : null}
      </div>
    </section>
  )
}

const PAGE = 10

function Snapshots({ job, me, now }: { job: JobDetail; me: Me; now: Date }) {
  const tz = me.deployment.timezone
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const q = useQuery(snapshotsQuery(job.name))
  const [all, setAll] = useState(false)
  const snaps = q.data ?? []
  const selectedId = search.snapshot && snaps.some((s) => s.id === search.snapshot) ? search.snapshot : snaps[0]?.id
  const selected = snaps.find((s) => s.id === selectedId)
  const shown = all ? snaps : snaps.slice(0, PAGE)
  if (selected && !shown.includes(selected)) shown.push(selected)

  return (
    <div className="split">
      <section aria-labelledby="snap-h" className="card card-clip main">
        <div className="hd">
          <h2 id="snap-h" className="h2">
            Snapshots
          </h2>
          {snaps.length > PAGE ? (
            <div className="row">
              <span className="sub">
                {shown.length} of {snaps.length} shown
              </span>
              <button type="button" className="btn btn-sm" onClick={() => setAll((v) => !v)}>
                {all ? 'Show fewer' : 'Show all'}
              </button>
            </div>
          ) : null}
        </div>
        {q.isPending ? (
          <TableSkeleton rows={6} cols={6} label="Loading snapshots" />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load snapshots" />
        ) : snaps.length === 0 ? (
          <EmptyState title="No snapshots yet">
            <p className="muted">The first successful backup of {job.name} will appear here.</p>
          </EmptyState>
        ) : (
          <div className="scroll-x">
            <table className="t compact">
              <caption className="sr">Snapshots of {job.name}. Choose one to see its manifest.</caption>
              <thead>
                <tr>
                  <th scope="col">Snapshot</th>
                  <th scope="col">Created</th>
                  <th scope="col" className="right">
                    Size
                  </th>
                  <th scope="col" className="right">
                    Duration
                  </th>
                  <th scope="col">Verify</th>
                  <th scope="col">Kept as</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((s) => {
                  const on = s.id === selectedId
                  return (
                    <tr key={s.id} className={on ? 'selected' : undefined}>
                      <td>
                        <button
                          type="button"
                          className="snap-pick"
                          aria-pressed={on}
                          onClick={() => void navigate({ search: { snapshot: s.id }, replace: true, resetScroll: false })}
                        >
                          {s.id}
                        </button>
                      </td>
                      <td>{formatWhen(s.started_at, tz, now)}</td>
                      <td className="right">{formatBytes(s.stored_bytes)}</td>
                      <td className="right">{formatDuration(s.duration_ms)}</td>
                      <td>{s.last_verify ? <Status info={VERIFY_STATE[s.last_verify.status]} /> : <span className="st">Not run</span>}</td>
                      <td className="wrap" style={{ minWidth: 140 }}>
                        {s.kept_by.length ? (
                          <span className="muted">{keptByText(s.kept_by)}</span>
                        ) : (
                          <span className="st warn">Next prune removes it</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <ManifestPanel job={job} me={me} snapshot={selected} now={now} />
    </div>
  )
}

function lockText(s: SnapshotSummary, tz: string, now: Date) {
  if (!s.lock_until) return 'None'
  const until = new Date(s.lock_until)
  return until.getTime() > now.getTime() ? `Until ${formatDay(until, tz, now)}` : `Expired ${formatDay(until, tz, now)}`
}

function ManifestPanel({ job, me, snapshot, now }: { job: JobDetail; me: Me; snapshot: SnapshotSummary | undefined; now: Date }) {
  const tz = me.deployment.timezone
  const q = useQuery({ ...manifestQuery(job.name, snapshot?.id ?? ''), enabled: !!snapshot })
  const verify = useRunVerify()
  const verifyBlock = blockReason(me, 'verify')
  const restoreBlock = blockReason(me, 'restore')
  const running = isActive(job.current_run?.state)

  return (
    <aside aria-labelledby="man-h" className="card side">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '14px 20px', borderBottom: '1px solid var(--border)' }}>
        <h2 id="man-h" className="h2">
          Manifest
        </h2>
        <span className="mono muted" style={{ overflowWrap: 'anywhere' }}>
          {snapshot?.id ?? 'No snapshot selected'}
        </span>
      </div>
      {!snapshot ? (
        <div className="body">
          <span className="muted">Choose a snapshot to see what it contains.</span>
        </div>
      ) : q.isPending ? (
        <BlockSkeleton lines={6} label="Loading manifest" />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load the manifest" />
      ) : (
        <div className="body" style={{ gap: 20 }}>
          <dl className="kv">
            <dt>Created</dt>
            <dd>{formatWhen(snapshot.started_at, tz, now)}</dd>
            {q.data.hostname ? (
              <>
                <dt>Host</dt>
                <dd className="mono">{q.data.hostname}</dd>
              </>
            ) : null}
            {q.data.source?.version ? (
              <>
                <dt>Server</dt>
                <dd>
                  {job.source_label?.split(' ')[0] ?? q.data.driver} {q.data.source.version}
                </dd>
              </>
            ) : null}
            <dt>Cistern</dt>
            <dd>
              {q.data.cistern_version ? `v${q.data.cistern_version}, ` : ''}format {q.data.format}
            </dd>
            <dt>Stored</dt>
            <dd>
              {formatBytes(snapshot.stored_bytes)}
              <span className="muted"> of {formatBytes(snapshot.plain_bytes)} plain</span>
            </dd>
            <dt>Object Lock</dt>
            <dd>{lockText(snapshot, tz, now)}</dd>
            <dt>Kept by</dt>
            <dd>{snapshot.kept_by.length ? keptByText(snapshot.kept_by) : 'No rule, the next prune removes it'}</dd>
            <dt>Last verify</dt>
            <dd>
              {snapshot.last_verify ? (
                <Status info={{ ...VERIFY_STATE[snapshot.last_verify.status], text: `${VERIFY_STATE[snapshot.last_verify.status].text} ${formatWhenLower(snapshot.last_verify.at, tz, now)}` }} />
              ) : (
                'Never verified'
              )}
            </dd>
            {snapshot.depends_on?.length ? (
              <>
                <dt>Depends on</dt>
                <dd className="mono">{snapshot.depends_on.join(', ')}</dd>
              </>
            ) : null}
            {q.data.encryption ? (
              <>
                <dt>Encryption</dt>
                <dd>
                  {q.data.encryption}
                  {q.data.compression ? `, ${q.data.compression}` : ''}
                </dd>
              </>
            ) : null}
          </dl>
          <div className="artifacts" aria-label="Artifacts" role="list">
            {q.data.artifacts.map((a) => (
              <div className="artifact" key={a.path} role="listitem">
                <div className="top">
                  <span className="mono" style={{ overflowWrap: 'anywhere' }}>
                    {a.name}
                  </span>
                  <span className="sub num">{formatBytes(a.stored_size)}</span>
                </div>
                <span className="hash" title={a.stored_sha256}>
                  <span aria-hidden="true">stored sha256 {shortHash(a.stored_sha256)}</span>
                  <span className="sr">stored sha256 {a.stored_sha256}</span>
                </span>
                <span className="hash" title={a.plain_sha256}>
                  <span aria-hidden="true">plain sha256 {shortHash(a.plain_sha256)}</span>
                  <span className="sr">plain sha256 {a.plain_sha256}</span>
                </span>
              </div>
            ))}
          </div>
          <div className="row">
            {can(me.roles, 'restore') && !restoreBlock ? (
              <Link to="/jobs/$job/restore" params={{ job: job.name }} search={{ snapshot: snapshot.id }} className="btn btn-p">
                Restore this snapshot
              </Link>
            ) : null}
            {can(me.roles, 'verify') ? (
              <button
                type="button"
                className="btn"
                disabled={verifyBlock !== null || running || verify.isPending}
                title={verifyBlock ?? (running ? 'A run is in progress' : undefined)}
                onClick={() => verify.mutate({ job: job.name, snapshot: snapshot.id })}
              >
                {verify.isPending ? 'Starting…' : 'Verify'}
              </button>
            ) : null}
            <a
              className="btn"
              href={`/api/v1/jobs/${encodeURIComponent(job.name)}/snapshots/${encodeURIComponent(snapshot.id)}/manifest.json`}
              download
            >
              Download manifest
            </a>
          </div>
          {verify.isError ? <InlineError error={verify.error} /> : null}
        </div>
      )}
    </aside>
  )
}
