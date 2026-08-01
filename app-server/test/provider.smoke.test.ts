import { existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { describe, expect, test } from "vitest"
import { compiledBinaryPath } from "./blackbox.ts"

// Live provider smoke tests: run the hidden `smoke` command of the compiled
// executable against the real providers. Opt-in (mise run test:smoke) because
// they need an installed, authenticated Codex CLI and Claude Code credentials.
// Prerequisites are checked here so failures are actionable, and each smoke
// runs from a working directory outside the repository.

const enabled = process.env["ATC_SMOKE"] === "1"

const runSmoke = async (provider: "codex" | "claude") => {
  const proc = Bun.spawn([compiledBinaryPath, "smoke", provider], {
    cwd: tmpdir(),
    stdout: "pipe",
    stderr: "pipe",
  })
  const exitCode = await proc.exited
  const stdout = await new Response(proc.stdout as ReadableStream).text()
  const stderr = await new Response(proc.stderr as ReadableStream).text()
  expect(exitCode, `atc smoke ${provider} failed\nstdout:\n${stdout}\nstderr:\n${stderr}`).toBe(0)
  expect(stdout).toContain("PASS")
}

describe.skipIf(!enabled)("live provider smoke (opt-in: mise run test:smoke)", () => {
  test("compiled artifact exists", () => {
    expect(
      existsSync(compiledBinaryPath),
      `missing compiled artifact at ${compiledBinaryPath}; run \`mise run build\``,
    ).toBe(true)
  })

  test("codex app-server round trip", async () => {
    const resolvable =
      process.env["ATC_CODEX_EXECUTABLE"] !== undefined || Bun.which("codex") !== null
    expect(
      resolvable,
      "codex CLI not found; install and authenticate Codex or set ATC_CODEX_EXECUTABLE",
    ).toBe(true)
    await runSmoke("codex")
  }, 120_000)

  test("Claude Agent SDK round trip", async () => {
    const staged = join(dirname(compiledBinaryPath), "claude")
    const resolvable = process.env["ATC_CLAUDE_CODE_EXECUTABLE"] !== undefined || existsSync(staged)
    expect(
      resolvable,
      `packaged Claude Code executable not staged at ${staged}; run \`mise run build\` or set ATC_CLAUDE_CODE_EXECUTABLE`,
    ).toBe(true)
    await runSmoke("claude")
  }, 300_000)
})
