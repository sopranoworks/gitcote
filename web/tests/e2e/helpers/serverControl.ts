import { spawn } from 'node:child_process'
import { readFileSync, writeFileSync, openSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Cross-process handoff file: globalSetup.ts (runs in Playwright's main
// process) writes it after spawning the real GitCote server; spec files
// (each in their own worker process, per Playwright's execution model) read
// it here to control that same process by PID, since they don't share a
// ChildProcess handle with globalSetup.
export const SERVER_INFO_PATH = join(tmpdir(), 'gitcote-e2e-server-info.json')

interface ServerInfo {
  binPath: string
  cfgPath: string
  port: number
  logPath: string
  pid: number
}

function readInfo(): ServerInfo {
  return JSON.parse(readFileSync(SERVER_INFO_PATH, 'utf8'))
}

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

// killAndRestartServer simulates a real crash-and-recover cycle for the
// restart-persistence E2E test: SIGKILLs the currently running gitcote
// server (no graceful shutdown — a real crash never gets one either, and
// this is specifically what exercises the "was an agent actually running
// when we died" recovery path, not a clean-exit path) and respawns the same
// binary against the SAME --config (same storage.base_dir), so whatever was
// written to bbolt/seed.json before the kill is exactly what the new process
// reads back on startup. Returns once the new process is answering HTTP
// requests again.
export async function killAndRestartServer(): Promise<void> {
  const info = readInfo()

  try {
    process.kill(info.pid, 'SIGKILL')
  } catch {
    // already dead
  }
  // Let the OS release the listening port before we try to rebind it.
  await new Promise((r) => setTimeout(r, 500))

  const logFd = openSync(info.logPath, 'a')
  const child = spawn(info.binPath, ['--config', info.cfgPath], {
    stdio: ['ignore', logFd, logFd],
    detached: true,
  })
  child.unref()

  writeFileSync(SERVER_INFO_PATH, JSON.stringify({ ...info, pid: child.pid }))

  await waitForHttp(`http://localhost:${info.port}/auth/status`)
}
