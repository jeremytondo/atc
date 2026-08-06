import { Cause, Context, Deferred, Effect, Layer, Queue, Schema, Semaphore, Stream } from "effect"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConnection,
  AgentEvent,
  EstablishedIdentity,
} from "./agentAdapter.ts"
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
import * as BuildInfo from "../platform/buildInfo.ts"
import * as CodexServer from "./codexServer.ts"
import { AppConfig } from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"

// The Codex AgentAdapter (ATC-123): one WebSocket JSON-RPC client of the
// profile's supervised, detached app-server (codexServer.ts), multiplexing
// every thread over that single shared connection — which is what makes
// TUI-driven turns observable on our feed. Invariants beyond the seam's:
//
//   - One writer per thread is enforced HERE (the app-server would accept a
//     concurrent second writer, and concurrent writers corrupt provider
//     turn attribution — ATC-83 evidence).
//   - Server-initiated requests (approvals, questions) are surfaced on the
//     feed and answered with an immediate JSON-RPC rejection — never left
//     hanging. Real responses are ATC-124 work.
//   - A lost server connection fails every live session's feed loudly; the
//     next create/resume reconnects. Sessions never survive their socket.
//   - Wire shapes stay private to this module; raw events are logged at
//     debug level only.

/** The codex-cli version this adapter was validated against (record + warn). */
export const CODEX_TESTED_VERSION = "0.146.0"

const REQUEST_TIMEOUT = "30 seconds"

export class CodexAdapter extends Context.Service<CodexAdapter, AgentAdapter>()(
  "app-server/CodexAdapter",
) {}

const unavailable = (reason: string) => new AgentUnavailable({ provider: "codex", reason })
const protocolError = (reason: string) => new AgentProtocolError({ provider: "codex", reason })
const conflict = (reason: string) => new AgentConflict({ provider: "codex", reason })

/** A JSON-RPC error response — mapped per call site (resume vs the rest). */
class RpcError extends Schema.TaggedErrorClass<RpcError>()("RpcError", {
  code: Schema.NullOr(Schema.Number),
  text: Schema.String,
}) {}

const ThreadReply = Schema.Struct({
  thread: Schema.Struct({
    id: Schema.String,
    cwd: Schema.String,
    status: Schema.optional(Schema.Unknown),
  }),
})

const TurnReply = Schema.Struct({ turn: Schema.Struct({ id: Schema.String }) })

// The real thread/list reply is paginated: { data, nextCursor } (probe
// evidence in experiments/provider-identity-resume, pinned generated schema
// V2ThreadListResponse). nextCursor is absent or null on the last page.
const ThreadListReply = Schema.Struct({
  data: Schema.Array(Schema.Struct({ id: Schema.String, status: Schema.optional(Schema.Unknown) })),
  nextCursor: Schema.optional(Schema.NullOr(Schema.String)),
})

const statusToActivity = (status: unknown): AgentActivity => {
  if (typeof status !== "object" || status === null) return "unknown"
  const record = status as { type?: unknown; activeFlags?: unknown }
  if (record.type === "idle") return "idle"
  if (record.type !== "active") return "unknown"
  const flags = Array.isArray(record.activeFlags) ? record.activeFlags : []
  return flags.includes("waitingOnUserInput") || flags.includes("waitingOnApproval")
    ? "needs_input"
    : "working"
}

const SERVER_REQUEST_KINDS: Record<string, "approval" | "question"> = {
  "item/commandExecution/requestApproval": "approval",
  "item/tool/requestUserInput": "question",
}

interface LiveSession {
  readonly threadId: string
  readonly queue: Queue.Queue<AgentEvent, AgentProtocolError | Cause.Done>
  activity: AgentActivity
  /** Any live turn on the thread — ours or an external writer's (a TUI). */
  activeTurn: string | null
  /** The turn THIS client started, if any — the only one whose provider
   * requests we may answer. */
  ownTurn: string | null
  /** Set when the transport died underneath the session (vs. caller close). */
  failed: boolean
}

/** Reserves the active-turn slot between the local check and the RPC reply. */
const PENDING_TURN = "(pending)"

interface PendingReply {
  readonly succeed: (result: unknown) => void
  readonly fail: (error: RpcError | AgentProtocolError) => void
}

interface ClientState {
  readonly socket: WebSocket
  readonly url: string
  readonly pending: Map<number, PendingReply>
  nextId: number
  alive: boolean
}

