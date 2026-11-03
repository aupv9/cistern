import { useId, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { approve, invalidateAfterAction, keys, reject, snapshotsQuery, type Approval, type Me } from '~/api/queries'
import { approveNoteError, decideBlock, effectiveState, NOTE_MAX, rejectNoteError } from '~/lib/approvals'
import { formatWhenLower } from '~/lib/format'
import { APPROVAL_STATE, VERIFY_STATE } from '~/lib/status'
import { InlineError } from './States'
import { Status } from './Status'

export function ApprovalCard({ approval, me, now, headingLevel = 2 }: { approval: Approval; me: Me; now: Date; headingLevel?: 1 | 2 }) {
  const tz = me.deployment.timezone
  const state = effectiveState(approval, now)
  const block = decideBlock(approval, me, now)
  const headingId = useId()
  const noteId = useId()
  const noteHint = useId()
  const blockId = useId()
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const [note, setNote] = useState('')
  const [noteErr, setNoteErr] = useState<string | null>(null)
  const queryClient = useQueryClient()
  const snaps = useQuery(snapshotsQuery(approval.job))
  const snap = snaps.data?.find((s) => s.id === approval.snapshot)

  const onDecided = (a: Approval) => {
    queryClient.setQueryData(keys.approval(a.id), a)
    invalidateAfterAction(queryClient, a.job)
  }
  const approveM = useMutation({ mutationFn: () => approve(approval.id, note.trim() || undefined), onSuccess: onDecided })
  const rejectM = useMutation({ mutationFn: () => reject(approval.id, note.trim()), onSuccess: onDecided })
  const busy = approveM.isPending || rejectM.isPending
  const H = headingLevel === 1 ? 'h1' : 'h2'

  return (
    <section aria-labelledby={headingId} className="card" data-testid={`approval-${approval.id}`}>
      <div className="row" style={{ alignItems: 'flex-start', justifyContent: 'space-between', gap: 12, padding: '16px 20px', borderBottom: '1px solid var(--border)' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0 }}>
          <div className="row" style={{ gap: '8px 14px' }}>
            <H id={headingId} style={{ margin: 0, fontSize: 18, fontWeight: 600, letterSpacing: '-0.02em' }}>
              Restore {approval.job} into {approval.target.name}
            </H>
            <Status info={APPROVAL_STATE[state]} />
          </div>
          <span className="sub" style={{ fontSize: 14 }}>
            {approval.requested_by.name} asked {formatWhenLower(approval.requested_at, tz, now)}.{' '}
            {state === 'pending'
              ? `Expires ${formatWhenLower(approval.expires_at, tz, now)}.`
              : state === 'expired'
                ? `Expired ${formatWhenLower(approval.expires_at, tz, now)}.`
                : ''}
          </span>
        </div>
        <span className="mono muted">{approval.id}</span>
      </div>

      <div className="row" style={{ alignItems: 'flex-start', gap: 24, padding: 20 }}>
        <dl className="kv" style={{ flex: '1 1 400px' }}>
          <dt>Snapshot</dt>
          <dd>
            <Link to="/jobs/$job" params={{ job: approval.job }} search={{ snapshot: approval.snapshot }} className="mono">
              {approval.snapshot}
            </Link>
            {snap ? `, ${formatWhenLower(snap.started_at, tz, now)}` : ''}
          </dd>
          <dt>Verify</dt>
          <dd>
            {snap?.last_verify ? (
              <Status info={{ ...VERIFY_STATE[snap.last_verify.status], text: `${VERIFY_STATE[snap.last_verify.status].text} ${formatWhenLower(snap.last_verify.at, tz, now)}` }} />
            ) : snaps.isPending ? (
              '…'
            ) : (
              'Never verified'
            )}
          </dd>
          <dt>Target</dt>
          <dd>
            {approval.target.description ?? approval.target.name}
            {approval.target.production ? (
              <>
                {' '}
                <span className="tag prod">Production</span>
              </>
            ) : null}
          </dd>
          <dt>Restore suffix</dt>
          <dd>
            <span className="mono">{approval.restore_suffix}</span>
            <span className="muted">, restored into new databases; the live ones are not changed</span>
          </dd>
        </dl>
        <div style={{ flex: '1 1 320px', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
          <span className="sub">Reason</span>
          <p className="reason">{approval.reason}</p>
        </div>
      </div>

      {state === 'pending' ? (
        <div className="foot body" style={{ gap: 14 }}>
          {block ? (
            <p id={blockId} style={{ margin: 0 }} className="muted">
              {block}
            </p>
          ) : (
            <div className="field">
              <label htmlFor={noteId}>
                Note <span style={{ fontWeight: 400, color: 'var(--fg2)' }}>required to reject</span>
              </label>
              <textarea
                id={noteId}
                ref={noteRef}
                className="textarea"
                rows={2}
                maxLength={NOTE_MAX}
                value={note}
                aria-invalid={noteErr !== null}
                aria-describedby={noteHint}
                onChange={(e) => {
                  setNote(e.target.value)
                  setNoteErr(null)
                }}
              />
              <span id={noteHint} className={noteErr ? 'field-err' : 'hint'}>
                {noteErr ?? `${approval.requested_by.name} sees this note.`}
              </span>
            </div>
          )}
          <InlineError error={approveM.error ?? rejectM.error} />
          <div className="row">
            <button
              type="button"
              className="btn btn-p"
              disabled={block !== null || busy}
              aria-describedby={block ? blockId : undefined}
              onClick={() => {
                const err = approveNoteError(note)
                if (err) {
                  setNoteErr(err)
                  return
                }
                approveM.mutate()
              }}
            >
              {approveM.isPending ? 'Approving…' : 'Approve and start restore'}
            </button>
            <button
              type="button"
              className="btn btn-d"
              disabled={block !== null || busy}
              aria-describedby={block ? blockId : undefined}
              onClick={() => {
                const err = rejectNoteError(note)
                if (err) {
                  setNoteErr(err)
                  noteRef.current?.focus()
                  return
                }
                rejectM.mutate()
              }}
            >
              {rejectM.isPending ? 'Rejecting…' : 'Reject'}
            </button>
          </div>
        </div>
      ) : (
        <div className="foot row" style={{ justifyContent: 'space-between', gap: 12, padding: '14px 20px' }} role="status">
          <span>
            {state === 'approved'
              ? `Approved by ${approval.decided_by?.name ?? 'an approver'}${approval.decided_at ? ` ${formatWhenLower(approval.decided_at, tz, now)}` : ''}.`
              : state === 'rejected'
                ? `Rejected by ${approval.decided_by?.name ?? 'an approver'}${approval.decided_at ? ` ${formatWhenLower(approval.decided_at, tz, now)}` : ''}.`
                : `Nobody decided before it expired ${formatWhenLower(approval.expires_at, tz, now)}.`}
            {approval.note ? <span className="muted"> Note: {approval.note}</span> : null}
          </span>
          {approval.run_id ? (
            <Link to="/runs/$run" params={{ run: approval.run_id }} className="btn btn-sm">
              Open restore run
            </Link>
          ) : null}
        </div>
      )}
    </section>
  )
}
