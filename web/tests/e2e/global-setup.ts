import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, openSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = join(here, '..', '..', '..') // web/tests/e2e -> repo root
const PORT = Number(process.env.GITYARD_E2E_PORT ?? 9099)
const MCP_PORT = PORT - 1

async function waitForHttp(url: string, timeoutMs = 20000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 200))
  }
  throw new Error(`server did not become ready at ${url}`)
}

let server: ChildProcess | null = null
let dataDir = ''

export default async function globalSetup(): Promise<() => Promise<void>> {
  const binPath = join(tmpdir(), 'gityard-e2e-bin')
  const mockReviewerBin = join(tmpdir(), 'gityard-e2e-mock-reviewer')
  const mockRejectorBin = join(tmpdir(), 'gityard-e2e-mock-rejector')
  execFileSync('go', ['build', '-o', binPath, './cmd/gityard'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })
  execFileSync('go', ['build', '-o', mockReviewerBin, './cmd/mock-reviewer'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })
  execFileSync('go', ['build', '-o', mockRejectorBin, './cmd/mock-rejector'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })

  dataDir = mkdtempSync(join(tmpdir(), 'gityard-e2e-data-'))
  const cfgPath = join(dataDir, 'gityard.yaml')
  const oauthPort = PORT - 2

  const agentsDir = join(dataDir, 'agents')
  mkdirSync(join(agentsDir, 'mock_reviewer'), { recursive: true })
  mkdirSync(join(agentsDir, 'mock_rejector'), { recursive: true })
  writeFileSync(
    join(agentsDir, 'mock_reviewer/agent.yaml'),
    [
      'agent:',
      '  role: reviewer',
      '  display_name: "Mock Reviewer (E2E)"',
      `  command: '${mockReviewerBin}'`,
      '  prompt: "Review and approve PR $PR_ID"',
      '',
    ].join('\n'),
  )
  writeFileSync(
    join(agentsDir, 'mock_rejector/agent.yaml'),
    [
      'agent:',
      '  role: reviewer',
      '  display_name: "Mock Rejector (E2E)"',
      `  command: '${mockRejectorBin}'`,
      '  prompt: "Review and reject PR $PR_ID"',
      '',
    ].join('\n'),
  )

  writeFileSync(
    cfgPath,
    [
      'server:',
      '  http:',
      `    listen: ":${PORT}"`,
      '  mcp:',
      '    plain:',
      `      listen: ":${MCP_PORT}"`,
      '    oauth:',
      `      listen: ":${oauthPort}"`,
      `      external_url: "http://localhost:${oauthPort}"`,
      '  log:',
      '    level: "warn"',
      '  auth:',
      '    enabled: false',
      '    users:',
      '      allow_first_run_admin: true',
      'identity:',
      '  user:',
      '    name: "Test Admin"',
      '    email: "admin@test.local"',
      'storage:',
      `  base_dir: "${join(dataDir, 'data')}"`,
      'agent_spawn:',
      '  enabled: true',
      `  agents_root: "${agentsDir}"`,
      '  default_timeout: "2m"',
      '',
    ].join('\n'),
  )

  const logFd = openSync(join(dataDir, 'server.log'), 'w')
  server = spawn(binPath, ['--config', cfgPath], {
    stdio: ['ignore', logFd, logFd],
  })

  await waitForHttp(`http://localhost:${PORT}/auth/status`)

  return async () => {
    server?.kill('SIGTERM')
    await new Promise((r) => setTimeout(r, 500))
    if (dataDir) rmSync(dataDir, { recursive: true, force: true })
  }
}
