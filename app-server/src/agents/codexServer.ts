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

// Supervision for the single long-lived `codex app-server` per ATC profile
// (ATC-123). Invariants that are invisible from the call sites:
//
//   - The server is spawned DETACHED (reparented through an intermediate sh,
//     zmx-style) because a live `codex resume --remote` TUI dies the moment
//     its app-server dies, with no reconnect loop. A scoped child would kill
//     every attached TUI on every ATC restart — so ATC shutdown deliberately
//     leaves the server running, and boot re-adopts it from the persisted
//     identity file + /readyz. Adopt-or-replace, never accumulate.
//   - Exactly one server per profile, always ATC's own `--listen` listener —
//     never the machine-wide `codex app-server proxy` daemon (externally
//     owned; observed held by the Codex desktop integration).
//   - A persisted pid is only trusted (and only ever signaled) after its
//     command line still looks like an app-server: pids get recycled, and
//     SIGKILLing a stranger is worse than leaking a server.
//   - `stop` is the only intentional-kill path. The exit watcher restarts a
//     server that died underneath us (with backoff); interrupting the
//     watcher (layer shutdown) never touches the process itself.

/** Persisted identity of the detached server, in stateDir. */
const ServerIdentity = Schema.Struct({
  pid: Schema.Number,
  port: Schema.Number,
  startedAt: Schema.String,
})

export interface CodexServerInfo {
  readonly pid: number
  readonly port: number
  /** JSON-RPC WebSocket endpoint (also the TUI `--remote` endpoint). */
  readonly url: string
}

export interface CodexServerOptions {
  /** Readiness window for a freshly spawned server. */
  readonly readyTimeout?: Duration.Input
  /**
   * Readiness window when adopting an already-running pid. Deliberately
   * generous by default: concluding a live server is dead kills every
   * attached TUI, so patience is the cheaper error.
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
     * The profile's ready app-server: adopt the persisted one when it is
     * alive and answers /readyz, replace it otherwise, spawn fresh when
     * there is none. Serialized — concurrent callers get the same server.
     */
    readonly ensure: () => Effect.Effect<CodexServerInfo, AgentUnavailable>
    /**
     * Intentionally stop the detached server and clear its identity. Live
     * TUIs attached to it will exit — callers own that decision. Stopping
     * an already-stopped server succeeds.
     */
    readonly stop: () => Effect.Effect<void, AgentUnavailable>
  }
>()("app-server/CodexServer") {}

const { unavailable } = providerErrors("codex")

/** Readiness-gate failure that must stop the retry loop: the pid is gone. */
class ServerExited extends Schema.TaggedErrorClass<ServerExited>()("ServerExited", {
  pid: Schema.Number,
}) {}

const infoFor = (pid: number, port: number): CodexServerInfo => ({
  pid,
  port,
  url: `ws://127.0.0.1:${port}`,
})

/** An OS-assigned free loopback port; the tiny claim race is acceptable. */
const freePort = Effect.try({
  try: () => {
    const listener = Bun.listen({
      hostname: "127.0.0.1",
      port: 0,
      socket: { data: () => {} },
    })
    const port = listener.port
    listener.stop(true)
    return port
  },
  catch: (error) => unavailable(`could not allocate a listen port: ${error}`),
})

