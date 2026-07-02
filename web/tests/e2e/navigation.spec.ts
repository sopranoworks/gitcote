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

async function ensureProject(page: Page, ns: string, proj: string) {
  await page.goto('/settings?item=namespaces')
  await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
  if ((await page.locator(`[data-testid="ns-${ns}"]`).count()) === 0) {
    await page.getByRole('button', { name: '+ New namespace' }).click()
    await page.getByRole('textbox', { name: 'Namespace name' }).fill(ns)
    await page.getByRole('button', { name: 'Create' }).click()
    await expect(page.locator(`[data-testid="ns-${ns}"]`)).toBeVisible({ timeout: 5000 })
  }
  const nsBlock = page.locator(`[data-testid="ns-${ns}"]`)
  if ((await nsBlock.getByText(proj).count()) === 0) {
    await nsBlock.getByRole('button', { name: '+ Add project' }).click()
    await page.getByRole('textbox', { name: 'Project name' }).fill(proj)
    await page.getByRole('button', { name: 'Create' }).click()
    await expect(nsBlock.getByText(proj)).toBeVisible({ timeout: 5000 })
  }
}

test.describe('Navigation', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('project list → Settings via gear icon', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 })
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(page).toHaveURL(/\/settings/)
    await expect(page.getByText('Settings', { exact: true }).first()).toBeVisible({ timeout: 5000 })
  })

  test('Settings → project list via brand click', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
    await page.getByRole('link', { name: 'All projects' }).click()
    await expect(page).toHaveURL('/')
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 5000 })
  })

  test('project list → project → Settings → project list', async ({ page }) => {
    await ensureProject(page, 'navns', 'navproj')
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 })

    // Click project card
    await page.locator('main a', { hasText: 'navproj' }).first().click()
    await expect(page).toHaveURL(/\/p\/navns\/navproj/, { timeout: 5000 })

    // Click Settings gear in rail
    const rail = page.locator('nav[aria-label="Activity bar"]')
    await rail.getByRole('button', { name: 'Settings' }).click()
    await expect(page).toHaveURL(/\/settings/)

    // Click brand → back to project list
    await page.getByRole('link', { name: 'All projects' }).click()
    await expect(page).toHaveURL('/')
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 5000 })
  })

  test('Settings gear icon toggles sidebar when already on settings', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })

    // Sidebar should be visible (Settings items list)
    const sidebar = page.locator('#sidebar')
    await expect(sidebar).toBeVisible()

    // Click gear again → sidebar closes
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(sidebar).not.toBeVisible({ timeout: 3000 })

    // Click gear again → sidebar opens
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(sidebar).toBeVisible({ timeout: 3000 })
  })

  test('explorer/search/history disabled on project list', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible({ timeout: 10000 })

    const rail = page.locator('nav[aria-label="Activity bar"]')
    await expect(rail.getByRole('button', { name: 'Explorer' })).toBeDisabled()
    await expect(rail.getByRole('button', { name: 'Search' })).toBeDisabled()
    await expect(rail.getByRole('button', { name: 'History' })).toBeDisabled()
    await expect(rail.getByRole('button', { name: 'Settings' })).toBeEnabled()
  })

  test('all rail icons enabled on project page', async ({ page }) => {
    await ensureProject(page, 'navns', 'navproj')
    await page.goto('/p/navns/navproj')
    await expect(page.locator('text=navproj').first()).toBeVisible({ timeout: 10000 })

    const rail = page.locator('nav[aria-label="Activity bar"]')
    await expect(rail.getByRole('button', { name: 'Explorer' })).toBeEnabled()
    await expect(rail.getByRole('button', { name: 'Search' })).toBeEnabled()
    await expect(rail.getByRole('button', { name: 'History' })).toBeEnabled()
    await expect(rail.getByRole('button', { name: 'Settings' })).toBeEnabled()
  })
})
