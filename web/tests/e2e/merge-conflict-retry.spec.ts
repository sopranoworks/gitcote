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

async function wsRequest(page: Page, type: string, payload: Record<string, unknown>) {
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

async function setPREventSettings(page: Page, ns: string, proj: string, settings: Record<string, unknown>) {
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
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-mcr-'))
  const url = `http://oauth2:${token}@localhost:${PORT}/${ns}/${proj}.git`
  git(tmp, 'clone', url, 'repo')
  const repo = join(tmp, 'repo')
  const hasBranches = execSync('git branch -a', { cwd: repo, encoding: 'utf8' }).trim()
  if (!hasBranches) {
    execSync('echo "seed content" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'initial commit')
    git(repo, 'branch', '-M', 'main')
    git(repo, 'push', '-u', 'origin', 'main')
  }
  return repo
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

// This exercises Task A of the seed-sync/merge-conflict WebUI recovery
// directive: commit c31e6b3 claimed RetryPanel shows a "Start merge"
// button for state === 'merge_conflict', but that was only proven against
// the backend (retry_pr_agent called directly via MCP). This confirms the
// button is actually rendered, enabled, and wired to the real backend from
// the PR detail view — not just that the underlying call works.
test.describe('Merge-conflict PR retry — WebUI', () => {
  let token = ''

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, 'mcr', 'demo')
    // Deliberately no on_merge_conflict config — this project has a real
    // reviewer (so the PR gets genuinely approved) but no merger at all,
    // reproducing "merge conflict with no merger ever spawned".
    await setPREventSettings(page, 'mcr', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })
    token = await issueGitToken(page)
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('Start merge button is visible and clickable for a merge-conflict PR with no merger configured', async ({ page }) => {
    test.setTimeout(120_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'mcr', 'demo')
    git(repo, 'checkout', '-b', 'feat/conflict-source')
    execSync('echo "PR-side change" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'PR-side change')
    git(repo, 'push', '-u', 'origin', 'feat/conflict-source',
      '-o', 'pull_request.create',
      '-o', 'pull_request.title=Conflict source PR')

    // Real mock_reviewer approval — proves the PR reaches approved through
    // the genuine review flow, not a fabricated state.
    await waitForPRState(page, 'mcr', 'demo', 1, 'approved')

    // Diverge the target branch directly, after approval — the same race
    // handlePRMerge is built to detect: approval doesn't re-check the
    // target on every subsequent commit, only on merge attempt.
    git(repo, 'checkout', 'main')
    execSync('echo "main-side change" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'main-side change')
    git(repo, 'push', 'origin', 'main')

    // Trigger the merge attempt via the same PR_MERGE action the "Confirm"
    // button calls — this is what actually flips the PR to merge_conflict
    // and fires onPRMergeConflict (a no-op here since no merger is
    // configured, per the c31e6b3 fix). Setup only; the assertions below
    // are all against the real rendered UI.
    await wsRequest(page, 'PR_MERGE', { namespace: 'mcr', projectName: 'demo', number: 1 })

    await waitForPRState(page, 'mcr', 'demo', 1, 'merge_conflict')
    await expect(content.locator('[data-state="merge_conflict"]')).toBeVisible()
    await page.screenshot({ path: 'test-results/merge-conflict-start-merge-visible.png', fullPage: false })

    const startMergeBtn = content.getByRole('button', { name: 'Start merge', exact: true })
    await expect(startMergeBtn).toBeVisible({ timeout: 5000 })
    await expect(startMergeBtn).toBeEnabled()

    // Click it — with no merger configured, the real backend call must
    // reject with a clear message (not silently no-op, not crash), proving
    // the button is genuinely wired to retry_pr_agent's merger-role path
    // added in c31e6b3, not just rendered.
    await startMergeBtn.click()
    await expect(content.getByText(/no merger agent configured/i)).toBeVisible({ timeout: 10000 })
    await page.screenshot({ path: 'test-results/merge-conflict-start-merge-rejected.png', fullPage: false })
  })
})
