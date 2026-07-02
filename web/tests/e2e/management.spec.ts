import { test, expect, type Page } from '@playwright/test'

const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)

async function loginAsAdmin(page: Page) {
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

test.describe('Namespace/project management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
  })

  test('namespace list renders', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
  })

  test('create namespace and project', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })

    // Create namespace
    await page.getByRole('button', { name: '+ New namespace' }).click()
    await page.getByRole('textbox', { name: 'Namespace name' }).fill('testns')
    await page.getByRole('button', { name: 'Create' }).click()
    await expect(page.locator('[data-testid="ns-testns"]')).toBeVisible({ timeout: 5000 })

    // Create project in namespace
    const nsBlock = page.locator('[data-testid="ns-testns"]')
    await nsBlock.getByRole('button', { name: '+ Add project' }).click()
    await page.getByRole('textbox', { name: 'Project name' }).fill('myrepo')
    await page.getByRole('button', { name: 'Create' }).click()
    await expect(page.getByRole('cell', { name: 'myrepo' })).toBeVisible({ timeout: 5000 })
  })
})
