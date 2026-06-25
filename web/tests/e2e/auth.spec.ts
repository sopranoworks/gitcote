import { test, expect } from '@playwright/test'

const PORT = Number(process.env.GITYARD_E2E_PORT ?? 9099)

test.describe('Auth flow', () => {
  test('first-run wizard appears when no users exist', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByText('GitYard Setup')).toBeVisible()
    await expect(page.getByText('Create the administrator account')).toBeVisible()
    await expect(page.getByPlaceholder('Email')).toBeVisible()
    await expect(page.getByPlaceholder('Display name')).toBeVisible()
    await expect(page.getByPlaceholder('Password')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Create Admin' })).toBeVisible()
  })

  test('create first admin account', async ({ page }) => {
    await page.goto('/')
    await page.getByPlaceholder('Email').fill('admin@test.local')
    await page.getByPlaceholder('Display name').fill('Test Admin')
    await page.getByPlaceholder('Password').fill('testpass123')
    await page.getByRole('button', { name: 'Create Admin' }).click()

    // After creation, login screen appears (user now exists)
    await expect(page.getByText('GitYard').first()).toBeVisible({ timeout: 5000 })
  })

  test('login with email and password', async ({ page }) => {
    // Ensure admin exists (from previous test or re-create)
    const status = await page.request.get(`http://localhost:${PORT}/auth/status`)
    const body = await status.json()
    if (!body.users_exist) {
      await page.request.post(`http://localhost:${PORT}/auth/register`, {
        data: { email: 'admin@test.local', display_name: 'Test Admin', password: 'testpass123' },
      })
    }

    await page.goto('/')
    // Should show login screen
    await expect(page.getByPlaceholder('Email')).toBeVisible({ timeout: 5000 })
    await page.getByPlaceholder('Email').fill('admin@test.local')
    await page.getByPlaceholder('Password').fill('testpass123')
    await page.getByRole('button', { name: 'Sign in' }).click()

    // After login, redirects to settings/management
    await expect(page.getByText('Settings')).toBeVisible({ timeout: 10000 })
  })

  test('settings page renders after login', async ({ page }) => {
    // Login
    await page.request.post(`http://localhost:${PORT}/auth/login`, {
      data: { email: 'admin@test.local', password: 'testpass123' },
    })
    await page.goto('/settings?item=namespaces')
    await expect(page.getByText('Namespace / project management')).toBeVisible({ timeout: 10000 })
  })
})
