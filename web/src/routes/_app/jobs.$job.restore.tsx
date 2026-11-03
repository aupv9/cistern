import { useId, useState, type FormEvent } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '~/api/client'
import {
  invalidateAfterAction,
  jobQuery,
  manifestQuery,
  previewQuery,
  snapshotsQuery,
  startRestore,
  type JobDetail,
  type Me,
  type RestoreResult,
  type RestoreTarget,
  type SnapshotSummary,
} from '~/api/queries'
import { CopyButton } from '~/components/CopyButton'
import { CheckCircleIcon, StepCurrentIcon, StepTodoIcon, WarningIcon } from '~/components/Icons'
import { BlockSkeleton, PageSkeleton } from '~/components/Skeleton'
import { EmptyState, ErrorState, InlineError } from '~/components/States'
import { Status } from '~/components/Status'
import { useDebounced } from '~/hooks/useDebounced'
import { useMe } from '~/hooks/useMe'
import { useNow } from '~/hooks/useNow'
import { formatBytes, formatWhen, formatWhenLower } from '~/lib/format'
import { blockReason, can } from '~/lib/roles'
import { canSubmitRestore, confirmState, reasonError, REASON_MAX, suffixError } from '~/lib/restore'
import { APPROVAL_STATE } from '~/lib/status'

type RestoreSearch = { snapshot?: string }

export const Route = createFileRoute('/_app/jobs/$job/restore')({
  validateSearch: (search: Record<string, unknown>): RestoreSearch => ({
    snapshot: typeof search.snapshot === 'string' && search.snapshot ? search.snapshot : undefined,
  }),
  head: ({ params }) => ({ meta: [{ title: `Restore ${params.job} · Cistern` }] }),
  component: RestorePage,
})

function RestorePage() {
  const { job } = Route.useParams()
  const me = useMe()
  const q = useQuery(jobQuery(job))
  const snaps = useQuery(snapshotsQuery(job))

  if (!can(me.roles, 'restore')) {
    return (
      <div className="page">
        <h1 className="h1">Restore {job}</h1>
        <div className="card">
          <EmptyState title="You cannot restore">
            <p className="muted">Restores need the Restorer role. Ask an administrator if you need it.</p>
            <Link to="/jobs/$job" params={{ job }} className="btn btn-sm">
              Back to {job}
            </Link>
          </EmptyState>
        </div>
      </div>
    )
  }
  if (q.isPending || snaps.isPending) return <PageSkeleton title={`Loading restore of ${job}`} />
  if (q.isError || snaps.isError) {
    const error = q.error ?? snaps.error
    const retry = () => {
      void q.refetch()
      void snaps.refetch()
    }
    return (
      <div className="page">
        <div className="card">
          <ErrorState error={error} onRetry={retry} title={`Could not load ${job}`} />
        </div>
      </div>
    )
  }
  if (q.data.restore_targets.length === 0 || snaps.data.length === 0) {
    return (
      <div className="page">
        <h1 className="h1">Restore {job}</h1>
        <div className="card">
          <EmptyState title={snaps.data.length === 0 ? 'Nothing to restore yet' : 'No restore targets'}>
            <p className="muted">
              {snaps.data.length === 0
                ? `${job} has no completed snapshot yet.`
                : `${job} has no restore_targets in the server configuration, so it cannot be restored from here. Use the CLI, or add a target.`}
            </p>
            <Link to="/jobs/$job" params={{ job }} className="btn btn-sm">
              Back to {job}
            </Link>
          </EmptyState>
        </div>
      </div>
    )
  }
  return <RestoreForm job={q.data} snapshots={snaps.data} me={me} />
}

function defaultTarget(targets: readonly RestoreTarget[]): string | null {
  return (targets.find((t) => !t.production) ?? targets[0])?.name ?? null
}

