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

test.describe('Resume banner', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('resume banner visible for super-user', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Seed push is paused after server restart')).toBeVisible({ timeout: 5000 })
  })

  test('resume banner has password input and button', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Seed push is paused')).toBeVisible({ timeout: 10000 })
    const banner = page.locator('text=Seed push is paused').locator('..')
    await expect(banner.locator('input[type="email"]')).toBeVisible()
    await expect(banner.locator('input[type="password"]')).toBeVisible()
    await expect(banner.getByRole('button', { name: 'Resume' })).toBeVisible()
  })

  test('resume with valid credentials dismisses banner', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Seed push is paused')).toBeVisible({ timeout: 10000 })

    const banner = page.locator('text=Seed push is paused').locator('..')
    await banner.locator('input[type="email"]').fill('admin@test.local')
    await banner.locator('input[type="password"]').fill('testpass123')
    await banner.getByRole('button', { name: 'Resume' }).click()

    await expect(page.getByText('Seed push is paused')).not.toBeVisible({ timeout: 10000 })
  })
})
