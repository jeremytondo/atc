import { Cause, Duration, Effect, Queue, Schema, Semaphore, Stream } from "effect"
import type { Scope } from "effect"
import { existsSync, realpathSync } from "node:fs"
import { basename } from "node:path"
import type { AgentId } from "../api/contract.ts"
import type * as Contract from "../api/contract.ts"
import type { Subprocess } from "../platform/subprocess.ts"
import { resolveExecutable } from "../platform/subprocess.ts"

// The shared provider seam (ATC-123): how ATC drives coding-agent providers
// (Codex app-server, Claude Agent SDK). Adapters own provider transports,
// wire shapes, and version details; everything above the seam sees only
// stable provider identity, exact-session create/resume, truthful normalized
// status, and safe control flow. Invariants every adapter must hold:
//
//   - Create and resume are distinct; neither ever falls back to the other.
//     Resume verifies the exact provider session id and working directory
//     from provider evidence and fails closed on any mismatch — a changed
//     identity is never adopted as a repair.
//   - Exactly one active writer per provider session. Connections are
//     writers; observation never opens a second writer.
//   - Status is derived from structured provider events only — never from
//     transcript or terminal-output parsing.
//   - Conversation content crosses the seam in the ONE contract vocabulary
//     (ThreadItem / ThreadTurn / ThreadRequest, contract.ts — ATC-193):
//     an adapter's job is provider wire shape → those types, full stop.
//     There is no seam-private content vocabulary and no mapping layer
//     above the adapters. Item ids are the provider's stable ids where it
//     has them, otherwise adapter-derived, unique within a thread, and
//     never minted above the seam.
//   - Provider requests (approvals, questions) are surfaced as
//     `requestOpened` and PARKED until `respond` answers them — no timeout,
//     no auto-reject; the request dies with its turn (closed on
//     turnCompleted) or its connection. Requests raised for a turn another
//     writer started are never ours to answer.
//   - Settings (ATC-205) cross the seam in the contract's ThreadSettings
//     vocabulary. The caller's settings are what a turn RUNS with:
//     `startTurn` (and `createSession`) take them, and the adapter pushes
//     only what differs from the live session's own values — an unchanged
//     turn costs no extra provider round-trip. The provider's word on its
//     session settings — a change made in its TUI or another client, or what
//     a fresh process actually applied — comes back as a `settings` event,
//     partial (only what the provider reported), and the caller adopts it.
//     The access ladder and the plan/chat mode are translated INSIDE the
//     adapters (Claude permissionMode; Codex approvalPolicy + sandbox +
//     collaborationMode); no rung name leaks below the seam and no provider
//     knob leaks above it.
//
// The fake adapter for tests lives in test/agents/fakeAgentAdapter.ts; per-provider
// service tags live with their adapters (codexAdapter.ts, claudeAdapter.ts).

export const AGENT_PROVIDERS = ["codex", "claude"] as const

export type AgentProvider = (typeof AGENT_PROVIDERS)[number]

/**
 * Public registry slug (contract `AgentId`) → seam provider. The Record
 * type makes an unmapped slug a compile error, so the public vocabulary
 * and the seam's can never drift apart silently.
 */
export const PROVIDER_FOR_AGENT: Record<typeof AgentId.Type, AgentProvider> = {
  codex: "codex",
  "claude-code": "claude",
}

/**
 * Nested-session environment markers, scrubbed wherever ATC launches an
 * agent process or TUI (uniformly — not per provider): inherited from a
 * dev-mode ATC running inside a session, they silently change provider
 * behavior (e.g. disable Claude transcript persistence, which breaks
 * resume).
 */
export const NESTED_SESSION_ENV_VARIABLES = [
  "CLAUDE_CODE_CHILD_SESSION",
  "CLAUDECODE",
  "CLAUDE_CODE_ENTRYPOINT",
  "CLAUDE_CODE_SSE_PORT",
  "CLAUDE_CODE_SESSION_ID",
  "CLAUDE_CODE_BRIDGE_SESSION_ID",
  "CLAUDE_PID",
] as const

/**
 * Normalized activity for one provider session. `unknown` means no evidence
 * — never a guess. A pending provider request always wins over the
 * provider's more general running state (`needs_input` beats `working`).
 */
export type AgentActivity = "idle" | "working" | "needs_input" | "unknown"

/** Busy covers a pending request too: a turn parked on an approval is mid-turn. */
export const isBusyActivity = (activity: AgentActivity): boolean =>
  activity === "working" || activity === "needs_input"

const ACTIVITY_PRECEDENCE: Record<AgentActivity, number> = {
  needs_input: 3,
  working: 2,
  unknown: 1,
  idle: 0,
}

/**
 * Reduce a session tree's activities (ATC-158): the root agent loop plus
 * every tracked background descendant (subagents, backgrounded shells,
 * workflows, pending session crons). Precedence is
 * `needs_input > working > unknown > idle`: any member needing user action
 * wins, any active member keeps the whole tree busy, and `idle` requires
 * EVERY member known-inactive — one unestablished member keeps the
 * aggregate `unknown`, never a guess.
 */
