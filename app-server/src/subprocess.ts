import { Cause, Context, Duration, Effect, Layer, Queue, Schema, Scope, Stream } from "effect"
import type { PlatformError } from "effect/PlatformError"
import { ChildProcess, ChildProcessSpawner } from "effect/unstable/process"

// The generic subprocess seam: how the App Server launches and supervises
// child processes. Children are acquired in Scopes, so closing the scope —
// on success, failure, timeout, or interruption — terminates the child
// (SIGTERM, escalating to SIGKILL after `forceKillAfter`). Callers compose
// timeouts with Effect.timeout; the scope guarantees cleanup.

/** How much recent stderr to keep for diagnostics. */
const STDERR_TAIL_LINES = 100
/** Individual captured lines are truncated to keep diagnostics bounded. */
const MAX_CAPTURED_LINE_LENGTH = 2_000

export class SubprocessError extends Schema.TaggedErrorClass<SubprocessError>()(
  "SubprocessError",
  {
    executable: Schema.String,
    operation: Schema.Literals(["spawn", "stdin", "stdout", "exit"]),
    message: Schema.String,
  },
) {}

export interface SpawnSpec {
  /** Absolute path or executable name; resolution happens before spawning. */
  readonly executable: string
  readonly args?: ReadonlyArray<string>
  /** Explicit environment for the child. Nothing is inherited implicitly. */
  readonly env?: Record<string, string>
  /** Merge `env` on top of the parent environment instead of replacing it. */
  readonly extendEnv?: boolean
  readonly cwd?: string
  /** Grace period before SIGKILL when the child ignores SIGTERM at cleanup. */
  readonly forceKillAfter?: Duration.Input
}

export interface Child {
  readonly pid: number
  /** stdout as a line stream. Consume it while the child runs. */
  readonly stdoutLines: Stream.Stream<string, SubprocessError>
  /** Snapshot of the most recent stderr lines, for failure diagnostics. */
  readonly stderrTail: Effect.Effect<ReadonlyArray<string>>
  /** Write one line to the child's stdin. */
  readonly writeLine: (line: string) => Effect.Effect<void>
  /** Close the child's stdin (EOF). */
  readonly endInput: Effect.Effect<void>
  /** Await exit. Fails if the child was terminated by a signal. */
  readonly exitCode: Effect.Effect<number, SubprocessError>
}

export class Subprocess extends Context.Service<
  Subprocess,
  {
    readonly spawn: (spec: SpawnSpec) => Effect.Effect<Child, SubprocessError, Scope.Scope>
  }
>()("app-server/Subprocess") {}

const truncate = (line: string): string =>
  line.length > MAX_CAPTURED_LINE_LENGTH ? `${line.slice(0, MAX_CAPTURED_LINE_LENGTH)}…` : line

const mapPlatformError =
  (executable: string, operation: SubprocessError["operation"]) =>
  (error: PlatformError): SubprocessError =>
    new SubprocessError({ executable, operation, message: error.message })

const spawn = Effect.fnUntraced(function* (
  spawner: ChildProcessSpawner.ChildProcessSpawner["Service"],
  spec: SpawnSpec,
) {
  const encoder = new TextEncoder()
  const stdin = yield* Queue.make<Uint8Array, Cause.Done>()
  const handle = yield* spawner
    .spawn(
      ChildProcess.make(spec.executable, [...(spec.args ?? [])], {
        ...(spec.cwd !== undefined ? { cwd: spec.cwd } : {}),
        env: spec.env ?? {},
        extendEnv: spec.extendEnv ?? false,
        stdin: { stream: Stream.fromQueue(stdin) },
        killSignal: "SIGTERM",
        forceKillAfter: spec.forceKillAfter ?? "2 seconds",
      }),
    )
    .pipe(Effect.mapError(mapPlatformError(spec.executable, "spawn")))

  // Drain stderr concurrently into a bounded tail so a chatty child can
  // neither fill the pipe (deadlock) nor grow diagnostics without limit.
  const stderrTail: Array<string> = []
  yield* handle.stderr.pipe(
    Stream.decodeText,
    Stream.splitLines,
    Stream.runForEach((line) =>
      Effect.sync(() => {
        stderrTail.push(truncate(line))
        if (stderrTail.length > STDERR_TAIL_LINES) stderrTail.shift()
      }),
    ),
    Effect.ignore,
    Effect.forkScoped,
  )

  const child: Child = {
    pid: handle.pid,
    stdoutLines: handle.stdout.pipe(
      Stream.decodeText,
      Stream.splitLines,
      Stream.mapError(mapPlatformError(spec.executable, "stdout")),
    ),
    stderrTail: Effect.sync(() => [...stderrTail]),
    writeLine: (line) => Effect.asVoid(Queue.offer(stdin, encoder.encode(`${line}\n`))),
    endInput: Effect.asVoid(Queue.end(stdin)),
    exitCode: handle.exitCode.pipe(
      Effect.map((code) => code as number),
      Effect.mapError(mapPlatformError(spec.executable, "exit")),
    ),
  }
  return child
})

export const layer = Layer.effect(Subprocess)(
  Effect.gen(function* () {
    const spawner = yield* ChildProcessSpawner.ChildProcessSpawner
    return { spawn: (spec) => spawn(spawner, spec) }
  }),
)
