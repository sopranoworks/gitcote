import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, openSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { SERVER_INFO_PATH } from './helpers/serverControl'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = join(here, '..', '..', '..') // web/tests/e2e -> repo root
const PORT = Number(process.env.GITCOTE_E2E_PORT ?? 9099)
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
  const binPath = join(tmpdir(), 'gitcote-e2e-bin')
  const mockReviewerBin = join(tmpdir(), 'gitcote-e2e-mock-reviewer')
  const mockRejectorBin = join(tmpdir(), 'gitcote-e2e-mock-rejector')
  const mockMergerBin = join(tmpdir(), 'gitcote-e2e-mock-merger')
  execFileSync('go', ['build', '-o', binPath, './cmd/gitcote'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })
  execFileSync('go', ['build', '-tags', 'e2e', '-o', mockReviewerBin, './internal/e2e/testcmd/mock-reviewer'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })
  execFileSync('go', ['build', '-tags', 'e2e', '-o', mockRejectorBin, './internal/e2e/testcmd/mock-rejector'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })
  execFileSync('go', ['build', '-tags', 'e2e', '-o', mockMergerBin, './internal/e2e/testcmd/mock-merger'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })

  dataDir = mkdtempSync(join(tmpdir(), 'gitcote-e2e-data-'))
  const cfgPath = join(dataDir, 'gitcote.yaml')
  const oauthPort = PORT - 2

  const agentsDir = join(dataDir, 'agents')
  mkdirSync(join(agentsDir, 'mock_reviewer'), { recursive: true })
  mkdirSync(join(agentsDir, 'mock_rejector'), { recursive: true })
  mkdirSync(join(agentsDir, 'mock_merger'), { recursive: true })
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
  // mock_merger never resolves anything — it just sleeps, so restart-recovery
  // tests can put GitCote into a genuine "agent actively running" state and
  // then kill the server out from under it.
  writeFileSync(
    join(agentsDir, 'mock_merger/agent.yaml'),
    [
      'agent:',
      '  role: merger',
      '  display_name: "Mock Merger (E2E, never finishes)"',
      `  command: '${mockMergerBin}'`,
      '  prompt: "Resolve merge conflict for $PR_ID"',
      '',
    ].join('\n'),
  )
  // mock_noop_reviewer exits 0 immediately without ever calling approve or
  // reject — the minimal, real way (via `sh -c`, no compiled binary needed)
  // to reproduce "reviewer exited without verdict" -> review_incomplete
  // through the actual agent-spawn code path, for restart-persistence tests.
  mkdirSync(join(agentsDir, 'mock_noop_reviewer'), { recursive: true })
  writeFileSync(
    join(agentsDir, 'mock_noop_reviewer/agent.yaml'),
    [
      'agent:',
      '  role: reviewer',
      '  display_name: "Mock No-op Reviewer (E2E, never verdicts)"',
      '  command: "true"',
      '  prompt: "Review PR $PR_ID"',
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

  const logPath = join(dataDir, 'server.log')
  const logFd = openSync(logPath, 'w')
  // detached + unref: spec files run in separate worker processes and need
  // to be able to kill/respawn this exact process by PID (see
  // helpers/serverControl.ts, used by the restart-persistence E2E test) —
  // it must not be tied to this globalSetup process's lifetime.
  server = spawn(binPath, ['--config', cfgPath], {
    stdio: ['ignore', logFd, logFd],
    detached: true,
  })
  server.unref()

  writeFileSync(SERVER_INFO_PATH, JSON.stringify({ binPath, cfgPath, port: PORT, logPath, pid: server.pid }))

  await waitForHttp(`http://localhost:${PORT}/auth/status`)

  return async () => {
    // Re-read the info file rather than trusting the closed-over `server`
    // handle — a restart-persistence test may have killed and respawned the
    // real server (a different PID) since this closure was created.
    let pid = server?.pid
    try {
      pid = JSON.parse(readFileSync(SERVER_INFO_PATH, 'utf8')).pid
    } catch {
      // info file missing/corrupt — fall back to the original PID
    }
    if (pid) {
      try { process.kill(pid, 'SIGTERM') } catch {
        // already dead
      }
    }
    await new Promise((r) => setTimeout(r, 500))
    try { rmSync(SERVER_INFO_PATH, { force: true }) } catch {
      // best effort
    }
    if (dataDir) rmSync(dataDir, { recursive: true, force: true })
  }
}
