import { dirname } from "node:path"
import { Console, Effect, FileSystem, Option, Stream } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import { BuildInfo } from "../platform/buildInfo.ts"
import { Subprocess } from "../platform/subprocess.ts"
import * as Cli from "./cli.ts"
import * as Service from "./service.ts"

// `atc upgrade`: reinstall this binary, in place, from its own release
// channel, then restart the service unit (if one is installed) onto the new
// binary. Invariants:
//
// - install.sh stays the single implementation of download/checksum/install.
//   This command fetches it from the repo's main branch and runs it with
//   ATC_CHANNEL/ATC_INSTALL_DIR pointed at this binary — it never
//   re-implements the installer.
// - Only release builds upgrade: the channel is stamped at compile time by
//   the release pipeline, so source runs and local `mise run install` builds
//   have none and are refused with the right alternative.
// - The stable channel is immutable tags, so a version match short-circuits;
//   the dev channel is a rolling tag whose version is unknowable without
//   downloading, so dev always reinstalls.

const REPO = "jeremytondo/atc"
const INSTALLER_URL = `https://raw.githubusercontent.com/${REPO}/main/install.sh`

/** The latest stable release tag (vX.Y.Z), or undefined when unreachable. */
const latestStableTag = Effect.tryPromise(() =>
  fetch(`https://api.github.com/repos/${REPO}/releases/latest`, {
    signal: AbortSignal.timeout(10_000),
    headers: { accept: "application/vnd.github+json" },
  }).then((response) => (response.ok ? response.json() : undefined)),
).pipe(
  Effect.map((body: unknown) => {
    if (typeof body !== "object" || body === null) return undefined
    const tag = (body as { readonly tag_name?: unknown }).tag_name
    return typeof tag === "string" ? tag : undefined
  }),
  Effect.orElseSucceed(() => undefined),
)

/** Run a child, streaming its stdout through; nonzero exit is a reported failure. */
const runStreaming = (
  subprocess: Subprocess["Service"],
  executable: string,
  args: ReadonlyArray<string>,
  env: Record<string, string>,
) =>
  Effect.gen(function* () {
    const child = yield* subprocess.spawn({ executable, args: [...args], env, extendEnv: true })
    yield* Stream.runForEach(child.stdoutLines, (line) => Console.log(line))
    const exitCode = yield* child.exitCode
    if (exitCode !== 0) {
      const stderr = (yield* child.stderrTail).join(" ").trim()
      return yield* Cli.failReported(
        `atc upgrade: ${executable} ${args.join(" ")} failed (exit ${exitCode})${stderr === "" ? "" : `: ${stderr}`}`,
      )
    }
  }).pipe(Effect.scoped)

const channelFlag = Flag.optional(
  Flag.string("channel").pipe(
    Flag.withDescription("Switch release channel: stable or dev (default: this build's channel)"),
  ),
)

export const upgrade = Command.make("upgrade", { channel: channelFlag }, (parsed) =>
  Effect.gen(function* () {
    const build = yield* BuildInfo
    const subprocess = yield* Subprocess
    const fs = yield* FileSystem.FileSystem
    const buildChannel = build.channel
    if (buildChannel === undefined) {
      return yield* Cli.failReported(
        "atc upgrade: this build has no release channel (source run or local build); update with `mise run install`, or install a release build: curl -fsSL " +
          `${INSTALLER_URL} | sh`,
      )
    }
    const channel = Option.getOrElse(parsed.channel, () => buildChannel)
    if (channel !== "stable" && channel !== "dev") {
      return yield* Cli.failReported(
        `atc upgrade: unknown channel ${channel} (expected stable or dev)`,
      )
    }
    if (channel === "stable" && buildChannel === "stable") {
      const latest = yield* latestStableTag
      if (latest === `v${build.version}`) {
        yield* Console.log(`atc v${build.version} is already the latest stable release`)
        return
      }
    }
    const installDir = dirname(process.execPath)
    yield* Console.log(`upgrading atc in ${installDir} (channel: ${channel})`)
    const script = yield* Effect.tryPromise(() =>
      fetch(INSTALLER_URL, { signal: AbortSignal.timeout(30_000) }).then((response) => {
        if (!response.ok) throw new Error(`fetching install.sh failed (HTTP ${response.status})`)
        return response.text()
      }),
    )
    yield* Effect.gen(function* () {
      const tmpDir = yield* fs.makeTempDirectoryScoped()
      const installer = `${tmpDir}/install.sh`
      yield* fs.writeFileString(installer, script)
      yield* runStreaming(subprocess, "sh", [installer], {
        ATC_CHANNEL: channel,
        ATC_INSTALL_DIR: installDir,
      })
    }).pipe(Effect.scoped)
    // The service unit points at the installed path, so a reinstall restarts
    // it onto the new binary; run it via that binary so new code does the work.
    const unitFile = yield* Service.installedUnitFile
    if (unitFile !== undefined) {
      yield* Console.log(`restarting ${unitFile} onto the new binary`)
      yield* runStreaming(subprocess, `${installDir}/atc`, ["service", "install"], {})
    }
    const installedVersion = yield* Effect.scoped(
      Effect.gen(function* () {
        const child = yield* subprocess.spawn({
          executable: `${installDir}/atc`,
          args: ["--version"],
          env: {},
          extendEnv: true,
        })
        const lines = yield* Stream.runCollect(child.stdoutLines)
        yield* child.exitCode
        return lines.join(" ").trim()
      }),
    )
    yield* Console.log(`upgraded: atc v${build.version} -> ${installedVersion}`)
  }).pipe(Cli.withSettledConfig("atc upgrade")),
).pipe(Command.withDescription("Reinstall atc from its release channel and restart the service"))
