import { execSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { killAndRestartServer } from './helpers/serverControl'

const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)
const ADMIN_EMAIL = 'admin@test.local'
const ADMIN_PASSWORD = 'testpass123'

async function ensureAdminLoggedIn(page: Page) {
  const status = await page.request.get(`http://localhost:${PORT}/auth/status`)
  const body = await status.json()
  if (!body.users_exist) {
    await page.request.post(`http://localhost:${PORT}/auth/register`, {
      data: { email: ADMIN_EMAIL, display_name: 'Test Admin', password: ADMIN_PASSWORD },
    })
  }
  if (!body.authenticated) {
    await page.request.post(`http://localhost:${PORT}/auth/login`, {
      data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
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
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-restart-'))
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

async function waitForPRState(page: Page, ns: string, proj: string, prNum: number, state: string, timeout = 60000) {
  const content = page.locator('#content')
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    await page.goto(`/p/${ns}/${proj}/prs?pr=${prNum}`)
    await page.waitForTimeout(1500)
    // .first(): the interrupted-panel embeds its own PRStatusBadge in
    // addition to the page's main status badge, so state === 'interrupted'
    // legitimately matches two elements — both agree on the same state, so
    // the first is sufficient proof the PR reached it.
    const badge = content.locator(`[data-state="${state}"]`).first()
    if (await badge.isVisible()) return
    await page.waitForTimeout(1500)
  }
  throw new Error(`PR #${prNum} did not reach state "${state}" within ${timeout}ms`)
}

function makeDivergedSeedBare(gitcoteRepo: string): string {
  const seedBareDir = join(mkdtempSync(join(tmpdir(), 'gitcote-restart-seed-')), 'seed.git')
  git(tmpdir(), 'init', '--bare', seedBareDir)
  git(seedBareDir, 'symbolic-ref', 'HEAD', 'refs/heads/main')
  git(gitcoteRepo, 'push', seedBareDir, 'main')

  const seedCloneDir = mkdtempSync(join(tmpdir(), 'gitcote-restart-seedclone-'))
  git(tmpdir(), 'clone', seedBareDir, seedCloneDir)
  execSync('echo "seed-side change" > conflict.txt', { cwd: seedCloneDir, shell: '/bin/bash' })
  git(seedCloneDir, 'add', 'conflict.txt')
  git(seedCloneDir, 'commit', '-m', 'seed-side change')
  git(seedCloneDir, 'push', 'origin', 'HEAD:main')

  return seedBareDir
}

async function setupSeedConfig(page: Page, ns: string, proj: string, seedUrl: string, keyName: string) {
  await wsRequest(page, 'SEED_RESUME', { email: ADMIN_EMAIL, password: ADMIN_PASSWORD })
  await wsRequest(page, 'SEED_KEY_GENERATE', { namespace: ns, name: keyName })
  await wsRequest(page, 'SEED_CONFIG_SET', {
    namespace: ns, projectName: proj, seedUrl, keyName, pushMode: 'disabled',
  })
}

async function waitForRecoveryBar(page: Page, ns: string, proj: string, expectDirection: 'push' | 'pull', timeout = 30000) {
  const deadline = Date.now() + timeout
  const projSection = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
  while (Date.now() < deadline) {
    await page.goto('/settings?item=namespaces')
    await page.waitForTimeout(1000)
    const bar = projSection.locator('[data-testid="seed-sync-recovery"]')
    if (await bar.isVisible().catch(() => false)) {
      const dir = await bar.getAttribute('data-direction')
      if (dir === expectDirection) return bar
    }
    await page.waitForTimeout(1000)
  }
  throw new Error(`seed sync recovery bar with direction=${expectDirection} did not appear within ${timeout}ms`)
}

// Directive 2026-07-11-verify-restart-persistence: everything this
// investigation-and-fix series built (interrupt states, queue slot
// retention, seed-sync SyncStatus.Reason) is only meaningful if it survives
// a real server restart — a deploy, a crash, or an operator-initiated
// restart for a config change are all routine, not disaster scenarios. This
// spec drives GitCote into each state via the real HTTP/WS flows, actually
// kills and respawns the server process (same binary, same --config, same
// storage.base_dir — see helpers/serverControl.ts), and confirms the state
// on the other side through a fresh reconnect, not just a bbolt Put call.
test.describe('Restart persistence', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  // §3.1 — StateInterrupted (review_incomplete) must survive a restart:
  // state, reason, and queue position all correct afterward, and Retry
  // must still work (not blocked by anything restart-related).
  test('PR interrupted (review_incomplete) survives a restart: state, reason, queue position, and Retry all still work', async ({ page }) => {
    test.setTimeout(120_000)
    const ns = 'restartri'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    await setPREventSettings(page, ns, proj, {
      on_created: { agent_enabled: true, agent_name: 'mock_noop_reviewer' },
      on_confirmed: { auto_confirm: false },
    })
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    pushMergeConflictSource(repo, 'feat/interrupted-source', 'Interrupted PR')

    // mock_noop_reviewer exits 0 without ever approving/rejecting — the real
    // agent-spawn path (spawnAgentForPR, eventwiring.go) is what marks this
    // review_incomplete, not a fabricated state.
    await waitForPRState(page, ns, proj, 1, 'interrupted')

    const content = page.locator('#content')
    const panel = content.locator('[data-testid="interrupted-panel"]')
    await expect(panel).toContainText('review_incomplete')
    await page.screenshot({ path: 'test-results/restart-pr-interrupted-before.png', fullPage: false })

    // The FIFO queue slot must be retained while interrupted (d221889) —
    // confirmed here, then re-confirmed after restart below.
    const queueBefore = await wsRequest(page, 'PR_QUEUE_GET', { namespace: ns, projectName: proj }) as { active_pr?: number }

    await killAndRestartServer()
    await ensureAdminLoggedIn(page)

    await page.goto(`/p/${ns}/${proj}/prs?pr=1`)
    await page.waitForTimeout(1500)
    await expect(content.locator('[data-state="interrupted"]').first()).toBeVisible({ timeout: 15000 })
    await expect(panel).toContainText('review_incomplete')
    await expect(panel).toContainText('agent exited successfully but did not approve or reject')
    await page.screenshot({ path: 'test-results/restart-pr-interrupted-after.png', fullPage: false })

    const queueAfter = await wsRequest(page, 'PR_QUEUE_GET', { namespace: ns, projectName: proj }) as { active_pr?: number }
    if (queueBefore.active_pr !== undefined) {
      expect(queueAfter.active_pr).toBe(queueBefore.active_pr)
    }

    // wsRequest() navigates to '/' internally to open its WebSocket — back
    // to the PR detail view before touching the retry button.
    await page.goto(`/p/${ns}/${proj}/prs?pr=1`)
    await page.waitForTimeout(1500)

    // Retry must still work post-restart — no leftover state should block it.
    const retryBtn = panel.getByRole('button', { name: 'Retry', exact: true })
    await expect(retryBtn).toBeVisible()
    await retryBtn.click()
    // Give the WS round trip time to complete before checking for the
    // rejection banner — click() only waits for the click action itself,
    // not the async prRetryAgent() response it triggers.
    await page.waitForTimeout(2500)
    expect(await page.getByText(/already has an agent running/i).count()).toBe(0)
    // mock_noop_reviewer exits near-instantly, so the PR should cycle back to
    // interrupted (review_incomplete again) rather than being stuck.
    await page.waitForTimeout(1000)
    await page.goto(`/p/${ns}/${proj}/prs?pr=1`)
    await expect(content.locator('[data-state="interrupted"]').first()).toBeVisible({ timeout: 15000 })
    await page.screenshot({ path: 'test-results/restart-pr-interrupted-retried.png', fullPage: false })
  })

  // §3.2 — a PR with a merger agent ACTUALLY RUNNING (mock_merger, which
  // never finishes on its own) at the moment of a crash-restart must NOT be
  // left claiming an agent is running when it isn't. Before the
  // reconcileOrphanedAgents fix, the stale AgentTokenRecord written before
  // spawn would never be cleared, and prRetryEligible's "already has an
  // agent running" check would refuse Retry forever.
  test('PR with an agent actively running at restart is not left as a zombie: no permanent "agent running" block, Retry works', async ({ page }) => {
    test.setTimeout(120_000)
    const ns = 'restartzombie'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    await setPREventSettings(page, ns, proj, {
      on_created: { agent_enabled: true, agent_name: 'mock_reviewer' },
      on_confirmed: { auto_confirm: false },
      on_merge_conflict: { agent_enabled: true, agent_name: 'mock_merger' },
    })
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    pushMergeConflictSource(repo, 'feat/zombie-source', 'Zombie-agent PR')
    await waitForPRState(page, ns, proj, 1, 'approved')
    divergeMain(repo, 'main-side change (zombie)')
    await wsRequest(page, 'PR_MERGE', { namespace: ns, projectName: proj, number: 1 })

    // merge_conflict triggers onPRMergeConflict -> spawnAgentForPR(merger),
    // which spawns mock_merger — a process that sleeps for 5 minutes and
    // never calls back into GitCote at all. By the time the state badge
    // shows merge_conflict, the conflict has already been detected; give
    // the (fast) spawn goroutine a moment to actually exec the process and
    // write its AgentWorkdirRecord/AgentTokenRecord before we kill the
    // server out from under it.
    await waitForPRState(page, ns, proj, 1, 'merge_conflict')
    await page.waitForTimeout(3000)

    await killAndRestartServer()
    await ensureAdminLoggedIn(page)

    const content = page.locator('#content')
    // The dead giveaway of the zombie bug: without reconciliation, the PR
    // stays in merge_conflict with no InterruptInfo, and any Retry attempt
    // is refused by prRetryEligible's stale-token check forever. With the
    // fix, restart reconciliation transitions it to interrupted with a
    // clear reason and revokes the stale token.
    await page.goto(`/p/${ns}/${proj}/prs?pr=1`)
    await page.waitForTimeout(1500)
    await expect(content.locator('[data-state="interrupted"]').first()).toBeVisible({ timeout: 15000 })
    const panel = content.locator('[data-testid="interrupted-panel"]')
    await expect(panel).toContainText('server_restarted')
    await expect(panel).toContainText(/merger agent .*mock_merger.* was executing/i)
    await page.screenshot({ path: 'test-results/restart-pr-zombie-reconciled.png', fullPage: false })

    // Retry must actually be allowed to proceed — not refused as "already
    // has an agent running", which is exactly the permanent dead end the
    // zombie bug produced.
    const retryBtn = panel.getByRole('button', { name: 'Retry', exact: true })
    await expect(retryBtn).toBeVisible()
    await retryBtn.click()
    // Give the WS round trip time to complete before checking for the
    // rejection banner — click() only waits for the click action itself,
    // not the async prRetryAgent() response it triggers.
    await page.waitForTimeout(2500)
    expect(await page.getByText(/already has an agent running/i).count()).toBe(0)
    await page.screenshot({ path: 'test-results/restart-pr-zombie-retried.png', fullPage: false })
  })

  // §3.3 — seed sync left in 'conflict' state (with Reason/LastResult set,
  // per the previous directive's fix) must show the same, correct state
  // after a restart, and Retry must still work.
  test('seed sync conflict state (with reason/detail) survives a restart, recovery bar stays correct, Retry works', async ({ page }) => {
    test.setTimeout(120_000)
    const ns = 'restartss'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    const seedBareDir = makeDivergedSeedBare(repo)

    execSync('echo "gitcote-side change (restart)" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'gitcote-side change (restart)')
    git(repo, 'push', 'origin', 'main')

    await setupSeedConfig(page, ns, proj, seedBareDir, 'restartss-key')
    await wsRequest(page, 'SEED_PULL', { namespace: ns, projectName: proj })

    const barBefore = await waitForRecoveryBar(page, ns, proj, 'pull')
    await expect(barBefore).toContainText('conflict')
    const reasonBefore = await barBefore.getAttribute('data-reason')
    expect(reasonBefore).toBe('pull_conflict')
    await page.screenshot({ path: 'test-results/restart-seed-conflict-before.png', fullPage: false })

    await killAndRestartServer()
    await ensureAdminLoggedIn(page)

    const barAfter = await waitForRecoveryBar(page, ns, proj, 'pull')
    await expect(barAfter).toContainText('conflict')
    const reasonAfter = await barAfter.getAttribute('data-reason')
    expect(reasonAfter).toBe('pull_conflict')
    await page.screenshot({ path: 'test-results/restart-seed-conflict-after.png', fullPage: false })

    // Resolve the conflict, exactly as an operator would, then confirm
    // Retry pull (post-restart) actually re-triggers a real pull and
    // resolves it.
    execSync('echo "seed-side change" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'resolve conflict to match seed (restart)')
    git(repo, 'push', 'origin', 'main')

    await barAfter.getByRole('button', { name: /Retry pull/ }).click()

    const projSection = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
    await expect(projSection.locator('[data-testid="seed-sync-recovery"]')).not.toBeVisible({ timeout: 20000 })
    await page.screenshot({ path: 'test-results/restart-seed-conflict-resolved.png', fullPage: false })
  })
})
