import type { Approval, Me } from '~/api/queries'
import { hasRole } from './roles'

export const NOTE_MIN = 3
export const NOTE_MAX = 500

export function effectiveState(a: Approval, now: Date = new Date()): Approval['state'] {
  if (a.state === 'pending' && new Date(a.expires_at).getTime() <= now.getTime()) return 'expired'
  return a.state
}

export function isOwnRequest(a: Approval, me: Me): boolean {
  return a.requested_by.id === me.user.id
}

export function decideBlock(a: Approval, me: Me, now: Date = new Date()): string | null {
  const state = effectiveState(a, now)
  if (state === 'expired') return 'This request expired before anyone decided. Ask for the restore again if it is still needed.'
  if (state !== 'pending') return 'This request has already been decided.'
  if (isOwnRequest(a, me)) return 'You asked for this restore, so someone else with the Approver role has to decide.'
  if (!hasRole(me.roles, 'approver')) return 'Only people with the Approver role can approve or reject restores.'
  if (a.can_decide !== true) return 'You cannot decide this request.'
  return null
}

export function rejectNoteError(note: string): string | null {
  const n = note.trim()
  if (n.length < NOTE_MIN) return 'Write why you are rejecting, so the requester knows what to change.'
  if (n.length > NOTE_MAX) return `Keep the note under ${NOTE_MAX} characters.`
  return null
}

export function approveNoteError(note: string): string | null {
  return note.trim().length > NOTE_MAX ? `Keep the note under ${NOTE_MAX} characters.` : null
}
