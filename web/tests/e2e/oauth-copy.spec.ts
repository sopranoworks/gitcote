import { test, expect, type Page } from '@playwright/test'

const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)

async function ensureAdminLoggedIn(page: Page) {
  const status = await page.request.get(`http://localhost:${PORT}/auth/status`)
  const body = await status.json()
  if (!body.users_exist) {
    await page.request.post(`http://localhost:${PORT}/auth/register`, {
      data: { email: 'admin@test.local', display_name: 'Test Admin', password: 'testpass123' },
    })
  }
  if (!body.authenticated) {
    await page.request.post(`http://localhost:${PORT}/auth/login`, {
      data: { email: 'admin@test.local', password: 'testpass123' },
    })
  }
}

test.describe('OAuth token copy', () => {
  test.beforeEach(async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await ensureAdminLoggedIn(page)
  })

  test('copy button copies self-issued token to clipboard', async ({ page }) => {
    await page.goto('/settings?item=oauth')
    await expect(page.getByText('OAuth connections')).toBeVisible({ timeout: 10000 })

    const selfSection = page.locator('[data-testid="self-issued-section"]')
    await selfSection.getByTestId('self-issue-name').fill('e2e-copy-test')
    await selfSection.getByRole('button', { name: 'Generate a token for the CLI' }).click()

    const copyBtn = page.getByRole('button', { name: 'Copy token' })
    await expect(copyBtn).toBeVisible({ timeout: 10000 })

    const tokenRow = copyBtn.locator('..')
    const tokenValue = await tokenRow.locator('code').textContent()
    expect(tokenValue).toBeTruthy()
    expect(tokenValue!.length).toBeGreaterThan(10)

    await copyBtn.click()

    await expect(copyBtn).toContainText('Copied')

    const clip = await page.evaluate(() => navigator.clipboard.readText())
    expect(clip).toBe(tokenValue)
  })
})
