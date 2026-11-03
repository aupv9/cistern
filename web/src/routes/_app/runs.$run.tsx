import { useEffect, useRef } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { isActive, runQuery, type Run } from '~/api/queries'
import { ErrorIcon } from '~/components/Icons'
import { RunProgress } from '~/components/RunProgress'
import { PageSkeleton } from '~/components/Skeleton'
import { ErrorState } from '~/components/States'
import { Status } from '~/components/Status'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { formatBytes, formatDuration, formatElapsed, formatWhen } from '~/lib/format'
import { OP_LABEL, RUN_STATE } from '~/lib/status'

export const Route = createFileRoute('/_app/runs/$run')({
  head: ({ params }) => ({ meta: [{ title: `Run ${params.run} · Cistern` }] }),
  component: RunPage,
})

export function runStatusSentence(run: Run): string {
  const op = OP_LABEL[run.op]
  switch (run.state) {
    case 'queued':
      return `${op} of ${run.job} is queued.`
    case 'running':
      return `${op} of ${run.job} is running.`
    case 'succeeded':
      return `${op} of ${run.job} succeeded.`
    case 'failed':
      return `${op} of ${run.job} failed.`
    case 'unknown':
      return `The daemon lost track of this ${run.op}. Check the job to see whether it finished.`
  }
}

function RunPage() {
  const { run: id } = Route.useParams()
  const me = useMe()
  const tz = me.deployment.timezone
  const q = useQuery(runQuery(id))
  const active = isActive(q.data?.state)
  const now = useNow(active ? 1_000 : 30_000)
  const queryClient = useQueryClient()
  const logRef = useRef<HTMLPreElement>(null)
  const wasActive = useRef(active)

  useEffect(() => {
    if (wasActive.current && !active && q.data) {
      void queryClient.invalidateQueries({ queryKey: ['job', q.data.job] })
      void queryClient.invalidateQueries({ queryKey: ['overview'] })
    }
    wasActive.current = active
  }, [active, q.data, queryClient])

  const lines = q.data?.log_tail?.length ?? 0
  useEffect(() => {
    const el = logRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    if (nearBottom || lines < 12) el.scrollTop = el.scrollHeight
  }, [lines])

  if (q.isPending) return <PageSkeleton title={`Loading run ${id}`} />
  if (q.isError) {
    return (
      <div className="page">
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title={`Could not load run ${id}`} />
        </div>
      </div>
    )
  }

  const run = q.data
  const by = run.trigger === 'schedule' ? 'Scheduler' : run.trigger === 'cli' ? 'CLI' : (run.requested_by?.name ?? 'Someone')
  const elapsed = run.duration_ms != null ? formatDuration(run.duration_ms) : run.started_at ? formatElapsed(run.started_at, now) : null

  return (
    <div className="page">
      <div className="page-head">
        <div className="page-title">
          <div className="row" style={{ gap: 14 }}>
            <h1 className="h1">
              {OP_LABEL[run.op]} {run.job}
            </h1>
            <span aria-live="polite" aria-atomic="true">
              <Status info={RUN_STATE[run.state]} />
              <span className="sr"> {runStatusSentence(run)}</span>
            </span>
          </div>
          <p>
            Run <span className="mono">{run.id}</span> · started by {by}
            {run.target ? ` · into ${run.target}` : ''}
          </p>
        </div>
        <div className="row">
          <Link to="/jobs/$job" params={{ job: run.job }} className="btn">
            Open {run.job}
          </Link>
          {run.approval_id ? (
            <Link to="/approvals/$id" params={{ id: run.approval_id }} className="btn">
              Open approval
            </Link>
          ) : null}
        </div>
      </div>

      {run.state === 'failed' && run.error ? (
        <section aria-label="Error" className="callout err">
          <ErrorIcon />
          <span className="text">
            <strong>The {run.op} failed.</strong>{' '}
            <span className="mono" style={{ fontSize: 13 }}>
              {run.error}
            </span>
          </span>
        </section>
      ) : null}
      {run.state === 'unknown' ? (
        <section aria-label="Unknown state" className="callout wrn">
          <span className="text">{runStatusSentence(run)}</span>
        </section>
      ) : null}

      <section aria-labelledby="prog-h" className="card">
        <div className="hd">
          <h2 id="prog-h" className="h2">
            Progress
          </h2>
          {active ? <span className="sub">Updates every 2 seconds</span> : null}
        </div>
        <div className="body" style={{ gap: 12 }}>
          {active || run.progress ? <RunProgress run={run} now={now} label={`${OP_LABEL[run.op]} progress`} /> : null}
          <dl className="kv">
            <dt>Queued</dt>
            <dd>{run.queued_at ? formatWhen(run.queued_at, tz, now) : '–'}</dd>
            <dt>Started</dt>
            <dd>{run.started_at ? formatWhen(run.started_at, tz, now) : 'Not yet'}</dd>
            <dt>Finished</dt>
            <dd>{run.finished_at ? formatWhen(run.finished_at, tz, now) : active ? 'Still running' : '–'}</dd>
            <dt>{run.finished_at ? 'Took' : 'Elapsed'}</dt>
            <dd>{elapsed ?? '–'}</dd>
            {run.snapshot ? (
              <>
                <dt>Snapshot</dt>
                <dd>
                  <Link to="/jobs/$job" params={{ job: run.job }} search={{ snapshot: run.snapshot }} className="mono">
                    {run.snapshot}
                  </Link>
                </dd>
              </>
            ) : null}
            {run.stored_bytes != null ? (
              <>
                <dt>Stored</dt>
                <dd>{formatBytes(run.stored_bytes)}</dd>
              </>
            ) : null}
            {run.target ? (
              <>
                <dt>Target</dt>
                <dd>{run.target}</dd>
              </>
            ) : null}
          </dl>
        </div>
      </section>

      <section aria-labelledby="log-h" className="card">
        <div className="hd">
          <h2 id="log-h" className="h2">
            Log
          </h2>
          <span className="sub">Last {run.log_tail?.length ?? 0} lines</span>
        </div>
        <div className="body">
          <pre ref={logRef} className="log" tabIndex={0} aria-label="Run log">
            {(run.log_tail ?? []).join('\n') || 'No log lines yet.'}
          </pre>
        </div>
      </section>
    </div>
  )
}
