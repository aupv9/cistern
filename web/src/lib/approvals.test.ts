import { describe, expect, it } from 'vitest'
import type { Approval, Me } from '~/api/queries'
import { decideBlock, effectiveState, rejectNoteError } from './approvals'

const now = new Date('2026-11-03T08:00:00Z')

const me: Me = {
  user: { id: 'u-minh', email: 'minh@example.com', name: 'Minh Le' },
  roles: ['viewer', 'approver'],
  csrf_token: 't',
  session_expires_at: '2026-11-03T16:00:00Z',
  deployment: { name: 'production', version: '0.3.0', timezone: 'UTC', daemon: { configured: true, reachable: true } },
}

const approval: Approval = {
  id: 'REQ-1',
  state: 'pending',
  job: 'orders-db',
  snapshot: '20261103T020004Z-a91c',
  target: { name: 'production', production: true },
  restore_suffix: '_restored',
  reason: 'INC-4821',
  requested_by: { id: 'u-linh', name: 'Linh Tran' },
  requested_at: '2026-11-03T07:48:00Z',
  expires_at: '2026-11-04T07:48:00Z',
  can_decide: true,
}

describe('approval decide rules', () => {
  it('allows an approver to decide someone else’s pending request', () => {
    expect(decideBlock(approval, me, now)).toBeNull()
  })

  it('blocks your own request', () => {
    expect(decideBlock({ ...approval, requested_by: { id: 'u-minh', name: 'Minh Le' }, can_decide: false }, me, now)).toMatch(/You asked/)
  })

  it('blocks people without the approver role', () => {
    expect(decideBlock({ ...approval, can_decide: false }, { ...me, roles: ['viewer', 'restorer'] }, now)).toMatch(/Approver role/)
  })

  it('trusts can_decide from the server', () => {
    expect(decideBlock({ ...approval, can_decide: false }, me, now)).toMatch(/cannot decide/)
  })

  it('treats a pending request past its expiry as expired', () => {
    const late = new Date('2026-11-05T00:00:00Z')
    expect(effectiveState(approval, late)).toBe('expired')
    expect(decideBlock(approval, me, late)).toMatch(/expired/)
    expect(decideBlock({ ...approval, state: 'approved' }, me, now)).toMatch(/already been decided/)
  })

  it('requires a note to reject', () => {
    expect(rejectNoteError('')).toMatch(/why/)
    expect(rejectNoteError('  no ')).toMatch(/why/)
    expect(rejectNoteError('Use staging instead')).toBeNull()
  })
})
