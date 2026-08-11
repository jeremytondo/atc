import {
  Cause,
  Context,
  Deferred,
  Duration,
  Effect,
  FileSystem,
  Layer,
  Queue,
  Schema,
  Semaphore,
  Stream,
} from "effect"
import * as path from "node:path"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConnection,
  AgentEvent,
  AgentSessionEvent,
  EstablishedIdentity,
} from "./agentAdapter.ts"
import {
  AgentConflict,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentResumeFailed,
  AgentUnavailable,
  aggregateActivity,
  makeVersionGate,
  NESTED_SESSION_ENV_VARIABLES,
  resolveProviderExecutable,
  samePath,
  titleInstruction,
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
//   - Activity is the aggregate of a thread's session tree (ATC-158).
//     Descendant (subagent) threads broadcast thread/status/changed but NOT
//     thread/started, and thread/list omits them (probed 2026-08-10,
//     experiments/subagent-activity): the child→root mapping comes from
//     subAgentActivity items on the parent's feed, live statuses fold into
//     the root's writer feed and observer queues as reducer aggregates, and
//     checkSession reconciles demand-driven via thread/loaded/list +
//     per-id thread/read. Descendant bookkeeping is in-memory and pruned
//     on teardown, root unobserve/unregister, and release.

/** The codex-cli version this adapter was validated against (record + warn). */
export const CODEX_TESTED_VERSION = "0.146.0"

const REQUEST_TIMEOUT = "30 seconds"

/** Bound on one `codex exec` title one-shot (ATC-155). */
const TITLE_TIMEOUT = "90 seconds"

/** The title one-shot's pinned model (ATC-155): a title needs the cheap
 * high-volume tier (~25× cheaper than the default coding models), never
 * the user's configured coding model. Verified live 2026-08-10. */
const TITLE_MODEL = "gpt-5.6-luna"

// One arming of the first-prompt discovery loop (ATC-155): first read
// immediately at the transition, then capped backoff while the rollout
// flush catches up (~1.4s behind turn start, probed live 2026-08-10).
// Worst case ~28s of polling per arming, then re-arm on a later transition.
const PROMPT_READ_ATTEMPTS = 8
const PROMPT_READ_BACKOFF_MS = 500
const PROMPT_READ_BACKOFF_CAP_MS = 5_000

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

// thread/loaded/list: ids of the sessions currently loaded in memory —
// running descendants included (probed, experiments/subagent-activity).
const LoadedListReply = Schema.Struct({
  data: Schema.Array(Schema.String),
  nextCursor: Schema.optional(Schema.NullOr(Schema.String)),
})

const ThreadReadReply = Schema.Struct({
  thread: Schema.Struct({
    id: Schema.String,
    parentThreadId: Schema.optional(Schema.NullOr(Schema.String)),
    status: Schema.optional(Schema.Unknown),
  }),
})

// thread/read: the reply's required `preview` field is "usually the first
// user message in the thread, if available" (generated schema, codex
// 0.147) — the first-prompt evidence a passive socket can reach (ATC-155).
// `thread/read {includeTurns: true}` is NOT usable for this: TUI-created
// threads run in paginated history mode and the server rejects
// includeTurns for them outright (probed live 2026-08-10).
const ThreadPreviewReply = Schema.Struct({
  thread: Schema.Struct({ preview: Schema.optional(Schema.NullOr(Schema.String)) }),
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
    const fs = yield* FileSystem.FileSystem

    // All live writer sessions, keyed by thread id. They always belong to
    // `current`; a connection teardown fails and clears every one of them.
    const sessions = new Map<string, LiveSession>()
    // Passive per-thread activity observers (TUI-driven sessions). Unlike
    // sessions they survive reconnects: the map is adapter-level, and a new
    // connection's broadcasts route to the same queues.
    const observers = new Map<string, Set<Queue.Queue<AgentActivity, Cause.Done>>>()

    // Descendant bookkeeping (ATC-158): each thread's own last-broadcast
    // status, the child→parent links learned from subAgentActivity items
    // (and checkSession reconciliation), and per-root descendant statuses.
    // Live evidence only — cleared on connection teardown and rebuilt from
    // fresh broadcasts or the demand-driven checkSession walk.
    const ownStatus = new Map<string, AgentActivity>()
    const parentOf = new Map<string, string>()
    const descendants = new Map<string, Map<string, AgentActivity>>()

    /** Follow parent links to the tree's root (bounded against cycles). */
    const resolveRoot = (threadId: string): string => {
      let current = threadId
      for (let hops = 0; hops < 32; hops++) {
        const parent = parentOf.get(current)
        if (parent === undefined) return current
        current = parent
      }
      return current
    }

    const registerDescendant = (parentId: string, childId: string): void => {
      if (parentId === childId) return
      parentOf.set(childId, parentId)
      const rootId = resolveRoot(childId)
      const set = descendants.get(rootId) ?? new Map<string, AgentActivity>()
      if (!set.has(childId)) set.set(childId, ownStatus.get(childId) ?? "working")
      descendants.set(rootId, set)
      // A thread that previously looked like a root (its parent link
      // arrived late) hands its tracked descendants to the real root.
      if (childId !== rootId) {
        const orphaned = descendants.get(childId)
        if (orphaned !== undefined) {
          descendants.delete(childId)
          for (const [id, activity] of orphaned) if (!set.has(id)) set.set(id, activity)
        }
      }
    }

    const aggregateFor = (rootId: string): AgentActivity =>
      aggregateActivity(ownStatus.get(rootId) ?? "unknown", descendants.get(rootId)?.values() ?? [])

    /** Publish the root's current aggregate to its observers and (deduped)
     * writer-session feed. */
    const emitAggregate = (rootId: string): void => {
      const aggregate = aggregateFor(rootId)
      for (const queue of observers.get(rootId) ?? []) {
        Queue.offerUnsafe(queue, aggregate)
      }
      const session = sessions.get(rootId)
      if (session !== undefined && aggregate !== session.activity) {
        session.activity = aggregate
        emit(session, { type: "activity", activity: aggregate })
      }
    }

    /** Drop a root's descendant bookkeeping once nothing consumes it. */
    const pruneRoot = (rootId: string): void => {
      if (sessions.has(rootId) || observers.has(rootId)) return
      const set = descendants.get(rootId)
      if (set !== undefined) {
        descendants.delete(rootId)
        for (const childId of set.keys()) {
          parentOf.delete(childId)
          ownStatus.delete(childId)
        }
      }
      ownStatus.delete(rootId)
    }
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
      const thread = params["thread"] as
        { id?: unknown; cwd?: unknown; parentThreadId?: unknown } | undefined
      if (typeof thread?.id !== "string" || typeof thread.cwd !== "string") return
      // A subagent thread spawning in the armed capture's cwd must never
      // be adopted as a fresh TUI's identity (ATC-158 robustness; probes
      // show descendants currently skip thread/started, this is defense).
      if (typeof thread.parentThreadId === "string") return
      if (!samePath(thread.cwd, pendingCapture.cwd)) return
      const capture = pendingCapture
      pendingCapture = null
      Deferred.doneUnsafe(
        capture.gate,
        Effect.succeed({ providerSessionId: thread.id, cwd: capture.cwd }),
      )
    }

    const emit = (session: LiveSession, event: AgentEvent): void => {
      // Bounded queue: a consumer that stopped draining loses the stream,
      // not the process — the ended queue tells it to reopen and resync
      // (the same policy the SSE fan-out applies in events.ts).
      if (!Queue.offerUnsafe(session.queue, event)) Queue.endUnsafe(session.queue)
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
        // Descendant state is live evidence of THIS connection; a fresh
        // connection's broadcasts or checkSession's walk rebuild it.
        ownStatus.clear()
        parentOf.clear()
        descendants.clear()
        // Observers outlive the socket, but their evidence just died with
        // it: tell them honestly rather than letting the last busy state
        // sit stale while the provider moves on unheard (ATC-158).
        for (const set of observers.values()) {
          for (const queue of set) Queue.offerUnsafe(queue, "unknown")
        }
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
      // The child→root mapping arrives as a subAgentActivity item on the
      // PARENT's feed — descendants do not broadcast thread/started
      // (probed, experiments/subagent-activity).
      if (message.method === "item/started" || message.method === "item/completed") {
        const item = params["item"] as { type?: unknown; agentThreadId?: unknown } | undefined
        if (item?.type === "subAgentActivity" && typeof item.agentThreadId === "string") {
          registerDescendant(threadId, item.agentThreadId)
          emitAggregate(resolveRoot(threadId))
        }
        return
      }
      // Coarse status fans out for EVERY thread on the shared server
      // without subscription (probed) — descendants included. A
      // descendant's change folds into its root's aggregate; a root's
      // change reaches its observers and (deduped) writer feed.
      if (message.method === "thread/status/changed") {
        const activity = statusToActivity(params["status"])
        ownStatus.set(threadId, activity)
        const rootId = resolveRoot(threadId)
        if (rootId !== threadId) descendants.get(rootId)?.set(threadId, activity)
        emitAggregate(rootId)
        return
      }
      const session = sessions.get(threadId)
      if (session === undefined) return
      switch (message.method) {
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
    const versionCheck = yield* makeVersionGate(
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

    const connect = Effect.gen(function* () {
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
    })

    // The connection attempt's outcome is memoized for a short window so a
    // dead Codex costs ONE probe (with its full connect timeouts) per window
    // instead of one per caller — a thread-list fan-out otherwise serializes
    // every record behind those timeouts (ATC-167 H4). The memo also records
    // interruption (cachedWithTTL caches whatever exit the driver produced),
    // so an interrupted driver invalidates on the way out rather than making
    // later callers replay its interrupt.
    const [cachedConnect, invalidateConnect] = yield* Effect.cachedInvalidateWithTTL(
      connect,
      "5 seconds",
    )

    /** The shared connection: reuse, or ensure the server and handshake. */
    const getClient = connectLock.withPermits(1)(
      Effect.gen(function* () {
        if (current !== null && current.alive) return current
        const state = yield* Effect.onInterrupt(cachedConnect, () => invalidateConnect)
        if (state.alive) return state
        // A memoized success can predate a teardown; never hand out a dead
        // connection — drop the memo and probe fresh.
        yield* invalidateConnect
        return yield* Effect.onInterrupt(cachedConnect, () => invalidateConnect)
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
        // Bounded (overflow ends the stream — see emit); a session that
        // buffers 256 undrained events has lost its consumer.
        const queue = yield* Queue.make<AgentEvent, AgentProtocolError | Cause.Done>({
          capacity: 256,
        })
        // Seeded from the provider's own thread status — never a guess —
        // folded with any descendants already tracked for this root.
        ownStatus.set(threadId, statusToActivity(initialStatus))
        const session: LiveSession = {
          threadId,
          queue,
          activity: "unknown",
          activeTurn: null,
          ownTurn: null,
          failed: false,
        }
        session.activity = aggregateFor(threadId)
        sessions.set(threadId, session)
        const unregister = (): void => {
          if (sessions.get(threadId) !== session) return
          sessions.delete(threadId)
          pruneRoot(threadId)
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

    /**
     * Walk one paginated thread/list population (thread/list returns only
     * non-archived threads unless `archived: true` — the populations are
     * disjoint): the found entry, `"absent"` when a complete walk
     * (nextCursor exhausted) omits the id, or `"unknown"` when the walk
     * could not be completed — a repeated cursor or the page cap, either a
     * provider that cannot make pagination progress or a pathologically
     * long list.
     */
    const walkThreadList = (state: ClientState, threadId: string, archived: boolean) =>
      Effect.gen(function* () {
        let cursor: string | undefined
        for (let page = 0; page < 100; page++) {
          const reply = yield* request(
            state,
            "thread/list",
            cursor === undefined ? { archived } : { archived, cursor },
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
          const thread = decoded.data.find((t) => t.id === threadId)
          if (thread !== undefined) return thread
          const next = decoded.nextCursor
          if (typeof next !== "string") return "absent" as const
          if (next === cursor) return "unknown" as const
          cursor = next
        }
        return "unknown" as const
      })

    /**
     * Find one thread across BOTH thread/list populations — a session
     * archived through another Codex surface still exists and must not be
     * reported as deleted. `"absent"` only when complete walks of both the
     * non-archived and archived lists omit the id; `"unknown"` when either
     * walk was inconclusive. Callers must not treat `"unknown"` as absence.
     */
    const findThread = (state: ClientState, threadId: string) =>
      Effect.gen(function* () {
        const live = yield* walkThreadList(state, threadId, false)
        if (typeof live !== "string") return live
        const archived = yield* walkThreadList(state, threadId, true)
        if (typeof archived !== "string") return archived
        return live === "absent" && archived === "absent"
          ? ("absent" as const)
          : ("unknown" as const)
      })

    /**
     * Rebuild the root's descendant graph from the provider (ATC-158):
     * walk thread/loaded/list, thread/read every loaded id, and chain
     * parent links back to the root, REPLACING the root's tracked
     * descendant set with the findings; `"unknown"` when the enumeration
     * could not be completed. Replacement is the point: a stale pre-walk
     * `working` entry whose thread is no longer loaded must not survive
     * reconciliation (it would pin the aggregate busy forever). parentOf
     * links are kept, so a child that spawned mid-walk re-enters on its
     * next status broadcast — racy transients self-heal; only staleness
     * is permanent, and replacement cures it.
     */
    const reconcileDescendants = (
      state: ClientState,
      rootId: string,
    ): Effect.Effect<"reconciled" | "unknown", AgentUnavailable> =>
      Effect.gen(function* () {
        const loaded: Array<string> = []
        let cursor: string | undefined
        for (let page = 0; ; page++) {
          if (page >= 100) return "unknown" as const
          const reply = yield* request(
            state,
            "thread/loaded/list",
            cursor === undefined ? {} : { cursor },
          ).pipe(
            Effect.mapError((error) =>
              unavailable(error._tag === "RpcError" ? error.text : error.reason),
            ),
          )
          const decoded = yield* Schema.decodeUnknownEffect(LoadedListReply)(reply).pipe(
            Effect.mapError((error) =>
              unavailable(`unexpected thread/loaded/list reply: ${error.message}`),
            ),
          )
          loaded.push(...decoded.data)
          const next = decoded.nextCursor
          if (typeof next !== "string") break
          if (next === cursor) return "unknown" as const
          cursor = next
        }
        // Cost bound: the loaded set is normally a handful of sessions on
        // the local in-memory server; a pathological population makes the
        // walk inconclusive rather than open-ended.
        if (loaded.length > 200) return "unknown" as const
        const info = new Map<string, { parent: string | null; activity: AgentActivity }>()
        for (const id of loaded) {
          if (id === rootId) continue
          const reply = yield* request(state, "thread/read", { threadId: id }).pipe(
            Effect.mapError((error) =>
              unavailable(error._tag === "RpcError" ? error.text : error.reason),
            ),
          )
          const decoded = yield* Schema.decodeUnknownEffect(ThreadReadReply)(reply).pipe(
            Effect.mapError((error) =>
              unavailable(`unexpected thread/read reply: ${error.message}`),
            ),
          )
          info.set(id, {
            parent:
              typeof decoded.thread.parentThreadId === "string"
                ? decoded.thread.parentThreadId
                : null,
            activity: statusToActivity(decoded.thread.status),
          })
        }
        // Drop tracked descendants the enumeration no longer contains:
        // unloaded means finished, and a stale busy entry must go.
        const tracked = descendants.get(rootId)
        if (tracked !== undefined) {
          const loadedSet = new Set(loaded)
          for (const id of [...tracked.keys()]) {
            if (!loadedSet.has(id)) tracked.delete(id)
          }
        }
        for (const [id, entry] of info) {
          let ancestor = entry.parent
          for (let hops = 0; ancestor !== null && hops < 32; hops++) {
            if (ancestor === rootId) {
              if (entry.parent !== null) registerDescendant(entry.parent, id)
              ownStatus.set(id, entry.activity)
              descendants.get(resolveRoot(id))?.set(id, entry.activity)
              break
            }
            ancestor = info.get(ancestor)?.parent ?? null
          }
        }
        return "reconciled" as const
      })

    const adapter: AgentAdapter = {
      provider: "codex",
      observationOutlivesTui: true,
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
          // Probe before launching (ATC-142): `codex resume` of a deleted or
          // pruned thread dies inside the pty with no typed error, and every
          // reopen repeats it. Only a COMPLETE walk that omits the id fails
          // closed; an incomplete walk ("unknown") launches blind like
          // before — an inconclusive probe must never fabricate absence.
          const state = yield* getClientRetryable
          const thread = yield* findThread(state, options.providerSessionId)
          if (thread === "absent") {
            return yield* Effect.fail(
              new AgentResumeFailed({
                provider: "codex",
                providerSessionId: options.providerSessionId,
                reason: "codex no longer has this session (its history was deleted or pruned)",
              }),
            )
          }
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
            // --cd is load-bearing: in --remote mode the TUI does not forward
            // its own working directory, so without it the app-server stamps
            // the new thread with ITS process cwd and the capture's cwd match
            // below can never adopt the thread (observed on codex 0.146.0).
            launchSpec: {
              command: [executable, "--cd", options.cwd, "--remote", info.url],
              env: {},
            },
            identity: Deferred.await(gate),
          }
        }),
      observeSession: (options) =>
        Effect.gen(function* () {
          // Ensure the shared connection exists — it is the evidence source.
          yield* getClientRetryable
          // Sliding: only the newest activity matters, so a slow consumer
          // drops stale states instead of buffering without bound.
          const queue = yield* Queue.make<AgentActivity, Cause.Done>({
            capacity: 16,
            strategy: "sliding",
          })
          const set = observers.get(options.providerSessionId) ?? new Set()
          set.add(queue)
          observers.set(options.providerSessionId, set)
          yield* Effect.addFinalizer(() =>
            Effect.sync(() => {
              set.delete(queue)
              if (set.size === 0) {
                observers.delete(options.providerSessionId)
                pruneRoot(options.providerSessionId)
              }
              Queue.endUnsafe(queue)
            }),
          )
          // User-prompt evidence is demand-driven (ATC-155 probe: item/*
          // notifications never fan out to this passive socket): the first
          // provider-evidenced transition arms ONE single-flight discovery
          // fiber, forked so the activity feed — and the confirmed-marker
          // write it drives — never waits on an RPC. The fiber polls
          // thread/read for the session's `preview` (its first user
          // message) on a capped backoff: the rollout flush LAGS the
          // turn's start (probed ~1.4s on the live server) while the turn
          // emits no further transitions until it ends, so waiting for the
          // next transition would name long turns only at completion.
          // Exhausting the backoff re-arms on a later transition (turn end
          // stays the worst-case fallback); a hard read failure spends the
          // attempt for good (logged at debug). The feed never lies — it
          // just goes without prompt evidence.
          const scope = yield* Effect.scope
          // Sliding: prompts arrive one per turn start; a slow consumer
          // loses old auto-name evidence, never the process.
          const promptQueue = yield* Queue.make<AgentSessionEvent, Cause.Done>({
            capacity: 16,
            strategy: "sliding",
          })
          yield* Effect.addFinalizer(() => Effect.sync(() => Queue.endUnsafe(promptQueue)))
          let promptPhase: "pending" | "reading" | "done" = "pending"
          const readPreview = Effect.gen(function* () {
            const state = yield* getClientRetryable
            const reply = yield* request(state, "thread/read", {
              threadId: options.providerSessionId,
            }).pipe(Effect.mapError(rpcToProtocol))
            const decoded = yield* decodeReply(ThreadPreviewReply, reply)
            const preview = (decoded.thread.preview ?? "").trim()
            return preview === "" ? null : preview
          })
          const discoverPrompt = Effect.gen(function* () {
            let delay = PROMPT_READ_BACKOFF_MS
            for (let attempt = 0; attempt < PROMPT_READ_ATTEMPTS; attempt++) {
              if (attempt > 0) {
                yield* Effect.sleep(Duration.millis(delay))
                delay = Math.min(delay * 2, PROMPT_READ_BACKOFF_CAP_MS)
              }
              const text = yield* readPreview
              if (text !== null) {
                promptPhase = "done"
                Queue.offerUnsafe(promptQueue, { type: "userPrompt", text })
                return
              }
            }
            promptPhase = "pending"
          }).pipe(
            Effect.catch((error) =>
              Effect.gen(function* () {
                promptPhase = "done"
                yield* Effect.logDebug("codex first-prompt read failed").pipe(
                  Effect.annotateLogs({
                    providerSessionId: options.providerSessionId,
                    reason: error.message,
                  }),
                )
              }),
            ),
          )
          const activityEvents = Stream.fromQueue(queue).pipe(
            Stream.mapEffect((activity): Effect.Effect<AgentSessionEvent> =>
              Effect.gen(function* () {
                // Any provider-evidenced transition can carry the news that
                // history exists; `unknown` is the teardown signal, where a
                // read could only fail and burn the attempt.
                if (promptPhase === "pending" && activity !== "unknown") {
                  promptPhase = "reading"
                  yield* discoverPrompt.pipe(Effect.forkIn(scope))
                }
                return { type: "activity", activity }
              }),
            ),
          )
          return Stream.merge(activityEvents, Stream.fromQueue(promptQueue))
        }),
      generateTitle: (options) =>
        Effect.scoped(
          Effect.gen(function* () {
            const executable = yield* resolveProviderExecutable("codex", config.codexExecutable)
            yield* versionCheck
            // The cheapest ephemeral invocation (T3Code precedent): no
            // session files, read-only sandbox, low reasoning. The prompt
            // rides stdin (never argv — ps would show it) and the reply
            // rides --output-last-message (stdout carries progress noise).
            const outputFile = path.join(config.stateDir, `codex-title-${crypto.randomUUID()}.txt`)
            yield* fs
              .makeDirectory(config.stateDir, { recursive: true })
              .pipe(
                Effect.mapError((error) =>
                  unavailable(`could not create stateDir: ${error.message}`),
                ),
              )
            yield* Effect.addFinalizer(() => fs.remove(outputFile).pipe(Effect.ignore))
            return yield* Effect.gen(function* () {
              const child = yield* subprocess
                .spawn({
                  executable,
                  args: [
                    "exec",
                    "--ephemeral",
                    "--skip-git-repo-check",
                    "-s",
                    "read-only",
                    "--model",
                    TITLE_MODEL,
                    "--config",
                    'model_reasoning_effort="low"',
                    "--color",
                    "never",
                    "--output-last-message",
                    outputFile,
                    "-",
                  ],
                  // The user's full environment (auth state) minus the
                  // shared nested-session markers (agentAdapter.ts scrub
                  // rule), composed inside the Subprocess seam.
                  env: {},
                  extendEnv: true,
                  unsetEnv: NESTED_SESSION_ENV_VARIABLES,
                  cwd: options.cwd,
                })
                .pipe(
                  Effect.mapError((error) =>
                    unavailable(`could not launch codex exec: ${error.message}`),
                  ),
                )
              // Keep the pipe drained so a chatty exec can never block on it.
              yield* Stream.runDrain(child.stdoutLines).pipe(Effect.ignore, Effect.forkScoped)
              yield* child.writeLine(titleInstruction(options.prompt)).pipe(
                Effect.andThen(child.endInput),
                Effect.mapError((error) =>
                  protocolError(`could not write the title prompt: ${error.message}`),
                ),
              )
              const exitCode = yield* child.exitCode.pipe(
                Effect.mapError((error) => protocolError(`codex exec failed: ${error.message}`)),
              )
              if (exitCode !== 0) {
                const tail = yield* child.stderrTail
                return yield* Effect.fail(
                  protocolError(
                    `codex exec exited with ${exitCode}` +
                      (tail.length > 0 ? `: ${tail.join("\n")}` : ""),
                  ),
                )
              }
              return yield* fs
                .readFileString(outputFile)
                .pipe(
                  Effect.mapError((error) =>
                    protocolError(`could not read the title output: ${error.message}`),
                  ),
                )
            }).pipe(
              Effect.timeoutOrElse({
                duration: TITLE_TIMEOUT,
                orElse: () =>
                  Effect.fail(
                    protocolError(
                      `title generation produced no result within ${Duration.format(Duration.fromInputUnsafe(TITLE_TIMEOUT))}`,
                    ),
                  ),
              }),
            )
          }),
        ),
      checkSession: (options) =>
        Effect.gen(function* () {
          const state = yield* getClientRetryable
          // thread/list (probed) is the reconciliation aid: absent thread or
          // undeterminable status is honestly `unknown`, never a guess.
          const thread = yield* findThread(state, options.providerSessionId)
          if (typeof thread === "string") return "unknown"
          // Demand-driven descendant reconciliation (ATC-158): thread/list
          // omits descendants, so enumerate the loaded sessions and read
          // each one's parent link and status. An inconclusive walk stays
          // `unknown` — unseen descendants could still be writing.
          const walk = yield* reconcileDescendants(state, options.providerSessionId)
          if (walk === "unknown") return "unknown"
          // Read the reconciled live map, not a walk-time snapshot, so
          // this answer and the live feeds cannot disagree.
          return aggregateActivity(
            statusToActivity(thread.status),
            descendants.get(options.providerSessionId)?.values() ?? [],
          )
        }),
      // Codex holds no per-session launch files or secrets; provider-owned
      // rollouts are never ATC's to touch. Descendant bookkeeping still
      // goes (guarded: a live observer keeps it until its own finalizer).
      releaseSession: (options) => Effect.sync(() => pruneRoot(options.providerSessionId)),
    }
    return adapter
  }),
)
