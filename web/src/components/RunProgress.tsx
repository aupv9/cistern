import type { Run } from '~/api/queries'
import { formatBytes, formatDuration } from '~/lib/format'

export function progressInfo(run: Run, now: Date = new Date()): { pct: number | null; text: string } {
  const p = run.progress
  let pct: number | null = null
  if (p?.bytes_total && p.bytes_total > 0 && p.bytes_done != null) pct = Math.min(1, p.bytes_done / p.bytes_total)
  else if (p?.artifacts_total && p.artifacts_total > 0 && p.artifacts_done != null) pct = Math.min(1, p.artifacts_done / p.artifacts_total)
  if (run.state === 'succeeded') pct = 1
  const verb = run.op === 'verify' ? 'checked' : run.op === 'restore' ? 'restored' : 'written'
  const bytesVerb = run.op === 'backup' ? 'written' : 'read'
  const parts: string[] = []
  if (p?.artifacts_total != null) parts.push(`${p.artifacts_done ?? 0} of ${p.artifacts_total} artifacts ${verb}`)
  if (p?.bytes_total != null) parts.push(`${formatBytes(p.bytes_done ?? 0)} of ${formatBytes(p.bytes_total)} ${bytesVerb}`)
  if (run.state === 'running' && pct != null && pct > 0.03 && pct < 1 && run.started_at) {
    const elapsed = now.getTime() - new Date(run.started_at).getTime()
    const left = (elapsed * (1 - pct)) / pct
    if (Number.isFinite(left) && left > 0) parts.push(`about ${formatDuration(Math.max(1000, left))} left`)
  }
  if (run.state === 'queued') parts.push('Waiting for the daemon to start it')
  return { pct, text: parts.join(' · ') }
}

export function RunProgress({ run, now, label }: { run: Run; now: Date; label: string }) {
  const { pct, text } = progressInfo(run, now)
  const value = pct == null ? undefined : Math.round(pct * 100)
  return (
    <>
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={value}
        aria-valuetext={value == null ? 'In progress' : `${value}%`}
        className={value == null ? 'progress indeterminate' : 'progress'}
      >
        <div style={{ width: value == null ? undefined : `${value}%` }} />
      </div>
      {text ? <span className="sub num">{text}</span> : null}
    </>
  )
}
