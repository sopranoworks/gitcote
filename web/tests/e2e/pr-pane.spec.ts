import { execSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
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

async function issueGitToken(page: Page): Promise<string> {
  await page.goto('/')
  await page.waitForTimeout(2000)
  const token = await page.evaluate(() => {
    return new Promise<string>((resolve, reject) => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
      ws.onopen = () => {
        ws.send(JSON.stringify({ type: 'OAUTH_ISSUE_SELF', payload: {} }))
      }
      ws.onmessage = (ev) => {
        const msg = JSON.parse(ev.data)
        if (msg.type === 'OAUTH_ISSUE_SELF') {
          ws.close()
          resolve(msg.payload.access_token)
        } else if (msg.type === 'ERROR') {
          ws.close()
          reject(new Error(msg.payload?.message ?? 'WS error'))
        }
      }
      ws.onerror = () => { ws.close(); reject(new Error('WS connection error')) }
      setTimeout(() => { ws.close(); reject(new Error('timeout')) }, 10000)
    })
  })
  return token
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

function createPR(token: string, ns: string, proj: string, branch: string, title: string, description: string) {
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-pr-'))
  const url = `http://oauth2:${token}@localhost:${PORT}/${ns}/${proj}.git`
  git(tmp, 'clone', url, 'repo')
  const repo = join(tmp, 'repo')

  const hasBranches = execSync('git branch -a', { cwd: repo, encoding: 'utf8' }).trim()
  if (!hasBranches) {
    execSync('echo "# Test" > README.md', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'README.md')
    git(repo, 'commit', '-m', 'initial commit')
    git(repo, 'push', '-u', 'origin', 'HEAD:refs/heads/main')
  }

  git(repo, 'checkout', '-b', branch)
  execSync(`echo "// ${title}" >> feature.txt`, { cwd: repo, shell: '/bin/bash' })
  git(repo, 'add', 'feature.txt')
  git(repo, 'commit', '-m', title)
  git(repo, 'push', '-u', 'origin', branch,
    '-o', 'pull_request.create',
    '-o', `pull_request.title=${title}`,
    '-o', `pull_request.description=${description}`,
  )
}

test.describe('PR Pane', () => {
  const ns = 'prpane'
  const proj = 'demo'

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)
    createPR(token, ns, proj, 'feat/auth', 'Add authentication', 'Implements **OAuth2** login flow.\\n\\n- Token exchange\\n- Refresh tokens\\n- Session management')
    createPR(token, ns, proj, 'feat/search', 'Implement search API', 'Full-text search across files.')
    createPR(token, ns, proj, 'fix/typo', 'Fix typo in README', 'Small fix.')
    await page.close()
  })

  test('PR tree view screenshot', async ({ page }) => {
    await ensureAdminLoggedIn(page)
    await page.goto(`/p/${ns}/${proj}/prs`)
    await page.waitForTimeout(2000)

    const rail = page.locator('nav[aria-label="Activity bar"]')
    await expect(rail.getByRole('button', { name: 'Pull Requests' })).toBeVisible({ timeout: 5000 })

    const sidebar = page.locator('#sidebar')
    await expect(sidebar).toBeVisible({ timeout: 5000 })
    // beforeAll creates 3 distinct PRs (feat/auth, feat/search, fix/typo),
    // so more than one "#N" entry is expected here — just confirm at least
    // one is visible rather than requiring an exact single match.
    await expect(sidebar.getByText('#').first()).toBeVisible({ timeout: 5000 })

    await page.screenshot({ path: 'test-results/pr-pane-tree-view.png', fullPage: false })
  })

  test('PR detail view screenshot', async ({ page }) => {
    await ensureAdminLoggedIn(page)
    await page.goto(`/p/${ns}/${proj}/prs`)
    await page.waitForTimeout(2000)

    const sidebar = page.locator('#sidebar')
    await expect(sidebar.getByText('#').first()).toBeVisible({ timeout: 5000 })

    await sidebar.getByText('Add authentication').click()
    await page.waitForTimeout(1000)

    const content = page.locator('#content')
    await expect(content.getByText('Add authentication')).toBeVisible({ timeout: 5000 })

    await page.screenshot({ path: 'test-results/pr-pane-detail-view.png', fullPage: false })
  })
})
