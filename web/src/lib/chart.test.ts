import { describe, expect, it } from 'vitest'
import type { Run } from '~/api/queries'
import { bytesUnit, chartModel, durationUnit, historyPoints, linearScale, nearestIndex, niceTicks, timeTicks, valueAxis, zonedMidnight } from './chart'

const GIB = 1024 ** 3
const DAY = 86_400_000

function run(i: number, over: Partial<Run> = {}): Run {
  const start = Date.UTC(2026, 9, 1, 19, 0) + i * DAY
  return {
    id: `run_${i}`,
    job: 'orders-db',
    op: 'backup',
    state: 'succeeded',
    trigger: 'schedule',
    started_at: new Date(start).toISOString(),
    finished_at: new Date(start + 200_000).toISOString(),
    duration_ms: 200_000 + i * 1000,
    stored_bytes: 2 * GIB + i * 0.01 * GIB,
    snapshot: `snap-${i}`,
    ...over,
  }
}

describe('scales', () => {
  it('maps the domain onto the range and back', () => {
    const s = linearScale([0, 100], [40, 440])
    expect(s(0)).toBe(40)
    expect(s(50)).toBe(240)
    expect(s(100)).toBe(440)
    expect(s.invert(240)).toBe(50)
    const y = linearScale([0, 10], [170, 20])
    expect(y(0)).toBe(170)
    expect(y(10)).toBe(20)
  })

  it('makes round ticks from zero', () => {
    expect(niceTicks(4.6)).toEqual({ ticks: [0, 2, 4, 6], max: 6 })
    expect(niceTicks(3.1)).toEqual({ ticks: [0, 1, 2, 3, 4], max: 4 })
    expect(niceTicks(780)).toEqual({ ticks: [0, 200, 400, 600, 800], max: 800 })
    expect(niceTicks(0)).toEqual({ ticks: [0, 1], max: 1 })
  })

  it('picks units for durations and sizes', () => {
    expect(durationUnit(50_000).label).toBe('s')
    expect(durationUnit(5 * 60_000).label).toBe('min')
    expect(durationUnit(3 * 3_600_000).label).toBe('h')
    expect(bytesUnit(800 * 1024 ** 2).label).toBe('MiB')
    expect(bytesUnit(3.1 * GIB).label).toBe('GiB')
    const axis = valueAxis(4.6 * 60_000, 'duration')
    expect(axis.unit.label).toBe('min')
    expect(axis.ticks).toEqual([0, 120_000, 240_000, 360_000])
    expect(axis.max).toBe(360_000)
  })

  it('puts time ticks on local midnights of the deployment time zone', () => {
    const tz = 'Asia/Ho_Chi_Minh'
    expect(new Date(zonedMidnight(2026, 11, 4, tz)).toISOString()).toBe('2026-11-03T17:00:00.000Z')
    const t0 = Date.UTC(2026, 9, 1, 19)
    const ticks = timeTicks(t0, t0 + 10 * DAY, tz, 6)
    expect(ticks[0]).toEqual({ t: Date.UTC(2026, 9, 3, 17), label: 'Oct 4' })
    expect(ticks.map((t) => t.label)).toEqual(['Oct 4', 'Oct 6', 'Oct 8', 'Oct 10', 'Oct 12'])
    const months = timeTicks(Date.UTC(2026, 7, 5), Date.UTC(2026, 10, 4), 'UTC', 6)
    expect(months.map((t) => t.label)).toEqual(['Sep', 'Oct', 'Nov'])
  })
})

describe('history model', () => {
  const runs = [run(2), run(0), run(1, { state: 'failed', duration_ms: 4000, stored_bytes: undefined, snapshot: undefined, error: 'timeout' }), run(3, { state: 'running' })]

  it('keeps finished runs, oldest first', () => {
    const pts = historyPoints(runs)
    expect(pts.map((p) => p.id)).toEqual(['run_0', 'run_1', 'run_2'])
    expect(pts[1]).toMatchObject({ failed: true, duration: null, stored: null, error: 'timeout' })
  })

  it('plots values on a zero-based axis and marks failures on the time axis', () => {
    const box = { width: 500, height: 200, top: 20, right: 60, bottom: 30, left: 40 }
    const m = chartModel(historyPoints(runs), 'duration', box, 'UTC')
    expect(m.x.domain).toEqual([Date.parse(run(0).started_at!), Date.parse(run(2).started_at!)])
    expect(m.x.range).toEqual([40, 440])
    expect(m.y.domain).toEqual([0, m.axis.max])
    expect(m.y.range).toEqual([170, 20])
    expect(m.axis.unit.label).toBe('min')
    expect(m.values.map((v) => v.p.id)).toEqual(['run_0', 'run_2'])
    expect(m.values[0]!.x).toBe(40)
    expect(m.values[1]!.x).toBe(440)
    expect(m.failures).toHaveLength(1)
    expect(m.failures[0]!.x).toBe(240)
    const stored = chartModel(historyPoints(runs), 'stored', box, 'UTC')
    expect(stored.axis.unit.label).toBe('GiB')
    expect(stored.values[1]!.y).toBeLessThan(stored.values[0]!.y)
  })

  it('finds the nearest point to the pointer', () => {
    expect(nearestIndex([40, 240, 440], 300)).toBe(1)
    expect(nearestIndex([40, 240, 440], 1000)).toBe(2)
    expect(nearestIndex([], 10)).toBe(-1)
  })
})
