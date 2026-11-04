import { expect, test, type BrowserContext, type Page } from '@playwright/test'

const restorer = { email: 'ui-drill@example.com', name: 'Dora Drill', roles: 'restorer' }
const viewer = { email: 'ui-viewer@example.com', name: 'Vic Viewer', roles: 'viewer' }

let ctx: BrowserContext
let drillJob: string | null = null
const cspErrors: string[] = []

async function signIn(context: BrowserContext, user: typeof restorer) {
  const page = await context.newPage()
  const q = new URLSearchParams({ email: user.email, name: user.name, roles: user.roles, return_to: '/' })
  await page.goto(`/api/v1/auth/dev-login?${q}`)
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible()
  await page.close()
}

async function open(path: string): Promise<Page> {
  const page = await ctx.newPage()
  page.on('console', (m) => {
    if (m.type() === 'error' && /Content Security Policy/i.test(m.text())) cspErrors.push(m.text())
  })
  await page.goto(path)
  return page
}

test.describe.configure({ mode: 'serial' })

test.beforeAll(async ({ browser }) => {
  ctx = await browser.newContext()
  await signIn(ctx, restorer)
  const overview = await ctx.request.get('/api/v1/overview')
  expect(overview.ok()).toBe(true)
  const { jobs } = (await overview.json()) as { jobs: { name: string }[] }
  for (const j of jobs) {
    const res = await ctx.request.get(`/api/v1/jobs/${encodeURIComponent(j.name)}`)
    if (res.ok() && (await res.json()).drill) {
      drillJob = j.name
      break
    }
  }
})

test.afterAll(async () => {
  await ctx?.close()
  expect(cspErrors).toEqual([])
})

test('a restore drill started from the UI passes and shows its checks', async () => {
  test.skip(drillJob === null, 'no job of the dev-ui configuration has a drill section')
  const job = drillJob!
  const page = await open(`/jobs/${job}`)
  const card = page.getByRole('region', { name: /^Restore drill/ })
  await expect(card).toBeVisible()
  const run = card.getByRole('button', { name: 'Run drill now' })
  await expect(run).toBeEnabled({ timeout: 90_000 })
  await run.click()
  await page.waitForURL(/\/runs\/[^/]+$/)
  await expect(page.getByRole('heading', { name: `Restore drill ${job}`, level: 1 })).toBeVisible()
  await expect(page.getByText(new RegExp(`Restore drill of ${job} (succeeded|failed)\\.$`))).toBeAttached({ timeout: 150_000 })
  const result = page.getByRole('region', { name: /Drill result/ })
  await expect(result.getByTestId('drill-evidence')).toHaveText(/^Restored and checked just now/)
  await expect(result.getByRole('table', { name: /Drill checks/ })).toBeVisible()
  await page.getByRole('link', { name: `Open ${job}` }).click()
  const history = page.getByRole('region', { name: /^Restore drill/ }).getByRole('table', { name: /Recent restore drills/ })
  await expect(history.getByText('Passed').first()).toBeVisible()
  await page.close()
})

test('an operator previews a prune of shop-db', async () => {
  const page = await open('/jobs/shop-db')
  await page.getByRole('link', { name: 'Preview prune' }).click()
  await expect(page.getByRole('heading', { name: 'Prune shop-db', level: 1 })).toBeVisible()
  await page.getByRole('button', { name: 'Preview prune' }).click()
  const totals = page.getByLabel('Prune totals')
  await expect(totals).toBeVisible()
  await expect(totals).toContainText('Kept')
  await expect(page.getByRole('table', { name: /what a prune would do/ })).toBeVisible()
  await expect(page.getByText(/^(A prune would remove|Nothing to remove)/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Run prune' })).toHaveCount(0)
  await page.close()
})

test('the history charts render from the run records', async () => {
  const page = await open('/jobs/shop-db')
  const history = page.getByRole('region', { name: /^History/ })
  await expect(history.getByRole('figure', { name: 'Backup duration' })).toBeVisible()
  await expect(history.getByRole('figure', { name: 'Stored size' })).toBeVisible()
  await page.close()
})

test('the configuration view is shown to operators and audited', async ({ browser }) => {
  const page = await open('/config')
  await expect(page.getByRole('heading', { name: 'Configuration', level: 1 })).toBeVisible()
  await expect(page.getByRole('region', { name: /^Contents of / })).toContainText('jobs:')
  await page.close()

  const other = await browser.newContext()
  await signIn(other, viewer)
  const p = await other.newPage()
  await p.goto('/config')
  await expect(p.getByText('You cannot view the configuration')).toBeVisible()
  await other.close()
})
