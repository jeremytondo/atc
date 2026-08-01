import { query, type SDKMessage } from "@anthropic-ai/claude-agent-sdk"
import { Console, Duration, Effect, Option, Runtime, Schema, Stream } from "effect"
import { Command } from "effect/unstable/cli"
import { existsSync } from "node:fs"
import { mkdtemp, rm } from "node:fs/promises"
import { createRequire } from "node:module"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import * as BuildInfo from "./buildInfo.ts"
import * as Subprocess from "./subprocess.ts"

// Unstable provider smoke checks (ATC-88). `atc smoke ...` proves that the
// compiled executable can launch each provider technology and complete one
// bounded structured round trip. The command is hidden, is not a stable
// interface, and will be replaced by the production provider integration.
// Provider SDK and protocol types stay private to this module.

export class SmokeError extends Schema.TaggedErrorClass<SmokeError>()("SmokeError", {
  provider: Schema.Literals(["codex", "claude"]),
  message: Schema.String,
}) {}

/** Failure already printed as diagnostics; stops runMain's loud re-report. */
class SmokeReported extends Schema.TaggedErrorClass<SmokeReported>()("SmokeReported", {}) {
  override readonly [Runtime.errorReported] = false
}

// --- Executable resolution ---------------------------------------------------

/** Codex: explicit env override, then PATH. */
export const resolveCodexExecutable = Effect.suspend(() => {
  const override = process.env["ATC_CODEX_EXECUTABLE"]
  if (override !== undefined && override !== "") return Effect.succeed(override)
  const found = Bun.which("codex")
  if (found !== null) return Effect.succeed(found)
  return Effect.fail(
    new SmokeError({
      provider: "codex",
      message: "codex not found on PATH; install the Codex CLI or set ATC_CODEX_EXECUTABLE",
    }),
  )
})

/**
 * Claude Code: explicit env override, then the packaged platform binary staged
 * next to this executable (compiled distribution), then the platform package
 * in node_modules (running from source). Never the working directory.
 */
export const resolveClaudeCodeExecutable = Effect.suspend(() => {
  const override = process.env["ATC_CLAUDE_CODE_EXECUTABLE"]
  if (override !== undefined && override !== "") return Effect.succeed(override)
  const adjacent = join(dirname(process.execPath), "claude")
  if (existsSync(adjacent)) return Effect.succeed(adjacent)
  const platformPackage = `@anthropic-ai/claude-agent-sdk-${process.platform}-${process.arch}`
  try {
    const resolved = createRequire(import.meta.url).resolve(`${platformPackage}/claude`)
    if (existsSync(resolved)) return Effect.succeed(resolved)
  } catch {
    // No node_modules in a compiled executable; fall through to the error.
  }
  return Effect.fail(
    new SmokeError({
      provider: "claude",
      message:
        "Claude Code executable not found; tried $ATC_CLAUDE_CODE_EXECUTABLE, " +
        `${adjacent}, and ${platformPackage} in node_modules. ` +
        "Run `mise run build` to stage it, or set ATC_CLAUDE_CODE_EXECUTABLE.",
    }),
  )
})

// --- Shared termination checks ----------------------------------------------

const isAlive = (pid: number): boolean => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

const verifyProcessGone = (provider: "codex" | "claude", pid: number) =>
  Effect.gen(function* () {
    for (let attempt = 0; attempt < 40 && isAlive(pid); attempt++) {
      yield* Effect.sleep("50 millis")
    }
    if (isAlive(pid)) {
      return yield* Effect.fail(
        new SmokeError({ provider, message: `child process ${pid} is still alive after cleanup` }),
      )
    }
  })

