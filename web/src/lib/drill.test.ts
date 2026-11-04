import { describe, expect, it } from 'vitest'
import type { DrillResult, JobDetail, Me } from '~/api/queries'
import { checkSummary, drillBlockReason, drillContents, drillEvidence, drillState, formatCompact } from './drill'

const now = new Date('2026-11-04T08:00:00Z')

function result(over: Partial<DrillResult> = {}): DrillResult {
  return {
    run_id: 'run_1',
    job: 'orders-db',
    snapshot: '20261101T190001Z-9315',
    target: 'staging',
    state: 'succeeded',
    started_at: '2026-11-01T22:00:00Z',
    finished_at: '2026-11-01T22:04:00Z',
    duration_ms: 240_000,
    checks: [
      { name: 'Restore', kind: 'restore', status: 'passed' },
      { name: 'Table count', kind: 'tables', status: 'passed', expected: 'at least 14', actual: '14' },
      { name: 'Orders today', kind: 'sql', status: 'passed', expected: 'more than 0', actual: '1,912' },
    ],
    tables: Array.from({ length: 14 }, (_, i) => ({ database: 'orders', name: `t${i}`, rows: i === 0 ? 1_100_000 : 10_000 })),
    ...over,
  }
}

function me(roles: Me['roles'], reachable = true): Me {
  return {
    user: { id: 'u1', email: 'a@example.com', name: 'A' },
    roles,
    csrf_token: 't',
    session_expires_at: '2026-11-04T16:00:00Z',
    deployment: { name: 'p', version: '0.5.0', timezone: 'UTC', daemon: { configured: true, reachable }, features: { prune: false, drills: true } },
  }
}

const job = {
  name: 'orders-db',
  snapshots: 3,
  drill: { target: 'staging', restore_suffix: '_drill', max_age_days: 35, keep: false },
} as unknown as JobDetail

describe('drill evidence', () => {
  it('states tables, rows and checks for a passed drill', () => {
    expect(drillEvidence(result(), now)).toBe('Restored and checked 2 days ago: 14 tables, 1.2 M rows, all checks passed.')
  })

  it('uses the object count when there are no tables', () => {
    const r = result({ tables: undefined, checks: [{ name: 'Objects', kind: 'objects', status: 'passed', expected: '2,359', actual: '2,359' }] })
    expect(drillEvidence(r, now)).toBe('Restored and checked 2 days ago: 2,359 objects, all checks passed.')
  })

  it('counts the failed checks', () => {
    const r = result({
      state: 'failed',
      checks: [
        { name: 'Restore', kind: 'restore', status: 'passed' },
        { name: 'Rows', kind: 'rows', status: 'failed', expected: 'at least 1,000,000', actual: '0' },
        { name: 'Balance', kind: 'sql', status: 'skipped' },
      ],
    })
    expect(drillEvidence(r, now)).toBe('The restore drill 2 days ago failed 1 of 3 checks.')
    expect(checkSummary(r)).toBe('1 of 3 failed')
  })

  it('treats a succeeded run with a failed check as failed', () => {
    const r = result({ checks: [{ name: 'x', kind: 'sql', status: 'failed' }] })
    expect(drillState(r)).toBe('failed')
  })

  it('explains a drill that failed before its checks', () => {
    expect(drillEvidence(result({ state: 'failed', checks: [], tables: [] }), now)).toBe('The restore drill 2 days ago failed before its checks ran.')
  })

  it('describes a running drill', () => {
    expect(drillEvidence(result({ state: 'running', finished_at: undefined }), now)).toBe('A restore drill is running now.')
  })

  it('formats contents and compact numbers', () => {
    expect(drillContents(result({ tables: [{ database: 'd', name: 't', rows: 1 }] }))).toBe('1 table, 1 row')
    expect(formatCompact(9_812)).toBe('9,812')
    expect(formatCompact(45_300)).toBe('45.3 K')
    expect(formatCompact(1_267_866)).toBe('1.3 M')
    expect(formatCompact(2_000_000)).toBe('2 M')
    expect(formatCompact(412_000_000)).toBe('412 M')
  })
})

describe('drill gating', () => {
  it('needs the restorer role, a drill section, the daemon and no active run', () => {
    expect(drillBlockReason(me(['viewer', 'operator']), job)).toBe('Needs the Restorer role.')
    expect(drillBlockReason(me(['viewer', 'restorer']), { ...job, drill: undefined })).toMatch(/no drill section/)
    expect(drillBlockReason(me(['viewer', 'restorer'], false), job)).toBe('The daemon is not reachable.')
    expect(drillBlockReason(me(['viewer', 'restorer']), { ...job, current_run: { id: 'r', job: 'orders-db', op: 'backup', state: 'running', trigger: 'schedule' } })).toBe(
      'A backup is in progress.',
    )
    expect(drillBlockReason(me(['viewer', 'restorer']), { ...job, snapshots: 0 })).toMatch(/no snapshot/)
    expect(drillBlockReason(me(['viewer', 'restorer']), job)).toBeNull()
    expect(drillBlockReason(me(['viewer', 'admin']), job)).toBeNull()
  })
})