export const aggregateActivity = (
  root: AgentActivity,
  descendants: Iterable<AgentActivity>,
): AgentActivity => {
  let aggregate = root
  for (const activity of descendants) {
    if (ACTIVITY_PRECEDENCE[activity] > ACTIVITY_PRECEDENCE[aggregate]) aggregate = activity
  }
  return aggregate
}

export type AgentTurnOutcome = "completed" | "interrupted" | "failed"

/** The contract vocabulary as the seam speaks it (see the header). */
export type ThreadItem = typeof Contract.ThreadItem.Type
export type ThreadTurn = typeof Contract.ThreadTurn.Type
export type ThreadRequest = typeof Contract.ThreadRequest.Type
export type ThreadRequestAnswer = typeof Contract.ThreadRequestAnswer.Type
export type ApprovalSubject = typeof Contract.ApprovalSubject.Type
export type FileChange = typeof Contract.FileChange.Type
export type ToolStatus = typeof Contract.ToolStatus.Type
export type ThreadSettings = typeof Contract.ThreadSettings.Type
export type ReasoningLevel = typeof Contract.ReasoningLevel.Type
export type ThreadAccess = typeof Contract.ThreadAccess.Type
export type ThreadMode = typeof Contract.ThreadMode.Type
export type AgentModel = typeof Contract.AgentModel.Type
export type ThreadAttachment = typeof Contract.ThreadAttachment.Type

/**
 * What a turn is started with (ATC-216): the user's text and the images it
 * carries, each stored at `path` on this host (attachments.ts). Adapters
 * hand the images to the provider in its own shape — bytes read here and
 * inlined (Claude), or the path for the provider to read (Codex) — and put
 * them on the prompt's `userMessage` item, so the transcript shows what the
 * provider saw.
 */
export interface TurnInput {
  readonly text: string
  readonly attachments: ReadonlyArray<ThreadAttachment>
}

/**
 * What a provider reported about its session's settings (ATC-205): only the
 * fields it spoke about are present; `reasoning: null` is "the model runs
 * with no effort level" (as opposed to "not reported").
 */
export interface ProviderSettings {
  readonly model?: string
  readonly reasoning?: ReasoningLevel | null
  readonly mode?: ThreadMode
  readonly access?: ThreadAccess
}

/** One turn of provider history, as `readHistory` returns it. */
export interface HistoryTurn {
  readonly turn: ThreadTurn
  readonly items: ReadonlyArray<ThreadItem>
}

/**
 * The item lifecycle events shared by both feeds: `itemStarted` opens an
 * item (a streaming text item arrives with `complete: false`), `itemUpdated`
 * re-sends the whole item after a change (a tool flipping to pending, output
 * landing), `itemCompleted` carries its final state. Every event carries the
 * FULL item — consumers upsert by id and never patch.
 */
export type AgentItemEvent =
  | { readonly type: "itemStarted"; readonly item: ThreadItem }
  | { readonly type: "itemUpdated"; readonly item: ThreadItem }
  | { readonly type: "itemCompleted"; readonly item: ThreadItem }

/**
 * One entry of a TUI-driven session's observation feed (observeSession).
 * `userPrompt` is best-effort evidence of a user prompt submitted to the
 * session — adapters emit at least the session's first prompt when their
 * evidence source carries it (Claude: the UserPromptSubmit webhook; Codex:
 * a demand-driven thread/read of the session's preview), and consumers
 * must tolerate duplicates, later prompts, and absence. Item events arrive
 * from a provider whose shared server fans conversation items out to
 * passive observers (Codex), and — the one item a hooks-fed observation
 * (Claude) can carry — the submitted prompt itself, as a completed
 * `userMessage` under an observed turn id, so a Chat reader shows what the
 * TUI is working on; the turn's remaining items reach ATC through
 * `readHistory` at its end.
 */
export type AgentSessionEvent =
  | { readonly type: "activity"; readonly activity: AgentActivity }
  | { readonly type: "userPrompt"; readonly text: string }
  | { readonly type: "settings"; readonly settings: ProviderSettings }
  | AgentItemEvent

/**
 * One entry of the normalized writer feed. Turn ids are provider-native
 * where the provider has them (Codex) and adapter-minted where it does not
 * (Claude) — unique within the thread either way, since the Thread runtime persists
 * them. `turnStarted` always precedes its `turnCompleted`; every item event
 * of a turn falls between the two. `textDelta` is live-only: it extends the
 * text of the `assistantText` / `reasoning` item whose `itemStarted` carried
 * `complete: false`, and is never persisted or replayed. Raw provider events
 * stay inside the adapters (logged there for diagnostics, never re-parsed
 * above the seam).
 */
export type AgentEvent =
  | { readonly type: "activity"; readonly activity: AgentActivity }
  | { readonly type: "turnStarted"; readonly turnId: string }
  | {
      readonly type: "turnCompleted"
      readonly turnId: string
      readonly outcome: AgentTurnOutcome
      /** Human-readable failure/interrupt detail, for diagnostics only. */
      readonly detail?: string
    }
  | AgentItemEvent
  | { readonly type: "textDelta"; readonly itemId: string; readonly delta: string }
  | { readonly type: "requestOpened"; readonly request: ThreadRequest }
  | { readonly type: "requestClosed"; readonly requestId: string }
  /** The provider's word on the session's settings (see the header). */
  | { readonly type: "settings"; readonly settings: ProviderSettings }