/** No direct child of this process may outlive a smoke check. */
const verifyNoChildOrphans = (provider: "codex" | "claude") =>
  Effect.gen(function* () {
    const subprocess = yield* Subprocess.Subprocess
    // The SDK reaps its child asynchronously, so allow a short settle window.
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
          yield* pgrep.exitCode // 0 = matches, 1 = none; either is a clean run
          return lines.filter((line) => line.trim() !== "")
        }),
      ).pipe(Effect.mapError((e) => new SmokeError({ provider, message: e.message })))
      if (survivors.length === 0) return
      if (attempt >= 20) {
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

interface JsonRpcMessage {
  readonly id?: unknown
  readonly result?: unknown
  readonly error?: { readonly code?: unknown; readonly message?: unknown }
}

const parseJsonRpcLine = (line: string) =>
  Effect.try({
    try: () => JSON.parse(line) as JsonRpcMessage,
    catch: () =>
      new SmokeError({
        provider: "codex",
        message: `invalid JSON on codex app-server stdout: ${line.slice(0, 200)}`,
      }),
  })

const failWithStderr = (child: Subprocess.Child, message: string) =>
  Effect.gen(function* () {
    const tail = yield* child.stderrTail
    const diagnostics = tail.length === 0 ? "" : `\nprovider stderr:\n${tail.join("\n")}`
    return yield* Effect.fail(new SmokeError({ provider: "codex", message: message + diagnostics }))
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
    let pid = 0
    yield* Effect.scoped(
      Effect.gen(function* () {
        const child = yield* subprocess.spawn(spec)
        pid = child.pid
        yield* Console.log(`launched ${spec.executable} (pid ${pid})`)
        yield* child.writeLine(
          JSON.stringify({
            id: 1,
            method: "initialize",
            params: {
              clientInfo: { name: "atc", title: "ATC App Server", version: build.version },
              capabilities: null,
            },
          }),
        )
        const response = yield* child.stdoutLines.pipe(
          Stream.mapEffect(parseJsonRpcLine),
          Stream.filter((message) => message.id === 1),
          Stream.runHead,
          Effect.timeoutOrElse({
            duration: initializeTimeout,
            orElse: () =>
              failWithStderr(
                child,
                `timed out after ${Duration.format(Duration.fromInputUnsafe(initializeTimeout))} waiting for the initialize response`,
              ),
          }),
        )
        if (Option.isNone(response)) {
          return yield* failWithStderr(child, "codex app-server closed stdout before responding")
        }
        if (response.value.error !== undefined) {
          return yield* failWithStderr(
            child,
            `initialize failed: ${JSON.stringify(response.value.error)}`,
          )
        }
        yield* Console.log("initialize round trip OK")
        yield* child.writeLine(JSON.stringify({ method: "initialized", params: {} }))
        yield* child.endInput
        const exitCode = yield* child.exitCode.pipe(
          Effect.timeoutOrElse({ duration: "5 seconds", orElse: () => Effect.succeed(null) }),
          Effect.orElseSucceed(() => null),
        )
        yield* exitCode === null
          ? Console.log("codex app-server did not exit on stdin EOF; terminating it")
          : Console.log(`codex app-server exited with code ${exitCode}`)
      }),
    )
    yield* verifyProcessGone("codex", pid)
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
  const env: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (value !== undefined) env[key] = value
  }
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
 * any repository.
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
      const summary = yield* Effect.tryPromise({
        try: () => claudeRoundTrip(claudeExecutable, cwd, abortController),
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
      )
      yield* Console.log(
        `claude session ${summary.sessionId} answered: ${JSON.stringify(summary.text)}`,
      )
      yield* verifyNoChildOrphans("claude")
      yield* Console.log("PASS: Claude Agent SDK round trip complete; no orphaned children")
    }),
  )

// --- CLI wiring ---------------------------------------------------------------

/** Print the failure as actionable diagnostics, then fail quietly. */
const reported = <A, R>(effect: Effect.Effect<A, SmokeError | Subprocess.SubprocessError, R>) =>
  effect.pipe(
    Effect.catchTag(["SmokeError", "SubprocessError"], (error) =>
      Console.error(
        error._tag === "SmokeError"
          ? `atc smoke ${error.provider}: ${error.message}`
          : `atc smoke: ${error.executable} ${error.operation} failed: ${error.message}`,
      ).pipe(Effect.andThen(Effect.fail(new SmokeReported()))),
    ),
  )

const codexCommand = Command.make("codex", {}, () =>
  reported(
    Effect.gen(function* () {
      const executable = yield* resolveCodexExecutable
      yield* codexSmoke({
        executable,
        args: ["app-server", "--stdio"],
        env: {},
        extendEnv: true,
        forceKillAfter: "2 seconds",
      })
    }),
  ),
).pipe(Command.withDescription("[unstable] Codex app-server launch and initialize round trip"))

const claudeCommand = Command.make("claude", {}, () =>
  reported(
    Effect.gen(function* () {
      const executable = yield* resolveClaudeCodeExecutable
      yield* claudeSmoke(executable)
    }),
  ),
).pipe(
  Command.withDescription("[unstable] Claude Agent SDK round trip via the packaged executable"),
)

/** Hidden and unstable: a de-risking entrypoint, not part of the CLI surface. */
export const smoke = Command.make("smoke").pipe(
  Command.withDescription("[unstable] Provider smoke checks for the compiled executable (ATC-88)"),
  Command.withSubcommands([codexCommand, claudeCommand]),
  Command.withHidden,
)
