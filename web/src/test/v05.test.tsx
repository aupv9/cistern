import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { getMockDb, resetMockDb } from '~/mocks/handlers'
import { renderApp } from './render'

async function drillCard() {
  return screen.findByRole('region', { name: /^Restore drill/ })
}

describe('restore drill on the job page', () => {
  it('leads with the evidence sentence, then checks and tables', async () => {
    renderApp('/jobs/orders-db')
    const card = await drillCard()
    const evidence = await within(card).findByTestId('drill-evidence')
    expect(evidence).toHaveTextContent(/^Restored and checked \d+ days? ago: 14 tables, 1\.\d M rows, all checks passed\.$/)
    expect(within(card).getByRole('table', { name: /Drill checks/ })).toBeInTheDocument()
    expect(within(card).getByText('No orphaned order items')).toBeInTheDocument()
    expect(within(card).getAllByText('Passed').length).toBeGreaterThan(5)
    const tables = within(card).getByRole('table', { name: /Largest restored tables/ })
    expect(within(tables).getAllByRole('row')).toHaveLength(10)
    expect(within(card).getByText('Runs weekly on Sunday at 05:00')).toBeInTheDocument()
    expect(within(card).getByText('Fails after 35 days without a passed drill')).toBeInTheDocument()
    const history = await within(card).findByRole('table', { name: /Recent restore drills/ })
    expect(within(history).getAllByText('Failed')).toHaveLength(1)
  })

  it('shows the failed check with expected and actual values', async () => {
    renderApp('/jobs/billing-db')
    const card = await drillCard()
    expect(await within(card).findByText(/^The restore drill \d+ days? ago failed 1 of 5 checks\.$/)).toBeInTheDocument()
    const row = within(card).getByText('ledger.entries has rows').closest('tr')!
    expect(row).toHaveClass('row-fail')
    expect(within(row).getByText('Failed')).toBeInTheDocument()
    expect(within(row).getByText('at least 1,000,000')).toBeInTheDocument()
    expect(within(row).getByText('0')).toBeInTheDocument()
  })

  it('starts a drill for restorers and opens the run page', async () => {
    const { user, router } = renderApp('/jobs/orders-db')
    const card = await drillCard()
    const button = within(card).getByRole('button', { name: 'Run drill now' })
    await waitFor(() => expect(button).toBeEnabled())
    await user.click(button)
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/runs\/run_/))
    expect(await screen.findByRole('heading', { name: 'Restore drill orders-db', level: 1 })).toBeInTheDocument()
    expect(getMockDb().audit.some((e) => e.action === 'drill.run' && e.job === 'orders-db')).toBe(true)
  })

  it('disables the button with a reason when the job has no drill or a run is active', async () => {
    renderApp('/jobs/shop-db')
    const card = await drillCard()
    expect(within(card).getByText('Restores of shop-db are not tested automatically.')).toBeInTheDocument()
    const button = within(card).getByRole('button', { name: 'Run drill now' })
    expect(button).toBeDisabled()
    expect(button).toHaveAccessibleDescription('shop-db has no drill section in the server configuration.')
  })

  it('disables the button while a backup runs', async () => {
    renderApp('/jobs/billing-db')
    const button = within(await drillCard()).getByRole('button', { name: 'Run drill now' })
    expect(button).toBeDisabled()
    expect(button).toHaveAccessibleDescription('A backup is in progress.')
  })

  it('hides the button from people without the restorer role', async () => {
    resetMockDb({ roles: ['viewer', 'operator'] })
    renderApp('/jobs/orders-db')
    const card = await drillCard()
    await within(card).findByTestId('drill-evidence')
    expect(within(card).queryByRole('button', { name: 'Run drill now' })).not.toBeInTheDocument()
  })

  it('shows the drill result on a finished drill run', async () => {
    const db = resetMockDb()
    const run = db.runs.find((r) => r.op === 'drill' && r.job === 'billing-db' && r.state === 'failed')!
    renderApp(`/runs/${run.id}`)
    const result = await screen.findByRole('region', { name: /Drill result/ })
    expect(within(result).getByTestId('drill-evidence')).toHaveTextContent(/failed 1 of 5 checks/)
    expect(within(result).getByText('ledger.entries has rows')).toBeInTheDocument()
    expect(screen.getByText('Restore drill of billing-db failed.')).toBeInTheDocument()
  })
})

describe('overview with drills', () => {
  it('shows the drill alert, the last drill column and the drill filter', async () => {
    const { user } = renderApp('/')
    expect(await screen.findByText('billing-db failed its last restore drill.')).toBeInTheDocument()
    const jobs = screen.getByRole('region', { name: 'Jobs' })
    await within(jobs).findByRole('link', { name: 'orders-db' })
    expect(within(jobs).getByRole('columnheader', { name: 'Last drill' })).toBeInTheDocument()
    expect(within(screen.getByTestId('job-row-users-db')).getByText('No drill')).toBeInTheDocument()
    expect(within(screen.getByTestId('job-row-orders-db')).getByText(/Passed,/)).toBeInTheDocument()
    await user.click(within(jobs).getByRole('radio', { name: /^Drill failing/ }))
    expect(within(jobs).getByRole('link', { name: 'billing-db' })).toBeInTheDocument()
    expect(within(jobs).queryByRole('link', { name: 'orders-db' })).not.toBeInTheDocument()
  })

  it('renders the drill_failing health state', async () => {
    const db = resetMockDb()
    const billing = db.runs.find((r) => r.job === 'billing-db' && r.state === 'running')!
    db.runs = db.runs.filter((r) => r !== billing)
    delete db.sims[billing.id]
    renderApp('/')
    const row = await screen.findByTestId('job-row-billing-db')
    expect(within(row).getByText('Drill failing')).toBeInTheDocument()
  })
})

