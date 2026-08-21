import {
  Cause,
  Context,
  Deferred,
  Duration,
  Effect,
  FileSystem,
  Layer,
  Option,
  Queue,
  Schedule,
  Schema,
  Semaphore,
  Stream,
} from "effect"
import * as Poll from "../platform/poll.ts"
import * as path from "node:path"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConflict,
  AgentConnection,
  AgentEvent,
  AgentIdentityMismatch,
  AgentItemEvent,
  AgentModel,
  AgentProtocolError,
  AgentSessionEvent,
  AgentUnavailable,
  EstablishedIdentity,
  ProviderSettings,
  ThreadAccess,
  ThreadAttachment,
  ThreadItem,
  ThreadRequest,
  ThreadSettings,
  TurnInput,
} from "./agentAdapter.ts"
import {
  AgentResumeFailed,
  aggregateActivity,
  buildTitleContext,
  emitAgentEvent,
  makeVersionGate,
  NESTED_SESSION_ENV_VARIABLES,
  parseVersion,
  providerErrors,
  resolveProviderExecutable,
  samePath,
  titleInstruction,
  versionIsOlder,
  withTitleTimeout,
  withToolStatus,
} from "./agentAdapter.ts"
import { isReasoningLevel } from "../api/contract.ts"
import * as BuildInfo from "../platform/buildInfo.ts"
import * as CodexItems from "./codexItems.ts"
import * as CodexServer from "./codexServer.ts"
import { AppConfig } from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"
import * as UnixWebSocket from "../platform/unixWebSocket.ts"

// The Codex AgentAdapter (ATC-123): one JSON-RPC client of the machine's
// shared Codex app-server (codexServer.ts adopts or starts it on Codex's
// well-known unix control socket — ATC-196), multiplexing every thread
// over that single held connection. Because every Codex client on the
// machine — ATC-launched TUIs, plain `codex` in a terminal, the desktop app
// over SSH — is a client of that same process, turns driven from any of
// them are observable on our feed and no surface conflicts with another
// for a thread's writer lock. Invariants beyond the seam's:
//
//   - The transport is a WebSocket over the unix socket (unixWebSocket.ts);
//     ATC-launched TUIs are pointed at the same socket explicitly
//     (`--remote unix://<path>`) so identity capture never depends on the
//     TUI's own auto-join, which silently turns off under `-c` overrides.
//   - One writer per thread is enforced HERE (the app-server would accept a
//     concurrent second writer, and concurrent writers corrupt provider
//     turn attribution — ATC-83 evidence).
//   - Server-initiated requests for OUR turns are parked (the JSON-RPC
//     request stays open) and surfaced as ThreadRequests; `respond` replies
//     on them (ATC-193). Requests of kinds the seam does not surface
//     (permissions, MCP elicitation, tool calls) keep an immediate JSON-RPC
//     rejection so a turn can never hang on ATC. Requests raised for a turn
//     another writer started are never answered here.
//   - Conversation items ride the fan-out for every thread (item/started,
//     item/completed — probed 2026-08-10, ATC-190): a writer session's feed
//     carries them normalized (codexItems.ts) plus text deltas; observers of
//     TUI-driven threads get the same item events. Deltas follow the
//     agentMessage / plan / reasoning-summary streams and always resolve to
//     the full text on item/completed.
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
//   - Settings (ATC-205) ride `turn/start` as overrides, which the protocol
//     keeps "for this turn and subsequent turns": model, effort,
//     approvalPolicy, and sandboxPolicy go out only when they differ from
//     what the session's `thread/start` / `thread/resume` reply echoed (the
//     live baseline), so an unchanged turn carries none. Two things always
//     ride: `approvalsReviewer: "user"` (omitting it keeps a previous
//     reviewer sticky) and `collaborationMode` (the reply echoes no mode,
//     so the caller's chat/plan is asserted every turn; the mode's
//     model/effort mirror the turn's). The access ladder is two knobs here
//     — Supervised is `untrusted` + `read-only` (workspace-write never asks
//     before edits), Full access is `never` + `danger-full-access` — and
//     the reverse read projects any (approvalPolicy, sandbox) pair onto the
//     nearest rung. `thread/settings/updated` (a change made in the TUI or
//     another client) reaches the thread's writer feed and its observers as
//     the seam's `settings` event; the catalog is `model/list`.

/** The codex-cli version this adapter was validated against (record + warn).
 * Gated twice: the CLI ATC resolves (TUI launches, title one-shots) and the
 * running app-server (from `initialize`'s userAgent) — an adopted server
 * can drift from the CLI. */
const CODEX_TESTED_VERSION = "0.147.0"

const REQUEST_TIMEOUT = "30 seconds"

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

const {
  unavailable,
  protocolError,
  conflict,
  identityMismatch,
  connectionClosed,
  turnStillActive,
  staleTurn,
  writerConnected,
  unknownRequest,
  answerKindMismatch,
} = providerErrors("codex")

/** A JSON-RPC error response — mapped per call site (resume vs the rest). */
class RpcError extends Schema.TaggedErrorClass<RpcError>()("RpcError", {
  code: Schema.NullOr(Schema.Number),
  text: Schema.String,
}) {}

// The reply's top level echoes the session's model / effort / approval /
// sandbox (probed live 2026-08-18) — the settings baseline (see the header).
// Decoded loosely: an unrecognized policy shape is "not reported", never a
// failed reply.
const SandboxPolicy = Schema.Struct({ type: Schema.String })
const ThreadReply = Schema.Struct({
  thread: Schema.Struct({
    id: Schema.String,
    cwd: Schema.String,
    status: Schema.optional(Schema.Unknown),
    // Populated on thread/resume only (generated schema): the history read.
    turns: Schema.optional(Schema.Unknown),
  }),
  model: Schema.optional(Schema.String),
  reasoningEffort: Schema.optional(Schema.NullOr(Schema.String)),
  approvalPolicy: Schema.optional(Schema.Unknown),
  sandbox: Schema.optional(Schema.Unknown),
})

// model/list (verified without the experimentalApi capability, 2026-08-18).
const ModelListReply = Schema.Struct({
  data: Schema.Array(
    Schema.Struct({
      model: Schema.String,
      displayName: Schema.String,
      description: Schema.String,
      hidden: Schema.Boolean,
      isDefault: Schema.Boolean,
      supportedReasoningEfforts: Schema.Array(Schema.Struct({ reasoningEffort: Schema.String })),
      defaultReasoningEffort: Schema.String,
    }),
  ),
  nextCursor: Schema.optional(Schema.NullOr(Schema.String)),
})

