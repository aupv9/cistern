import { useId } from 'react'
import { confirmState, REASON_MAX } from '~/lib/restore'

export function ReasonField({
  value,
  onChange,
  onBlur,
  error,
  placeholder,
  hint,
}: {
  value: string
  onChange: (v: string) => void
  onBlur?: () => void
  error?: string | null
  placeholder?: string
  hint: string
}) {
  const id = useId()
  const hintId = useId()
  return (
    <div className="field">
      <label htmlFor={id}>Reason</label>
      <textarea
        id={id}
        className="textarea"
        rows={3}
        maxLength={REASON_MAX}
        value={value}
        aria-invalid={!!error}
        aria-describedby={hintId}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onBlur={onBlur}
      />
      <span id={hintId} className={error ? 'field-err' : 'hint'}>
        {error ?? hint}
      </span>
    </div>
  )
}

export function ConfirmJobName({
  job,
  value,
  onChange,
  serverError,
  idleHint,
}: {
  job: string
  value: string
  onChange: (v: string) => void
  serverError?: string
  idleHint: string
}) {
  const id = useId()
  const hintId = useId()
  const confirm = confirmState(value, job)
  return (
    <div className="field">
      <label htmlFor={id}>
        Type <span className="chip">{job}</span> to confirm
      </label>
      <input
        id={id}
        type="text"
        className={`input mono${confirm === 'match' ? ' is-valid' : ''}`}
        style={{ minHeight: 40 }}
        autoComplete="off"
        spellCheck={false}
        autoCapitalize="off"
        value={value}
        aria-invalid={confirm === 'mismatch' || !!serverError}
        aria-describedby={hintId}
        onChange={(e) => onChange(e.target.value)}
      />
      <span id={hintId} className={confirm === 'mismatch' || serverError ? 'field-err' : 'hint'}>
        {serverError ??
          (confirm === 'match' ? 'Name matches.' : confirm === 'mismatch' ? `That is not the job name. Type ${job} exactly.` : idleHint)}
      </span>
    </div>
  )
}
