import type { Run } from '~/api/queries'
import { formatDay, formatDuration, formatBytes, MONTHS, zonedParts } from './format'

export type Scale = ((v: number) => number) & { domain: [number, number]; range: [number, number]; invert: (px: number) => number }

export function linearScale(domain: [number, number], range: [number, number]): Scale {
  const [d0, d1] = domain
  const [r0, r1] = range
  const span = d1 - d0 || 1
  const f = ((v: number) => r0 + ((v - d0) / span) * (r1 - r0)) as Scale
  f.domain = domain
  f.range = range
  f.invert = (px: number) => d0 + ((px - r0) / (r1 - r0 || 1)) * span
  return f
}

export function niceStep(span: number, count: number): number {
  if (!(span > 0) || count < 1) return 1
  const raw = span / count
  const mag = 10 ** Math.floor(Math.log10(raw))
  const norm = raw / mag
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 2.5 ? 2.5 : norm <= 5 ? 5 : 10
  return step * mag
}

export function niceTicks(max: number, count = 4): { ticks: number[]; max: number } {
  if (!(max > 0)) return { ticks: [0, 1], max: 1 }
  const step = niceStep(max, count)
  const top = Math.ceil(max / step - 1e-9) * step
  const ticks: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(Number(v.toPrecision(12)))
  return { ticks, max: top }
}

export type Unit = { label: string; size: number }

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE

export function durationUnit(maxMs: number): Unit {
  if (maxMs < 2 * MINUTE) return { label: 's', size: SECOND }
  if (maxMs < 2 * HOUR) return { label: 'min', size: MINUTE }
  return { label: 'h', size: HOUR }
}

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

export function bytesUnit(maxBytes: number): Unit {
  let i = 0
  while (i < BYTE_UNITS.length - 1 && maxBytes >= 1024 ** (i + 1)) i += 1
  return { label: BYTE_UNITS[i]!, size: 1024 ** i }
}

export function formatTick(v: number): string {
  if (Number.isInteger(v)) return new Intl.NumberFormat('en-US').format(v)
  return String(Number(v.toFixed(2)))
}

export type ValueAxis = { unit: Unit; ticks: number[]; max: number }

export function valueAxis(maxValue: number, kind: 'duration' | 'bytes', count = 4): ValueAxis {
  const unit = kind === 'duration' ? durationUnit(maxValue) : bytesUnit(maxValue)
  const { ticks, max } = niceTicks(maxValue / unit.size, count)
  return { unit, ticks: ticks.map((t) => t * unit.size), max: max * unit.size }
}

function offsetMs(ms: number, tz: string): number {
  const p = zonedParts(new Date(ms), tz)
  const wall = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute)
  return wall - Math.floor(ms / MINUTE) * MINUTE
}

export function zonedMidnight(year: number, month: number, day: number, tz: string): number {
  const guess = Date.UTC(year, month - 1, day)
  const first = guess - offsetMs(guess, tz)
  return guess - offsetMs(first, tz)
}

export type TimeTick = { t: number; label: string }

const DAY_STEPS = [1, 2, 7, 14]
const MONTH_STEPS = [1, 2, 3, 6, 12]

export function timeTicks(t0: number, t1: number, tz: string, maxTicks = 6): TimeTick[] {
  if (!(t1 > t0)) return []
  const days = (t1 - t0) / 86_400_000
  const start = zonedParts(new Date(t0), tz)
  const out: TimeTick[] = []
  const dayStep = DAY_STEPS.find((s) => days / s <= maxTicks)
  if (dayStep) {
    let y = start.year
    let m = start.month
    let d = start.day
    for (let i = 0; i < 400; i += 1) {
      const t = zonedMidnight(y, m, d, tz)
      if (t > t1) break
      if (t >= t0) out.push({ t, label: `${MONTHS[m - 1]} ${d}` })
      const next = new Date(Date.UTC(y, m - 1, d + dayStep))
      y = next.getUTCFullYear()
      m = next.getUTCMonth() + 1
      d = next.getUTCDate()
    }
    return out
  }
  const months = days / 30.4
  const monthStep = MONTH_STEPS.find((s) => months / s <= maxTicks) ?? 12
  let y = start.year
  let m = start.month + 1
  if (m > 12) {
    m = 1
    y += 1
  }
  for (let i = 0; i < 200; i += 1) {
    const t = zonedMidnight(y, m, 1, tz)
    if (t > t1) break
    if (t >= t0 && (m - 1) % monthStep === 0) out.push({ t, label: m === 1 ? `${MONTHS[0]} ${y}` : MONTHS[m - 1]! })
    m += 1
    if (m > 12) {
      m = 1
      y += 1
    }
  }
  return out
}

