import { expect, test, type Browser, type BrowserContext, type Page } from '@playwright/test'

type User = { email: string; name: string; roles: string }

const restorer: User = { email: 'ui-restorer@example.com', name: 'Uma Restorer', roles: 'restorer' }
const approver: User = { email: 'ui-approver@example.com', name: 'Abe Approver', roles: 'approver' }
const suffix = `_ui${Date.now().toString(36)}`

let asRestorer: BrowserContext
let asApprover: BrowserContext
let approvalPath = ''
const cspErrors: string[] = []

async function signedIn(browser: Browser, user: User): Promise<BrowserContext> {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const q = new URLSearchParams({ email: user.email, name: user.name, roles: user.roles, return_to: '/' })
  await page.goto(`/api/v1/auth/dev-login?${q}`)
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible()
  await page.close()
  return ctx
}

async function open(ctx: BrowserContext, path: string): Promise<Page> {
  const page = await ctx.newPage()
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy/i.test(m.text())) cspErrors.push(m.text())
  })
  await page.goto(path)
  return page
}

async function waitForRun(page: Page, op: string, job: string) {
  await page.waitForURL(/\/runs\/[^/]+$/)
  await expect(page.getByRole('heading', { name: `${op} ${job}`, level: 1 })).toBeVisible()
  await expect(page.getByText(new RegExp(`${op} of ${job} (succeeded|failed)\\.$`))).toBeAttached({ timeout: 150_000 })
  await expect(page.getByText(`${op} of ${job} succeeded.`)).toBeAttached()
}

test.describe.configure({ mode: 'serial' })

test.beforeAll(async ({ browser }) => {
  asRestorer = await signedIn(browser, restorer)
  asApprover = await signedIn(browser, approver)
})

test.afterAll(async () => {
  await asRestorer?.close()
  await asApprover?.close()
})

test('overview shows the jobs of the real backend', async () => {
  const page = await open(asRestorer, '/')
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible()
  for (const job of ['shop-db', 'crm-db', 'legacy-db', 'analytics']) {
    await expect(page.getByTestId(`job-row-${job}`).getByRole('link', { name: job })).toBeVisible()
  }
  await expect(page.getByTestId('job-row-legacy-db')).toContainText('Failing')
  await expect(page.getByTestId('job-row-analytics')).toContainText('Never run')
  await page.close()
})

test('a backup started from the UI succeeds', async () => {
  const page = await open(asRestorer, '/jobs/shop-db')
  await expect(page.getByRole('heading', { name: 'shop-db', level: 1 })).toBeVisible()
  const run = page.getByRole('button', { name: 'Run backup' })
  await expect(run).toBeEnabled({ timeout: 60_000 })
  await run.click()
  await waitForRun(page, 'Backup', 'shop-db')
  await page.close()
})

test('a restore into the non-production target runs right away', async () => {
  const page = await open(asRestorer, '/jobs/shop-db/restore')
  await expect(page.getByRole('heading', { name: 'Restore shop-db', level: 1 })).toBeVisible()
  await page.getByRole('radio', { name: /scratch/ }).check()
  await page.getByLabel('Restore suffix').fill(suffix)
  await expect(page.getByText(`restore_suffix=${suffix}`)).toBeVisible()
  await page.getByLabel('Reason').fill('UI end-to-end test: restore into scratch')
  await page.getByLabel(/to confirm/).fill('shop-db')
  const start = page.getByRole('button', { name: 'Start restore' })
  await expect(start).toBeEnabled()
  await start.click()
  await waitForRun(page, 'Restore', 'shop-db')
  await page.close()
})

test('a production restore waits for approval and a second person approves it', async () => {
  const page = await open(asRestorer, '/jobs/shop-db/restore')
  await page.getByRole('radio', { name: /production/ }).check()
  await page.getByLabel('Restore suffix').fill(`${suffix}_prod`)
  await page.getByLabel('Reason').fill('UI end-to-end test: production restore with approval')
  await page.getByLabel(/to confirm/).fill('shop-db')
  const submit = page.getByRole('button', { name: 'Request approval' })
  await expect(submit).toBeEnabled()
  await submit.click()
  await expect(page.getByText(/is waiting for approval/)).toBeVisible()
  await expect(page.getByText('Waiting for approval').first()).toBeVisible()
  await page.getByRole('link', { name: 'Open the request' }).click()
  await page.waitForURL(/\/approvals\/[^/]+$/)
  approvalPath = new URL(page.url()).pathname
  await expect(page.getByRole('button', { name: 'Approve and start restore' })).toBeDisabled()
  await page.close()

  const other = await open(asApprover, approvalPath)
  await expect(other.getByText(`${restorer.name} asked`)).toBeVisible()
  const approve = other.getByRole('button', { name: 'Approve and start restore' })
  await expect(approve).toBeEnabled()
  await approve.click()
  await expect(other.getByText(`Approved by ${approver.name}`)).toBeVisible()
  await other.getByRole('link', { name: 'Open restore run' }).click()
  await waitForRun(other, 'Restore', 'shop-db')
  await other.close()
})

test('the audit log shows what happened', async () => {
  const page = await open(asApprover, '/audit')
  await expect(page.getByRole('heading', { name: 'Audit log', level: 1 })).toBeVisible()
  const entries = page.getByRole('region', { name: 'Entries' })
  for (const action of ['Sign-in', 'Backup started', 'Restore started', 'Restore requested', 'Approval granted', 'Run finished']) {
    await expect(entries.getByRole('cell', { name: action, exact: true }).first()).toBeVisible()
  }
  await expect(entries.getByText(restorer.name).first()).toBeVisible()
  await expect(entries.getByText(approver.name).first()).toBeVisible()
  await page.close()
})

test('dark mode applies, is remembered and the CSP allows the boot script', async () => {
  const page = await asRestorer.newPage()
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy/i.test(m.text())) cspErrors.push(m.text())
  })
  await page.emulateMedia({ colorScheme: 'light' })
  await page.goto('/')
  const html = page.locator('html')
  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  await expect.poll(bg).toBe('rgb(250, 250, 250)')
  await page.getByRole('button', { name: 'Switch to dark theme' }).click()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await expect.poll(bg).toBe('rgb(0, 0, 0)')
  await page.reload()
  await expect(html).toHaveAttribute('data-theme', 'dark')
  await expect.poll(bg).toBe('rgb(0, 0, 0)')
  await expect(page.getByRole('button', { name: 'Switch to light theme' })).toBeVisible()
  await page.close()
  expect(cspErrors).toEqual([])
})
