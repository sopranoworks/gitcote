import { execSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
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

async function setPREventSettings(
  page: Page,
  ns: string,
  proj: string,
  settings: Record<string, unknown>,
) {
  await page.goto('/')
  await page.waitForTimeout(1000)
  await page.evaluate(
    ({ ns, proj, settings }) => {
      return new Promise<void>((resolve, reject) => {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
        const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
        ws.onopen = () => {
          ws.send(JSON.stringify({
            type: 'PR_EVENT_SETTINGS_SET_PROJECT',
            payload: { namespace: ns, projectName: proj, settings },
          }))
        }
        ws.onmessage = (ev) => {
          const msg = JSON.parse(ev.data)
          if (msg.type === 'PR_EVENT_SETTINGS_SET_PROJECT') { ws.close(); resolve() }
          else if (msg.type === 'ERROR') { ws.close(); reject(new Error(msg.payload?.message ?? 'WS error')) }
        }
        ws.onerror = () => { ws.close(); reject(new Error('WS connection error')) }
        setTimeout(() => { ws.close(); reject(new Error('timeout')) }, 10000)
      })
    },
    { ns, proj, settings },
  )
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
  const tmp = mkdtempSync(join(tmpdir(), 'gityard-mc-'))
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

test.describe('Manual confirm flow', () => {
  let token = ''

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, 'mca', 'demo')
    await ensureProject(page, 'mcb', 'demo')
    await ensureProject(page, 'mcc', 'demo')

    await setPREventSettings(page, 'mca', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })
    await setPREventSettings(page, 'mcb', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_rejector' },
      on_confirmed: { auto_confirm: false },
    })
    await setPREventSettings(page, 'mcc', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })

    token = await issueGitToken(page)
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('Test A: approve → CONFIRM button → merge', async ({ page }) => {
    test.setTimeout(120_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'mca', 'demo')
    pushPR(repo, 'feat/approve', 'Approve flow PR')

    await waitForPRState(page, 'mca', 'demo', 1, 'approved')

    await expect(content.getByText('Approve flow PR')).toBeVisible({ timeout: 5000 })
    await expect(content.getByRole('button', { name: 'Confirm' })).toBeVisible()
    await expect(content.locator('[data-state="approved"]')).toBeVisible()
    await page.screenshot({ path: 'test-results/test-a-approved-confirm-button.png', fullPage: false })

    await content.getByRole('button', { name: 'Confirm' }).click()

    await waitForPRState(page, 'mca', 'demo', 1, 'merged')
    await expect(content.locator('[data-state="merged"]')).toBeVisible()
    await page.screenshot({ path: 'test-results/test-a-merged.png', fullPage: false })
  })

  test('Test B: reviewer rejects → REJECTED state', async ({ page }) => {
    test.setTimeout(120_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'mcb', 'demo')
    pushPR(repo, 'feat/reject', 'Reject flow PR')

    await waitForPRState(page, 'mcb', 'demo', 1, 'rejected')

    await expect(content.getByText('Reject flow PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="rejected"]')).toBeVisible()
    await expect(content.getByRole('button', { name: 'Confirm' })).not.toBeVisible()
    await expect(content.getByRole('button', { name: 'Close' })).toBeVisible()
    await page.screenshot({ path: 'test-results/test-b-rejected.png', fullPage: false })
  })

  test('Test C: queue blocking — PR #2 reviewer not spawned while PR #1 awaits confirm', async ({ page }) => {
    test.setTimeout(180_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'mcc', 'demo')
    pushPR(repo, 'feat/first', 'First PR')

    await waitForPRState(page, 'mcc', 'demo', 1, 'approved')

    git(repo, 'checkout', 'main')
    pushPR(repo, 'feat/second', 'Second PR')

    await page.waitForTimeout(15_000)

    await page.goto('/p/mcc/demo/prs?pr=2')
    await page.waitForTimeout(2000)
    await expect(content.getByText('Second PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="open"]')).toBeVisible()
    await expect(content.getByRole('button', { name: 'Confirm' })).not.toBeVisible()

    await page.goto('/p/mcc/demo/prs?pr=1')
    await page.waitForTimeout(1500)
    await expect(content.locator('[data-state="approved"]')).toBeVisible({ timeout: 5000 })
    await expect(content.getByRole('button', { name: 'Confirm' })).toBeVisible()

    await page.screenshot({ path: 'test-results/test-c-queue-blocked.png', fullPage: false })

    await content.getByRole('button', { name: 'Confirm' }).click()
    await waitForPRState(page, 'mcc', 'demo', 1, 'merged')

    await waitForPRState(page, 'mcc', 'demo', 2, 'approved', 60_000)
    await expect(content.getByText('Second PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="approved"]')).toBeVisible()
    await page.screenshot({ path: 'test-results/test-c-queue-unblocked.png', fullPage: false })
  })
})
