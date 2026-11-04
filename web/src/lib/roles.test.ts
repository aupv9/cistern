import { describe, expect, it } from 'vitest'
import type { Me } from '~/api/queries'
import { blockReason, can, canRunPrune, describeRoles, pruneRunBlock, readOnlyReason } from './roles'

function me(roles: Me['roles'], daemon = { configured: true, reachable: true }, features = { prune: true, drills: true }): Me {
  return {
    user: { id: 'u1', email: 'a@example.com', name: 'A' },
    roles,
    csrf_token: 't',
    session_expires_at: '2026-11-03T10:00:00Z',
    deployment: { name: 'production', version: '0.3.0', timezone: 'UTC', daemon, features },
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

  it('gates drills, prune and the configuration view', () => {
    expect(can(['viewer', 'operator'], 'drill')).toBe(false)
    expect(can(['viewer', 'restorer'], 'drill')).toBe(true)
    expect(can(['viewer'], 'prune_preview')).toBe(false)
    expect(can(['viewer', 'operator'], 'prune_preview')).toBe(true)
    expect(can(['viewer', 'operator', 'restorer'], 'prune')).toBe(false)
    expect(can(['viewer', 'admin'], 'prune')).toBe(true)
    expect(can(['viewer'], 'config')).toBe(false)
    expect(can(['viewer', 'operator'], 'config')).toBe(true)
    expect(blockReason(me(['viewer', 'operator'], { configured: true, reachable: false }), 'prune_preview')).toBeNull()
    expect(blockReason(me(['viewer', 'restorer'], { configured: true, reachable: false }), 'drill')).toMatch(/not reachable/)
  })

  it('runs prune only for admins when the server enables it', () => {
    expect(pruneRunBlock(me(['viewer', 'operator']))).toBe('Running a prune needs the Admin role.')
    expect(canRunPrune(me(['viewer', 'operator']))).toBe(false)
    expect(pruneRunBlock(me(['viewer', 'admin']))).toBeNull()
    expect(canRunPrune(me(['viewer', 'admin']))).toBe(true)
    const off = me(['viewer', 'admin'], { configured: true, reachable: true }, { prune: false, drills: true })
    expect(canRunPrune(off)).toBe(false)
    expect(pruneRunBlock(off)).toMatch(/runs from the CLI or the prune CronJob/)
    expect(pruneRunBlock(me(['viewer', 'admin'], { configured: true, reachable: false }))).toMatch(/not reachable/)
  })
})
