import type {
  CanUseTool,
  HookCallbackMatcher,
  HookEvent,
  Options,
  PermissionResult,
  PermissionUpdate,
  SDKMessage,
  SDKUserMessage,
  SessionMessage,
} from "@anthropic-ai/claude-agent-sdk"
import { getSessionMessages, query } from "@anthropic-ai/claude-agent-sdk"
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
  Schema,
  Stream,
} from "effect"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConflict,
  AgentConnection,
  AgentEvent,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentSessionEvent,
  AgentUnavailable,
  HistoryTurn,
  ThreadItem,
  ThreadRequest,
} from "./agentAdapter.ts"
import {
  AgentResumeFailed,
  buildTitleContext,
  emitAgentEvent,
  makeVersionGate,
  NESTED_SESSION_ENV_VARIABLES,
  providerErrors,
  resolveProviderExecutable,
  samePath,
  titleInstruction,
  withTitleTimeout,
  withToolStatus,
} from "./agentAdapter.ts"
import * as path from "node:path"
import * as ClaudeHooks from "./claudeHooks.ts"
import * as ClaudeItems from "./claudeItems.ts"
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
//     transport defects. SUCCESS results derive no activity (ATC-158): the
//     SDK emits extra results for background wake turns and can hold a
//     turn's result while background work runs (probed 2026-08-10), so
//     activity flows from hook-level evidence and session_state_changed.
//   - Activity is the aggregate of the session tree: each session owns a
//     ClaudeHooks.ActivityTracker fed by the in-process hook callbacks,
//     session_state_changed (root loop), and background_tasks_changed
//     (level snapshot); what the feed carries is always the reducer's
//     aggregate, so a root Stop with live background work stays `working`
//     and the last SubagentStop lands the `idle` transition.
//   - Known residual: the SDK stream carries no correlation between a
//     `result` and the input that caused it, so a background wake-turn's
//     held result flushing just after a client startTurn is paired with
//     that client turn (premature turnCompleted). Any discriminator would
//     be a guess; activity stays truthful either way because results no
//     longer derive it.
//   - A turn that ends in anything but success ends the connection (the
//     interrupted/failed outcome is emitted first); callers re-resume.
//     Success keeps the held query open for further turns.
//   - The SDK is the one sanctioned exception to two repo invariants: it
//     spawns the claude child itself (not via Subprocess) and the child
//     env is built from process.env directly — the SDK owns that process,
//     and the env must carry the user's credentials verbatim.
//   - Conversation items (ATC-193): the query runs with
//     includePartialMessages, so assistant text and thinking stream as
//     content blocks — itemStarted at content_block_start (keyed by the API
//     message id + block index), textDelta per delta, itemCompleted at
//     content_block_stop; the SDK's per-block `assistant` messages are then
//     used only for tool_use blocks (a message whose id never streamed still
//     yields its text from them). tool_result user messages complete the
//     tool items; the mapping itself is claudeItems.ts. The user's own prompt
//     is emitted at startTurn (the SDK never echoes it): the one item this
//     adapter keys itself, `${turnId}:prompt`.
//   - canUseTool callbacks PARK (the promise stays pending) as
//     ThreadRequests until `respond` resolves them; a turn's end or the
//     session's close resolves any survivor with a deny so the SDK child
//     never waits on a request nothing will answer.
//   - One live `claude` process per thread (ATC-203). Claude Code has no
//     shared brain: two live processes on one session id FORK it — each
//     holds its conversation in memory and appends with its own leaf, and
//     neither sees the other's turns. So a session is driven by exactly one
//     process at a time, owned by the active surface — the SDK child this
//     adapter opens with `query()`, or the TUI launched from the launch spec
//     below (`sharedServer: false` tells the Thread runtime to hand off
//     between them: it ends one before starting the other, and it never
//     ends a process it did not start). History is therefore read through
//     the SDK's `getSessionMessages` (the official surface — no direct
//     JSONL reads); with a single writer there is one leaf, so it is
//     truthful. Threads that forked under the old adapter resume from
//     their last-written leaf; the orphaned branch is history the model
//     will not see (one-time, no compatibility path).
//   - End a TUI only after its last turn is on disk: Claude appends a
//     turn's assistant line AFTER its Stop hook has run, so a read at the
//     observed idle settles briefly while the newest turn is still
//     output-less (readHistory) — that bounded settle is the gate the
//     runtime runs before ending a TUI, and the only wait this adapter
//     keeps.
//   - One writer per session id, enforced here. Webhook hook activity is
//     NOT bridged into these feeds: a TUI-driven session has no adapter
//     connection by definition, and while ATC drives, the same vocabulary
//     already arrives as in-process SDK callbacks — a webhook delivery for
//     a live connection could only be stale or spoofed. TUI-driven activity
//     flows through observeSession below, which is the claudeHooks
//     subscriber; consumers stay above the seam. That feed also carries the
//     TUI's submitted prompt as an observed `userMessage` item (the seam's
//     one hooks-carried item), so a Chat reader shows what the TUI is
//     working on before the turn's re-read lands.

