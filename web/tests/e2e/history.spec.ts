import { execSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
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

async function issueGitToken(page: Page): Promise<string> {
  await page.goto('/')
  await page.waitForTimeout(2000)
  return page.evaluate(() => {
    return new Promise<string>((resolve, reject) => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
      ws.onopen = () => {
        ws.send(JSON.stringify({ type: 'OAUTH_ISSUE_SELF', payload: {} }))
      }
      ws.onmessage = (ev) => {
        const msg = JSON.parse(ev.data)
        if (msg.type === 'OAUTH_ISSUE_SELF') { ws.close(); resolve(msg.payload.access_token) }
        else if (msg.type === 'ERROR') { ws.close(); reject(new Error(msg.payload?.message ?? 'WS error')) }
      }
      ws.onerror = () => { ws.close(); reject(new Error('WS connection error')) }
      setTimeout(() => { ws.close(); reject(new Error('timeout')) }, 10000)
    })
  })
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

function pushTestCommits(token: string, ns: string, proj: string) {
  const tmp = mkdtempSync(join(tmpdir(), 'gityard-hist-'))
  const url = `http://oauth2:${token}@localhost:${PORT}/${ns}/${proj}.git`
  git(tmp, 'clone', url, 'repo')
  const repo = join(tmp, 'repo')

  writeFileSync(join(repo, 'hello.txt'), 'Hello, World!\n')
  git(repo, 'add', 'hello.txt')
  git(repo, 'commit', '-m', 'add hello.txt')
  git(repo, 'push', '-u', 'origin', 'HEAD:refs/heads/main')

  writeFileSync(join(repo, 'hello.txt'), 'Hello, GitYard!\nSecond line.\n')
  git(repo, 'add', 'hello.txt')
  git(repo, 'commit', '-m', 'update hello.txt with second line')
  git(repo, 'push', 'origin', 'HEAD:main')

  writeFileSync(join(repo, 'hello.txt'), 'Hello, GitYard!\nSecond line.\nThird line added.\n')
  git(repo, 'add', 'hello.txt')
  git(repo, 'commit', '-m', 'add third line to hello.txt')
  git(repo, 'push', 'origin', 'HEAD:main')
}

test.describe('History', () => {
  const ns = 'histns'
  const proj = 'histproj'

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)
    pushTestCommits(token, ns, proj)
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('history rail button opens history page', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}`)
    await expect(page.locator('text=' + proj).first()).toBeVisible({ timeout: 10000 })

    const rail = page.locator('nav[aria-label="Activity bar"]')
    await rail.getByRole('button', { name: 'History' }).click()

    await expect(page).toHaveURL(new RegExp(`/p/${ns}/${proj}/history`), { timeout: 5000 })
    await expect(page.getByText('Select a file to see its history.')).toBeVisible({ timeout: 5000 })
  })

  test('history sidebar shows commit log for file', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}/history/hello.txt`)
    await expect(page.locator('[aria-label="Commit history"]')).toBeVisible({ timeout: 10000 })

    const commits = page.locator('[aria-label="Commit history"] ul li')
    await expect(commits).toHaveCount(3, { timeout: 5000 })
    await expect(commits.first()).toContainText('add third line')

    await page.screenshot({ path: 'test-results/gityard-history-sidebar.png', fullPage: true })
  })

  test('click commit loads file at version', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}/history/hello.txt?mode=version`)

    const commits = page.locator('[aria-label="Commit history"] ul li')
    await expect(commits).toHaveCount(3, { timeout: 10000 })

    // Click the second commit ("update hello.txt with second line")
    await commits.nth(1).click()
    await expect(page).toHaveURL(/at=/, { timeout: 5000 })

    // Verify content at that version
    await expect(page.getByText('Hello, GitYard!')).toBeVisible({ timeout: 5000 })
    await expect(page.getByText('Second line.')).toBeVisible({ timeout: 5000 })
    await expect(page.getByText('Third line added.')).not.toBeVisible()

    await page.screenshot({ path: 'test-results/gityard-history-version-view.png', fullPage: true })
  })

  test('diff view shows changes between versions', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}/history/hello.txt?mode=diff`)

    const commits = page.locator('[aria-label="Commit history"] ul li')
    await expect(commits).toHaveCount(3, { timeout: 10000 })

    // Default diff should show between latest and previous commit
    const diffLines = page.locator('[data-op]')
    await expect(diffLines.first()).toBeVisible({ timeout: 5000 })

    // Should see added line
    const addedLines = page.locator('[data-op="add"]')
    await expect(addedLines.first()).toBeVisible({ timeout: 5000 })

    await page.screenshot({ path: 'test-results/gityard-history-diff-view.png', fullPage: true })
  })
})
