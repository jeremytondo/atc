import { Deferred, Context, Duration, Effect, Layer, Ref, Schedule, Schema, Stream } from "effect"
import { existsSync } from "node:fs"
import { AppConfig } from "./config.ts"
import * as Subprocess from "./subprocess.ts"

// Tailscale exposure is always a foreground `tailscale serve` child owned by
// the server scope: the route therefore disappears when ATC exits. Trust is
// fail-closed against the status snapshot — only a child whose banner was
// observed is `running`. Runtime failures never take down the loopback server;
// the supervisor records one useful reason and retries forever with bounded
// jittered backoff.

export type TailscaleStatus =
  | { readonly state: "disabled" }
  | { readonly state: "starting" }
  | { readonly state: "running"; readonly url: string }
  | { readonly state: "error"; readonly reason: string }

export class Tailscale extends Context.Service<
  Tailscale,
  { readonly status: Effect.Effect<TailscaleStatus> }
>()("app-server/Tailscale") {}

/** Boot-time configuration failure: requested exposure cannot even be tried. */
export class TailscaleConfigError extends Schema.TaggedErrorClass<TailscaleConfigError>()(
  "TailscaleConfigError",
  { reason: Schema.String },
) {
  override get message(): string {
    return this.reason
  }
}

class AttemptFailed extends Schema.TaggedErrorClass<AttemptFailed>()("AttemptFailed", {
  reason: Schema.String,
}) {}

const StatusOutput = Schema.Struct({
  BackendState: Schema.String,
  Self: Schema.Struct({ DNSName: Schema.String }),
})

const decodeStatus = Schema.decodeEffect(Schema.fromJsonString(StatusOutput))
const STATUS_TIMEOUT: Duration.Input = "10 seconds"
const SERVE_READY_TIMEOUT: Duration.Input = "15 seconds"

const failed = (reason: string) => new AttemptFailed({ reason })

const detail = (lines: ReadonlyArray<string>, fallback: string): string => {
  const text = lines
    .map((line) => line.trim())
    .filter((line) => line !== "")
    .join(" ")
  return text === "" ? fallback : text
}

const backendReason = (backendState: string): string =>
  backendState === "NeedsLogin"
    ? "tailscale is logged out (BackendState NeedsLogin)"
    : backendState === "Stopped"
      ? "tailscale is not running"
      : `tailscale is not running (BackendState ${backendState})`

const runStatus = (
  subprocess: Subprocess.Subprocess["Service"],
  executable: string,
): Effect.Effect<{ readonly dnsName: string }, AttemptFailed> =>
  Effect.gen(function* () {
    const child = yield* subprocess.spawn({
      executable,
      args: ["status", "--json"],
      env: {},
      extendEnv: true,
    })
    yield* child.endInput
    const stdout = yield* Stream.runCollect(child.stdoutLines)
    const exitCode = yield* child.exitCode
    const stderr = yield* child.stderrTail
    if (exitCode !== 0) {
      return yield* Effect.fail(
        failed(detail(stderr, `tailscale status exited with code ${exitCode}`)),
      )
    }
    // The macOS GUI-bundled CLI reports some failures as plain text on stdout
    // with exit code 0, so quote the unexpected output instead of a bare
    // "invalid JSON" that would hide the actual diagnostic.
    const decoded = yield* decodeStatus(stdout.join("\n")).pipe(
      Effect.mapError(() =>
        failed(
          `tailscale status returned unexpected output: ${detail(stdout.slice(0, 3), "(empty)")}`,
        ),
      ),
    )
    if (decoded.BackendState !== "Running") {
      return yield* Effect.fail(failed(backendReason(decoded.BackendState)))
    }
    const dnsName = decoded.Self.DNSName.replace(/\.$/, "")
    if (dnsName === "") {
      return yield* Effect.fail(failed("tailscale status did not report this node's DNS name"))
    }
    return { dnsName }
  }).pipe(
    Effect.scoped,
    Effect.catchTag("SubprocessError", (error) =>
      Effect.fail(failed(`cannot run tailscale status (${error.operation}): ${error.message}`)),
    ),
    Effect.timeoutOrElse({
      duration: STATUS_TIMEOUT,
      orElse: () => Effect.fail(failed("tailscale status timed out")),
    }),
  )

