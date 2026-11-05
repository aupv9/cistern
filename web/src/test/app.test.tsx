import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { getMockDb, resetMockDb } from '~/mocks/handlers'
import { renderApp } from './render'

async function jobsRegion() {
  await screen.findByRole('link', { name: 'orders-db' })
  return screen.getByRole('region', { name: 'Jobs' })
}

describe('overview', () => {
  it('shows jobs, the failing alert and the pending approval', async () => {
    renderApp('/')
    expect(await screen.findByRole('heading', { name: 'Overview', level: 1 })).toBeInTheDocument()
    expect(await screen.findByText('shop-db failed its last 2 backups.')).toBeInTheDocument()
    const jobs = await jobsRegion()
    expect(within(jobs).getAllByRole('row')).toHaveLength(6)
    expect(screen.getByText('Restore orders-db into production')).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /^Approvals\s*, 1 pending$/ })).toBeInTheDocument()
  })

  it('filters jobs by status and search', async () => {
    const { user } = renderApp('/')
    const jobs = await jobsRegion()
    await user.click(within(jobs).getByRole('radio', { name: /^Failing/ }))
    expect(within(jobs).getByRole('radio', { name: /^Failing/ })).toHaveAttribute('aria-checked', 'true')
    expect(within(jobs).getByText('shop-db')).toBeInTheDocument()
    expect(within(jobs).queryByText('orders-db')).not.toBeInTheDocument()
    await user.click(within(jobs).getByRole('radio', { name: /^All/ }))
    await user.type(within(jobs).getByRole('searchbox', { name: 'Search jobs' }), 'nothing-like-this')
    expect(within(jobs).getByText('No jobs match these filters.')).toBeInTheDocument()
    await user.click(within(jobs).getByRole('button', { name: 'Clear filters' }))
    expect(within(jobs).getByText('orders-db')).toBeInTheDocument()
  })

  it('sorts jobs by a column header', async () => {
    const { user } = renderApp('/')
    const jobs = await jobsRegion()
    const names = () => within(jobs).getAllByTestId(/^job-row-/).map((r) => r.dataset.testid!.replace('job-row-', ''))
    const header = within(jobs).getByRole('columnheader', { name: /Job/ })
    expect(header).toHaveAttribute('aria-sort', 'none')
    await user.click(within(header).getByRole('button'))
    expect(header).toHaveAttribute('aria-sort', 'ascending')
    const asc = names()
    expect(asc).toEqual([...asc].sort())
    await user.click(within(header).getByRole('button'))
    expect(header).toHaveAttribute('aria-sort', 'descending')
    expect(names()).toEqual([...asc].reverse())
  })

  it('puts failing jobs first when sorted by status', async () => {
    const { user } = renderApp('/')
    const jobs = await jobsRegion()
    const header = within(jobs).getByRole('columnheader', { name: /Status/ })
    await user.click(within(header).getByRole('button'))
    expect(header).toHaveAttribute('aria-sort', 'ascending')
    expect(within(jobs).getAllByTestId(/^job-row-/)[0]).toHaveAttribute('data-testid', 'job-row-shop-db')
  })

  it('hides a column from the Columns menu and remembers it', async () => {
    const { user, unmount } = renderApp('/')
    const jobs = await jobsRegion()
    expect(within(jobs).getByRole('columnheader', { name: /Stored/ })).toBeInTheDocument()
    await user.click(within(jobs).getByRole('button', { name: 'Columns' }))
    const item = await screen.findByRole('menuitemcheckbox', { name: 'Stored' })
    expect(item).toHaveAttribute('aria-checked', 'true')
    await user.click(item)
    expect(within(jobs).queryByRole('columnheader', { name: /Stored/ })).not.toBeInTheDocument()
    expect(screen.getByRole('menuitemcheckbox', { name: 'Stored' })).toHaveAttribute('aria-checked', 'false')
    await user.keyboard('{Escape}')
    unmount()
    renderApp('/')
    const again = await jobsRegion()
    expect(within(again).queryByRole('columnheader', { name: /Stored/ })).not.toBeInTheDocument()
  })

  it('hides Run backup from viewers', async () => {
    resetMockDb({ roles: ['viewer'] })
    renderApp('/')
    const jobs = await jobsRegion()
    expect(within(jobs).queryByRole('button', { name: /Run backup/ })).not.toBeInTheDocument()
  })

  it('shows Run backup to operators and disables it while a job runs', async () => {
    renderApp('/')
    const jobs = await jobsRegion()
    expect(await within(jobs).findByRole('button', { name: 'Run backup of orders-db' })).toBeEnabled()
    expect(within(jobs).getByRole('button', { name: 'Run backup of billing-db' })).toBeDisabled()
  })

  it('shows a read-only banner and disables actions when the daemon is down', async () => {
    const db = resetMockDb()
    db.me.deployment.daemon.reachable = false
    renderApp('/')
    expect(await screen.findByText(/not reachable right now/)).toBeInTheDocument()
    const jobs = await jobsRegion()
    expect(await within(jobs).findByRole('button', { name: 'Run backup of orders-db' })).toBeDisabled()
  })

  it('sends people without a role to the denied page', async () => {
    resetMockDb({ roles: [] })
    renderApp('/')
    expect(await screen.findByRole('heading', { name: 'You do not have access' })).toBeInTheDocument()
  })
})