/** Correlation handle for exactly one turn — interrupt targets this turn. */
export interface AgentTurn {
  readonly turnId: string
}

/**
 * How to launch the provider's TUI attached to an existing session. The
 * caller owns the actual launch (a Terminal session); the adapter owns argv
 * so provider flags never leak above the seam.
 */
export interface TuiLaunchSpec {
  /** Full argv; argv[0] is the resolved provider executable. */
  readonly command: ReadonlyArray<string>
  /** Environment the TUI needs on top of the terminal's own. */
  readonly env: Readonly<Record<string, string>>
}

/** A resume launch, plus any adapter metadata the launch minted or rotated
 * — the caller must persist it, or state minted here (e.g. a hook secret)
 * dies with the process while the TUI keeps using it. */
export interface TuiLaunch {
  readonly launchSpec: TuiLaunchSpec
  readonly providerMetadata?: string
}

/**
 * A live writer connection to one provider session. Lives in a Scope:
 * closing the scope stops the driver (releasing the writer role) and never
 * deletes provider history.
 *
 * A connection can also end BEFORE its scope closes: transport loss, or a
 * provider whose sessions terminate with a non-success turn (Claude). The
 * uniform contract for consumers: `AgentUnavailable` from a control call
 * means "this session is over — resume it for a fresh connection";
 * `AgentConflict` means the target itself is invalid (busy/stale turn, or
 * a handle the caller already closed).
 */
export interface AgentConnection {
  /**
   * The provider session identity. Exact and verified as soon as provider
   * evidence exists; for a deferred-verification provider (Claude resume)
   * it is the expected id until the first turn verifies it fail-closed.
   */
  readonly providerSessionId: string
  /** Verified canonical working directory. */
  readonly cwd: string
  /** Current normalized activity snapshot. */
  readonly activity: Effect.Effect<AgentActivity>
  /**
   * The normalized status feed. Single-consumer. Ends cleanly when the
   * caller closes the connection scope, or after delivering a terminal
   * `turnCompleted` on a provider whose connections end with non-success
   * turns; FAILS with `AgentProtocolError` when the connection dies with
   * no truthful terminal event (transport loss, failed verification).
   * Fan-out to multiple observers happens above the seam.
   */
  readonly events: Stream.Stream<AgentEvent, AgentProtocolError>
  /**
   * Start exactly one turn with user input, running with `settings`: the
   * adapter pushes whatever differs from the live session's own values
   * before the input goes out (nothing when nothing changed). Fails with
   * `AgentConflict` while another turn is active on this connection;
   * completion arrives on the feed as `turnCompleted`. A provider whose
   * resume verification is deferred (Claude) completes it here on the
   * connection's first turn, so `AgentResumeFailed` /
   * `AgentIdentityMismatch` can surface fail-closed.
   */
  readonly startTurn: (
    input: TurnInput,
    settings: ThreadSettings,
  ) => Effect.Effect<
    AgentTurn,
    | AgentConflict
    | AgentUnavailable
    | AgentResumeFailed
    | AgentIdentityMismatch
    | AgentProtocolError
  >
  /**
   * Interrupt exactly `turn`. A stale or unknown target fails with
   * `AgentConflict` — never "interrupt whatever is active". Success means
   * the provider accepted the interrupt; the truthful outcome is the feed's
   * `turnCompleted` with outcome `interrupted`.
   */
  readonly interrupt: (
    turn: AgentTurn,
  ) => Effect.Effect<void, AgentConflict | AgentUnavailable | AgentProtocolError>
  /**
   * Answer a parked provider request (Codex: reply on the JSON-RPC request;
   * Claude: resolve the parked `canUseTool`). The answer's `kind` must
   * match the request's. Success means the provider took the answer; the
   * feed's `requestClosed` follows. An unknown, already-answered, or
   * mismatched request is `AgentConflict`.
   */
  readonly respond: (
    requestId: string,
    answer: ThreadRequestAnswer,
  ) => Effect.Effect<void, AgentConflict | AgentUnavailable | AgentProtocolError>
}

/** A successful create: the connection plus the already-started first turn. */
export interface AgentSessionStart {
  readonly connection: AgentConnection
  readonly turn: AgentTurn
}

/**
 * Verified identity of a freshly launched TUI session (ATC-124). The
 * optional metadata is an opaque adapter-owned blob the caller persists on
 * the thread and hands back on later seam calls (e.g. the Claude hook
 * secret); the domain never reads inside it.
 */
export interface EstablishedIdentity {
  readonly providerSessionId: string
  /** The verified canonical working directory the caller asked for. */
  readonly cwd: string
  readonly providerMetadata?: string
}

/**
 * What `prepareTuiSession` hands back: how to launch the fresh provider
 * TUI, and one awaitable resolving to the session's verified identity.
 * How identity is established is adapter-internal: Claude pre-assigns an
 * id and resolves immediately; Codex resolves when the launched TUI's
 * `thread/started` is captured (cwd-checked fail closed). Awaiting is the
 * caller's job and must be bounded by the caller.
 */
