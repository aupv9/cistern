import { describe, expect, it } from 'vitest'
import type { Me } from '~/api/queries'
import { blockReason, can, describeRoles, readOnlyReason } from './roles'

function me(roles: Me['roles'], daemon = { configured: true, reachable: true }): Me {
  return {
    user: { id: 'u1', email: 'a@example.com', name: 'A' },
    roles,
    csrf_token: 't',
    session_expires_at: '2026-11-03T10:00:00Z',
    deployment: { name: 'production', version: '0.3.0', timezone: 'UTC', daemon },
  }
}

describe('role gating', () => {
  it('maps capabilities to roles', () => {
    expect(can(['viewer'], 'backup')).toBe(false)
    expect(can(['viewer', 'operator'], 'backup')).toBe(true)
    expect(can(['viewer', 'operator'], 'restore')).toBe(false)
    expect(can(['viewer', 'operator', 'restorer'], 'restore')).toBe(true)
    expect(can(['viewer', 'operator', 'restorer', 'admin'], 'approve')).toBe(false)
    expect(can(['viewer', 'approver'], 'approve')).toBe(true)
  })

  it('describes roles the way the header shows them', () => {
    expect(describeRoles(['viewer', 'operator', 'restorer'])).toBe('Operator, Restorer')
    expect(describeRoles(['viewer'])).toBe('Viewer')
    expect(describeRoles(['viewer', 'operator', 'restorer', 'admin'])).toBe('Admin')
    expect(describeRoles(['viewer', 'approver'])).toBe('Viewer, Approver')
  })

  it('blocks actions when the daemon is missing or down', () => {
    expect(readOnlyReason(me(['viewer']).deployment)).toBeNull()
    expect(readOnlyReason(me(['viewer'], { configured: false, reachable: false }).deployment)).toMatch(/read-only/)
    expect(blockReason(me(['viewer', 'operator'], { configured: true, reachable: false }), 'backup')).toMatch(/not reachable/)
    expect(blockReason(me(['viewer']), 'backup')).toBe('Needs the Operator role.')
    expect(blockReason(me(['viewer', 'operator']), 'backup')).toBeNull()
  })
})
