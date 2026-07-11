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

// Pushes a source branch and a diverging main, reproducing a genuine
// merge-conflict PR the same way production does: real reviewer approval,
// then the target diverges after approval so the merge attempt itself
// (not PR creation) is what discovers the conflict.
function pushMergeConflictSource(repo: string, branch: string, title: string) {
  git(repo, 'checkout', '-b', branch)
  execSync('echo "PR-side change" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
  git(repo, 'add', 'conflict.txt')
  git(repo, 'commit', '-m', title)
  git(repo, 'push', '-u', 'origin', branch,
    '-o', 'pull_request.create',
    '-o', `pull_request.title=${title}`)
}

function divergeMain(repo: string, content: string) {
  git(repo, 'checkout', 'main')
  execSync(`echo "${content}" > conflict.txt`, { cwd: repo, shell: '/bin/bash' })
  git(repo, 'add', 'conflict.txt')
  git(repo, 'commit', '-m', content)
  git(repo, 'push', 'origin', 'main')
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
    pushMergeConflictSource(repo, 'feat/conflict-source', 'Conflict source PR')

    // Real mock_reviewer approval — proves the PR reaches approved through
    // the genuine review flow, not a fabricated state.
    await waitForPRState(page, 'mcr', 'demo', 1, 'approved')

    // Diverge the target branch directly, after approval — the same race
    // handlePRMerge is built to detect: approval doesn't re-check the
    // target on every subsequent commit, only on merge attempt.
    divergeMain(repo, 'main-side change')

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

  // Task A, directive 2026-07-11-verify-ui-concurrency-and-disambiguation:
  // a rapid double-click must never crash the page or leave it in a
  // confusing state (e.g. two stacked/contradictory messages) — whether
  // the frontend's busy-disable or the backend's acquirePRAgentLock is
  // what actually prevents the second call from doing anything, the
  // observable result must be clean.
  test('rapid double-click on Start merge does not crash or leave a confusing state', async ({ page }) => {
    test.setTimeout(120_000)
    const content = page.locator('#content')

    // Own project — a merge-conflict PR left unresolved (as in the previous
    // test) would otherwise occupy the FIFO queue slot forever and block a
    // second PR in the same project from ever reaching "approved".
    await ensureProject(page, 'mcrdbl', 'demo')
    await setPREventSettings(page, 'mcrdbl', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })
    const dblToken = await issueGitToken(page)

    const repo = cloneAndInit(dblToken, 'mcrdbl', 'demo')
    pushMergeConflictSource(repo, 'feat/dblclick-source', 'Double-click conflict PR')
    await waitForPRState(page, 'mcrdbl', 'demo', 1, 'approved')
    divergeMain(repo, 'main-side change (dblclick)')
    await wsRequest(page, 'PR_MERGE', { namespace: 'mcrdbl', projectName: 'demo', number: 1 })
    await waitForPRState(page, 'mcrdbl', 'demo', 1, 'merge_conflict')

    const startMergeBtn = content.getByRole('button', { name: 'Start merge', exact: true })
    await expect(startMergeBtn).toBeVisible({ timeout: 5000 })

    // Fire two clicks as close together as Playwright allows. allSettled
    // plus a short per-click timeout means a stability/detached-element
    // outcome from the loser (e.g. if a re-render swaps the button) is
    // treated as an acceptable settle, not a hang until the test timeout.
    await Promise.allSettled([
      startMergeBtn.click({ timeout: 5000 }),
      startMergeBtn.click({ force: true, timeout: 5000 }),
    ])

    // Whichever of the two "won" (frontend disable, or backend lock), the
    // settled page must show exactly one rejection message, not a pile of
    // duplicated/contradictory ones, and the page must still be
    // responsive (not stuck mid-request forever).
    const errorText = page.getByText(/no merger agent configured|already in progress/i)
    await expect(errorText).toBeVisible({ timeout: 10000 })
    const errorCount = await errorText.count()
    if (errorCount > 1) {
      throw new Error(`expected at most one error message after a rapid double-click, found ${errorCount}`)
    }
    // The panel must still be usable afterward — not stuck disabled.
    await expect(startMergeBtn).toBeEnabled({ timeout: 10000 })
    await page.screenshot({ path: 'test-results/merge-conflict-dblclick-settled.png', fullPage: false })
  })

  // Task A, multi-session: N independent connections (standing in for N
  // browser tabs/sessions — each opens its own real WebSocket exactly as
  // the app's wsClient does) all fire retry_pr_agent for the SAME
  // merge-conflict PR at effectively the same instant. No merger is
  // configured, so every call is ultimately rejected either way — but
  // that's exactly what makes this a clean concurrency probe: the only
  // question is HOW each call is rejected. acquirePRAgentLock's critical
  // section on this rejection path is short (released synchronously, not
  // held through a spawn), so depending on arrival order a late call can
  // legitimately see either "an agent operation is already in progress"
  // (genuinely raced against the winner) or "no merger agent configured"
  // (arrived after the winner already finished and released the lock) —
  // both are correct, clean outcomes. What must NEVER happen: a crash, an
  // unexpected error, or more than one call actually proceeding to spawn.
  test('multiple concurrent sessions racing Start merge: no crash, no double-spawn, only clean rejections', async ({ page }) => {
    test.setTimeout(120_000)
    await ensureProject(page, 'mcrace', 'demo')
    await setPREventSettings(page, 'mcrace', 'demo', {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
    })
    const raceToken = await issueGitToken(page)

    const repo = cloneAndInit(raceToken, 'mcrace', 'demo')
    pushMergeConflictSource(repo, 'feat/race-source', 'Race conflict PR')
    await waitForPRState(page, 'mcrace', 'demo', 1, 'approved')
    divergeMain(repo, 'main-side change (race)')
    await wsRequest(page, 'PR_MERGE', { namespace: 'mcrace', projectName: 'demo', number: 1 })
    await waitForPRState(page, 'mcrace', 'demo', 1, 'merge_conflict')

    const outcomes = await page.evaluate(
      ({ ns, proj, n }) => {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
        function fire(): Promise<{ ok: boolean; message: string }> {
          return new Promise((resolve) => {
            const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
            ws.onopen = () => {
              ws.send(JSON.stringify({
                type: 'PR_RETRY_AGENT',
                payload: { namespace: ns, projectName: proj, number: 1 },
              }))
            }
            ws.onmessage = (ev) => {
              const msg = JSON.parse(ev.data)
              ws.close()
              if (msg.type === 'ERROR') resolve({ ok: false, message: msg.payload?.message ?? '' })
              else resolve({ ok: true, message: msg.payload?.message ?? '' })
            }
            ws.onerror = () => resolve({ ok: false, message: 'ws error' })
            setTimeout(() => { ws.close(); resolve({ ok: false, message: 'timeout' }) }, 15000)
          })
        }
        // Open all sockets first so only the .send() calls race, not
        // connection setup.
        const sockets: Promise<{ ok: boolean; message: string }>[] = []
        for (let i = 0; i < n; i++) sockets.push(fire())
        return Promise.all(sockets)
      },
      { ns: 'mcrace', proj: 'demo', n: 6 },
    )

    const succeeded = outcomes.filter(o => o.ok)
    const cleanRejection = /already in progress|no merger agent configured/i
    const unexpected = outcomes.filter(o => !o.ok && !cleanRejection.test(o.message))

    if (unexpected.length > 0) {
      throw new Error(`unexpected non-clean rejection(s): ${JSON.stringify(unexpected)}`)
    }
    // With no merger configured, nothing should ever actually succeed —
    // proving concurrency doesn't accidentally let a spawn slip through
    // that the AgentEnabled=false check would otherwise have blocked.
    if (succeeded.length > 0) {
      throw new Error(`expected no call to succeed (no merger configured), got ${succeeded.length}: ${JSON.stringify(outcomes)}`)
    }
    if (outcomes.length !== 6) {
      throw new Error(`expected 6 responses, got ${outcomes.length}`)
    }
  })
})
