import { describe, expect, test } from "vitest"

// Black-box test of the real entrypoint: spawn `bun src/main.ts serve` on an
// ephemeral loopback port, exercise both endpoints over TCP, and verify clean
// shutdown on SIGTERM (130 = interrupted by signal), including with a request
// in flight.

const appServerRoot = new URL("..", import.meta.url).pathname

const freePort = async (): Promise<number> => {
  const probe = Bun.serve({ port: 0, fetch: () => new Response("") })
  const port = probe.port!
  await probe.stop(true)
  return port
}

const waitForHealth = async (base: string): Promise<Response> => {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      return await fetch(`${base}/api/v1/health`)
    } catch {
      await Bun.sleep(50)
    }
  }
  throw new Error(`server at ${base} never became healthy`)
}

describe("atc serve (black box)", () => {
  test("serves both endpoints and shuts down cleanly on SIGTERM", async () => {
    const port = await freePort()
    const base = `http://127.0.0.1:${port}`
    const proc = Bun.spawn(["bun", "src/main.ts", "serve", "--port", String(port)], {
      cwd: appServerRoot,
      stdout: "ignore",
      stderr: "pipe",
    })
    try {
      const health = await waitForHealth(base)
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

      // Shut down with a request in flight: the fetch may complete or be
      // dropped mid-connection, but the process itself must exit cleanly.
      const inFlight = fetch(`${base}/api/v1/health`).then(
        () => "completed",
        () => "dropped",
      )
      proc.kill("SIGTERM")
      expect(await proc.exited).toBe(130)
      expect(["completed", "dropped"]).toContain(await inFlight)

      // The port is released once the process is gone.
      await expect(fetch(`${base}/api/v1/health`)).rejects.toThrow()
    } finally {
      proc.kill()
    }
  }, 30_000)

  test("fails with one friendly line when the port is taken", async () => {
    const occupant = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response("") })
    try {
      const proc = Bun.spawn(["bun", "src/main.ts", "serve", "--port", String(occupant.port)], {
        cwd: appServerRoot,
        stdout: "ignore",
        stderr: "pipe",
      })
      expect(await proc.exited).toBe(1)
      const stderr = await new Response(proc.stderr).text()
      expect(stderr).toMatch(/^atc serve: /)
      expect(stderr.trim().split("\n")).toHaveLength(1)
    } finally {
      await occupant.stop(true)
    }
  }, 30_000)
})