export const layer = Layer.effect(CodexAdapter)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    const build = yield* BuildInfo.BuildInfo
    const codexServer = yield* CodexServer.CodexServer
    const subprocess = yield* Subprocess.Subprocess

    // All live writer sessions, keyed by thread id. They always belong to
    // `current`; a connection teardown fails and clears every one of them.
    const sessions = new Map<string, LiveSession>()
    // Passive per-thread activity observers (TUI-driven sessions). Unlike
    // sessions they survive reconnects: the map is adapter-level, and a new
    // connection's broadcasts route to the same queues.
    const observers = new Map<string, Set<Queue.Queue<AgentActivity, Cause.Done>>>()
    const connectLock = yield* Semaphore.make(1)
    const resumeLock = yield* Semaphore.make(1)
    // Serializes TUI launch → thread/started capture (prepareTuiSession).
    const launchLock = yield* Semaphore.make(1)
    interface PendingCapture {
      readonly cwd: string
      readonly gate: Deferred.Deferred<
        EstablishedIdentity,
        AgentUnavailable | AgentIdentityMismatch | AgentProtocolError
      >
    }
    let pendingCapture: PendingCapture | null = null
    let current: ClientState | null = null

    /**
     * A broadcast `thread/started` while a capture is armed: adopt the new
     * thread iff its cwd matches the launch we serialized. A different cwd
     * is another client's thread — leave the capture armed. (A manual
     * `codex --remote` in the same cwd inside this window is the accepted,
     * recorded residual risk.)
     */
    const captureStarted = (params: Record<string, unknown>): void => {
      if (pendingCapture === null) return
      const thread = params["thread"] as { id?: unknown; cwd?: unknown } | undefined
      if (typeof thread?.id !== "string" || typeof thread.cwd !== "string") return
      if (!samePath(thread.cwd, pendingCapture.cwd)) return
      const capture = pendingCapture
      pendingCapture = null
      Deferred.doneUnsafe(
        capture.gate,
        Effect.succeed({ providerSessionId: thread.id, cwd: capture.cwd }),
      )
    }

    const emit = (session: LiveSession, event: AgentEvent): void => {
      Queue.offerUnsafe(session.queue, event)
    }

    const teardown = (state: ClientState, reason: string): void => {
      if (!state.alive) return
      state.alive = false
      const error = protocolError(reason)
      for (const reply of state.pending.values()) reply.fail(error)
      state.pending.clear()
      // Sessions belong to the CURRENT connection only — tearing down a
      // stale socket (e.g. a failed handshake) must not touch them.
      if (current === state) {
        current = null
        for (const session of sessions.values()) {
          session.failed = true
          Queue.failCauseUnsafe(session.queue, Cause.fail(error))
        }
        sessions.clear()
        // An armed capture fails closed with the connection: the caller
        // abandons the launch and retries rather than adopting anything
        // observed through a fresh, unserialized window.
        if (pendingCapture !== null) {
          Deferred.doneUnsafe(pendingCapture.gate, Effect.fail(error))
          pendingCapture = null
        }
      }
      state.socket.close()
    }

    const handleServerRequest = (
      state: ClientState,
      message: {
        id: number | string
        method: string
        params: Record<string, unknown> | undefined
      },
    ): void => {
      const kind = SERVER_REQUEST_KINDS[message.method]
      const threadId = message.params?.["threadId"]
      const turnId = message.params?.["turnId"]
      const session = typeof threadId === "string" ? sessions.get(threadId) : undefined
      // Only answer requests for turns THIS client started: a shared-server
      // request belonging to another writer (a TUI's approval prompt) is
      // never ours to reject. Requests for our turns are surfaced, then
      // answered with an immediate rejection — never left hanging. Real
      // responses are ATC-124.
      if (session === undefined || session.ownTurn !== turnId) return
      if (kind !== undefined) {
        const requestId = String(message.id)
        emit(session, { type: "requestOpened", requestId, kind })
        emit(session, { type: "requestClosed", requestId })
      }
      state.socket.send(
        JSON.stringify({
          id: message.id,
          error: {
            code: -32601,
            message: "atc does not answer provider requests yet (native-mode work)",
          },
        }),
      )
    }

    const handleNotification = (message: {
      method: string
      params: Record<string, unknown> | undefined
    }): void => {
      const params = message.params ?? {}
      if (message.method === "thread/started") return captureStarted(params)
      const threadId = params["threadId"]
      if (typeof threadId !== "string") return
      // Coarse status fans out to every thread on the shared server without
      // subscription (probed) — observers get it even with no writer session.
      if (message.method === "thread/status/changed") {
        const activity = statusToActivity(params["status"])
        for (const queue of observers.get(threadId) ?? []) {
          Queue.offerUnsafe(queue, activity)
        }
      }
      const session = sessions.get(threadId)
      if (session === undefined) return
      switch (message.method) {
        case "thread/status/changed": {
          const activity = statusToActivity(params["status"])
          if (activity !== session.activity) {
            session.activity = activity
            emit(session, { type: "activity", activity })
          }
          return
        }
        case "turn/started": {
          const turnId = (params["turn"] as { id?: unknown } | undefined)?.id
          if (typeof turnId !== "string") return
          session.activeTurn = turnId
          emit(session, { type: "turnStarted", turnId })
          return
        }
        case "turn/completed": {
          const turn = params["turn"] as { id?: unknown; status?: unknown } | undefined
          const turnId = turn?.id
          if (typeof turnId !== "string") return
          if (session.activeTurn === turnId) session.activeTurn = null
          if (session.ownTurn === turnId) session.ownTurn = null
          const status = turn?.status
          const outcome =
            status === "completed"
              ? "completed"
              : status === "interrupted"
                ? "interrupted"
                : "failed"
          emit(
            session,
            outcome === "failed"
              ? { type: "turnCompleted", turnId, outcome, detail: String(status) }
              : { type: "turnCompleted", turnId, outcome },
          )
          return
        }
        default:
          // Item deltas, token usage, MCP startup noise: not status facts.
          return
      }
    }

    const dispatch = (state: ClientState, raw: string): void => {
      let message: {
        id?: number | string
        method?: string
        params?: Record<string, unknown>
        result?: unknown
        error?: { code?: number; message?: string } | null
      }
      try {
        message = JSON.parse(raw) as typeof message
      } catch {
        teardown(state, "codex app-server sent invalid JSON")
        return
      }
      if (message.id !== undefined && message.method === undefined) {
        // Ids we send are numeric; tolerate a string echo of one.
        const id = typeof message.id === "number" ? message.id : Number(message.id)
        const reply = Number.isInteger(id) ? state.pending.get(id) : undefined
        if (reply === undefined) return
        state.pending.delete(id)
        if (message.error !== undefined && message.error !== null) {
          reply.fail(
            new RpcError({
              code: message.error.code ?? null,
              text: message.error.message ?? "unknown error",
            }),
          )
          return
        }
        reply.succeed(message.result ?? {})
        return
      }
      // Requests and notifications only make sense from the connection the
      // sessions belong to; a stale socket's frames must not touch them.
      if (current !== state) return
      if (message.id !== undefined && message.method !== undefined) {
        handleServerRequest(state, {
          id: message.id,
          method: message.method,
          params: message.params,
        })
        return
      }
      if (message.method !== undefined)
        handleNotification({ method: message.method, params: message.params })
    }

    const request = (
      state: ClientState,
      method: string,
      params: Record<string, unknown>,
    ): Effect.Effect<unknown, RpcError | AgentProtocolError> =>
      Effect.callback<unknown, RpcError | AgentProtocolError>((resume) => {
        if (!state.alive) {
          resume(Effect.fail(protocolError("the app-server connection is closed")))
          return
        }
        const id = state.nextId++
        state.pending.set(id, {
          succeed: (result) => resume(Effect.succeed(result)),
          fail: (error) => resume(Effect.fail(error)),
        })
        state.socket.send(JSON.stringify({ id, method, params }))
        return Effect.sync(() => state.pending.delete(id))
      }).pipe(
        Effect.timeoutOrElse({
          duration: REQUEST_TIMEOUT,
          orElse: () => Effect.fail(protocolError(`${method} timed out`)),
        }),
      )

    /** Every non-resume call site: an RPC error is a protocol failure. */
    const rpcToProtocol = (error: RpcError | AgentProtocolError): AgentProtocolError =>
      error._tag === "RpcError" ? protocolError(error.text) : error

    const decodeReply = <S extends Schema.Top>(schema: S, reply: unknown) =>
      Schema.decodeUnknownEffect(schema)(reply).pipe(
        Effect.mapError((error) => protocolError(`unexpected reply shape: ${error.message}`)),
      )

    // Record + warn version drift, once, on first use (never blocks).
    const versionCheck = makeVersionGate(
      subprocess,
      "codex",
      config.codexExecutable,
      CODEX_TESTED_VERSION,
    )

    const openSocket = (url: string): Effect.Effect<ClientState, AgentUnavailable> =>
      Effect.callback<ClientState, AgentUnavailable>((resume) => {
        const socket = new WebSocket(url)
        const state: ClientState = { socket, url, pending: new Map(), nextId: 1, alive: true }
        socket.onopen = () => resume(Effect.succeed(state))
        socket.onerror = () => {
          teardown(state, "connection failed")
          resume(Effect.fail(unavailable(`could not connect to ${url}`)))
        }
        socket.onclose = () => teardown(state, "the codex app-server connection closed")
        socket.onmessage = (event) => dispatch(state, String(event.data))
        return Effect.sync(() => {
          if (current !== state) teardown(state, "connection attempt interrupted")
        })
      }).pipe(
        Effect.timeoutOrElse({
          duration: "5 seconds",
          orElse: () => Effect.fail(unavailable(`timed out connecting to ${url}`)),
        }),
      )

    /** The shared connection: reuse, or ensure the server and handshake. */
    const getClient = connectLock.withPermits(1)(
      Effect.gen(function* () {
        if (current !== null && current.alive) return current
        yield* versionCheck
        const info = yield* codexServer.ensure()
        const state = yield* openSocket(info.url)
        // A failed or interrupted handshake must close this socket: a
        // half-open connection would double-deliver broadcasts and, on its
        // eventual death, could not be told apart from the live one.
        yield* request(state, "initialize", {
          clientInfo: { name: "atc", title: "ATC App Server", version: build.version },
          capabilities: null,
        }).pipe(
          Effect.mapError(rpcToProtocol),
          Effect.onExit((exit) =>
            Effect.sync(() => {
              if (exit._tag === "Failure") teardown(state, "the handshake failed")
            }),
          ),
        )
        state.socket.send(JSON.stringify({ method: "initialized", params: {} }))
        current = state
        return state
      }),
    )

    // The passive seam methods promise only AgentUnavailable: a broken
    // handshake is "cannot consult the provider right now" to them.
    const getClientRetryable = getClient.pipe(
      Effect.catchTag("AgentProtocolError", (error) => Effect.fail(unavailable(error.reason))),
    )

    // Close the socket with the layer; the detached server itself lives on.
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        if (current !== null) teardown(current, "atc is shutting down")
      }),
    )

    const registerSession = (
      state: ClientState,
      threadId: string,
      cwd: string,
      initialStatus: unknown,
    ) =>
      Effect.gen(function* () {
        const queue = yield* Queue.make<AgentEvent, AgentProtocolError | Cause.Done>()
        const session: LiveSession = {
          threadId,
          queue,
          // Seeded from the provider's own thread status — never a guess.
          activity: statusToActivity(initialStatus),
          activeTurn: null,
          ownTurn: null,
          failed: false,
        }
        sessions.set(threadId, session)
        const unregister = (): void => {
          if (sessions.get(threadId) !== session) return
          sessions.delete(threadId)
          Queue.endUnsafe(queue)
          if (state.alive) {
            state.socket.send(
              JSON.stringify({
                id: state.nextId++,
                method: "thread/unsubscribe",
                params: { threadId },
              }),
            )
          }
        }
        yield* Effect.addFinalizer(() => Effect.sync(unregister))

        // A dead transport is the retryable AgentUnavailable; only a
        // caller-closed connection is a conflict.
        const requireLive = Effect.suspend(
          (): Effect.Effect<void, AgentConflict | AgentUnavailable> =>
            session.failed || !state.alive
              ? Effect.fail(
                  new AgentUnavailable({
                    provider: "codex",
                    reason: "the app-server connection was lost; resume the session",
                  }),
                )
              : sessions.get(threadId) === session
                ? Effect.void
                : Effect.fail(conflict("the connection is closed")),
        )

        const connection: AgentConnection = {
          providerSessionId: threadId,
          cwd,
          activity: Effect.sync(() => session.activity),
          events: Stream.fromQueue(queue),
          startTurn: (input) =>
            Effect.gen(function* () {
              yield* requireLive
              if (session.activeTurn !== null) {
                return yield* Effect.fail(conflict(`turn ${session.activeTurn} is still active`))
              }
              // Reserve the slot before suspending on the RPC, or two
              // concurrent startTurn calls would both pass the check.
              session.activeTurn = PENDING_TURN
              session.ownTurn = PENDING_TURN
              const decoded = yield* request(state, "turn/start", {
                threadId,
                input: [{ type: "text", text: input }],
              }).pipe(
                Effect.mapError(rpcToProtocol),
                Effect.flatMap((reply) => decodeReply(TurnReply, reply)),
                Effect.onExit((exit) =>
                  Effect.sync(() => {
                    if (exit._tag !== "Failure") return
                    if (session.activeTurn === PENDING_TURN) session.activeTurn = null
                    if (session.ownTurn === PENDING_TURN) session.ownTurn = null
                  }),
                ),
              )
              // turn/started may have landed first with the real id.
              if (session.activeTurn === PENDING_TURN) session.activeTurn = decoded.turn.id
              session.ownTurn = decoded.turn.id
              return { turnId: decoded.turn.id }
            }),
          interrupt: (turn) =>
            Effect.gen(function* () {
              yield* requireLive
              if (session.activeTurn !== turn.turnId) {
                return yield* Effect.fail(conflict(`turn ${turn.turnId} is not the active turn`))
              }
              // A provider-side rejection means the target went stale in
              // flight — the same conflict as losing the local race.
              yield* request(state, "turn/interrupt", { threadId, turnId: turn.turnId }).pipe(
                Effect.mapError((error) =>
                  error._tag === "RpcError" ? conflict(error.text) : error,
                ),
              )
            }),
        }
        return { connection, unregister }
      })

    const adapter: AgentAdapter = {
      provider: "codex",
      capabilities: { testedVersion: CODEX_TESTED_VERSION, tuiObservation: "shared-server" },
      createSession: (options) =>
        Effect.gen(function* () {
          // thread/start broadcasts thread/started to every socket — ours
          // included — so it must never run while a TUI capture is armed:
          // an ATC-driven create in the same cwd would be mis-adopted as
          // the TUI's identity. The launch lock serializes both paths.
          const { connection, unregister } = yield* launchLock.withPermits(1)(
            Effect.gen(function* () {
              const state = yield* getClient
              const reply = yield* request(state, "thread/start", {
                cwd: options.cwd,
                approvalPolicy: "never",
                sandbox: "workspace-write",
              }).pipe(Effect.mapError(rpcToProtocol))
              const decoded = yield* decodeReply(ThreadReply, reply)
              if (!samePath(decoded.thread.cwd, options.cwd)) {
                return yield* Effect.fail(
                  new AgentIdentityMismatch({
                    provider: "codex",
                    field: "cwd",
                    expected: options.cwd,
                    actual: decoded.thread.cwd,
                  }),
                )
              }
              return yield* registerSession(
                state,
                decoded.thread.id,
                options.cwd,
                decoded.thread.status,
              )
            }),
          )
          // The seam widens startTurn's errors for deferred-verification
          // providers; codex verified above, so those tags are impossible.
          // A failed first turn releases the writer slot — the caller got
          // no connection handle, so nothing must stay claimed.
          const turn = yield* connection.startTurn(options.input).pipe(
            Effect.catchTag("AgentResumeFailed", (error) => Effect.die(error)),
            Effect.tapError(() => Effect.sync(unregister)),
          )
          return { connection, turn }
        }),
      resumeSession: (options) =>
        // Serialized so two concurrent resumes of one thread cannot both
        // pass the single-writer check while the RPC is in flight.
        resumeLock.withPermits(1)(
          Effect.gen(function* () {
            const state = yield* getClient
            if (sessions.has(options.providerSessionId)) {
              return yield* Effect.fail(
                conflict(`a writer is already connected to ${options.providerSessionId}`),
              )
            }
            const reply = yield* request(state, "thread/resume", {
              threadId: options.providerSessionId,
              cwd: options.cwd,
            }).pipe(
              Effect.mapError((error) =>
                error._tag === "RpcError" && error.code === -32600
                  ? new AgentResumeFailed({
                      provider: "codex",
                      providerSessionId: options.providerSessionId,
                      reason: error.text,
                    })
                  : rpcToProtocol(error),
              ),
            )
            const decoded = yield* decodeReply(ThreadReply, reply)
            if (decoded.thread.id !== options.providerSessionId) {
              return yield* Effect.fail(
                new AgentIdentityMismatch({
                  provider: "codex",
                  field: "sessionId",
                  expected: options.providerSessionId,
                  actual: decoded.thread.id,
                }),
              )
            }
            if (!samePath(decoded.thread.cwd, options.cwd)) {
              return yield* Effect.fail(
                new AgentIdentityMismatch({
                  provider: "codex",
                  field: "cwd",
                  expected: options.cwd,
                  actual: decoded.thread.cwd,
                }),
              )
            }
            const { connection } = yield* registerSession(
              state,
              decoded.thread.id,
              options.cwd,
              decoded.thread.status,
            )
            return connection
          }),
        ),
      tuiLaunch: (options) =>
        Effect.gen(function* () {
          const executable = yield* resolveProviderExecutable("codex", config.codexExecutable)
          const info = yield* codexServer.ensure()
          return {
            launchSpec: {
              command: [executable, "resume", "--remote", info.url, options.providerSessionId],
              env: {},
            },
          }
        }),
      prepareTuiSession: (options) =>
        Effect.gen(function* () {
          const executable = yield* resolveProviderExecutable("codex", config.codexExecutable)
          // The held observer connection is the capture channel: it must
          // exist before the TUI can be launched.
          yield* getClientRetryable
          const info = yield* codexServer.ensure()
          // Codex has no pre-assignment flag, so identity is captured from
          // the fresh TUI's thread/started broadcast. The launch lock is
          // held for the caller's whole scope: launch → capture is
          // serialized (against other prepares AND createSession's own
          // thread/start), making the correlation deterministic. Interruptible:
          // a queued caller must stay cancellable while a slow capture holds
          // the permit.
          yield* Effect.acquireRelease(launchLock.take(1), () => launchLock.release(1), {
            interruptible: true,
          })
          const gate = yield* Deferred.make<
            EstablishedIdentity,
            AgentUnavailable | AgentIdentityMismatch | AgentProtocolError
          >()
          pendingCapture = { cwd: options.cwd, gate }
          yield* Effect.addFinalizer(() =>
            Effect.sync(() => {
              if (pendingCapture?.gate !== gate) return
              pendingCapture = null
              // Anyone still awaiting identity outside the scope must not
              // hang forever on an abandoned launch.
              Deferred.doneUnsafe(gate, Effect.fail(unavailable("the launch was abandoned")))
            }),
          )
          return {
            launchSpec: { command: [executable, "--remote", info.url], env: {} },
            identity: Deferred.await(gate),
          }
        }),
      observeSession: (options) =>
        Effect.gen(function* () {
          // Ensure the shared connection exists — it is the evidence source.
          yield* getClientRetryable
          const queue = yield* Queue.make<AgentActivity, Cause.Done>()
          const set = observers.get(options.providerSessionId) ?? new Set()
          set.add(queue)
          observers.set(options.providerSessionId, set)
          yield* Effect.addFinalizer(() =>
            Effect.sync(() => {
              set.delete(queue)
              if (set.size === 0) observers.delete(options.providerSessionId)
              Queue.endUnsafe(queue)
            }),
          )
          return Stream.fromQueue(queue)
        }),
      checkSession: (options) =>
        Effect.gen(function* () {
          const state = yield* getClientRetryable
          // thread/list (probed) is the reconciliation aid: absent thread or
          // undeterminable status is honestly `unknown`, never a guess. The
          // reply is paginated; walk pages until the target thread appears
          // or the cursor runs out.
          let cursor: string | undefined
          while (true) {
            const reply = yield* request(
              state,
              "thread/list",
              cursor === undefined ? {} : { cursor },
            ).pipe(
              Effect.mapError((error) =>
                unavailable(error._tag === "RpcError" ? error.text : error.reason),
              ),
            )
            const decoded = yield* Schema.decodeUnknownEffect(ThreadListReply)(reply).pipe(
              Effect.mapError((error) =>
                unavailable(`unexpected thread/list reply: ${error.message}`),
              ),
            )
            const thread = decoded.data.find((t) => t.id === options.providerSessionId)
            if (thread !== undefined) return statusToActivity(thread.status)
            const next = decoded.nextCursor
            // A repeated cursor cannot make progress; treat it as the end.
            if (typeof next !== "string" || next === cursor) return "unknown"
            cursor = next
          }
        }),
      // Codex holds no per-session launch files or secrets; provider-owned
      // rollouts are never ATC's to touch.
      releaseSession: () => Effect.void,
    }
    return adapter
  }),
)
