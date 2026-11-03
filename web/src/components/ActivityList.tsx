import { Link } from '@tanstack/react-router'
import type { AuditEntry } from '~/api/queries'
import { formatDay, formatTime } from '~/lib/format'
import { AUDIT_RESULT, auditActionLabel } from '~/lib/status'
import { Status } from './Status'

export function describeEntry(e: AuditEntry): string {
  if (e.summary) return e.summary
  const parts = [e.actor.name, auditActionLabel(e.action).toLowerCase()]
  if (e.job) parts.push(e.job)
  if (e.target) parts.push(`into ${e.target}`)
  return parts.join(' ')
}

function sameDay(a: string, b: Date, tz: string) {
  return formatDay(a, tz, b) === formatDay(b, tz, b)
}

export function ActivityList({ entries, tz, now }: { entries: readonly AuditEntry[]; tz: string; now: Date }) {
  if (entries.length === 0) return <p className="state muted">No activity yet.</p>
  return (
    <ul className="list">
      {entries.map((e) => (
        <li key={e.id}>
          <span className="when">
            <time dateTime={e.time}>{sameDay(e.time, now, tz) ? formatTime(e.time, tz) : formatDay(e.time, tz, now)}</time>
          </span>
          <span className="what">
            {e.run_id ? (
              <Link to="/runs/$run" params={{ run: e.run_id }} className="quiet-link">
                {describeEntry(e)}
              </Link>
            ) : e.approval_id ? (
              <Link to="/approvals/$id" params={{ id: e.approval_id }} className="quiet-link">
                {describeEntry(e)}
              </Link>
            ) : (
              describeEntry(e)
            )}
          </span>
          <Status info={AUDIT_RESULT[e.result]} small />
        </li>
      ))}
    </ul>
  )
}
