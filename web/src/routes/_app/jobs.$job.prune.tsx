import { useState, type FormEvent } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '~/api/client'
import { invalidateAfterAction, isActive, jobQuery, previewPrune, startPrune, type JobDetail, type Me, type PrunePreview } from '~/api/queries'
import { ConfirmJobName, ReasonField } from '~/components/ConfirmFields'
import { CopyButton } from '~/components/CopyButton'
import { PruneTotals } from '~/components/Prune'
import { InfoIcon, LockIcon, WarningIcon } from '~/components/Icons'
import { PageSkeleton } from '~/components/Skeleton'
import { EmptyState, ErrorState, InlineError } from '~/components/States'
import { Status } from '~/components/Status'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { formatBytes, formatDay, formatWhen, plural } from '~/lib/format'
import { retentionText } from '~/lib/job'
import { isLocked, lockedWarning, PRUNE_ACTION, pruneHeadline, pruneReasonText } from '~/lib/prune'
import { confirmState, reasonError } from '~/lib/restore'
import { blockReason, can, pruneRunBlock } from '~/lib/roles'

export const Route = createFileRoute('/_app/jobs/$job/prune')({
  head: ({ params }) => ({ meta: [{ title: `Prune ${params.job} · Cistern` }] }),
  component: PrunePage,
})

function PrunePage() {
  const { job } = Route.useParams()
  const me = useMe()
  const q = useQuery(jobQuery(job))

  if (!can(me.roles, 'prune_preview')) {
    return (
      <div className="page">
        <h1 className="h1">Prune {job}</h1>
        <div className="card">
          <EmptyState title="You cannot preview a prune">
            <p className="muted">Previewing a prune needs the Operator role. Ask an administrator if you need it.</p>
            <Link to="/jobs/$job" params={{ job }} className="btn btn-sm">
              Back to {job}
            </Link>
          </EmptyState>
        </div>
      </div>
    )
  }
  if (q.isPending) return <PageSkeleton title={`Loading prune of ${job}`} />
  if (q.isError) {
    return (
      <div className="page">
        <div className="card">
          <ErrorState error={q.error} onRetry={() => void q.refetch()} title={`Could not load ${job}`} />
        </div>
      </div>
    )
  }
  return <PruneView job={q.data} me={me} />
}

function PruneView({ job, me }: { job: JobDetail; me: Me }) {
  const now = useNow(30_000)
  const tz = me.deployment.timezone
  const preview = useMutation({ mutationFn: () => previewPrune(job.name) })
  const p = preview.data

  return (
    <div className="page">
      <div className="page-head">
        <div className="page-title">
          <h1 className="h1">Prune {job.name}</h1>
          <p>Prune removes the snapshots no retention rule keeps. A preview changes nothing.</p>
        </div>
        <Link to="/jobs/$job" params={{ job: job.name }} className="btn">
          Back to {job.name}
        </Link>
      </div>

      <section aria-labelledby="pv-h" className="card card-clip">
        <div className="hd">
          <h2 id="pv-h" className="h2">
            Preview
          </h2>
          <button type="button" className={p ? 'btn btn-sm' : 'btn btn-sm btn-p'} disabled={preview.isPending} aria-busy={preview.isPending} onClick={() => preview.mutate()}>
            {preview.isPending ? 'Computing…' : p ? 'Refresh preview' : 'Preview prune'}
          </button>
        </div>
        <div className="cells">
          <div className="cell">
            <span className="sub">Retention</span>
            <span>{retentionText(job.retention)}</span>
          </div>
          <div className="cell">
            <span className="sub">After each backup</span>
            <span>{job.prune_after_backup ? 'Prunes automatically' : 'Does not prune, run it separately'}</span>
          </div>
        </div>
        {preview.isError ? (
          <div className="body">
            <InlineError error={preview.error} />
          </div>
        ) : p ? (
          <PreviewResult preview={p} tz={tz} now={now} />
        ) : (
          <div className="body">
            <p className="muted" style={{ margin: 0 }}>
              Compute what a prune of {job.name} would keep and remove right now, with the rule that keeps each snapshot and its Object Lock date.
            </p>
          </div>
        )}
      </section>

      <RunPrunePanel job={job} me={me} preview={p} />
    </div>
  )
}

