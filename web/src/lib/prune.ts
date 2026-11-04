import type { PruneItem, PrunePreview } from '~/api/queries'
import { formatDay, plural } from './format'
import { keptByText } from './job'
import type { StatusInfo } from './status'

export const PRUNE_ACTION: Record<PruneItem['action'], StatusInfo> = {
  keep: { text: 'Keep', tone: 'ok' },
  remove: { text: 'Remove', tone: 'warn' },
  'remove-incomplete': { text: 'Remove, incomplete', tone: 'warn' },
}

export function pruneReasonText(item: PruneItem): string {
  if (item.reasons.length === 0) return item.action === 'keep' ? 'Kept' : 'No retention rule keeps it'
  return keptByText(item.reasons)
}

export function isLocked(item: PruneItem, now: Date = new Date()): boolean {
  return item.action !== 'keep' && !!item.lock_until && new Date(item.lock_until).getTime() > now.getTime()
}

export function lockedItems(preview: PrunePreview, now: Date = new Date()): PruneItem[] {
  return preview.items.filter((i) => isLocked(i, now))
}

export function lockedWarning(preview: PrunePreview, tz: string, now: Date = new Date()): string | null {
  const locked = lockedItems(preview, now)
  const count = Math.max(preview.locked, locked.length)
  if (count === 0) return null
  const until = locked
    .map((i) => i.lock_until!)
    .sort()
    .at(-1)
  const when = until ? `, the last one until ${formatDay(until, tz, now)}` : ''
  const subject = count === 1 ? '1 snapshot to remove is' : `${plural(count, 'snapshot')} to remove are`
  return `${subject} still under Object Lock${when}. The storage refuses to delete ${count === 1 ? 'it' : 'them'} before then, so the prune reports ${count === 1 ? 'an error' : 'an error for each'} and removes ${count === 1 ? 'it' : 'them'} on a later run.`
}

export function pruneHeadline(preview: PrunePreview): string {
  if (preview.removed === 0) return `Nothing to remove. All ${plural(preview.kept, 'snapshot')} are kept by a retention rule.`
  return `A prune would remove ${plural(preview.removed, 'snapshot')} and keep ${preview.kept}.`
}
