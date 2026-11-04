import type { PrunePreview, PruneSummary } from '~/api/queries'
import { formatBytes, formatCount, plural } from '~/lib/format'

export function PruneTotals({ preview }: { preview: PrunePreview }) {
  return (
    <dl className="cells totals" aria-label="Prune totals">
      <div className="cell">
        <dt className="sub">Kept</dt>
        <dd>{plural(preview.kept, 'snapshot')}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Removed</dt>
        <dd>{plural(preview.removed, 'snapshot')}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Frees</dt>
        <dd>{formatBytes(preview.freed_bytes)}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Still locked</dt>
        <dd className={preview.locked ? 'warn-text' : undefined}>{plural(preview.locked, 'snapshot')}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Run records removed</dt>
        <dd>{formatCount(preview.run_records_removed)}</dd>
      </div>
    </dl>
  )
}

export function PruneSummaryView({ summary }: { summary: PruneSummary }) {
  return (
    <dl className="cells totals" aria-label="Prune counts">
      <div className="cell">
        <dt className="sub">Kept</dt>
        <dd>{plural(summary.kept, 'snapshot')}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Removed</dt>
        <dd>{plural(summary.removed, 'snapshot')}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Errors</dt>
        <dd className={summary.errors ? 'warn-text' : undefined}>{formatCount(summary.errors)}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Freed</dt>
        <dd>{formatBytes(summary.freed_bytes ?? 0)}</dd>
      </div>
      <div className="cell">
        <dt className="sub">Run records removed</dt>
        <dd>{formatCount(summary.run_records_removed)}</dd>
      </div>
    </dl>
  )
}
