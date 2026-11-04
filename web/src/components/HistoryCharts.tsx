import { useId, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { historyQuery } from '~/api/queries'
import { useElementWidth } from '~/hooks/useElementWidth'
import { chartModel, formatMetric, formatTick, historyPoints, linePath, nearestIndex, type HistoryPoint } from '~/lib/chart'
import { formatBytes, formatDay, formatDuration, formatWhen, median, plural, timeZoneLabel } from '~/lib/format'
import { BlockSkeleton } from './Skeleton'
import { EmptyState, ErrorState } from './States'

type Metric = 'duration' | 'stored'

const BOX = { height: 196, top: 22, right: 64, bottom: 28, left: 44 }

export function HistorySection({ job, tz, now }: { job: string; tz: string; now: Date }) {
  const q = useQuery(historyQuery(job))
  const [view, setView] = useState<'chart' | 'table'>('chart')
  const [active, setActive] = useState<number | null>(null)
  const points = q.data ? historyPoints(q.data) : []
  const failed = points.filter((p) => p.failed).length
  const first = points[0]
  const last = points.at(-1)

  return (
    <section aria-labelledby="hist-h" className="card">
      <div className="hd">
        <div className="row" style={{ gap: '4px 12px' }}>
          <h2 id="hist-h" className="h2">
            History
          </h2>
          {points.length ? (
            <span className="sub num">
              {plural(points.length, 'backup')}
              {first && last ? `, ${formatDay(new Date(first.t), tz, now)} to ${formatDay(new Date(last.t), tz, now)}` : ''}
              {failed ? `, ${failed} failed` : ''} · times in {timeZoneLabel(tz)}
            </span>
          ) : null}
        </div>
        {points.length ? (
          <div role="group" aria-label="Show history as" className="seg">
            <button type="button" aria-pressed={view === 'chart'} onClick={() => setView('chart')}>
              Charts
            </button>
            <button type="button" aria-pressed={view === 'table'} onClick={() => setView('table')}>
              Table
            </button>
          </div>
        ) : null}
      </div>
      {q.isPending ? (
        <BlockSkeleton lines={5} label="Loading history" />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} title="Could not load the history" />
      ) : points.length === 0 ? (
        <EmptyState title="No finished backups yet">
          <p className="muted">Duration and size charts appear after the first backup of {job} finishes.</p>
        </EmptyState>
      ) : view === 'table' ? (
        <HistoryTable points={points} tz={tz} now={now} job={job} />
      ) : (
        <>
          <div className="charts" onPointerLeave={() => setActive(null)}>
            <TimeChart metric="duration" title="Backup duration" points={points} tz={tz} now={now} active={active} onActive={setActive} />
            <TimeChart metric="stored" title="Stored size" points={points} tz={tz} now={now} active={active} onActive={setActive} />
          </div>
          <HistoryTable points={points} tz={tz} now={now} job={job} hidden />
        </>
      )}
    </section>
  )
}

