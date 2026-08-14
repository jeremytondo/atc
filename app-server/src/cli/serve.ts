import { Console, Effect, FileSystem, Layer, Option, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import * as AuthToken from "../platform/authToken.ts"
import { BuildInfo } from "../platform/buildInfo.ts"
import { AppConfig } from "../platform/config.ts"
import * as Config from "../platform/config.ts"
import { ServerInfoResponse } from "../api/contract.ts"
import {
  isProcessAlive,
  processHasArgvToken,
  Subprocess,
  waitForProcessExit,
} from "../platform/subprocess.ts"
import * as Server from "../server.ts"
import * as Cli from "./cli.ts"

// Running the service: `serve` is the foreground primitive (what a
// supervisor owns, and what `start` spawns); `start`/`stop`/`status` are the
// background management porcelain over one pidfile in the state directory.
// The CLI carries zero domain-layer imports.
//
// Liveness discipline: the pidfile records intent, never truth. Every
// consumer re-verifies against the live process table (and, before
// signaling, that the pid still looks like our server — pids get recycled),
// and stale files are removed on sight rather than trusted.

/** The one reusable port contract for CLI (and config/API) inputs. */
export const Port = Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 65535 }))

export const port = Flag.integer("port").pipe(
  Flag.withSchema(Port),
  Flag.optional,
  Flag.withDescription("TCP port to listen on (overrides ATC_PORT/config.toml/default)"),
)

export const bind = Flag.string("bind").pipe(
  Flag.optional,
  Flag.withDescription(
    "Address to bind (default 127.0.0.1; 0.0.0.0 also opens remote access — overrides ATC_BIND/config.toml)",
  ),
)

export const tailscale = Flag.boolean("tailscale").pipe(
  Flag.optional,
  Flag.withDescription(
    "Expose the loopback server through supervised Tailscale Serve (overrides ATC_TAILSCALE/config.toml)",
  ),
)

// System errors carry a syscall code (EADDRINUSE, EACCES, ...). Those are
// environment problems and become one friendly line; a migration failure is
// deliberately a defect that halts boot and gets the same treatment (the
// diagnostic names the migration); anything else is a bug and re-dies so it
// keeps the default loud report.
const isSystemError = (defect: unknown): defect is Error & { code: string } =>
  defect instanceof Error && typeof (defect as { code?: unknown }).code === "string"

const isMigrationError = (defect: unknown): defect is Error =>
  defect instanceof Error && (defect as { _tag?: unknown })._tag === "MigrationError"

export const serve = Command.make("serve", { port, bind, tailscale }, ({ port, bind, tailscale }) =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    // The flag level of the precedence rule: flags > env > file > defaults.
    // The override is folded back into the AppConfig the server stack sees,
    // so consumers of the settled port (e.g. the zmx adapter's ATC_ENDPOINT
    // injection) always read the port actually being served.
    const effective = {
      ...config,
      port: Option.getOrElse(port, () => config.port),
      bind: Option.getOrElse(bind, () => config.bind),
      tailscale: Option.getOrElse(tailscale, () => config.tailscale),
    }
    // Compiled builds keep structured logs file-only (see logging.ts), which
    // would make a successful foreground start indistinguishable from a
    // hang. One human-facing stderr line announces the server; dev runs
    // already announce through the pretty logger. A port conflict still
    // fails with its own diagnostic right after.
    const build = yield* BuildInfo
    if (build.commit !== "dev") {
      yield* Effect.sync(() => {
        process.stderr.write(
          `atc app server starting on http://${Server.hostForUrl(effective.bind)}:${effective.port} (logs: ${effective.logFile})\n`,
        )
      })
    }
    yield* Layer.launch(Server.production({ port: effective.port, hostname: effective.bind })).pipe(
      Effect.provideService(AppConfig, effective),
    )
  }).pipe(
    Effect.provide(Config.layer),
    Effect.catch(Cli.reportOnce("atc serve")),
    Effect.catchDefect((defect) =>
      isSystemError(defect) || isMigrationError(defect)
        ? Cli.failReported(`atc serve: ${defect.message}`)
        : Effect.die(defect),
    ),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))

// --- Background management ---

/** Pidfile contents: which process, and where it was told to listen. `bind`
 * is optional because the release before ATC-148 wrote `{ pid, port }`; those
 * servers listened on the then-hardcoded loopback address, so a missing bind
 * decodes as `127.0.0.1` and upgrades keep managing the old process. */
const PidRecord = Schema.Struct({
  pid: Schema.Int,
  port: Schema.Int,
  bind: Schema.optionalKey(Schema.String),
})
const decodePidRecord = Schema.decodeEffect(Schema.fromJsonString(PidRecord))
const encodePidRecord = Schema.encodeEffect(Schema.fromJsonString(PidRecord))

