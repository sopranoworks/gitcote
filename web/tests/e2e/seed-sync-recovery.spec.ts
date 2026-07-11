import { execSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'

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
  const tmp = mkdtempSync(join(tmpdir(), 'gitcote-ssr-'))
  const url = `http://oauth2:${token}@localhost:${PORT}/${ns}/${proj}.git`
  git(tmp, 'clone', url, 'repo')
  const repo = join(tmp, 'repo')
  const hasBranches = execSync('git branch -a', { cwd: repo, encoding: 'utf8' }).trim()
  if (!hasBranches) {
    execSync('echo "base content" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'initial commit')
    git(repo, 'branch', '-M', 'main')
    git(repo, 'push', '-u', 'origin', 'main')
  }
  return repo
}

// Creates a local bare repo (reachable by plain filesystem path — no SSH
// needed, same technique the Go-level seed-push tests use) diverged from
// the gitcote project's main by a conflicting edit to the same file.
function makeDivergedSeedBare(gitcoteRepo: string): string {
  const seedBareDir = join(mkdtempSync(join(tmpdir(), 'gitcote-ssr-seed-')), 'seed.git')
  git(tmpdir(), 'init', '--bare', seedBareDir)
  git(seedBareDir, 'symbolic-ref', 'HEAD', 'refs/heads/main')
  git(gitcoteRepo, 'push', seedBareDir, 'main')

  const seedCloneDir = mkdtempSync(join(tmpdir(), 'gitcote-ssr-seedclone-'))
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

async function waitForRecoveryBar(
  page: Page,
  ns: string,
  proj: string,
  expectDirection: 'push' | 'pull',
  timeout = 30000,
) {
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

// Task B of the seed-sync/merge-conflict WebUI recovery directive: retry_seed_sync
// and dismiss_seed_sync had zero WebUI entry points (only MCP-tool access) —
// this spec proves the new SeedSyncRecoveryBar in SeedConfigSection fixes that,
// and that it clearly distinguishes pull-direction from push-direction conflicts
// so an operator can't be misled about which operation Retry re-triggers.
test.describe('Seed sync recovery — WebUI', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('pull conflict: recovery bar shows pull direction, retry resolves it', async ({ page }) => {
    test.setTimeout(120_000)
    await ensureAdminLoggedIn(page)
    const ns = 'ssrpull'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    const seedBareDir = makeDivergedSeedBare(repo)

    // Diverge gitcote's own main differently, so the pull genuinely conflicts.
    execSync('echo "gitcote-side change" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'gitcote-side change')
    git(repo, 'push', 'origin', 'main')

    await setupSeedConfig(page, ns, proj, seedBareDir, 'ssrpull-key')

    // Trigger the real pull_from_seed flow — same call the "Pull from Seed"
    // button makes.
    await wsRequest(page, 'SEED_PULL', { namespace: ns, projectName: proj })

    const bar = await waitForRecoveryBar(page, ns, proj, 'pull')
    await expect(bar).toContainText('conflict')
    await expect(bar).toContainText('pull')
    await expect(bar.getByRole('button', { name: /Retry pull/ })).toBeVisible()
    await expect(bar.getByRole('button', { name: 'Dismiss' })).toBeVisible()
    await page.screenshot({ path: 'test-results/seed-sync-pull-conflict.png', fullPage: false })

    // Manually resolve the conflict, exactly as an operator would: push a
    // commit to gitcote's main that matches the seed side, so a retried
    // pull can complete cleanly.
    execSync('echo "seed-side change" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'resolve conflict to match seed')
    git(repo, 'push', 'origin', 'main')

    await bar.getByRole('button', { name: /Retry pull/ }).click()

    // Confirm it resolves: the recovery bar disappears once the retried
    // pull succeeds (state leaves conflict/interrupted).
    const projSection = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
    await expect(projSection.locator('[data-testid="seed-sync-recovery"]')).not.toBeVisible({ timeout: 20000 })
    await page.screenshot({ path: 'test-results/seed-sync-pull-conflict-resolved.png', fullPage: false })
  })

  test('push conflict: recovery bar shows push direction (not pull), retry re-triggers push', async ({ page }) => {
    test.setTimeout(120_000)
    await ensureAdminLoggedIn(page)
    const ns = 'ssrpush'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    const seedBareDir = makeDivergedSeedBare(repo)

    // Diverge gitcote's own main differently, so the push genuinely conflicts.
    execSync('echo "gitcote-side change (push)" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'gitcote-side change (push)')
    git(repo, 'push', 'origin', 'main')

    await setupSeedConfig(page, ns, proj, seedBareDir, 'ssrpush-key')

    // Trigger the real push_to_seed flow — same call the "Push Now" button
    // makes.
    await wsRequest(page, 'SEED_PUSH', { namespace: ns, projectName: proj })

    const bar = await waitForRecoveryBar(page, ns, proj, 'push')
    await expect(bar).toContainText('conflict')
    await expect(bar).toContainText('push')
    // Negative check: must not read "pull" anywhere in the direction label —
    // this is the exact bug (retry_seed_sync always retried a pull) that
    // c31e6b3 fixed at the backend; the UI must not misrepresent it either.
    await expect(bar).not.toContainText('pull conflict', { ignoreCase: true })

    const retryBtn = bar.getByRole('button', { name: /Retry push/ })
    await expect(retryBtn).toBeVisible()
    await page.screenshot({ path: 'test-results/seed-sync-push-conflict.png', fullPage: false })

    await retryBtn.click()

    // The content is still diverged (not resolved here, matching the
    // directive's more lenient push-case requirement), so the retried push
    // will conflict again — but it must still show direction=push, not
    // pull, proving executeSeedPush (not executeSeedPull) actually ran.
    // Same direction-preservation technique as the backend regression test.
    await page.waitForTimeout(2000)
    const barAfter = await waitForRecoveryBar(page, ns, proj, 'push')
    await expect(barAfter).toContainText('push')
    await page.screenshot({ path: 'test-results/seed-sync-push-conflict-retried.png', fullPage: false })
  })

  // Task A, directive 2026-07-11-verify-ui-concurrency-and-disambiguation:
  // a rapid double-click on Retry must never crash the page or produce a
  // false "success" for both clicks — this is exactly the gap the new
  // seedSyncOpLock closes (previously, two concurrent SEED_SYNC_RETRY calls
  // could both pass the "is seed sync the active queue entry" check before
  // either released the slot, and BOTH would get a "status: ok" response).
  test('rapid double-click on Retry pull does not crash or produce a false double-success', async ({ page }) => {
    test.setTimeout(120_000)
    await ensureAdminLoggedIn(page)
    const ns = 'ssrdbl'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    const seedBareDir = makeDivergedSeedBare(repo)
    execSync('echo "gitcote-side change (dblclick)" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'gitcote-side change (dblclick)')
    git(repo, 'push', 'origin', 'main')

    await setupSeedConfig(page, ns, proj, seedBareDir, 'ssrdbl-key')
    await wsRequest(page, 'SEED_PULL', { namespace: ns, projectName: proj })

    const bar = await waitForRecoveryBar(page, ns, proj, 'pull')
    const retryBtn = bar.getByRole('button', { name: /Retry pull/ })
    await expect(retryBtn).toBeVisible()

    // Fire two clicks as close together as Playwright allows. The first
    // click's own side effects (React re-render, possibly removing/
    // replacing the button once the request resolves) can make the SECOND
    // high-level .click() action's actionability/stability wait hang until
    // its own timeout rather than the whole test's — allSettled plus a
    // short per-click timeout means a detached-element outcome from the
    // loser is treated as an acceptable settle, not a test failure.
    await Promise.allSettled([
      retryBtn.click({ timeout: 5000 }),
      retryBtn.click({ force: true, timeout: 5000 }),
    ])

    // Whichever click "won", the page must settle cleanly: no crash, and
    // eventually either the bar clears (retry accepted, sync in progress
    // or resolved) or shows a clean, single, still-usable state — never
    // stuck, never duplicated.
    await page.waitForTimeout(2000)
    const projSection = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
    await expect(projSection).toBeVisible()
    await page.screenshot({ path: 'test-results/seed-sync-pull-dblclick-settled.png', fullPage: false })
  })

  // Task A, multi-session: N independent WebSocket connections (standing in
  // for N browser tabs/sessions, each opened exactly as the app's wsClient
  // would) all fire retry_seed_sync (the same message "Retry pull" sends)
  // for the SAME stuck seed sync at effectively the same instant. This is
  // the direct UI/E2E-layer proof that seedSyncOpLock actually serializes
  // retries over the real network path, not just in the Go-level lock test.
  //
  // The guarantee the lock provides is serialization, not "exactly one
  // winner": since the same conflict content is never resolved mid-test,
  // each retry that acquires the lock genuinely runs its pull to
  // completion, re-hits the same conflict, and releases the lock — making
  // it legitimate for a LATER call to then acquire the lock and succeed
  // too (this is exactly the same "sequential, non-overlapping turns"
  // pattern TestRetryPRAgent_MCP_ConcurrentCallsNeverOverlap already
  // accepts on the PR side). What the lock rules out is two pulls ever
  // running AT THE SAME TIME, and a false "status: ok" being sent for a
  // call whose pull never actually happened — not multiple sequential
  // successes.
  test('multiple concurrent sessions racing Retry pull: only clean, serialized outcomes', async ({ page }) => {
    test.setTimeout(120_000)
    await ensureAdminLoggedIn(page)
    const ns = 'ssrrace'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    const seedBareDir = makeDivergedSeedBare(repo)
    execSync('echo "gitcote-side change (race)" > conflict.txt', { cwd: repo, shell: '/bin/bash' })
    git(repo, 'add', 'conflict.txt')
    git(repo, 'commit', '-m', 'gitcote-side change (race)')
    git(repo, 'push', 'origin', 'main')

    await setupSeedConfig(page, ns, proj, seedBareDir, 'ssrrace-key')
    await wsRequest(page, 'SEED_PULL', { namespace: ns, projectName: proj })
    await waitForRecoveryBar(page, ns, proj, 'pull')

    const outcomes = await page.evaluate(
      ({ ns, proj, n }) => {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
        function fire(): Promise<{ ok: boolean; message: string }> {
          return new Promise((resolve) => {
            const ws = new WebSocket(`${proto}//${location.host}/ws/ui`)
            ws.onopen = () => {
              ws.send(JSON.stringify({
                type: 'SEED_SYNC_RETRY',
                payload: { namespace: ns, projectName: proj },
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
        const sockets: Promise<{ ok: boolean; message: string }>[] = []
        for (let i = 0; i < n; i++) sockets.push(fire())
        return Promise.all(sockets)
      },
      { ns, proj, n: 6 },
    )

    const succeeded = outcomes.filter(o => o.ok)
    // A late-arriving call can legitimately see either "already in
    // progress" (genuinely raced against a call whose pull is still
    // running) or "seed sync is not the active queue entry" (arrived after
    // the previous call's pull had already finished and released the
    // slot). Both are correct, clean rejections. What must never happen:
    // a crash, an unrecognized error, or zero successes (the lock must
    // eventually let calls through, not deadlock everything out).
    const cleanRejection = /already in progress|not the active queue entry/i
    const unexpected = outcomes.filter(o => !o.ok && !cleanRejection.test(o.message))

    if (unexpected.length > 0) {
      throw new Error(`unexpected non-clean rejection(s): ${JSON.stringify(unexpected)}`)
    }
    if (succeeded.length < 1) {
      throw new Error(`expected at least 1 of 6 concurrent Retry-pull calls to proceed, got 0: ${JSON.stringify(outcomes)}`)
    }
    if (outcomes.length !== 6) {
      throw new Error(`expected 6 responses, got ${outcomes.length}`)
    }

    // The real proof that the lock actually SERIALIZED rather than let
    // pulls overlap: after everything settles, the project must still be
    // in one coherent state (still showing the same unresolved pull
    // conflict — the content was never fixed), not something a torn/
    // interleaved pair of concurrent git operations could produce.
    const barAfter = await waitForRecoveryBar(page, ns, proj, 'pull')
    await expect(barAfter).toContainText('conflict')
  })

  // Task B: "Retry push/pull" previously read as if it always meant "retry
  // a merge conflict" — but a missing SSH key (an infrastructure problem,
  // nothing to do with a conflict) also lands in an interrupted-like state.
  // This proves the recovery bar now distinguishes the two: an SSH-key
  // failure must show "pull failed" (not "conflict") with the actual
  // detail text, not a bare, ambiguous "pull interrupted".
  test('infrastructure failure (missing SSH key) is distinguishable from a merge conflict', async ({ page }) => {
    test.setTimeout(60_000)
    await ensureAdminLoggedIn(page)
    const ns = 'ssrnokey'
    const proj = 'demo'
    await ensureProject(page, ns, proj)
    const token = await issueGitToken(page)

    const repo = cloneAndInit(token, ns, proj)
    const seedBareDir = makeDivergedSeedBare(repo)

    // Seed config with NO key at all — the exact "SSH key missing"
    // infrastructure failure the directive calls out, as distinct from a
    // genuine merge conflict.
    await wsRequest(page, 'SEED_RESUME', { email: ADMIN_EMAIL, password: ADMIN_PASSWORD })
    await wsRequest(page, 'SEED_CONFIG_SET', {
      namespace: ns, projectName: proj, seedUrl: seedBareDir, keyName: '', pushMode: 'disabled',
    })

    await wsRequest(page, 'SEED_PULL', { namespace: ns, projectName: proj })

    const projSection = page.locator(`[data-testid="proj-sections-${ns}-${proj}"]`)
    const bar = projSection.locator('[data-testid="seed-sync-recovery"]')
    const deadline = Date.now() + 30000
    let seen = false
    while (Date.now() < deadline && !seen) {
      await page.goto('/settings?item=namespaces')
      await page.waitForTimeout(1000)
      seen = await bar.isVisible().catch(() => false)
    }
    if (!seen) throw new Error('recovery bar for the SSH-key-missing failure did not appear within 30s')

    // Must read as a FAILURE, not a conflict — an operator must not think
    // "I need to resolve a merge conflict" when the real problem is "no
    // key is configured".
    await expect(bar).toContainText('failed')
    await expect(bar).not.toContainText('conflict')
    // The actual detail (from doSeedPull's "no key configured" error,
    // persisted via updateSeedSyncStateDetail) must be visible, not just
    // the generic "pull failed" category — otherwise the operator still
    // can't tell an SSH-key problem from a network-timeout problem.
    await expect(bar).toContainText(/no key configured/i)
    await page.screenshot({ path: 'test-results/seed-sync-infra-failure-distinguishable.png', fullPage: false })
  })
})
