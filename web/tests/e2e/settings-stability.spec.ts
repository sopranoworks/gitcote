import { test, expect, type Page } from '@playwright/test'

const PORT = Number(process.env.GITYARD_E2E_PORT ?? 9099)

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

test.describe('Settings stability', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('resume banner only on namespace/project management', async ({ page }) => {
    // Navigate to My Account — banner should NOT be visible
    await page.goto('/settings?item=account')
    await expect(page.getByText('My Account')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Seed push is paused')).not.toBeVisible({ timeout: 2000 })

    // Navigate to Namespace/project management — banner should be visible
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Seed push is paused')).toBeVisible({ timeout: 5000 })
  })

  test('OAuth connections page shows management when OAuth enabled', async ({ page }) => {
    await page.goto('/settings?item=oauth')
    await expect(page.getByText('OAuth connections')).toBeVisible({ timeout: 10000 })
    // Should NOT show "not enabled" — OAuth is enabled in test config
    await expect(page.getByText('not enabled')).not.toBeVisible({ timeout: 3000 })
  })

  test('SSH Keys page renders correctly for super-user', async ({ page }) => {
    await page.goto('/settings?item=sshkeys')
    await expect(page.getByRole('heading', { name: 'SSH Keys' })).toBeVisible({ timeout: 10000 })
    // Should show key management, not denied message
    await expect(page.getByText('namespace level')).toBeVisible()
    await expect(page.getByText('do not have permission')).not.toBeVisible({ timeout: 2000 })
  })

  test('cycling through all settings items keeps sidebar intact', async ({ page }) => {
    const items = ['account', 'users', 'oauth', 'namespaces', 'sshkeys']

    for (const item of items) {
      await page.goto(`/settings?item=${item}`)
      await page.waitForTimeout(500)

      // Sidebar should show all settings items (not just My Account)
      const sidebar = page.locator('nav[aria-label="Settings items"]')
      await expect(sidebar).toBeVisible({ timeout: 5000 })
      await expect(sidebar.getByText('My Account')).toBeVisible()
      await expect(sidebar.getByText('User management')).toBeVisible()
      await expect(sidebar.getByText('Namespace / project management')).toBeVisible()
      await expect(sidebar.getByText('SSH Keys')).toBeVisible()
    }
  })

  test('SSH Keys then other item — sidebar stays intact', async ({ page }) => {
    // Go to SSH Keys first
    await page.goto('/settings?item=sshkeys')
    await expect(page.getByRole('heading', { name: 'SSH Keys' })).toBeVisible({ timeout: 10000 })

    // Go to User management
    const sidebar = page.locator('nav[aria-label="Settings items"]')
    await sidebar.getByText('User management').click()
    await expect(page.getByRole('heading', { name: 'User management' })).toBeVisible({ timeout: 5000 })

    // All sidebar items should still be visible
    await expect(sidebar.getByText('My Account')).toBeVisible()
    await expect(sidebar.getByText('OAuth connections')).toBeVisible()
    await expect(sidebar.getByText('Namespace / project management')).toBeVisible()
    await expect(sidebar.getByText('SSH Keys')).toBeVisible()

    // Go back to SSH Keys
    await sidebar.getByText('SSH Keys').click()
    await expect(page.getByRole('heading', { name: 'SSH Keys' })).toBeVisible({ timeout: 5000 })
  })
})
