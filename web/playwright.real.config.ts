import { defineConfig, devices } from '@playwright/test'

const baseURL = process.env.DEV_UI_BASE
if (!baseURL) {
  throw new Error('DEV_UI_BASE is not set: run through scripts/dev-ui.sh --run (see scripts/e2e-local.sh ui)')
}

export default defineConfig({
  testDir: './e2e/real',
  timeout: 180_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  outputDir: 'test-results/real',
  reporter: [[process.env.CI ? 'github' : 'list'], ['html', { open: 'never', outputFolder: 'playwright-report/real' }]],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
