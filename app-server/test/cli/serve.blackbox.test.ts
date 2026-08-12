import { mkdtempSync, readFileSync, rmSync, statSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll, describe, expect, test } from "vitest"
import {
  appServerRoot,
  cleanupTempDirs,
  isolatedEnv,
  rawStatus,
  runCli,
  spawnServe,
  spawnServeHealthy,
} from "../blackbox.ts"
import { makeFakeZmxSandbox } from "../testLayers.ts"

// Black-box tests of the real entrypoint: spawn `bun src/main.ts serve` on a
// loopback port, exercise both endpoints over TCP, and verify clean shutdown
// on SIGTERM and SIGINT (exit 130 = interrupted by signal), including with an
// open half-sent request at signal time.

// Tests run under `bun --bun vitest`, so execPath is the pinned bun binary —
// no PATH lookup for the child. Isolated locations keep the spawned server's
// database and log file out of the real user directories.
const scratch = mkdtempSync(join(tmpdir(), "atc-serve-blackbox-"))
afterAll(() => {
  rmSync(scratch, { recursive: true, force: true })
  cleanupTempDirs()
})

// Startup terminal reconciliation consults zmx: pin the executable to the
// deterministic fake so these tests need no zmx install anywhere they run
// (a missing install would add a startup warning line to stderr).
const sandbox = makeFakeZmxSandbox()
const serveEnv = () => isolatedEnv(scratch, { ATC_ZMX_EXECUTABLE: sandbox.wrapper })
const spawnFromSource = (port: number, env: ReturnType<typeof serveEnv>) =>
  spawnServe([process.execPath, "src/main.ts"], port, appServerRoot, env)

describe("atc serve (black box)", () => {
  test.each(["SIGTERM", "SIGINT"] as const)(
    "serves both endpoints and shuts down cleanly on %s",
    async (signal) => {
      // The env is rebuilt per spawn attempt: each serveEnv() allocates a
      // fresh state home, and the log assertion below must read the one the
      // surviving server actually wrote to.
      let env!: ReturnType<typeof serveEnv>
      const { proc, port, base, health } = await spawnServeHealthy((attemptPort) => {
        env = serveEnv()
        return spawnFromSource(attemptPort, env)
      })
      try {
        expect(health.status).toBe(200)
        expect(await health.json()).toEqual({ status: "ok" })

        const version = await fetch(`${base}/api/v1/version`)
        expect(version.status).toBe(200)
        expect(await version.json()).toEqual({
          version: expect.any(String),
          apiVersion: "v1",
          commit: expect.any(String),
          builtAt: expect.any(String),
        })

        // Leave a half-sent request open across the signal: shutdown must not
        // hang on the open connection.
        const halfOpen = await Bun.connect({
          hostname: "127.0.0.1",
          port,
          socket: { data: () => {}, close: () => {}, error: () => {} },
        })
        halfOpen.write("GET /api/v1/health HTTP/1.1\r\nHost: atc\r\n")

        proc.kill(signal)
        expect(await proc.exited).toBe(130)
        halfOpen.end()

        // The port is released once the process is gone.
        await expect(fetch(`${base}/api/v1/health`)).rejects.toThrow()

        // The file log records the clean exit — a supervising app can tell
        // it from a kill, whose log simply stops.
        const log = await Bun.file(`${env.XDG_STATE_HOME}/atc/atc.log`).text()
        expect(log).toContain("shutting down")
      } finally {
        proc.kill()
      }
    },
    30_000,
  )

  // The remote-access trust rule (ATC-148) end to end: the real server
  // generates the credential at startup, enforces it for any non-loopback
  // Host, and honors a CLI rotation immediately — no restart. A loopback
  // connection with a non-loopback Host is the shape both a proxied request
  // (`tailscale serve` preserves the incoming Host) and a DNS-rebinding
  // attempt present, so the token is required for it by design.
  test("generates the auth token on start and enforces rotation live", async () => {
    let env!: ReturnType<typeof serveEnv>
    const { proc, port } = await spawnServeHealthy((attemptPort) => {
      env = serveEnv()
      return spawnFromSource(attemptPort, env)
    })
    try {
      const tokenFile = `${env.XDG_DATA_HOME}/atc/auth-token`
      expect(statSync(tokenFile).mode & 0o777).toBe(0o600)
      const token = readFileSync(tokenFile, "utf8").trim()
      expect(token).toMatch(/^atc_/)

      const remoteHost = "Host: workstation.tailnet:7332"
      expect(await rawStatus(port, [remoteHost])).toBe(403)
      expect(await rawStatus(port, [remoteHost, `Authorization: Bearer ${token}`])).toBe(200)

      const rotated = await runCli(
        [process.execPath, "src/main.ts"],
        ["token", "rotate"],
        appServerRoot,
        env,
      )
      expect(rotated.exitCode).toBe(0)
      const newToken = rotated.stdout.trim()
      expect(newToken).not.toBe(token)
      expect(await rawStatus(port, [remoteHost, `Authorization: Bearer ${token}`])).toBe(403)
      expect(await rawStatus(port, [remoteHost, `Authorization: Bearer ${newToken}`])).toBe(200)
    } finally {
      proc.kill()
      await proc.exited
    }
  }, 30_000)

  // `--bind 0.0.0.0` opens the listener beyond loopback while loopback still
  // answers (0.0.0.0 includes it), so local clients keep working. Exercises
  // the serve.ts flag-resolution path end to end.
  test("serves over loopback when bound to 0.0.0.0 via --bind", async () => {
    const { proc, health } = await spawnServeHealthy((attemptPort) =>
      Bun.spawn(
        [
          process.execPath,
          "src/main.ts",
          "serve",
          "--port",
          String(attemptPort),
          "--bind",
          "0.0.0.0",
        ],
        { cwd: appServerRoot, env: serveEnv(), stdout: "pipe", stderr: "pipe" },
      ),
    )
    try {
      expect(health.status).toBe(200)
      expect(await health.json()).toEqual({ status: "ok" })
    } finally {
      proc.kill()
      await proc.exited
    }
  }, 30_000)

  // The freePort claim race, deterministically: a first child that dies at
  // startup (as one that lost its port does) costs a retry on a fresh pick,
  // not the test.
  test("spawnServeHealthy respawns when the child exits before becoming healthy", async () => {
    let attempts = 0
    const { proc, health } = await spawnServeHealthy((port) => {
      attempts++
      if (attempts === 1) {
        return Bun.spawn(["sh", "-c", "exit 1"], { stdout: "pipe", stderr: "pipe" })
      }
      return spawnFromSource(port, serveEnv())
    })
    try {
      expect(attempts).toBe(2)
      expect(health.status).toBe(200)
    } finally {
      proc.kill()
      await proc.exited
    }
  }, 30_000)

  test("fails with one friendly stderr line when the port is taken", async () => {
    const occupant = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response("") })
    const proc = spawnFromSource(occupant.port!, serveEnv())
    try {
      expect(await proc.exited).toBe(1)
      const stderr = await new Response(proc.stderr as ReadableStream).text()
      expect(stderr).toMatch(/^atc serve: /)
      expect(stderr.trim().split("\n")).toHaveLength(1)
      expect(await new Response(proc.stdout as ReadableStream).text()).toBe("")
    } finally {
      proc.kill()
      await occupant.stop(true)
    }
  }, 30_000)
})
