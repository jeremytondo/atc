import {
  Cause,
  Context,
  Deferred,
  Duration,
  Effect,
  Layer,
  Queue,
  Schema,
  Scope,
  Stream,
} from "effect"
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
/** How much recent PTY output (UTF-16 units) to keep for diagnostics. */
const PTY_OUTPUT_TAIL_LENGTH = 2_000
/**
 * PTY output chunks buffered ahead of the consumer. Terminals are lossy on
 * overflow by nature (the multiplexer repaints on reattach), so the oldest
 * chunks slide out rather than growing memory without bound.
 */
const PTY_OUTPUT_QUEUE_CAPACITY = 1_024

export class SubprocessError extends Schema.TaggedErrorClass<SubprocessError>()("SubprocessError", {
  executable: Schema.String,
  operation: Schema.Literals(["spawn", "stdin", "stdout", "exit", "resize"]),
  message: Schema.String,
}) {}

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

export interface PtySpawnSpec extends SpawnSpec {
  /** Initial pseudo-terminal size. */
  readonly cols?: number
  readonly rows?: number
}

/** Default PTY size when the caller has no client-provided dimensions yet. */
export const DEFAULT_PTY_COLS = 80
export const DEFAULT_PTY_ROWS = 24

export interface Child {
  readonly pid: number
  /**
   * stdout as a line stream. Single-consumer: run it exactly once, and
   * consume it while the child runs so the pipe never fills.
   */
  readonly stdoutLines: Stream.Stream<string, SubprocessError>
  /** Snapshot of the most recent stderr lines, for failure diagnostics. */
  readonly stderrTail: Effect.Effect<ReadonlyArray<string>>
  /** Write one line to the child's stdin; fails once stdin is closed. */
  readonly writeLine: (line: string) => Effect.Effect<void, SubprocessError>
  /** Close the child's stdin (EOF). Idempotent. */
  readonly endInput: Effect.Effect<void>
  /** Await exit. Fails if the child was terminated by a signal. */
  readonly exitCode: Effect.Effect<number, SubprocessError>
}

export interface PtyChild {
  readonly pid: number
  /**
   * Raw terminal output (the child's stdout and stderr interleaved through
   * the PTY). Single-consumer: run it exactly once; it ends at PTY EOF. A
   * consumer slower than the child loses the oldest chunks once the bounded
   * buffer fills.
   */
  readonly output: Stream.Stream<Uint8Array, SubprocessError>
  /** Snapshot of recent decoded output, for failure diagnostics. */
  readonly outputTail: Effect.Effect<string>
  /** Write raw bytes to the PTY — the child sees them as terminal input. */
  readonly write: (data: Uint8Array | string) => Effect.Effect<void, SubprocessError>
  /** Resize the PTY; the child observes it as a window-size change. */
  readonly resize: (size: {
    readonly cols: number
    readonly rows: number
  }) => Effect.Effect<void, SubprocessError>
  /** Await exit. Fails if the child was terminated by a signal. */
  readonly exitCode: Effect.Effect<number, SubprocessError>
}

export class Subprocess extends Context.Service<
  Subprocess,
  {
    readonly spawn: (spec: SpawnSpec) => Effect.Effect<Child, SubprocessError, Scope.Scope>
    /**
     * Spawn under a Bun-native pseudo-terminal, with the same scope
     * guarantees as `spawn`: closing the scope terminates the child (SIGTERM
     * escalating to SIGKILL) and releases the PTY.
     */
    readonly spawnPty: (spec: PtySpawnSpec) => Effect.Effect<PtyChild, SubprocessError, Scope.Scope>
  }
>()("app-server/Subprocess") {}

/** True while `pid` refers to a live (or not yet reaped) process. */
export const isProcessAlive = (pid: number): boolean => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/**
 * Poll until `pid` is gone; resolves to whether it exited within the window.
 * Scope close already awaits child termination, so callers use this as an
 * explicit post-condition check, not as cleanup.
 */
export const waitForProcessExit = (
  pid: number,
  options?: { readonly attempts?: number; readonly interval?: Duration.Input },
): Effect.Effect<boolean> =>
  Effect.gen(function* () {
    const attempts = options?.attempts ?? 40
    for (let attempt = 0; attempt < attempts && isProcessAlive(pid); attempt++) {
      yield* Effect.sleep(options?.interval ?? "50 millis")
    }
    return !isProcessAlive(pid)
  })

