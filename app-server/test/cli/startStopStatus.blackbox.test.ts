import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll, describe, expect, test } from "vitest"
import {
  appServerRoot,
  cleanupTempDirs,
  freePort,
  isolatedEnv,
  runCli,
  waitUntil,
} from "../blackbox.ts"
import { makeFakeZmxSandbox } from "../testLayers.ts"

// Black-box lifecycle of the self-managed background commands: `atc start`
// spawns a detached `atc serve` and owns the pidfile; `status` and `stop`
// re-verify liveness rather than trusting it. Spawned from source, so the
// detached child is `bun src/main.ts serve` resolved against the repo cwd.
// (The supervisor-based `atc service` family is a different feature —
// cli/service.ts — unit-tested in service.test.ts.)

const scratch = mkdtempSync(join(tmpdir(), "atc-service-blackbox-"))
const startedPids: Array<number> = []

afterAll(() => {
  // A failed assertion must not leak a detached server; these pids came from
  // pidfiles inside this suite's isolated state dir, so they are ours.
  for (const pid of startedPids) {
    try {
      process.kill(pid, "SIGKILL")
    } catch {
      // already gone
    }
  }
  rmSync(scratch, { recursive: true, force: true })
  cleanupTempDirs()
})

const sandbox = makeFakeZmxSandbox()
const env = isolatedEnv(scratch, { ATC_ZMX_EXECUTABLE: sandbox.wrapper })
const logFile = join(env.XDG_STATE_HOME, "atc", "atc.log")
const pidFile = join(env.XDG_STATE_HOME, "atc", "atc.pid")
const cli = (...args: Array<string>) =>
  runCli([process.execPath, "src/main.ts"], args, appServerRoot, env)

const isAlive = (pid: number) => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

describe("atc start / stop / status (black box)", () => {
  test("the full lifecycle: start, verify, idempotent start, stop, verify", async () => {
    const port = await freePort()

    // Nothing running yet.
    const idle = await cli("status")
    expect(idle.exitCode).not.toBe(0)
    expect(idle.stderr).toContain("not running")

    const started = await cli("start", "--port", String(port))
    expect(started.stderr).toBe("")
    expect(started.exitCode).toBe(0)
    expect(started.stdout).toContain(`started atc app server`)
    expect(started.stdout).toContain(`http://127.0.0.1:${port}`)

    const record = JSON.parse(readFileSync(pidFile, "utf8")) as { pid: number; port: number }
    startedPids.push(record.pid)
    expect(record.port).toBe(port)
    expect(isAlive(record.pid)).toBe(true)

    // start already waited for health; the endpoint answers immediately.
    const health = await fetch(`http://127.0.0.1:${port}/api/v1/health`)
    expect(health.status).toBe(200)

    const again = await cli("start", "--port", String(port))
    expect(again.exitCode).toBe(0)
    expect(again.stdout).toContain(`already running (pid ${record.pid})`)

    const running = await cli("status")
    expect(running.exitCode).toBe(0)
    expect(running.stdout).toContain(`running (pid ${record.pid})`)
    expect(running.stdout).toContain(`  web ui    http://127.0.0.1:${port}/`)
    expect(running.stdout).toContain(
      `  api       http://127.0.0.1:${port}/api/v1 (openapi: http://127.0.0.1:${port}/openapi.json)`,
    )
    expect(running.stdout).toContain(
      "  auth      loopback clients only; no token needed (bind 127.0.0.1)",
    )
    expect(running.stdout).toContain(`  log       ${logFile}`)
    expect(running.stdout).toContain(`  pid-file  ${pidFile}`)

    const stopped = await cli("stop")
    expect(stopped.exitCode).toBe(0)
    expect(stopped.stdout).toContain(`stopped atc app server (pid ${record.pid})`)
    await waitUntil(() => !isAlive(record.pid), "the stopped server to exit")
    expect(existsSync(pidFile)).toBe(false)

    const gone = await cli("status")
    expect(gone.exitCode).not.toBe(0)
    expect(gone.stderr).toContain("not running")

    // stop is idempotent.
    const stopAgain = await cli("stop")
    expect(stopAgain.exitCode).toBe(0)
    expect(stopAgain.stdout).toContain("not running")
  }, 60_000)

  // The release before ATC-148 wrote `{ pid, port }` pidfiles (no bind).
  // `stop` proving it decodes the old shape — dead pid, so it removes the
  // stale file rather than reporting "not running" from a failed decode —
  // is what keeps upgrades able to manage a server the old binary started.
  test("a pre-bind pidfile ({ pid, port }) still decodes", async () => {
    const shortLived = Bun.spawn(["sh", "-c", "exit 0"])
    await shortLived.exited
    mkdirSync(join(env.XDG_STATE_HOME, "atc"), { recursive: true })
    writeFileSync(pidFile, JSON.stringify({ pid: shortLived.pid, port: 1 }))

    const stopped = await cli("stop")
    expect(stopped.exitCode).toBe(0)
    expect(stopped.stdout).toContain("removed a stale pidfile")
    expect(existsSync(pidFile)).toBe(false)
  }, 60_000)

  // A specific (non-wildcard) bind: probes and reported URLs target the bind
  // itself, and IPv6 literals come out bracketed.
  test("start/status/stop manage a server bound to a specific address (::1)", async () => {
    const port = await freePort()
    const started = await cli("start", "--port", String(port), "--bind", "::1")
    expect(started.stderr).toBe("")
    expect(started.exitCode).toBe(0)
    expect(started.stdout).toContain(`http://[::1]:${port}`)
    const record = JSON.parse(readFileSync(pidFile, "utf8")) as { pid: number }
    startedPids.push(record.pid)

    const status = await cli("status")
    expect(status.exitCode).toBe(0)
    expect(status.stdout).toContain(`http://[::1]:${port}`)

    const stopped = await cli("stop")
    expect(stopped.exitCode).toBe(0)
  }, 60_000)

  test("a stale pidfile is replaced by start and reported by status", async () => {
    // A pid that existed and is certainly gone now. The pidfile's parent
    // exists regardless of test order.
    const shortLived = Bun.spawn(["sh", "-c", "exit 0"])
    await shortLived.exited
    mkdirSync(join(env.XDG_STATE_HOME, "atc"), { recursive: true })
    writeFileSync(pidFile, JSON.stringify({ pid: shortLived.pid, port: 1 }))

    const stale = await cli("status")
    expect(stale.exitCode).not.toBe(0)
    expect(stale.stderr).toContain("not running")

    const port = await freePort()
    const started = await cli("start", "--port", String(port))
    expect(started.exitCode).toBe(0)
    const record = JSON.parse(readFileSync(pidFile, "utf8")) as { pid: number; port: number }
    startedPids.push(record.pid)
    expect(record.pid).not.toBe(shortLived.pid)

    const stopped = await cli("stop")
    expect(stopped.exitCode).toBe(0)
  }, 60_000)
})
