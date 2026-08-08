import { Console, Effect, FileSystem, Layer, Option, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import { BuildInfo } from "../platform/buildInfo.ts"
import { AppConfig, layer as appConfigLayer } from "../platform/config.ts"
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
  Flag.withDescription(
    "TCP port to listen on (loopback only; overrides ATC_PORT/config.toml/default)",
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

export const serve = Command.make("serve", { port }, ({ port }) =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    // The flag level of the precedence rule: flags > env > file > defaults.
    // The override is folded back into the AppConfig the server stack sees,
    // so consumers of the settled port (e.g. the zmx adapter's ATC_ENDPOINT
    // injection) always read the port actually being served.
    const effective = { ...config, port: Option.getOrElse(port, () => config.port) }
    // Compiled builds keep structured logs file-only (see logging.ts), which
    // would make a successful foreground start indistinguishable from a
    // hang. One human-facing stderr line announces the server; dev runs
    // already announce through the pretty logger. A port conflict still
    // fails with its own diagnostic right after.
    const build = yield* BuildInfo
    if (build.commit !== "dev") {
      yield* Effect.sync(() => {
        process.stderr.write(
          `atc app server starting on http://127.0.0.1:${effective.port} (logs: ${effective.logFile})\n`,
        )
      })
    }
    yield* Layer.launch(Server.production({ port: effective.port })).pipe(
      Effect.provideService(AppConfig, effective),
    )
  }).pipe(
    Effect.provide(appConfigLayer),
    Effect.catch(Cli.reportOnce("atc serve")),
    Effect.catchDefect((defect) =>
      isSystemError(defect) || isMigrationError(defect)
        ? Cli.failReported(`atc serve: ${defect.message}`)
        : Effect.die(defect),
    ),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))

// --- Background management ---

/** Pidfile contents: which process, and which port it was started on. */
const PidRecord = Schema.Struct({ pid: Schema.Int, port: Schema.Int })
const decodePidRecord = Schema.decodeEffect(Schema.fromJsonString(PidRecord))
const encodePidRecord = Schema.encodeEffect(Schema.fromJsonString(PidRecord))

/** A missing or unreadable pidfile is "not running", never an error. */
const readPidRecord = (fs: FileSystem.FileSystem, file: string) =>
  fs.readFileString(file).pipe(
    Effect.flatMap(decodePidRecord),
    Effect.catch(() => Effect.succeed(undefined)),
  )

/** One bounded health probe; false on any failure. */
const probeHealth = (port: number, timeoutMillis: number) =>
  Effect.tryPromise(() =>
    fetch(`http://127.0.0.1:${port}/api/v1/health`, {
      signal: AbortSignal.timeout(timeoutMillis),
    }),
  ).pipe(
    Effect.map((response) => response.ok),
    Effect.catch(() => Effect.succeed(false)),
  )

/** Polls `check` until true or the deadline lapses. */
const pollUntil = (deadlineMillis: number, intervalMillis: number, check: Effect.Effect<boolean>) =>
  Effect.gen(function* () {
    const attempts = Math.ceil(deadlineMillis / intervalMillis)
    for (let attempt = 0; attempt < attempts; attempt++) {
      if (yield* check) return true
      yield* Effect.sleep(intervalMillis)
    }
    return yield* check
  })

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

/** Shared command tail: settled config plus the one-line diagnostic.
 * Failures the body already reported pass through unchanged. */
const serviceCommand = (name: string) => {
  return <E>(
    effect: Effect.Effect<void, E, AppConfig | FileSystem.FileSystem | Subprocess | BuildInfo>,
  ) => effect.pipe(Effect.provide(appConfigLayer), Effect.catch(Cli.reportOnce(`atc ${name}`)))
}

export const start = Command.make("start", { port }, ({ port }) =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    const effective = { ...config, port: Option.getOrElse(port, () => config.port) }
    const fs = yield* FileSystem.FileSystem
    const subprocess = yield* Subprocess
    const existing = yield* readPidRecord(fs, effective.pidFile)
    if (existing !== undefined && isProcessAlive(existing.pid)) {
      yield* Console.log(
        `atc app server already running (pid ${existing.pid}) on http://127.0.0.1:${existing.port}`,
      )
      return
    }
    // A healthy responder without a live pidfile is a foreground serve or
    // someone else's instance; a second server would only fail its bind
    // after the fact and this start would then claim the wrong process.
    if (yield* probeHealth(effective.port, 1_000)) {
      return yield* Cli.failReported(
        `atc start: something is already serving on port ${effective.port} (a foreground \`atc serve\`?); stop it or pass --port`,
      )
    }
    // Spawn ourselves: the compiled binary is process.execPath alone; a
    // source run is the bun runtime plus the entry script (argv[1]),
    // resolved against this same working directory.
    const build = yield* BuildInfo
    const serveArgs = ["serve", "--port", String(effective.port)]
    const pid = yield* subprocess.spawnDetached(
      build.commit === "dev"
        ? { executable: process.execPath, args: [process.argv[1] ?? "src/main.ts", ...serveArgs] }
        : { executable: process.execPath, args: serveArgs },
    )
    yield* fs.makeDirectory(config.stateDir, { recursive: true })
    yield* encodePidRecord({ pid, port: effective.port }).pipe(
      Effect.flatMap((json) => fs.writeFileString(effective.pidFile, json)),
    )
    const healthy = yield* pollUntil(15_000, 150, probeHealth(effective.port, 1_000))
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
      `started atc app server (pid ${pid}) on http://127.0.0.1:${effective.port} (logs: ${effective.logFile})`,
    )
  }).pipe(serviceCommand("start")),
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
  }).pipe(serviceCommand("stop")),
).pipe(Command.withDescription("Stop the background App Server"))

export const status = Command.make("status", {}, () =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const record = yield* readPidRecord(fs, config.pidFile)
    if (record === undefined || !isProcessAlive(record.pid)) {
      return yield* Cli.failReported("atc status: not running")
    }
    const healthy = yield* probeHealth(record.port, 2_000)
    if (!healthy) {
      return yield* Cli.failReported(
        `atc status: running (pid ${record.pid}) but not answering on http://127.0.0.1:${record.port}; logs: ${config.logFile}`,
      )
    }
    yield* Console.log(`running (pid ${record.pid}) on http://127.0.0.1:${record.port}`)
  }).pipe(serviceCommand("status")),
).pipe(Command.withDescription("Report background App Server status"))
