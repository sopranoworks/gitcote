import { test, expect } from '@playwright/test'

const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)

test.describe('Auth flow', () => {
  test('first-run wizard appears when no users exist', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Welcome to GitCote' })).toBeVisible()
    await expect(page.getByText('No users exist yet')).toBeVisible()
    await expect(page.locator('#fr-email')).toBeVisible()
    await expect(page.locator('#fr-name')).toBeVisible()
    await expect(page.locator('#fr-pw')).toBeVisible()
    await expect(page.locator('#fr-pw2')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Create administrator' })).toBeVisible()
  })

  test('create first admin account', async ({ page }) => {
    await page.goto('/')
    await page.locator('#fr-email').fill('admin@test.local')
    await page.locator('#fr-name').fill('Test Admin')
    await page.locator('#fr-pw').fill('testpass123')
    await page.locator('#fr-pw2').fill('testpass123')
    await page.getByRole('button', { name: 'Create administrator' }).click()

    await expect(page.getByRole('heading', { name: 'Welcome to GitCote' })).toBeHidden({ timeout: 10000 })
  })

  test('login with email and password', async ({ page }) => {
    const status = await page.request.get(`http://localhost:${PORT}/auth/status`)
    const body = await status.json()
    if (!body.users_exist) {
      await page.request.post(`http://localhost:${PORT}/auth/register`, {
        data: { email: 'admin@test.local', display_name: 'Test Admin', password: 'testpass123' },
      })
    }

    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Sign in to GitCote' })).toBeVisible({ timeout: 5000 })
    await page.locator('#lg-email').fill('admin@test.local')
    await page.locator('#lg-pw').fill('testpass123')
    await page.getByRole('button', { name: 'Sign in' }).click()

    await expect(page.getByText('Projects')).toBeVisible({ timeout: 10000 })
  })

  test('settings page renders after login', async ({ page }) => {
    await page.request.post(`http://localhost:${PORT}/auth/login`, {
      data: { email: 'admin@test.local', password: 'testpass123' },
    })
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
  })
})
