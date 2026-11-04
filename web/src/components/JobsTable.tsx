import { useId, useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { JobSummary, Me } from '~/api/queries'
import { formatBytes, formatElapsed, formatRelative, formatWhen } from '~/lib/format'
import { blockReason, can } from '~/lib/roles'
import { DRILL_STATE, HEALTH } from '~/lib/status'
import { SearchIcon } from './Icons'
import { RunStrip } from './RunStrip'
import { Status } from './Status'

export type JobFilter = 'all' | 'failing' | 'running' | 'drill' | 'verify'

export const JOB_FILTERS: { id: JobFilter; label: string; match: (j: JobSummary) => boolean }[] = [
  { id: 'all', label: 'All', match: () => true },
  { id: 'failing', label: 'Failing', match: (j) => j.health === 'failing' || j.health === 'stale' },
  { id: 'running', label: 'Running', match: (j) => j.health === 'running' || j.running_op != null },
  { id: 'drill', label: 'Drill failing', match: (j) => j.health === 'drill_failing' || j.last_drill?.state === 'failed' },
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

export function LastDrill({ job, tz, now }: { job: JobSummary; tz: string; now: Date }) {
  const d = job.last_drill
  if (!d) return <span className="muted">{job.drill_schedule ? 'Not yet' : 'No drill'}</span>
  const info = DRILL_STATE[d.state]
  const tone = info.tone === 'neutral' ? '' : info.tone
  return (
    <span className={`st ${tone}`} title={`${info.text}, ${formatWhen(d.at, tz, now)}`}>
      <span className="sr">{info.text}, </span>
      {d.state === 'running' || d.state === 'queued' ? info.text : formatRelative(d.at, now)}
    </span>
  )
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
  const drills = me.deployment.features.drills || jobs.some((j) => j.last_drill || j.drill_schedule)
  const filters = drills ? JOB_FILTERS : JOB_FILTERS.filter((f) => f.id !== 'drill')
  const cols = 7 + (drills ? 1 : 0) + (canBackup && onRunBackup ? 1 : 0)

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
            {filters.map((f) => (
              <button key={f.id} type="button" aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
                {f.label}
                <span className="n">{jobs.filter(f.match).length}</span>
              </button>
            ))}
          </div>
        </div>
      </div>
      <div className="scroll-x">
        <table className="t jobs">
          <thead>
            <tr>
              <th scope="col">Job</th>
              <th scope="col">Status</th>
              <th scope="col">Last 30 runs</th>
              <th scope="col">Last success</th>
              <th scope="col">Last verify</th>
              {drills ? <th scope="col">Last drill</th> : null}
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
                  {drills ? (
                    <td>
                      <LastDrill job={j} tz={tz} now={now} />
                    </td>
                  ) : null}
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
                <td colSpan={cols}>
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
