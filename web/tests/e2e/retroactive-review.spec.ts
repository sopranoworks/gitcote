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

async function wsRequest(
  page: Page,
  type: string,
  payload: Record<string, unknown>,
) {
  await page.goto('/')
  await page.waitForTimeout(1000)
  return page.evaluate(
    ({ type, payload }) => {
      return new Promise<unknown>((resolve, reject) => {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
        const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
        ws.onopen = () => {
          ws.send(JSON.stringify({ type, payload }))
        }
        ws.onmessage = (ev) => {
          const msg = JSON.parse(ev.data)
          if (msg.type === type) { ws.close(); resolve(msg.payload) }
          else if (msg.type === 'ERROR') { ws.close(); reject(new Error(msg.payload?.message ?? 'WS error')) }
        }
        ws.onerror = () => { ws.close(); reject(new Error('WS connection error')) }
        setTimeout(() => { ws.close(); reject(new Error('timeout')) }, 10000)
      })
    },
    { type, payload },
  )
}

async function setPREventSettings(
  page: Page,
  ns: string,
  proj: string,
  settings: Record<string, unknown>,
) {
  await wsRequest(page, 'PR_EVENT_SETTINGS_SET_PROJECT', { namespace: ns, projectName: proj, settings })
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

function cloneAndInit(token: string, ns: string, proj: string): string {
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-rar-'))
  const url = `http://oauth2:${token}@localhost:${PORT}/${ns}/${proj}.git`
  git(tmp, 'clone', url, 'repo')
  const repo = join(tmp, 'repo')
  const hasBranches = execSync('git branch -a', { cwd: repo, encoding: 'utf8' }).trim()
  if (!hasBranches) {
    execSync('echo "# Test" > README.md', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'README.md')
    git(repo, 'commit', '-m', 'initial commit')
    git(repo, 'branch', '-M', 'main')
    git(repo, 'push', '-u', 'origin', 'main')
  }
  return repo
}

function pushPR(repo: string, branch: string, title: string) {
  git(repo, 'checkout', '-b', branch)
  const filename = branch.replace(/\//g, '-') + '.txt'
  execSync(`echo "// ${title}" > '${filename}'`, { cwd: repo, shell: '/bin/bash' })
  git(repo, 'add', filename)
  git(repo, 'commit', '-m', title)
  git(repo, 'push', '-u', 'origin', branch,
    '-o', 'pull_request.create',
    '-o', `pull_request.title=${title}`,
  )
}

async function waitForPRState(page: Page, ns: string, proj: string, prNum: number, state: string, timeout = 60000) {
  const content = page.locator('#content')
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    await page.goto(`/p/${ns}/${proj}/prs?pr=${prNum}`)
    await page.waitForTimeout(1500)
    const badge = content.locator(`[data-state="${state}"]`)
    if (await badge.isVisible()) return
    await page.waitForTimeout(1500)
  }
  throw new Error(`PR #${prNum} did not reach state "${state}" within ${timeout}ms`)
}

test.describe('Retroactive review recovery', () => {
  let token = ''

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, 'rar', 'demo')
    await ensureProject(page, 'rarq', 'demo')
    // Deliberately do NOT configure on_created for either project yet —
    // matches a fresh project before an operator has done any agent
    // configuration.
    token = await issueGitToken(page)
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('Retry button appears for a never-attempted open PR, is clickable, and actually spawns a reviewer', async ({ page }) => {
    test.setTimeout(120_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'rar', 'demo')
    pushPR(repo, 'feat/no-agent', 'No agent configured PR')

    // No agent was ever configured for on_created, so the PR must sit open
    // with no crash and no interrupted state — give it a real window to
    // prove nothing happens on its own.
    await page.waitForTimeout(8000)
    await page.goto('/p/rar/demo/prs?pr=1')
    await page.waitForTimeout(1500)
    await expect(content.getByText('No agent configured PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="open"]')).toBeVisible()
    await expect(content.locator('[data-testid="interrupted-panel"]')).not.toBeVisible()
    await page.screenshot({ path: 'test-results/retroactive-no-agent-open.png', fullPage: false })

    // This PR is the active queue entry with no prior agent attempt, so
    // it's retry-eligible from the moment it's created — the same button
    // used for interrupted-PR recovery must be visible here too (backend:
    // prRetryEligible; frontend: retry_eligible on PR_GET).
    const retryPanel = content.locator('[data-testid="retry-eligible-panel"]')
    await expect(retryPanel).toBeVisible({ timeout: 5000 })
    const startReviewBtn = retryPanel.getByRole('button', { name: 'Start review' })
    await expect(startReviewBtn).toBeVisible()
    await page.screenshot({ path: 'test-results/retroactive-retry-button-visible.png', fullPage: false })

    // Operator configures a reviewer agent after the fact.
    await setPREventSettings(page, 'rar', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })
    await page.goto('/p/rar/demo/prs?pr=1')
    await page.waitForTimeout(1000)

    // Click the actual button in the UI — not a direct MCP/WS call — and
    // confirm it drives the same spawn/approval flow.
    await content.locator('[data-testid="retry-eligible-panel"]').getByRole('button', { name: 'Start review' }).click()

    await waitForPRState(page, 'rar', 'demo', 1, 'approved')
    await expect(content.locator('[data-state="approved"]')).toBeVisible()
    await page.screenshot({ path: 'test-results/retroactive-review-approved.png', fullPage: false })
  })

  test('Retry button does NOT appear for a PR queued behind another (no queue-jumping from the UI)', async ({ page }) => {
    test.setTimeout(120_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'rarq', 'demo')
    pushPR(repo, 'feat/first', 'First queued PR')
    git(repo, 'checkout', 'main')
    pushPR(repo, 'feat/second', 'Second queued PR')

    // No agent configured at all, so PR #1 (active) never leaves 'open' —
    // which keeps PR #2 queued behind it, exactly the scenario to check.
    await page.waitForTimeout(5000)

    await page.goto('/p/rarq/demo/prs?pr=1')
    await page.waitForTimeout(1500)
    await expect(content.getByText('First queued PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="open"]')).toBeVisible()
    await expect(content.locator('[data-testid="retry-eligible-panel"]')).toBeVisible({ timeout: 5000 })

    await page.goto('/p/rarq/demo/prs?pr=2')
    await page.waitForTimeout(1500)
    await expect(content.getByText('Second queued PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="open"]')).toBeVisible()
    // PR #2 is open too, but it is NOT the active queue entry — the Retry
    // button must not appear, or an operator could jump it ahead of PR #1.
    await expect(content.locator('[data-testid="retry-eligible-panel"]')).not.toBeVisible()
    await page.screenshot({ path: 'test-results/retroactive-queued-no-retry-button.png', fullPage: false })
  })
})
