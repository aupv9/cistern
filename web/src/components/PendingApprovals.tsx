import { Link } from '@tanstack/react-router'
import type { Approval } from '~/api/queries'
import { formatRelative } from '~/lib/format'

export function PendingApprovals({ approvals, now }: { approvals: readonly Approval[]; now: Date }) {
  if (approvals.length === 0) {
    return (
      <div className="body">
        <span className="muted">Nothing is waiting for approval.</span>
      </div>
    )
  }
  return (
    <ul className="list" style={{ padding: 0 }}>
      {approvals.slice(0, 3).map((a) => (
        <li key={a.id} style={{ flexDirection: 'column', alignItems: 'stretch', gap: 6, padding: 16 }}>
          <span style={{ fontWeight: 500 }}>
            Restore {a.job} into {a.target.name}
          </span>
          <span className="sub">
            {a.requested_by.name} asked {formatRelative(a.requested_at, now)}. Expires {formatRelative(a.expires_at, now)}.
          </span>
          <Link to="/approvals/$id" params={{ id: a.id }} className="btn btn-sm" style={{ alignSelf: 'flex-start', marginTop: 8 }}>
            Review request<span className="sr"> {a.id}</span>
          </Link>
        </li>
      ))}
      {approvals.length > 3 ? (
        <li style={{ padding: '12px 16px' }}>
          <Link to="/approvals">See all {approvals.length} requests</Link>
        </li>
      ) : null}
    </ul>
  )
}
