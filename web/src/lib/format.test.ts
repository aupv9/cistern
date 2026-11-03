import { describe, expect, it } from 'vitest'
import { describeCron, formatBytes, formatDuration, formatRelative, formatWhen, formatWhenLower, median, shortHash, timeZoneLabel } from './format'

describe('formatBytes', () => {
  it('uses binary units with one decimal below 10', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(18 * 1024)).toBe('18 KiB')
    expect(formatBytes(820 * 1024 ** 2)).toBe('820 MiB')
    expect(formatBytes(3.1 * 1024 ** 3)).toBe('3.1 GiB')
    expect(formatBytes(214 * 1024 ** 3)).toBe('214 GiB')
    expect(formatBytes(1.2 * 1024 ** 4)).toBe('1.2 TiB')
  })

  it('never shows 1024 of a unit', () => {
    expect(formatBytes(1024 ** 2 - 1)).toBe('1.0 MiB')
  })

  it('handles missing and invalid values', () => {
    expect(formatBytes(undefined)).toBe('–')
    expect(formatBytes(-1)).toBe('–')
    expect(formatBytes(Number.NaN)).toBe('–')
  })
})

describe('formatDuration', () => {
  it('formats minutes and padded seconds', () => {
    expect(formatDuration(4 * 60_000 + 12_000)).toBe('4m 12s')
    expect(formatDuration(4 * 60_000 + 5_000)).toBe('4m 05s')
  })

  it('formats short and long durations', () => {
    expect(formatDuration(450)).toBe('450 ms')
    expect(formatDuration(42_000)).toBe('42s')
    expect(formatDuration(3_600_000 + 2 * 60_000)).toBe('1h 02m')
  })
})

describe('formatWhen', () => {
  const tz = 'Asia/Ho_Chi_Minh'
  const now = new Date('2026-11-03T07:00:00Z')

  it('shows Today and Tomorrow in the deployment time zone', () => {
    expect(formatWhen('2026-11-02T19:04:00Z', tz, now)).toBe('Today 02:04')
    expect(formatWhen('2026-11-03T19:00:00Z', tz, now)).toBe('Tomorrow 02:00')
  })

  it('shows month and day for other dates, and the year when it differs', () => {
    expect(formatWhen('2026-10-31T19:31:00Z', tz, now)).toBe('Nov 1, 02:31')
    expect(formatWhen('2025-12-31T10:00:00Z', tz, now)).toBe('Dec 31, 2025, 17:00')
  })

  it('respects the zone when the UTC day differs', () => {
    expect(formatWhen('2026-11-02T19:04:00Z', 'UTC', now)).toBe('Nov 2, 19:04')
  })

  it('has a lowercase form for sentences', () => {
    expect(formatWhenLower('2026-11-02T19:04:00Z', tz, now)).toBe('today at 02:04')
    expect(formatWhenLower('2026-10-31T19:31:00Z', tz, now)).toBe('on Nov 1, 02:31')
  })
})

describe('formatRelative', () => {
  const now = new Date('2026-11-03T07:00:00Z')
  it('describes past and future times', () => {
    expect(formatRelative('2026-11-03T06:48:00Z', now)).toBe('12 min ago')
    expect(formatRelative('2026-11-04T06:00:00Z', now)).toBe('in 23 h')
    expect(formatRelative('2026-11-03T06:59:50Z', now)).toBe('just now')
    expect(formatRelative('2026-10-30T07:00:00Z', now)).toBe('4 days ago')
  })
})

describe('helpers', () => {
  it('labels time zones as UTC offsets', () => {
    expect(timeZoneLabel('Asia/Ho_Chi_Minh')).toBe('UTC+7')
    expect(timeZoneLabel('UTC')).toBe('UTC')
  })

  it('describes simple cron expressions', () => {
    expect(describeCron('0 2 * * *')).toBe('daily at 02:00')
    expect(describeCron('0 2,14 * * *')).toBe('daily at 02:00 and 14:00')
    expect(describeCron('30 3 * * 0')).toBe('weekly on Sunday at 03:30')
    expect(describeCron('*/5 * * * *')).toBe('*/5 * * * *')
  })

  it('computes medians and shortens hashes', () => {
    expect(median([3, 1, 2])).toBe(2)
    expect(median([1, 2, 3, 4])).toBe(2.5)
    expect(median([])).toBeNull()
    expect(shortHash('a'.repeat(64))).toBe(`${'a'.repeat(16)}…${'a'.repeat(8)}`)
  })
})