const superviseServe = (
  subprocess: Subprocess.Subprocess["Service"],
  executable: string,
  port: number,
  dnsName: string,
  status: Ref.Ref<TailscaleStatus>,
) =>
  Effect.gen(function* () {
    const child = yield* subprocess.spawn({
      executable,
      args: ["serve", `--https=${port}`, `localhost:${port}`],
      env: {},
      extendEnv: true,
    })
    yield* child.endInput

    const stdoutReady = yield* Deferred.make<void>()
    yield* child.stdoutLines.pipe(
      Stream.runForEach((line) =>
        line.includes("https://") ? Deferred.succeed(stdoutReady, void 0) : Effect.void,
      ),
      Effect.ignore,
      Effect.forkScoped,
    )
    const stderrReady = Effect.gen(function* () {
      while (true) {
        const stderr = yield* child.stderrTail
        if (stderr.some((line) => line.includes("https://"))) return
        yield* Effect.sleep("50 millis")
      }
    })
    const banner = Effect.raceFirst(Deferred.await(stdoutReady), stderrReady).pipe(
      Effect.as({ _tag: "Ready" } as const),
    )
    const exited = child.exitCode.pipe(
      Effect.map((exitCode) => ({ _tag: "Exited", exitCode }) as const),
      Effect.catchTag("SubprocessError", (error) =>
        Effect.fail(failed(`tailscale serve exited: ${error.message}`)),
      ),
    )
    const startup = yield* Effect.raceFirst(banner, exited).pipe(
      Effect.timeoutOrElse({
        duration: SERVE_READY_TIMEOUT,
        orElse: () => Effect.fail(failed("tailscale serve did not become ready within 15 seconds")),
      }),
    )
    if (startup._tag === "Exited") {
      const stderr = yield* child.stderrTail
      return yield* Effect.fail(
        failed(detail(stderr, `tailscale serve exited with code ${startup.exitCode}`)),
      )
    }

    const url = `https://${dnsName}:${port}`
    yield* Ref.set(status, { state: "running", url })
    yield* Effect.logInfo(`tailscale serving at ${url}`)
    const exitCode = yield* child.exitCode.pipe(
      Effect.catchTag("SubprocessError", (error) =>
        Effect.fail(failed(`tailscale serve exited: ${error.message}`)),
      ),
    )
    const stderr = yield* child.stderrTail
    return yield* Effect.fail(
      failed(detail(stderr, `tailscale serve exited with code ${exitCode}`)),
    )
  }).pipe(
    Effect.scoped,
    Effect.catchTag("SubprocessError", (error) =>
      Effect.fail(failed(`cannot run tailscale serve (${error.operation}): ${error.message}`)),
    ),
  )

const retrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.jittered,
  Schedule.modifyDelay(({ duration }) =>
    Effect.succeed(Duration.min(duration, Duration.seconds(30))),
  ),
)

export const layer = Layer.effect(Tailscale)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    if (!config.tailscale) {
      return { status: Effect.succeed({ state: "disabled" } as const) }
    }

    const subprocess = yield* Subprocess.Subprocess
    const configured = config.tailscaleExecutable
    const executable = Subprocess.resolveExecutable(configured)
    if (executable === null || !existsSync(executable)) {
      return yield* Effect.fail(
        new TailscaleConfigError({
          reason:
            `tailscale executable "${configured}" not found; install Tailscale or set ` +
            "tailscaleExecutable in config.toml / ATC_TAILSCALE_EXECUTABLE",
        }),
      )
    }

    const status = yield* Ref.make<TailscaleStatus>({ state: "starting" })
    const attempt = Effect.gen(function* () {
      yield* Ref.set(status, { state: "starting" })
      const { dnsName } = yield* runStatus(subprocess, executable)
      yield* superviseServe(subprocess, executable, config.port, dnsName, status)
    }).pipe(
      Effect.tapError((error) =>
        Ref.set(status, { state: "error", reason: error.reason }).pipe(
          Effect.andThen(Effect.logWarning(error.reason)),
        ),
      ),
    )
    yield* attempt.pipe(Effect.retry({ schedule: retrySchedule }), Effect.forkScoped)
    return { status: Ref.get(status) }
  }),
)