// thread/settings/updated: the thread's full settings after a change made
// by any client. `collaborationMode.mode` is the only channel that reports
// the plan/chat mode.
const ThreadSettingsUpdatedNotification = Schema.Struct({
  threadId: Schema.String,
  threadSettings: Schema.Struct({
    model: Schema.String,
    effort: Schema.optional(Schema.NullOr(Schema.String)),
    approvalPolicy: Schema.Unknown,
    sandboxPolicy: Schema.Unknown,
    collaborationMode: Schema.optional(Schema.Struct({ mode: Schema.String })),
  }),
})
const decodeThreadSettingsUpdated = Schema.decodeUnknownOption(ThreadSettingsUpdatedNotification)
const decodeSandboxPolicy = Schema.decodeUnknownOption(SandboxPolicy)

// --- The access ladder, both directions (see the header) ---------------------

/** ... and its `turn/start` knobs (sandboxPolicy as a typed object). */
const SANDBOX_POLICY_TYPE = {
  "read-only": "readOnly",
  "workspace-write": "workspaceWrite",
  "danger-full-access": "dangerFullAccess",
} as const

/** The rung's `thread/start` knobs (sandbox as a mode string) ... */
const ACCESS_TO_THREAD_KNOBS: Record<
  ThreadAccess,
  { readonly approvalPolicy: string; readonly sandbox: keyof typeof SANDBOX_POLICY_TYPE }
> = {
  supervised: { approvalPolicy: "untrusted", sandbox: "read-only" },
  autoAcceptEdits: { approvalPolicy: "on-request", sandbox: "workspace-write" },
  auto: { approvalPolicy: "never", sandbox: "workspace-write" },
  fullAccess: { approvalPolicy: "never", sandbox: "danger-full-access" },
}

const turnKnobs = (access: ThreadAccess) => {
  const knobs = ACCESS_TO_THREAD_KNOBS[access]
  return {
    approvalPolicy: knobs.approvalPolicy,
    sandboxPolicy: { type: SANDBOX_POLICY_TYPE[knobs.sandbox] },
  }
}

/**
 * Project a reported (approvalPolicy, sandbox type) pair onto the nearest
 * rung: the sandbox decides the extremes (read-only is Supervised,
 * danger-full-access is Full access, whatever the approval policy), and
 * within workspace-write only `never` is Auto — every asking policy
 * (on-request, untrusted, granular) is Auto-accept edits. Undefined when
 * neither knob was reported.
 */
const accessFromKnobs = (
  approvalPolicy: unknown,
  sandboxType: string | undefined,
): ThreadAccess | undefined => {
  if (sandboxType === "readOnly") return "supervised"
  if (sandboxType === "dangerFullAccess") return "fullAccess"
  if (sandboxType === "workspaceWrite")
    return approvalPolicy === "never" ? "auto" : "autoAcceptEdits"
  return undefined
}

/** The baseline a thread/start or thread/resume reply establishes. */
const echoedSettings = (reply: typeof ThreadReply.Type): ProviderSettings =>
  reportedSettings({
    model: reply.model,
    effort: reply.reasoningEffort,
    approvalPolicy: reply.approvalPolicy,
    sandbox: reply.sandbox,
  })

/**
 * The turn/start overrides for `settings` against the live baseline: only
 * what differs, plus the two that always ride (see the header).
 */
const settingsOverrides = (live: ProviderSettings, settings: ThreadSettings) => ({
  ...(live.model !== settings.model ? { model: settings.model } : {}),
  ...(settings.reasoning !== undefined && live.reasoning !== settings.reasoning
    ? { effort: settings.reasoning }
    : {}),
  ...(live.access !== settings.access ? turnKnobs(settings.access) : {}),
  approvalsReviewer: "user",
  collaborationMode: {
    mode: settings.mode === "plan" ? "plan" : "default",
    settings: { model: settings.model, reasoning_effort: settings.reasoning ?? null },
  },
})

/** The seam's report from a reply or notification's echoed knobs. */
const reportedSettings = (echo: {
  readonly model?: string | undefined
  readonly effort?: string | null | undefined
  readonly approvalPolicy?: unknown
  readonly sandbox?: unknown
  readonly mode?: string | undefined
}): ProviderSettings => {
  const sandboxType = decodeSandboxPolicy(echo.sandbox).pipe(
    Option.map((policy) => policy.type),
    Option.getOrUndefined,
  )
  const access = accessFromKnobs(echo.approvalPolicy, sandboxType)
  return {
    ...(echo.model !== undefined ? { model: echo.model } : {}),
    ...(echo.effort === undefined
      ? {}
      : { reasoning: isReasoningLevel(echo.effort) ? echo.effort : null }),
    ...(access !== undefined ? { access } : {}),
    ...(echo.mode === "plan"
      ? { mode: "plan" as const }
      : echo.mode === "default"
        ? { mode: "chat" as const }
        : {}),
  }
}

const TurnReply = Schema.Struct({ turn: Schema.Struct({ id: Schema.String }) })

// initialize's userAgent carries the server's own version, e.g.
// "codex_chatgpt_ios_remote/0.147.0 (Mac OS 26.5; arm64) ..." (probed live
// 2026-08-15) — the only version evidence an adopted server offers.
const InitializeReply = Schema.Struct({ userAgent: Schema.optional(Schema.String) })

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

// The notification shapes the adapter acts on, decoded with Option-returning
// decoders: an unrecognized or drifted payload stays the deliberate silent
// skip (the default branch always tolerated unknown shapes), but a
// recognized method no longer half-parses through casts.
const SubAgentActivityNotification = Schema.Struct({
  threadId: Schema.String,
  item: Schema.Struct({
    type: Schema.Literal("subAgentActivity"),
    agentThreadId: Schema.String,
  }),
})
// The full-text conversation items on the shared server's fan-out
// (ATC-190): the only conversation evidence a passive socket can reach for
// a TUI-created thread — thread/read {includeTurns: true} is rejected for
// them outright (probed live 2026-08-10). agentMessage carries top-level
// `text`; userMessage carries `content` text blocks — both accepted, and
// an item carrying neither is skipped like any other unrecognized shape.
const MessageItemNotification = Schema.Struct({
  threadId: Schema.String,
  item: Schema.Struct({
    type: Schema.Literals(["userMessage", "agentMessage"]),
    text: Schema.optional(Schema.String),
    content: Schema.optional(
      Schema.Array(Schema.Struct({ type: Schema.String, text: Schema.optional(Schema.String) })),
    ),
  }),
})

