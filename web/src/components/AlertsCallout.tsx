import { Link } from '@tanstack/react-router'
import type { Alert, JobSummary } from '~/api/queries'
import { formatDay, formatWhen } from '~/lib/format'
import { ErrorIcon, WarningIcon } from './Icons'

function trailingFailures(job: JobSummary | undefined): number {
  if (!job) return 0
  let n = 0
  for (let i = job.recent_backups.length - 1; i >= 0; i -= 1) {
    const s = job.recent_backups[i]!.status
    if (s === 'running' || s === 'queued') continue
    if (s !== 'failed') break
    n += 1
  }
  return n
}

export function alertHeadline(alert: Alert, job: JobSummary | undefined, tz: string, now: Date): string {
  switch (alert.kind) {
    case 'backup_failing': {
      const n = trailingFailures(job)
      if (n >= 2) return `${alert.job} failed its last ${n} backups.`
      if (n === 1) return `${alert.job} failed its last backup.`
      return `${alert.job} backups are failing.`
    }
    case 'backup_stale':
      return `${alert.job} has had no successful backup since ${formatWhen(alert.since, tz, now)}.`
    case 'verify_failing':
      return `${alert.job} failed its last verify.`
    case 'verify_stale':
      return job && !job.last_verify ? `${alert.job} has never been verified.` : `${alert.job} has not been verified since ${formatDay(alert.since, tz, now)}.`
    case 'daemon_unreachable':
      return 'The Cistern daemon is not reachable.'
  }
}

function tail(alert: Alert, job: JobSummary | undefined, tz: string, now: Date): string {
  if (alert.kind === 'backup_failing') {
    return job?.last_success ? ` Last good snapshot is from ${formatDay(job.last_success, tz, now)}.` : ' There is no good snapshot yet.'
  }
  return ''
}

function sentence(s: string) {
  const t = s.trim()
  return /[.!?]$/.test(t) ? t : `${t}.`
}

function isError(a: Alert) {
  return a.kind !== 'verify_stale'
}

function Group({ alerts, jobs, tz, now, tone }: { alerts: Alert[]; jobs: readonly JobSummary[]; tz: string; now: Date; tone: 'err' | 'wrn' }) {
  if (alerts.length === 0) return null
  const Icon = tone === 'err' ? ErrorIcon : WarningIcon
  const items = alerts.map((a) => {
    const job = jobs.find((j) => j.name === a.job)
    return { a, job, headline: alertHeadline(a, job, tz, now), rest: `${a.message ? ` ${sentence(a.message)}` : ''}${tail(a, job, tz, now)}` }
  })
  return (
    <section aria-label={tone === 'err' ? 'Needs attention' : 'Warnings'} className={`callout ${tone}`}>
      <Icon />
      <ul className="callout-list">
        {items.map(({ a, headline, rest }) => (
          <li key={`${a.kind}-${a.job}`}>
            <span>
              <strong>{headline}</strong>
              {rest}
            </span>
            {a.job ? (
              <Link to="/jobs/$job" params={{ job: a.job }}>
                Open {a.job}
              </Link>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  )
}

export function AlertsCallout({ alerts, jobs, tz, now }: { alerts: readonly Alert[]; jobs: readonly JobSummary[]; tz: string; now: Date }) {
  const errors = alerts.filter(isError)
  const warnings = alerts.filter((a) => !isError(a))
  return (
    <>
      <Group alerts={errors} jobs={jobs} tz={tz} now={now} tone="err" />
      <Group alerts={warnings} jobs={jobs} tz={tz} now={now} tone="wrn" />
    </>
  )
}