const truncate = (line: string): string =>
  line.length > MAX_CAPTURED_LINE_LENGTH ? `${line.slice(0, MAX_CAPTURED_LINE_LENGTH)}…` : line

/** The one place a SubprocessError is built from an arbitrary cause. */
const subprocessError =
  (executable: string, operation: SubprocessError["operation"]) =>
  (error: unknown): SubprocessError =>
    new SubprocessError({
      executable,
      operation,
      message: error instanceof Error ? error.message : String(error),
    })

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
    .pipe(Effect.mapError(subprocessError(spec.executable, "spawn")))

  // End stdin once the child exits so later writes fail loudly instead of
  // vanishing into a dead pipe. (A write in the instant between exit and its
  // observation can still be lost — inherent to pipes.)
  yield* handle.exitCode.pipe(Effect.ignore, Effect.andThen(Queue.end(stdin)), Effect.forkScoped)

  // Drain stderr concurrently into a bounded tail so a chatty child cannot
  // fill the pipe (deadlock) or grow diagnostics without limit. Lines are
  // split by hand rather than with Stream.splitLines so the pending fragment
  // stays capped even when the child never writes a newline.
  const stderrTail: Array<string> = []
  const stderrDrained = yield* Deferred.make<void>()
  const pushLine = (line: string): void => {
    stderrTail.push(truncate(line))
    if (stderrTail.length > STDERR_TAIL_LINES) stderrTail.shift()
  }
  let pendingLine = ""
  yield* Effect.gen(function* () {
    yield* handle.stderr.pipe(
      Stream.decodeText,
      Stream.runForEach((chunk) =>
        Effect.sync(() => {
          pendingLine += chunk
          let newline
          while ((newline = pendingLine.indexOf("\n")) !== -1) {
            pushLine(pendingLine.slice(0, newline).replace(/\r$/, ""))
            pendingLine = pendingLine.slice(newline + 1)
          }
          // Cap the unterminated fragment; overflow is dropped, and truncate
          // marks the eventual line as cut.
          if (pendingLine.length > MAX_CAPTURED_LINE_LENGTH) {
            pendingLine = pendingLine.slice(0, MAX_CAPTURED_LINE_LENGTH + 1)
          }
        }),
      ),
    )
    if (pendingLine !== "") pushLine(pendingLine)
  }).pipe(
    Effect.ensuring(Deferred.succeed(stderrDrained, void 0)),
    Effect.ignore,
    Effect.forkScoped,
  )

  const child: Child = {
    pid: handle.pid,
    stdoutLines: handle.stdout.pipe(
      Stream.decodeText,
      Stream.splitLines,
      Stream.mapError(subprocessError(spec.executable, "stdout")),
    ),
    stderrTail: Effect.sync(() => [...stderrTail]),
    writeLine: (line) =>
      Queue.offer(stdin, encoder.encode(`${line}\n`)).pipe(
        Effect.flatMap((accepted) =>
          accepted
            ? Effect.void
            : Effect.fail(
                new SubprocessError({
                  executable: spec.executable,
                  operation: "stdin",
                  message: "stdin is closed (input ended or child exited)",
                }),
              ),
        ),
      ),
    endInput: Effect.asVoid(Queue.end(stdin)),
    // Exit alone does not mean the final stderr lines have been observed;
    // wait (bounded — a grandchild could hold the pipe open) for the drain
    // so an immediate stderrTail read after exit sees the diagnostics.
    exitCode: handle.exitCode.pipe(
      Effect.map((code) => code as number),
      Effect.mapError(subprocessError(spec.executable, "exit")),
      Effect.tap(() =>
        Deferred.await(stderrDrained).pipe(Effect.timeout("1 second"), Effect.ignore),
      ),
    ),
  }
  return child
})