/** The Claude Code version this adapter was validated against. */
const CLAUDE_TESTED_VERSION = "2.1.235"

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
  /** The getSessionMessages seam, injectable so tests can script transcripts. */
  readonly sessionMessagesFn?: typeof getSessionMessages
  /** How long create/first-turn waits for the SDK's identity evidence. */
  readonly initTimeout?: Duration.Input
  /** Pause between readHistory's settle re-reads (see the header). */
  readonly historySettleDelay?: Duration.Input
}

/** How many settle re-reads readHistory allows before taking the transcript as it is. */
const HISTORY_SETTLE_ATTEMPTS = 8

// The thread's opaque providerMetadata, as this adapter mints it. Decoded
// tolerantly: unknown or malformed metadata is "no secret", never an error.
const ProviderMetadata = Schema.fromJsonString(
  Schema.Struct({ hookSecret: Schema.optional(Schema.String) }),
)
const decodeProviderMetadata = Schema.decodeUnknownOption(ProviderMetadata)

const {
  unavailable,
  protocolError,
  identityMismatch,
  connectionClosed,
  turnStillActive,
  staleTurn,
  writerConnected,
  unknownRequest,
  answerKindMismatch,
} = providerErrors("claude")

/**
 * Environment for the SDK child: the user's environment (credentials) plus
 * the state-events opt-in, minus the shared nested-session markers
 * (agentAdapter.ts) a dev-mode ATC would otherwise pass down.
 */
const claudeEnvironment = (): Record<string, string> => {
  const env = Subprocess.inheritedEnv(NESTED_SESSION_ENV_VARIABLES)
  env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
  return env
}

/**
 * Every Claude TUI opts into Remote Control as part of the same interactive
 * driver. An ineligible account still gets the normal TUI with Claude's own
 * failure notification, so remote availability never becomes a launch gate.
 *
 * `--remote-control` takes an optional session name, so it must stay the
 * last token in argv — anything appended after it would be swallowed as
 * the name.
 */
const claudeTuiCommand = (
  executable: string,
  sessionFlag: "--resume" | "--session-id",
  providerSessionId: string,
  settingsFile: string,
): ReadonlyArray<string> => [
  executable,
  sessionFlag,
  providerSessionId,
  "--settings",
  settingsFile,
  "--remote-control",
]

/** The hook vocabulary delivered in-process while ATC drives (and via the
 * webhook while a TUI drives — same normalization, claudeHooks.ts). The
 * subagent/task lifecycle events carry the background evidence the
 * aggregate tracker needs (ATC-158). */
const HOOK_EVENTS: ReadonlyArray<HookEvent> = [
  "SessionStart",
  "SessionEnd",
  "UserPromptSubmit",
  "PreToolUse",
  "PostToolUse",
  "PostToolUseFailure",
  "Stop",
  "StopFailure",
  "SubagentStart",
  "SubagentStop",
  "TaskCreated",
  "TaskCompleted",
  "Notification",
  "PermissionRequest",
  "PermissionDenied",
]

const userMessage = (text: string): SDKUserMessage => ({
  type: "user",
  message: { role: "user", content: text },
  parent_tool_use_id: null,
})

/**
 * Labeled plain-text lines from a transcript read (ATC-190): root user and
 * assistant text blocks only — tool payloads, tool results, and system
 * entries carry no title signal, and tool-nested messages are dispatch,
 * not conversation.
 */
