import { defineConfig, devices } from '@playwright/test'

const port = 4173

export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  fullyParallel: true,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run build:mock && npm run serve:mock',
    url: `http://127.0.0.1:${port}/_shell.html`,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
  },
})