const messageItemText = (item: typeof MessageItemNotification.Type.item): string =>
  item.text ??
  (item.content ?? [])
    .flatMap((block) => (block.type === "text" && block.text !== undefined ? [block.text] : []))
    .join("\n")
// item/started and item/completed carry the item plus its turn; the raw
// item is normalized by codexItems.ts (unknown types skip there).
const ItemNotification = Schema.Struct({
  threadId: Schema.String,
  turnId: Schema.String,
  item: Schema.Unknown,
})
// The text streams the seam forwards as deltas (see the header).
const TextDeltaNotification = Schema.Struct({
  threadId: Schema.String,
  itemId: Schema.String,
  delta: Schema.String,
})
const TEXT_DELTA_METHODS = new Set([
  "item/agentMessage/delta",
  "item/plan/delta",
  "item/reasoning/summaryTextDelta",
])
// serverRequest/resolved: a request answered or cleared elsewhere (another
// client, or lifecycle cleanup such as an interrupted turn).
const RequestResolvedNotification = Schema.Struct({
  threadId: Schema.String,
  requestId: Schema.Union([Schema.String, Schema.Number]),
})
const StatusChangedNotification = Schema.Struct({
  threadId: Schema.String,
  status: Schema.optional(Schema.Unknown),
})
const TurnStartedNotification = Schema.Struct({
  threadId: Schema.String,
  turn: Schema.Struct({ id: Schema.String }),
})
const TurnCompletedNotification = Schema.Struct({
  threadId: Schema.String,
  turn: Schema.Struct({ id: Schema.String, status: Schema.optional(Schema.Unknown) }),
})
// thread/started, for the armed TUI capture. parentThreadId stays Unknown:
// its *presence as a string* is the subagent-defense signal, checked at the
// capture site.
const ThreadStartedNotification = Schema.Struct({
  thread: Schema.Struct({
    id: Schema.String,
    cwd: Schema.String,
    parentThreadId: Schema.optional(Schema.Unknown),
  }),
})
const decodeSubAgentActivity = Schema.decodeUnknownOption(SubAgentActivityNotification)
const decodeMessageItem = Schema.decodeUnknownOption(MessageItemNotification)
const decodeItemNotification = Schema.decodeUnknownOption(ItemNotification)
const decodeTextDelta = Schema.decodeUnknownOption(TextDeltaNotification)
const decodeRequestResolved = Schema.decodeUnknownOption(RequestResolvedNotification)
const decodeStatusChanged = Schema.decodeUnknownOption(StatusChangedNotification)
const decodeTurnStarted = Schema.decodeUnknownOption(TurnStartedNotification)
const decodeTurnCompleted = Schema.decodeUnknownOption(TurnCompletedNotification)
const decodeThreadStarted = Schema.decodeUnknownOption(ThreadStartedNotification)

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

/** A parked server request: the JSON-RPC id to reply on plus its normalized shape. */
interface PendingRequest {
  readonly rpcId: number | string
  readonly request: ThreadRequest
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
  /** The active turn's TOOL items by id (latest state), so an approval can
   * name its item and flip it to pending; cleared at turn end. Text items
   * are never retained — the Thread runtime owns the transcript. */
  readonly toolItems: Map<string, ThreadItem>
  /** Parked requests by request id, closed with their turn. */
  readonly pendingRequests: Map<string, PendingRequest>
  /** The attachments this client sent, by the localImage path it named, so
   * the provider's echo of the prompt maps back to them (ATC-216). */
  readonly attachmentsByPath: Map<string, ThreadAttachment>
  /** Set when the transport died underneath the session (vs. caller close). */
  failed: boolean
  /** The settings baseline: what the session runs with, as last echoed
   * (thread/start, thread/resume, thread/settings/updated) or pushed. */
  live: ProviderSettings
}

/** skills/list, decoded to what a command listing needs. */
const SkillsListReply = Schema.Struct({
  data: Schema.Array(
    Schema.Struct({
      skills: Schema.Array(
        Schema.Struct({
          name: Schema.String,
          description: Schema.String,
          enabled: Schema.Boolean,
          shortDescription: Schema.optional(Schema.NullOr(Schema.String)),
        }),
      ),
    }),
  ),
})

/** Reserves the active-turn slot between the local check and the RPC reply. */
const PENDING_TURN = "(pending)"

/** The turn input in the protocol's shape: the text, then each image as a
 * `localImage` the app-server reads from the shared host (ATC-216). */
const turnInputBlocks = (input: TurnInput) => [
  ...(input.text === "" ? [] : [{ type: "text" as const, text: input.text }]),
  ...input.attachments.map((attachment) => ({
    type: "localImage" as const,
    path: attachment.path,
  })),
]

interface PendingReply {
  readonly succeed: (result: unknown) => void
  readonly fail: (error: RpcError | AgentProtocolError) => void
}

interface ClientState {
  readonly socket: UnixWebSocket.Connection
  readonly pending: Map<number, PendingReply>
  nextId: number
  alive: boolean
}

// --- Closure-free RPC plumbing (ATC-167 M13): everything below talks to a
// ClientState it is handed, never to layer state, so it lives outside the
// layer body. ---

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

/** thread/resume's error mapping: the server's invalid-request code on an
 * unknown thread id is the fail-closed AgentResumeFailed. */
const resumeError =
  (providerSessionId: string) =>
  (error: RpcError | AgentProtocolError): AgentResumeFailed | AgentProtocolError =>
    error._tag === "RpcError" && error.code === -32600
      ? new AgentResumeFailed({ provider: "codex", providerSessionId, reason: error.text })
      : rpcToProtocol(error)

/** The passive seam's error mapping: an RPC failure is "unavailable". */
const rpcToUnavailable = (error: RpcError | AgentProtocolError): AgentUnavailable =>
  unavailable(error._tag === "RpcError" ? error.text : error.reason)

/** `decodeReply` for the passive seam, naming the method in the diagnostic. */
const decodeUnavailable =
  <S extends Schema.Top>(schema: S, method: string) =>
  (reply: unknown): Effect.Effect<S["Type"], AgentUnavailable, S["DecodingServices"]> =>
    Schema.decodeUnknownEffect(schema)(reply).pipe(
      Effect.mapError((error) => unavailable(`unexpected ${method} reply: ${error.message}`)),
    )

