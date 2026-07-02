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

const ns = 'agenttest'
const proj = 'demo'

test.describe('Agent Settings', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, ns, proj)
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('Agent Settings page screenshot', async ({ page }) => {
    await page.goto('/settings?item=agent-settings')
    await page.waitForTimeout(1000)
    await expect(page.getByText('PR Events (Global Defaults)')).toBeVisible({ timeout: 10000 })
    await page.screenshot({ path: 'test-results/agent-settings-page.png', fullPage: false })
  })

  test('management page checkbox and button screenshot', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
    const projSections = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
    await expect(projSections.getByText('Custom agent settings')).toBeVisible({ timeout: 5000 })
    await expect(projSections.getByRole('button', { name: 'Agent settings' })).toBeVisible()
    await page.screenshot({ path: 'test-results/agent-settings-management.png', fullPage: false })
  })

  test('modal dialog screenshot', async ({ page }) => {
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
    const projSections = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
    await expect(projSections.getByText('Custom agent settings')).toBeVisible({ timeout: 5000 })

    await projSections.getByText('Custom agent settings').click()
    await page.waitForTimeout(500)

    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible({ timeout: 5000 })
    await expect(dialog.getByText('PR Events')).toBeVisible()
    await expect(dialog.getByText('Seed Events')).toBeVisible()
    await page.screenshot({ path: 'test-results/agent-settings-modal.png', fullPage: false })
  })

  test('project top page checkbox and button screenshot', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}`)
    await page.waitForTimeout(1000)
    await expect(page.getByText('Custom agent settings')).toBeVisible({ timeout: 10000 })
    await expect(page.getByRole('button', { name: 'Agent settings' })).toBeVisible()
    await page.screenshot({ path: 'test-results/agent-settings-project-top.png', fullPage: false })
  })
})