const transcriptLines = (messages: ReadonlyArray<SessionMessage>): Array<string> => {
  const lines: Array<string> = []
  for (const entry of messages) {
    if (
      (entry.type !== "user" && entry.type !== "assistant") ||
      entry.parent_tool_use_id !== null
    ) {
      continue
    }
    const content = (entry.message as { content?: unknown } | null)?.content
    const text =
      typeof content === "string"
        ? content
        : Array.isArray(content)
          ? content
              .flatMap((block) => {
                const candidate = block as { type?: unknown; text?: unknown }
                return candidate.type === "text" && typeof candidate.text === "string"
                  ? [candidate.text]
                  : []
              })
              .join("\n")
          : ""
    if (text.trim() !== "") lines.push(`${entry.type}: ${text.trim()}`)
  }
  return lines
}

type GateError = AgentResumeFailed | AgentIdentityMismatch | AgentProtocolError

/** A parked canUseTool: what the SDK asked, and how to answer it. */
interface PendingRequest {
  readonly request: ThreadRequest
  readonly input: Record<string, unknown>
  readonly suggestions: ReadonlyArray<PermissionUpdate>
  readonly resolve: (result: PermissionResult) => void
}

/** One API message being streamed (per root/subagent lane): its id and the
 * text/thinking blocks opened so far, by block index. */
interface StreamingMessage {
  readonly messageId: string
  readonly parentItemId: string | null
  readonly blocks: Map<number, ThreadItem>
}

interface LiveSession {
  /** The id resume promised (null for create — any id is acceptable). */
  readonly expectedSessionId: string | null
  readonly cwd: string
  readonly queue: Queue.Queue<AgentEvent, AgentProtocolError | Cause.Done>
  readonly abort: AbortController
  /** Aggregate session-tree activity state (ATC-158). */
  readonly tracker: ClaudeHooks.ActivityTracker
  readonly pushInput: (text: string) => void
  readonly closeInput: () => void
  readonly initGate: Deferred.Deferred<void, GateError>
  interrupt: () => Promise<unknown>
  sessionId: string | null
  activity: AgentActivity
  activeTurn: string | null
  interruptRequested: boolean
  /** The active turn's TOOL items by id (latest state): a tool_result or
   * approval flip needs the item it completes. Text is never retained. */
  readonly toolItems: Map<string, ThreadItem>
  /** In-flight streamed messages by lane (parent_tool_use_id, "" = root). */
  readonly streaming: Map<string, StreamingMessage>
  /** API message ids whose text arrived through the stream — their
   * `assistant` messages contribute tool_use blocks only. */
  readonly streamedMessages: Set<string>
  /** tool_use ids whose canUseTool landed before the tool_use block. */
  readonly pendingTools: Set<string>
  readonly pendingRequests: Map<string, PendingRequest>
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
      const sessionMessagesFn = adapterOptions.sessionMessagesFn ?? getSessionMessages
      const initTimeout = adapterOptions.initTimeout ?? "60 seconds"
      const historySettleDelay = adapterOptions.historySettleDelay ?? "250 millis"

      /** Live sessions by verified (and, for resumes, expected) session id. */
      const sessions = new Map<string, LiveSession>()

      const emit = (session: LiveSession, event: AgentEvent): void =>
        emitAgentEvent(session.queue, event, protocolError)

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

      /** Publish an item on the feed; tool items are also retained. */
      const emitItem = (
        session: LiveSession,
        type: "itemStarted" | "itemUpdated" | "itemCompleted",
        item: ThreadItem,
      ): void => {
        if ("status" in item) session.toolItems.set(item.id, item)
        emit(session, { type, item })
      }

      /** Flip a tool item pending ↔ running around its approval. */
      const restatusItem = (
        session: LiveSession,
        itemId: string,
        status: "pending" | "running",
      ): void => {
        const item = session.toolItems.get(itemId)
        const updated = item === undefined ? null : withToolStatus(item, status)
        if (updated !== null) emitItem(session, "itemUpdated", updated)
      }

      /** Answer every parked request with a deny and close it (turn end,
       * session close): the SDK child must never wait on a request nothing
       * will answer. */
      const closePendingRequests = (session: LiveSession, reason: string): void => {
        for (const [requestId, pending] of session.pendingRequests) {
          pending.resolve({ behavior: "deny", message: reason })
          emit(session, { type: "requestClosed", requestId })
        }
        session.pendingRequests.clear()
        session.pendingTools.clear()
      }