export interface PreparedTuiSession {
  readonly launchSpec: TuiLaunchSpec
  readonly identity: Effect.Effect<
    EstablishedIdentity,
    AgentUnavailable | AgentIdentityMismatch | AgentProtocolError
  >
}

/**
 * The shared adapter interface. Create requires initial input because both
 * providers establish durable identity while starting a turn (and a Codex
 * thread with zero completed turns is not restart-resumable), so a created
 * session is durable only once its first turn completes.
 */
export interface AgentAdapter {
  readonly provider: AgentProvider
  /**
   * Whether the provider runs one shared server that every process — the
   * TUI, ATC's own connection — attaches to (Codex): observeSession's feed
   * stays authoritative once no TUI drives the session, a turn ATC started
   * survives ATC's restart, and a TUI and a native connection coexist on
   * one session. Without it (Claude) each process holds the session in
   * memory: the hooks feed dies silently with the TUI (a busy state must
   * re-derive through checkSession), and two live processes FORK the
   * session — so the Thread runtime keeps one live process per thread
   * (its surface-ownership rules, threadRuntime.ts) and keeps that one
   * process's writer connection resident across native turns (ATC-207).
   */
  readonly sharedServer: boolean
  /**
   * Create a new provider session in `cwd` and start its first turn. The
   * provider's echoed working directory is verified like resume's — a
   * disagreeing echo is `AgentIdentityMismatch`, never adopted.
   */
  readonly createSession: (options: {
    readonly cwd: string
    readonly input: TurnInput
    readonly settings: ThreadSettings
  }) => Effect.Effect<
    AgentSessionStart,
    AgentUnavailable | AgentConflict | AgentIdentityMismatch | AgentProtocolError,
    Scope.Scope
  >
  /**
   * Resume the exact existing session. Never creates one: an unknown id is
   * `AgentResumeFailed`, and evidence disagreeing with the expected id or
   * cwd is `AgentIdentityMismatch`. Verification uses the earliest provider
   * evidence available: Codex verifies here (thread/resume echoes identity);
   * the Claude SDK emits no identity evidence until a turn starts (probed
   * 2026-08-03), so its verification completes — still fail-closed — at the
   * connection's first `startTurn`. `settings` are what the connection is
   * opened with: a one-process provider (Claude) spawns its child with them
   * so the first turn pushes nothing; a shared-server provider (Codex) reads
   * its baseline from the resume reply instead and lets the first turn push
   * the difference — the resume itself never reconfigures a session.
   */
  readonly resumeSession: (options: {
    readonly providerSessionId: string
    readonly cwd: string
    readonly settings: ThreadSettings
  }) => Effect.Effect<
    AgentConnection,
    | AgentUnavailable
    | AgentConflict
    | AgentResumeFailed
    | AgentIdentityMismatch
    | AgentProtocolError,
    Scope.Scope
  >
  /**
   * How to launch the provider TUI attached to an existing session.
   * `providerMetadata` is deliberately required (pass undefined when the
   * thread has none): forgetting to thread it through would silently
   * rotate adapter state out from under a running TUI.
   *
   * A provider that can probe session existence (Codex, via thread/list)
   * does so here and fails `AgentResumeFailed` when the session is gone —
   * launching blind would die inside the pty with no typed error. A
   * provider that cannot probe (Claude: checkSession is hardwired
   * `unknown`) launches blind, so "the open succeeded but the terminal
   * died within seconds" remains a state clients must expect.
   */
  readonly tuiLaunch: (options: {
    readonly providerSessionId: string
    readonly cwd: string
    readonly providerMetadata: string | undefined
  }) => Effect.Effect<TuiLaunch, AgentUnavailable | AgentResumeFailed>
  /**
   * Prepare a FRESH provider TUI session in `cwd`: the uniform contract
   * behind "provider sessions materialize on first interaction". The Scope
   * owns the adapter's capture/serialization resources (Codex holds the
   * per-provider launch lock until the scope closes, so concurrent
   * launches cannot mis-adopt each other's identity); close it once
   * `identity` has resolved, or to abandon the launch.
   */
  readonly prepareTuiSession: (options: {
    readonly cwd: string
  }) => Effect.Effect<PreparedTuiSession, AgentUnavailable, Scope.Scope>
  /**
   * The one normalized per-session subscription for TUI-driven sessions:
   * activity plus best-effort user-prompt evidence (AgentSessionEvent).
   * Whether it is fed by hook webhooks (Claude) or the shared server's
   * status fan-out (Codex) is adapter-internal. Metadata is handed back so
   * adapters can restore per-session state (hook secrets) across ATC
   * restarts. Scoped: closing unsubscribes. The stream goes silent — never
   * lies — when the adapter loses its evidence source.
   */
  readonly observeSession: (options: {
    readonly providerSessionId: string
    readonly providerMetadata: string | undefined
  }) => Effect.Effect<Stream.Stream<AgentSessionEvent>, AgentUnavailable, Scope.Scope>
  /**
   * The provider's model catalog (ATC-205), normalized: `value` is the id
   * ThreadSettings.model stores and the adapter sends back to the provider;
   * every entry names a real model (Claude's `default` meta-entry is folded
   * into the alias it resolves to). Never a persisted session; the caller
   * caches it.
   */
  readonly listModels: () => Effect.Effect<
    ReadonlyArray<AgentModel>,
    AgentUnavailable | AgentProtocolError
  >
  /**
   * One bounded, session-less completion: a short display title for a
   * thread whose first user prompt is `prompt` (ATC-155). Runs as the
   * provider's cheapest ephemeral invocation — never a persisted provider
   * session, never a writer on any existing one. With `refine` (ATC-190)
   * the same one-shot re-titles from collected conversation context and is
   * told to keep a current title that already fits. Returns the model's
   * raw text; callers apply `sanitizeTitle`. Failures are ordinary typed
   * errors — the caller decides that a missing title is not an error.
   */
  readonly generateTitle: (options: {
    readonly cwd: string
    readonly prompt: string
    readonly refine?: TitleRefinement
  }) => Effect.Effect<string, AgentUnavailable | AgentProtocolError>
  /**
   * Best-effort recent conversation text for one session, feeding the one
   * title refinement (ATC-190): whatever the adapter can reach cheaply —
   * an on-demand transcript read (Claude) or the in-memory retention of
   * observed message items (Codex) — as plain `user:`/`assistant:` lines
   * capped near 8KB (`buildTitleContext`). Null when nothing usable
   * exists. Never fails and never touches provider session state.
   */
  readonly collectTitleContext: (options: {
    readonly providerSessionId: string
    readonly cwd: string
  }) => Effect.Effect<string | null>
  /**
   * The provider's own record of the session's conversation, normalized:
   * every turn with its items, oldest first. Codex reads `thread/resume`
   * (side-effect free on a loaded thread; it also loads the thread, which
   * the read path needs); Claude reads `getSessionMessages` and groups it
   * into turns at each top-level user message. Never opens a writer, never
   * starts a turn. Threads uses it to REPLACE its copy wholesale — item ids
   * here need not match the ids the same turns carried live.
   */
  readonly readHistory: (options: {
    readonly providerSessionId: string
    readonly cwd: string
  }) => Effect.Effect<
    ReadonlyArray<HistoryTurn>,
    AgentUnavailable | AgentResumeFailed | AgentProtocolError
  >
  /**
   * Demand-driven reconciliation: the provider's current word on the
   * session's activity, `unknown` when it offers no evidence. Never
   * guesses; failures are the retryable AgentUnavailable.
   */
  readonly checkSession: (options: {
    readonly providerSessionId: string
  }) => Effect.Effect<AgentActivity, AgentUnavailable>
  /**
   * Release adapter-owned per-session resources (launch files, secret
   * registrations) when the owning thread is deleted. Best-effort and
   * idempotent — cleanup problems are logged, never surfaced.
   */
  readonly releaseSession: (options: {
    readonly providerSessionId: string
    readonly providerMetadata: string | undefined
  }) => Effect.Effect<void>
}

