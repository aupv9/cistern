import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { RunPoint } from '~/api/queries'
import { RunStrip, stripLabel } from './RunStrip'

function points(pattern: string): RunPoint[] {
  const map = { o: 'succeeded', x: 'failed', r: 'running' } as const
  return pattern.split('').map((c, i) => ({
    status: map[c as keyof typeof map],
    started_at: new Date(Date.UTC(2026, 9, 5 + i, 19)).toISOString(),
    duration_ms: 240_000 + i * 1000,
  }))
}

describe('RunStrip', () => {
  it('labels the strip for screen readers', () => {
    render(<RunStrip points={points('ooooox' + 'r')} />)
    expect(screen.getByRole('img', { name: 'Last 7 runs: 5 succeeded, 1 failed, 1 running' })).toBeInTheDocument()
  })

  it('says all succeeded and the duration range in the large strip', () => {
    expect(stripLabel(points('ooo'), 'backups', true)).toBe('Last 3 backups: all 3 succeeded, from 4m 00s to 4m 02s')
  })

  it('draws one bar per run with a class per state', () => {
    render(<RunStrip points={points('oxr')} />)
    const strip = screen.getByTestId('run-strip')
    const bars = Array.from(strip.children)
    expect(bars).toHaveLength(3)
    expect(bars.map((b) => b.className)).toEqual(['b-ok', 'b-fail', 'b-run'])
  })

  it('shows an empty state without runs', () => {
    render(<RunStrip points={[]} />)
    expect(screen.getByText('No runs yet')).toBeInTheDocument()
  })
})
