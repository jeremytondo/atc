import {
  Context,
  Duration,
  Effect,
  FiberHandle,
  FileSystem,
  Layer,
  Option,
  Ref,
  Schedule,
  Schema,
  Semaphore,
  Stream,
} from "effect"
import * as path from "node:path"
import { providerErrors, resolveProviderExecutable } from "./agentAdapter.ts"
import type { AgentUnavailable } from "./agentAdapter.ts"
import { AppConfig } from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"
import * as UnixWebSocket from "../platform/unixWebSocket.ts"

// Supervision of the ONE shared `codex app-server` per CODEX_HOME (ATC-196),
// reached on Codex's well-known control socket
// ($CODEX_HOME/app-server-control/app-server-control.sock) — the address
// every Codex client on the machine meets at: the desktop/mobile apps over
// SSH, plain `codex` invocations (which auto-join a live server), and ATC.
// Invariants that are invisible from the call sites:
//
//   - Adopt-or-start. Whatever answers the socket is adopted as-is; ATC
//     starts `codex app-server --listen unix://` only when nothing answers,
//     and never runs an app-server on any other address. A private server
//     splits the thread store — one process may write a thread at a time,
//     and live events reach only clients of the same process — which is
//     exactly the failure this module exists to prevent.
//   - Ownership boundary. ATC never kills, replaces, or signals a server it
//     did not start, and never invokes `codex app-server daemon *` (the
//     managed daemon/updater). It replaces only its own recorded pid, and
//     only once that pid is dead or has stopped answering. A codex upgrade
//     therefore takes effect on the next start after the USER stops the
//     server — never implicitly.
//   - The server ATC starts is spawned DETACHED (reparented through an
//     intermediate sh, zmx-style) because a `codex resume --remote` TUI
//     dies the moment its app-server dies, with no reconnect loop. So ATC
//     shutdown deliberately leaves the server running, and the next boot
//     adopts it from the socket like any other client would.
//   - A persisted pid is only trusted (and only ever signaled) after its
//     command line still looks like an app-server: pids get recycled, and
//     SIGKILLing a stranger is worse than leaking a server.
//   - "Answers" means accepts a WebSocket handshake. The adapter's
//     `initialize` right after is what actually gates use; the probe only
//     decides whether a start is needed.
//   - `stop` is the only intentional-kill path and only ever targets our
//     own recorded pid. The exit watcher restarts our own dead server (a
//     server that appeared on the socket meanwhile is adopted, never
//     replaced); interrupting the watcher never touches the process.

/** Persisted identity of the server ATC started, in stateDir. */
const ServerIdentity = Schema.Struct({
  pid: Schema.Number,
  startedAt: Schema.String,
})

export interface CodexServerInfo {
  /** The well-known control socket every Codex client on the machine shares. */
  readonly socketPath: string
  /** The pid of the server ATC started, or null when a foreign server was adopted. */
  readonly pid: number | null
}

export interface CodexServerOptions {
  /** How long a freshly started server may take to answer. */
  readonly readyTimeout?: Duration.Input
  /**
   * How long our own already-running server may take to answer again
   * before it is replaced. Deliberately generous by default: concluding a
   * live server is dead kills every attached TUI, so patience is the
   * cheaper error.
   */
  readonly adoptTimeout?: Duration.Input
  /** Exit-watcher poll interval. */
  readonly watchInterval?: Duration.Input
}

/** Restart attempts after an unexpected server death, then give up until next use. */
const RESTART_RETRIES = 5

export class CodexServer extends Context.Service<
  CodexServer,
  {
    /**
     * The shared app-server: adopt whatever answers the well-known socket,
     * else start one (detached). Serialized — concurrent callers get the
     * same answer.
     */
    readonly ensure: () => Effect.Effect<CodexServerInfo, AgentUnavailable>
    /**
     * Intentionally stop the server ATC started and clear its identity;
     * a foreign server is left alone. Live TUIs attached to it will exit —
     * callers own that decision. Stopping nothing succeeds.
     */
    readonly stop: () => Effect.Effect<void, AgentUnavailable>
  }
>()("app-server/CodexServer") {}

const { unavailable } = providerErrors("codex")

/** Readiness-wait outcome that must stop polling: the pid is gone. */
class ServerExited extends Schema.TaggedErrorClass<ServerExited>()("ServerExited", {
  pid: Schema.Number,
}) {}

/** Codex's hardcoded control socket location under its home directory. */
export const controlSocketPath = (codexHome: string): string =>
  path.join(codexHome, "app-server-control", "app-server-control.sock")

