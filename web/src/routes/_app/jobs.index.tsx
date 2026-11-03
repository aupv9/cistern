import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { overviewQuery } from '~/api/queries'
import { JobsTable } from '~/components/JobsTable'
import { TableSkeleton } from '~/components/Skeleton'
import { ErrorState, InlineError } from '~/components/States'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { useRunBackup } from '~/hooks/useRunActions'
import { timeZoneLabel } from '~/lib/format'

export const Route = createFileRoute('/_app/jobs/')({
  head: () => ({ meta: [{ title: 'Jobs · Cistern' }] }),
  component: JobsPage,
})

function JobsPage() {
  const me = useMe()
  const now = useNow(15_000)
  const q = useQuery(overviewQuery)
  const backup = useRunBackup()
  return (
    <div className="page">
      <div className="page-title">
        <h1 className="h1">Jobs</h1>
        <p>Every configured backup job, its recent runs and when it runs next. Times in {timeZoneLabel(me.deployment.timezone)}.</p>
      </div>
      {backup.isError ? <InlineError error={backup.error} /> : null}
      {q.data ? (
        <JobsTable
          jobs={q.data.jobs}
          me={me}
          now={now}
          title="All jobs"
          onRunBackup={(job) => backup.mutate(job)}
          busyJob={backup.isPending ? backup.variables : null}
        />
      ) : q.isError ? (
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load jobs" />
        </div>
      ) : (
        <section className="card" aria-label="Jobs">
          <TableSkeleton rows={5} cols={7} label="Loading jobs" />
        </section>
      )}
    </div>
  )
}
