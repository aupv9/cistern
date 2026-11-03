import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { approvalsQuery } from '~/api/queries'
import { ApprovalCard } from '~/components/ApprovalCard'
import { BlockSkeleton, TableSkeleton } from '~/components/Skeleton'
import { EmptyState, ErrorState } from '~/components/States'
import { Status } from '~/components/Status'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { effectiveState } from '~/lib/approvals'
import { formatWhen } from '~/lib/format'
import { APPROVAL_STATE } from '~/lib/status'

export const Route = createFileRoute('/_app/approvals/')({
  head: () => ({ meta: [{ title: 'Approvals · Cistern' }] }),
  component: ApprovalsPage,
})

function ApprovalsPage() {
  const me = useMe()
  const now = useNow(30_000)
  const tz = me.deployment.timezone
  const q = useQuery(approvalsQuery('all'))
  const pending = (q.data ?? []).filter((a) => effectiveState(a, now) === 'pending')
  const decided = (q.data ?? []).filter((a) => effectiveState(a, now) !== 'pending')

  return (
    <div className="page">
      <div className="page-title">
        <h1 className="h1">Approvals</h1>
        <p>Production restores wait here for a second person. Nobody can approve their own request.</p>
      </div>

      {q.isPending ? (
        <div className="card">
          <BlockSkeleton lines={5} label="Loading approvals" />
        </div>
      ) : q.isError ? (
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load approvals" />
        </div>
      ) : pending.length === 0 ? (
        <div className="card">
          <EmptyState title="Nothing is waiting for approval">
            <p className="muted">Requests to restore into production targets show up here.</p>
          </EmptyState>
        </div>
      ) : (
        pending.map((a) => <ApprovalCard key={a.id} approval={a} me={me} now={now} />)
      )}

      <section aria-labelledby="hist-h" className="card card-clip">
        <div className="hd">
          <h2 id="hist-h" className="h2">
            Recent decisions
          </h2>
        </div>
        {q.isPending ? (
          <TableSkeleton rows={3} cols={6} label="Loading decisions" />
        ) : decided.length === 0 ? (
          <EmptyState title="No decisions yet" />
        ) : (
          <div className="scroll-x">
            <table className="t">
              <thead>
                <tr>
                  <th scope="col">Request</th>
                  <th scope="col">Job</th>
                  <th scope="col">Target</th>
                  <th scope="col">Asked by</th>
                  <th scope="col">Decided by</th>
                  <th scope="col">When</th>
                  <th scope="col">Result</th>
                </tr>
              </thead>
              <tbody>
                {decided.map((a) => (
                  <tr key={a.id}>
                    <td>
                      <Link to="/approvals/$id" params={{ id: a.id }} className="mono">
                        {a.id}
                      </Link>
                    </td>
                    <td>{a.job}</td>
                    <td>{a.target.name}</td>
                    <td>{a.requested_by.name}</td>
                    <td>{a.decided_by ? a.decided_by.name : <span className="muted">No one</span>}</td>
                    <td>{formatWhen(a.decided_at ?? a.expires_at, tz, now)}</td>
                    <td>
                      <Status info={APPROVAL_STATE[effectiveState(a, now)]} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
