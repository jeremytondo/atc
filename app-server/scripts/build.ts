// Compiles src/main.ts into standalone `atc` executables with Bun.
//
// Usage: bun run scripts/build.ts [options] [target ...]
//   default          compile for the host platform only
//   --all            cross-compile every release target
//   --version=X.Y.Z  stamp a release version (default: package.json version)
//   --channel=C      stamp a release channel (stable|dev); local builds carry
//                    none, which makes `atc upgrade` refuse to touch them
//   --commit=SHA      stamp an explicit source commit (release builds only)
//   --built-at=TIME   stamp the shared ISO-8601 product build time
//   target ...       compile exactly these targets (release CI builds darwin
//                    targets on macOS so Bun's ad-hoc code signature is
//                    applied natively)
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

const args = process.argv.slice(2)
// A typo'd flag must not silently release with the package.json fallback
// version, so anything dash-prefixed and unrecognized is fatal.
const unknownFlags = args.filter(
  (arg) =>
    arg.startsWith("--") &&
    arg !== "--all" &&
    !arg.startsWith("--version=") &&
    !arg.startsWith("--channel=") &&
    !arg.startsWith("--commit=") &&
    !arg.startsWith("--built-at="),
)
if (unknownFlags.length > 0) {
  throw new Error(`unknown flag(s): ${unknownFlags.join(", ")}`)
}
const version = args.find((arg) => arg.startsWith("--version="))?.slice("--version=".length)
const channel = args.find((arg) => arg.startsWith("--channel="))?.slice("--channel=".length)
const explicitCommit = args.find((arg) => arg.startsWith("--commit="))?.slice("--commit=".length)
const explicitBuiltAt = args
  .find((arg) => arg.startsWith("--built-at="))
  ?.slice("--built-at=".length)
const releaseTimestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/
if (channel !== undefined && channel !== "stable" && channel !== "dev") {
  throw new Error(`unknown channel ${channel}; expected stable or dev`)
}
if (version === "") {
  throw new Error("--version must not be empty")
}
if (explicitCommit === "") {
  throw new Error("--commit must not be empty")
}
if (explicitBuiltAt !== undefined) {
  const parsedBuiltAt = Date.parse(explicitBuiltAt)
  const canonicalBuiltAt = isNaN(parsedBuiltAt)
    ? ""
    : new Date(parsedBuiltAt).toISOString().replace(".000Z", "Z")
  if (!releaseTimestampPattern.test(explicitBuiltAt) || canonicalBuiltAt !== explicitBuiltAt) {
    throw new Error("--built-at must be a valid UTC timestamp in YYYY-MM-DDTHH:MM:SSZ format")
  }
}
if (
  channel !== undefined &&
  (version === undefined || explicitCommit === undefined || explicitBuiltAt === undefined)
) {
  throw new Error(
    "release builds require --version, --commit, and --built-at together with --channel",
  )
}

// Local builds describe the working tree. Release builds receive one identity
// computed before the product jobs fan out, so every artifact carries the same
// commit and timestamp.
const head = run(["git", "rev-parse", "HEAD"])
const workingTreeDirty = run(["git", "status", "--porcelain"]) !== ""
if (explicitCommit !== undefined && explicitCommit !== head) {
  throw new Error(`release commit ${explicitCommit} does not match checked-out HEAD ${head}`)
}
if (channel !== undefined && workingTreeDirty) {
  throw new Error("release builds require a clean working tree")
}
const dirty = workingTreeDirty ? "-dirty" : ""
const commit = explicitCommit ?? head + dirty
const builtAt = explicitBuiltAt ?? new Date().toISOString()
const requested = args.filter((arg) => !arg.startsWith("--"))
const invalid = requested.filter((target) => !(TARGETS as readonly string[]).includes(target))
if (invalid.length > 0) {
  throw new Error(`unknown target(s) ${invalid.join(", ")}; expected: ${TARGETS.join(", ")}`)
}

const targets: ReadonlyArray<Target> = args.includes("--all")
  ? TARGETS
  : requested.length > 0
    ? (requested as ReadonlyArray<Target>)
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
    ...(version === undefined ? [] : [`--define=ATC_BUILD_VERSION=${JSON.stringify(version)}`]),
    ...(channel === undefined ? [] : [`--define=ATC_BUILD_CHANNEL=${JSON.stringify(channel)}`]),
    `--outfile=${outfile}`,
    "src/main.ts",
  ])
  console.log(
    `built ${outfile} (${version === undefined ? "" : `version ${version}, `}commit ${commit.slice(0, 12)}${workingTreeDirty ? " (dirty)" : ""}, ${builtAt})`,
  )
}