// Internal-only failures (ATC-123): not part of the HTTP contract, so no
// httpApiStatus annotations. The Threads domain maps them to the public
// tagged errors (threads.ts mapAgentError).

/** Provider missing, not ready, or gone — retryable once the cause is fixed. */
export class AgentUnavailable extends Schema.TaggedErrorClass<AgentUnavailable>()(
  "AgentUnavailable",
  { provider: Schema.Literals(AGENT_PROVIDERS), reason: Schema.String },
) {
  override get message(): string {
    return `${this.provider} unavailable: ${this.reason}`
  }
}

/** The provider rejected the exact persisted session id (fail closed). */
export class AgentResumeFailed extends Schema.TaggedErrorClass<AgentResumeFailed>()(
  "AgentResumeFailed",
  {
    provider: Schema.Literals(AGENT_PROVIDERS),
    providerSessionId: Schema.String,
    reason: Schema.String,
  },
) {
  override get message(): string {
    return `${this.provider} resume of ${this.providerSessionId} failed: ${this.reason}`
  }
}

/** Provider evidence disagrees with the expected identity — never adopted. */
export class AgentIdentityMismatch extends Schema.TaggedErrorClass<AgentIdentityMismatch>()(
  "AgentIdentityMismatch",
  {
    provider: Schema.Literals(AGENT_PROVIDERS),
    field: Schema.Literals(["sessionId", "cwd"]),
    expected: Schema.String,
    actual: Schema.String,
  },
) {
  override get message(): string {
    return `${this.provider} ${this.field} mismatch: expected ${this.expected}, got ${this.actual}`
  }
}

/** Required structured evidence was absent or invalid, or the transport broke. */
export class AgentProtocolError extends Schema.TaggedErrorClass<AgentProtocolError>()(
  "AgentProtocolError",
  { provider: Schema.Literals(AGENT_PROVIDERS), reason: Schema.String },
) {
  override get message(): string {
    return `${this.provider} protocol error: ${this.reason}`
  }
}