// The PTY variant uses Bun's native pseudo-terminal (Bun.spawn `terminal:`),
// which Effect's ChildProcessSpawner does not model. This module is the one
// place allowed to call Bun.spawn, so the scoped kill+reap guarantees stay
// uniform across both variants.
const spawnPty = Effect.fnUntraced(function* (spec: PtySpawnSpec) {
  const output = yield* Queue.make<Uint8Array, Cause.Done>({
    capacity: PTY_OUTPUT_QUEUE_CAPACITY,
    strategy: "sliding",
  })
  const decoder = new TextDecoder("utf-8", { fatal: false })
  let outputTail = ""
  // Closure is tracked explicitly because Bun's terminal.write can keep
  // reporting full byte counts briefly after the child exits (observed on
  // Linux), which would let writes vanish while claiming success.
  let ptyClosed = false
  const markClosed = () => {
    ptyClosed = true
    Queue.endUnsafe(output)
  }

  const proc = yield* Effect.acquireRelease(
    Effect.try({
      try: () =>
        Bun.spawn([spec.executable, ...(spec.args ?? [])], {
          ...(spec.cwd !== undefined ? { cwd: spec.cwd } : {}),
          env: spec.extendEnv === true ? { ...process.env, ...spec.env } : (spec.env ?? {}),
          terminal: {
            cols: spec.cols ?? DEFAULT_PTY_COLS,
            rows: spec.rows ?? DEFAULT_PTY_ROWS,
            // Bun may reuse the buffer between callbacks; copy before queueing.
            data: (_terminal, data) => {
              Queue.offerUnsafe(output, Uint8Array.from(data))
              outputTail = (outputTail + decoder.decode(data, { stream: true })).slice(
                -PTY_OUTPUT_TAIL_LENGTH,
              )
            },
            exit: () => {
              markClosed()
            },
          },
        }),
      catch: subprocessError(spec.executable, "spawn"),
    }),
    (proc) =>
      Effect.promise(async () => {
        proc.kill("SIGTERM")
        const escalation = setTimeout(
          () => proc.kill("SIGKILL"),
          Duration.toMillis(spec.forceKillAfter ?? "2 seconds"),
        )
        await proc.exited.catch(() => {})
        clearTimeout(escalation)
        try {
          proc.terminal?.close()
        } catch {
          // Already closed by the PTY EOF path.
        }
      }),
  )

  // The PTY exit callback covers EOF; this covers any exit path where the
  // callback never fires, so consumers of `output` cannot hang. Registered
  // before `exitCode` can be awaited, so a caller that has observed exit
  // always sees the PTY as closed.
  proc.exited.finally(markClosed).catch(() => {})

  const terminal = proc.terminal
  const terminalOp = (
    operation: SubprocessError["operation"],
    run: () => void,
  ): Effect.Effect<void, SubprocessError> =>
    Effect.try({ try: run, catch: subprocessError(spec.executable, operation) })

  const child: PtyChild = {
    pid: proc.pid,
    output: Stream.fromQueue(output),
    outputTail: Effect.sync(() => outputTail),
    write: (data) =>
      terminalOp("stdin", () => {
        // terminal.write cannot be trusted after exit (see markClosed).
        if (ptyClosed) {
          throw new Error("terminal is closed (child exited)")
        }
        const length = typeof data === "string" ? Buffer.byteLength(data) : data.byteLength
        const written = terminal?.write(data) ?? 0
        // Bun buffers internally, so a short write signals a real problem
        // rather than ordinary backpressure; losing keystrokes silently is
        // worse than failing the write.
        if (written < length) {
          throw new Error(`short write: ${written} of ${length} bytes accepted`)
        }
      }),
    resize: (size) => terminalOp("resize", () => terminal?.resize(size.cols, size.rows)),
    exitCode: Effect.promise(() => proc.exited).pipe(
      Effect.flatMap((code) =>
        proc.signalCode !== null
          ? Effect.fail(
              new SubprocessError({
                executable: spec.executable,
                operation: "exit",
                message: `terminated by ${proc.signalCode}`,
              }),
            )
          : Effect.succeed(code),
      ),
    ),
  }
  return child
})

export const layer = Layer.effect(Subprocess)(
  Effect.gen(function* () {
    const spawner = yield* ChildProcessSpawner.ChildProcessSpawner
    return { spawn: (spec) => spawn(spawner, spec), spawnPty }
  }),
)
