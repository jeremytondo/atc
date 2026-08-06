import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll, describe, expect, test } from "vitest"
import {
  appServerRoot,
  cleanupTempDirs,
  freePort,
  isolatedEnv,
  spawnServe,
  waitForHealth,
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
      const port = await freePort()
      const base = `http://127.0.0.1:${port}`
      const env = serveEnv()
      const proc = spawnFromSource(port, env)
      try {
        const health = await waitForHealth(base, proc)
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