/** Writer or turn-target conflict: the control target is not (or no longer) valid. */
export class AgentConflict extends Schema.TaggedErrorClass<AgentConflict>()("AgentConflict", {
  provider: Schema.Literals(AGENT_PROVIDERS),
  reason: Schema.String,
}) {
  override get message(): string {
    return `${this.provider} conflict: ${this.reason}`
  }
}

/**
 * The per-provider error constructors and shared conflict-message rules
 * every adapter otherwise re-declares (ATC-167 M12). The message builders
 * are the seam's vocabulary: identical situations must read identically
 * across providers.
 */
export const providerErrors = (provider: AgentProvider) => {
  const conflict = (reason: string) => new AgentConflict({ provider, reason })
  return {
    unavailable: (reason: string) => new AgentUnavailable({ provider, reason }),
    protocolError: (reason: string) => new AgentProtocolError({ provider, reason }),
    conflict,
    identityMismatch: (field: "sessionId" | "cwd", expected: string, actual: string) =>
      new AgentIdentityMismatch({ provider, field, expected, actual }),
    connectionClosed: () => conflict("the connection is closed"),
    turnStillActive: (turnId: string) => conflict(`turn ${turnId} is still active`),
    staleTurn: (turnId: string) => conflict(`turn ${turnId} is not the active turn`),
    writerConnected: (providerSessionId: string) =>
      conflict(`a writer is already connected to ${providerSessionId}`),
    unknownRequest: (requestId: string) => conflict(`request ${requestId} is not pending`),
    answerKindMismatch: (requestId: string, expected: string) =>
      conflict(`request ${requestId} expects a ${expected} answer`),
  }
}

/** The shared tool-status rule: a declined approval is an error, and every
 * error carries a human-readable reason (`toolFields` in the contract). */
export const toolOutcome = (
  status: "pending" | "running" | "completed" | "declined",
  failure: string | null,
): { readonly status: ToolStatus; readonly error?: string } => {
  if (status === "declined") return { status: "error", error: "declined" }
  if (failure !== null) return { status: "error", error: failure }
  return { status }
}

/** The shared fileChange title rule: one verb for the batch, the file name
 * for a single change, a count beyond that. */
export const fileChangeTitle = (changes: ReadonlyArray<FileChange>): string => {
  const kinds = new Set(changes.map((change) => change.kind))
  const verb =
    kinds.size === 1 && kinds.has("add")
      ? "Create"
      : kinds.size === 1 && kinds.has("delete")
        ? "Delete"
        : "Edit"
  if (changes.length === 1) return `${verb} ${basename(changes[0]!.path)}`
  return `${verb} ${changes.length} files`
}

/**
 * The pending ↔ running flip a parked approval performs on its tool item:
 * the re-sent item, or null when there is nothing to change (not a tool
 * item, already there, or past those states).
 */
export const withToolStatus = (
  item: ThreadItem,
  status: "pending" | "running",
): ThreadItem | null => {
  if (!("status" in item) || item.status === status) return null
  if (item.status !== "pending" && item.status !== "running") return null
  return { ...item, status }
}

/** Bound on one title one-shot (ATC-155); a hung child/query is cut here. */
const TITLE_TIMEOUT = "90 seconds"

