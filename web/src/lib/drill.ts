import type { DrillCheck, DrillResult, JobDetail, Me } from '~/api/queries'
import { isActive } from '~/api/queries'
import { describeCron, formatCount, formatRelative, plural } from './format'
import { blockReason, can } from './roles'
import type { StatusInfo } from './status'

export const CHECK_KIND_LABEL: Record<DrillCheck['kind'], string> = {
  restore: 'Restore',
  tables: 'Tables',
  rows: 'Rows',
  sql: 'SQL check',
  objects: 'Objects',
  cleanup: 'Cleanup',
}

export const CHECK_STATUS: Record<DrillCheck['status'], StatusInfo> = {
  passed: { text: 'Passed', tone: 'ok' },
  failed: { text: 'Failed', tone: 'fail' },
  skipped: { text: 'Skipped', tone: 'neutral' },
}

export function formatCompact(n: number): string {
  const abs = Math.abs(n)
  if (abs < 10_000) return formatCount(n)
  const units: [number, string][] = [
    [1e12, 'T'],
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'K'],
  ]
  for (const [size, suffix] of units) {
    if (abs >= size) {
      const v = n / size
      const text = Math.abs(v) < 100 ? v.toFixed(1).replace(/\.0$/, '') : String(Math.round(v))
      return `${text} ${suffix}`
    }
  }
  return formatCount(n)
}

export function drillPassed(r: DrillResult): boolean {
  return r.state === 'succeeded' && !r.checks.some((c) => c.status === 'failed')
}

export function drillState(r: DrillResult): DrillResult['state'] {
  if (r.state === 'succeeded' && r.checks.some((c) => c.status === 'failed')) return 'failed'
  return r.state
}

export function totalRows(r: DrillResult): number {
  return r.total_rows ?? (r.tables ?? []).reduce((a, t) => a + t.rows, 0)
}

export function totalTables(r: DrillResult): number {
  return r.total_tables ?? (r.tables ?? []).length
}

function objectCount(r: DrillResult): string | null {
  const c = r.checks.find((x) => x.kind === 'objects' && x.actual)
  if (!c?.actual) return null
  const n = Number(c.actual.replace(/[,\s]/g, ''))
  return Number.isFinite(n) ? `${formatCount(n)} objects` : null
}

export function drillContents(r: DrillResult): string | null {
  const tables = totalTables(r)
  const rows = totalRows(r)
  if (tables > 0) return `${plural(tables, 'table')}, ${formatCompact(rows)} ${rows === 1 ? 'row' : 'rows'}`
  return objectCount(r)
}

export function drillEvidence(r: DrillResult, now: Date = new Date()): string {
  if (isActive(r.state)) return 'A restore drill is running now.'
  const when = formatRelative(r.finished_at ?? r.started_at, now)
  const failed = r.checks.filter((c) => c.status === 'failed')
  if (drillPassed(r)) {
    const what = drillContents(r)
    const checks = r.checks.length ? 'all checks passed' : 'no checks configured'
    return what ? `Restored and checked ${when}: ${what}, ${checks}.` : `Restored and checked ${when}, ${checks}.`
  }
  if (r.state === 'unknown') return `The restore drill ${when} ended in an unknown state.`
  if (failed.length > 0) return `The restore drill ${when} failed ${failed.length} of ${plural(r.checks.length, 'check')}.`
  return `The restore drill ${when} failed before its checks ran.`
}

export function checkSummary(r: DrillResult): string {
  const passed = r.checks.filter((c) => c.status === 'passed').length
  const failed = r.checks.filter((c) => c.status === 'failed').length
  if (r.checks.length === 0) return 'No checks'
  if (failed) return `${failed} of ${r.checks.length} failed`
  return `${passed} of ${r.checks.length} passed`
}

export function drillScheduleText(drill: NonNullable<JobDetail['drill']>): string {
  const s = describeCron(drill.schedule)
  return s ? `Runs ${s}` : 'On demand only, no schedule'
}

export function maxAgeText(days: number): string {
  return `Fails after ${plural(days, 'day')} without a passed drill`
}

export function drillBlockReason(me: Me, job: JobDetail): string | null {
  if (!can(me.roles, 'drill')) return 'Needs the Restorer role.'
  if (!job.drill) return `${job.name} has no drill section in the server configuration.`
  const blocked = blockReason(me, 'drill')
  if (blocked) return blocked
  if (isActive(job.current_run?.state)) return `A ${job.current_run?.op ?? 'run'} is in progress.`
  if (job.snapshots === 0) return 'There is no snapshot to restore yet.'
  return null
}