function PreviewResult({ preview, tz, now }: { preview: PrunePreview; tz: string; now: Date }) {
  const warning = lockedWarning(preview, tz, now)
  const [onlyRemoved, setOnlyRemoved] = useState(false)
  const items = onlyRemoved ? preview.items.filter((i) => i.action !== 'keep') : preview.items
  return (
    <div aria-live="polite">
      <PruneTotals preview={preview} />
      <div className="body" style={{ gap: 12 }}>
        <p className="lead" role="status">
          {pruneHeadline(preview)}
        </p>
        {warning ? (
          <div className="callout wrn box" data-testid="locked-warning">
            <WarningIcon />
            <span className="text">{warning}</span>
          </div>
        ) : null}
        {preview.removed > 0 ? (
          <div role="group" aria-label="Show snapshots" className="seg" style={{ alignSelf: 'flex-start' }}>
            <button type="button" aria-pressed={!onlyRemoved} onClick={() => setOnlyRemoved(false)}>
              All<span className="n">{preview.items.length}</span>
            </button>
            <button type="button" aria-pressed={onlyRemoved} onClick={() => setOnlyRemoved(true)}>
              To remove<span className="n">{preview.removed}</span>
            </button>
          </div>
        ) : null}
      </div>
      <div className="scroll-x">
        <table className="t compact">
          <caption className="sr">Snapshots of {preview.job} and what a prune would do with each</caption>
          <thead>
            <tr>
              <th scope="col">Snapshot</th>
              <th scope="col">Created</th>
              <th scope="col">Action</th>
              <th scope="col">Why</th>
              <th scope="col" className="right">
                Size
              </th>
              <th scope="col">Object Lock</th>
            </tr>
          </thead>
          <tbody>
            {items.map((i) => {
              const locked = isLocked(i, now)
              const lockFuture = i.lock_until && new Date(i.lock_until).getTime() > now.getTime()
              return (
                <tr key={i.snapshot} className={i.action !== 'keep' ? 'row-remove' : undefined}>
                  <td className="mono">{i.snapshot}</td>
                  <td>{i.finished_at ? formatWhen(i.finished_at, tz, now) : '–'}</td>
                  <td>
                    <Status info={PRUNE_ACTION[i.action]} small />
                  </td>
                  <td className="wrap" style={{ minWidth: 160 }}>
                    <span className={i.action === 'keep' ? 'muted' : undefined}>{pruneReasonText(i)}</span>
                  </td>
                  <td className="right">{formatBytes(i.stored_bytes)}</td>
                  <td>
                    {locked ? (
                      <span className="lock warn-text">
                        <LockIcon size={14} />
                        Until {formatDay(i.lock_until, tz, now)}
                      </span>
                    ) : lockFuture ? (
                      <span>Until {formatDay(i.lock_until, tz, now)}</span>
                    ) : i.lock_until ? (
                      <span className="muted">Expired</span>
                    ) : (
                      <span className="muted">None</span>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function RunPrunePanel({ job, me, preview }: { job: JobDetail; me: Me; preview: PrunePreview | undefined }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [reason, setReason] = useState('')
  const [typed, setTyped] = useState('')
  const [touched, setTouched] = useState(false)
  const isAdmin = can(me.roles, 'prune')
  const featureOn = me.deployment.features.prune
  const cli = `cistern prune --job ${job.name}`

  const submit = useMutation({
    mutationFn: () => startPrune(job.name, { reason: reason.trim(), confirm_job: typed }),
    onSuccess: (ref) => {
      invalidateAfterAction(queryClient, job.name)
      void navigate({ to: '/runs/$run', params: { run: ref.run_id } })
    },
  })

  if (!isAdmin || !featureOn) {
    return (
      <section aria-labelledby="run-prune-h" className="card">
        <div className="hd">
          <h2 id="run-prune-h" className="h2">
            Run prune
          </h2>
        </div>
        <div className="body narrow">
          <div className="callout info box">
            <InfoIcon />
            <span className="text">{pruneRunBlock(me)}</span>
          </div>
          <p className="muted" style={{ margin: 0 }}>
            {featureOn
              ? 'Ask an admin to run it, or run it from the CLI:'
              : job.prune_after_backup
                ? `${job.name} also prunes after every successful backup. To prune now, run:`
                : 'To prune now, run:'}
          </p>
        </div>
        <div className="hd" style={{ borderTop: '1px solid var(--border)', minHeight: 44 }}>
          <span className="sub">CLI</span>
          <CopyButton text={cli} />
        </div>
        <pre className="code">{cli}</pre>
      </section>
    )
  }

  const busy = isActive(job.current_run?.state)
  const blocked = blockReason(me, 'prune') ?? (busy ? `A ${job.current_run?.op ?? 'run'} of ${job.name} is in progress.` : null)
  const serverFields = submit.error instanceof ApiError ? submit.error.fields : {}
  const reasonErr = touched ? reasonError(reason) : null
  const ready = !!preview && blocked === null && !submit.isPending && reasonError(reason) === null && confirmState(typed, job.name) === 'match'
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (ready) submit.mutate()
  }

  return (
    <section aria-labelledby="run-prune-h" className="card">
      <div className="hd">
        <h2 id="run-prune-h" className="h2">
          Run prune
        </h2>
      </div>
      <form className="body narrow" onSubmit={onSubmit} noValidate>
        {preview && preview.removed > 0 ? (
          <p style={{ margin: 0 }}>
            Deletes {plural(preview.removed, 'snapshot')} and frees about {formatBytes(preview.freed_bytes)}. This cannot be undone.
          </p>
        ) : preview ? (
          <p className="muted" style={{ margin: 0 }}>
            The preview found nothing to remove. A prune now only cleans up run records.
          </p>
        ) : (
          <p className="muted" style={{ margin: 0 }}>
            Preview the prune first, so you can see what it deletes.
          </p>
        )}
        {blocked ? (
          <div className="callout info box">
            <span>{blocked} A prune cannot start right now.</span>
          </div>
        ) : null}
        <ReasonField
          value={reason}
          onChange={setReason}
          onBlur={() => setTouched(true)}
          error={reasonErr ?? serverFields.reason}
          placeholder="For example CHG-112: free space after the retention change"
          hint="Required. It is kept in the audit log."
        />
        <ConfirmJobName
          job={job.name}
          value={typed}
          onChange={setTyped}
          serverError={serverFields.confirm_job}
          idleHint="Typing the name guards against pruning the wrong job."
        />
        {submit.isError && !(submit.error instanceof ApiError && Object.keys(submit.error.fields).length) ? <InlineError error={submit.error} /> : null}
        <button type="submit" className="btn btn-p" style={{ minHeight: 44 }} disabled={!ready}>
          {submit.isPending ? 'Sending…' : 'Run prune'}
        </button>
        {!ready ? <span className="sr">{preview ? 'Fill in the reason and type the job name to enable this button.' : 'Preview the prune to enable this button.'}</span> : null}
      </form>
    </section>
  )
}