/** A missing or unreadable pidfile is "not running", never an error. */
const readPidRecord = (fs: FileSystem.FileSystem, file: string) =>
  fs.readFileString(file).pipe(
    Effect.flatMap(decodePidRecord),
    Effect.map((record) => ({ ...record, bind: record.bind ?? "127.0.0.1" })),
    Effect.catch(() => Effect.succeed(undefined)),
  )

const signal = (pid: number, name: "SIGTERM" | "SIGKILL") =>
  Effect.sync(() => {
    // A process that exits between the liveness check and the signal is
    // simply already stopped.
    try {
      process.kill(pid, name)
    } catch {
      // ignored
    }
  })

export const start = Command.make("start", { port, bind, tailscale }, ({ port, bind, tailscale }) =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    const effective = {
      ...config,
      port: Option.getOrElse(port, () => config.port),
      bind: Option.getOrElse(bind, () => config.bind),
      tailscale: Option.getOrElse(tailscale, () => config.tailscale),
    }
    const fs = yield* FileSystem.FileSystem
    const subprocess = yield* Subprocess
    const existing = yield* readPidRecord(fs, effective.pidFile)
    if (existing !== undefined && isProcessAlive(existing.pid)) {
      yield* Console.log(
        `atc app server already running (pid ${existing.pid}) on http://${Server.hostForUrl(existing.bind)}:${existing.port}`,
      )
      return
    }
    // A non-loopback probe host needs the bearer token; prepare it up front
    // (the spawned server adopts the same file — `ensure` resolves the
    // create race), and let token-file trouble fail with its own actionable
    // diagnostic rather than 15 s of 403s and a generic unhealthy report.
    // Loopback starts never touch the token, so a broken token file cannot
    // block them (mirroring the server's own boot rule).
    const probeHost = Cli.reachableHost(effective.bind)
    const token = Cli.probeNeedsToken(probeHost) ? yield* AuthToken.ensure : undefined
    // A healthy responder without a live pidfile is a foreground serve or
    // someone else's instance; a second server would only fail its bind
    // after the fact and this start would then claim the wrong process.
    if (yield* Cli.probeHealth(probeHost, effective.port, 1_000, token)) {
      return yield* Cli.failReported(
        `atc start: something is already serving on port ${effective.port} (a foreground \`atc serve\`?); stop it or pass --port`,
      )
    }
    // Spawn ourselves: the compiled binary is process.execPath alone; a
    // source run is the bun runtime plus the entry script (argv[1]),
    // resolved against this same working directory.
    const build = yield* BuildInfo
    const serveArgs = [
      "serve",
      "--port",
      String(effective.port),
      "--bind",
      effective.bind,
      ...(effective.tailscale ? ["--tailscale"] : []),
    ]
    const pid = yield* subprocess.spawnDetached(
      build.commit === "dev"
        ? { executable: process.execPath, args: [process.argv[1] ?? "src/main.ts", ...serveArgs] }
        : { executable: process.execPath, args: serveArgs },
    )
    yield* fs.makeDirectory(config.stateDir, { recursive: true })
    yield* encodePidRecord({ pid, port: effective.port, bind: effective.bind }).pipe(
      Effect.flatMap((json) => fs.writeFileString(effective.pidFile, json)),
    )
    const healthy = yield* Cli.pollUntil(
      15_000,
      150,
      Cli.probeHealth(probeHost, effective.port, 1_000, token),
    )
    if (!healthy) {
      // Success is only ever reported for a healthy server, so the spawned
      // child is stopped rather than left as an orphan no `stop` can reach.
      yield* signal(pid, "SIGTERM")
      yield* waitForProcessExit(pid, { attempts: 50, interval: "100 millis" })
      // Concurrent starts are not serialized; remove the pidfile only while
      // it still records OUR child, so a racing start's healthy server is
      // never made invisible by this cleanup.
      const current = yield* readPidRecord(fs, effective.pidFile)
      if (current?.pid === pid) {
        yield* fs.remove(effective.pidFile).pipe(Effect.ignore)
      }
      return yield* Cli.failReported(
        `atc start: the server did not become healthy within 15s (stopped pid ${pid}); logs: ${effective.logFile}`,
      )
    }
    yield* Console.log(
      `started atc app server (pid ${pid}) on http://${Server.hostForUrl(effective.bind)}:${effective.port} (logs: ${effective.logFile})`,
    )
  }).pipe(Cli.withSettledConfig("atc start")),
).pipe(Command.withDescription("Start the App Server in the background"))

