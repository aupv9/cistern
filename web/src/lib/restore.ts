export const SUFFIX_PATTERN = /^_[a-z0-9_]{1,30}$/
export const REASON_MIN = 3
export const REASON_MAX = 500

export type ConfirmState = 'empty' | 'match' | 'mismatch'

export function confirmState(typed: string, job: string): ConfirmState {
  if (typed === '') return 'empty'
  return typed === job ? 'match' : 'mismatch'
}

export function suffixError(suffix: string): string | null {
  if (SUFFIX_PATTERN.test(suffix)) return null
  if (!suffix.startsWith('_')) return 'Start with an underscore, for example _restored.'
  if (suffix.length > 31) return 'Use at most 30 characters after the underscore.'
  if (suffix.length < 2) return 'Add at least one character after the underscore.'
  return 'Use only lowercase letters, digits and underscores.'
}

export function reasonError(reason: string): string | null {
  const r = reason.trim()
  if (r.length < REASON_MIN) return `Write at least ${REASON_MIN} characters, for example an incident or ticket number.`
  if (r.length > REASON_MAX) return `Keep it under ${REASON_MAX} characters.`
  return null
}

export type RestoreFormState = {
  job: string
  typed: string
  reason: string
  suffix: string
  target: string | null
  previewReady: boolean
  submitting: boolean
  blocked: string | null
}

export function canSubmitRestore(s: RestoreFormState): boolean {
  return (
    s.blocked === null &&
    !s.submitting &&
    s.target !== null &&
    s.previewReady &&
    suffixError(s.suffix) === null &&
    reasonError(s.reason) === null &&
    confirmState(s.typed, s.job) === 'match'
  )
}
