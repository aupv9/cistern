import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { approvalQuery } from '~/api/queries'
import { ApprovalCard } from '~/components/ApprovalCard'
import { PageSkeleton } from '~/components/Skeleton'
import { ErrorState } from '~/components/States'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'

export const Route = createFileRoute('/_app/approvals/$id')({
  head: ({ params }) => ({ meta: [{ title: `${params.id} · Approvals · Cistern` }] }),
  component: ApprovalPage,
})

function ApprovalPage() {
  const { id } = Route.useParams()
  const me = useMe()
  const now = useNow(30_000)
  const q = useQuery(approvalQuery(id))
  if (q.isPending) return <PageSkeleton title={`Loading ${id}`} />
  return (
    <div className="page">
      <div className="page-title">
        <p>
          <Link to="/approvals">All approvals</Link>
        </p>
      </div>
      {q.isError ? (
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title={`Could not load ${id}`} />
        </div>
      ) : (
        <ApprovalCard approval={q.data} me={me} now={now} headingLevel={1} />
      )}
    </div>
  )
}
