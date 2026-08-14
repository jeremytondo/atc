// Runs the real oxlint binary against an in-memory fixture with exactly one
// atc plugin rule enabled, so each rule is tested end to end through oxlint's
// plugin host rather than against a mocked context. Per-file exemptions live
// in .oxlintrc.json overrides, not in the rules, so fixtures here never need
// them; the standing `mise run lint` over the repo covers the override side.
import { mkdtemp, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { fileURLToPath } from "node:url"
import { expect, test } from "vitest"

// fileURLToPath, not URL.pathname: the latter percent-encodes spaces and
// similar characters, which oxlint would then fail to resolve.
const pluginPath = fileURLToPath(new URL("../../lint/index.ts", import.meta.url))
const oxlintBin = join(Bun.resolveSync("oxlint/package.json", process.cwd()), "..", "bin/oxlint")

const run = async (rule: string, source: string) => {
  const dir = await mkdtemp(join(tmpdir(), "atc-oxlint-"))
  const configPath = join(dir, ".oxlintrc.json")
  const fixturePath = join(dir, "fixture.ts")
  await writeFile(
    configPath,
    JSON.stringify({ jsPlugins: [pluginPath], rules: { [rule]: "error" } }),
  )
  await writeFile(fixturePath, source)

  const proc = Bun.spawnSync([process.execPath, oxlintBin, "--config", configPath, fixturePath], {
    cwd: dir,
    stdout: "pipe",
    stderr: "pipe",
  })
  return { exitCode: proc.exitCode, output: `${proc.stdout.toString()}${proc.stderr.toString()}` }
}

/** Vitest cases asserting oxlint accepts or reports the given sources. */
export const ruleTests = (rule: string) => ({
  valid(name: string, source: string) {
    test(`${rule}: allows ${name}`, async () => {
      const result = await run(rule, source)
      // Exit code is the signal: oxlint's success summary ("Found 0 warnings
      // and 0 errors.") itself contains the word "error".
      expect(result.output).not.toContain(`(${rule})`)
      expect(result.exitCode).toBe(0)
    })
  },
  invalid(name: string, source: string, messageMatch?: string) {
    test(`${rule}: reports ${name}`, async () => {
      const result = await run(rule, source)
      expect(result.exitCode).not.toBe(0)
      const [pluginName, shortName] = rule.split("/")
      expect(result.output).toContain(`${pluginName}(${shortName})`)
      if (messageMatch !== undefined) expect(result.output).toContain(messageMatch)
    })
  },
})
