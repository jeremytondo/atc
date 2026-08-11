import { homedir } from "node:os"
import { dirname, resolve } from "node:path"
import { Console, Effect, FileSystem, Stream } from "effect"
import { Command } from "effect/unstable/cli"
import * as AuthToken from "../platform/authToken.ts"
import { BuildInfo } from "../platform/buildInfo.ts"
import { AppConfig } from "../platform/config.ts"
import { Subprocess } from "../platform/subprocess.ts"
import * as Server from "../server.ts"
import * as Cli from "./cli.ts"

// Supervisor-managed service installation (ATC-127): `atc service` writes a
// user-scope unit — a launchd LaunchAgent on macOS, a systemd user unit on
// Linux (systemd >= 240 for append: logging) — that runs `atc serve` in the
// foreground and restarts it when it dies. This is deliberately separate
// from `atc start/stop/status` (serve.ts), which self-manages an
// unsupervised background process via a pidfile: a supervisor-run server
// never writes a pidfile, so the two families cannot see each other's
// processes. Invariants:
//
// - The unit runs flagless `atc serve`, so the service reads the config-file
//   and default layers of the settled configuration. Supervisors start
//   services with a minimal environment — shell exports (ATC_*, PATH) do NOT
//   reach it — so the installing shell's PATH is stamped into the unit;
//   without it the service could not resolve zmx or the provider CLIs.
//   Everything else belongs in config.toml.
// - Unit rendering is pure and test-covered; launchctl/systemctl calls stay
//   thin, and their failures surface as one diagnostic carrying the
//   supervisor's own stderr. install/start only report success after the
//   server answers its health endpoint.
// - "stop" means "until next login" on both platforms: launchd bootout /
//   systemctl stop leave the unit installed (RunAtLoad / enable), matching
//   supervisor convention. `uninstall` is the way out.
// - launchd operations try the gui domain first and fall back to the user
//   domain, so installs over ssh to a Mac without a console session work.

/** The launchd label / systemd unit name; also the unit file basenames. */
export const serviceName = "atc.server"

const escapeXml = (value: string): string =>
  value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")

// systemd unit files C-unescape inside double quotes and expand % specifiers
// everywhere, so both need escaping along with the quotes themselves.
const escapeUnitValue = (value: string): string =>
  value.replaceAll("\\", "\\\\").replaceAll('"', '\\"').replaceAll("%", "%%")

/**
 * The command a supervisor should run: the compiled binary itself, or the
 * bun runtime plus the absolute entry script for a source run (absolute
 * because unit files execute from the user's home, not this cwd).
 */
export const programArguments = (devCommit: boolean): ReadonlyArray<string> =>
  devCommit
    ? [process.execPath, resolve(process.argv[1] ?? "src/main.ts"), "serve"]
    : [process.execPath, "serve"]

/** LaunchAgent plist: KeepAlive relaunches the server whenever it exits. */
export const launchAgentPlist = (
  args: ReadonlyArray<string>,
  logFile: string,
  path: string,
): string => `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${serviceName}</string>
  <key>ProgramArguments</key>
  <array>
${args.map((arg) => `    <string>${escapeXml(arg)}</string>`).join("\n")}
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>${escapeXml(path)}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${escapeXml(logFile)}</string>
  <key>StandardErrorPath</key>
  <string>${escapeXml(logFile)}</string>
</dict>
</plist>
`

/** systemd user unit: Restart=always with backoff; logs append to `logFile`. */
export const systemdUnit = (
  args: ReadonlyArray<string>,
  logFile: string,
  path: string,
): string => `[Unit]
Description=ATC App Server

[Service]
Type=simple
ExecStart=${args.map((arg) => `"${escapeUnitValue(arg)}"`).join(" ")}
Environment="PATH=${escapeUnitValue(path)}"
Restart=always
RestartSec=5
StandardOutput=append:${logFile}
StandardError=append:${logFile}

[Install]
WantedBy=default.target
`

export const launchAgentPath = (home: string): string =>
  `${home}/Library/LaunchAgents/${serviceName}.plist`

/** systemd's own config-home convention (not an atc path — see header). */
export const systemdUnitPath = (env: {
  readonly XDG_CONFIG_HOME?: string | undefined
  readonly HOME?: string | undefined
}): string =>
  `${env.XDG_CONFIG_HOME ?? `${env.HOME ?? homedir()}/.config`}/systemd/user/${serviceName}.service`