describe('job detail', () => {
  it('shows the summary, run strip and manifest of the selected snapshot', async () => {
    const { user } = renderApp('/jobs/orders-db')
    expect(await screen.findByRole('heading', { name: 'orders-db', level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: /Last 30 backups: all 30 succeeded/ })).toBeInTheDocument()
    const manifest = screen.getByRole('complementary', { name: 'Manifest' })
    expect(await within(manifest).findByText('db/orders.dump')).toBeInTheDocument()
    const picks = await screen.findAllByRole('button', { pressed: false })
    const second = picks.find((b) => b.className === 'snap-pick')!
    await user.click(second)
    await waitFor(() => expect(within(manifest).getByText(second.textContent!)).toBeInTheDocument())
  })

  it('shows the current run with progress and a log toggle', async () => {
    const { user } = renderApp('/jobs/billing-db')
    expect(await screen.findByText('Backup running')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: 'Backup progress' })).toBeInTheDocument()
    const toggle = screen.getByRole('button', { name: 'Show log' })
    await user.click(toggle)
    expect(screen.getByRole('button', { name: 'Hide log' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('shows not found for an unknown job', async () => {
    renderApp('/jobs/nope')
    expect(await screen.findByText('There is no job named nope.')).toBeInTheDocument()
  })
})

describe('restore', () => {
  it('enables the button only after a reason and the exact job name', async () => {
    const { user } = renderApp('/jobs/orders-db/restore')
    const submit = await screen.findByRole('button', { name: 'Start restore' })
    expect(submit).toBeDisabled()
    await screen.findByText(/cistern restore --job orders-db/)
    await user.type(screen.getByLabelText('Reason'), 'INC-4821 rows deleted')
    const confirm = screen.getByLabelText(/to confirm/)
    await user.type(confirm, 'orders')
    expect(screen.getByText('That is not the job name. Type orders-db exactly.')).toBeInTheDocument()
    expect(confirm).toHaveAttribute('aria-invalid', 'true')
    expect(submit).toBeDisabled()
    await user.type(confirm, '-db')
    expect(screen.getByText('Name matches.')).toBeInTheDocument()
    await waitFor(() => expect(submit).toBeEnabled())
  })

  it('asks for approval when the target is production', async () => {
    const { user, router } = renderApp('/jobs/orders-db/restore')
    await screen.findByText(/--set host=staging-db.internal/)
    await user.click(screen.getByRole('radio', { name: /production/ }))
    expect(await screen.findByText(/--set host=pg-orders.internal/)).toBeInTheDocument()
    expect(screen.getByText(/is a production target/)).toBeInTheDocument()
    await user.type(screen.getByLabelText('Reason'), 'INC-4821 rows deleted')
    await user.type(screen.getByLabelText(/to confirm/), 'orders-db')
    const submit = screen.getByRole('button', { name: 'Request approval' })
    await waitFor(() => expect(submit).toBeEnabled())
    await user.click(submit)
    expect(await screen.findByText(/is waiting for approval/)).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'Open the request' })
    expect(link).toHaveAttribute('href', '/approvals/REQ-0194')
    expect(getMockDb().approvals.find((a) => a.id === 'REQ-0194')?.requested_by.name).toBe('An Nguyen')
    expect(router.state.location.pathname).toBe('/jobs/orders-db/restore')
  })

  it('goes to the run page for a staging restore', async () => {
    const { user, router } = renderApp('/jobs/orders-db/restore')
    await screen.findByText(/--set host=staging-db.internal/)
    await user.type(screen.getByLabelText('Reason'), 'Check refunds')
    await user.type(screen.getByLabelText(/to confirm/), 'orders-db')
    const submit = screen.getByRole('button', { name: 'Start restore' })
    await waitFor(() => expect(submit).toBeEnabled())
    await user.click(submit)
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/runs\/run_/))
    expect(await screen.findByRole('heading', { name: 'Restore orders-db' })).toBeInTheDocument()
  })

  it('validates the suffix before asking for a preview', async () => {
    const { user } = renderApp('/jobs/orders-db/restore')
    const suffix = await screen.findByLabelText('Restore suffix')
    await user.clear(suffix)
    await user.type(suffix, 'Bad')
    expect(screen.getByText('Start with an underscore, for example _restored.')).toBeInTheDocument()
  })
})