/** One /readyz probe; any failure (refused, non-2xx, >1s) is "not ready". */
const probeReady = (port: number): Effect.Effect<void, AgentUnavailable> =>
  Effect.tryPromise({
    try: (signal) => fetch(`http://127.0.0.1:${port}/readyz`, { signal }),
    catch: () => unavailable(`not answering /readyz on port ${port}`),
  }).pipe(
    Effect.timeoutOrElse({
      duration: "1 second",
      orElse: () => Effect.fail(unavailable(`/readyz probe timed out on port ${port}`)),
    }),
    Effect.flatMap((response) =>
      response.ok
        ? Effect.void
        : Effect.fail(unavailable(`/readyz answered ${response.status} on port ${port}`)),
    ),
  )

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
      const identityFile = path.join(config.stateDir, "codex-app-server.json")
      const logFile = path.join(config.stateDir, "codex-app-server.log")
      const readyTimeout = options.readyTimeout ?? "15 seconds"
      const adoptTimeout = options.adoptTimeout ?? "10 seconds"
      const watchInterval = options.watchInterval ?? "1 second"
      const readySchedule = Schedule.exponential("100 millis").pipe(Schedule.jittered)

      const current = yield* Ref.make<CodexServerInfo | null>(null)
      const lock = yield* Semaphore.make(1)
      const watcher = yield* FiberHandle.make()

      /**
       * Whether `pid` still looks like the app-server we recorded. Guards
       * every adoption and every signal against pid recycling.
       */
      const isOurServer = (pid: number) =>
        Subprocess.processHasArgvToken(subprocess, pid, "app-server")

      /** Terminate `pid` only if it is provably still our server. */
      const terminateOurs = (pid: number) =>
        Effect.gen(function* () {
          if (yield* isOurServer(pid)) yield* terminate(pid)
        })

      /** Last log lines, for actionable spawn/readiness diagnostics. */
      const logTail = fs.readFileString(logFile).pipe(
        Effect.map((text) => {
          const lines = text.trimEnd().split("\n").slice(-10).join("\n")
          return lines === "" ? "" : `\ncodex app-server log:\n${lines}`
        }),
        Effect.orElseSucceed(() => ""),
      )

      /**
       * Poll /readyz until ready, the window elapses, or the pid dies —
       * a dead pid fails immediately instead of burning the whole window
       * (the lock is held while this runs).
       */
      const awaitReady = (pid: number, port: number, window: Duration.Input) =>
        Effect.suspend((): Effect.Effect<void, AgentUnavailable | ServerExited> =>
          Subprocess.isProcessAlive(pid)
            ? probeReady(port)
            : Effect.fail(new ServerExited({ pid })),
        ).pipe(
          Effect.retry({
            schedule: readySchedule,
            while: (error): error is AgentUnavailable => error._tag === "AgentUnavailable",
          }),
          Effect.timeoutOrElse({
            duration: window,
            orElse: () => Effect.fail(unavailable(`gave up waiting for /readyz on port ${port}`)),
          }),
          Effect.catch((error) =>
            Effect.gen(function* () {
              const tail = yield* logTail
              const cause =
                error._tag === "ServerExited"
                  ? `process ${pid} exited before becoming ready`
                  : `not ready within ${Duration.format(Duration.fromInputUnsafe(window))} on port ${port}`
              return yield* Effect.fail(unavailable(cause + tail))
            }),
          ),
        )

      const readIdentity = fs.readFileString(identityFile).pipe(
        Effect.flatMap((text) =>
          Effect.try({ try: () => JSON.parse(text) as unknown, catch: (error) => error }),
        ),
        Effect.flatMap((json) => Schema.decodeUnknownEffect(ServerIdentity)(json)),
        Effect.map(Option.some),
        // Absent or corrupt identity is "no server" — replaced, never trusted.
        Effect.orElseSucceed(() => Option.none<typeof ServerIdentity.Type>()),
      )

      const writeIdentity = (pid: number, port: number) =>
        fs
          .writeFileString(
            identityFile,
            JSON.stringify({ pid, port, startedAt: new Date().toISOString() }, null, 2),
          )
          .pipe(
            Effect.mapError((error) => unavailable(`could not persist identity: ${error.message}`)),
          )

      const clearIdentity = fs.remove(identityFile).pipe(Effect.ignore)

      // Detached spawn, zmx-style: an intermediate sh backgrounds the server
      // (reparenting it away from ATC, stdin from /dev/null so it can never
      // see EOF when ATC exits) and echoes the server's pid. The sh itself
      // is a normal scoped child that exits immediately.
      const spawnDetached = Effect.gen(function* () {
        const executable = yield* resolveProviderExecutable("codex", config.codexExecutable)
        const port = yield* freePort
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
                `nohup "$1" app-server --listen "$2" >"$3" 2>&1 </dev/null & echo $!`,
                "sh",
                executable,
                `ws://127.0.0.1:${port}`,
                logFile,
              ],
              env: {},
              // The server needs the user's full environment (auth state).
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
        // reap it, or "never accumulate" is broken.
        yield* Effect.gen(function* () {
          yield* writeIdentity(pid, port)
          yield* awaitReady(pid, port, readyTimeout)
        }).pipe(
          Effect.onExit((exit) =>
            exit._tag === "Failure"
              ? terminate(pid).pipe(Effect.ignore, Effect.andThen(clearIdentity))
              : Effect.void,
          ),
        )
        yield* Effect.logInfo("codex app-server started").pipe(Effect.annotateLogs({ pid, port }))
        return infoFor(pid, port)
      })

      // Adopt-or-replace against the persisted identity, then spawn fresh if
      // nothing was adoptable. Callers hold the lock.
      const acquire = Effect.gen(function* () {
        const persisted = yield* readIdentity
        if (Option.isSome(persisted)) {
          const { pid, port } = persisted.value
          if (yield* isOurServer(pid)) {
            const adopted = yield* awaitReady(pid, port, adoptTimeout).pipe(
              Effect.as(true),
              Effect.orElseSucceed(() => false),
            )
            if (adopted) {
              yield* Effect.logInfo("codex app-server adopted").pipe(
                Effect.annotateLogs({ pid, port }),
              )
              return infoFor(pid, port)
            }
            // Alive but not answering: replace it rather than accumulate.
            yield* terminate(pid)
          }
          yield* clearIdentity
        }
        return yield* spawnDetached
      })

      const watchLoop: Effect.Effect<void> = Effect.gen(function* () {
        while (true) {
          const info = yield* Ref.get(current)
          if (info === null) return
          if (Subprocess.isProcessAlive(info.pid)) {
            yield* Effect.sleep(watchInterval)
            continue
          }
          yield* Effect.logWarning("codex app-server exited unexpectedly; restarting").pipe(
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
              if (cached !== null && Subprocess.isProcessAlive(cached.pid)) {
                const stillReady = yield* probeReady(cached.port).pipe(
                  Effect.as(true),
                  Effect.orElseSucceed(() => false),
                )
                // Not ready ≠ dead: acquire re-probes within adoptTimeout
                // before concluding anything drastic.
                if (stillReady) return cached
              }
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
              // would be unadoptable forever.
              const cached = yield* Ref.get(current)
              const persisted = yield* readIdentity
              const pids = new Set<number>()
              if (cached !== null) pids.add(cached.pid)
              if (Option.isSome(persisted)) pids.add(persisted.value.pid)
              for (const pid of pids) yield* terminateOurs(pid)
              yield* clearIdentity
              yield* Ref.set(current, null)
            }),
          )
        })

      return { ensure, stop }
    }),
  )

export const layer = layerWith({})
