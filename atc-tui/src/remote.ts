import { Context, Deferred, Duration, Effect, Layer, Ref, Schedule, Schema, Stream } from "effect"
import type { Scope } from "effect"
import { unlink } from "node:fs/promises"
import * as Poll from "../../app-server/src/platform/poll.ts"
import * as Subprocess from "../../app-server/src/platform/subprocess.ts"
import * as Config from "./config.ts"
import * as Transport from "./transport.ts"

// Remote-controller mode owns one quiet SSH socket forward for HTTP/SSE. The
// forward is an implementation detail: the App Server stays loopback-only,
// and every request still crosses the canonical HTTP API. A failed SSH
// connection is retried for the life of the TUI; initial startup is bounded
// so a bad host or missing key remains actionable. Each invocation gets a
// private Unix socket, eliminating fixed-port conflicts and stale adoption.

const STARTUP_TIMEOUT = "15 seconds"

export class TunnelError extends Schema.TaggedErrorClass<TunnelError>()("TunnelError", {
  reason: Schema.String,
}) {
  override get message(): string {
    return this.reason
  }
}

export interface TunnelSpec {
  readonly executable: string
  readonly args: ReadonlyArray<string>
}

/** Options shared by the quiet API tunnel and each interactive attachment. */
export const connectionArgs: ReadonlyArray<string> = [
  "-o",
  "ServerAliveInterval=5",
  "-o",
  "ServerAliveCountMax=2",
  "-o",
  "ConnectTimeout=5",
]

export const tunnelSpec = (connection: Config.RemoteConnection): TunnelSpec => ({
  executable: connection.sshExecutable,
  args: [
    "-N",
    "-T",
    "-o",
    "ExitOnForwardFailure=yes",
    // The tunnel must belong to this exact child process. User SSH config may
    // otherwise hand the forward to a persistent ControlMaster or background
    // the client, leaving the forward alive after the TUI's scope closes.
    "-o",
    "ControlMaster=no",
    "-o",
    "ControlPath=none",
    "-o",
    "ForkAfterAuthentication=no",
    "-o",
    "StreamLocalBindUnlink=yes",
    "-o",
    "StreamLocalBindMask=0177",
    ...connectionArgs,
    "-L",
    `${connection.socketPath}:127.0.0.1:${connection.remotePort}`,
    connection.host,
  ],
})

const probe = (config: Config.ClientConfig["Service"]): Effect.Effect<boolean> =>
  Effect.tryPromise(() =>
    Transport.fetch(config, new URL("/api/v1/health", config.endpoint), {
      signal: AbortSignal.timeout(1_000),
    }),
  ).pipe(
    Effect.map((response) => response.ok),
    Effect.catch(() => Effect.succeed(false)),
  )

const waitUntilReachable = (config: Config.ClientConfig["Service"]) =>
  Poll.pollUntil(probe(config), {
    until: (reachable) => reachable,
    schedule: Schedule.spaced("100 millis").pipe(Schedule.upTo({ times: 50 })),
  }).pipe(
    Effect.flatMap((reachable) =>
      reachable
        ? Effect.void
        : Effect.fail(
            new TunnelError({ reason: "the forwarded App Server did not become reachable" }),
          ),
    ),
  )

const detail = (lines: ReadonlyArray<string>, fallback: string): string => {
  const message = lines.join(" ").trim()
  return message === "" ? fallback : message
}

const superviseAttempt = (
  subprocess: Subprocess.Subprocess["Service"],
  config: Config.ClientConfig["Service"],
  connection: Config.RemoteConnection,
  ready: Deferred.Deferred<void>,
) =>
  Effect.scoped(
    Effect.gen(function* () {
      const spec = tunnelSpec(connection)
      const child = yield* subprocess.spawn({
        executable: spec.executable,
        args: spec.args,
        env: Object.fromEntries(
          Object.entries(config.environment).filter(
            (entry): entry is [string, string] => entry[1] !== undefined,
          ),
        ),
      })
      yield* child.endInput
      yield* child.stdoutLines.pipe(Stream.runDrain, Effect.ignore, Effect.forkScoped)

      const startup = yield* Effect.raceFirst(
        waitUntilReachable(config).pipe(Effect.as({ type: "ready" as const })),
        child.exitCode.pipe(Effect.map((exitCode) => ({ type: "exited" as const, exitCode }))),
      )
      if (startup.type === "exited") {
        const stderr = yield* child.stderrTail
        return yield* Effect.fail(
          new TunnelError({
            reason: detail(
              stderr,
              `SSH tunnel to ${connection.host} exited with code ${startup.exitCode}`,
            ),
          }),
        )
      }

      yield* Deferred.succeed(ready, void 0)
      const exitCode = yield* child.exitCode
      const stderr = yield* child.stderrTail
      return yield* Effect.fail(
        new TunnelError({
          reason: detail(stderr, `SSH tunnel to ${connection.host} exited with code ${exitCode}`),
        }),
      )
    }),
  ).pipe(
    Effect.catchTag("SubprocessError", (error) =>
      Effect.fail(
        new TunnelError({
          reason: `cannot run SSH tunnel to ${connection.host}: ${error.message}`,
        }),
      ),
    ),
  )

const retrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.modifyDelay(({ duration }) =>
    Effect.succeed(Duration.min(duration, Duration.seconds(8))),
  ),
)

const removeSocket = (socketPath: string): Effect.Effect<void> =>
  Effect.tryPromise(() => unlink(socketPath)).pipe(Effect.ignore)

export class Remote extends Context.Service<
  Remote,
  {
    readonly label: string
    readonly start: Effect.Effect<void, TunnelError, Scope.Scope>
  }
>()("atc-tui/Remote") {}

export const make = Effect.gen(function* () {
  const config = yield* Config.ClientConfig
  if (config.connection.type === "local") {
    return Remote.of({ label: config.endpoint.origin, start: Effect.void })
  }

  const connection = config.connection
  const subprocess = yield* Subprocess.Subprocess
  const start = Effect.gen(function* () {
    yield* Effect.addFinalizer(() => removeSocket(connection.socketPath))
    const ready = yield* Deferred.make<void>()
    const lastFailure = yield* Ref.make<string | undefined>(undefined)
    const attempt = superviseAttempt(subprocess, config, connection, ready).pipe(
      Effect.tapError((error) => Ref.set(lastFailure, error.reason)),
    )
    yield* attempt.pipe(Effect.retry({ schedule: retrySchedule }), Effect.forkScoped)
    yield* Deferred.await(ready).pipe(
      Effect.timeoutOrElse({
        duration: STARTUP_TIMEOUT,
        orElse: () =>
          Ref.get(lastFailure).pipe(
            Effect.flatMap((reason) =>
              Effect.fail(
                new TunnelError({
                  reason:
                    reason ??
                    `SSH tunnel to ${connection.host} did not reach its App Server within 15 seconds`,
                }),
              ),
            ),
          ),
      }),
    )
  })
  return Remote.of({
    label: `${connection.host} via SSH`,
    start,
  })
})

export const layer = Layer.effect(Remote)(make)