/** One handshake probe: whether something completes a WebSocket upgrade. */
const probeSocket = (socketPath: string): Effect.Effect<boolean> =>
  Effect.callback<boolean>((resume) => {
    const socket = UnixWebSocket.open(socketPath)
    // Resume before closing: close() reports onclose synchronously.
    socket.onopen = () => {
      resume(Effect.succeed(true))
      socket.close()
    }
    socket.onclose = () => resume(Effect.succeed(false))
    return Effect.sync(() => socket.close())
  }).pipe(Effect.timeoutOrElse({ duration: "1 second", orElse: () => Effect.succeed(false) }))

/** SIGTERM → bounded wait → SIGKILL → verified gone. Absent pid succeeds. */
const terminate = (pid: number): Effect.Effect<void, AgentUnavailable> =>
  Effect.gen(function* () {
    const signal = (name: NodeJS.Signals) =>
      Effect.sync(() => {
        try {
          process.kill(pid, name)
        } catch {
          // Already gone.
        }
      })
    yield* signal("SIGTERM")
    if (yield* Subprocess.waitForProcessExit(pid)) return
    yield* signal("SIGKILL")
    if (yield* Subprocess.waitForProcessExit(pid)) return
    return yield* Effect.fail(unavailable(`process ${pid} survived SIGTERM and SIGKILL`))
  })

