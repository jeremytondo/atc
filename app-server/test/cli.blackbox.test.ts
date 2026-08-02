import { existsSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll, beforeAll, describe, expect, test } from "vitest"
import packageJson from "../package.json"
import {
  appServerRoot,
  freePort,
  isolatedEnv,
  runCli,
  spawnServe,
  waitForHealth,
} from "./blackbox.ts"

// Black-box tests of the client-backed CLI commands: exact stdout, stderr,
// and exit codes, against a real `atc serve` spawned from source.
//
// Contract: API-backed commands take zero connection flags — the base URL is
// derived from the settled configuration (ATC_PORT > config.toml > default).
// Success prints the JSON payload (2-space indented) on stdout and exits 0
// with empty stderr; config/request failures print one `atc <command>:` line
// on stderr and exit 1 with empty stdout; invalid usage prints an ERROR block
// on stderr (help goes to stdout) and exits 1.

const scratch = mkdtempSync(join(tmpdir(), "atc-cli-blackbox-"))
let serverPort: number
let env: Record<string, string | undefined>

const cli = (args: ReadonlyArray<string>, extraEnv: Record<string, string> = {}) =>
  runCli([process.execPath, "src/main.ts"], args, appServerRoot, { ...env, ...extraEnv })

let server: Bun.Subprocess

beforeAll(async () => {
  serverPort = await freePort()
  env = isolatedEnv(scratch, { ATC_PORT: String(serverPort) })
  server = spawnServe([process.execPath, "src/main.ts"], serverPort, appServerRoot, env)
  await waitForHealth(`http://127.0.0.1:${serverPort}`, server)
})

afterAll(() => {
  server.kill()
  rmSync(scratch, { recursive: true, force: true })
})

describe("atc health/version (black box)", () => {
  test("health resolves the server from ATC_PORT and exits 0", async () => {
    const result = await cli(["health"])
    expect(result.stdout).toBe('{\n  "status": "ok"\n}\n')
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)
  })

  test("version prints the payload JSON and exits 0", async () => {
    const result = await cli(["version"])
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

  test("the config file supplies the port when the environment is silent", async () => {
    const configFile = join(scratch, "config.toml")
    await Bun.write(configFile, `port = ${serverPort}\n`)
    const result = await cli(["health"], { ATC_PORT: "", ATC_CONFIG: configFile })
    expect(result.stdout).toBe('{\n  "status": "ok"\n}\n')
    expect(result.exitCode).toBe(0)
  })

  test("an unreachable server is one stderr line and exit 1", async () => {
    // Port 1 is privileged and never bound by tests, so refusal is
    // deterministic — unlike a released ephemeral port, which another
    // parallel test worker could rebind.
    const result = await cli(["health"], { ATC_PORT: "1" })
    expect(result.stdout).toBe("")
    expect(result.stderr).toMatch(/^atc health: \S/)
    expect(result.stderr.trim().split("\n")).toHaveLength(1)
    expect(result.exitCode).toBe(1)
  })

  test("invalid configuration is one stderr line naming the source and exit 1", async () => {
    const result = await cli(["health"], { ATC_PORT: "70000" })
    expect(result.stdout).toBe("")
    expect(result.stderr).toMatch(/^atc health: \S/)
    expect(result.stderr).toContain("65535")
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
      const result = await cli(["health"], { ATC_PORT: String(misbehaving.port) })
      expect(result.stdout).toBe("")
      expect(result.stderr).toMatch(/^atc health: \S/)
      // Schema decode errors are multi-line by default; the CLI must collapse
      // them to keep the one-line diagnostic contract.
      expect(result.stderr.trim().split("\n")).toHaveLength(1)
      expect(result.exitCode).toBe(1)
    } finally {
      await misbehaving.stop(true)
    }
  })

  test("the removed --url flag is invalid usage and exits 1", async () => {
    const result = await cli(["health", "--url", "http://127.0.0.1:1"])
    expect(result.stderr).toContain("--url")
    expect(result.exitCode).toBe(1)
  })
})

describe("atc project / atc fs (black box)", () => {
  test("the server created and migrated the database in the configured data directory", () => {
    expect(existsSync(join(scratch, "data", "atc", "atc.db"))).toBe(true)
  })

  test("project lifecycle: create, list, get, update, delete", async () => {
    const created = await cli(["project", "create", "--name", "BlackBox", "--directory", scratch])
    expect(created.stderr).toBe("")
    expect(created.exitCode).toBe(0)
    const project = JSON.parse(created.stdout) as { id: string; name: string }
    expect(project.name).toBe("BlackBox")

    const listed = await cli(["project", "list"])
    expect(JSON.parse(listed.stdout)).toEqual([project])

    const fetched = await cli(["project", "get", project.id])
    expect(JSON.parse(fetched.stdout)).toEqual(project)

    const updated = await cli(["project", "update", project.id, "--name", "Renamed"])
    expect((JSON.parse(updated.stdout) as { name: string }).name).toBe("Renamed")

    // Deletion requires explicit confirmation; the CLI never prompts.
    const refused = await cli(["project", "delete", project.id])
    expect(refused.stderr).toContain("--yes")
    expect(refused.exitCode).toBe(1)

    const deleted = await cli(["project", "delete", project.id, "--yes"])
    expect(deleted.stdout).toBe("")
    expect(deleted.stderr).toBe("")
    expect(deleted.exitCode).toBe(0)

    // A second delete of the same id is a real diagnostic, not an empty line.
    const gone = await cli(["project", "delete", project.id, "--yes"])
    expect(gone.stderr.trim()).toBe(`atc project delete: no project with id ${project.id}`)
    expect(gone.exitCode).toBe(1)

    const empty = await cli(["project", "list"])
    expect(JSON.parse(empty.stdout)).toEqual([])
  })

  test("a missing directory is one stderr line and exit 1", async () => {
    const result = await cli([
      "project",
      "create",
      "--name",
      "Nope",
      "--directory",
      join(scratch, "does-not-exist"),
    ])
    expect(result.stdout).toBe("")
    // The tagged API error surfaces as a human diagnostic, never a bare tag
    // or an empty line.
    expect(result.stderr).toMatch(/^atc project create: directory .* does not exist$/m)
    expect(result.exitCode).toBe(1)
  })

  test("fs check resolves relative paths client-side and reports health", async () => {
    const available = await cli(["fs", "check", "."])
    expect(available.exitCode).toBe(0)
    expect((JSON.parse(available.stdout) as { state: string }).state).toBe("available")

    const missing = await cli(["fs", "check", join(scratch, "does-not-exist")])
    expect(missing.exitCode).toBe(0)
    expect((JSON.parse(missing.stdout) as { state: string }).state).toBe("missing")
  })
})
