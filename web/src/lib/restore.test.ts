import { describe, expect, it } from 'vitest'
import { canSubmitRestore, confirmState, reasonError, suffixError, type RestoreFormState } from './restore'

const base: RestoreFormState = {
  job: 'orders-db',
  typed: 'orders-db',
  reason: 'INC-4821',
  suffix: '_restored',
  target: 'staging',
  previewReady: true,
  submitting: false,
  blocked: null,
}

describe('restore confirmation', () => {
  it('compares the typed name exactly', () => {
    expect(confirmState('', 'orders-db')).toBe('empty')
    expect(confirmState('orders-d', 'orders-db')).toBe('mismatch')
    expect(confirmState('Orders-db', 'orders-db')).toBe('mismatch')
    expect(confirmState('orders-db', 'orders-db')).toBe('match')
  })

  it('validates the suffix and reason', () => {
    expect(suffixError('_restored')).toBeNull()
    expect(suffixError('restored')).toMatch(/underscore/)
    expect(suffixError('_Restored')).toMatch(/lowercase/)
    expect(suffixError(`_${'a'.repeat(31)}`)).toMatch(/30/)
    expect(reasonError('ab')).toMatch(/at least 3/)
    expect(reasonError('INC-1')).toBeNull()
  })

  it('enables submit only when everything is ready', () => {
    expect(canSubmitRestore(base)).toBe(true)
    expect(canSubmitRestore({ ...base, typed: 'orders' })).toBe(false)
    expect(canSubmitRestore({ ...base, reason: ' ' })).toBe(false)
    expect(canSubmitRestore({ ...base, target: null })).toBe(false)
    expect(canSubmitRestore({ ...base, previewReady: false })).toBe(false)
    expect(canSubmitRestore({ ...base, submitting: true })).toBe(false)
    expect(canSubmitRestore({ ...base, blocked: 'The daemon is not reachable.' })).toBe(false)
    expect(canSubmitRestore({ ...base, suffix: 'bad' })).toBe(false)
  })
})