type Platform = "darwin" | "linux"

const currentPlatform =
  process.platform === "darwin" || process.platform === "linux"
    ? Effect.succeed(process.platform)
    : Cli.failReported(
        `atc service: unsupported platform ${process.platform} (launchd on macOS, systemd user units on Linux)`,
      )

const installerPath = () => process.env["PATH"] ?? "/usr/local/bin:/usr/bin:/bin"

/** One thin supervisor call; failure carries the tool's stderr. Stdout is
 * drained per the Subprocess contract (and `print` output is discarded). */
const run = (
  subprocess: Subprocess["Service"],
  command: string,
  executable: string,
  args: ReadonlyArray<string>,
) =>
  Effect.gen(function* () {
    const child = yield* subprocess.spawn({ executable, args: [...args], env: {}, extendEnv: true })
    yield* Stream.runDrain(child.stdoutLines).pipe(Effect.ignore)
    const exitCode = yield* child.exitCode
    if (exitCode !== 0) {
      const stderr = (yield* child.stderrTail).join(" ").trim()
      return yield* Cli.failReported(
        `atc service ${command}: ${executable} ${args.join(" ")} failed (exit ${exitCode})${stderr === "" ? "" : `: ${stderr}`}`,
      )
    }
  }).pipe(Effect.scoped)

/** Same call, but a non-zero exit is an expected state, not a failure. */
const runIgnoring = (
  subprocess: Subprocess["Service"],
  executable: string,
  args: ReadonlyArray<string>,
) =>
  Effect.gen(function* () {
    const child = yield* subprocess.spawn({ executable, args: [...args], env: {}, extendEnv: true })
    yield* Stream.runDrain(child.stdoutLines).pipe(Effect.ignore)
    return yield* child.exitCode
  }).pipe(
    Effect.scoped,
    Effect.orElseSucceed(() => -1),
  )

// gui first (normal console login), user as the ssh/headless fallback: the
// gui domain does not exist without an Aqua session.
// launchd exists only where getuid does (macOS); reaching this without one
// is a bug, so it dies rather than modeling a failure nothing can handle.
const launchdDomains: Effect.Effect<ReadonlyArray<string>> = Effect.suspend(() => {
  const uid = process.getuid?.()
  return uid === undefined
    ? Effect.die(new Error("launchd domains require a POSIX uid"))
    : Effect.succeed([`gui/${uid}`, `user/${uid}`])
})

/** The domain the agent is currently loaded in, or undefined. */
const launchdLoadedDomain = (subprocess: Subprocess["Service"]) =>
  Effect.gen(function* () {
    for (const domain of yield* launchdDomains) {
      if (
        (yield* runIgnoring(subprocess, "launchctl", ["print", `${domain}/${serviceName}`])) === 0
      ) {
        return domain
      }
    }
    return undefined
  })

const launchdLoaded = (subprocess: Subprocess["Service"]) =>
  launchdLoadedDomain(subprocess).pipe(Effect.map((domain) => domain !== undefined))

/** Unload from every domain and wait until launchd forgets the label —
 * bootstrapping again while the old instance is still winding down fails. */
const launchdUnload = (subprocess: Subprocess["Service"]) =>
  Effect.gen(function* () {
    for (const domain of yield* launchdDomains) {
      yield* runIgnoring(subprocess, "launchctl", ["bootout", `${domain}/${serviceName}`])
    }
    yield* Cli.pollUntil(
      5_000,
      100,
      launchdLoaded(subprocess).pipe(Effect.map((loaded) => !loaded)),
    )
  })

/** Bootstrap into the first domain that accepts it; report the last failure. */
const launchdBootstrap = (subprocess: Subprocess["Service"], command: string, unitFile: string) =>
  Effect.gen(function* () {
    const domains = yield* launchdDomains
    for (const domain of domains.slice(0, -1)) {
      if ((yield* runIgnoring(subprocess, "launchctl", ["bootstrap", domain, unitFile])) === 0) {
        return
      }
    }
    const fallback = domains.at(-1)
    if (fallback === undefined) return yield* Effect.die(new Error("no launchd domains"))
    yield* run(subprocess, command, "launchctl", ["bootstrap", fallback, unitFile])
  })

const unitFileFor = (platform: Platform): string =>
  platform === "darwin"
    ? launchAgentPath(homedir())
    : systemdUnitPath({
        XDG_CONFIG_HOME: process.env["XDG_CONFIG_HOME"],
        HOME: process.env["HOME"],
      })

