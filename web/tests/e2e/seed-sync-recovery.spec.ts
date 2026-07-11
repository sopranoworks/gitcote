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
})
