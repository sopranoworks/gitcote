import { execSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'

const PORT = Number(process.env.GITYARD_E2E_PORT ?? 9099)
const MCP_PORT = PORT - 1

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

function createPR(token: string, ns: string, proj: string, branch: string, title: string) {
  const tmp = mkdtempSync(join(tmpdir(), 'gityard-btn-'))
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
  execSync(`echo "// ${title}" > feature.txt`, { cwd: repo, shell: '/bin/bash' })
  git(repo, 'add', 'feature.txt')
  git(repo, 'commit', '-m', title)
  git(repo, 'push', '-u', 'origin', branch,
    '-o', 'pull_request.create',
    `-o`, `pull_request.title=${title}`,
  )
}

async function parseSSEResponse(res: Response): Promise<unknown> {
  const text = await res.text()
  const ct = res.headers.get('content-type') ?? ''
  if (ct.includes('application/json')) return JSON.parse(text)
  for (const line of text.split('\n')) {
    if (line.startsWith('data: ')) return JSON.parse(line.slice(6))
  }
  throw new Error(`unexpected MCP response: ${text.slice(0, 200)}`)
}

async function mcpApprove(token: string, ns: string, proj: string, prNumber: number) {
  const base = `http://localhost:${MCP_PORT}`
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json, text/event-stream',
    'Authorization': `Bearer ${token}`,
  }

  const initRes = await fetch(`${base}/mcp`, {
    method: 'POST', headers,
    body: JSON.stringify({
      jsonrpc: '2.0', id: 1, method: 'initialize',
      params: {
        protocolVersion: '2025-03-26', capabilities: {},
        clientInfo: { name: 'e2e-test', version: '1.0' },
      },
    }),
  })
  const sessionId = initRes.headers.get('mcp-session-id')
  if (sessionId) headers['Mcp-Session-Id'] = sessionId
  await parseSSEResponse(initRes)

  await fetch(`${base}/mcp`, {
    method: 'POST', headers,
    body: JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} }),
  })

  const callRes = await fetch(`${base}/mcp`, {
    method: 'POST', headers,
    body: JSON.stringify({
      jsonrpc: '2.0', id: 2, method: 'tools/call',
      params: {
        name: 'approve_pull_request',
        arguments: { namespace: ns, project_name: proj, number: prNumber },
      },
    }),
  })
  const result = await parseSSEResponse(callRes) as { result?: { isError?: boolean; content?: { text?: string }[] } }
  if (result.result?.isError) {
    throw new Error(`MCP approve failed: ${result.result.content?.[0]?.text}`)
  }
  return result
}

const ns = 'prbtn'
const proj = 'demo'

test.describe('PR action buttons', () => {
  let token = ''

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    await ensureAdminLoggedIn(page)
    await ensureProject(page, ns, proj)
    token = await issueGitToken(page)
    createPR(token, ns, proj, 'feat/buttons', 'Button test PR')
    await page.close()
  })

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page)
  })

  test('open PR shows Reject only (no Confirm)', async ({ page }) => {
    await page.goto(`/p/${ns}/${proj}/prs?pr=1`)
    await page.waitForTimeout(1500)
    const content = page.locator('#content')
    await expect(content.getByText('Button test PR')).toBeVisible({ timeout: 5000 })
    await expect(content.getByRole('button', { name: 'Reject' })).toBeVisible()
    await expect(content.getByRole('button', { name: 'Confirm' })).not.toBeVisible()
    await page.screenshot({ path: 'test-results/pr-buttons-open.png', fullPage: false })
  })

  test('approved PR shows Confirm + Reject', async ({ page }) => {
    await mcpApprove(token, ns, proj, 1)
    await page.goto(`/p/${ns}/${proj}/prs?pr=1`)
    await page.waitForTimeout(1500)
    const content = page.locator('#content')
    await expect(content.getByText('Button test PR')).toBeVisible({ timeout: 5000 })
    await expect(content.getByRole('button', { name: 'Confirm' })).toBeVisible({ timeout: 5000 })
    await expect(content.getByRole('button', { name: 'Reject' })).toBeVisible()
    await page.screenshot({ path: 'test-results/pr-buttons-approved.png', fullPage: false })
  })
})
