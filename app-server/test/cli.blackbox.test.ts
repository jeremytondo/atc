import { afterAll, beforeAll, describe, expect, test } from "vitest"
import packageJson from "../package.json"
import { appServerRoot, freePort, runCli, spawnServe, waitForHealth } from "./blackbox.ts"

// Black-box tests of the client-backed CLI commands: exact stdout, stderr,
// and exit codes, against a real `atc serve` spawned from source.
//
// Contract: success prints the JSON payload (2-space indented) on stdout and
// exits 0 with empty stderr; request failures print one `atc <command>:` line
// on stderr and exit 1 with empty stdout; invalid usage prints an ERROR block
// on stderr (help goes to stdout) and exits 1.

const cli = (args: ReadonlyArray<string>) =>
  runCli([process.execPath, "src/main.ts"], args, appServerRoot)

let serverPort: number
let base: string
let server: Bun.Subprocess

beforeAll(async () => {
  serverPort = await freePort()
  base = `http://127.0.0.1:${serverPort}`
  server = spawnServe([process.execPath, "src/main.ts"], serverPort, appServerRoot)
  await waitForHealth(base, server)
})

afterAll(() => {
  server.kill()
})

describe("atc health/version (black box)", () => {
  test("health prints the payload JSON and exits 0", async () => {
    const result = await cli(["health", "--url", base])
    expect(result.stdout).toBe('{\n  "status": "ok"\n}\n')
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)
  })

  test("version prints the payload JSON and exits 0", async () => {
    const result = await cli(["version", "--url", base])
    // Running from source, build metadata is deterministic dev placeholders.
    const expected = {
      version: packageJson.version,
      apiVersion: "v1",
      commit: "dev",
      builtAt: "dev",
    }
    expect(result.stdout).toBe(JSON.stringify(expected, null, 2) + "\n")
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)
  })

  test("an unreachable server is one stderr line and exit 1", async () => {
    // Port 1 is privileged and never bound by tests, so refusal is
    // deterministic — unlike a released ephemeral port, which another
    // parallel test worker could rebind.
    const result = await cli(["health", "--url", "http://127.0.0.1:1"])
    expect(result.stdout).toBe("")
    expect(result.stderr).toMatch(/^atc health: /)
    expect(result.stderr.trim().split("\n")).toHaveLength(1)
    expect(result.exitCode).toBe(1)
  })

  test("a contract-violating response is one stderr line and exit 1", async () => {
    const misbehaving = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      fetch: () => Response.json({ status: "degraded" }),
    })
    try {
      const result = await cli(["health", "--url", `http://127.0.0.1:${misbehaving.port}`])
      expect(result.stdout).toBe("")
      expect(result.stderr).toMatch(/^atc health: /)
      // Schema decode errors are multi-line by default; the CLI must collapse
      // them to keep the one-line diagnostic contract.
      expect(result.stderr.trim().split("\n")).toHaveLength(1)
      expect(result.exitCode).toBe(1)
    } finally {
      await misbehaving.stop(true)
    }
  })

  test("a missing --url flag is invalid usage: help on stdout, error on stderr, exit 1", async () => {
    const result = await cli(["health"])
    expect(result.stdout).toContain("USAGE")
    expect(result.stderr).toContain("--url")
    expect(result.exitCode).toBe(1)
  })

  test("a malformed --url value is invalid usage and exits 1", async () => {
    const result = await cli(["version", "--url", "not a url"])
    expect(result.stderr).toContain("--url")
    expect(result.exitCode).toBe(1)
  })
})
