import { useId, useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { JobSummary, Me } from '~/api/queries'
import { formatBytes, formatElapsed, formatWhen } from '~/lib/format'
import { blockReason, can } from '~/lib/roles'
import { HEALTH } from '~/lib/status'
import { SearchIcon } from './Icons'
import { RunStrip } from './RunStrip'
import { Status } from './Status'

export type JobFilter = 'all' | 'failing' | 'running' | 'verify'

export const JOB_FILTERS: { id: JobFilter; label: string; match: (j: JobSummary) => boolean }[] = [
  { id: 'all', label: 'All', match: () => true },
  { id: 'failing', label: 'Failing', match: (j) => j.health === 'failing' || j.health === 'stale' },
  { id: 'running', label: 'Running', match: (j) => j.health === 'running' || j.running_op != null },
  { id: 'verify', label: 'Verify overdue', match: (j) => j.health === 'verify_overdue' },
]

export function filterJobs(jobs: readonly JobSummary[], filter: JobFilter, q: string): JobSummary[] {
  const f = JOB_FILTERS.find((x) => x.id === filter) ?? JOB_FILTERS[0]!
  const needle = q.trim().toLowerCase()
  return jobs.filter((j) => {
    if (!f.match(j)) return false
    if (!needle) return true
    return [j.name, j.driver, j.source_label, j.repository].some((v) => v?.toLowerCase().includes(needle))
  })
}

function runningSince(j: JobSummary): string | undefined {
  const last = j.recent_backups.at(-1)
  if (last && (last.status === 'running' || last.status === 'queued')) return last.started_at
  return undefined
}

export function JobsTable({
  jobs,
  me,
  now,
  title = 'Jobs',
  headingLevel = 2,
  onRunBackup,
  busyJob,
}: {
  jobs: readonly JobSummary[]
  me: Me
  now: Date
  title?: string
  headingLevel?: 2 | 3
  onRunBackup?: (job: string) => void
  busyJob?: string | null
}) {
  const [filter, setFilter] = useState<JobFilter>('all')
  const [q, setQ] = useState('')
  const headingId = useId()
  const searchId = useId()
  const tz = me.deployment.timezone
  const shown = filterJobs(jobs, filter, q)
  const canBackup = can(me.roles, 'backup')
  const backupBlocked = blockReason(me, 'backup')
  const Heading = headingLevel === 2 ? 'h2' : 'h3'

  return (
    <section aria-labelledby={headingId} className="card card-clip">
      <div className="hd">
        <Heading id={headingId} className="h2">
          {title}
        </Heading>
        <div className="row">
          <div className="search">
            <SearchIcon />
            <label htmlFor={searchId} className="sr">
              Search jobs
            </label>
            <input
              id={searchId}
              className="input"
              type="search"
              placeholder="Search jobs"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
          </div>
          <div role="group" aria-label="Filter by status" className="seg">
            {JOB_FILTERS.map((f) => (
              <button key={f.id} type="button" aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
                {f.label}
                <span className="n">{jobs.filter(f.match).length}</span>
              </button>
            ))}
          </div>
        </div>
      </div>
      <div className="scroll-x">
        <table className="t">
          <thead>
            <tr>
              <th scope="col">Job</th>
              <th scope="col">Status</th>
              <th scope="col">Last 30 runs</th>
              <th scope="col">Last success</th>
              <th scope="col">Last verify</th>
              <th scope="col">Next run</th>
              <th scope="col" className="right">
                Stored
              </th>
              {canBackup && onRunBackup ? (
                <th scope="col">
                  <span className="sr">Actions</span>
                </th>
              ) : null}
            </tr>
          </thead>
          <tbody aria-live="polite">
            {shown.map((j) => {
              const since = runningSince(j)
              const running = j.health === 'running' || j.running_op != null
              return (
                <tr key={j.name} data-testid={`job-row-${j.name}`}>
                  <td>
                    <div className="cell-name">
                      <Link to="/jobs/$job" params={{ job: j.name }}>
                        {j.name}
                      </Link>
                      <span className="sub">{j.source_label ?? j.driver}</span>
                    </div>
                  </td>
                  <td>
                    <Status info={HEALTH[j.health]} />
                  </td>
                  <td>
                    <RunStrip points={j.recent_backups} timeZone={tz} />
                  </td>
                  <td>{j.last_success ? formatWhen(j.last_success, tz, now) : 'Never'}</td>
                  <td>{j.last_verify ? formatWhen(j.last_verify, tz, now) : j.verify_schedule ? 'Never' : 'Not scheduled'}</td>
                  <td>
                    {running
                      ? since
                        ? `Running for ${formatElapsed(since, now)}`
                        : `${j.running_op ? j.running_op.charAt(0).toUpperCase() + j.running_op.slice(1) : 'Run'} in progress`
                      : j.next_run
                        ? formatWhen(j.next_run, tz, now)
                        : j.schedule
                          ? '–'
                          : 'Not scheduled'}
                  </td>
                  <td className="right">{formatBytes(j.stored_bytes)}</td>
                  {canBackup && onRunBackup ? (
                    <td className="right">
                      <button
                        type="button"
                        className="btn btn-sm"
                        aria-label={`Run backup of ${j.name}`}
                        title={backupBlocked ?? (running ? 'A run is already in progress' : undefined)}
                        disabled={backupBlocked !== null || running || busyJob === j.name}
                        onClick={() => onRunBackup(j.name)}
                      >
                        {busyJob === j.name ? 'Starting…' : 'Run backup'}
                      </button>
                    </td>
                  ) : null}
                </tr>
              )
            })}
            {shown.length === 0 ? (
              <tr className="empty">
                <td colSpan={canBackup && onRunBackup ? 8 : 7}>
                  <div className="state" style={{ padding: 0 }}>
                    <span>{jobs.length === 0 ? 'No jobs are configured yet.' : 'No jobs match these filters.'}</span>
                    {jobs.length > 0 ? (
                      <button
                        type="button"
                        className="btn btn-sm"
                        onClick={() => {
                          setFilter('all')
                          setQ('')
                        }}
                      >
                        Clear filters
                      </button>
                    ) : null}
                  </div>
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </section>
  )
}