describe('approvals', () => {
  it('needs a note to reject, then records the decision', async () => {
    const { user } = renderApp('/approvals/REQ-0193')
    const reject = await screen.findByRole('button', { name: 'Reject' })
    await user.click(reject)
    expect(screen.getByText('Write why you are rejecting, so the requester knows what to change.')).toBeInTheDocument()
    expect(screen.getByLabelText(/Note/)).toHaveFocus()
    await user.type(screen.getByLabelText(/Note/), 'Restore into staging first')
    await user.click(reject)
    expect(await screen.findByText(/Rejected by An Nguyen/)).toBeInTheDocument()
    expect(getMockDb().approvals.find((a) => a.id === 'REQ-0193')?.state).toBe('rejected')
  })

  it('approves and links to the restore run', async () => {
    const { user } = renderApp('/approvals/REQ-0193')
    await user.click(await screen.findByRole('button', { name: 'Approve and start restore' }))
    expect(await screen.findByText(/Approved by An Nguyen/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open restore run' })).toBeInTheDocument()
  })

  it('explains why you cannot decide your own request', async () => {
    const db = resetMockDb()
    const req = db.approvals.find((a) => a.id === 'REQ-0193')!
    req.requested_by = db.me.user
    renderApp('/approvals/REQ-0193')
    expect(await screen.findByText(/You asked for this restore/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Approve and start restore' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Reject' })).toBeDisabled()
  })

  it('explains that only approvers can decide', async () => {
    resetMockDb({ roles: ['viewer', 'operator', 'restorer'] })
    renderApp('/approvals/REQ-0193')
    expect(await screen.findByText(/Only people with the Approver role/)).toBeInTheDocument()
  })

  it('shows an expired request without actions', async () => {
    renderApp('/approvals/REQ-0176')
    expect(await screen.findByText(/Nobody decided before it expired/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument()
  })
})

describe('audit log', () => {
  it('pages with Older and Newer and filters by result', async () => {
    const { user } = renderApp('/audit')
    const older = await screen.findByRole('button', { name: 'Older' })
    await waitFor(() => expect(older).toBeEnabled())
    expect(screen.getByRole('button', { name: 'Newer' })).toBeDisabled()
    await user.click(older)
    expect(await screen.findByText(/Page 2/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Newer' }))
    expect(await screen.findByText(/Page 1/)).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText('Result'), 'denied')
    await waitFor(() => expect(screen.getAllByRole('row').length).toBeLessThan(6))
    expect(screen.getByRole('link', { name: /Export CSV/ }).getAttribute('href')).toMatch(/result=denied/)
  })
})

describe('run page', () => {
  it('shows status with a live region and the log', async () => {
    const db = resetMockDb()
    const run = db.runs.find((r) => r.state === 'failed' && r.job === 'shop-db')!
    renderApp(`/runs/${run.id}`)
    expect(await screen.findByRole('heading', { name: 'Backup shop-db' })).toBeInTheDocument()
    expect(screen.getByText('Backup of shop-db failed.')).toBeInTheDocument()
    expect(screen.getAllByText(/Access denied for user/).length).toBeGreaterThan(0)
    expect(screen.getByRole('region', { name: 'Log' })).toBeInTheDocument()
  })
})
