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

async function ensureNamespace(page: Page, name: string) {
  await page.goto('/settings?item=namespaces')
  await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
  const exists = await page.locator(`[data-testid="ns-${name}"]`).count()
  if (exists === 0) {
    await page.getByRole('button', { name: '+ New namespace' }).click()
    await page.getByRole('textbox', { name: 'Namespace name' }).fill(name)
    await page.getByRole('button', { name: 'Create' }).click()
    await expect(page.locator(`[data-testid="ns-${name}"]`)).toBeVisible({ timeout: 5000 })
  }
}

test.describe('SSH key management', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('SSH Keys section visible in namespace', async ({ page }) => {
    await ensureNamespace(page, 'sshtest')
    const section = page.locator('[data-testid="ns-sshtest"]')
    await expect(section.getByRole('heading', { name: 'SSH Keys' })).toBeVisible({ timeout: 5000 })
  })

  test('generate SSH key and see public key', async ({ page }) => {
    await ensureNamespace(page, 'sshtest')

    const section = page.locator('[data-testid="ns-sshtest"]')
    await section.getByPlaceholder('Key name').fill('e2e-deploy')
    await section.getByRole('button', { name: 'Generate Key' }).click()

    await expect(page.getByText('ssh-ed25519')).toBeVisible({ timeout: 5000 })
    await expect(page.getByText('copy and add as a deploy key')).toBeVisible()
  })

  test('key appears in list after generation', async ({ page }) => {
    await ensureNamespace(page, 'sshtest')
    await expect(page.getByText('e2e-deploy')).toBeVisible({ timeout: 5000 })
  })

  test('delete SSH key', async ({ page }) => {
    await ensureNamespace(page, 'sshtest')

    const section = page.locator('[data-testid="ns-sshtest"]')
    const keyExists = await section.getByText('e2e-delete-me').count()
    if (keyExists === 0) {
      await section.getByPlaceholder('Key name').fill('e2e-delete-me')
      await section.getByRole('button', { name: 'Generate Key' }).click()
      await expect(page.getByText('ssh-ed25519')).toBeVisible({ timeout: 5000 })
      await section.getByRole('button', { name: 'Dismiss' }).click()
    }

    page.on('dialog', (dialog) => dialog.accept())
    const row = section.locator('tr', { hasText: 'e2e-delete-me' })
    await row.getByRole('button', { name: 'Delete' }).click()

    await expect(row).not.toBeVisible({ timeout: 5000 })
  })
})
