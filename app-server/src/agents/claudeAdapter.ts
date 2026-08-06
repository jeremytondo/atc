import type {
  CanUseTool,
  HookCallbackMatcher,
  HookEvent,
  Options,
  SDKMessage,
  SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk"
import { query } from "@anthropic-ai/claude-agent-sdk"
import {
  Cause,
  Context,
  Deferred,
  Duration,
  Effect,
  FileSystem,
  Layer,
  Queue,
  Stream,
} from "effect"
import type { AgentActivity, AgentAdapter, AgentConnection, AgentEvent } from "./agentAdapter.ts"
import {
  AgentConflict,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentResumeFailed,
  AgentUnavailable,
  makeVersionGate,
  resolveProviderExecutable,
  samePath,
} from "./agentAdapter.ts"
import * as path from "node:path"
import * as ClaudeHooks from "./claudeHooks.ts"
import { AppConfig } from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"

// The Claude AgentAdapter (ATC-123): sessions are held in-process Agent SDK
// `query()` calls with streaming input (the only mode that can interrupt),
// no child supervision needed. Invariants beyond the seam's:
//
//   - The SDK emits NO identity evidence until the first user message
//     (probed 2026-08-03), so resume verification completes at the first
//     startTurn: an unknown id surfaces there as AgentResumeFailed (the
//     provider's single error result, no replacement session created), a
//     disagreeing session_id or cwd as AgentIdentityMismatch. Create sends
//     its initial input immediately, so create verifies before returning.
//   - Error-path idle derives from the `result` message itself — Stop /
//     StopFailure hooks do NOT fire on error results, and the SDK iterator
//     throws after one; both are normal turn-end shapes here, never
//     transport defects.
//   - A turn that ends in anything but success ends the connection (the
//     interrupted/failed outcome is emitted first); callers re-resume.
//     Success keeps the held query open for further turns.
//   - The SDK is the one sanctioned exception to two repo invariants: it
//     spawns the claude child itself (not via Subprocess) and the child
//     env is built from process.env directly — the SDK owns that process,
//     and the env must carry the user's credentials verbatim.
//   - One writer per session id, enforced here. Webhook hook activity is
//     NOT bridged into these feeds: a TUI-driven session has no adapter
//     connection by definition, and while ATC drives, the same vocabulary
//     already arrives as in-process SDK callbacks — a webhook delivery for
//     a live connection could only be stale or spoofed. TUI-driven activity
//     flows through observeSession below, which is the claudeHooks
//     subscriber; consumers stay above the seam.

/** The Claude Code version this adapter was validated against. */
export const CLAUDE_TESTED_VERSION = "2.1.221"

export class ClaudeAdapter extends Context.Service<ClaudeAdapter, AgentAdapter>()(
  "app-server/ClaudeAdapter",
) {}

/** The query() seam, injectable so fixture tests can script SDK streams. */
export type ClaudeQueryFn = (args: {
  readonly prompt: AsyncIterable<SDKUserMessage>
  readonly options: Options
}) => AsyncIterable<SDKMessage> & { readonly interrupt: () => Promise<unknown> }

export interface ClaudeAdapterOptions {
  readonly queryFn?: ClaudeQueryFn
  /** How long create/first-turn waits for the SDK's identity evidence. */
  readonly initTimeout?: Duration.Input
}

const protocolError = (reason: string) => new AgentProtocolError({ provider: "claude", reason })
const conflict = (reason: string) => new AgentConflict({ provider: "claude", reason })

/**
 * Environment for the SDK child: the user's environment (credentials) plus
 * the state-events opt-in, minus the nested-session markers a dev-mode ATC
 * would otherwise pass down (they silently disable transcript persistence,
 * which breaks resume).
 */
const NESTED_SESSION_VARIABLES = [
  "CLAUDE_CODE_CHILD_SESSION",
  "CLAUDECODE",
  "CLAUDE_CODE_ENTRYPOINT",
  "CLAUDE_CODE_SSE_PORT",
  "CLAUDE_CODE_SESSION_ID",
  "CLAUDE_CODE_BRIDGE_SESSION_ID",
  "CLAUDE_PID",
]

const claudeEnvironment = (): Record<string, string> => {
  const env: Record<string, string> = {}
  for (const [key, value] of Object.entries(process.env)) {
    if (value !== undefined) env[key] = value
  }
  env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
  for (const name of NESTED_SESSION_VARIABLES) delete env[name]
  return env
}

/** The hook vocabulary delivered in-process while ATC drives (and via the
 * webhook while a TUI drives — same normalization, claudeHooks.ts). */
const HOOK_EVENTS: ReadonlyArray<HookEvent> = [
  "UserPromptSubmit",
  "PreToolUse",
  "PostToolUse",
  "Stop",
  "StopFailure",
  "Notification",
  "PermissionRequest",
]

const userMessage = (text: string): SDKUserMessage => ({
  type: "user",
  message: { role: "user", content: text },
  parent_tool_use_id: null,
})

/** An async input queue the held query() drains; push feeds it more turns. */
const makeInputQueue = () => {
  const pending: Array<SDKUserMessage> = []
  let wake: (() => void) | null = null
  let closed = false
  const push = (message: SDKUserMessage): void => {
    pending.push(message)
    wake?.()
  }
  const close = (): void => {
    closed = true
    wake?.()
  }
  async function* stream(): AsyncGenerator<SDKUserMessage> {
    while (true) {
      const next = pending.shift()
      if (next !== undefined) {
        yield next
        continue
      }
      if (closed) return
      await new Promise<void>((resolve) => {
        wake = () => {
          wake = null
          resolve()
        }
      })
    }
  }
  return { push, close, stream }
}

type GateError = AgentResumeFailed | AgentIdentityMismatch | AgentProtocolError

interface LiveSession {
  /** The id resume promised (null for create — any id is acceptable). */
  readonly expectedSessionId: string | null
  readonly cwd: string
  readonly queue: Queue.Queue<AgentEvent, AgentProtocolError | Cause.Done>
  readonly abort: AbortController
  readonly pushInput: (text: string) => void
  readonly closeInput: () => void
  readonly initGate: Deferred.Deferred<void, GateError>
  interrupt: () => Promise<unknown>
  sessionId: string | null
  activity: AgentActivity
  activeTurn: string | null
  interruptRequested: boolean
  closed: boolean
  /** The session itself ended (terminal turn, transport, failed
   * verification) — as opposed to the caller closing its handle. */
  over: boolean
}

export const layerWith = (adapterOptions: ClaudeAdapterOptions) =>
  Layer.effect(ClaudeAdapter)(
    Effect.gen(function* () {
      const config = yield* AppConfig
      const subprocess = yield* Subprocess.Subprocess
      const fs = yield* FileSystem.FileSystem
      const hooks = yield* ClaudeHooks.ClaudeHooks
      const queryFn: ClaudeQueryFn = adapterOptions.queryFn ?? (query as unknown as ClaudeQueryFn)
      const initTimeout = adapterOptions.initTimeout ?? "60 seconds"

      /** Live sessions by verified (and, for resumes, expected) session id. */
      const sessions = new Map<string, LiveSession>()
      let nextTurn = 1

      const emit = (session: LiveSession, event: AgentEvent): void => {
        Queue.offerUnsafe(session.queue, event)
      }

      const setActivity = (session: LiveSession, activity: AgentActivity): void => {
        if (session.closed || session.activity === activity) return
        session.activity = activity
        emit(session, { type: "activity", activity })
      }

      const dropFromRegistry = (session: LiveSession): void => {
        for (const key of [session.expectedSessionId, session.sessionId]) {
          if (key !== null && sessions.get(key) === session) sessions.delete(key)
        }
      }

      const closeSession = (session: LiveSession, failure?: AgentProtocolError): void => {
        if (session.closed) return
        session.closed = true
        dropFromRegistry(session)
        if (failure !== undefined) {
          Queue.failCauseUnsafe(session.queue, Cause.fail(failure))
        } else {
          Queue.endUnsafe(session.queue)
        }
        session.closeInput()
        session.abort.abort()
      }

      const failGate = (session: LiveSession, error: GateError): void => {
        session.over = true
        Deferred.doneUnsafe(session.initGate, Effect.fail(error))
        // The feed fails loudly too: a consumer holding only `events` must
        // not mistake a dead session for a clean end.
        closeSession(session, protocolError(error.message))
      }

      const handleMessage = (session: LiveSession, message: SDKMessage): void => {
        if (session.closed) return
        if (message.type === "system" && message.subtype === "init") {
          if (
            session.expectedSessionId !== null &&
            message.session_id !== session.expectedSessionId
          ) {
            return failGate(
              session,
              new AgentIdentityMismatch({
                provider: "claude",
                field: "sessionId",
                expected: session.expectedSessionId,
                actual: message.session_id,
              }),
            )
          }
          if (!samePath(message.cwd, session.cwd)) {
            return failGate(
              session,
              new AgentIdentityMismatch({
                provider: "claude",
                field: "cwd",
                expected: session.cwd,
                actual: message.cwd,
              }),
            )
          }
          session.sessionId = message.session_id
          if (!sessions.has(message.session_id)) sessions.set(message.session_id, session)
          Deferred.doneUnsafe(session.initGate, Effect.void)
          return
        }
        if (message.type === "system" && message.subtype === "session_state_changed") {
          const state = (message as { state?: unknown }).state
          if (state === "running") setActivity(session, "working")
          if (state === "idle") setActivity(session, "idle")
          if (state === "requires_action") setActivity(session, "needs_input")
          return
        }
        if (message.type === "result") {
          const errors = (message as { errors?: ReadonlyArray<string> }).errors ?? []
          if (session.sessionId === null) {
            // The invalid-resume shape: one error result, no init, no
            // replacement session — the fail-closed contract. Classified
            // structurally (an error result before any identity evidence on
            // a resume), not by matching provider prose.
            const text = errors.join("; ")
            return failGate(
              session,
              session.expectedSessionId !== null && message.subtype !== "success"
                ? new AgentResumeFailed({
                    provider: "claude",
                    providerSessionId: session.expectedSessionId,
                    reason: text,
                  })
                : protocolError(`the session ended before identity evidence: ${text}`),
            )
          }
          const outcome =
            message.subtype === "success"
              ? "completed"
              : session.interruptRequested
                ? "interrupted"
                : "failed"
          const turnId = session.activeTurn
          session.activeTurn = null
          session.interruptRequested = false
          if (turnId !== null) {
            emit(
              session,
              outcome === "failed"
                ? {
                    type: "turnCompleted",
                    turnId,
                    outcome,
                    detail: `${message.subtype}: ${errors.join("; ")}`,
                  }
                : { type: "turnCompleted", turnId, outcome },
            )
          }
          // Error-path idle derives from the result itself (probe finding:
          // Stop/StopFailure do not fire here).
          setActivity(session, "idle")
          if (outcome !== "completed") {
            session.over = true
            closeSession(session)
          }
          return
        }
      }

      const consume = (session: LiveSession, source: AsyncIterable<SDKMessage>) =>
        Effect.tryPromise({
          try: async () => {
            for await (const message of source) {
              handleMessage(session, message)
              if (session.closed) return
            }
          },
          catch: (error) => (error instanceof Error ? error.message : String(error)),
        }).pipe(
          Effect.catch((reason) =>
            Effect.sync(() => {
              // The SDK iterator throws after an error result; when the
              // result was already handled the session is closed and this
              // is expected. Otherwise it is the turn's failure.
              if (session.closed) return
              if (session.sessionId === null) {
                return failGate(session, protocolError(`the SDK stream failed: ${reason}`))
              }
              const turnId = session.activeTurn
              session.activeTurn = null
              if (turnId !== null) {
                emit(session, {
                  type: "turnCompleted",
                  turnId,
                  outcome: session.interruptRequested ? "interrupted" : "failed",
                  detail: reason,
                })
              }
              setActivity(session, "idle")
              session.over = true
              closeSession(session)
            }),
          ),
          Effect.ensuring(Effect.sync(() => closeSession(session))),
        )

      const makeCanUseTool =
        (session: LiveSession): CanUseTool =>
        (toolName, _input, options) => {
          // Activity is NOT touched here: requires_action state events carry
          // that truthfully, and a synchronous needs_input/working round
          // trip would clobber overlapping callbacks once responses become
          // asynchronous (ATC-124).
          if (!session.closed) {
            const requestId = options.requestId
            const kind = toolName === "AskUserQuestion" ? "question" : "approval"
            emit(session, { type: "requestOpened", requestId, kind })
            emit(session, { type: "requestClosed", requestId })
          }
          return Promise.resolve({
            behavior: "deny",
            message: "atc does not answer provider requests yet (ATC-124)",
          })
        }

      const makeHooks = (
        session: LiveSession,
      ): Partial<Record<HookEvent, Array<HookCallbackMatcher>>> =>
        Object.fromEntries(
          HOOK_EVENTS.map((event) => [
            event,
            [
              {
                hooks: [
                  (input: { hook_event_name: string }) => {
                    const activity = ClaudeHooks.hookActivity(
                      input.hook_event_name,
                      input as unknown as Record<string, unknown>,
                    )
                    if (activity !== null && !session.closed) setActivity(session, activity)
                    return Promise.resolve({ continue: true as const })
                  },
                ],
              },
            ],
          ]),
        )

      const versionCheck = makeVersionGate(
        subprocess,
        "claude",
        config.claudeExecutable,
        CLAUDE_TESTED_VERSION,
      )

      const hookFilePaths = (providerSessionId: string) => ({
        headerFile: path.join(config.stateDir, `claude-hooks-${providerSessionId}.header`),
        settingsFile: path.join(config.stateDir, `claude-hooks-${providerSessionId}.json`),
      })

      const removeHookFiles = (providerSessionId: string) =>
        Effect.gen(function* () {
          const { headerFile, settingsFile } = hookFilePaths(providerSessionId)
          yield* fs.remove(headerFile).pipe(Effect.ignore)
          yield* fs.remove(settingsFile).pipe(Effect.ignore)
        })

      /** The persisted hook secret, if the thread's opaque metadata has one. */
      const metadataSecret = (metadata: string | undefined): string | null => {
        if (metadata === undefined) return null
        try {
          const parsed = JSON.parse(metadata) as { hookSecret?: unknown }
          return typeof parsed.hookSecret === "string" ? parsed.hookSecret : null
        } catch {
          return null
        }
      }

      /**
       * Register the session's webhook secret (reusing the persisted one so
       * it stays stable for the thread's life, minting on first use) and
       * write the per-session hook plumbing. The secret only ever lives in
       * 0600 files: the settings file and a curl header file (`-H @file`) —
       * never in any argv (ps would show it, including curl's) and never in
       * the URL (request paths land in tracer span names). Files are
       * overwritten per launch and removed by releaseSession.
       */
      const ensureHookPlumbing = (providerSessionId: string, metadata: string | undefined) =>
        Effect.gen(function* () {
          const known = metadataSecret(metadata)
          const secret = known ?? (yield* hooks.registerSecret(providerSessionId))
          if (known !== null) yield* hooks.adoptSecret(providerSessionId, known)
          const url = `http://127.0.0.1:${config.port}/internal/claude/hooks`
          const { headerFile, settingsFile } = hookFilePaths(providerSessionId)
          const command =
            `curl -fsS -m 5 -X POST -H 'Content-Type: application/json' ` +
            `-H @'${headerFile}' --data-binary @- '${url}'`
          const hookConfig = Object.fromEntries(
            HOOK_EVENTS.map((event) => [event, [{ hooks: [{ type: "command", command }] }]]),
          )
          const write = (file: string, content: string) =>
            fs.writeFileString(file, content).pipe(Effect.andThen(fs.chmod(file, 0o600)))
          yield* fs.makeDirectory(config.stateDir, { recursive: true }).pipe(
            Effect.andThen(write(headerFile, `${ClaudeHooks.SECRET_HEADER}: ${secret}`)),
            Effect.andThen(write(settingsFile, JSON.stringify({ hooks: hookConfig }))),
            Effect.mapError(
              (error) =>
                new AgentUnavailable({
                  provider: "claude",
                  reason: `could not write the hook settings files: ${error.message}`,
                }),
            ),
            // A failed (or interrupted) write sequence must not strand the
            // allocation it sits inside: drop any partial file, and revoke
            // the secret iff this call minted it — a metadata-carried secret
            // may still be validating a running TUI's hooks.
            Effect.onError(() =>
              Effect.gen(function* () {
                if (known === null) yield* hooks.revokeSecret(providerSessionId)
                yield* removeHookFiles(providerSessionId)
              }),
            ),
          )
          return { secret, settingsFile }
        })

      const openSession = (options: { readonly cwd: string; readonly resume?: string }) =>
        Effect.gen(function* () {
          const executable = yield* resolveProviderExecutable("claude", config.claudeExecutable)
          yield* versionCheck
          const input = makeInputQueue()
          const queue = yield* Queue.make<AgentEvent, AgentProtocolError | Cause.Done>()
          const initGate = yield* Deferred.make<void, GateError>()
          const session: LiveSession = {
            expectedSessionId: options.resume ?? null,
            cwd: options.cwd,
            queue,
            abort: new AbortController(),
            pushInput: (text) => input.push(userMessage(text)),
            closeInput: input.close,
            initGate,
            interrupt: () => Promise.resolve(),
            sessionId: null,
            activity: "unknown",
            activeTurn: null,
            interruptRequested: false,
            closed: false,
            over: false,
          }
          if (options.resume !== undefined) {
            if (sessions.has(options.resume)) {
              return yield* Effect.fail(
                conflict(`a writer is already connected to ${options.resume}`),
              )
            }
            sessions.set(options.resume, session)
          }
          // Registered before queryFn can fail: an uncleaned registry entry
          // would lock the session id out of resume for the process's life.
          yield* Effect.addFinalizer(() => Effect.sync(() => closeSession(session)))
          const handle = queryFn({
            prompt: input.stream(),
            options: {
              abortController: session.abort,
              cwd: options.cwd,
              env: claudeEnvironment(),
              pathToClaudeCodeExecutable: executable,
              ...(options.resume !== undefined ? { resume: options.resume } : {}),
              settingSources: [],
              permissionMode: "default",
              persistSession: true,
              canUseTool: makeCanUseTool(session),
              hooks: makeHooks(session),
            },
          })
          session.interrupt = () => handle.interrupt()
          yield* consume(session, handle).pipe(Effect.forkScoped)
          return session
        })

      const awaitVerified = (session: LiveSession) =>
        Deferred.await(session.initGate).pipe(
          Effect.timeoutOrElse({
            duration: initTimeout,
            orElse: () =>
              Effect.sync(() => {
                const error = protocolError(
                  `no identity evidence within ${Duration.format(Duration.fromInputUnsafe(initTimeout))}`,
                )
                // Keep the gate and the session in agreement.
                Deferred.doneUnsafe(session.initGate, Effect.fail(error))
                closeSession(session, error)
                return error
              }).pipe(Effect.flatMap(Effect.fail)),
          }),
        )

      const makeConnection = (session: LiveSession): AgentConnection => {
        // The seam's uniform rule: a session that ended underneath the
        // caller is AgentUnavailable ("resume it"); a handle the caller
        // already closed is a conflict.
        const requireOpen = Effect.suspend(
          (): Effect.Effect<void, AgentConflict | AgentUnavailable> =>
            session.over
              ? Effect.fail(
                  new AgentUnavailable({
                    provider: "claude",
                    reason: "the session ended; resume it for a fresh connection",
                  }),
                )
              : session.closed
                ? Effect.fail(conflict("the connection is closed"))
                : Effect.void,
        )
        return {
          get providerSessionId() {
            return session.sessionId ?? session.expectedSessionId ?? ""
          },
          cwd: session.cwd,
          activity: Effect.sync(() => session.activity),
          events: Stream.fromQueue(session.queue),
          startTurn: (text) =>
            Effect.gen(function* () {
              yield* requireOpen
              if (session.activeTurn !== null) {
                return yield* Effect.fail(conflict(`turn ${session.activeTurn} is still active`))
              }
              const turnId = `claude-turn-${nextTurn++}`
              session.activeTurn = turnId
              // Emitted before anything can await: the consume fiber may
              // deliver this turn's result on the very next microtask, and
              // turnStarted must precede turnCompleted on the feed.
              emit(session, { type: "turnStarted", turnId })
              session.pushInput(text)
              if (session.sessionId === null) {
                // First turn after a resume: this is where the deferred
                // identity verification lands (see the module header).
                yield* awaitVerified(session).pipe(
                  Effect.tapError(() =>
                    Effect.sync(() => {
                      session.activeTurn = null
                    }),
                  ),
                )
              }
              return { turnId }
            }),
          interrupt: (turn) =>
            Effect.gen(function* () {
              yield* requireOpen
              if (session.activeTurn !== turn.turnId) {
                return yield* Effect.fail(conflict(`turn ${turn.turnId} is not the active turn`))
              }
              session.interruptRequested = true
              yield* Effect.tryPromise({
                try: () => session.interrupt(),
                catch: (error) => protocolError(`interrupt failed: ${error}`),
              }).pipe(
                // A failed interrupt must not relabel the turn's real
                // outcome as "interrupted" later.
                Effect.tapError(() =>
                  Effect.sync(() => {
                    session.interruptRequested = false
                  }),
                ),
              )
            }),
        }
      }

      const adapter: AgentAdapter = {
        provider: "claude",
        capabilities: { testedVersion: CLAUDE_TESTED_VERSION, tuiObservation: "hooks" },
        createSession: (options) =>
          Effect.gen(function* () {
            const session = yield* openSession({ cwd: options.cwd })
            const turnId = `claude-turn-${nextTurn++}`
            session.activeTurn = turnId
            emit(session, { type: "turnStarted", turnId })
            session.pushInput(options.input)
            // A create has no expected id, so AgentResumeFailed cannot
            // happen here — same rule as the codex adapter.
            yield* awaitVerified(session).pipe(
              Effect.catchTag("AgentResumeFailed", (error) => Effect.die(error)),
            )
            return { connection: makeConnection(session), turn: { turnId } }
          }),
        resumeSession: (options) =>
          Effect.gen(function* () {
            const session = yield* openSession({
              cwd: options.cwd,
              resume: options.providerSessionId,
            })
            return makeConnection(session)
          }),
        tuiLaunch: (options) =>
          Effect.gen(function* () {
            const executable = yield* resolveProviderExecutable("claude", config.claudeExecutable)
            const { secret, settingsFile } = yield* ensureHookPlumbing(
              options.providerSessionId,
              options.providerMetadata,
            )
            return {
              launchSpec: {
                command: [
                  executable,
                  "--resume",
                  options.providerSessionId,
                  "--settings",
                  settingsFile,
                ],
                env: {},
              },
              // Always handed back: when the caller had no metadata this
              // launch minted the secret, and only persistence keeps the
              // running TUI's hooks valid across an ATC restart.
              providerMetadata: JSON.stringify({ hookSecret: secret }),
            }
          }),
        prepareTuiSession: (options) =>
          Effect.gen(function* () {
            const executable = yield* resolveProviderExecutable("claude", config.claudeExecutable)
            yield* versionCheck
            // Pre-assignment (probed 2026-08-05): `--session-id` creates
            // exactly the minted id, and a duplicate launch fails closed —
            // so identity resolves immediately and the first hook payload
            // is confirmation, not discovery. The secret is minted once
            // here and rides the thread's metadata from then on.
            const providerSessionId = crypto.randomUUID()
            // An abandoned prepare (the caller never awaited identity —
            // e.g. its terminal launch failed) must not leak the secret
            // registration or the 0600 files. acquireRelease pairs the
            // plumbing with its cleanup atomically, so interruption cannot
            // land between the allocation and its finalizer.
            let claimed = false
            const { secret, settingsFile } = yield* Effect.acquireRelease(
              ensureHookPlumbing(providerSessionId, undefined),
              () =>
                claimed
                  ? Effect.void
                  : Effect.gen(function* () {
                      yield* hooks.revokeSecret(providerSessionId)
                      yield* removeHookFiles(providerSessionId)
                    }),
            )
            return {
              launchSpec: {
                command: [
                  executable,
                  "--session-id",
                  providerSessionId,
                  "--settings",
                  settingsFile,
                ],
                env: {},
              },
              identity: Effect.sync(() => {
                claimed = true
                return {
                  providerSessionId,
                  cwd: options.cwd,
                  providerMetadata: JSON.stringify({ hookSecret: secret }),
                }
              }),
            }
          }),
        observeSession: (options) =>
          Effect.gen(function* () {
            // Restore the persisted secret first: the registry is
            // in-memory, so a TUI launched before an ATC restart keeps
            // validating only because the thread's metadata carries it.
            const known = metadataSecret(options.providerMetadata)
            if (known !== null) yield* hooks.adoptSecret(options.providerSessionId, known)
            const queue = yield* Queue.make<AgentActivity, Cause.Done>()
            // Registered before the subscription so it runs after it on
            // scope close: unsubscribe first, then end the queue.
            yield* Effect.addFinalizer(() => Effect.sync(() => Queue.endUnsafe(queue)))
            yield* hooks.subscribe((sessionId, activity) => {
              if (sessionId === options.providerSessionId) Queue.offerUnsafe(queue, activity)
            })
            return Stream.fromQueue(queue)
          }),
        // Claude offers no cheap truthful liveness query for a session it
        // is not driving; the domain's linked-terminal liveness carries
        // staleness re-derivation for TUI-driven Claude sessions.
        checkSession: () => Effect.succeed("unknown" as const),
        releaseSession: (options) =>
          Effect.gen(function* () {
            yield* hooks.revokeSecret(options.providerSessionId)
            yield* removeHookFiles(options.providerSessionId)
          }),
      }
      return adapter
    }),
  )

export const layer = layerWith({})