function HistoryTable({ points, tz, now, job, hidden }: { points: HistoryPoint[]; tz: string; now: Date; job: string; hidden?: boolean }) {
  const rows = [...points].reverse()
  const table = (
    <table className="t compact">
      <caption className="sr">
        Backups of {job}, newest first, with duration and stored size. Times in {timeZoneLabel(tz)}.
      </caption>
      <thead>
        <tr>
          <th scope="col">Started</th>
          <th scope="col">Result</th>
          <th scope="col" className="right">
            Duration
          </th>
          <th scope="col" className="right">
            Stored
          </th>
          <th scope="col">Snapshot</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((p) => (
          <tr key={p.id}>
            <td>{formatWhen(new Date(p.t), tz, now)}</td>
            <td>{p.failed ? <span className="st fail">Failed</span> : <span className="st ok">Succeeded</span>}</td>
            <td className="right">{p.failed ? '–' : formatDuration(p.duration)}</td>
            <td className="right">{p.failed ? '–' : formatBytes(p.stored)}</td>
            <td>
              {hidden ? (
                (p.snapshot ?? p.error ?? '–')
              ) : p.failed ? (
                <span className="muted wrap">{p.error ?? '–'}</span>
              ) : (
                <Link to="/runs/$run" params={{ run: p.id }} className="mono">
                  {p.snapshot ?? p.id}
                </Link>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
  if (hidden) return <div className="sr">{table}</div>
  return <div className="scroll-x hist-table">{table}</div>
}

function summaryFor(metric: Metric, points: HistoryPoint[]): string {
  const vals = points.map((p) => p[metric]).filter((v): v is number => v != null)
  if (vals.length === 0) return 'No successful backups'
  const lastV = vals.at(-1)!
  if (metric === 'duration') {
    const med = median(vals)
    return `Latest ${formatDuration(lastV)}, median ${formatDuration(med)}`
  }
  const firstV = vals[0]!
  const change = firstV > 0 ? ((lastV - firstV) / firstV) * 100 : 0
  const dir = Math.abs(change) < 1 ? 'about the same as' : change > 0 ? `up ${Math.round(change)}% from` : `down ${Math.round(-change)}% from`
  return `Latest ${formatBytes(lastV)}, ${dir} ${formatBytes(firstV)}`
}

function TimeChart({
  metric,
  title,
  points,
  tz,
  now,
  active,
  onActive,
}: {
  metric: Metric
  title: string
  points: HistoryPoint[]
  tz: string
  now: Date
  active: number | null
  onActive: (i: number | null) => void
}) {
  const [ref, width] = useElementWidth<HTMLDivElement>()
  const [focused, setFocused] = useState(false)
  const titleId = useId()
  const descId = useId()
  const box = { ...BOX, width: Math.max(240, width) }
  const m = chartModel(points, metric, box, tz)
  const xs = points.map((p) => m.x(p.t))
  const baseline = m.y(0)
  const lastValue = m.values.at(-1)
  const summary = summaryFor(metric, points)
  const failures = m.failures.length
  const cur = active != null ? points[active] : undefined
  const curX = active != null ? xs[active] : undefined
  const curV = cur ? cur[metric] : null

  const onMove = (e: PointerEvent<SVGRectElement>) => {
    const rect = e.currentTarget.getBoundingClientRect()
    const scale = rect.width > 0 ? (box.width - box.left - box.right) / rect.width : 1
    onActive(nearestIndex(xs, box.left + (e.clientX - rect.left) * scale))
  }

  const onKey = (e: KeyboardEvent<SVGSVGElement>) => {
    const lastI = points.length - 1
    const at = active ?? lastI
    let next: number | null
    if (e.key === 'ArrowLeft') next = Math.max(0, at - 1)
    else if (e.key === 'ArrowRight') next = Math.min(lastI, at + 1)
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = lastI
    else if (e.key === 'PageUp') next = Math.max(0, at - 10)
    else if (e.key === 'PageDown') next = Math.min(lastI, at + 10)
    else if (e.key === 'Escape') next = null
    else return
    e.preventDefault()
    onActive(next)
  }

  const tipStyle = curX == null ? undefined : curX > box.width / 2 ? { right: box.width - curX + 10 } : { left: curX + 10 }

  return (
    <figure className="chart" aria-labelledby={titleId}>
      <figcaption className="chart-head">
        <span id={titleId} className="chart-title">
          {title}
        </span>
        <span className="sub num" id={descId}>
          {summary}
        </span>
        {failures ? (
          <span className="chart-key sub">
            <span className="key-fail" aria-hidden="true" />
            {plural(failures, 'failed backup')}
          </span>
        ) : null}
      </figcaption>
      <div ref={ref} className="chart-plot">
        <svg
          width={box.width}
          height={box.height}
          viewBox={`0 0 ${box.width} ${box.height}`}
          role="group"
          tabIndex={0}
          aria-labelledby={titleId}
          aria-describedby={descId}
          aria-roledescription="chart"
          onKeyDown={onKey}
          onFocus={() => {
            setFocused(true)
            if (active == null) onActive(points.length - 1)
          }}
          onBlur={() => {
            setFocused(false)
            onActive(null)
          }}
        >
          <g className="grid" aria-hidden="true">
            {m.axis.ticks.map((t) => (
              <g key={t}>
                <line x1={box.left} x2={box.width - box.right} y1={m.y(t)} y2={m.y(t)} className={t === 0 ? 'base' : undefined} />
                <text x={box.left - 8} y={m.y(t)} dy="0.32em" textAnchor="end">
                  {formatTick(t / m.axis.unit.size)}
                </text>
              </g>
            ))}
            <text x={box.left - 8} y={box.top - 10} textAnchor="end" className="unit">
              {m.axis.unit.label}
            </text>
            {m.xTicks.map((t) => (
              <g key={t.t}>
                <line x1={m.x(t.t)} x2={m.x(t.t)} y1={baseline} y2={baseline + 4} />
                <text x={m.x(t.t)} y={baseline + 18} textAnchor="middle">
                  {t.label}
                </text>
              </g>
            ))}
          </g>
          {m.values.length > 1 ? (
            <path
              className="area"
              aria-hidden="true"
              d={`${linePath(m.values)}L${m.values.at(-1)!.x.toFixed(1)},${baseline}L${m.values[0]!.x.toFixed(1)},${baseline}Z`}
            />
          ) : null}
          <path className="line" aria-hidden="true" d={linePath(m.values)} />
          {m.failures.map((f) => (
            <circle key={f.p.id} className="fail-mark" aria-hidden="true" cx={f.x} cy={baseline} r={4} data-testid="fail-mark" />
          ))}
          {lastValue ? (
            <g aria-hidden="true">
              <circle className="dot" cx={lastValue.x} cy={lastValue.y} r={4} />
              <text x={lastValue.x + 8} y={lastValue.y} dy="0.32em" className="last">
                {formatMetric(metric, lastValue.v)}
              </text>
            </g>
          ) : null}
          {curX != null ? (
            <g aria-hidden="true" className="cursor">
              <line x1={curX} x2={curX} y1={box.top} y2={baseline} />
              {curV != null ? <circle className="dot" cx={curX} cy={m.y(curV)} r={4} /> : null}
              {cur?.failed ? <circle className="fail-mark on" cx={curX} cy={baseline} r={5} /> : null}
            </g>
          ) : null}
          <rect
            className="hit"
            x={box.left}
            y={0}
            width={Math.max(0, box.width - box.left - box.right)}
            height={box.height}
            onPointerMove={onMove}
            onPointerDown={onMove}
          />
        </svg>
        {cur && curX != null ? (
          <div className="chart-tip" style={tipStyle} aria-hidden="true">
            <strong className="num">{cur.failed ? 'Failed' : curV != null ? formatMetric(metric, curV) : '–'}</strong>
            <span className="sub num">{formatWhen(new Date(cur.t), tz, now)}</span>
            {cur.failed && cur.error ? <span className="sub tip-err">{cur.error}</span> : null}
            {!cur.failed && cur.snapshot ? <span className="sub mono">{cur.snapshot}</span> : null}
          </div>
        ) : null}
        <span className="sr" aria-live="polite">
          {focused && cur ? `${formatWhen(new Date(cur.t), tz, now)}: ${cur.failed ? `failed${cur.error ? `, ${cur.error}` : ''}` : curV != null ? formatMetric(metric, curV) : 'no value'}` : ''}
        </span>
      </div>
    </figure>
  )
}