function RestoreForm({ job, snapshots, me }: { job: JobDetail; snapshots: SnapshotSummary[]; me: Me }) {
  const search = Route.useSearch()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const now = useNow(30_000)
  const tz = me.deployment.timezone
  const ids = { snap: useId(), suffix: useId(), suffixHint: useId(), reason: useId(), reasonHint: useId(), confirm: useId(), confirmHint: useId(), submitHint: useId() }

  const initialSnap = search.snapshot && snapshots.some((s) => s.id === search.snapshot) ? search.snapshot : 'latest'
  const [snapshotRef, setSnapshotRef] = useState(initialSnap)
  const [target, setTarget] = useState<string | null>(defaultTarget(job.restore_targets))
  const [suffix, setSuffix] = useState('_restored')
  const [reason, setReason] = useState('')
  const [typed, setTyped] = useState('')
  const [touched, setTouched] = useState({ reason: false, suffix: false })
  const [result, setResult] = useState<RestoreResult | null>(null)

  const isS3 = job.driver === 's3'
  const debouncedSuffix = useDebounced(suffix, 300)
  const suffixErr = isS3 ? null : suffixError(suffix)
  const debouncedSuffixOk = isS3 || suffixError(debouncedSuffix) === null
  const input = { snapshot: snapshotRef, target: target ?? '', restore_suffix: isS3 ? undefined : debouncedSuffix }
  const preview = useQuery(previewQuery(job.name, input, target !== null && debouncedSuffixOk))
  const p = preview.data && !preview.isPlaceholderData ? preview.data : undefined
  const resolved = p?.snapshot ?? (snapshotRef === 'latest' ? snapshots[0] : snapshots.find((s) => s.id === snapshotRef))
  const manifest = useQuery({ ...manifestQuery(job.name, resolved?.id ?? ''), enabled: !!resolved })
  const chosenTarget = job.restore_targets.find((t) => t.name === target)
  const production = chosenTarget?.production ?? false
  const blocked = blockReason(me, 'restore')

  const submit = useMutation({
    mutationFn: () =>
      startRestore(job.name, {
        snapshot: p?.snapshot.id ?? snapshotRef,
        target: target ?? '',
        restore_suffix: isS3 ? undefined : suffix,
        reason: reason.trim(),
        confirm_job: typed,
      }),
    onSuccess: (res) => {
      invalidateAfterAction(queryClient, job.name)
      if (!res.needs_approval && res.run_id) {
        void navigate({ to: '/runs/$run', params: { run: res.run_id } })
        return
      }
      setResult(res)
      window.scrollTo({ top: 0 })
    },
  })

  const serverFields = submit.error instanceof ApiError ? submit.error.fields : preview.error instanceof ApiError ? preview.error.fields : {}
  const confirm = confirmState(typed, job.name)
  const ready = canSubmitRestore({
    job: job.name,
    typed,
    reason,
    suffix: isS3 ? '_x' : suffix,
    target,
    previewReady: !!p && !preview.isFetching,
    submitting: submit.isPending,
    blocked,
  })
  const reasonErr = touched.reason ? reasonError(reason) : null

  if (result?.needs_approval) {
    return (
      <div className="page">
        <div className="page-title">
          <h1 className="h1">Restore {job.name}</h1>
        </div>
        <section className="card" aria-labelledby="wait-h">
          <div className="body">
            <div className="row" style={{ gap: '8px 14px' }}>
              <h2 id="wait-h" className="h2" style={{ fontSize: 18 }}>
                Restore into {chosenTarget?.name ?? target}
              </h2>
              <Status info={APPROVAL_STATE.pending} />
            </div>
            <p role="status" style={{ margin: 0 }}>
              Your request{result.approval_id ? <> <span className="mono">{result.approval_id}</span></> : null} is waiting for approval. Someone with the
              Approver role, other than you, must approve it within 24 hours. Nothing has been restored yet.
            </p>
            <div className="row">
              {result.approval_id ? (
                <Link to="/approvals/$id" params={{ id: result.approval_id }} className="btn btn-p">
                  Open the request
                </Link>
              ) : (
                <Link to="/approvals" className="btn btn-p">
                  Open approvals
                </Link>
              )}
              <Link to="/jobs/$job" params={{ job: job.name }} className="btn">
                Back to {job.name}
              </Link>
            </div>
          </div>
        </section>
      </div>
    )
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    setTouched({ reason: true, suffix: true })
    if (ready) submit.mutate()
  }

  const steps = [
    { label: 'Snapshot', done: !!resolved },
    { label: 'Target', done: !!chosenTarget },
  ]

  return (
    <form className="page" onSubmit={onSubmit} noValidate aria-labelledby="restore-h">
      <div className="page-title">
        <h1 id="restore-h" className="h1">
          Restore {job.name}
        </h1>
        <p>Nothing is written until you confirm. Every restore is recorded in the audit log.</p>
      </div>

      <ol aria-label="Steps" className="steps">
        {steps.flatMap((s) => [
          <li key={s.label} className="step">
            <span style={{ color: s.done ? 'var(--ok)' : 'var(--fg2)', display: 'inline-flex' }}>{s.done ? <CheckCircleIcon /> : <StepTodoIcon />}</span>
            {s.label}
            <span className="sr">{s.done ? ', done' : ', to do'}</span>
          </li>,
          <li key={`${s.label}-rule`} aria-hidden="true" className="step-rule" />,
        ])}
        <li className="step" aria-current="step">
          <StepCurrentIcon />
          Review and confirm
        </li>
      </ol>

      <div className="split">
        <div className="main stack">
          <section aria-labelledby="src-h" className="card">
            <div className="hd">
              <h2 id="src-h" className="h2">
                From
              </h2>
              <Link to="/jobs/$job" params={{ job: job.name }} search={resolved ? { snapshot: resolved.id } : {}}>
                See snapshots
              </Link>
            </div>
            <div className="body">
              <div className="field">
                <label htmlFor={ids.snap}>Snapshot</label>
                <select id={ids.snap} className="select" value={snapshotRef} onChange={(e) => setSnapshotRef(e.target.value)}>
                  <option value="latest">Latest ({snapshots[0]?.id})</option>
                  {snapshots.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.id}, {formatWhen(s.started_at, tz, now)}
                      {s.last_verify?.status === 'succeeded' ? ', verified' : ''}
                    </option>
                  ))}
                </select>
                {serverFields.snapshot ? <span className="field-err">{serverFields.snapshot}</span> : null}
              </div>
              {resolved ? (
                <dl className="kv">
                  <dt>Snapshot</dt>
                  <dd className="mono">{resolved.id}</dd>
                  <dt>Created</dt>
                  <dd>
                    {formatWhen(resolved.started_at, tz, now)}
                    {resolved.last_verify
                      ? resolved.last_verify.status === 'succeeded'
                        ? `, verified ${formatWhenLower(resolved.last_verify.at, tz, now)}`
                        : `, last verify ${resolved.last_verify.status}`
                      : ', never verified'}
                  </dd>
                  <dt>Contents</dt>
                  <dd>
                    {manifest.data
                      ? manifest.data.artifacts.map((a) => `${a.name} ${formatBytes(a.stored_size)}`).join(', ')
                      : `${resolved.artifacts} artifacts, ${formatBytes(resolved.stored_bytes)}`}
                  </dd>
                  {resolved.depends_on?.length ? (
                    <>
                      <dt>Depends on</dt>
                      <dd className="mono">{resolved.depends_on.join(', ')}</dd>
                    </>
                  ) : null}
                </dl>
              ) : null}
            </div>
          </section>

          <section aria-labelledby="tgt-h" className="card">
            <div className="hd">
              <h2 id="tgt-h" className="h2">
                Into
              </h2>
            </div>
            <div className="body">
              <fieldset className="radio-cards">
                <legend className="sr">Restore target</legend>
                {job.restore_targets.map((t) => (
                  <label key={t.name} className="radio-card">
                    <input type="radio" name="target" value={t.name} checked={target === t.name} onChange={() => setTarget(t.name)} />
                    <span className="rc-body">
                      <span className="rc-title">
                        {t.name}
                        {t.production ? <span className="tag prod">Production</span> : null}
                      </span>
                      <span className="sub">
                        {t.description ? `${t.description} · ` : ''}
                        {t.production ? 'needs a second approver' : 'starts right away'}
                      </span>
                    </span>
                  </label>
                ))}
              </fieldset>
              {serverFields.target ? <span className="field-err">{serverFields.target}</span> : null}
              {!isS3 ? (
                <div className="field">
                  <label htmlFor={ids.suffix}>Restore suffix</label>
                  <input
                    id={ids.suffix}
                    className="input mono"
                    style={{ maxWidth: 280 }}
                    value={suffix}
                    autoComplete="off"
                    spellCheck={false}
                    aria-invalid={suffixErr !== null || !!serverFields.restore_suffix}
                    aria-describedby={ids.suffixHint}
                    onChange={(e) => setSuffix(e.target.value)}
                    onBlur={() => setTouched((t) => ({ ...t, suffix: true }))}
                  />
                  <span id={ids.suffixHint} className={suffixErr || serverFields.restore_suffix ? 'field-err' : 'hint'}>
                    {suffixErr ?? serverFields.restore_suffix ?? 'Each database is restored as a new database with this suffix. The live databases are not touched.'}
                  </span>
                </div>
              ) : null}
              <div aria-live="polite" aria-busy={preview.isFetching}>
                {preview.isError && !(preview.error instanceof ApiError && Object.keys(preview.error.fields).length) ? (
                  <InlineError error={preview.error} />
                ) : p ? (
                  <dl className="kv">
                    {Object.entries(p.overrides).map(([k, v]) => (
                      <div key={k} style={{ display: 'contents' }}>
                        <dt>{k}</dt>
                        <dd className="mono">{v || '(empty)'}</dd>
                      </div>
                    ))}
                  </dl>
                ) : target ? (
                  <BlockSkeleton lines={3} label="Preparing preview" />
                ) : null}
              </div>
            </div>
          </section>

          <section aria-labelledby="cmd-h" className="card">
            <div className="hd">
              <h2 id="cmd-h" className="h2">
                Same restore from the CLI
              </h2>
              {p ? <CopyButton text={p.cli_command} /> : null}
            </div>
            {p ? <pre className="code">{p.cli_command}</pre> : <BlockSkeleton lines={4} label="Preparing command" />}
          </section>
        </div>

        <aside aria-labelledby="conf-h" className="card side">
          <div className="hd">
            <h2 id="conf-h" className="h2">
              Confirm
            </h2>
          </div>
          <div className="body">
            {production ? (
              <div className="callout wrn box">
                <WarningIcon />
                <span>
                  <strong>{chosenTarget?.name} is a production target.</strong> Someone with the Approver role, other than you, must approve this restore
                  within 24 hours.
                </span>
              </div>
            ) : null}
            {blocked ? (
              <div className="callout info box">
                <span>{blocked} Restores cannot start right now.</span>
              </div>
            ) : null}
            <div className="field">
              <label htmlFor={ids.reason}>Reason</label>
              <textarea
                id={ids.reason}
                className="textarea"
                rows={3}
                maxLength={REASON_MAX}
                value={reason}
                aria-invalid={!!reasonErr || !!serverFields.reason}
                aria-describedby={ids.reasonHint}
                placeholder="For example INC-4821: rows deleted by a faulty migration"
                onChange={(e) => setReason(e.target.value)}
                onBlur={() => setTouched((t) => ({ ...t, reason: true }))}
              />
              <span id={ids.reasonHint} className={reasonErr || serverFields.reason ? 'field-err' : 'hint'}>
                {reasonErr ?? serverFields.reason ?? 'Required. It is shown to the approver and kept in the audit log.'}
              </span>
            </div>
            <div className="field">
              <label htmlFor={ids.confirm}>
                Type <span className="chip">{job.name}</span> to confirm
              </label>
              <input
                id={ids.confirm}
                type="text"
                className={`input mono${confirm === 'match' ? ' is-valid' : ''}`}
                style={{ minHeight: 40 }}
                autoComplete="off"
                spellCheck={false}
                autoCapitalize="off"
                value={typed}
                aria-invalid={confirm === 'mismatch' || !!serverFields.confirm_job}
                aria-describedby={ids.confirmHint}
                onChange={(e) => setTyped(e.target.value)}
              />
              <span id={ids.confirmHint} className={confirm === 'mismatch' ? 'field-err' : 'hint'}>
                {serverFields.confirm_job ??
                  (confirm === 'match'
                    ? 'Name matches.'
                    : confirm === 'mismatch'
                      ? `That is not the job name. Type ${job.name} exactly.`
                      : 'Typing the name guards against restoring the wrong job.')}
              </span>
            </div>
            {submit.isError && !(submit.error instanceof ApiError && Object.keys(submit.error.fields).length) ? <InlineError error={submit.error} /> : null}
            <button type="submit" className="btn btn-p" style={{ minHeight: 44 }} disabled={!ready} aria-describedby={ids.submitHint}>
              {submit.isPending ? 'Sending…' : production ? 'Request approval' : 'Start restore'}
            </button>
            <span id={ids.submitHint} className="sr">
              {ready ? '' : 'Fill in the reason and type the job name to enable this button.'}
            </span>
            <Link to="/jobs/$job" params={{ job: job.name }} className="btn btn-ghost">
              Cancel
            </Link>
          </div>
        </aside>
      </div>
    </form>
  )
}
