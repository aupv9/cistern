import type { Me, Role } from '~/api/queries'

export type Capability = 'backup' | 'verify' | 'restore' | 'approve'

const NEEDS: Record<Capability, Role> = {
  backup: 'operator',
  verify: 'operator',
  restore: 'restorer',
  approve: 'approver',
}

const ROLE_LABEL: Record<Role, string> = {
  viewer: 'Viewer',
  operator: 'Operator',
  restorer: 'Restorer',
  approver: 'Approver',
  admin: 'Admin',
}

export function roleLabel(role: Role): string {
  return ROLE_LABEL[role]
}

export function hasRole(roles: readonly Role[], role: Role): boolean {
  if (roles.includes(role)) return true
  if (role !== 'approver' && roles.includes('admin')) return true
  return false
}

export function can(roles: readonly Role[], capability: Capability): boolean {
  return hasRole(roles, NEEDS[capability])
}

export function hasAnyRole(roles: readonly Role[]): boolean {
  return roles.length > 0
}

export function describeRoles(roles: readonly Role[]): string {
  const out: string[] = []
  if (roles.includes('admin')) out.push('Admin')
  else {
    if (roles.includes('operator')) out.push('Operator')
    if (roles.includes('restorer')) out.push('Restorer')
    if (out.length === 0 && roles.includes('viewer')) out.push('Viewer')
  }
  if (roles.includes('approver')) out.push('Approver')
  return out.join(', ') || 'No role'
}

export function readOnlyReason(deployment: Me['deployment']): string | null {
  if (!deployment.daemon.configured) {
    return 'No Cistern daemon is configured for this server, so the UI is read-only. You can browse jobs, snapshots and the audit log, but not start backups, verifies or restores.'
  }
  if (!deployment.daemon.reachable) {
    return 'The Cistern daemon is not reachable right now, so the UI is read-only. Backups, verifies and restores can start again when it is back.'
  }
  return null
}

export function blockReason(me: Me, capability: Capability): string | null {
  if (!can(me.roles, capability)) {
    return `Needs the ${ROLE_LABEL[NEEDS[capability]]} role.`
  }
  if (capability !== 'approve' && readOnlyReason(me.deployment)) {
    return me.deployment.daemon.configured ? 'The daemon is not reachable.' : 'No daemon is configured.'
  }
  return null
}
