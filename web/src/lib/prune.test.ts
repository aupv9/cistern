import { describe, expect, it } from 'vitest'
import type { PrunePreview } from '~/api/queries'
import { isLocked, lockedItems, lockedWarning, pruneHeadline, pruneReasonText } from './prune'

const now = new Date('2026-11-04T08:00:00Z')

const preview: PrunePreview = {
  job: 'billing-db',
  items: [
    { snapshot: 'a', action: 'keep', reasons: ['last', 'daily'], stored_bytes: 100, lock_until: '2026-11-11T00:00:00Z' },
    { snapshot: 'b', action: 'remove', reasons: [], stored_bytes: 200, lock_until: '2026-11-09T07:00:00Z' },
    { snapshot: 'c', action: 'remove', reasons: [], stored_bytes: 300, lock_until: '2026-11-06T07:00:00Z' },
    { snapshot: 'd', action: 'remove', reasons: [], stored_bytes: 400, lock_until: '2026-10-20T00:00:00Z' },
    { snapshot: 'e', action: 'remove-incomplete', reasons: ['incomplete'], stored_bytes: 10 },
    { snapshot: 'f', action: 'keep', reasons: ['depends_on'], stored_bytes: 1 },
  ],
  kept: 2,
  removed: 4,
  freed_bytes: 910,
  locked: 2,
  run_records_removed: 6,
}

describe('prune preview', () => {
  it('finds removals still under Object Lock', () => {
    expect(lockedItems(preview, now).map((i) => i.snapshot)).toEqual(['b', 'c'])
    expect(isLocked(preview.items[0]!, now)).toBe(false)
    expect(isLocked(preview.items[3]!, now)).toBe(false)
  })

  it('warns about locked snapshots with the last lock date', () => {
    expect(lockedWarning(preview, 'UTC', now)).toBe(
      '2 snapshots to remove are still under Object Lock, the last one until Nov 9. The storage refuses to delete them before then, so the prune reports an error for each and removes them on a later run.',
    )
    const one = { ...preview, items: preview.items.filter((i) => i.snapshot !== 'b'), locked: 1 }
    expect(lockedWarning(one, 'UTC', now)).toMatch(/^1 snapshot to remove is still under Object Lock, the last one until Nov 6\. .* reports an error and removes it/)
  })

  it('has no warning when nothing is locked', () => {
    expect(lockedWarning({ ...preview, items: [], locked: 0 }, 'UTC', now)).toBeNull()
  })

  it('summarizes totals and reasons', () => {
    expect(pruneHeadline(preview)).toBe('A prune would remove 4 snapshots and keep 2.')
    expect(pruneHeadline({ ...preview, removed: 0, kept: 9 })).toBe('Nothing to remove. All 9 snapshots are kept by a retention rule.')
    expect(pruneReasonText(preview.items[0]!)).toBe('last, daily')
    expect(pruneReasonText(preview.items[5]!)).toBe('needed by a newer snapshot')
    expect(pruneReasonText(preview.items[1]!)).toBe('No retention rule keeps it')
  })
})