/**
 * Drive one paginated codex RPC to completion (the {data, nextCursor} page
 * shape): `onPage` sees every page and may finish the walk early by
 * returning a value; a complete walk (cursor exhausted) yields
 * `complete()`; a repeated cursor or the page cap yields "unknown" — a
 * provider that cannot make pagination progress, or a pathologically long
 * population.
 */
const walkPaginated = <
  Page extends { readonly nextCursor?: string | null | undefined },
  Found,
  Done,
  E,
>(options: {
  readonly page: (cursor: string | undefined) => Effect.Effect<Page, E>
  readonly onPage: (page: Page) => Found | undefined
  readonly complete: () => Done
}): Effect.Effect<Found | Done | "unknown", E> =>
  Effect.gen(function* () {
    let cursor: string | undefined
    for (let page = 0; page < 100; page++) {
      const decoded = yield* options.page(cursor)
      const found = options.onPage(decoded)
      if (found !== undefined) return found
      const next = decoded.nextCursor
      if (typeof next !== "string") return options.complete()
      if (next === cursor) return "unknown" as const
      cursor = next
    }
    return "unknown" as const
  })

/**
 * Walk one paginated thread/list population (thread/list returns only
 * non-archived threads unless `archived: true` — the populations are
 * disjoint): the found entry, `"absent"` when a complete walk omits the
 * id, or `"unknown"` when the walk could not be completed.
 */
