import type { RunPoint } from '~/api/queries'
import { formatDuration, formatWhen } from '~/lib/format'
import { RUN_STATE } from '~/lib/status'

export function stripSummary(points: readonly RunPoint[]) {
  const count = (s: RunPoint['status']) => points.filter((p) => p.status === s).length
  return {
    succeeded: count('succeeded'),
    failed: count('failed'),
    running: count('running') + count('queued'),
    unknown: count('unknown'),
    total: points.length,
  }
}

export function stripLabel(points: readonly RunPoint[], noun = 'runs', withDurations = false): string {
  if (points.length === 0) return `No ${noun} yet`
  const s = stripSummary(points)
  const head = `Last ${s.total} ${noun}`
  let body: string
  if (s.succeeded === s.total) body = s.total === 1 ? '1 succeeded' : `all ${s.total} succeeded`
  else {
    const parts = [`${s.succeeded} succeeded`]
    if (s.failed) parts.push(`${s.failed} failed`)
    if (s.running) parts.push(`${s.running} running`)
    if (s.unknown) parts.push(`${s.unknown} unknown`)
    body = parts.join(', ')
  }
  let tail = ''
  if (withDurations) {
    const ds = points.filter((p) => p.status === 'succeeded' && p.duration_ms != null).map((p) => p.duration_ms as number)
    if (ds.length > 1) tail = `, from ${formatDuration(Math.min(...ds))} to ${formatDuration(Math.max(...ds))}`
  }
  return `${head}: ${body}${tail}`
}

const CLASS: Record<RunPoint['status'], string> = {
  succeeded: 'b-ok',
  failed: 'b-fail',
  running: 'b-run',
  queued: 'b-run',
  unknown: 'b-unknown',
}

export function RunStrip({
  points,
  size = 'sm',
  noun = 'runs',
  timeZone,
}: {
  points: readonly RunPoint[]
  size?: 'sm' | 'lg'
  noun?: string
  timeZone?: string
}) {
  if (points.length === 0) return <span className="strip-empty">No {noun} yet</span>
  const lg = size === 'lg'
  const durations = points.filter((p) => p.status === 'succeeded' && p.duration_ms != null).map((p) => p.duration_ms as number)
  const min = durations.length ? Math.min(...durations) : 0
  const max = durations.length ? Math.max(...durations) : 0
  const heightFor = (p: RunPoint) => {
    if (!lg || p.status !== 'succeeded' || p.duration_ms == null || max === min) return undefined
    return Math.round(16 + ((p.duration_ms - min) / (max - min)) * 10)
  }
  return (
    <span role="img" aria-label={stripLabel(points, noun, lg)} className={lg ? 'strip lg' : 'strip'} data-testid="run-strip">
      {points.map((p, i) => {
        const h = heightFor(p)
        const title = [RUN_STATE[p.status].text, timeZone ? formatWhen(p.started_at, timeZone) : null, p.duration_ms != null ? formatDuration(p.duration_ms) : null]
          .filter(Boolean)
          .join(' · ')
        return <span key={`${p.started_at}-${i}`} className={CLASS[p.status]} data-status={p.status} title={title} style={h ? { height: h } : undefined} />
      })}
    </span>
  )
}