/** Success only when the served address answers health within the deadline. */
const awaitHealthy = (
  command: string,
  config: AppConfig["Service"],
  token: string | undefined,
  serviceLog: string,
) =>
  Effect.gen(function* () {
    const probeHost = Cli.reachableHost(config.bind)
    const healthy = yield* Cli.pollUntil(
      15_000,
      150,
      Cli.probeHealth(probeHost, config.port, 1_000, token),
    )
    if (!healthy) {
      return yield* Cli.failReported(
        `atc service ${command}: the server did not become healthy within 15s; logs: ${config.logFile} (supervisor: ${serviceLog})`,
      )
    }
  })

const install = Command.make("install", {}, () =>
  Effect.gen(function* () {
    const platform = yield* currentPlatform
    const config = yield* AppConfig
    const build = yield* BuildInfo
    const fs = yield* FileSystem.FileSystem
    const subprocess = yield* Subprocess
    const args = programArguments(build.commit === "dev")
    const serviceLog = `${config.stateDir}/service.log`
    const unitFile = unitFileFor(platform)
    // Re-running install over our own service is the update path (the unit
    // points at the stable installed binary, so a reinstall restarts onto
    // it). Only an UNMANAGED responder is a conflict: it would make the
    // supervised server crash-loop on its bind while this install claimed a
    // process it does not manage.
    const ownService =
      platform === "darwin"
        ? yield* launchdLoaded(subprocess)
        : (yield* runIgnoring(subprocess, "systemctl", [
            "--user",
            "is-active",
            "--quiet",
            serviceName,
          ])) === 0
    const probeHost = Cli.reachableHost(config.bind)
    const token = Cli.probeNeedsToken(probeHost) ? yield* AuthToken.ensure : undefined
    if (!ownService && (yield* Cli.probeHealth(probeHost, config.port, 1_000, token))) {
      return yield* Cli.failReported(
        `atc service install: something is already serving on port ${config.port} (a foreground \`atc serve\` or \`atc start\`?); stop it first`,
      )
    }
    yield* fs.makeDirectory(config.stateDir, { recursive: true })
    yield* fs.makeDirectory(dirname(unitFile), { recursive: true })
    if (platform === "darwin") {
      yield* fs.writeFileString(unitFile, launchAgentPlist(args, serviceLog, installerPath()))
      yield* launchdUnload(subprocess)
      yield* launchdBootstrap(subprocess, "install", unitFile)
    } else {
      yield* fs.writeFileString(unitFile, systemdUnit(args, serviceLog, installerPath()))
      yield* run(subprocess, "install", "systemctl", ["--user", "daemon-reload"])
      yield* run(subprocess, "install", "systemctl", ["--user", "enable", serviceName])
      // restart, not `enable --now`: --now is a no-op on an already-active
      // unit, which would leave a reinstall running the old binary/unit.
      yield* run(subprocess, "install", "systemctl", ["--user", "restart", serviceName])
    }
    yield* awaitHealthy("install", config, token, serviceLog)
    yield* Console.log(`installed and started ${serviceName} (${unitFile})`)
    yield* Console.log(`server: http://${Server.hostForUrl(config.bind)}:${config.port}`)
    yield* Console.log(`logs: ${config.logFile} (supervisor: ${serviceLog})`)
    if (platform === "linux") {
      yield* Console.log(
        "headless machines: run `loginctl enable-linger` once so the service outlives logins",
      )
    }
  }).pipe(Cli.withSettledConfig("atc service install")),
).pipe(Command.withDescription("Install and start the user-scope service unit (launchd/systemd)"))

const uninstall = Command.make("uninstall", {}, () =>
  Effect.gen(function* () {
    const platform = yield* currentPlatform
    const fs = yield* FileSystem.FileSystem
    const subprocess = yield* Subprocess
    const unitFile = unitFileFor(platform)
    if (platform === "darwin") {
      yield* launchdUnload(subprocess)
    } else {
      yield* runIgnoring(subprocess, "systemctl", ["--user", "disable", "--now", serviceName])
    }
    const existed = yield* fs.exists(unitFile).pipe(Effect.orElseSucceed(() => false))
    yield* fs.remove(unitFile).pipe(Effect.ignore)
    if (platform === "linux") {
      yield* runIgnoring(subprocess, "systemctl", ["--user", "daemon-reload"])
    }
    yield* Console.log(
      existed ? `uninstalled ${serviceName}` : `${serviceName} was not installed; nothing removed`,
    )
  }).pipe(Cli.withSettledConfig("atc service uninstall")),
).pipe(Command.withDescription("Stop the service and remove its unit file"))

