// Compiles src/main.ts into standalone `atc` executables with Bun.
//
// Usage: bun run scripts/build.ts [--all]
//   default  compile for the host platform only
//   --all    cross-compile every release target
//
// Output names are deterministic (dist/atc-<os>-<arch>) so CI and release
// tooling can consume them without discovery logic.

import { fileURLToPath } from "node:url"

// Keep in sync with the artifact checks in .github/workflows/app-server-ci.yml.
const TARGETS = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64"] as const
type Target = (typeof TARGETS)[number]

const appServerRoot = fileURLToPath(new URL("..", import.meta.url))

const host = `${process.platform}-${process.arch}`
const hostTarget: Target | null = (TARGETS as readonly string[]).includes(host)
  ? (host as Target)
  : null

const run = (cmd: ReadonlyArray<string>): string => {
  const result = Bun.spawnSync([...cmd], { cwd: appServerRoot, stdout: "pipe", stderr: "inherit" })
  if (result.exitCode !== 0) {
    throw new Error(`${cmd.join(" ")} failed with exit code ${result.exitCode}`)
  }
  return result.stdout.toString().trim()
}

// The stamped commit is git HEAD (the working-copy parent in a colocated jj
// repo), so mark builds whose tree differs from it.
const dirty = run(["git", "status", "--porcelain"]) === "" ? "" : "-dirty"
const commit = run(["git", "rev-parse", "HEAD"]) + dirty
const builtAt = new Date().toISOString()

const targets: ReadonlyArray<Target> = process.argv.includes("--all")
  ? TARGETS
  : hostTarget !== null
    ? [hostTarget]
    : []
if (targets.length === 0) {
  throw new Error(`unsupported host platform ${host}; expected one of: ${TARGETS.join(", ")}`)
}

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
  console.log(`built ${outfile} (commit ${commit.slice(0, 12)}${dirty}, ${builtAt})`)
}