export const stop = Command.make("stop", {}, () =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const record = yield* readPidRecord(fs, config.pidFile)
    if (record === undefined) {
      yield* Console.log("atc app server is not running")
      return
    }
    if (!isProcessAlive(record.pid)) {
      yield* fs.remove(config.pidFile).pipe(Effect.ignore)
      yield* Console.log("atc app server is not running (removed a stale pidfile)")
      return
    }
    // Pids get recycled: never signal a process that no longer looks like
    // our server (`serve` is an exact argv token in both the compiled and
    // source forms; an unverifiable pid is never signaled).
    const subprocess = yield* Subprocess
    const looksLikeOurs = yield* processHasArgvToken(subprocess, record.pid, "serve")
    if (!looksLikeOurs) {
      yield* fs.remove(config.pidFile).pipe(Effect.ignore)
      return yield* Cli.failReported(
        `atc stop: pid ${record.pid} no longer looks like the atc server; removed the stale pidfile without signaling it`,
      )
    }
    yield* signal(record.pid, "SIGTERM")
    const exited = yield* waitForProcessExit(record.pid, {
      attempts: 100,
      interval: "100 millis",
    })
    if (!exited) {
      yield* signal(record.pid, "SIGKILL")
      const killed = yield* waitForProcessExit(record.pid, {
        attempts: 20,
        interval: "100 millis",
      })
      if (!killed) {
        // Still alive even after SIGKILL (unkillable state): the pidfile
        // still tells the truth, so it stays.
        return yield* Cli.failReported(
          `atc stop: pid ${record.pid} did not exit even after SIGKILL`,
        )
      }
    }
    yield* fs.remove(config.pidFile).pipe(Effect.ignore)
    yield* Console.log(`stopped atc app server (pid ${record.pid})`)
  }).pipe(Cli.withSettledConfig("atc stop")),
).pipe(Command.withDescription("Stop the background App Server"))

export const status = Command.make("status", {}, () =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const record = yield* readPidRecord(fs, config.pidFile)
    if (record === undefined || !isProcessAlive(record.pid)) {
      return yield* Cli.failReported("atc status: not running")
    }
    // Status never creates or repairs the token file; a missing or invalid
    // one simply leaves the probe unauthenticated (and the server it cannot
    // reach was not serving remote clients anyway — verify fails closed).
    const probeHost = Cli.reachableHost(record.bind)
    const token = Cli.probeNeedsToken(probeHost)
      ? yield* AuthToken.read.pipe(Effect.catch(() => Effect.succeed(undefined)))
      : undefined
    const healthy = yield* Cli.probeHealth(probeHost, record.port, 2_000, token)
    const url = `http://${Server.hostForUrl(record.bind)}:${record.port}`
    if (!healthy) {
      return yield* Cli.failReported(
        `atc status: running (pid ${record.pid}) but not answering on ${url}; logs: ${config.logFile}`,
      )
    }
    yield* Console.log(`running (pid ${record.pid}) on ${url}`)
    // The ui/api lines are for pasting into a browser or client, so wildcard
    // binds report the loopback address that actually connects; the first
    // line keeps reporting the bind itself.
    const reachableUrl = `http://${Server.hostForUrl(probeHost)}:${record.port}`
    // Status keeps reporting even if this probe fails: the health check above
    // already established the server is up, so a server-info hiccup only
    // costs the tailscale line, never the whole report.
    const serverInfo = yield* Effect.tryPromise(async () => {
      const response = await fetch(`${reachableUrl}/api/v1/server-info`, {
        signal: AbortSignal.timeout(2_000),
        headers: token === undefined ? {} : { authorization: `Bearer ${token}` },
      })
      if (!response.ok) throw new Error(`server-info returned ${response.status}`)
      return response.json()
    }).pipe(Effect.flatMap(Schema.decodeUnknownEffect(ServerInfoResponse)), Effect.option)
    yield* Console.log(`  web ui    ${reachableUrl}/`)
    yield* Console.log(`  api       ${reachableUrl}/api/v1 (openapi: ${reachableUrl}/openapi.json)`)
    if (Option.isSome(serverInfo) && serverInfo.value.tailscale.state !== "disabled") {
      const tailscale = serverInfo.value.tailscale
      yield* Console.log(
        tailscale.state === "running"
          ? `  tailscale ${tailscale.url}`
          : `  tailscale ${tailscale.state}${tailscale.reason === undefined ? "" : `: ${tailscale.reason}`}`,
      )
    }
    yield* Console.log(
      Cli.probeNeedsToken(record.bind)
        ? `  auth      open on ${record.bind}; non-loopback clients need the bearer token (${config.tokenFile})`
        : `  auth      loopback clients only; no token needed (bind ${record.bind})`,
    )
    yield* Console.log(`  log       ${config.logFile}`)
    yield* Console.log(`  pid-file  ${config.pidFile}`)
  }).pipe(Cli.withSettledConfig("atc status")),
).pipe(Command.withDescription("Report background App Server status"))
