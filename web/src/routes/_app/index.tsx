import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { overviewQuery, type Overview } from '~/api/queries'
import { ActivityList } from '~/components/ActivityList'
import { AlertsCallout } from '~/components/AlertsCallout'
import { RefreshIcon } from '~/components/Icons'
import { JobsTable } from '~/components/JobsTable'
import { PendingApprovals } from '~/components/PendingApprovals'
import { BlockSkeleton, Skel, TableSkeleton } from '~/components/Skeleton'
import { ErrorState, InlineError } from '~/components/States'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { useRunBackup } from '~/hooks/useRunActions'
import { formatBytes, formatRelative, plural, timeZoneLabel } from '~/lib/format'

export const Route = createFileRoute('/_app/')({
  head: () => ({ meta: [{ title: 'Overview · Cistern' }] }),
  component: OverviewPage,
})

export function overviewSubtitle(data: Overview, tz: string): string {
  const parts = [plural(data.jobs.length, 'job')]
  for (const r of data.repositories ?? []) {
    let s = `${formatBytes(r.stored_bytes)} in ${r.name}`
    if (r.object_lock) s += `, Object Lock ${r.object_lock.mode} for ${plural(r.object_lock.days, 'day')}`
    parts.push(s)
  }
  parts.push(`times in ${timeZoneLabel(tz)}`)
  return parts.join(' · ')
}

function OverviewPage() {
  const me = useMe()
  const now = useNow(15_000)
  const tz = me.deployment.timezone
  const q = useQuery(overviewQuery)
  const backup = useRunBackup()

  return (
    <div className="page">
      <div className="page-head">
        <div className="page-title">
          <h1 className="h1">Overview</h1>
          <p>{q.data ? overviewSubtitle(q.data, tz) : <Skel w={360} />}</p>
        </div>
        <div className="row" style={{ gap: 12 }}>
          {q.dataUpdatedAt ? <span className="sub">Updated {formatRelative(new Date(q.dataUpdatedAt), new Date(Math.max(now.getTime(), q.dataUpdatedAt)))}</span> : null}
          <button type="button" className="btn" onClick={() => void q.refetch()} disabled={q.isFetching} aria-busy={q.isFetching}>
            <RefreshIcon />
            {q.isFetching && q.data ? 'Refreshing' : 'Refresh'}
          </button>
        </div>
      </div>

      {q.isError && !q.data ? (
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load the overview" />
        </div>
      ) : null}

      {q.data ? <AlertsCallout alerts={q.data.alerts} jobs={q.data.jobs} tz={tz} now={now} /> : null}

      {backup.isError ? <InlineError error={backup.error} /> : null}

      {q.data ? (
        <JobsTable
          jobs={q.data.jobs}
          me={me}
          now={now}
          onRunBackup={(job) => backup.mutate(job)}
          busyJob={backup.isPending ? backup.variables : null}
        />
      ) : q.isPending ? (
        <section className="card" aria-label="Jobs">
          <div className="hd">
            <h2 className="h2">Jobs</h2>
          </div>
          <TableSkeleton rows={5} cols={7} label="Loading jobs" />
        </section>
      ) : null}

      <div className="split">
        <section aria-labelledby="act-h" className="card main">
          <div className="hd">
            <h2 id="act-h" className="h2">
              Recent activity
            </h2>
            <Link to="/audit">Audit log</Link>
          </div>
          {q.data ? <ActivityList entries={q.data.recent_activity} tz={tz} now={now} /> : <BlockSkeleton lines={5} label="Loading activity" />}
        </section>
        <section aria-labelledby="appr-h" className="card side">
          <div className="hd">
            <h2 id="appr-h" className="h2">
              Waiting for approval
            </h2>
          </div>
          {q.data ? <PendingApprovals approvals={q.data.pending_approvals} now={now} /> : <BlockSkeleton lines={3} label="Loading approvals" />}
        </section>
      </div>
    </div>
  )
}