/** The shared title-generation bound, failing with the provider's protocol error. */
export const withTitleTimeout =
  (protocolError: (reason: string) => AgentProtocolError) =>
  <A, E, R>(self: Effect.Effect<A, E, R>): Effect.Effect<A, E | AgentProtocolError, R> =>
    self.pipe(
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

/**
 * Deliver one event onto a session's writer feed. Overflow (a bounded
 * queue whose consumer stopped draining) FAILS the stream rather than
 * ending it — a clean end would read as a completed turn to the consumer,
 * not a truncated one (the events.ts subscriber policy, adapted). The
 * overflow branch arms once the session queues carry capacities (the
 * ATC-172 reliability change bounds them); on unbounded queues it is
 * reachable only for an already-ended queue, where failCauseUnsafe no-ops.
 */
export const emitAgentEvent = (
  queue: Queue.Queue<AgentEvent, AgentProtocolError | Cause.Done>,
  event: AgentEvent,
  protocolError: (reason: string) => AgentProtocolError,
): void => {
  if (!Queue.offerUnsafe(queue, event)) {
    Queue.failCauseUnsafe(
      queue,
      Cause.fail(protocolError("session event stream overflowed; reopen and resync")),
    )
  }
}

const INSTALL_HINTS: Record<AgentProvider, string> = {
  codex: "install the Codex CLI",
  claude: "install and authenticate Claude Code",
}

const CONFIG_KEYS: Record<AgentProvider, string> = {
  codex: "codexExecutable",
  claude: "claudeExecutable",
}

/**
 * Resolve a provider executable from its settled configuration value
 * (AppConfig `codexExecutable` / `claudeExecutable`), or fail with the one
 * actionable install-or-configure diagnostic. Configured paths are
 * existence-checked here so a typo'd path fails as this diagnostic, not as
 * a downstream launch timeout.
 */
export const resolveProviderExecutable = (
  provider: AgentProvider,
  configured: string,
): Effect.Effect<string, AgentUnavailable> =>
  Effect.suspend(() => {
    const found = resolveExecutable(configured)
    if (found !== null && existsSync(found)) return Effect.succeed(found)
    const key = CONFIG_KEYS[provider]
    return Effect.fail(
      new AgentUnavailable({
        provider,
        reason:
          `${configured} not found; ${INSTALL_HINTS[provider]} ` +
          `or set ${key} in config.toml / ATC_${provider.toUpperCase()}_EXECUTABLE`,
      }),
    )
  })

/** The refinement inputs (ATC-190): collected conversation context plus the
 * title the thread currently carries (null when the first pass adopted
 * nothing). */
export interface TitleRefinement {
  readonly context: string
  readonly currentTitle: string | null
}

/** Cap on collected refinement context (T3Code precedent): the one-shot
 * needs the gist of the conversation, never a transcript. */
export const TITLE_CONTEXT_LIMIT = 8_192

/**
 * Assemble `collectTitleContext` output from labeled conversation lines:
 * the most recent text within `TITLE_CONTEXT_LIMIT`, or null when nothing
 * usable remains. Shared so every adapter's context reads the same to the
 * shared instruction.
 */
export const buildTitleContext = (entries: ReadonlyArray<string>): string | null => {
  const lines = entries.map((entry) => entry.trim()).filter((entry) => entry !== "")
  if (lines.length === 0) return null
  const text = lines.join("\n")
  return text.length > TITLE_CONTEXT_LIMIT ? text.slice(-TITLE_CONTEXT_LIMIT) : text
}

/**
 * The one thread-title instruction (ATC-155), shared by every adapter's
 * generateTitle so Claude and Codex titles read the same. The user prompt
 * is capped so a pasted wall of text cannot blow up the one-shot's cost —
 * the opening lines are what a title needs.
 *
 * The run is tool-free by design (ATC-190): a referenced identifier is
 * never looked up here. The refinement variant carries the conversation
 * the primary agent already produced — which names what any identifier
 * turned out to mean — and demands no churn: a current title that already
 * fits comes back verbatim.
 */
export const titleInstruction = (prompt: string, refine?: TitleRefinement): string =>
  [
    "You write concise titles for coding-agent conversation threads.",
    "Reply with the title only: a single line of plain text, with nothing before or after it.",
    "Rules:",
    "- Summarize the user's request; never answer it or restate it verbatim.",
    "- Make the descriptive part 3-8 words, specific over generic.",
    "- Use sentence case, not title case: capitalize only the first word and proper nouns.",
    "- No quotes, no markdown, no trailing punctuation.",
    "- When the request clearly matches one category below, prefix the title with its exact label followed by ' - ':",
    "  - Build: create, implement, change, or fix something.",
    "  - Review: review or critique existing code or work.",
    "  - Grill: explicitly grill or stress-test a plan or design.",
    "  - Explore: brainstorm, research, compare, or understand options.",
    "  - Investigate: diagnose a suspected bug or problem without asking for a fix.",
    "- If no category clearly matches, omit the prefix.",
    "- Keep every identifier the request names — issue id, PR number, URL — verbatim, and put it ahead of the descriptive part; identifiers do not count toward the word budget.",
    "- A leading $name or /name is a skill invocation: the name is the action being asked for (for example $grill means grill whatever follows).",
    "- Never guess what a referenced identifier points to: without evidence of what it means, the title may say nothing about the resource beyond the identifier itself.",
    ...(refine === undefined
      ? []
      : [
          "",
          "The conversation below shows what the work actually turned out to be — including what any referenced identifier means. Title the actual work.",
          ...(refine.currentTitle === null
            ? []
            : [
                `The thread's current title: ${refine.currentTitle}`,
                "If that title already describes the actual work, reply with it exactly, unchanged. Replace it only when the conversation reveals something it misses.",
              ]),
          "",
          "The conversation so far:",
          // Already capped: refinement context only ever comes from
          // collectTitleContext, whose contract is buildTitleContext.
          refine.context,
        ]),
    "",
    "The user's first message:",
    prompt.length > 4_000 ? `${prompt.slice(0, 4_000)}…` : prompt,
  ].join("\n")

const WRAPPING_QUOTES = [
  ['"', '"'],
  ["'", "'"],
  ["“", "”"],
  ["‘", "’"],
  ["`", "`"],
] as const

/**
 * Output hygiene for generated titles, enforced in code rather than trusted
 * to the prompt (T3Code/OpenCode precedent): one line only, whitespace
 * collapsed, wrapping quotes stripped, trailing punctuation dropped, capped
 * at ~50 characters on a word boundary. Returns null when nothing usable
 * remains — the caller gives up silently.
 *
 * The cap applies to the whole title, identifiers included; the instruction
 * keeps them ahead of the descriptive part so the cut lands on prose. An
 * identifier longer than the cap itself (a deep URL) is still truncated —
 * a title that long would not be a title.
 *
 * The line taken is the LAST non-empty one (ATC-186): a small model that
 * ignores "the title only" narrates first and answers last ("I can't look
 * that up… \n\n Build - ATC-186") — and the narration is never the title.
 */
export const sanitizeTitle = (raw: string): string | null => {
  const line = raw
    .split("\n")
    .map((candidate) => candidate.trim())
    .findLast((candidate) => candidate !== "")
  if (line === undefined) return null
  let title = line.replace(/\s+/g, " ").trim()
  for (let stripped = true; stripped;) {
    stripped = false
    for (const [open, close] of WRAPPING_QUOTES) {
      if (title.length >= 2 && title.startsWith(open) && title.endsWith(close)) {
        title = title.slice(1, -1).trim()
        stripped = true
      }
    }
  }
  title = title.replace(/[.,;:!?…]+$/, "").trim()
  if (title.length > 50) {
    const cut = title.slice(0, 50)
    const lastSpace = cut.lastIndexOf(" ")
    title = (lastSpace > 20 ? cut.slice(0, lastSpace) : cut).trimEnd()
  }
  return title === "" ? null : title
}

/** Symlink-tolerant path equality (macOS tmpdir lives behind /private). */
export const samePath = (left: string, right: string): boolean => {
  const canonical = (value: string): string => {
    try {
      return realpathSync(value)
    } catch {
      return value
    }
  }
  return canonical(left) === canonical(right)
}

/**
 * Read `<executable> --version` and extract an x.y.z version string;
 * null when the version cannot be determined (any spawn or parse failure).
 */
export const readInstalledVersion = (
  subprocess: Subprocess["Service"],
  executable: string,
): Effect.Effect<string | null> =>
  Effect.scoped(
    Effect.gen(function* () {
      const child = yield* subprocess.spawn({
        executable,
        args: ["--version"],
        env: {},
        extendEnv: true,
      })
      const lines = yield* Stream.runCollect(child.stdoutLines)
      yield* child.exitCode
      return lines.join(" ")
    }),
  ).pipe(
    // Bounded: a wedged binary must not hang a demand-driven registry read.
    Effect.timeoutOrElse({ duration: "5 seconds", orElse: () => Effect.succeed("") }),
    Effect.orElseSucceed(() => ""),
    Effect.map(parseVersion),
  )

/** The first x.y.z in `text` (a --version line, a userAgent), else null. */
export const parseVersion = (text: string): string | null =>
  text
    .match(/(\d+)\.(\d+)\.(\d+)/)
    ?.slice(1, 4)
    .join(".") ?? null

/** Whether `installed` is below the `tested` x.y.z floor (component-wise). */
export const versionIsOlder = (installed: string, tested: string): boolean => {
  const left = installed.split(".").map(Number)
  const right = tested.split(".").map(Number)
  for (let index = 0; index < 3; index++) {
    const difference = (left[index] ?? 0) - (right[index] ?? 0)
    if (difference !== 0) return difference < 0
  }
  return false
}

/**
 * Memoize one effect's success for `ttl`, safely under concurrency (the
 * version gate and the model catalogs use it). NOT plain Effect.cached: the
 * cache memoizes whatever exit its driving fiber produces, interruption and
 * failure included, so a dropped request driving the first run would poison
 * every later caller for the process, and a provider that was down once
 * would stay "down" until the TTL. Instead the memo is invalidated when its
 * driver fails or is interrupted, and the semaphore keeps callers out of the
 * cache's waiter path (a waiter racing an invalidate would replay a cleared
 * exit).
 */
export const memoizeSuccess = <A, E, R>(
  effect: Effect.Effect<A, E, R>,
  ttl: Duration.Input,
): Effect.Effect<Effect.Effect<A, E, R>> =>
  Effect.gen(function* () {
    const lock = yield* Semaphore.make(1)
    const [cached, invalidate] = yield* Effect.cachedInvalidateWithTTL(effect, ttl)
    return lock.withPermits(1)(
      Effect.onInterrupt(cached, () => invalidate).pipe(Effect.tapError(() => invalidate)),
    )
  })

/**
 * The shared version-drift rule (record + warn, never block), memoized to
 * one single-flight check per adapter (`memoizeSuccess`): resolve the
 * configured executable, read the installed version via `--version`, and log
 * one actionable warning when it is below the floor the adapter was
 * validated against. Warn-only: a missing executable never fails the gate —
 * every call site resolves or spawns the executable itself and reports the
 * actionable diagnostic there — and a memoized failure would pin
 * "unavailable" past a mid-session install.
 */
export const makeVersionGate = (
  subprocess: Subprocess["Service"],
  provider: AgentProvider,
  configured: string,
  testedVersion: string,
): Effect.Effect<Effect.Effect<void>> =>
  Effect.gen(function* () {
    const check = Effect.gen(function* () {
      const executable = yield* resolveProviderExecutable(provider, configured)
      const installed = yield* readInstalledVersion(subprocess, executable)
      if (installed === null) {
        return yield* Effect.logWarning(
          `could not determine the installed ${provider} version (tested against ${testedVersion})`,
        )
      }
      if (versionIsOlder(installed, testedVersion)) {
        yield* Effect.logWarning(
          `installed ${provider} ${installed} is older than the tested ${testedVersion}; proceeding, but upgrade it if provider behavior misbehaves`,
        )
      }
    }).pipe(Effect.catchTag("AgentUnavailable", () => Effect.void))
    return yield* memoizeSuccess(check, Duration.infinity)
  })