const walkThreadList = (state: ClientState, threadId: string, archived: boolean) =>
  walkPaginated({
    page: (cursor) =>
      request(
        state,
        "thread/list",
        cursor === undefined ? { archived } : { archived, cursor },
      ).pipe(
        Effect.mapError(rpcToUnavailable),
        Effect.flatMap(decodeUnavailable(ThreadListReply, "thread/list")),
      ),
    onPage: (page) => page.data.find((t) => t.id === threadId),
    complete: () => "absent" as const,
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
    return live === "absent" && archived === "absent" ? ("absent" as const) : ("unknown" as const)
  })

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
    // Passive per-thread observers (TUI-driven sessions): activity plus the
    // fan-out's item events. Unlike sessions they survive reconnects: the
    // map is adapter-level, and a new connection's broadcasts route to the
    // same queues.
    const observers = new Map<string, Set<Queue.Queue<AgentSessionEvent, Cause.Done>>>()

    // Descendant bookkeeping (ATC-158): each thread's own last-broadcast
    // status, the child→parent links learned from subAgentActivity items
    // (and checkSession reconciliation), and per-root descendant statuses.
    // Live evidence only — cleared on connection teardown and rebuilt from
    // fresh broadcasts or the demand-driven checkSession walk.
    const ownStatus = new Map<string, AgentActivity>()
    const parentOf = new Map<string, string>()
    const descendants = new Map<string, Map<string, AgentActivity>>()

    // Recent conversation text per OBSERVED session (ATC-190), retained
    // from the userMessage/agentMessage items already arriving on the held
    // socket — no new RPCs, no polling. Folded through buildTitleContext on
    // every message, so the stored value is always the already-capped
    // context itself. In-memory only and pruned with the rest of the
    // per-root bookkeeping; losing it merely costs a title refinement its
    // context.
    const recentMessages = new Map<string, string>()

    const retainMessage = (threadId: string, kind: "userMessage" | "agentMessage", raw: string) => {
      if (!observers.has(threadId)) return
      const text = raw.trim()
      if (text === "") return
      const line = `${kind === "userMessage" ? "user" : "assistant"}: ${text}`
      const context = buildTitleContext([recentMessages.get(threadId) ?? "", line])
      if (context !== null) recentMessages.set(threadId, context)
    }

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
    /** Deliver one event to a thread's observers. Overflow ends the
     * observation: dropping intermediate transitions would be silent
     * evidence loss (the first busy drives the confirm marker), while an
     * ended feed makes the consumer re-observe and re-derive. */
    const observe = (threadId: string, event: AgentSessionEvent): void => {
      for (const queue of observers.get(threadId) ?? []) {
        if (!Queue.offerUnsafe(queue, event)) Queue.endUnsafe(queue)
      }
    }

    const emitAggregate = (rootId: string): void => {
      const aggregate = aggregateFor(rootId)
      observe(rootId, { type: "activity", activity: aggregate })
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
      recentMessages.delete(rootId)
    }
    // Raw frames arrive on a sync WebSocket callback with no fiber to log
    // from, so the header's "raw events are logged at debug level" runs
    // through this bridge: a sliding queue (bursts may drop old frames —
    // acceptable for diagnostics) drained by one scoped logger fiber.
    const rawFrames = yield* Queue.make<string>({ capacity: 256, strategy: "sliding" })
    yield* Stream.fromQueue(rawFrames).pipe(
      Stream.runForEach((raw) =>
        Effect.logDebug("codex app-server frame").pipe(Effect.annotateLogs({ raw })),
      ),
      Effect.forkScoped,
    )

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
      const decoded = decodeThreadStarted(params)
      if (Option.isNone(decoded)) return
      const thread = decoded.value.thread
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

    const emit = (session: LiveSession, event: AgentEvent): void =>
      emitAgentEvent(session.queue, event, protocolError)

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
        for (const threadId of observers.keys()) {
          observe(threadId, { type: "activity", activity: "unknown" })
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

    /** Flip a tool item pending ↔ running around its approval. */
    const restatusItem = (
      session: LiveSession,
      itemId: string | undefined,
      status: "pending" | "running",
    ): void => {
      const item = itemId === undefined ? undefined : session.toolItems.get(itemId)
      const updated = item === undefined ? null : withToolStatus(item, status)
      if (updated === null) return
      session.toolItems.set(updated.id, updated)
      emit(session, { type: "itemUpdated", item: updated })
    }

    const handleServerRequest = (
      state: ClientState,
      message: {
        id: number | string
        method: string
        params: Record<string, unknown> | undefined
      },
    ): void => {
      const threadId = message.params?.["threadId"]
      const turnId = message.params?.["turnId"]
      const session = typeof threadId === "string" ? sessions.get(threadId) : undefined
      // Only answer requests for turns THIS client started: a shared-server
      // request belonging to another writer (a TUI's approval prompt) is
      // never ours to answer or reject.
      if (session === undefined || session.ownTurn !== turnId) return
      const requestId = String(message.id)
      const itemId = message.params?.["itemId"]
      const request = CodexItems.mapRequest(
        message.method,
        requestId,
        message.params,
        typeof itemId === "string" ? session.toolItems.get(itemId) : undefined,
        new Date().toISOString(),
      )
      if (request === null) {
        // Not a kind the seam surfaces (yet): the conservative reject, so
        // the turn proceeds instead of hanging on ATC.
        state.socket.send(
          JSON.stringify({
            id: message.id,
            error: { code: -32601, message: "atc does not answer this kind of provider request" },
          }),
        )
        return
      }
      session.pendingRequests.set(requestId, { rpcId: message.id, request })
      restatusItem(session, request.itemId, "pending")
      emit(session, { type: "requestOpened", request })
    }

    /** Close every parked request of a session (its turn ended). */
    const closePendingRequests = (session: LiveSession): void => {
      for (const requestId of session.pendingRequests.keys()) {
        emit(session, { type: "requestClosed", requestId })
      }
      session.pendingRequests.clear()
    }

    const handleNotification = (message: {
      method: string
      params: Record<string, unknown> | undefined
    }): void => {
      const params = message.params ?? {}
      if (message.method === "thread/started") return captureStarted(params)
      // The child→root mapping arrives as a subAgentActivity item on the
      // PARENT's feed — descendants do not broadcast thread/started
      // (probed, experiments/subagent-activity).
      if (message.method === "item/started" || message.method === "item/completed") {
        const subAgent = decodeSubAgentActivity(params)
        if (Option.isSome(subAgent)) {
          registerDescendant(subAgent.value.threadId, subAgent.value.item.agentThreadId)
          emitAggregate(resolveRoot(subAgent.value.threadId))
        }
        // Completed conversation items feed the title-context retention.
        if (message.method === "item/completed") {
          const item = decodeMessageItem(params)
          if (Option.isSome(item)) {
            retainMessage(
              item.value.threadId,
              item.value.item.type,
              messageItemText(item.value.item),
            )
          }
        }
        // The normalized item reaches the thread's writer feed and its
        // observers alike; an item type the seam does not carry, or a
        // drifted shape, is the deliberate silent skip.
        const decoded = decodeItemNotification(params)
        if (Option.isNone(decoded)) return
        const phase = message.method === "item/started" ? "started" : "completed"
        const session = sessions.get(decoded.value.threadId)
        const item = CodexItems.mapItem(decoded.value.item, decoded.value.turnId, phase, (path) =>
          session?.attachmentsByPath.get(path),
        )
        if (item === null) return
        const event: AgentItemEvent =
          phase === "started" ? { type: "itemStarted", item } : { type: "itemCompleted", item }
        if (session !== undefined) {
          if ("status" in item) session.toolItems.set(item.id, item)
          emit(session, event)
        }
        observe(decoded.value.threadId, event)
        return
      }
      if (message.method === "thread/settings/updated") {
        const decoded = decodeThreadSettingsUpdated(params)
        if (Option.isNone(decoded)) return
        const { threadId, threadSettings } = decoded.value
        const reported = reportedSettings({
          model: threadSettings.model,
          effort: threadSettings.effort,
          approvalPolicy: threadSettings.approvalPolicy,
          sandbox: threadSettings.sandboxPolicy,
          mode: threadSettings.collaborationMode?.mode,
        })
        const session = sessions.get(threadId)
        if (session !== undefined) {
          session.live = { ...session.live, ...reported }
          emit(session, { type: "settings", settings: reported })
        }
        observe(threadId, { type: "settings", settings: reported })
        return
      }
      if (message.method === "serverRequest/resolved") {
        // Resolved elsewhere (another client answered, or the turn's
        // cleanup cleared it): the parked request is gone, and a later
        // respond is the conflict it should be.
        const decoded = decodeRequestResolved(params)
        if (Option.isNone(decoded)) return
        const session = sessions.get(decoded.value.threadId)
        const requestId = String(decoded.value.requestId)
        if (session === undefined || !session.pendingRequests.delete(requestId)) return
        emit(session, { type: "requestClosed", requestId })
        return
      }
      if (TEXT_DELTA_METHODS.has(message.method)) {
        const decoded = decodeTextDelta(params)
        if (Option.isNone(decoded)) return
        const session = sessions.get(decoded.value.threadId)
        if (session === undefined) return
        emit(session, {
          type: "textDelta",
          itemId: decoded.value.itemId,
          delta: decoded.value.delta,
        })
        return
      }
      // Coarse status fans out for EVERY thread on the shared server
      // without subscription (probed) — descendants included. A
      // descendant's change folds into its root's aggregate; a root's
      // change reaches its observers and (deduped) writer feed.
      if (message.method === "thread/status/changed") {
        const decoded = decodeStatusChanged(params)
        if (Option.isNone(decoded)) return
        const { threadId, status } = decoded.value
        const activity = statusToActivity(status)
        ownStatus.set(threadId, activity)
        const rootId = resolveRoot(threadId)
        if (rootId !== threadId) descendants.get(rootId)?.set(threadId, activity)
        emitAggregate(rootId)
        return
      }
      if (message.method === "turn/started") {
        const decoded = decodeTurnStarted(params)
        if (Option.isNone(decoded)) return
        const session = sessions.get(decoded.value.threadId)
        if (session === undefined) return
        session.activeTurn = decoded.value.turn.id
        emit(session, { type: "turnStarted", turnId: decoded.value.turn.id })
        return
      }
      if (message.method === "turn/completed") {
        const decoded = decodeTurnCompleted(params)
        if (Option.isNone(decoded)) return
        const session = sessions.get(decoded.value.threadId)
        if (session === undefined) return
        const { id: turnId, status } = decoded.value.turn
        if (session.activeTurn === turnId) session.activeTurn = null
        if (session.ownTurn === turnId) {
          session.ownTurn = null
          // Requests die with their turn (an interrupted turn drops its
          // parked approvals); a re-run turn re-asks.
          closePendingRequests(session)
        }
        session.toolItems.clear()
        const outcome =
          status === "completed" ? "completed" : status === "interrupted" ? "interrupted" : "failed"
        emit(
          session,
          outcome === "failed"
            ? { type: "turnCompleted", turnId, outcome, detail: String(status) }
            : { type: "turnCompleted", turnId, outcome },
        )
        return
      }
      // Output deltas, token usage, MCP startup noise: not facts the seam carries.
    }

    const dispatch = (state: ClientState, raw: string): void => {
      Queue.offerUnsafe(rawFrames, raw)
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

    // Record + warn version drift, once, on first use (never blocks).
    const versionCheck = yield* makeVersionGate(
      subprocess,
      "codex",
      config.codexExecutable,
      CODEX_TESTED_VERSION,
    )

    const openSocket = (socketPath: string): Effect.Effect<ClientState, AgentUnavailable> =>
      Effect.callback<ClientState, AgentUnavailable>((resume) => {
        const socket = UnixWebSocket.open(socketPath)
        const state: ClientState = { socket, pending: new Map(), nextId: 1, alive: true }
        socket.onopen = () => resume(Effect.succeed(state))
        // Before open this is the connect failure; after, a lost connection.
        socket.onclose = (reason) => {
          teardown(state, `the codex app-server connection closed: ${reason}`)
          resume(Effect.fail(unavailable(`could not connect to ${socketPath}: ${reason}`)))
        }
        socket.onmessage = (text) => dispatch(state, text)
        return Effect.sync(() => {
          if (current !== state) teardown(state, "connection attempt interrupted")
        })
      }).pipe(
        Effect.timeoutOrElse({
          duration: "5 seconds",
          orElse: () => Effect.fail(unavailable(`timed out connecting to ${socketPath}`)),
        }),
      )

    const connect = Effect.gen(function* () {
      yield* versionCheck
      const info = yield* codexServer.ensure()
      const state = yield* openSocket(info.socketPath)
      // A failed or interrupted handshake must close this socket: a
      // half-open connection would double-deliver broadcasts and, on its
      // eventual death, could not be told apart from the live one.
      const reply = yield* request(state, "initialize", {
        clientInfo: { name: "atc", title: "ATC App Server", version: build.version },
        // turn/start's collaborationMode (the plan/chat mode) is gated behind
        // the experimental capability (probed live 2026-08-18); T3Code opens
        // the same way.
        capabilities: { experimentalApi: true },
      }).pipe(
        Effect.mapError(rpcToProtocol),
        Effect.flatMap((reply) => decodeReply(InitializeReply, reply)),
        Effect.onExit((exit) =>
          Effect.sync(() => {
            if (exit._tag === "Failure") teardown(state, "the handshake failed")
          }),
        ),
      )
      state.socket.send(JSON.stringify({ method: "initialized", params: {} }))
      current = state
      // The shared server may be older than the CLI ATC resolved (adopted
      // from another client): record + warn, never block.
      const serverVersion = parseVersion(reply.userAgent ?? "")
      if (serverVersion !== null && versionIsOlder(serverVersion, CODEX_TESTED_VERSION)) {
        yield* Effect.logWarning(
          `the shared codex app-server is ${serverVersion}, older than the tested ${CODEX_TESTED_VERSION}; proceeding, but restart it on a newer codex if provider behavior misbehaves`,
        )
      }
      return state
    })

    // The connection attempt's outcome is memoized for a short window so a
    // dead Codex costs ONE probe (with its full connect timeouts) per window
    // instead of one per caller — a thread-list fan-out otherwise serializes
    // every record behind those timeouts (ATC-167 H4). The memo also records
    // interruption (cachedWithTTL caches whatever exit the driver produced),
    // so an interrupted driver invalidates on the way out rather than making
    // later callers replay its interrupt. cachedConnect may only be entered
    // under connectLock: without it, an invalidate racing a just-released
    // cache waiter would replay a cleared exit. The window also briefly
    // shadows codexServer's own crash-restart watcher — a retry schedule
    // against getClient shorter than the window would spin on the memo.
    const [cachedConnect, invalidateConnect] = yield* Effect.cachedInvalidateWithTTL(
      connect,
      "5 seconds",
    )
    const attemptConnect = Effect.onInterrupt(cachedConnect, () => invalidateConnect)

    /** The shared connection: reuse, or ensure the server and handshake. */
    const getClient = connectLock.withPermits(1)(
      Effect.gen(function* () {
        if (current !== null && current.alive) return current
        const state = yield* attemptConnect
        if (state.alive) return state
        // A memoized success can predate a teardown; never hand out a dead
        // connection — drop the memo and probe fresh.
        yield* invalidateConnect
        return yield* attemptConnect
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
      live: ProviderSettings,
    ) =>
      Effect.gen(function* () {
        // Bounded (overflow ends the stream — see emit); a session that
        // buffers 256 undrained events has lost its consumer.
        const queue = yield* Queue.make<AgentEvent, AgentProtocolError | Cause.Done>({
          capacity: 256,
        })
        const session: LiveSession = {
          threadId,
          queue,
          activity: "unknown",
          activeTurn: null,
          ownTurn: null,
          toolItems: new Map(),
          pendingRequests: new Map(),
          attachmentsByPath: new Map(),
          failed: false,
          live,
        }
        const unregister = (): void => {
          if (sessions.get(threadId) !== session) return
          sessions.delete(threadId)
          pruneRoot(threadId)
          Queue.endUnsafe(queue)
          if (state.alive) {
            // Requests parked on a closing connection get the conservative
            // reject: nothing will answer them, and an unanswered request
            // would park the provider's turn forever.
            for (const pending of session.pendingRequests.values()) {
              state.socket.send(
                JSON.stringify({
                  id: pending.rpcId,
                  error: {
                    code: -32601,
                    message: "atc closed the connection with the request pending",
                  },
                }),
              )
            }
            session.pendingRequests.clear()
            state.socket.send(
              JSON.stringify({
                id: state.nextId++,
                method: "thread/unsubscribe",
                params: { threadId },
              }),
            )
          }
        }
        // Registry publication and its finalizer land atomically
        // (acquireRelease — ATC-167 M4). Seeded from the provider's own
        // thread status — never a guess — folded with any descendants
        // already tracked for this root.
        yield* Effect.acquireRelease(
          Effect.sync(() => {
            ownStatus.set(threadId, statusToActivity(initialStatus))
            session.activity = aggregateFor(threadId)
            sessions.set(threadId, session)
          }),
          () => Effect.sync(unregister),
        )

        // A dead transport is the retryable AgentUnavailable; only a
        // caller-closed connection is a conflict.
        const requireLive = Effect.suspend(
          (): Effect.Effect<void, AgentConflict | AgentUnavailable> =>
            session.failed || !state.alive
              ? Effect.fail(unavailable("the app-server connection was lost; resume the session"))
              : sessions.get(threadId) === session
                ? Effect.void
                : Effect.fail(connectionClosed()),
        )

        const connection: AgentConnection = {
          providerSessionId: threadId,
          cwd,
          activity: Effect.sync(() => session.activity),
          events: Stream.fromQueue(queue),
          startTurn: (input, settings) =>
            Effect.gen(function* () {
              yield* requireLive
              if (session.activeTurn !== null) {
                return yield* Effect.fail(turnStillActive(session.activeTurn))
              }
              // Reserve the slot before suspending on the RPC, or two
              // concurrent startTurn calls would both pass the check.
              session.activeTurn = PENDING_TURN
              session.ownTurn = PENDING_TURN
              const overrides = settingsOverrides(session.live, settings)
              for (const attachment of input.attachments) {
                session.attachmentsByPath.set(attachment.path, attachment)
              }
              const decoded = yield* request(state, "turn/start", {
                threadId,
                input: turnInputBlocks(input),
                ...overrides,
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
              // The overrides are sticky: the session now runs with them.
              session.live = {
                model: settings.model,
                reasoning: settings.reasoning ?? null,
                mode: settings.mode,
                access: settings.access,
              }
              return { turnId: decoded.turn.id }
            }),
          steer: (input, turn) =>
            Effect.gen(function* () {
              yield* requireLive
              if (session.activeTurn !== turn.turnId) {
                return yield* Effect.fail(staleTurn(turn.turnId))
              }
              for (const attachment of input.attachments) {
                session.attachmentsByPath.set(attachment.path, attachment)
              }
              // expectedTurnId is the server's own precondition; its
              // rejection means the turn ended in flight — a conflict, as
              // for interrupt. The server echoes the message as the turn's
              // next userMessage item on the fan-out.
              yield* request(state, "turn/steer", {
                threadId,
                expectedTurnId: turn.turnId,
                input: turnInputBlocks(input),
              }).pipe(
                Effect.mapError((error) =>
                  error._tag === "RpcError" ? conflict(error.text) : error,
                ),
              )
            }),
          interrupt: (turn) =>
            Effect.gen(function* () {
              yield* requireLive
              if (session.activeTurn !== turn.turnId) {
                return yield* Effect.fail(staleTurn(turn.turnId))
              }
              // A provider-side rejection means the target went stale in
              // flight — the same conflict as losing the local race.
              yield* request(state, "turn/interrupt", { threadId, turnId: turn.turnId }).pipe(
                Effect.mapError((error) =>
                  error._tag === "RpcError" ? conflict(error.text) : error,
                ),
              )
            }),
          respond: (requestId, answer) =>
            Effect.gen(function* () {
              yield* requireLive
              const pending = session.pendingRequests.get(requestId)
              if (pending === undefined) return yield* Effect.fail(unknownRequest(requestId))
              if (pending.request.kind !== answer.kind) {
                return yield* Effect.fail(answerKindMismatch(requestId, pending.request.kind))
              }
              session.pendingRequests.delete(requestId)
              state.socket.send(
                JSON.stringify({ id: pending.rpcId, result: CodexItems.answerResult(answer) }),
              )
              // The item resumes (or ends declined — item/completed says so).
              restatusItem(session, pending.request.itemId, "running")
              emit(session, { type: "requestClosed", requestId })
            }),
        }
        return { connection, session, unregister }
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
        const walk = yield* walkPaginated({
          page: (cursor) =>
            request(state, "thread/loaded/list", cursor === undefined ? {} : { cursor }).pipe(
              Effect.mapError(rpcToUnavailable),
              Effect.flatMap(decodeUnavailable(LoadedListReply, "thread/loaded/list")),
            ),
          onPage: (page) => {
            loaded.push(...page.data)
            return undefined
          },
          complete: () => "complete" as const,
        })
        if (walk !== "complete") return "unknown" as const
        // Cost bound: the loaded set is normally a handful of sessions on
        // the local in-memory server; a pathological population makes the
        // walk inconclusive rather than open-ended.
        if (loaded.length > 200) return "unknown" as const
        const info = new Map<string, { parent: string | null; activity: AgentActivity }>()
        for (const id of loaded) {
          if (id === rootId) continue
          const decoded = yield* request(state, "thread/read", { threadId: id }).pipe(
            Effect.mapError(rpcToUnavailable),
            Effect.flatMap(decodeUnavailable(ThreadReadReply, "thread/read")),
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
          for (const id of tracked.keys()) {
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
      sharedServer: true,
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
                model: options.settings.model,
                ...ACCESS_TO_THREAD_KNOBS[options.settings.access],
                approvalsReviewer: "user",
              }).pipe(Effect.mapError(rpcToProtocol))
              const decoded = yield* decodeReply(ThreadReply, reply)
              if (!samePath(decoded.thread.cwd, options.cwd)) {
                return yield* Effect.fail(identityMismatch("cwd", options.cwd, decoded.thread.cwd))
              }
              return yield* registerSession(
                state,
                decoded.thread.id,
                options.cwd,
                decoded.thread.status,
                echoedSettings(decoded),
              )
            }),
          )
          // The seam widens startTurn's errors for deferred-verification
          // providers; codex verified above, so those tags are impossible.
          // A failed first turn releases the writer slot — the caller got
          // no connection handle, so nothing must stay claimed.
          const turn = yield* connection.startTurn(options.input, options.settings).pipe(
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
              return yield* Effect.fail(writerConnected(options.providerSessionId))
            }
            const reply = yield* request(state, "thread/resume", {
              threadId: options.providerSessionId,
              cwd: options.cwd,
            }).pipe(Effect.mapError(resumeError(options.providerSessionId)))
            const decoded = yield* decodeReply(ThreadReply, reply)
            if (decoded.thread.id !== options.providerSessionId) {
              return yield* Effect.fail(
                identityMismatch("sessionId", options.providerSessionId, decoded.thread.id),
              )
            }
            if (!samePath(decoded.thread.cwd, options.cwd)) {
              return yield* Effect.fail(identityMismatch("cwd", options.cwd, decoded.thread.cwd))
            }
            const { connection, session } = yield* registerSession(
              state,
              decoded.thread.id,
              options.cwd,
              decoded.thread.status,
              echoedSettings(decoded),
            )
            // A turn still running on the shared server (ours before a
            // restart, or a TUI's) is the interrupt target this connection
            // may name; thread/resume's turns say which it is.
            const running = yield* CodexItems.decodeHistoryTurns(decoded.thread.turns ?? []).pipe(
              Effect.map((turns) => turns.findLast((turn) => turn.status === "inProgress")),
              Effect.orElseSucceed(() => undefined),
            )
            if (running !== undefined && session.activeTurn === null) {
              session.activeTurn = running.id
            }
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
              command: [
                executable,
                "resume",
                "--remote",
                `unix://${info.socketPath}`,
                options.providerSessionId,
              ],
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
              command: [executable, "--cd", options.cwd, "--remote", `unix://${info.socketPath}`],
              env: {},
            },
            identity: Deferred.await(gate),
          }
        }),
      observeSession: (options) =>
        Effect.gen(function* () {
          // Ensure the shared connection exists — it is the evidence source.
          yield* getClientRetryable
          // Bounded (overflow ends the feed — see observe); items arrive in
          // bursts, so the backlog is sized for a turn's worth of them.
          const queue = yield* Queue.make<AgentSessionEvent, Cause.Done>({ capacity: 256 })
          // Observer registration and its finalizer land atomically
          // (acquireRelease — ATC-167 M4).
          yield* Effect.acquireRelease(
            Effect.sync(() => {
              const set = observers.get(options.providerSessionId) ?? new Set()
              set.add(queue)
              observers.set(options.providerSessionId, set)
              return set
            }),
            (set) =>
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
          // Bounded like the activity queues; prompts arrive one per turn
          // start, so 16 undrained means the observation is dead anyway.
          const promptQueue = yield* Effect.acquireRelease(
            Queue.make<AgentSessionEvent, Cause.Done>({ capacity: 16 }),
            (queue) => Effect.sync(() => Queue.endUnsafe(queue)),
          )
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
          const discoverPrompt = Poll.pollUntil(readPreview, {
            until: (text) => text !== null,
            schedule: Schedule.min([
              Schedule.exponential(Duration.millis(PROMPT_READ_BACKOFF_MS)),
              Schedule.spaced(Duration.millis(PROMPT_READ_BACKOFF_CAP_MS)),
            ]).pipe(Schedule.upTo({ times: PROMPT_READ_ATTEMPTS - 1 })),
          }).pipe(
            Effect.flatMap((text) =>
              Effect.sync(() => {
                if (text === null) {
                  promptPhase = "pending"
                  return
                }
                promptPhase = "done"
                if (!Queue.offerUnsafe(promptQueue, { type: "userPrompt", text })) {
                  Queue.endUnsafe(promptQueue)
                }
              }),
            ),
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
          const observedEvents = Stream.fromQueue(queue).pipe(
            Stream.tap((event) =>
              Effect.gen(function* () {
                // Any provider-evidenced transition can carry the news that
                // history exists; `unknown` is the teardown signal, where a
                // read could only fail and burn the attempt.
                if (
                  event.type === "activity" &&
                  promptPhase === "pending" &&
                  event.activity !== "unknown"
                ) {
                  promptPhase = "reading"
                  yield* discoverPrompt.pipe(Effect.forkIn(scope))
                }
              }),
            ),
          )
          return Stream.merge(observedEvents, Stream.fromQueue(promptQueue))
        }),
      // The shared server's skill index for the directory (the protocol
      // lists skills only — custom prompts have no listing); disabled
      // skills are not offered.
      listCommands: (options) =>
        Effect.gen(function* () {
          const state = yield* getClient
          const reply = yield* request(state, "skills/list", { cwds: [options.cwd] }).pipe(
            Effect.mapError(rpcToProtocol),
            Effect.flatMap((raw) => decodeReply(SkillsListReply, raw)),
          )
          return reply.data
            .flatMap((entry) => entry.skills)
            .filter((skill) => skill.enabled)
            .map((skill) => ({
              name: skill.name,
              description: skill.shortDescription ?? skill.description,
            }))
        }),
      // model/list, every page (walkPaginated; a walk that cannot complete
      // is a protocol error, never a partial catalog), hidden entries
      // dropped; efforts outside the seam's ReasoningLevel vocabulary are
      // dropped too (the model still lists).
      listModels: () =>
        Effect.gen(function* () {
          const state = yield* getClient
          const collected: Array<(typeof ModelListReply.Type.data)[number]> = []
          const walked = yield* walkPaginated({
            page: (cursor) =>
              request(state, "model/list", cursor === undefined ? {} : { cursor }).pipe(
                Effect.mapError(rpcToProtocol),
                Effect.flatMap((reply) => decodeReply(ModelListReply, reply)),
              ),
            onPage: (page) => {
              collected.push(...page.data)
              return undefined
            },
            complete: () => "complete" as const,
          })
          if (walked !== "complete") {
            return yield* Effect.fail(protocolError("model/list pagination did not complete"))
          }
          return collected
            .filter((model) => !model.hidden)
            .map((model) => {
              const levels = model.supportedReasoningEfforts
                .map((option) => option.reasoningEffort)
                .filter(isReasoningLevel)
              // Clamped to what the model lists: a default outside its own
              // levels would be adopted on a model switch and then rejected.
              const reported = model.defaultReasoningEffort
              const defaultLevel =
                isReasoningLevel(reported) && levels.includes(reported) ? reported : levels[0]
              const entry: { -readonly [K in keyof AgentModel]: AgentModel[K] } = {
                value: model.model,
                displayName: model.displayName,
                description: model.description,
                isDefault: model.isDefault,
                supportedEffortLevels: levels,
              }
              if (defaultLevel !== undefined) entry.defaultEffortLevel = defaultLevel
              return entry
            })
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
              yield* child.writeLine(titleInstruction(options.prompt, options.refine)).pipe(
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
            }).pipe(withTitleTimeout(protocolError))
          }),
        ),
      collectTitleContext: (options) =>
        Effect.sync(() => recentMessages.get(options.providerSessionId) ?? null),
      readHistory: (options) =>
        Effect.gen(function* () {
          const state = yield* getClient
          // thread/resume returns full typed turns[].items[] and is
          // side-effect free on an already-loaded thread (probed
          // 2026-08-17). It loads the thread when needed — which the read
          // needs anyway — and never opens a writer or starts a turn.
          const reply = yield* request(state, "thread/resume", {
            threadId: options.providerSessionId,
            cwd: options.cwd,
          }).pipe(Effect.mapError(resumeError(options.providerSessionId)))
          const decoded = yield* decodeReply(ThreadReply, reply)
          if (decoded.thread.id !== options.providerSessionId) {
            return yield* Effect.fail(
              protocolError(
                `thread/resume answered for ${decoded.thread.id}, not ${options.providerSessionId}`,
              ),
            )
          }
          const turns = yield* CodexItems.decodeHistoryTurns(decoded.thread.turns ?? []).pipe(
            Effect.mapError((error) => protocolError(`unexpected turns shape: ${error.message}`)),
          )
          return CodexItems.mapHistory(turns)
        }),
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
