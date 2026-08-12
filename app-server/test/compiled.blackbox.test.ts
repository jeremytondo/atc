import { existsSync } from "node:fs"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { describe, expect, test } from "vitest"
import {
  compiledBinaryPath,
  isolatedEnv,
  runCli,
  spawnServe,
  spawnServeHealthy,
  waitForHealth,
} from "./blackbox.ts"

// Black-box validation of the compiled artifact: the standalone executable
// must serve health/version with real injected build metadata, create and
// migrate its database in the configured data directory, survive a restart as
// a clean no-op, resolve flag-less CLI commands from the same configuration,
// enforce the loopback trust boundary, and shut down cleanly — all from a
// working directory outside the repository.
//
// Opt-in (mise run test:compiled) so the default fast suite never depends on
// a build; CI runs it natively on macOS arm64 and Linux x64.

const enabled =
  process.env["ATC_COMPILED_TEST"] === "1" || process.env["ATC_COMPILED_BIN"] !== undefined

describe.skipIf(!enabled)("compiled atc artifact (opt-in: mise run test:compiled)", () => {
  test("serves, persists, restarts cleanly, and enforces local trust from outside the repository", async () => {
    expect(
      existsSync(compiledBinaryPath),
      `missing compiled artifact at ${compiledBinaryPath}; run \`mise run build\``,
    ).toBe(true)

    // A scratch cwd and isolated XDG locations outside the repository:
    // compiled behavior must depend on neither the working directory nor the
    // developer's real config/data/state.
    const outsideCwd = mkdtempSync(join(tmpdir(), "atc-compiled-"))
    // The env is rebuilt per spawn attempt: it carries the port, and each
    // isolatedEnv call allocates a fresh state home, which must be the one
    // the surviving server actually used.
    let env!: ReturnType<typeof isolatedEnv>
    const first = await spawnServeHealthy((attemptPort) => {
      env = isolatedEnv(outsideCwd, { ATC_PORT: String(attemptPort) })
      return spawnServe([compiledBinaryPath], attemptPort, outsideCwd, env)
    })
    const { port, base, health } = first
    const dbFile = join(outsideCwd, "data", "atc", "atc.db")
    let proc = first.proc
    try {
      expect(health.status).toBe(200)
      expect(await health.json()).toEqual({ status: "ok" })

      // First boot created and migrated the database in the configured
      // data directory, and the log file landed in the state directory.
      expect(existsSync(dbFile)).toBe(true)
      expect(existsSync(join(env.XDG_STATE_HOME, "atc", "atc.log"))).toBe(true)

      const version = await fetch(`${base}/api/v1/version`)
      expect(version.status).toBe(200)
      const body = (await version.json()) as {
        version: string
        apiVersion: string
        commit: string
        builtAt: string
      }
      expect(body.apiVersion).toBe("v1")
      // Compiled builds must carry real injected metadata, not dev fallbacks.
      // Local builds from an edited tree carry a -dirty marker; CI is exact.
      expect(body.commit).toMatch(/^[0-9a-f]{40}(-dirty)?$/)
      expect(Number.isNaN(Date.parse(body.builtAt))).toBe(false)

      // The admin UI ships inside the executable: pages and hashed assets
      // are embedded at compile time, served from a cwd outside the repo.
      const home = await fetch(`${base}/`)
      expect(home.status).toBe(200)
      expect(home.headers.get("content-type")).toContain("text/html")
      const homeHtml = await home.text()
      const docs = await fetch(`${base}/docs`)
      expect(docs.status).toBe(200)
      const assetPath = homeHtml.match(/_app\/immutable\/entry\/[\w.-]+\.js/)?.[0]
      expect(assetPath).toBeDefined()
      const asset = await fetch(`${base}/${assetPath}`)
      expect(asset.status).toBe(200)
      expect(asset.headers.get("cache-control")).toContain("immutable")
      expect((await fetch(`${base}/no-such-page`)).status).toBe(404)

      // The local trust boundary ships in the executable: cross-origin
      // browser requests are rejected.
      const crossOrigin = await fetch(`${base}/api/v1/health`, {
        headers: { Origin: "http://evil.example" },
      })
      expect(crossOrigin.status).toBe(403)

      // Flag-less CLI commands resolve the server from the same settled
      // configuration the server booted from.
      const healthCli = await runCli([compiledBinaryPath], ["health"], outsideCwd, env)
      expect(healthCli.stdout).toBe('{\n  "status": "ok"\n}\n')
      expect(healthCli.stderr).toBe("")
      expect(healthCli.exitCode).toBe(0)

      const versionCli = await runCli([compiledBinaryPath], ["version"], outsideCwd, env)
      expect(JSON.parse(versionCli.stdout)).toEqual(body)
      expect(versionCli.exitCode).toBe(0)

      // The service command group ships in the executable. Status checks the
      // unit file before ever touching launchctl/systemctl, so with HOME and
      // XDG_CONFIG_HOME isolated this never reaches the host's supervisor.
      const serviceStatus = await runCli([compiledBinaryPath], ["service", "status"], outsideCwd, {
        ...env,
        HOME: outsideCwd,
      })
      expect(serviceStatus.exitCode).toBe(1)
      expect(serviceStatus.stderr).toContain("not installed")

      // The full persistence stack works in the compiled binary.
      const created = await runCli(
        [compiledBinaryPath],
        ["project", "create", "--name", "Compiled", "--directory", outsideCwd],
        outsideCwd,
        env,
      )
      expect(created.stderr).toBe("")
      expect(created.exitCode).toBe(0)
      const project = JSON.parse(created.stdout) as { id: string }

      proc.kill("SIGTERM")
      expect(await proc.exited).toBe(130)

      // Restart against the same data directory: migrations are a clean
      // no-op and the durable record is still there.
      proc = spawnServe([compiledBinaryPath], port, outsideCwd, env)
      const restarted = await waitForHealth(base, proc)
      expect(restarted.status).toBe(200)
      const listed = await runCli([compiledBinaryPath], ["project", "list"], outsideCwd, env)
      expect((JSON.parse(listed.stdout) as Array<{ id: string }>).map((p) => p.id)).toEqual([
        project.id,
      ])

      proc.kill("SIGTERM")
      expect(await proc.exited).toBe(130)
    } finally {
      proc.kill()
      rmSync(outsideCwd, { recursive: true, force: true })
    }
  }, 60_000)
})
