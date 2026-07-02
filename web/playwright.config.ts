import { defineConfig, devices } from '@playwright/test'

const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: (() => {
    const r: import('@playwright/test').ReporterDescription[] = [['list']]
    if (process.env.PLAYWRIGHT_JSON_OUTPUT_FILE) {
      r.push(['json', { outputFile: process.env.PLAYWRIGHT_JSON_OUTPUT_FILE }])
    }
    return r
  })(),
  globalSetup: './tests/e2e/global-setup.ts',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{
    name: 'chromium',
    use: {
      ...devices['Desktop Chrome'],
      launchOptions: {
        executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined,
      },
    },
  }],
})
