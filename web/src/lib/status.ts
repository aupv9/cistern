import type { ApprovalState, AuditAction, AuditResult, JobHealth, Op, RunState } from '~/api/queries'

export type Tone = 'ok' | 'fail' | 'run' | 'warn' | 'neutral'
export type StatusInfo = { text: string; tone: Tone }

export const HEALTH: Record<JobHealth, StatusInfo> = {
  healthy: { text: 'Healthy', tone: 'ok' },
  failing: { text: 'Failing', tone: 'fail' },
  running: { text: 'Running', tone: 'run' },
  stale: { text: 'Stale', tone: 'fail' },
  verify_overdue: { text: 'Verify overdue', tone: 'warn' },
  never_run: { text: 'Never run', tone: 'neutral' },
}

export const RUN_STATE: Record<RunState, StatusInfo> = {
  queued: { text: 'Queued', tone: 'neutral' },
  running: { text: 'Running', tone: 'run' },
  succeeded: { text: 'Succeeded', tone: 'ok' },
  failed: { text: 'Failed', tone: 'fail' },
  unknown: { text: 'Unknown', tone: 'warn' },
}

export const VERIFY_STATE: Record<RunState, StatusInfo> = {
  queued: { text: 'Queued', tone: 'neutral' },
  running: { text: 'Running', tone: 'run' },
  succeeded: { text: 'Passed', tone: 'ok' },
  failed: { text: 'Failed', tone: 'fail' },
  unknown: { text: 'Unknown', tone: 'warn' },
}

export const AUDIT_RESULT: Record<AuditResult, StatusInfo> = {
  accepted: { text: 'Accepted', tone: 'run' },
  succeeded: { text: 'Succeeded', tone: 'ok' },
  failed: { text: 'Failed', tone: 'fail' },
  denied: { text: 'Denied', tone: 'fail' },
  pending: { text: 'Pending', tone: 'warn' },
}

export const APPROVAL_STATE: Record<ApprovalState, StatusInfo> = {
  pending: { text: 'Waiting for approval', tone: 'warn' },
  approved: { text: 'Approved', tone: 'ok' },
  rejected: { text: 'Rejected', tone: 'fail' },
  expired: { text: 'Expired', tone: 'neutral' },
}

export const OP_LABEL: Record<Op, string> = {
  backup: 'Backup',
  verify: 'Verify',
  restore: 'Restore',
  prune: 'Prune',
}

export const AUDIT_ACTIONS: { value: AuditAction; label: string }[] = [
  { value: 'backup.run', label: 'Backup started' },
  { value: 'verify.run', label: 'Verify started' },
  { value: 'restore.run', label: 'Restore started' },
  { value: 'restore.request', label: 'Restore requested' },
  { value: 'approval.approve', label: 'Approval granted' },
  { value: 'approval.reject', label: 'Approval rejected' },
  { value: 'approval.expire', label: 'Approval expired' },
  { value: 'run.finish', label: 'Run finished' },
  { value: 'auth.login', label: 'Sign-in' },
  { value: 'auth.logout', label: 'Sign-out' },
  { value: 'auth.denied', label: 'Sign-in denied' },
  { value: 'access.denied', label: 'Access denied' },
]

export function auditActionLabel(action: AuditAction): string {
  return AUDIT_ACTIONS.find((a) => a.value === action)?.label ?? action
}

export const AUDIT_RESULTS: { value: AuditResult; label: string }[] = [
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'failed', label: 'Failed' },
  { value: 'denied', label: 'Denied' },
  { value: 'pending', label: 'Pending' },
  { value: 'accepted', label: 'Accepted' },
]
