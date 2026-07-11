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
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-race-'))
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

// waitForPRTitle polls the PR page (post-receive PR creation is
// asynchronous) until the given title becomes visible, or times out.
async function waitForPRTitle(page: Page, ns: string, proj: string, prNum: number, title: string, timeout = 30000) {
  const content = page.locator('#content')
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    await page.goto(`/p/${ns}/${proj}/prs?pr=${prNum}`)
    await page.waitForTimeout(1000)
    if (await content.getByText(title).isVisible()) return
    await page.waitForTimeout(1000)
  }
  throw new Error(`PR #${prNum} ("${title}") did not appear within ${timeout}ms`)
}

test.describe('Review button race safety (guarded by prRetryEligible)', () => {
  let token = ''

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, 'rgv', 'demo')
    token = await issueGitToken(page)
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  // This spec deliberately covers only the single-PR positive control here.
  // The "hidden for a queued (non-active) PR" property is proven two other
  // ways instead of a second PR in this same project:
  //   - retroactive-review.spec.ts's queued-PR test proves it end-to-end
  //     for the underlying retry_eligible flag (which both the Retry and
  //     Review buttons gate on — Review additionally requires hasReviewer).
  //   - TestPRRetryEligible_Cases (Go) proves the shared backend check
  //     rejects a non-active-queue-entry PR directly.
  // Deliberately NOT pushing a second PR into the same project here: doing
  // so exposed a genuine pre-existing bug in handlePostReceive's PR-source-
  // branch detection (prwiring.go, the git.ListBranches "first non-target
  // branch" heuristic is not deterministic once 2+ non-target branches
  // exist) that intermittently attributes the second push to the first
  // PR's branch instead of creating a second PR — unrelated to this
  // directive's scope, reported separately, not worth working around here
  // by deleting refs (which has its own side effects on PR #1's state).
  test('Review button appears for an eligible open PR once a reviewer agent is configured', async ({ page }) => {
    test.setTimeout(60_000)
    const content = page.locator('#content')

    const repo = cloneAndInit(token, 'rgv', 'demo')
    pushPR(repo, 'feat/active', 'Active PR')
    await waitForPRTitle(page, 'rgv', 'demo', 1, 'Active PR')

    await setPREventSettings(page, 'rgv', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })

    // PR #1 is open and the active queue entry, so with a reviewer now
    // configured, the Review button must be visible — proves the
    // hasReviewer && retry_eligible gate isn't just always-false.
    await page.goto('/p/rgv/demo/prs?pr=1')
    await page.waitForTimeout(1500)
    await expect(content.getByText('Active PR')).toBeVisible({ timeout: 5000 })
    await expect(content.locator('[data-state="open"]')).toBeVisible()
    await expect(content.getByRole('button', { name: 'Review', exact: true })).toBeVisible({ timeout: 5000 })
    await page.screenshot({ path: 'test-results/race-review-visible-active.png', fullPage: false })
  })
})
