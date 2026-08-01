// Compiles src/main.ts into standalone `atc` executables with Bun.
//
// Usage: bun run scripts/build.ts [--all]
//   default  compile for the host platform only
//   --all    cross-compile every release target
//
// Output names are deterministic (dist/atc-<os>-<arch>) so CI and release
// tooling can consume them without discovery logic.

import { existsSync } from "node:fs"
import { copyFile, mkdir } from "node:fs/promises"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

export const TARGETS = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64"] as const
type Target = (typeof TARGETS)[number]

const appServerRoot = fileURLToPath(new URL("..", import.meta.url))

const hostTarget = (): Target => {
  const target = `${process.platform}-${process.arch}`
  if (!(TARGETS as readonly string[]).includes(target)) {
    throw new Error(`unsupported host platform ${target}; expected one of: ${TARGETS.join(", ")}`)
  }
  return target as Target
}

const run = (cmd: ReadonlyArray<string>): string => {
  const result = Bun.spawnSync([...cmd], { cwd: appServerRoot, stdout: "pipe", stderr: "inherit" })
  if (result.exitCode !== 0) {
    throw new Error(`${cmd.join(" ")} failed with exit code ${result.exitCode}`)
  }
  return result.stdout.toString().trim()
}

const commit = run(["git", "rev-parse", "HEAD"])
const builtAt = new Date().toISOString()
const targets: ReadonlyArray<Target> = process.argv.includes("--all") ? TARGETS : [hostTarget()]

for (const target of targets) {
  const outfile = `dist/atc-${target}`
  run([
    process.execPath,
    "build",
    "--compile",
    `--target=bun-${target}`,
    // A stray .env or bunfig.toml in whatever directory the executable starts
    // from must not change server behavior.
    "--no-compile-autoload-dotenv",
    "--no-compile-autoload-bunfig",
    // The define value is a JS expression, so a string needs its quotes.
    `--define=ATC_BUILD_COMMIT=${JSON.stringify(commit)}`,
    `--define=ATC_BUILD_BUILT_AT=${JSON.stringify(builtAt)}`,
    `--outfile=${outfile}`,
    "src/main.ts",
  ])
  console.log(`built ${outfile} (commit ${commit.slice(0, 12)}, ${builtAt})`)
}

// Stage the Claude Agent SDK's packaged platform-specific Claude Code binary
// next to the compiled executable. The smoke command resolves it relative to
// the executable's own location (never the working directory). Bun only
// installs the host's platform package, so cross-compiled targets ship
// without it until the release milestone settles per-target acquisition.
const claudeSource = join(
  appServerRoot,
  "node_modules",
  `@anthropic-ai/claude-agent-sdk-${hostTarget()}`,
  "claude",
)
if (targets.includes(hostTarget()) && existsSync(claudeSource)) {
  await mkdir(join(appServerRoot, "dist"), { recursive: true })
  await copyFile(claudeSource, join(appServerRoot, "dist", "claude"))
  console.log("staged dist/claude (packaged Claude Code executable for the host platform)")
}