export type HistoryPoint = {
  id: string
  t: number
  failed: boolean
  duration: number | null
  stored: number | null
  snapshot?: string
  error?: string
}

export function historyPoints(runs: readonly Run[]): HistoryPoint[] {
  return runs
    .filter((r) => r.state === 'succeeded' || r.state === 'failed')
    .map((r) => ({
      id: r.id,
      t: new Date(r.started_at ?? r.queued_at ?? r.finished_at ?? 0).getTime(),
      failed: r.state === 'failed',
      duration: r.state === 'succeeded' && r.duration_ms != null ? r.duration_ms : null,
      stored: r.state === 'succeeded' && r.stored_bytes != null ? r.stored_bytes : null,
      snapshot: r.snapshot,
      error: r.error,
    }))
    .filter((p) => Number.isFinite(p.t) && p.t > 0)
    .sort((a, b) => a.t - b.t)
}

export type ChartBox = { width: number; height: number; top: number; right: number; bottom: number; left: number }

export type ChartModel = {
  x: Scale
  y: Scale
  axis: ValueAxis
  xTicks: TimeTick[]
  values: { p: HistoryPoint; x: number; y: number; v: number }[]
  failures: { p: HistoryPoint; x: number }[]
}

export function chartModel(points: readonly HistoryPoint[], metric: 'duration' | 'stored', box: ChartBox, tz: string): ChartModel {
  const ts = points.map((p) => p.t)
  let t0 = ts.length ? Math.min(...ts) : 0
  let t1 = ts.length ? Math.max(...ts) : 1
  if (t1 - t0 < 86_400_000) {
    const mid = (t0 + t1) / 2
    t0 = mid - 43_200_000
    t1 = mid + 43_200_000
  }
  const vals = points.map((p) => p[metric]).filter((v): v is number => v != null)
  const axis = valueAxis(vals.length ? Math.max(...vals) : 0, metric === 'duration' ? 'duration' : 'bytes')
  const x = linearScale([t0, t1], [box.left, box.width - box.right])
  const y = linearScale([0, axis.max], [box.height - box.bottom, box.top])
  const plotWidth = box.width - box.left - box.right
  const xTicks = timeTicks(t0, t1, tz, Math.max(2, Math.min(7, Math.floor(plotWidth / 80))))
  return {
    x,
    y,
    axis,
    xTicks,
    values: points.flatMap((p) => {
      const v = p[metric]
      return v == null ? [] : [{ p, x: x(p.t), y: y(v), v }]
    }),
    failures: points.filter((p) => p.failed).map((p) => ({ p, x: x(p.t) })),
  }
}

export function linePath(values: readonly { x: number; y: number }[]): string {
  return values.map((v, i) => `${i === 0 ? 'M' : 'L'}${v.x.toFixed(1)},${v.y.toFixed(1)}`).join('')
}

export function nearestIndex(xs: readonly number[], px: number): number {
  if (xs.length === 0) return -1
  let best = 0
  let dist = Infinity
  xs.forEach((x, i) => {
    const d = Math.abs(x - px)
    if (d < dist) {
      dist = d
      best = i
    }
  })
  return best
}

export function formatMetric(metric: 'duration' | 'stored', v: number): string {
  return metric === 'duration' ? formatDuration(v) : formatBytes(v)
}

export function pointLabel(p: HistoryPoint, tz: string, now: Date = new Date()): string {
  const day = formatDay(new Date(p.t), tz, now)
  if (p.failed) return `${day}: failed${p.error ? `, ${p.error}` : ''}`
  return `${day}: ${formatDuration(p.duration)}, ${formatBytes(p.stored)}`
}