export const layerWith = (options: CodexServerOptions) =>
  Layer.effect(CodexServer)(
    Effect.gen(function* () {
      const config = yield* AppConfig
      const subprocess = yield* Subprocess.Subprocess
      const fs = yield* FileSystem.FileSystem
      const socketPath = controlSocketPath(config.codexHome)
      const identityFile = path.join(config.stateDir, "codex-app-server.json")
      const logFile = path.join(config.stateDir, "codex-app-server.log")
      const readyTimeout = options.readyTimeout ?? "15 seconds"
      const adoptTimeout = options.adoptTimeout ?? "10 seconds"
      const watchInterval = options.watchInterval ?? "1 second"
      // Capped backoff: a real server takes a second or two to bind, and an
      // uncapped exponential would leave whole seconds on the table.
      const readySchedule = Schedule.min([
        Schedule.exponential("100 millis"),
        Schedule.spaced("1 second"),
      ]).pipe(Schedule.jittered)

      const current = yield* Ref.make<CodexServerInfo | null>(null)
      const lock = yield* Semaphore.make(1)
      const watcher = yield* FiberHandle.make()

      /**
       * Whether `pid` still looks like the app-server we recorded. Guards
       * every adoption and every signal against pid recycling.
       */
      const isOurServer = (pid: number) =>
        Subprocess.processHasArgvToken(subprocess, pid, "app-server")

      /** Last log lines, for actionable start/readiness diagnostics. */
      const logTail = fs.readFileString(logFile).pipe(
        Effect.map((text) => {
          const lines = text.trimEnd().split("\n").slice(-10).join("\n")
          return lines === "" ? "" : `\ncodex app-server log:\n${lines}`
        }),
        Effect.orElseSucceed(() => ""),
      )

      /**
       * Poll the socket until it answers, the window elapses, or `pid`
       * dies — a dead pid fails immediately instead of burning the whole
       * window (the lock is held while this runs).
       */
      const awaitAnswer = (pid: number, window: Duration.Input) =>
        Effect.suspend((): Effect.Effect<void, AgentUnavailable | ServerExited> =>
          Effect.gen(function* () {
            if (yield* probeSocket(socketPath)) return
            return yield* Subprocess.isProcessAlive(pid)
              ? Effect.fail(unavailable("not answering yet"))
              : Effect.fail(new ServerExited({ pid }))
          }),
        ).pipe(
          Effect.retry({
            schedule: readySchedule,
            while: (error): error is AgentUnavailable => error._tag === "AgentUnavailable",
          }),
          Effect.timeoutOrElse({
            duration: window,
            orElse: () =>
              Effect.fail(
                unavailable(
                  `not answering on ${socketPath} within ${Duration.format(Duration.fromInputUnsafe(window))}`,
                ),
              ),
          }),
        )

      const readIdentity = fs.readFileString(identityFile).pipe(
        Effect.flatMap((text) =>
          Effect.try({ try: () => JSON.parse(text) as unknown, catch: (error) => error }),
        ),
        Effect.flatMap((json) => Schema.decodeUnknownEffect(ServerIdentity)(json)),
        Effect.map(Option.some),
        // Absent or corrupt identity is "no server of ours" — never trusted.
        Effect.orElseSucceed(() => Option.none<typeof ServerIdentity.Type>()),
      )

      const writeIdentity = (pid: number) =>
        fs
          .writeFileString(
            identityFile,
            JSON.stringify({ pid, startedAt: new Date().toISOString() }, null, 2),
          )
          .pipe(
            Effect.mapError((error) => unavailable(`could not persist identity: ${error.message}`)),
          )

      const clearIdentity = fs.remove(identityFile).pipe(Effect.ignore)

      /**
       * Who serves an answering socket: our recorded pid when it is alive
       * and still an app-server, otherwise a foreign server (and a stale
       * identity of ours is cleared).
       */
      const claimAnswering = Effect.gen(function* () {
        const persisted = yield* readIdentity
        if (Option.isSome(persisted) && (yield* isOurServer(persisted.value.pid))) {
          return { socketPath, pid: persisted.value.pid }
        }
        yield* clearIdentity
        return { socketPath, pid: null }
      })

      // Detached spawn, zmx-style: an intermediate sh backgrounds the server
      // (reparenting it away from ATC, stdin from /dev/null so it can never
      // see EOF when ATC exits) and echoes the server's pid. The sh itself
      // is a normal scoped child that exits immediately. `--listen unix://`
      // with no path binds Codex's well-known socket for the CODEX_HOME the
      // server inherits — the derivation stays codex's.
      const spawnDetached = Effect.gen(function* () {
        const executable = yield* resolveProviderExecutable("codex", config.codexExecutable)
        yield* fs
          .makeDirectory(config.stateDir, { recursive: true })
          .pipe(
            Effect.mapError((error) => unavailable(`could not create stateDir: ${error.message}`)),
          )
        const pidText = yield* Effect.scoped(
          Effect.gen(function* () {
            const child = yield* subprocess.spawn({
              executable: "/bin/sh",
              args: [
                "-c",
                `nohup "$1" app-server --listen unix:// >"$2" 2>&1 </dev/null & echo $!`,
                "sh",
                executable,
                logFile,
              ],
              env: {},
              // The server needs the user's full environment (auth state,
              // CODEX_HOME).
              extendEnv: true,
              // A fixed, neutral cwd: the detached server outlives ATC, and
              // codex uses the server's cwd as the default for threads whose
              // client did not send one — an inherited launch cwd would leak
              // into those threads (and pin a project directory open).
              cwd: config.stateDir,
            })
            const lines = yield* Stream.runCollect(child.stdoutLines)
            yield* child.exitCode
            return lines[0]?.trim() ?? ""
          }),
        ).pipe(Effect.mapError((error) => unavailable(`detached spawn failed: ${error.message}`)))
        const pid = Number.parseInt(pidText, 10)
        if (!Number.isInteger(pid) || pid <= 0) {
          return yield* Effect.fail(
            unavailable(`detached spawn did not report a pid: "${pidText}"`),
          )
        }
        // From here the detached server exists: any non-success exit —
        // identity write failure, readiness failure, interruption — must
        // reap it, or ATC could accumulate servers of its own. (If a
        // foreign server binds the socket while our child is still
        // starting, the answer seen here may be the foreign one's while our
        // child has not yet exited "already in use"; the identity then
        // briefly names that pid, and the exit watcher reconciles on its
        // next tick by adopting the winner.)
        const outcome = yield* Effect.gen(function* () {
          yield* writeIdentity(pid)
          return yield* awaitAnswer(pid, readyTimeout).pipe(
            // Answering while our child is already gone: another server
            // took the socket and ours exited "already in use".
            Effect.map(() =>
              Subprocess.isProcessAlive(pid) ? ("started" as const) : ("raced" as const),
            ),
            Effect.catchTag("ServerExited", () =>
              Effect.gen(function* () {
                // Our child exited: either it lost the start race to
                // another client's server (codex exits "already in use"),
                // which we then adopt, or it failed outright.
                if (yield* probeSocket(socketPath)) return "raced" as const
                const tail = yield* logTail
                return yield* Effect.fail(
                  unavailable(`process ${pid} exited before answering on ${socketPath}` + tail),
                )
              }),
            ),
            Effect.catchTag("AgentUnavailable", (error) =>
              Effect.gen(function* () {
                const tail = yield* logTail
                return yield* Effect.fail(unavailable(error.reason + tail))
              }),
            ),
          )
        }).pipe(
          Effect.onExit((exit) =>
            exit._tag === "Failure"
              ? terminate(pid).pipe(Effect.ignore, Effect.andThen(clearIdentity))
              : Effect.void,
          ),
        )
        if (outcome === "raced") {
          yield* clearIdentity
          yield* Effect.logInfo("codex app-server adopted (another client started it first)")
          return { socketPath, pid: null }
        }
        yield* Effect.logInfo("codex app-server started").pipe(
          Effect.annotateLogs({ pid, socketPath }),
        )
        return { socketPath, pid }
      })

      // Adopt whatever answers; else wait for (or replace) our own recorded
      // server; else start one. Callers hold the lock.
      const acquire = Effect.gen(function* () {
        if (yield* probeSocket(socketPath)) {
          const info = yield* claimAnswering
          yield* Effect.logInfo(
            info.pid === null ? "codex app-server adopted" : "codex app-server re-adopted",
          ).pipe(Effect.annotateLogs({ socketPath, pid: info.pid }))
          return info
        }
        const persisted = yield* readIdentity
        if (Option.isSome(persisted)) {
          const { pid } = persisted.value
          if (yield* isOurServer(pid)) {
            // Ours, alive, silent: it may still be starting up. Wait, and
            // only then replace it — replacing our own server is within
            // the ownership boundary; a foreign one never gets here.
            const answered = yield* awaitAnswer(pid, adoptTimeout).pipe(
              Effect.as(true),
              Effect.orElseSucceed(() => false),
            )
            if (answered) return { socketPath, pid }
            yield* terminate(pid)
          }
          yield* clearIdentity
        }
        return yield* spawnDetached
      })

      const watchLoop: Effect.Effect<void> = Effect.gen(function* () {
        while (true) {
          const info = yield* Ref.get(current)
          // Only a server we started is ours to watch and restart.
          if (info === null || info.pid === null) return
          if (Subprocess.isProcessAlive(info.pid)) {
            yield* Effect.sleep(watchInterval)
            continue
          }
          yield* Effect.logWarning("codex app-server exited unexpectedly; recovering").pipe(
            Effect.annotateLogs({ pid: info.pid }),
          )
          yield* Ref.set(current, null)
          const restarted = yield* lock
            .withPermits(1)(
              Effect.gen(function* () {
                const next = yield* acquire
                yield* Ref.set(current, next)
                return next
              }),
            )
            .pipe(
              Effect.retry({
                schedule: Schedule.exponential("500 millis").pipe(Schedule.jittered),
                times: RESTART_RETRIES,
              }),
              Effect.option,
            )
          if (Option.isNone(restarted)) {
            yield* Effect.logError(
              "codex app-server could not be restarted; giving up until next use",
            )
            return
          }
        }
      })

      const ensureWatching = FiberHandle.run(watcher, watchLoop, { onlyIfMissing: true }).pipe(
        Effect.asVoid,
      )

      const ensure = () =>
        lock
          .withPermits(1)(
            Effect.gen(function* () {
              const cached = yield* Ref.get(current)
              const ownAlive =
                cached !== null && (cached.pid === null || Subprocess.isProcessAlive(cached.pid))
              // Not answering ≠ dead: acquire re-probes within adoptTimeout
              // before concluding anything drastic.
              if (ownAlive && (yield* probeSocket(socketPath))) return cached
              yield* Ref.set(current, null)
              const info = yield* acquire
              yield* Ref.set(current, info)
              return info
            }),
          )
          .pipe(Effect.tap(() => ensureWatching))

      const stop = () =>
        Effect.gen(function* () {
          // Interrupt the watcher first so it cannot resurrect the server.
          yield* FiberHandle.clear(watcher)
          yield* lock.withPermits(1)(
            Effect.gen(function* () {
              // Cache and file normally agree; if they diverged (another
              // process rewrote the file), reap both — an unrecorded server
              // of ours would be unadoptable forever.
              const cached = yield* Ref.get(current)
              const persisted = yield* readIdentity
              const pids = new Set<number>()
              if (cached !== null && cached.pid !== null) pids.add(cached.pid)
              if (Option.isSome(persisted)) pids.add(persisted.value.pid)
              for (const pid of pids) {
                if (yield* isOurServer(pid)) yield* terminate(pid)
              }
              yield* clearIdentity
              yield* Ref.set(current, null)
            }),
          )
        })

      return { ensure, stop }
    }),
  )

export const layer = layerWith({})