const start = Command.make("start", {}, () =>
  Effect.gen(function* () {
    const platform = yield* currentPlatform
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const subprocess = yield* Subprocess
    const unitFile = unitFileFor(platform)
    if (!(yield* fs.exists(unitFile).pipe(Effect.orElseSucceed(() => false)))) {
      return yield* Cli.failReported(
        `atc service start: ${unitFile} does not exist; run \`atc service install\` first`,
      )
    }
    if (platform === "darwin") {
      // Loading starts it (RunAtLoad); when already loaded, kickstart covers
      // the stopped-but-loaded case in whichever domain holds it.
      const loadedDomain = yield* launchdLoadedDomain(subprocess)
      if (loadedDomain === undefined) {
        yield* launchdBootstrap(subprocess, "start", unitFile)
      } else {
        yield* run(subprocess, "start", "launchctl", [
          "kickstart",
          `${loadedDomain}/${serviceName}`,
        ])
      }
    } else {
      yield* run(subprocess, "start", "systemctl", ["--user", "start", serviceName])
    }
    const probeHost = Cli.reachableHost(config.bind)
    const token = Cli.probeNeedsToken(probeHost)
      ? yield* AuthToken.read.pipe(Effect.orElseSucceed(() => undefined))
      : undefined
    yield* awaitHealthy("start", config, token, `${config.stateDir}/service.log`)
    yield* Console.log(
      `started ${serviceName} on http://${Server.hostForUrl(config.bind)}:${config.port}`,
    )
  }).pipe(Cli.withSettledConfig("atc service start")),
).pipe(Command.withDescription("Start the installed service"))

const stop = Command.make("stop", {}, () =>
  Effect.gen(function* () {
    const platform = yield* currentPlatform
    const subprocess = yield* Subprocess
    if (platform === "darwin") {
      if (!(yield* launchdLoaded(subprocess))) {
        yield* Console.log(`${serviceName} is not running`)
        return
      }
      yield* launchdUnload(subprocess)
    } else {
      // systemctl stop is already a no-op success on an inactive unit.
      yield* run(subprocess, "stop", "systemctl", ["--user", "stop", serviceName])
    }
    yield* Console.log(`stopped ${serviceName} (still installed; returns at next login)`)
  }).pipe(Cli.withSettledConfig("atc service stop")),
).pipe(Command.withDescription("Stop the running service (keeps it installed)"))

const status = Command.make("status", {}, () =>
  Effect.gen(function* () {
    const platform = yield* currentPlatform
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const subprocess = yield* Subprocess
    const unitFile = unitFileFor(platform)
    if (!(yield* fs.exists(unitFile).pipe(Effect.orElseSucceed(() => false)))) {
      return yield* Cli.failReported(`atc service status: not installed (no ${unitFile})`)
    }
    const loaded =
      platform === "darwin"
        ? yield* launchdLoaded(subprocess)
        : (yield* runIgnoring(subprocess, "systemctl", [
            "--user",
            "is-active",
            "--quiet",
            serviceName,
          ])) === 0
    if (!loaded) {
      return yield* Cli.failReported(
        `atc service status: installed but not running (${unitFile}); run \`atc service start\``,
      )
    }
    // The supervisor thinks it is running; the health probe says whether the
    // server actually answers on its settled address.
    const probeHost = Cli.reachableHost(config.bind)
    const token = Cli.probeNeedsToken(probeHost)
      ? yield* AuthToken.read.pipe(Effect.orElseSucceed(() => undefined))
      : undefined
    const url = `http://${Server.hostForUrl(config.bind)}:${config.port}`
    if (yield* Cli.probeHealth(probeHost, config.port, 2_000, token)) {
      yield* Console.log(`running on ${url}`)
      return
    }
    return yield* Cli.failReported(
      `atc service status: supervisor reports running but ${url} is not answering; logs: ${config.logFile}`,
    )
  }).pipe(Cli.withSettledConfig("atc service status")),
).pipe(Command.withDescription("Report service and server health"))

export const service = Command.make("service").pipe(
  Command.withDescription("Manage atc as a user-scope system service (launchd/systemd)"),
  Command.withSubcommands([install, uninstall, start, stop, status]),
)
