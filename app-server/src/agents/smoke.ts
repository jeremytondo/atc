import { query, type SDKMessage } from "@anthropic-ai/claude-agent-sdk"
import { Console, Deferred, Duration, Effect, Runtime, Schema, Stream } from "effect"
import { Command } from "effect/unstable/cli"
import { mkdtemp, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { AgentUnavailable, resolveProviderExecutable } from "./agentAdapter.ts"
import * as BuildInfo from "../platform/buildInfo.ts"
import { AppConfig, ConfigLoadError } from "../platform/config.ts"
import * as Config from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"

// Compiled-artifact provider smoke checks (ATC-88). `atc smoke ...` proves
// the COMPILED executable can launch each provider technology, complete one
// bounded structured round trip, and leave no orphaned children. This is
// deliberately not redundant with the adapters' own live smoke tests, which
// run from source in-process: the risks here are compiled-binary ones (the
// bundled Claude Agent SDK spawning its child, executable resolution outside
// the repo), and `mise run test:smoke` drives this command after Bun, Effect,
// or Agent SDK upgrades. The command stays hidden and unstable. Caveat: the
// codex check speaks the stdio transport, not the production detached-server
// path (codexServer.ts) — it asserts only that the configured executable
// launches and answers JSON-RPC from the compiled binary. Provider SDK and
// protocol types stay private to this module.

export class SmokeError extends Schema.TaggedErrorClass<SmokeError>()("SmokeError", {
  provider: Schema.Literals(["codex", "claude"]),
  message: Schema.String,
}) {}

/** Failure already printed as diagnostics; stops runMain's loud re-report. */
class SmokeReported extends Schema.TaggedErrorClass<SmokeReported>()("SmokeReported", {}) {
  override readonly [Runtime.errorReported] = false
}

// --- Shared termination checks ----------------------------------------------

const stderrSuffix = (tail: ReadonlyArray<string>): string =>
  tail.length === 0 ? "" : `\nprovider stderr:\n${tail.join("\n")}`

// Scope close already awaits child termination (SIGTERM + SIGKILL
// escalation), so this is not cleanup: it is the explicit no-orphan proof the
// smoke checks exist to provide.
const verifyProcessGone = (provider: "codex" | "claude", pid: number) =>
  Effect.gen(function* () {
    const exited = yield* Subprocess.waitForProcessExit(pid)
    if (!exited) {
      return yield* Effect.fail(
        new SmokeError({ provider, message: `child process ${pid} is still alive after cleanup` }),
      )
    }
  })

/** No direct child of this process may outlive a smoke check. */
const verifyNoChildOrphans = (provider: "codex" | "claude") =>
  Effect.gen(function* () {
    const subprocess = yield* Subprocess.Subprocess
    // The settle window must outlast the Agent SDK's full termination
    // escalation — abort closes stdin, SIGTERM follows after 2s, SIGKILL
    // after 5 more — because its timers are unref'ed and die with us.
    for (let attempt = 0; ; attempt++) {
      const survivors = yield* Effect.scoped(
        Effect.gen(function* () {
          const pgrep = yield* subprocess.spawn({
            executable: "pgrep",
            args: ["-l", "-P", String(process.pid)],
            env: {},
            extendEnv: true,
          })
          const lines = yield* Stream.runCollect(pgrep.stdoutLines)
          // 0 = matches, 1 = none; anything else means the check itself broke.
          const exitCode = yield* pgrep.exitCode
          if (exitCode > 1) {
            const tail = yield* pgrep.stderrTail
            return yield* Effect.fail(
              new SmokeError({
                provider,
                message: `orphan check could not run: pgrep exited with ${exitCode}${stderrSuffix(tail)}`,
              }),
            )
          }
          return lines.filter((line) => line.trim() !== "")
        }),
      ).pipe(
        Effect.catchTag("SubprocessError", (error) =>
          // A broken check is not a provider failure; say which one it is.
          Effect.fail(
            new SmokeError({ provider, message: `orphan check could not run: ${error.message}` }),
          ),
        ),
      )
      if (survivors.length === 0) return
      if (attempt >= 100) {
        return yield* Effect.fail(
          new SmokeError({
            provider,
            message: `orphaned child processes remain: ${survivors.join("; ")}`,
          }),
        )
      }
      yield* Effect.sleep("100 millis")
    }
  })

// --- Codex: one JSON-RPC initialize round trip over stdio --------------------

/**
 * The one message shape this smoke check inspects. A real round trip must
 * carry a `result` object or an `error` object; `null` counts as absent.
 */
const InitializeResponse = Schema.Struct({
  result: Schema.optional(Schema.NullOr(Schema.Record(Schema.String, Schema.Unknown))),
  error: Schema.optional(
    Schema.NullOr(
      Schema.Struct({
        code: Schema.optional(Schema.Number),
        message: Schema.optional(Schema.String),
      }),
    ),
  ),
})

const INITIALIZE_REQUEST_ID = 1

/** Loose parse: provider stdout noise (banners, warnings) is tolerated. */
const parseResponseLine = (line: string): unknown | undefined => {
  try {
    const parsed: unknown = JSON.parse(line)
    if (
      typeof parsed === "object" &&
      parsed !== null &&
      (parsed as { id?: unknown }).id === INITIALIZE_REQUEST_ID
    ) {
      return parsed
    }
  } catch {
    // Not protocol JSON; skip.
  }
  return undefined
}

const failWithStderr = (child: Subprocess.Child, message: string) =>
  Effect.gen(function* () {
    const tail = yield* child.stderrTail
    return yield* Effect.fail(
      new SmokeError({ provider: "codex", message: message + stderrSuffix(tail) }),
    )
  })

/**
 * Launch a codex app-server over stdio, complete `initialize`/`initialized`,
 * and shut the child down. The spawn spec and timeout are parameters so tests
 * can substitute a fixture app-server for the real executable.
 */
export const codexSmoke = (
  spec: Subprocess.SpawnSpec,
  options?: { readonly initializeTimeout?: Duration.Input },
) =>
  Effect.gen(function* () {
    const initializeTimeout = options?.initializeTimeout ?? "15 seconds"
    const build = yield* BuildInfo.BuildInfo
    const subprocess = yield* Subprocess.Subprocess
    let pid: number | null = null
    yield* Effect.scoped(
      Effect.gen(function* () {
        const child = yield* subprocess.spawn(spec)
        pid = child.pid
        yield* Console.log(`launched ${spec.executable} (pid ${pid})`)

        // Consume stdout exactly once for the child's whole lifetime, so the
        // pipe can never fill: the initialize response resolves the deferred
        // and everything else keeps draining.
        const responseSlot = yield* Deferred.make<unknown, SmokeError>()
        yield* Effect.gen(function* () {
          yield* child.stdoutLines.pipe(
            Stream.runForEach((line) => {
              const response = parseResponseLine(line)
              return response === undefined
                ? Effect.void
                : Effect.asVoid(Deferred.succeed(responseSlot, response))
            }),
            Effect.ignore,
          )
          // Stream end without a response = child closed stdout early.
          const tail = yield* child.stderrTail
          yield* Deferred.fail(
            responseSlot,
            new SmokeError({
              provider: "codex",
              message: "codex app-server closed stdout before responding" + stderrSuffix(tail),
            }),
          )
        }).pipe(Effect.forkScoped)

        yield* child.writeLine(
          JSON.stringify({
            id: INITIALIZE_REQUEST_ID,
            method: "initialize",
            params: {
              clientInfo: { name: "atc", title: "ATC App Server", version: build.version },
              capabilities: null,
            },
          }),
        )
        const raw = yield* Deferred.await(responseSlot).pipe(
          Effect.timeoutOrElse({
            duration: initializeTimeout,
            orElse: () =>
              failWithStderr(
                child,
                `timed out after ${Duration.format(Duration.fromInputUnsafe(initializeTimeout))} waiting for the initialize response`,
              ),
          }),
        )
        const response = yield* Schema.decodeUnknownEffect(InitializeResponse)(raw).pipe(
          Effect.mapError(
            (error) =>
              new SmokeError({
                provider: "codex",
                message: `unexpected initialize response shape: ${error.message}`,
              }),
          ),
        )
        if (response.error !== undefined && response.error !== null) {
          return yield* failWithStderr(
            child,
            `initialize failed: ${JSON.stringify(response.error)}`,
          )
        }
        if (response.result === undefined || response.result === null) {
          return yield* failWithStderr(
            child,
            "initialize response carried neither result nor error",
          )
        }
        yield* Console.log("initialize round trip OK")
        yield* child.writeLine(JSON.stringify({ method: "initialized", params: {} }))
        yield* child.endInput
        const exitCode = yield* child.exitCode.pipe(
          Effect.timeoutOrElse({ duration: "5 seconds", orElse: () => Effect.succeed(null) }),
          Effect.orElseSucceed(() => null),
        )
        // A clean shutdown means exit 0; the null branch is the deliberate
        // forced-cleanup path (EOF ignored, scope close terminates).
        if (exitCode !== null && exitCode !== 0) {
          return yield* failWithStderr(
            child,
            `codex app-server exited with code ${exitCode} after the round trip`,
          )
        }
        yield* exitCode === null
          ? Console.log("codex app-server did not exit on stdin EOF; terminating it")
          : Console.log("codex app-server exited cleanly on stdin EOF")
      }),
    )
    if (pid !== null) yield* verifyProcessGone("codex", pid)
    yield* Console.log("PASS: codex app-server round trip complete; child terminated")
  })

// --- Claude: one Agent SDK round trip ----------------------------------------

const describeError = (error: unknown): string =>
  error instanceof Error ? (error.stack ?? error.message) : String(error)

const claudeRoundTrip = async (
  pathToClaudeCodeExecutable: string,
  cwd: string,
  abortController: AbortController,
): Promise<{ readonly sessionId: string; readonly text: string }> => {
  // Controlled but complete environment: the child needs HOME and any
  // credential configuration the user has; nothing is added or dropped.
  const env = Subprocess.inheritedEnv()
  const stream = query({
    prompt: "Reply with exactly: ATC-SMOKE-OK",
    options: {
      abortController,
      cwd,
      env,
      pathToClaudeCodeExecutable,
      allowedTools: [],
      tools: [],
      settingSources: [],
      permissionMode: "dontAsk",
      maxTurns: 1,
      persistSession: false,
    },
  })
  let sessionId: string | undefined
  let resultText: string | undefined
  for await (const message of stream as AsyncIterable<SDKMessage>) {
    if (message.type === "system" && message.subtype === "init") {
      sessionId = message.session_id
    } else if (message.type === "result") {
      if (message.subtype !== "success") {
        throw new Error(`claude result was ${message.subtype}`)
      }
      resultText = message.result
    }
  }
  if (sessionId === undefined || resultText === undefined) {
    throw new Error("claude stream ended without a system/init and a successful result")
  }
  return { sessionId, text: resultText }
}

/**
 * One bounded round trip through the Claude Agent SDK using an explicitly
 * resolved packaged Claude Code executable, from a working directory outside
 * any repository. The no-orphan check runs on success *and* failure — the
 * failure path is exactly where orphans are likely.
 */
export const claudeSmoke = (claudeExecutable: string) =>
  Effect.scoped(
    Effect.gen(function* () {
      yield* Console.log(`claude code executable: ${claudeExecutable}`)
      const cwd = yield* Effect.acquireRelease(
        Effect.promise(() => mkdtemp(join(tmpdir(), "atc-smoke-"))),
        (dir) => Effect.promise(() => rm(dir, { recursive: true, force: true })),
      )
      const abortController = new AbortController()
      yield* Effect.addFinalizer(() => Effect.sync(() => abortController.abort()))
      const attempt = yield* Effect.result(
        Effect.tryPromise({
          // Wire Effect interruption straight through to the SDK.
          try: (signal) => {
            signal.addEventListener("abort", () => abortController.abort())
            return claudeRoundTrip(claudeExecutable, cwd, abortController)
          },
          catch: (error) => new SmokeError({ provider: "claude", message: describeError(error) }),
        }).pipe(
          Effect.timeoutOrElse({
            duration: "180 seconds",
            orElse: () =>
              Effect.fail(
                new SmokeError({
                  provider: "claude",
                  message: "timed out after 180s waiting for the Claude round trip",
                }),
              ),
          }),
        ),
      )
      // Whatever happened, the SDK's child must be gone before we finish
      // (and before the temp cwd is removed underneath it). Keep the original
      // failure primary so an orphan report never masks the root cause.
      abortController.abort()
      const orphanCheck = yield* Effect.result(verifyNoChildOrphans("claude"))
      if (attempt._tag === "Failure") {
        return yield* Effect.fail(
          orphanCheck._tag === "Failure"
            ? new SmokeError({
                provider: "claude",
                message: `${attempt.failure.message}\nadditionally: ${orphanCheck.failure.message}`,
              })
            : attempt.failure,
        )
      }
      if (orphanCheck._tag === "Failure") return yield* Effect.fail(orphanCheck.failure)
      yield* Console.log(
        `claude session ${attempt.success.sessionId} answered: ${JSON.stringify(attempt.success.text)}`,
      )
      yield* Console.log("PASS: Claude Agent SDK round trip complete; no orphaned children")
    }),
  )

// --- CLI wiring ---------------------------------------------------------------

/** Print the failure as actionable diagnostics, then fail quietly. */
const reportFailures = <A, R>(
  effect: Effect.Effect<
    A,
    SmokeError | Subprocess.SubprocessError | AgentUnavailable | ConfigLoadError,
    R
  >,
) =>
  effect.pipe(
    Effect.catchTag(
      ["SmokeError", "SubprocessError", "AgentUnavailable", "ConfigLoadError"],
      (error) =>
        Console.error(
          error._tag === "SubprocessError"
            ? `atc smoke: ${error.executable} ${error.operation} failed: ${error.message}`
            : error._tag === "ConfigLoadError"
              ? `atc smoke: ${error.source}: ${error.message}`
              : `atc smoke ${error.provider}: ${error.message}`,
        ).pipe(Effect.andThen(Effect.fail(new SmokeReported()))),
    ),
  )

// Provider executables come from the settled configuration (AppConfig
// codexExecutable / claudeExecutable) — the same values the production
// adapters read, so a smoke pass proves the configured provider works.
const codexCommand = Command.make("codex", {}, () =>
  reportFailures(
    Effect.gen(function* () {
      const config = yield* AppConfig
      const executable = yield* resolveProviderExecutable("codex", config.codexExecutable)
      yield* codexSmoke({
        executable,
        args: ["app-server", "--stdio"],
        env: {},
        extendEnv: true,
        forceKillAfter: "2 seconds",
      })
    }).pipe(Effect.provide(Config.layer)),
  ),
).pipe(Command.withDescription("[unstable] Codex app-server launch and initialize round trip"))

const claudeCommand = Command.make("claude", {}, () =>
  reportFailures(
    Effect.gen(function* () {
      const config = yield* AppConfig
      const executable = yield* resolveProviderExecutable("claude", config.claudeExecutable)
      yield* claudeSmoke(executable)
    }).pipe(Effect.provide(Config.layer)),
  ),
).pipe(
  Command.withDescription("[unstable] Claude Agent SDK round trip via the packaged executable"),
)

/** Hidden and unstable: the compiled-artifact check behind `mise run
 * test:smoke`, not part of the CLI surface. */
export const smoke = Command.make("smoke").pipe(
  Command.withDescription("[unstable] Provider smoke checks for the compiled executable (ATC-88)"),
  Command.withSubcommands([codexCommand, claudeCommand]),
  Command.withHidden,
)