      /** Complete every block still streaming (a lane replaced mid-block, or
       * a turn cut short): a `complete: false` item must never outlive the
       * evidence that could finish it. */
      const flushStreaming = (session: LiveSession): void => {
        for (const streaming of session.streaming.values()) {
          for (const block of streaming.blocks.values()) {
            if (block.type === "assistantText" || block.type === "reasoning") {
              emitItem(session, "itemCompleted", { ...block, complete: true })
            }
          }
        }
        session.streaming.clear()
      }

      /** A turn ended: its parked requests and item bookkeeping go with it. */
      const endTurnState = (session: LiveSession): void => {
        closePendingRequests(session, "the turn ended")
        flushStreaming(session)
        session.toolItems.clear()
        session.streamedMessages.clear()
      }

      const closeSession = (session: LiveSession, failure?: AgentProtocolError): void => {
        if (session.closed) return
        closePendingRequests(session, "the session closed")
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

      const handleStreamEvent = (
        session: LiveSession,
        message: { readonly event: unknown; readonly parent_tool_use_id: string | null },
      ): void => {
        const turnId = session.activeTurn
        if (turnId === null) return
        const lane = message.parent_tool_use_id ?? ""
        const event = message.event as {
          readonly type?: unknown
          readonly index?: unknown
          readonly message?: { readonly id?: unknown }
          readonly content_block?: { readonly type?: unknown }
          readonly delta?: {
            readonly type?: unknown
            readonly text?: unknown
            readonly thinking?: unknown
          }
        }
        if (event.type === "message_start" && typeof event.message?.id === "string") {
          // A lane replaced mid-block completes what it still had open.
          const previous = session.streaming.get(lane)
          if (previous !== undefined) {
            for (const block of previous.blocks.values()) {
              if (block.type === "assistantText" || block.type === "reasoning") {
                emitItem(session, "itemCompleted", { ...block, complete: true })
              }
            }
          }
          session.streaming.set(lane, {
            messageId: event.message.id,
            parentItemId: message.parent_tool_use_id,
            blocks: new Map(),
          })
          session.streamedMessages.add(event.message.id)
          return
        }
        const streaming = session.streaming.get(lane)
        if (streaming === undefined || typeof event.index !== "number") return
        if (event.type === "content_block_start") {
          const kind = event.content_block?.type
          if (kind !== "text" && kind !== "thinking") return
          const item = ClaudeItems.textItem(
            `${streaming.messageId}:${event.index}`,
            turnId,
            kind,
            "",
            false,
            streaming.parentItemId,
          )
          streaming.blocks.set(event.index, item)
          emitItem(session, "itemStarted", item)
          return
        }
        const block = streaming.blocks.get(event.index)
        if (block === undefined || (block.type !== "assistantText" && block.type !== "reasoning")) {
          return
        }
        if (event.type === "content_block_delta") {
          const delta =
            event.delta?.type === "text_delta" && typeof event.delta.text === "string"
              ? event.delta.text
              : event.delta?.type === "thinking_delta" && typeof event.delta.thinking === "string"
                ? event.delta.thinking
                : null
          if (delta === null) return
          streaming.blocks.set(event.index, { ...block, text: block.text + delta })
          emit(session, { type: "textDelta", itemId: block.id, delta })
          return
        }
        if (event.type === "content_block_stop") {
          streaming.blocks.delete(event.index)
          emitItem(session, "itemCompleted", { ...block, complete: true })
        }
      }

      const handleAssistantMessage = (
        session: LiveSession,
        message: {
          readonly uuid: string
          readonly message: { readonly id?: unknown; readonly content?: unknown }
          readonly parent_tool_use_id: string | null
        },
      ): void => {
        const turnId = session.activeTurn
        if (turnId === null) return
        const streamed =
          typeof message.message.id === "string" && session.streamedMessages.has(message.message.id)
        ClaudeItems.contentBlocks(message.message.content).forEach((block, index) => {
          if (block.type === "tool_use") {
            const pending = session.pendingTools.delete(block.id)
            emitItem(
              session,
              "itemStarted",
              ClaudeItems.toolItem(
                block,
                turnId,
                message.parent_tool_use_id,
                pending ? "pending" : "running",
              ),
            )
            return
          }
          if (streamed || block.type === "tool_result") return
          // Already complete: one event, like a history item.
          emitItem(
            session,
            "itemCompleted",
            ClaudeItems.textItem(
              `${message.uuid}:${index}`,
              turnId,
              block.type,
              block.type === "text" ? block.text : block.thinking,
              true,
              message.parent_tool_use_id,
            ),
          )
        })
      }

      const handleUserMessage = (
        session: LiveSession,
        message: {
          readonly message: { readonly content?: unknown }
          readonly tool_use_result?: unknown
        },
      ): void => {
        for (const block of ClaudeItems.contentBlocks(message.message.content)) {
          if (block.type !== "tool_result") continue
          const item = session.toolItems.get(block.tool_use_id)
          if (item === undefined) continue
          emitItem(
            session,
            "itemCompleted",
            ClaudeItems.completeToolItem(item, block, message.tool_use_result),
          )
        }
      }

      const handleMessage = (session: LiveSession, message: SDKMessage): void => {
        if (session.closed) return
        if (message.type === "stream_event") return handleStreamEvent(session, message)
        if (message.type === "assistant") return handleAssistantMessage(session, message)
        if (message.type === "user") return handleUserMessage(session, message)
        if (message.type === "system" && message.subtype === "compact_boundary") {
          const turnId = session.activeTurn
          const uuid = (message as { uuid?: unknown }).uuid
          if (turnId !== null && typeof uuid === "string") {
            emitItem(session, "itemCompleted", {
              type: "compaction",
              id: uuid,
              turnId,
              providerMetadata: { claude: message.compact_metadata },
            })
          }
          return
        }
        if (message.type === "system" && message.subtype === "init") {
          if (
            session.expectedSessionId !== null &&
            message.session_id !== session.expectedSessionId
          ) {
            return failGate(
              session,
              identityMismatch("sessionId", session.expectedSessionId, message.session_id),
            )
          }
          if (!samePath(message.cwd, session.cwd)) {
            return failGate(session, identityMismatch("cwd", session.cwd, message.cwd))
          }
          session.sessionId = message.session_id
          if (!sessions.has(message.session_id)) sessions.set(message.session_id, session)
          Deferred.doneUnsafe(session.initGate, Effect.void)
          return
        }
        if (message.type === "system" && message.subtype === "session_state_changed") {
          // Root-loop evidence only: the emitted activity is the tracker's
          // aggregate, so live background work keeps the session busy.
          const state = (message as { state?: unknown }).state
          if (state === "running") setActivity(session, session.tracker.setRoot("working"))
          if (state === "idle") setActivity(session, session.tracker.setRoot("idle"))
          if (state === "requires_action") {
            setActivity(session, session.tracker.setRoot("needs_input"))
          }
          return
        }
        if (message.type === "system" && message.subtype === "background_tasks_changed") {
          // The SDK's level snapshot of in-flight background work; task_id
          // matches the hook snapshots' id vocabulary (probed 2026-08-10).
          const tasks = (message as { tasks?: unknown }).tasks
          const ids = Array.isArray(tasks)
            ? tasks.flatMap((task) => {
                const id = (task as { task_id?: unknown }).task_id
                return typeof id === "string" ? [id] : []
              })
            : []
          setActivity(session, session.tracker.replaceBackground(ids))
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
          endTurnState(session)
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
          // A success result derives NO activity (ATC-158): wake-turn and
          // held-back results would clobber live background evidence; the
          // hooks and session_state_changed carry the truth. Error-path
          // idle still derives from the result itself (probe finding:
          // Stop/StopFailure do not fire here) — the session is over.
          if (outcome !== "completed") {
            setActivity(session, "idle")
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
              endTurnState(session)
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
        (toolName, input, options) =>
          new Promise<PermissionResult>((resolve) => {
            // Activity is NOT touched here: requires_action state events
            // carry that truthfully. The request parks until `respond`
            // resolves it, or the turn/session ends (closePendingRequests).
            if (session.closed || session.activeTurn === null) {
              resolve({ behavior: "deny", message: "no turn is accepting requests" })
              return
            }
            const request = ClaudeItems.mapRequest(
              toolName,
              input,
              options,
              session.activeTurn,
              new Date().toISOString(),
            )
            session.pendingRequests.set(options.requestId, {
              request,
              input,
              suggestions: options.suggestions ?? [],
              resolve,
            })
            // The tool_use block may land before or after its permission ask.
            if (session.toolItems.has(options.toolUseID)) {
              restatusItem(session, options.toolUseID, "pending")
            } else {
              session.pendingTools.add(options.toolUseID)
            }
            emit(session, { type: "requestOpened", request })
          })

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
                    const activity = session.tracker.update(
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

      const versionCheck = yield* makeVersionGate(
        subprocess,
        "claude",
        config.claudeExecutable,
        CLAUDE_TESTED_VERSION,
      )

      // The launch preamble every operation shares: resolve the executable
      // (its failure IS the actionable diagnostic) and run the drift gate.
      const resolvedExecutable = Effect.gen(function* () {
        const executable = yield* resolveProviderExecutable("claude", config.claudeExecutable)
        yield* versionCheck
        return executable
      })

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
        return decodeProviderMetadata(metadata).pipe(
          Option.match({
            onNone: () => null,
            onSome: (parsed) => parsed.hookSecret ?? null,
          }),
        )
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
            Effect.mapError((error) =>
              unavailable(`could not write the hook settings files: ${error.message}`),
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
          const executable = yield* resolvedExecutable
          // The held query() drains this queue as its async input iterable;
          // push feeds it more turns, end is EOF. The iterable drives its
          // pulls on a detached fiber reclaimed only when the queue ends —
          // closeSession's closeInput (which every exit path reaches via
          // the acquireRelease below) is what keeps it from parking on an
          // empty queue forever.
          const inputQueue = yield* Queue.make<SDKUserMessage, Cause.Done>()
          // Bounded (overflow fails the stream — see emit); a session that
          // buffers 256 undrained events has lost its consumer.
          const queue = yield* Queue.make<AgentEvent, AgentProtocolError | Cause.Done>({
            capacity: 256,
          })
          const initGate = yield* Deferred.make<void, GateError>()
          const session: LiveSession = {
            expectedSessionId: options.resume ?? null,
            cwd: options.cwd,
            queue,
            abort: new AbortController(),
            tracker: ClaudeHooks.makeActivityTracker(),
            pushInput: (text) => {
              Queue.offerUnsafe(inputQueue, userMessage(text))
            },
            closeInput: () => {
              Queue.endUnsafe(inputQueue)
            },
            initGate,
            interrupt: () => Promise.resolve(),
            sessionId: null,
            activity: "unknown",
            activeTurn: null,
            interruptRequested: false,
            toolItems: new Map(),
            streaming: new Map(),
            streamedMessages: new Set(),
            pendingTools: new Set(),
            pendingRequests: new Map(),
            closed: false,
            over: false,
          }
          // Registered before queryFn can fail (an uncleaned registry entry
          // would lock the session id out of resume for the process's life),
          // with the single-writer check INSIDE the same sync acquire so no
          // interleaving can split check from claim, and the close finalizer
          // armed atomically with it (acquireRelease — ATC-167 M4).
          const claim = yield* Effect.acquireRelease(
            Effect.sync(() => {
              if (options.resume !== undefined) {
                if (sessions.has(options.resume)) return { conflict: options.resume }
                sessions.set(options.resume, session)
              }
              return "claimed" as const
            }),
            (outcome) =>
              Effect.sync(() => {
                if (outcome === "claimed") closeSession(session)
              }),
          )
          if (claim !== "claimed") {
            return yield* Effect.fail(writerConnected(claim.conflict))
          }
          const handle = queryFn({
            prompt: yield* Stream.toAsyncIterableEffect(Stream.fromQueue(inputQueue)),
            options: {
              abortController: session.abort,
              cwd: options.cwd,
              env: claudeEnvironment(),
              pathToClaudeCodeExecutable: executable,
              ...(options.resume !== undefined ? { resume: options.resume } : {}),
              settingSources: [],
              permissionMode: "default",
              persistSession: true,
              includePartialMessages: true,
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

      /**
       * Open a turn on the feed: turnStarted, then the user's prompt as the
       * turn's first item (the SDK never echoes it), then the input itself.
       * Turn ids are uuids — the Thread runtime persists them, so a process-local
       * counter would collide with an earlier process's turns.
       */
      const beginTurn = (session: LiveSession, text: string): string => {
        const turnId = `claude-turn-${crypto.randomUUID()}`
        session.activeTurn = turnId
        // Emitted before anything can await: the consume fiber may deliver
        // this turn's result on the very next microtask, and turnStarted
        // must precede turnCompleted on the feed.
        emit(session, { type: "turnStarted", turnId })
        emitItem(session, "itemCompleted", {
          type: "userMessage",
          id: `${turnId}:prompt`,
          turnId,
          text,
        })
        session.pushInput(text)
        return turnId
      }

      /** The bounded transcript read behind both readers (getSessionMessages). */
      const readTranscript = (
        providerSessionId: string,
        cwd: string,
      ): Effect.Effect<ReadonlyArray<SessionMessage>, string> =>
        Effect.tryPromise({
          try: () => sessionMessagesFn(providerSessionId, { dir: cwd }),
          catch: (error) => (error instanceof Error ? error.message : String(error)),
        }).pipe(
          Effect.timeoutOrElse({
            duration: "10 seconds",
            orElse: () => Effect.fail("the transcript read timed out"),
          }),
        )

      /**
       * readHistory's read: re-read while the newest turn holds nothing but
       * its prompt, because Claude appends the assistant line after the Stop
       * hook that triggers the observed-idle read (bounded — a turn that
       * really produced nothing simply reads as such after the last try).
       */
      const readSettledHistory = (
        providerSessionId: string,
        cwd: string,
        attempt = 0,
      ): Effect.Effect<ReadonlyArray<HistoryTurn>, string> =>
        readTranscript(providerSessionId, cwd).pipe(
          Effect.map(ClaudeItems.mapHistory),
          Effect.flatMap((history) => {
            const newest = history.at(-1)
            const settled = newest === undefined || newest.items.length > 1
            return settled || attempt >= HISTORY_SETTLE_ATTEMPTS
              ? Effect.succeed(history)
              : Effect.sleep(historySettleDelay).pipe(
                  Effect.andThen(readSettledHistory(providerSessionId, cwd, attempt + 1)),
                )
          }),
        )

      const makeConnection = (session: LiveSession): AgentConnection => {
        // The seam's uniform rule: a session that ended underneath the
        // caller is AgentUnavailable ("resume it"); a handle the caller
        // already closed is a conflict.
        const requireOpen = Effect.suspend(
          (): Effect.Effect<void, AgentConflict | AgentUnavailable> =>
            session.over
              ? Effect.fail(unavailable("the session ended; resume it for a fresh connection"))
              : session.closed
                ? Effect.fail(connectionClosed())
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
                return yield* Effect.fail(turnStillActive(session.activeTurn))
              }
              const turnId = beginTurn(session, text)
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
                return yield* Effect.fail(staleTurn(turn.turnId))
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
          respond: (requestId, answer) =>
            Effect.gen(function* () {
              yield* requireOpen
              const pending = session.pendingRequests.get(requestId)
              if (pending === undefined) return yield* Effect.fail(unknownRequest(requestId))
              if (pending.request.kind !== answer.kind) {
                return yield* Effect.fail(answerKindMismatch(requestId, pending.request.kind))
              }
              session.pendingRequests.delete(requestId)
              // A cancel is a deny that also interrupts; the SDK carries the
              // interrupt, and the outcome must read as interrupted.
              if (answer.kind === "approval" && answer.decision === "cancel") {
                session.interruptRequested = true
              }
              pending.resolve(
                ClaudeItems.permissionResult(
                  pending.request,
                  answer,
                  pending.input,
                  pending.suggestions,
                ),
              )
              if (pending.request.itemId !== undefined) {
                // Answered before its tool_use block landed: the block must
                // not start pending against a request that is already gone.
                session.pendingTools.delete(pending.request.itemId)
                restatusItem(session, pending.request.itemId, "running")
              }
              emit(session, { type: "requestClosed", requestId })
            }),
        }
      }

      const adapter: AgentAdapter = {
        provider: "claude",
        sharedServer: false,
        createSession: (options) =>
          Effect.gen(function* () {
            const session = yield* openSession({ cwd: options.cwd })
            const turnId = beginTurn(session, options.input)
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
            const executable = yield* resolvedExecutable
            const { secret, settingsFile } = yield* ensureHookPlumbing(
              options.providerSessionId,
              options.providerMetadata,
            )
            return {
              launchSpec: {
                command: claudeTuiCommand(
                  executable,
                  "--resume",
                  options.providerSessionId,
                  settingsFile,
                ),
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
            const executable = yield* resolvedExecutable
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
                command: claudeTuiCommand(
                  executable,
                  "--session-id",
                  providerSessionId,
                  settingsFile,
                ),
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
            // Bounded (overflow ends the feed — see the subscriber below);
            // dropping hook events silently would lose confirm/auto-name
            // evidence, while an ended feed makes the consumer re-observe.
            const queue = yield* Queue.make<AgentSessionEvent, Cause.Done>({ capacity: 32 })
            // Registered before the subscription so it runs after it on
            // scope close: unsubscribe first, then end the queue.
            yield* Effect.addFinalizer(() => Effect.sync(() => Queue.endUnsafe(queue)))
            const offer = (event: AgentSessionEvent): void => {
              if (!Queue.offerUnsafe(queue, event)) Queue.endUnsafe(queue)
            }
            yield* hooks.subscribe((sessionId, event) => {
              if (sessionId !== options.providerSessionId) return
              offer(event)
              // The TUI's prompt as an observed item (see the header): its
              // own turn id, replaced wholesale by the re-read at the
              // turn's end.
              if (event.type === "userPrompt") {
                const turnId = `claude-observed-${crypto.randomUUID()}`
                offer({
                  type: "itemCompleted",
                  item: { type: "userMessage", id: `${turnId}:prompt`, turnId, text: event.text },
                })
              }
            })
            return Stream.fromQueue(queue)
          }),
        generateTitle: (options) =>
          Effect.scoped(
            Effect.gen(function* () {
              const executable = yield* resolvedExecutable
              // The smoke-test one-shot shape (smoke.ts claudeRoundTrip):
              // one turn, no tools, nothing persisted — plus a haiku-class
              // model, since a title needs speed, not depth. Strict MCP
              // keeps the project's own .mcp.json out of an unattended run.
              const abort = new AbortController()
              yield* Effect.addFinalizer(() => Effect.sync(() => abort.abort()))
              const prompt = yield* Stream.toAsyncIterableEffect(
                Stream.make(userMessage(titleInstruction(options.prompt, options.refine))),
              )
              // Wrapped so a synchronous SDK setup throw is the promised
              // typed error, not a defect.
              const handle = yield* Effect.try({
                try: () =>
                  queryFn({
                    prompt,
                    options: {
                      abortController: abort,
                      cwd: options.cwd,
                      env: claudeEnvironment(),
                      pathToClaudeCodeExecutable: executable,
                      settingSources: [],
                      strictMcpConfig: true,
                      permissionMode: "dontAsk",
                      tools: [],
                      maxTurns: 1,
                      persistSession: false,
                      model: "haiku",
                    },
                  }),
                catch: (error) =>
                  protocolError(
                    `title generation failed to start: ${error instanceof Error ? error.message : String(error)}`,
                  ),
              })
              return yield* Effect.tryPromise({
                // Stop at the FIRST result: the scope's abort (not iterator
                // exhaustion) ends the query, so a fixture or SDK that holds
                // the stream open cannot hang the one-shot.
                try: async () => {
                  for await (const message of handle) {
                    if (message.type !== "result") continue
                    if (message.subtype !== "success") {
                      const errors = (message as { errors?: ReadonlyArray<string> }).errors ?? []
                      throw new Error(`the turn ended ${message.subtype}: ${errors.join("; ")}`)
                    }
                    return (message as { result?: string }).result ?? ""
                  }
                  throw new Error("the stream ended without a result")
                },
                catch: (error) =>
                  protocolError(
                    `title generation failed: ${error instanceof Error ? error.message : String(error)}`,
                  ),
              }).pipe(withTitleTimeout(protocolError))
            }),
          ),
        // On-demand transcript read via the SDK (ATC-190) — the conversation
        // the primary agent already produced; nothing is retained here.
        // Best-effort by
        // contract: any read failure — a hung read included, hence the
        // bound — is debug-logged and reads as "nothing usable".
        collectTitleContext: (options) =>
          readTranscript(options.providerSessionId, options.cwd).pipe(
            Effect.map(transcriptLines),
            Effect.map(buildTitleContext),
            Effect.catch((reason) =>
              Effect.logDebug("claude title-context read failed").pipe(
                Effect.annotateLogs({
                  providerSessionId: options.providerSessionId,
                  reason,
                }),
                Effect.as(null),
              ),
            ),
          ),
        // The same read, normalized (claudeItems.ts). An unknown session
        // reads as empty — the SDK cannot tell "no such session" from "no
        // messages yet"; only a failing read is an error.
        readHistory: (options) =>
          readSettledHistory(options.providerSessionId, options.cwd).pipe(
            Effect.mapError((reason) => protocolError(`the transcript read failed: ${reason}`)),
          ),
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
