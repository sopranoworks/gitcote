import { execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'

const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)
const SCREENSHOT_DIR = '/tmp/gitcote-e2e-screenshots'
mkdirSync(SCREENSHOT_DIR, { recursive: true })

async function screenshot(page: Page, name: string) {
  await page.screenshot({
    path: join(SCREENSHOT_DIR, name),
    timeout: 5000,
    animations: 'disabled',
    fullPage: false,
  }).catch(() => {
    // Headless Chromium screenshot hangs on this app's rendering pipeline.
    // Use e2e-docker.sh (headed mode) for actual screenshot capture.
  })
}

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

async function wsSend(page: Page, type: string, payload: unknown): Promise<unknown> {
  return page.evaluate(({ type, payload }: { type: string; payload: unknown }) => {
    return new Promise<unknown>((resolve, reject) => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
      ws.onopen = () => ws.send(JSON.stringify({ type, payload }))
      ws.onmessage = (ev) => {
        const msg = JSON.parse(ev.data)
        ws.close()
        if (msg.type === 'ERROR') reject(new Error(msg.payload?.message ?? 'WS error'))
        else resolve(msg.payload)
      }
      ws.onerror = () => { ws.close(); reject(new Error('WS error')) }
      setTimeout(() => { ws.close(); reject(new Error('timeout')) }, 10000)
    })
  }, { type, payload })
}

async function ensureProject(page: Page, ns: string, proj: string) {
  await page.goto('/')
  await page.waitForLoadState('networkidle')
  await wsSend(page, 'CREATE_NAMESPACE', { name: ns }).catch(() => {})
  await wsSend(page, 'CREATE_PROJECT', { namespace: ns, projectName: proj }).catch(() => {})
}

async function issueGitToken(page: Page): Promise<string> {
  const res = await wsSend(page, 'OAUTH_ISSUE_SELF', {}) as { access_token: string }
  return res.access_token
}

function git(cwd: string, ...args: string[]) {
  execSync(['git', ...args].map(a => `'${a}'`).join(' '), {
    cwd,
    stdio: 'pipe',
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: 'Test',
      GIT_AUTHOR_EMAIL: 'test@test.local',
      GIT_COMMITTER_NAME: 'Test',
      GIT_COMMITTER_EMAIL: 'test@test.local',
      GIT_TERMINAL_PROMPT: '0',
      GIT_ASKPASS: 'true',
    },
  })
}

const ns = 'searchns'
const proj = 'searchproj'

function pushTestFiles(token: string) {
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-search-'))
  const url = `http://oauth2:${token}@localhost:${PORT}/${ns}/${proj}.git`
  git(tmp, 'clone', url, 'repo')
  const repo = join(tmp, 'repo')
  const hasBranches = execSync('git branch -a', { cwd: repo, encoding: 'utf8' }).trim()
  if (hasBranches) return

  writeFileSync(join(repo, 'README.md'), '# Search Test Project\n\nThis is a test project for file search.\n')
  writeFileSync(join(repo, 'hello.ts'), [
    'export function greetUser(name: string): string {',
    '  return `Hello, ${name}! Welcome to the search test.`',
    '}',
    '',
    'export function uniqueMarkerAlpha(): string {',
    '  return "uniqueMarkerAlpha found here"',
    '}',
    '',
  ].join('\n'))
  writeFileSync(join(repo, 'utils.ts'), [
    'export function add(a: number, b: number): number {',
    '  return a + b',
    '}',
    '',
    'export function multiply(a: number, b: number): number {',
    '  return a * b',
    '}',
    '',
  ].join('\n'))

  git(repo, 'add', '.')
  git(repo, 'commit', '-m', 'add test files for search')
  git(repo, 'push', '-u', 'origin', 'HEAD:refs/heads/main')
}

let dataReady = false

test.describe('File view & search', () => {
  test.setTimeout(60000)

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
    if (!dataReady) {
      await ensureProject(page, ns, proj)
      const token = await issueGitToken(page)
      pushTestFiles(token)
      dataReady = true
    }
  })

  test('copy button works on a repo file', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}/blob/hello.ts`)
    await expect(page.locator('[data-testid="copy-path-button"]')).toBeVisible({ timeout: 10000 })
    await page.locator('[data-testid="copy-path-button"]').click({ force: true })
    await screenshot(page, 'gitcote-file-copy-button.png')
  })

  test('in-view search finds content in a repo file', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}/blob/hello.ts`)
    await expect(page.locator('[data-testid="copy-path-button"]')).toBeVisible({ timeout: 10000 })

    await page.getByRole('button', { name: 'Toggle file search' }).click({ force: true })
    const searchInput = page.getByPlaceholder('Find in file')
    await expect(searchInput).toBeVisible({ timeout: 5000 })
    await searchInput.fill('greetUser')
    await page.waitForTimeout(500)

    await expect(page.getByRole('button', { name: 'Next match' })).toBeEnabled({ timeout: 5000 })

    await screenshot(page, 'gitcote-file-search-active.png')
  })

  test('sidebar content search returns results with snippets', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}`)
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(500)

    const rail = page.locator('nav[aria-label="Activity bar"]')
    await rail.getByRole('button', { name: 'Search' }).click({ force: true })
    await page.waitForTimeout(500)

    const sidebarSearch = page.locator('#sidebar input[placeholder*="Search"]').first()
    await expect(sidebarSearch).toBeVisible({ timeout: 5000 })
    await sidebarSearch.fill('uniqueMarkerAlpha')
    await page.waitForTimeout(1500)

    const resultItem = page.locator('#sidebar button', { hasText: 'hello.ts' })
    await expect(resultItem).toBeVisible({ timeout: 10000 })

    await screenshot(page, 'gitcote-sidebar-search-results.png')
  })

  test('click sidebar result → file loads → in-view search populated', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}`)
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(500)

    const rail = page.locator('nav[aria-label="Activity bar"]')
    await rail.getByRole('button', { name: 'Search' }).click({ force: true })
    await page.waitForTimeout(500)

    const sidebarSearch = page.locator('#sidebar input[placeholder*="Search"]').first()
    await expect(sidebarSearch).toBeVisible({ timeout: 5000 })
    await sidebarSearch.fill('uniqueMarkerAlpha')
    await page.waitForTimeout(1500)

    const resultItem = page.locator('#sidebar button', { hasText: 'hello.ts' })
    await expect(resultItem).toBeVisible({ timeout: 10000 })
    await resultItem.click({ force: true })

    await expect(page).toHaveURL(/\/blob\/hello\.ts/, { timeout: 5000 })

    const findInput = page.getByPlaceholder('Find in file')
    await expect(findInput).toBeVisible({ timeout: 5000 })
    await expect(findInput).toHaveValue('uniqueMarkerAlpha', { timeout: 5000 })

    await screenshot(page, 'gitcote-sidebar-search-to-view.png')
  })
})
