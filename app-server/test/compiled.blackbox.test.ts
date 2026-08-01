import { existsSync } from "node:fs"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { describe, expect, test } from "vitest"
import { compiledBinaryPath, freePort, spawnServe, waitForHealth } from "./blackbox.ts"

// Black-box validation of the compiled artifact: the standalone executable
// must serve health/version with real injected build metadata and shut down
// cleanly, all from a working directory outside the repository.
//
// Opt-in (mise run test:compiled) so the default fast suite never depends on
// a build; CI runs it natively on macOS arm64 and Linux x64.

const enabled =
  process.env["ATC_COMPILED_TEST"] === "1" || process.env["ATC_COMPILED_BIN"] !== undefined

describe.skipIf(!enabled)("compiled atc artifact (opt-in: mise run test:compiled)", () => {
  test("serves health/version with injected build metadata from outside the repository and exits cleanly on SIGTERM", async () => {
    expect(
      existsSync(compiledBinaryPath),
      `missing compiled artifact at ${compiledBinaryPath}; run \`mise run build\``,
    ).toBe(true)

    // A scratch cwd outside the repository: compiled behavior must not
    // depend on the repository working directory.
    const outsideCwd = mkdtempSync(join(tmpdir(), "atc-compiled-"))
    const port = await freePort()
    const base = `http://127.0.0.1:${port}`
    const proc = spawnServe([compiledBinaryPath], port, outsideCwd)
    try {
      const health = await waitForHealth(base, proc)
      expect(health.status).toBe(200)
      expect(await health.json()).toEqual({ status: "ok" })

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

      proc.kill("SIGTERM")
      expect(await proc.exited).toBe(130)
    } finally {
      proc.kill()
      rmSync(outsideCwd, { recursive: true, force: true })
    }
  }, 30_000)
})
