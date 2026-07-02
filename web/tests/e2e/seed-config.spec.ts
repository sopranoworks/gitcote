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

test.describe('Seed configuration', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('seed section visible for project', async ({ page }) => {
    await ensureProject(page, 'seedns', 'seedproj')
    const projSection = page.locator('[data-testid="proj-sections-seedns-seedproj"]')
    await expect(projSection).toBeVisible({ timeout: 5000 })
    await expect(projSection.getByText('Seed').first()).toBeVisible()
  })

  test('seed config form fields present', async ({ page }) => {
    await ensureProject(page, 'seedns', 'seedproj')
    const projSection = page.locator('[data-testid="proj-sections-seedns-seedproj"]')
    await expect(projSection).toBeVisible({ timeout: 5000 })
    await expect(projSection.locator('input[placeholder*="github"]')).toBeVisible()
    await expect(projSection.getByRole('button', { name: 'Save' })).toBeVisible()
  })

  test('save seed config', async ({ page }) => {
    await ensureProject(page, 'seedns', 'seedproj')
    const projSection = page.locator('[data-testid="proj-sections-seedns-seedproj"]')
    await expect(projSection).toBeVisible({ timeout: 5000 })

    await projSection.locator('input[placeholder*="github"]').fill('git@github.com:test/repo.git')

    const selects = projSection.locator('select')
    const modeSelect = selects.last()
    await modeSelect.selectOption('on-merge')

    await projSection.getByRole('button', { name: 'Save' }).click()

    await expect(page.getByText('Seed config saved')).toBeVisible({ timeout: 10000 })
  })

  test('sync status badge shows for project', async ({ page }) => {
    await ensureProject(page, 'seedns', 'seedproj')
    const projSection = page.locator('[data-testid="proj-sections-seedns-seedproj"]')
    await expect(projSection).toBeVisible({ timeout: 5000 })
    const dot = projSection.locator('[data-state]')
    await expect(dot.first()).toBeVisible({ timeout: 5000 })
  })
})