describe('prune', () => {
  it('previews what a prune keeps and removes and warns about locked snapshots', async () => {
    const { user } = renderApp('/jobs/billing-db/prune')
    await user.click(await screen.findByRole('button', { name: 'Preview prune' }))
    const totals = await screen.findByLabelText('Prune totals')
    expect(within(totals).getByText('Removed').nextSibling).toHaveTextContent(/^\d+ snapshots?$/)
    const warning = screen.getByTestId('locked-warning')
    expect(warning).toHaveTextContent(/still under Object Lock, the last one until/)
    const removeRows = screen.getAllByText('Remove')
    expect(removeRows.length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: /To remove/ }))
    expect(screen.queryAllByText('Keep')).toHaveLength(0)
  })

  it('explains that running a prune needs the admin role', async () => {
    const { user } = renderApp('/jobs/billing-db/prune')
    await user.click(await screen.findByRole('button', { name: 'Preview prune' }))
    expect(await screen.findByText('Running a prune needs the Admin role.')).toBeInTheDocument()
    expect(screen.getByText('cistern prune --job billing-db')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Run prune' })).not.toBeInTheDocument()
  })

  it('explains the CLI route when prune is off for the UI', async () => {
    const db = resetMockDb({ roles: ['viewer', 'operator', 'admin'] })
    db.me.deployment.features.prune = false
    renderApp('/jobs/uploads/prune')
    expect(await screen.findByText(/runs from the CLI or the prune CronJob/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Run prune' })).not.toBeInTheDocument()
  })

  it('lets an admin run a prune after a preview, a reason and the job name', async () => {
    resetMockDb({ roles: ['viewer', 'operator', 'admin'] })
    const { user, router } = renderApp('/jobs/uploads/prune')
    const run = await screen.findByRole('button', { name: 'Run prune' })
    expect(run).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Preview prune' }))
    await screen.findByText(/A prune would remove/)
    await user.type(screen.getByLabelText('Reason'), 'CHG-112 free space')
    await user.type(screen.getByLabelText(/to confirm/), 'upload')
    expect(screen.getByText('That is not the job name. Type uploads exactly.')).toBeInTheDocument()
    expect(run).toBeDisabled()
    await user.type(screen.getByLabelText(/to confirm/), 's')
    await waitFor(() => expect(run).toBeEnabled())
    await user.click(run)
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/runs\/run_/))
    expect(await screen.findByRole('heading', { name: 'Prune uploads', level: 1 })).toBeInTheDocument()
  })

  it('tells viewers they cannot preview', async () => {
    resetMockDb({ roles: ['viewer'] })
    renderApp('/jobs/orders-db/prune')
    expect(await screen.findByText('You cannot preview a prune')).toBeInTheDocument()
  })
})

describe('history charts', () => {
  it('draws duration and size charts with failed runs marked and a table fallback', async () => {
    renderApp('/jobs/orders-db')
    const section = await screen.findByRole('region', { name: /^History/ })
    const duration = await within(section).findByRole('figure', { name: 'Backup duration' })
    expect(within(section).getByRole('figure', { name: 'Stored size' })).toBeInTheDocument()
    expect(within(duration).getAllByTestId('fail-mark')).toHaveLength(3)
    expect(within(duration).getByText('min')).toBeInTheDocument()
    const table = within(section).getByRole('table', { name: /Backups of orders-db/ })
    expect(within(table).getAllByRole('row').length).toBeGreaterThan(80)
  })

  it('moves between points with the keyboard and announces them', async () => {
    const { user } = renderApp('/jobs/orders-db')
    const section = await screen.findByRole('region', { name: /^History/ })
    const chart = await within(section).findByRole('group', { name: 'Backup duration' })
    chart.focus()
    await waitFor(() => expect(section.querySelector('.chart-tip')).not.toBeNull())
    await user.keyboard('{Home}')
    const live = within(section).getAllByText((_, el) => el?.getAttribute('aria-live') === 'polite' && !!el.textContent)
    expect(live[0]).toHaveTextContent(/^Aug|^Jul|^Sep/)
  })
})

describe('configuration view', () => {
  it('shows the file with line numbers, redactions and the literal secret warning', async () => {
    renderApp('/config')
    expect(await screen.findByRole('heading', { name: 'Configuration', level: 1 })).toBeInTheDocument()
    expect(screen.getByText(/recorded in the audit log/)).toBeInTheDocument()
    expect(screen.getByText('A secret is written into the file.')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: /^line \d+$/ })
    const line = link.getAttribute('href')!.slice(1)
    const row = document.getElementById(line)!
    expect(row).toHaveClass('secret')
    expect(within(row).getByText('<redacted>')).toHaveClass('redacted')
    expect(screen.getByRole('region', { name: /Contents of \/etc\/cistern\/cistern.yaml/ })).toBeInTheDocument()
    expect(getMockDb().audit.some((e) => e.action === 'config.view')).toBe(true)
  })

  it('is a tab for operators', async () => {
    renderApp('/')
    expect(await screen.findByRole('link', { name: 'Configuration' })).toBeInTheDocument()
  })

  it('is not offered to viewers', async () => {
    resetMockDb({ roles: ['viewer'] })
    renderApp('/config')
    expect(await screen.findByText('You cannot view the configuration')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Configuration' })).not.toBeInTheDocument()
  })
})
