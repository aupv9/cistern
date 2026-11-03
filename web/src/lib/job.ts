import type { JobDetail, JobSummary } from '~/api/queries'
import { describeCron, formatCount, plural } from './format'

export function sourceLine(job: JobDetail): string {
  const s = job.source ?? {}
  const head = job.source_label ?? job.driver
  const where = s.host ? (s.port ? `${s.host}:${s.port}` : s.host) : s.bucket ? `bucket ${s.bucket}${s.prefix ? `/${s.prefix}` : ''}` : null
  const parts = [where ? `${head} on ${where}` : head]
  const dbs = s.databases ?? s.database
  if (dbs) {
    const list = dbs.split(',').map((d) => d.trim()).filter(Boolean)
    parts.push(`${list.length > 1 ? 'databases' : 'database'} ${list.join(', ')}`)
  }
  if (s.endpoint) parts.push(`endpoint ${s.endpoint}`)
  return parts.join(' · ')
}

export function scheduleLines(job: JobSummary): string[] {
  const b = describeCron(job.schedule)
  const v = describeCron(job.verify_schedule)
  return [b ? `Backup ${b}` : 'No backup schedule, runs from the CLI or here', v ? `Verify ${v}` : 'No verify schedule']
}

const RULES = ['last', 'hourly', 'daily', 'weekly', 'monthly', 'yearly'] as const

export function retentionText(retention: JobDetail['retention']): string {
  const parts = RULES.filter((r) => (retention[r] ?? 0) > 0).map((r) => (r === 'last' ? `${formatCount(retention[r]!)} latest` : `${formatCount(retention[r]!)} ${r}`))
  return parts.length ? parts.join(', ') : 'Keeps everything, never pruned'
}

export function encryptionText(job: JobDetail): [string, string] {
  const mode = job.encryption.mode
  if (!mode || mode === 'none') return ['Not encrypted', 'Artifacts are stored in plain form']
  const label = mode === 'age-x25519' ? 'age X25519' : mode
  return [label, plural(job.encryption.recipients, 'recipient')]
}

export function objectLockText(lock: JobDetail['object_lock']): string {
  return lock ? `Object Lock ${lock.mode}, ${plural(lock.days, 'day')}` : 'No Object Lock'
}

const KEPT_BY_LABEL: Record<string, string> = {
  depends_on: 'needed by a newer snapshot',
  no_policy: 'no retention policy',
}

export function keptByText(keptBy: readonly string[]): string {
  return keptBy.map((k) => KEPT_BY_LABEL[k] ?? k).join(', ')
}
