import {
  Cause,
  Context,
  Deferred,
  Duration,
  Effect,
  Exit,
  Fiber,
  Layer,
  Option,
  Queue,
  Scope,
  Semaphore,
  Stream,
} from "effect"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import { isBusyActivity } from "../agents/agentAdapter.ts"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConnection,
  AgentEvent,
  AgentItemEvent,
  AgentSessionEvent,
  AgentTurn,
  ProviderSettings,
  ThreadAttachment,
  ThreadRequest,
  ThreadRequestAnswer,
  ThreadTurn,
  TurnInput,
} from "../agents/agentAdapter.ts"
import type * as Contract from "../api/contract.ts"
import {
  AttachmentNotFound,
  InvalidRequestAnswer,
  isAgentId,
  ProviderSessionConflict,
  ProviderUnavailable,
  QueuedPromptNotFound,
  RequestNotFound,
  ThreadArchived,
  ThreadKindMismatch,
  ThreadNotFound,
} from "../api/contract.ts"
import type { ZmxUnavailable } from "../api/contract.ts"
import { Events } from "../events/events.ts"
import type { Terminal } from "../terminals/terminals.ts"
import { Attachments } from "./attachments.ts"
import { ThreadNaming } from "./threadNaming.ts"
import { requireKind, ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"
import { applyProviderSettings } from "./threadSettings.ts"
import type { ThreadSettings } from "./threadSettings.ts"
import { ThreadTui } from "./threadTui.ts"
import { TranscriptRepository } from "./transcriptRepository.ts"
import type { QueuedPromptRecord, TranscriptChange, TurnSource } from "./transcriptRepository.ts"

export type ThreadEvent = typeof Contract.ThreadEvent.Type
export type ThreadTranscript = typeof Contract.ThreadTranscript.Type
export type QueuedPrompt = typeof Contract.QueuedPrompt.Type

// The Thread runtime (ATC-193): the server-owned run, and everything live
// about a thread's provider session. It drives a thread's conversation
// through the AgentAdapter seam with no Terminal involved — prompts, the
// durable prompt queue, the turn loop, parked provider requests, the
// transcript projection with its per-thread event stream, the observation
// of TUI-driven sessions, and the live activity ledger every surface reads.
// Every surface (macOS, CLI, Linear) is a client of this one service;
// nothing here is provider-specific beyond the seam's `sharedServer`
// capability. Invariants:
//
//   - Thread kinds (ATC-224): a chat thread is driven only by this
//     runtime's own writer — prompt, interrupt, answer, and the queue
//     refuse a tui thread (ThreadKindMismatch) — and is NEVER observed or
//     re-read: `observe` is a no-op on it, no re-read trigger (idle,
//     startup, a failed connection, a TUI end) ever replaces its
//     transcript, and `snapshot.invalidated` never reaches its stream. Its
//     transcript is ATC's append-only record of the turns ATC drove. A tui
//     thread is the converse: observed and re-read as the bullets below
//     say, never prompted. A chat thread whose confirmed provider session
//     is gone (AgentResumeFailed on resume, or on Claude's first turn
//     after it) is not stuck: the turn starts a fresh session, the
//     transcript is kept, and one `notice` item opens the turn. A Codex
//     chat thread mid-turn in another Codex client refuses the prompt
//     (un-admitted, ProviderSessionConflict) — nothing queues behind a
//     turn ATC cannot see. The ownership and observation bullets below
//     therefore apply to tui threads only (ATC-226 removes them).
//   - A prompt is admitted, always: it lands in the queue (durable), and if
//     the thread is idle it starts a turn at once — the one exception being
//     a provider that refuses that immediate start, which un-admits the
//     prompt so the caller's error means "not accepted". A thread busy
//     under a TUI (the ledger says so) queues the prompt and drains at the
//     observed idle: no busy error, no lock between surfaces. A prompt
//     marked `now` (ATC-216) is instead handed to the turn running on our
//     writer — the seam's `steer` — and recorded against that turn; with no
//     such turn, or one that ended first, it is an ordinary prompt. A turn runs
//     on a writer connection the runtime holds; client connections never
//     matter to a run's lifetime. A closing writer stays registered until
//     its connection has closed, so the next prompt never races it.
//   - Connection lifetime is not turn lifetime (ATC-207): the Run ends at
//     turn end, the writer need not. A one-process provider's writer stays
//     resident after a successful turn — the next prompt starts on it
//     rather than re-spawning and resuming — and while held its feed owns
//     the ledger; it must close before another process takes the session
//     (the TUI hand-off), it closes when the provider ends it, on release
//     and shutdown, and idle residency is bounded (`armIdleClose`). A
//     shared-server provider's connection is a cheap subscription that
//     coexists with its TUI: it holds nothing between turns, as before.
//     A turn the provider starts by itself on a held one-process
//     connection (Claude's root loop woken by a finished background task)
//     is ATC's run: adopted at its turnStarted, ended by `finish` like a
//     prompted turn, its requests answerable; a prompt admitted meanwhile
//     waits for it (the connection refuses a second turn) and drains at
//     its end. On a shared server the same event is another writer's turn
//     and stays observed.
//   - One live process per thread (ATC-203), on a provider whose sessions
//     live in one process at a time (`sharedServer: false` — Claude; two
//     live processes fork the session). The native side owns the thread by
//     default; the TUI owns it only while clients want it: `tuiWanted` is a
//     per-thread, last-writer-wins mark — openTui sets it, closeTui clears
//     it, and a TUI found live at a thread's first observation (no client
//     has spoken for it yet — after a restart) is wanted until a client
//     says otherwise. It is deliberately not per-client presence
//     (single user; a client that quits without closing leaves the TUI in
//     charge until the next close, nothing worse). A native turn against a
//     live TUI queues while the TUI is busy (the ledger), then ENDS the TUI
//     and resumes the session itself; when the run ends with the queue
//     drained and the TUI still wanted, the TUI is relaunched. The ledger
//     is the only liveness evidence Claude offers (checkSession is
//     `unknown`), so `unknown` — a TUI observed since a restart that has
//     not spoken yet — is taken as idle: the settle gate below is the
//     remaining guard, and a queue that could otherwise never drain is the
//     worse failure. A TUI open
//     during a native run is refused (ThreadBusy) and happens at the run's
//     end instead, and no native turn starts while a TUI launch is in
//     flight. Closing the TUI ends it at once when idle, at its next
//     observed idle when busy. A TUI ends only after its last turn is on
//     disk — `endTui`: the settled re-read (the adapter's readHistory)
//     first, then the kill; a read that fails leaves the TUI alive (the
//     prompt stays queued / the close fails retryably) rather than guess.
//     While a writer is held, observed activity is ignored: the ended TUI's
//     own SessionEnd must not read as the native turn's end. A
//     shared-server provider (Codex) needs none of this — its TUI and
//     native connection coexist — and its observation keeps writing the
//     ledger between runs as before.
//   - Observation is per thread and demand-driven: `observe` starts (once
//     per provider session) the adapter's normalized subscription for a
//     TUI-driven session and drains it into the ledger, the transcript copy
//     (observed items), and ThreadNaming; `unobserve` ends it. A superseded
//     session's subscription must never keep driving the thread, and a
//     feed that ends on its own releases its claim so the next read
//     re-subscribes instead of trusting a dead stream.
//   - The transcript copy is a projection of provider history, never the
//     authority: `seq` (allocated by the repository) orders durable changes
//     and replays them (subscribe after=seq); a re-read (`readHistory`)
//     replaces the copy wholesale and bumps snapshotVersion — triggered at
//     startup for threads mid-work, when a TUI-driven thread goes idle, and
//     before a TUI is ended (its last turn), never for a turn ATC drove
//     itself. An empty history never replaces the copy (a swept Claude
//     transcript keeps ATC's copy readable).
//   - Provider requests park in memory until answered through
//     `answerRequest`; they die with their turn or the process (a re-run
//     turn re-asks). Answers are validated against the request here, so
//     adapters only ever see well-formed answers.
//   - Settings (ATC-205): every turn starts with the thread's stored
//     settings (re-read under the start lock, so a change made while a
//     prompt waited is what the turn runs with); the adapter pushes only
//     what differs from the live session. The provider's own word on its
//     session settings — the seam's `settings` events, on the writer feed
//     and the observation feed alike — is adopted into the row as it
//     arrives (provider state wins), which is how a change made in a TUI or
//     another provider client shows up here — except that a report merely
//     confirming what the writer's last turn pushed is not news
//     (applyProviderSettings): a confirmation that lands after the user
//     changed a setting again must not roll that change back.
//   - The activity ledger is the one place live activity lives (the
//     observation drain and threads.ts's demand-driven re-derivation feed
//     it too): a first busy confirms the thread, a busy→idle drop stamps
//     last_finished_at (unread, ATC-160), and every transition publishes
//     the thread as updated. Turn end forces idle (the turn is done; ATC-160
//     stamps it); a resident connection's later evidence — background work
//     re-busying the thread, then its idle — applies on top, and a
//     connection that ends forces idle again, since nothing can report on
//     it any more. While the runtime holds a writer on a thread, its ledger
//     entry is authoritative: never re-derived, and observed activity is
//     ignored. The ledger also feeds ThreadNaming every busy/idle edge,
//     whichever surface produced it (ATC-202).
//   - Startup: threads with a running turn re-read history (their status
//     is whatever the provider says); a turn still running on a shared
//     provider server (Codex) is reattached and followed to its end, any
//     other still-running turn is marked interrupted; then queues drain.
//     A provider that is unavailable at startup leaves prompts queued —
//     the next prompt on that thread retries the drain.
//   - Live-only events (text.delta, request.*, queue.updated) carry no seq
//     and are never replayed; a replay re-sends changed rows as
//     item.updated / turn.started|completed with the row's current state,
//     in seq order. A subscriber rejoining from before the latest
//     replacement gets snapshot.invalidated instead — the deleted rows
//     cannot be replayed, so it refetches.
//   - Streamed text is durable while it streams (ATC-214): deltas stay
//     live-only, and on a throttle the writer persists the text so far as
//     an item.updated partial (complete stays false), so a reconnect, a
//     second window, or GET /transcript sees a mid-answer bubble instead of
//     an empty one. The tail since the last flush is lost with the process;
//     byte-exact resume is a non-goal. Partials cover items whose start this
//     writer saw: a turn reattached after a restart streams live-only until
//     the adapter re-sends the item — seeding from the stored copy could
//     persist text with a hole where the missed deltas were.
//   - Recovery, prompt admission, run end, and release serialize per thread
//     behind one start lock; threads still recovering at startup queue
//     their prompts until recovery has settled them.

/** The default transcript page (the contract caps requests at the same size). */
const TRANSCRIPT_PAGE = 200

/** Per-subscriber backlog; overflow ends the stream (resubscribe after=seq). */
const SUBSCRIBER_CAPACITY = 512

/** One live turn ATC drives (or follows, after a restart). */
interface Run {
  readonly turnId: string
  readonly turn: AgentTurn
  readonly requests: Map<string, ThreadRequest>
  finished: boolean
}

/** Once accumulated delta text reaches this length (UTF-16 units — close
 * enough for a flush heuristic) the partial persists at once; the throttle
 * timer covers the slow drip. */
const STREAMING_FLUSH_LENGTH = 16 * 1024

/**
 * The streamed text of one open item on a writer's feed (the header's
 * durable-partials rule): the adapter's last full item plus every delta
 * since, and the flush bookkeeping.
 */
interface StreamingText {
  /** The last full item the adapter sent (complete: false). */
  base: typeof Contract.ThreadItemAssistantText.Type | typeof Contract.ThreadItemReasoning.Type
  /** base.text plus every delta since. */
  text: string
  /** Bytes accumulated since the last persist. */
  pending: number
  /** The throttle timer is armed; the flush it runs disarms it. */
  armed: boolean
}

/**
 * A writer connection the runtime holds on a thread's provider session:
 * the process (Claude) or subscription (Codex) behind it lives in `scope`,
 * its feed drains on a fiber of that scope, and it carries at most one Run.
 */
interface Writer {
  readonly scope: Scope.Closeable
  readonly connection: AgentConnection
  /** Serializes the feed drain against a turn start on this connection, so
   * a turn's first events always find its Run registered (see startNext). */
  readonly lock: Semaphore.Semaphore
  run: Run | undefined
  /** Set the instant a close is decided (and completed once the writer is
   * closed and deregistered): nothing starts on the connection any more,
   * and its busy evidence is ignored — a lingering subagent's evidence on
   * a closing feed would busy the thread with nothing left to report its
   * idle. Every close of the writer waits on this one latch. */
  closing: Deferred.Deferred<void> | undefined
  /** The idle-close timer of a resident writer (see armIdleClose). */
  idleClose: Fiber.Fiber<void> | undefined
  /** The settings the last turn on this connection was started with — what
   * the provider's next report merely confirms (applyProviderSettings). */
  pushed: ThreadSettings | undefined
  /** Streaming text items open on this feed (the durable-partials rule). */
  readonly streaming: Map<string, StreamingText>
}

export interface ThreadRuntimeOptions {
  /** How long a one-process provider's writer stays resident with no turn
   * running (the header's bounded-residency rule). */
  readonly residentIdleTimeout?: Duration.Input
  /** How often streamed text persists as a durable partial (the header's
   * durable-partials rule). */
  readonly streamingFlushInterval?: Duration.Input
}

export class ThreadRuntime extends Context.Service<
  ThreadRuntime,
  {
    /** Admit a prompt; `turnId` present ⇒ it started at once (or, with
     * `when: "now"`, joined the running turn), else queued. `attachments`
     * are ids of the thread's own uploads (ATC-216); a foreign or unknown
     * id refuses the prompt before it is admitted. */
    readonly prompt: (
      id: string,
      input: {
        readonly prompt: string
        readonly attachments?: ReadonlyArray<string> | undefined
        readonly when?: "queue" | "now" | undefined
      },
    ) => Effect.Effect<
      { readonly promptId: string; readonly turnId?: string },
      | ThreadNotFound
      | ThreadArchived
      | ThreadKindMismatch
      | AttachmentNotFound
      | ProviderUnavailable
      | ProviderSessionConflict
    >
    /** Interrupt the running turn; an idle thread is a no-op. Queued prompts stay. */
    readonly interrupt: (
      id: string,
    ) => Effect.Effect<void, ThreadNotFound | ThreadKindMismatch | ProviderUnavailable>
    readonly listRequests: (
      id: string,
    ) => Effect.Effect<ReadonlyArray<ThreadRequest>, ThreadNotFound>
    readonly answerRequest: (
      id: string,
      requestId: string,
      answer: ThreadRequestAnswer,
    ) => Effect.Effect<
      void,
      | ThreadNotFound
      | ThreadKindMismatch
      | RequestNotFound
      | InvalidRequestAnswer
      | ProviderUnavailable
    >
    readonly listQueue: (id: string) => Effect.Effect<ReadonlyArray<QueuedPrompt>, ThreadNotFound>
    readonly deleteQueued: (
      id: string,
      promptId: string,
    ) => Effect.Effect<void, ThreadNotFound | ThreadKindMismatch | QueuedPromptNotFound>
    /** A page of the transcript (newest `limit`, default 200 — the contract
     * bounds it; or the page before `before`). */
    readonly transcript: (
      id: string,
      options?: { readonly before?: string | undefined; readonly limit?: number | undefined },
    ) => Effect.Effect<ThreadTranscript, ThreadNotFound>
    /**
     * The per-thread event stream. With `after`, every durable change past
     * that seq replays first (current row state), then live events follow;
     * without it the stream is live only. Registered eagerly (a publish
     * after this returns is delivered); the stream's end unsubscribes.
     */
    readonly subscribe: (
      id: string,
      after?: number | undefined,
    ) => Effect.Effect<Stream.Stream<ThreadEvent>, ThreadNotFound>
    /** Replace the copy with provider history (see the header's rules; a
     * no-op on a chat thread). No route calls this — the triggers are
     * internal; it exists for tests and manual repair. */
    readonly reread: (
      id: string,
    ) => Effect.Effect<void, ThreadNotFound | ProviderUnavailable | ProviderSessionConflict>

    // --- The seams threads.ts consumes -------------------------------------

    /** The ledger's current activity for a thread (undefined = no evidence). */
    readonly activity: (id: string) => Effect.Effect<AgentActivity | undefined>
    /** Whether the runtime holds a live writer on the thread — a turn in
     * progress, or a one-process provider's resident connection between
     * turns: its feed is the ledger's evidence either way. */
    readonly hasWriter: (id: string) => Effect.Effect<boolean>
    /** Feed one activity observation through the ledger's rules (see header). */
    readonly noteActivity: (
      record: ThreadRecord,
      activity: AgentActivity,
    ) => Effect.Effect<{ readonly previous: AgentActivity | undefined }>
    /** The thread went idle under observation: re-read unless the busy was
     * ours, end a TUI no client shows, drain the queue. */
    readonly observedIdle: (record: ThreadRecord) => Effect.Effect<void>
    /** Start (once) the thread's session observation — call it whenever a
     * TUI is live or being launched on the record's session. A no-op on a
     * chat thread (the header). */
    readonly observe: (record: ThreadRecord) => Effect.Effect<void>
    /** End the thread's observation (a superseded session, a put-away). */
    readonly unobserve: (id: string) => Effect.Effect<void>
    /** Whether the record's current session is under observation. */
    readonly observing: (record: ThreadRecord) => Effect.Effect<boolean>
    /**
     * A client shows the TUI (openThreadTerminal, tui threads only): mark
     * it wanted, then launch under the ownership rules — the live linked
     * terminal is returned as-is (checked again once the launch claim is
     * held, so an automatic relaunch and an explicit open can never both
     * launch), and no native turn starts while the launch is in flight.
     */
    readonly openTui: <E, R>(
      record: ThreadRecord,
      launch: Effect.Effect<Terminal, E, R>,
    ) => Effect.Effect<Terminal, E, R>
    /**
     * A client stopped showing the TUI (closeThreadTerminal): the native
     * side takes the thread back on a one-process provider — an idle TUI
     * ends now (its last turn on disk first — a failed read fails the close
     * retryably and leaves the TUI running), a busy one at its observed
     * idle. A shared-server provider's TUI keeps running.
     */
    readonly closeTui: (
      record: ThreadRecord,
    ) => Effect.Effect<void, ProviderUnavailable | ProviderSessionConflict | ZmxUnavailable>
    /** Release the run, the observation, the surface marks, and the thread's
     * event streams (archive/delete): the connection closes, its turn is
     * recorded interrupted, and the provider keeps its own state. */
    readonly release: (id: string) => Effect.Effect<void>
  }
>()("app-server/ThreadRuntime") {}

const make = (options: ThreadRuntimeOptions) =>
  Effect.gen(function* () {
    const repository = yield* ThreadRepository
    const transcripts = yield* TranscriptRepository
    const attachments = yield* Attachments
    const registry = yield* AgentRegistry
    const events = yield* Events
    const naming = yield* ThreadNaming
    const tui = yield* ThreadTui
    // Writers, observations, and their drain fibers outlive their
    // originating requests; they live in the service scope so shutdown
    // releases every connection and subscription.
    const serviceScope = yield* Effect.scope
    const residentIdleTimeout = options.residentIdleTimeout ?? "10 minutes"
    const streamingFlushInterval = options.streamingFlushInterval ?? "250 millis"

    const writers = new Map<string, Writer>()
    /** The turn the runtime drives on a thread, if any. */
    const runOf = (id: string): Run | undefined => writers.get(id)?.run
    const hubs = new Map<string, Set<Queue.Queue<ThreadEvent, Cause.Done>>>()
    const liveActivity = new Map<string, AgentActivity>()
    /** The observed session per thread; the child scope closes it. */
    interface Observation {
      readonly providerSessionId: string
      readonly scope: Scope.Closeable
    }
    const observed = new Map<string, Observation>()
    /** Threads whose clients want the TUI (the header's ownership rule). */
    const tuiWanted = new Set<string>()
    /** Threads whose TUI want is settled — a client said open or close, or
     * a live TUI was adopted at first observation. Only an unsettled
     * thread's observation may mark the TUI wanted: a feed that ends and is
     * re-observed must not undo an explicit close. */
    const tuiSettled = new Set<string>()
    /** Threads with a TUI launch in flight: no native turn starts meanwhile. */
    const tuiOpening = new Set<string>()
    // Threads the startup pass has not settled yet: their prompts wait.
    const recovering = new Set(yield* transcripts.threadsNeedingAttention())
    /** Per-thread serialization of "is a run active → start the next prompt". */
    const startLocks = new Map<string, Semaphore.Semaphore>()
    const withStartLock =
      (id: string) =>
      <A, E, R>(effect: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> =>
        Effect.suspend(() => {
          const lock = startLocks.get(id) ?? Semaphore.makeUnsafe(1)
          startLocks.set(id, lock)
          return lock.withPermit(effect)
        })

    const adapterFor = (record: ThreadRecord): AgentAdapter | undefined =>
      isAgentId(record.agentId) ? registry.adapterFor(record.agentId) : undefined

    const requireAdapter = (record: ThreadRecord) =>
      Effect.suspend(() => {
        const adapter = adapterFor(record)
        return adapter === undefined
          ? Effect.fail(
              new ProviderUnavailable({
                agentId: record.agentId,
                reason: `this build knows no agent "${record.agentId}"`,
              }),
            )
          : Effect.succeed(adapter)
      })

    const isBusy = isBusyActivity

    /** The thread's provider keeps a session in one process at a time (the
     * header's ownership rule applies); an unknown agent needs no hand-off. */
    const oneProcess = (record: ThreadRecord): boolean => adapterFor(record)?.sharedServer === false

    // --- Per-thread event hub -------------------------------------------------

    const publish = (threadId: string, event: ThreadEvent): void => {
      const subscribers = hubs.get(threadId)
      if (subscribers === undefined) return
      for (const queue of subscribers) {
        if (Queue.offerUnsafe(queue, event)) continue
        // A subscriber that stopped draining lost this event: end its
        // stream so it resubscribes after=seq rather than reading a gap.
        Queue.endUnsafe(queue)
        subscribers.delete(queue)
      }
    }

    // --- Transcript projection --------------------------------------------------

    const recordItem = (threadId: string, event: AgentItemEvent): Effect.Effect<void> =>
      Effect.gen(function* () {
        // The stored item is the published one: the repository stamps
        // timestamps, and clients must see exactly what a read serves.
        const { seq, item } = yield* transcripts.upsertItem(threadId, event.item)
        const type =
          event.type === "itemStarted"
            ? "item.started"
            : event.type === "itemUpdated"
              ? "item.updated"
              : "item.completed"
        publish(threadId, { type, seq, item })
      })

    /** Upsert a turn and publish the row as it now stands (an update keeps
     * the row's promptId/startedAt), so live events carry current state. */
    const recordTurn = (
      threadId: string,
      turn: ThreadTurn,
      source: TurnSource,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        const stored = yield* transcripts.upsertTurn(threadId, turn, source)
        publish(threadId, {
          type: stored.turn.status === "running" ? "turn.started" : "turn.completed",
          seq: stored.seq,
          turn: stored.turn,
        })
      })

    /** A waiting prompt's attachments (ATC-216). The ids are the thread's
     * own and cascade with it, so a miss here is a defect, not a 404. */
    const attachmentsOf = (
      threadId: string,
      queued: QueuedPromptRecord,
    ): Effect.Effect<ReadonlyArray<ThreadAttachment>> =>
      attachments.resolve(threadId, queued.attachmentIds).pipe(Effect.orDie)

    /** What the turn starts with: the prompt and its resolved images. */
    const turnInputOf = (threadId: string, queued: QueuedPromptRecord): Effect.Effect<TurnInput> =>
      attachmentsOf(threadId, queued).pipe(
        Effect.map((resolved) => ({ text: queued.prompt, attachments: resolved })),
      )

    /** The waiting prompts in the contract's shape, oldest first. */
    const queuedPrompts = (threadId: string): Effect.Effect<ReadonlyArray<QueuedPrompt>> =>
      transcripts.listWaiting(threadId).pipe(
        Effect.flatMap(
          Effect.forEach((queued) =>
            attachmentsOf(threadId, queued).pipe(
              Effect.map((resolved): QueuedPrompt => ({
                id: queued.id,
                prompt: queued.prompt,
                queuedAt: queued.queuedAt,
                ...(resolved.length > 0 ? { attachments: resolved } : {}),
              })),
            ),
          ),
        ),
      )

    /**
     * Hand an admitted (waiting) prompt to the turn running on the thread's
     * writer (ATC-216 "now"): the seam's `steer`, under the writer's lock so
     * the item it produces lands after the Run that owns it, and — in one
     * uninterruptible step with it — the row stamped started against that
     * turn, so a provider that took the message never has it delivered
     * again by the drain. Undefined when no turn of ours runs (a TUI-driven
     * turn has no writer to hand to) or the turn ended first: the prompt
     * stays waiting for the ordinary drain.
     */
    const steerRunning = (
      id: string,
      queued: QueuedPromptRecord,
      input: TurnInput,
    ): Effect.Effect<{ readonly promptId: string; readonly turnId: string } | undefined> =>
      Effect.gen(function* () {
        const writer = writers.get(id)
        const run = writer?.run
        if (writer === undefined || run === undefined || run.finished) return undefined
        const joined = yield* writer.lock
          .withPermit(
            Effect.uninterruptible(
              writer.connection
                .steer(input, run.turn)
                .pipe(Effect.andThen(transcripts.startWaiting(id, queued.id, run.turnId))),
            ),
          )
          .pipe(
            Effect.catch((error) =>
              Effect.logDebug("the running turn did not take the prompt; it queues").pipe(
                Effect.annotateLogs({ threadId: id, reason: error.message }),
                Effect.as(false),
              ),
            ),
          )
        return joined ? { promptId: queued.id, turnId: run.turnId } : undefined
      })

    const publishQueue = (threadId: string): Effect.Effect<void> =>
      queuedPrompts(threadId).pipe(
        Effect.map((prompts) => publish(threadId, { type: "queue.updated", prompts })),
      )

    const replayEvent = (change: TranscriptChange): ThreadEvent =>
      change.change.kind === "item"
        ? { type: "item.updated", seq: change.seq, item: change.change.item }
        : {
            type: change.change.turn.status === "running" ? "turn.started" : "turn.completed",
            seq: change.seq,
            turn: change.change.turn,
          }

    /** Replace the copy with what the provider has; empty never wipes. A
     * chat thread's copy is ATC's own record (the header): never replaced,
     * whichever trigger asks — the one guard every re-read path shares. */
    const rereadRecord = (
      record: ThreadRecord,
    ): Effect.Effect<void, ProviderUnavailable | ProviderSessionConflict> =>
      Effect.gen(function* () {
        if (record.kind !== "tui") return
        const adapter = yield* requireAdapter(record)
        const providerSessionId = record.providerSessionId
        if (providerSessionId === undefined) return
        const history = yield* adapter
          .readHistory({ providerSessionId, cwd: record.workingDirectory })
          .pipe(Effect.mapError(mapAgentError(record)))
        // Nothing to replace with is nothing to replace: a swept transcript
        // keeps ATC's copy readable, and an empty copy stays quiet.
        if (history.length === 0) return
        const { seq, snapshotVersion } = yield* transcripts.replace(record.id, history)
        publish(record.id, { type: "snapshot.invalidated", seq, snapshotVersion })
        yield* events.publish({ resource: "thread", id: record.id, change: "updated" })
      })

    // --- Activity ledger ------------------------------------------------------------

    const noteActivity = (
      record: ThreadRecord,
      activity: AgentActivity,
    ): Effect.Effect<{ readonly previous: AgentActivity | undefined }> =>
      Effect.gen(function* () {
        const previous = liveActivity.get(record.id)
        liveActivity.set(record.id, activity)
        // The first busy confirms (idempotent in the repository): a
        // submitted prompt is durable provider history worth protecting.
        if (isBusy(activity) && (previous === undefined || !isBusy(previous))) {
          yield* repository.confirm(record.id)
        }
        // The busy→idle drop IS the turn-finished signal (ATC-160): stamp
        // before publishing so the refetch already sees the thread unread.
        if (previous !== undefined && isBusy(previous) && activity === "idle") {
          yield* repository.markFinished(record.id)
        }
        yield* naming.noteActivity(record, previous, activity)
        if (previous !== activity) {
          yield* events.publish({ resource: "thread", id: record.id, change: "updated" })
        }
        return { previous }
      })

    // --- The turn loop ---------------------------------------------------------------

    const mapAgentError =
      (record: ThreadRecord) => (error: { readonly _tag: string; readonly message: string }) =>
        error._tag === "AgentUnavailable" || error._tag === "AgentProtocolError"
          ? new ProviderUnavailable({ agentId: record.agentId, reason: error.message })
          : new ProviderSessionConflict({ threadId: record.id, reason: error.message })

    /** Close every parked request of a run (its turn ended). */
    const closeRequests = (threadId: string, run: Run): void => {
      for (const requestId of run.requests.keys())
        publish(threadId, { type: "request.closed", requestId })
      run.requests.clear()
    }

    /**
     * Begin the writer's close unless begun (the mark is set at once:
     * nothing starts on it, its busy evidence is ignored) and return the
     * latch its close completes: the scope closes in the service scope —
     * the drain fiber, the connection, the process behind it — and the
     * writer deregisters once that is done (a prompt admitted meanwhile
     * waits out the latch rather than racing the connection). One close per writer,
     * shared by every caller: a second Scope.close of a closing scope
     * returns before the first's finalizers are done, and a TUI must not
     * launch on a process still shutting down.
     */
    const beginClose = (id: string, writer: Writer): Effect.Effect<Deferred.Deferred<void>> =>
      Effect.suspend(() => {
        if (writer.closing !== undefined) return Effect.succeed(writer.closing)
        const closing = Deferred.makeUnsafe<void>()
        writer.closing = closing
        return Scope.close(writer.scope, Exit.void).pipe(
          // Deregister and release the waiters however the close ends — an
          // interrupted close (shutdown) must not leave them parked.
          Effect.ensuring(
            Effect.sync(() => {
              if (writers.get(id) === writer) writers.delete(id)
            }).pipe(Effect.andThen(Deferred.succeed(closing, void 0))),
          ),
          Effect.forkIn(serviceScope),
          Effect.as(closing),
        )
      })

    /** Close the writer and wait until it is closed and deregistered. Not
     * for the writer's own fibers — the close interrupts them. */
    const closeWriter = (id: string, writer: Writer): Effect.Effect<void> =>
      beginClose(id, writer).pipe(Effect.flatMap(Deferred.await))

    /**
     * Close the writer from one of its own fibers (the drain, the idle
     * timer): the close begins now; the wait for it, then `then`, then the
     * next queued prompt (on a fresh connection) and a wanted TUI's return
     * run in the service scope. Uninterruptible from the begin to the fork:
     * the close interrupts this very fiber, and the continuation is what
     * starts a prompt admitted during the close.
     */
    const dropWriter = (
      id: string,
      writer: Writer,
      then: Effect.Effect<void> = Effect.void,
    ): Effect.Effect<void> =>
      Effect.uninterruptible(
        beginClose(id, writer).pipe(
          Effect.flatMap((closing) =>
            Deferred.await(closing).pipe(
              Effect.andThen(then),
              Effect.andThen(startNextLogged(id)),
              Effect.andThen(relaunchTui(id)),
              Effect.forkIn(serviceScope),
            ),
          ),
          Effect.asVoid,
        ),
      )

    /**
     * Bounded residency (the header): after `residentIdleTimeout` an idle
     * resident writer closes, under the start lock — a prompt admitted
     * after that resumes afresh. A wait that finds the ledger busy
     * (background work on the connection) waits again. One timer per
     * writer: a turn starting on it interrupts the timer, and the timer
     * lives in the writer's scope, so any other close ends it.
     */
    const armIdleClose = (record: ThreadRecord, writer: Writer): Effect.Effect<void> =>
      Effect.gen(function* () {
        while (true) {
          yield* Effect.sleep(residentIdleTimeout)
          const closed = yield* withStartLock(record.id)(
            Effect.gen(function* () {
              if (writer.closing !== undefined || writer.run !== undefined) return true
              if (isBusy(liveActivity.get(record.id) ?? "unknown")) return false
              yield* Effect.logDebug("closing the resident connection: idle past the timeout").pipe(
                Effect.annotateLogs({ threadId: record.id }),
              )
              yield* dropWriter(record.id, writer)
              return true
            }),
          )
          if (closed) return
        }
      }).pipe(
        Effect.forkIn(writer.scope),
        Effect.map((fiber) => {
          writer.idleClose = fiber
        }),
      )

    /**
     * End a run: the turn row takes its outcome, parked requests close, and
     * the Run leaves the writer. A shared-server provider's writer drops
     * with it (nothing is held between turns), forcing the ledger idle —
     * nothing can report on it any more. A one-process provider's stays
     * resident — the next queued prompt starts on it (or a wanted TUI
     * takes over), the idle timer bounds it — and the ledger takes the
     * connection's own snapshot: idle, or the background work that outlives
     * the turn, whose idle then lands on this same feed.
     */
    const finish = (
      record: ThreadRecord,
      writer: Writer,
      run: Run,
      status: "completed" | "interrupted" | "failed",
      detail?: string,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (run.finished) return
        run.finished = true
        closeRequests(record.id, run)
        // A turn ending mid-stream keeps the text accumulated so far: one
        // final flush before the turn row closes, then the ledger clears —
        // an item's final state is the adapter's to send (or a re-read's).
        yield* Effect.forEach([...writer.streaming.keys()], (itemId) =>
          flushStreaming(record, writer, itemId),
        )
        writer.streaming.clear()
        // The upsert keeps the row's promptId/startedAt (COALESCE).
        yield* recordTurn(
          record.id,
          {
            id: run.turnId,
            status,
            ...(status === "failed" && detail !== undefined ? { error: detail } : {}),
            endedAt: new Date().toISOString(),
          },
          "native",
        )
        writer.run = undefined
        if (!oneProcess(record)) {
          yield* noteActivity(record, "idle")
          return yield* dropWriter(record.id, writer)
        }
        yield* noteActivity(record, yield* writer.connection.activity)
        yield* armIdleClose(record, writer)
        yield* startNextLogged(record.id).pipe(
          Effect.andThen(relaunchTui(record.id)),
          Effect.forkIn(serviceScope),
        )
      })

    /**
     * Provider state wins (the header's settings bullet): merge what the
     * provider reported — minus what merely confirms the writer's own push
     * — over the row as it stands NOW, never over the record the feed was
     * opened with, which a client patch may have moved on from; publish
     * only a real change. The write is the repository's compare-and-swap:
     * a client patch landing between the read and the write makes it miss,
     * and the report is re-merged on top of that patch rather than over
     * it. A vanished row (a delete racing the feed) is a no-op.
     */
    const adoptSettings = (
      record: ThreadRecord,
      reported: ProviderSettings,
      pushed: ThreadSettings | undefined,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        for (let attempt = 0; attempt < 8; attempt++) {
          const current = yield* repository.get(record.id)
          if (Option.isNone(current)) return
          const adopted = applyProviderSettings(current.value.settings, reported, pushed)
          if (adopted === undefined) return
          const updated = yield* repository.setSettingsIfUnchanged(
            record.id,
            current.value.settings,
            adopted,
          )
          if (Option.isSome(updated)) {
            return yield* events.publish({ resource: "thread", id: record.id, change: "updated" })
          }
        }
        yield* Effect.logWarning("a provider settings report never settled against the row").pipe(
          Effect.annotateLogs({ threadId: record.id }),
        )
      })

    /**
     * Keep the writer's streaming ledger current from the adapter's own
     * item events: a text item that is not complete is (re)based — the
     * adapter re-sends the whole item, and recordItem persists it right
     * after — and a completed one (or any complete re-send) leaves the
     * ledger, its final state coming from the adapter.
     */
    const trackStreaming = (writer: Writer, event: AgentItemEvent): void => {
      const item = event.item
      if (item.type !== "assistantText" && item.type !== "reasoning") return
      if (event.type === "itemCompleted" || item.complete) {
        writer.streaming.delete(item.id)
        return
      }
      writer.streaming.set(item.id, { base: item, text: item.text, pending: 0, armed: false })
    }

    /** Persist and publish the text so far (an item.updated, complete still
     * false). Callers hold the writer's lock. An entry that flushed early
     * (the length cap) or completed leaves nothing to do. */
    const flushStreaming = (record: ThreadRecord, writer: Writer, itemId: string) =>
      Effect.suspend(() => {
        const entry = writer.streaming.get(itemId)
        if (entry === undefined || entry.pending === 0) return Effect.void
        entry.pending = 0
        return recordItem(record.id, {
          type: "itemUpdated",
          item: { ...entry.base, text: entry.text },
        })
      })

    /** Every delta stays live-only on the wire; the accumulated text
     * persists on the throttle (or at the length cap), the header's
     * durable-partials rule. Only the timer itself disarms, so an item has
     * at most one timer in flight. */
    const onTextDelta = (
      record: ThreadRecord,
      writer: Writer,
      event: { readonly itemId: string; readonly delta: string },
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        publish(record.id, { type: "text.delta", itemId: event.itemId, delta: event.delta })
        const entry = writer.streaming.get(event.itemId)
        if (entry === undefined) return
        entry.text += event.delta
        entry.pending += event.delta.length
        if (entry.pending >= STREAMING_FLUSH_LENGTH) {
          return yield* flushStreaming(record, writer, event.itemId)
        }
        if (entry.armed) return
        entry.armed = true
        yield* Effect.sleep(streamingFlushInterval).pipe(
          Effect.andThen(
            writer.lock.withPermit(
              Effect.suspend(() => {
                const armed = writer.streaming.get(event.itemId)
                if (armed !== undefined) armed.armed = false
                return flushStreaming(record, writer, event.itemId)
              }),
            ),
          ),
          Effect.forkIn(writer.scope),
        )
      })

    const handleEvent = (
      record: ThreadRecord,
      writer: Writer,
      event: AgentEvent,
    ): Effect.Effect<void> => {
      const run = writer.run
      switch (event.type) {
        case "activity":
          return writer.closing !== undefined && isBusy(event.activity)
            ? Effect.void
            : noteActivity(record, event.activity).pipe(Effect.asVoid)
        case "itemStarted":
        case "itemUpdated":
        case "itemCompleted":
          return Effect.sync(() => trackStreaming(writer, event)).pipe(
            Effect.andThen(recordItem(record.id, event)),
          )
        case "textDelta":
          return onTextDelta(record, writer, event)
        case "requestOpened":
          // A request belongs to a turn (the seam parks it with one); with
          // no Run of ours to hold it, nobody could answer it.
          return Effect.sync(() => {
            if (run === undefined) return
            run.requests.set(event.request.id, event.request)
            publish(record.id, { type: "request.opened", request: event.request })
          })
        case "requestClosed":
          return Effect.sync(() => {
            if (run?.requests.delete(event.requestId) !== true) return
            publish(record.id, { type: "request.closed", requestId: event.requestId })
          })
        case "settings":
          return adoptSettings(record, event.settings, writer.pushed)
        case "turnStarted": {
          if (event.turnId === run?.turnId) return Effect.void
          // A turn ATC did not start. On a one-process provider the held
          // connection IS the session's only process, so the turn is ours
          // to run (the provider woke itself: the header's provider-started
          // bullet); on a shared server it is another writer's — a TUI
          // mid-run — and only observed.
          if (run === undefined && oneProcess(record)) {
            return adoptTurn(record, writer, { turnId: event.turnId })
          }
          return recordTurn(
            record.id,
            { id: event.turnId, status: "running", startedAt: new Date().toISOString() },
            "observed",
          )
        }
        case "turnCompleted":
          return run !== undefined && event.turnId === run.turnId
            ? finish(record, writer, run, event.outcome, event.detail)
            : recordTurn(
                record.id,
                { id: event.turnId, status: event.outcome, endedAt: new Date().toISOString() },
                "observed",
              )
      }
    }

    /**
     * The feed ended on its own — cleanly (the provider ended the session:
     * a non-success turn on Claude, a resident child that exited) or with
     * a failure (transport loss). With a run still open ATC no longer
     * knows how that turn is doing — the provider may still be running it
     * — so the run is dropped without guessing an outcome and the
     * provider's history re-read for the truth (a turn history still calls
     * running is the startup pass's job if the provider stays
     * unreachable). Either way nothing can report on the connection any
     * more: the writer drops, and the next prompt resumes afresh.
     */
    const writerEnded = (
      record: ThreadRecord,
      writer: Writer,
      reason: string | undefined,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        const run = writer.run
        if (run === undefined || run.finished) {
          yield* noteActivity(record, "idle")
          yield* Effect.logDebug("the resident connection ended").pipe(
            Effect.annotateLogs({ threadId: record.id, reason: reason ?? "the feed ended" }),
          )
          return yield* dropWriter(record.id, writer)
        }
        run.finished = true
        writer.run = undefined
        closeRequests(record.id, run)
        // A chat thread's transcript takes only what ATC saw (the header):
        // the text streamed so far is kept (the last flush, as `finish`
        // does), the turn ends failed with the reason, and nothing is
        // re-read.
        if (record.kind === "chat") {
          yield* writer.lock.withPermit(
            Effect.forEach([...writer.streaming.keys()], (itemId) =>
              flushStreaming(record, writer, itemId),
            ),
          )
          writer.streaming.clear()
          yield* recordTurn(
            record.id,
            {
              id: run.turnId,
              status: "failed",
              error: `the provider connection ended mid-turn: ${reason ?? "the feed ended"}`,
              endedAt: new Date().toISOString(),
            },
            "native",
          )
          yield* noteActivity(record, "idle")
          yield* naming.noteFeedEnded(record)
          return yield* dropWriter(record.id, writer)
        }
        yield* noteActivity(record, "unknown")
        yield* naming.noteFeedEnded(record)
        yield* Effect.logWarning("the turn's connection failed; re-reading provider history").pipe(
          Effect.annotateLogs({
            threadId: record.id,
            turnId: run.turnId,
            reason: reason ?? "the feed ended without a turn end",
          }),
        )
        yield* dropWriter(
          record.id,
          writer,
          rereadRecord(record).pipe(
            Effect.catch((error) =>
              Effect.logWarning("re-read after a failed connection failed").pipe(
                Effect.annotateLogs({ threadId: record.id, reason: error.message }),
              ),
            ),
          ),
        )
      })

    /**
     * Register a writer and fork its drain (in the writer's scope: ATC's
     * own closes interrupt it before the feed ends, so only a feed that
     * ends or fails on its own reaches `writerEnded`). Every event is
     * handled under the writer's lock — the same lock a turn start holds
     * across startTurn and Run registration.
     */
    const startWriter = (record: ThreadRecord, writer: Writer): Effect.Effect<void> =>
      Effect.gen(function* () {
        writers.set(record.id, writer)
        // The connection's snapshot seeds the ledger so a read racing the
        // first feed event already sees what the provider says.
        yield* noteActivity(record, yield* writer.connection.activity)
        yield* writer.connection.events.pipe(
          Stream.runForEach((event) => writer.lock.withPermit(handleEvent(record, writer, event))),
          Effect.andThen(Effect.suspend(() => writerEnded(record, writer, undefined))),
          Effect.catch((error) => writerEnded(record, writer, error.message)),
          Effect.forkIn(writer.scope),
        )
      })

    const makeWriter = (scope: Scope.Closeable, connection: AgentConnection): Writer => ({
      scope,
      connection,
      lock: Semaphore.makeUnsafe(1),
      run: undefined,
      closing: undefined,
      idleClose: undefined,
      pushed: undefined,
      streaming: new Map(),
    })

    /**
     * Resume the confirmed session and start the turn on it, in a child of
     * the writer's scope. `lost` when the provider no longer has the
     * session — AgentResumeFailed on the resume (Codex), or on the first
     * turn after it (Claude verifies there): the header's lost-session
     * rule — with the child closed, so the dead connection never lingers
     * in the writer's scope while the caller starts afresh. A turn the
     * provider refuses because one is already running on the session —
     * another Codex client's — is the header's busy-elsewhere refusal.
     */
    const resumeConfirmed = (
      record: ThreadRecord,
      adapter: AgentAdapter,
      input: TurnInput,
      scope: Scope.Closeable,
      providerSessionId: string,
    ): Effect.Effect<
      { readonly connection: AgentConnection; readonly turn: AgentTurn } | { readonly lost: true },
      ProviderUnavailable | ProviderSessionConflict
    > =>
      Effect.suspend(() => {
        const child = Scope.forkUnsafe(scope)
        return Effect.gen(function* () {
          const connection = yield* adapter.resumeSession({
            providerSessionId,
            cwd: record.workingDirectory,
            settings: record.settings,
          })
          const turn = yield* connection.startTurn(input, record.settings).pipe(
            Effect.catchTag("AgentConflict", (error) =>
              Effect.fail(
                new ProviderSessionConflict({
                  threadId: record.id,
                  reason: `the provider is already running a turn on this thread (another client started it); the prompt was not queued — retry once that turn ends. ${error.reason}`,
                }),
              ),
            ),
          )
          return { connection, turn }
        }).pipe(
          Scope.provide(child),
          Effect.catchTag("AgentResumeFailed", (error) =>
            Scope.close(child, Exit.void).pipe(
              Effect.andThen(
                Effect.logWarning(
                  "the provider no longer has the thread's session; starting afresh",
                ).pipe(
                  Effect.annotateLogs({
                    threadId: record.id,
                    providerSessionId,
                    reason: error.reason,
                  }),
                ),
              ),
              Effect.as({ lost: true as const }),
            ),
          ),
          Effect.mapError((error) =>
            error._tag === "ProviderSessionConflict" ? error : mapAgentError(record)(error),
          ),
        )
      })

    /**
     * Open a fresh writer for the prompt: a confirmed thread resumes its
     * exact session; anything else — an unconfirmed thread, or a confirmed
     * one whose session the provider lost (resumeConfirmed) — creates a
     * fresh one (the second `materialize` caller ATC-124 anticipated):
     * identity is persisted — and confirmed, the prompt is durable
     * provider history — before the caller registers the run. `lost` tells
     * the caller to open the turn with the notice. The connection lives in
     * a child of the service scope; a failure here closes it again.
     */
    const openWriter = (
      record: ThreadRecord,
      adapter: AgentAdapter,
      input: TurnInput,
    ): Effect.Effect<
      {
        readonly writer: Writer
        readonly turn: AgentTurn
        readonly record: ThreadRecord
        readonly lost?: true
      },
      ProviderUnavailable | ProviderSessionConflict
    > =>
      Effect.suspend(() => {
        const scope = Scope.forkUnsafe(serviceScope)
        return Effect.gen(function* () {
          const cwd = record.workingDirectory
          const confirmed =
            record.providerSessionId !== undefined && record.confirmedAt !== undefined
              ? yield* resumeConfirmed(record, adapter, input, scope, record.providerSessionId)
              : undefined
          if (confirmed !== undefined && "connection" in confirmed) {
            return {
              writer: { ...makeWriter(scope, confirmed.connection), pushed: record.settings },
              turn: confirmed.turn,
              record,
            }
          }
          // Unconfirmed (none, or zero completed turns) or lost: a fresh
          // session, as openTerminal's materialize does — there is no
          // history to protect (or none the provider still has), and the
          // superseded session's adapter resources go.
          if (record.providerSessionId !== undefined) {
            yield* adapter.releaseSession({
              providerSessionId: record.providerSessionId,
              providerMetadata: record.providerMetadata,
            })
          }
          const session = yield* adapter
            .createSession({ cwd, input, settings: record.settings })
            .pipe(Effect.mapError(mapAgentError(record)))
          const still = yield* repository.get(record.id)
          if (Option.isNone(still)) {
            return yield* Effect.die(new Error(`thread ${record.id} vanished mid-prompt`))
          }
          const adopted = yield* repository.setProviderSession(
            record.id,
            session.connection.providerSessionId,
            null,
          )
          yield* repository.confirm(record.id)
          yield* events.publish({ resource: "thread", id: record.id, change: "updated" })
          return {
            writer: { ...makeWriter(scope, session.connection), pushed: record.settings },
            turn: session.turn,
            record: adopted,
            ...(confirmed !== undefined ? { lost: true as const } : {}),
          }
        }).pipe(
          Scope.provide(scope),
          Effect.onExit((exit) =>
            exit._tag === "Failure" ? Scope.close(scope, Exit.void) : Effect.void,
          ),
        )
      })

    /** The lost-session line (the header). */
    const recordLostSessionNotice = (threadId: string, turnId: string): Effect.Effect<void> =>
      recordItem(threadId, {
        type: "itemCompleted",
        item: {
          type: "notice",
          id: `${turnId}-lost-session`,
          turnId,
          text: "Previous session was lost; the agent will not remember earlier turns.",
        },
      })

    /**
     * Start the oldest waiting prompt if the thread can take one: no turn
     * running, not recovering, not archived, and — unless a resident writer
     * is held (its feed is the ledger; the connection takes a turn whenever
     * no turn is on it) — not busy under another surface (the ledger's word:
     * a TUI turn in progress queues us until the observed idle drains).
     * A resident writer takes the turn without reopening the session; one
     * the provider has ended since closes here and a fresh resume follows.
     * Under the per-thread start lock, with the record re-read inside it,
     * so two prompts cannot both start and an archive or delete that won
     * the lock first is honored.
     */
    const startNext = (
      id: string,
    ): Effect.Effect<
      Option.Option<{ readonly promptId: string; readonly turnId: string }>,
      ProviderUnavailable | ProviderSessionConflict
    > =>
      withStartLock(id)(
        Effect.gen(function* () {
          const registered = writers.get(id)
          if (registered?.run !== undefined || recovering.has(id)) return Option.none()
          // A writer mid-close (a shared-server turn just ended, the idle
          // timer fired): wait out its latch here, under the lock, and
          // resume afresh — so the caller's own call starts its prompt and
          // is answered with the turn, rather than "queued" for a start the
          // close's continuation makes moments later.
          if (registered?.closing !== undefined) yield* Deferred.await(registered.closing)
          const held = registered?.closing === undefined ? registered : undefined
          const found = yield* repository.get(id)
          if (Option.isNone(found) || found.value.archivedAt !== undefined) return Option.none()
          const record = found.value
          if (held === undefined && isBusy(liveActivity.get(id) ?? "unknown")) return Option.none()
          if (tuiOpening.has(id)) return Option.none()
          const next = yield* transcripts.peek(id)
          if (Option.isNone(next)) return Option.none()
          const adapter = yield* requireAdapter(record)
          const prompt = next.value.prompt
          const input = yield* turnInputOf(record.id, next.value)
          if (held !== undefined) {
            const reused = yield* startOnWriter(record, held, next.value, input)
            if (reused === "busy") return Option.none()
            if (Option.isSome(reused)) {
              yield* naming.notePrompt(record, prompt)
              return reused
            }
            yield* closeWriter(id, held)
          }
          // Native wins (the header's ownership rule): a live TUI on a
          // one-process provider ends before this turn resumes the session
          // — unless it turns out busy, in which case the observed idle
          // drains this prompt.
          if (!adapter.sharedServer && (yield* takeOverTui(record)) === "busy") {
            return Option.none()
          }
          const opened = yield* openWriter(record, adapter, input)
          // The adopted record predates this turn's confirm (a fresh
          // session is unconfirmed by construction), which is what makes a
          // native first prompt eligible for naming (ATC-202).
          yield* naming.notePrompt(opened.record, prompt)
          // From here the connection is ours until the writer owns it: no
          // interruption (a dropped request) may land between the two, or
          // the writer would stay open with nobody to close it.
          // The turn row first (the notice belongs to it), the lost-session
          // notice second, and only then the feed: the notice is durable
          // before any item of the provider's can land, so it opens the turn.
          yield* Effect.uninterruptible(
            registerRun(opened.record, opened.writer, next.value, opened.turn).pipe(
              Effect.andThen(
                opened.lost === undefined
                  ? Effect.void
                  : recordLostSessionNotice(opened.record.id, opened.turn.turnId),
              ),
              Effect.andThen(startWriter(opened.record, opened.writer)),
              Effect.onExit((exit) =>
                exit._tag === "Failure" && writers.get(id) !== opened.writer
                  ? Scope.close(opened.writer.scope, Exit.void)
                  : Effect.void,
              ),
            ),
          )
          return Option.some({ promptId: next.value.id, turnId: opened.turn.turnId })
        }),
      )

    /**
     * Start the prompt on a resident writer. Under the writer's lock (the
     * drain waits) and uninterruptibly: the turn's first events must find
     * its Run registered, and a turn started with no Run to end it would
     * hold the connection for good. "busy" when the connection is in a
     * turn of its own we have not seen yet (AgentConflict: the provider
     * woke itself — its turnStarted is behind this lock and will be
     * adopted next, and its end drains the queue), so the prompt simply
     * waits. `none` when the connection would not take the turn — the
     * provider ended it since (the seam: a closed or over connection
     * refuses control calls) — so the caller closes it and resumes afresh.
     */
    const startOnWriter = (
      record: ThreadRecord,
      writer: Writer,
      queued: QueuedPromptRecord,
      input: TurnInput,
    ): Effect.Effect<
      Option.Option<{ readonly promptId: string; readonly turnId: string }> | "busy"
    > =>
      writer.lock.withPermit(
        Effect.uninterruptible(
          Effect.gen(function* () {
            const turn = yield* writer.connection.startTurn(input, record.settings)
            writer.pushed = record.settings
            yield* registerRun(record, writer, queued, turn)
            return Option.some({ promptId: queued.id, turnId: turn.turnId })
          }).pipe(
            Effect.catchTag("AgentConflict", (error) =>
              Effect.logDebug(
                "the resident connection is mid-turn on its own; the prompt waits",
              ).pipe(
                Effect.annotateLogs({ threadId: record.id, reason: error.reason }),
                Effect.as("busy" as const),
              ),
            ),
            Effect.catch((error) =>
              Effect.logDebug("the resident connection refused the turn; resuming afresh").pipe(
                Effect.annotateLogs({ threadId: record.id, reason: error.message }),
                Effect.as(Option.none()),
              ),
            ),
          ),
        ),
      )

    /** The turn row (the prompt is no longer waiting), its live event, and
     * the Run on the writer — the writer's drain sees nothing of the turn
     * before this returns (startWriter forks it after; startOnWriter holds
     * the lock across it). */
    const registerRun = (
      record: ThreadRecord,
      writer: Writer,
      queued: QueuedPromptRecord,
      turn: AgentTurn,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        const row: ThreadTurn = {
          id: turn.turnId,
          status: "running",
          promptId: queued.id,
          startedAt: new Date().toISOString(),
        }
        const seq = yield* transcripts.beginTurn(record.id, queued.id, row)
        publish(record.id, { type: "turn.started", seq, turn: row })
        yield* publishQueue(record.id)
        yield* holdRun(writer, turn)
      })

    /**
     * A turn the provider started on a held one-process connection (the
     * header's provider-started bullet) becomes ATC's Run: no prompt of
     * ours behind it, so only the turn row is recorded, then it is held
     * like any other — its requests park here, `finish` ends it and
     * drains the queue. Under the writer's lock (the drain is), so it
     * never races a start on this connection.
     */
    const adoptTurn = (
      record: ThreadRecord,
      writer: Writer,
      turn: AgentTurn,
    ): Effect.Effect<void> =>
      recordTurn(
        record.id,
        { id: turn.turnId, status: "running", startedAt: new Date().toISOString() },
        "native",
      ).pipe(Effect.andThen(holdRun(writer, turn)))

    /** The Run on the writer, its row already recorded. */
    const holdRun = (writer: Writer, turn: AgentTurn): Effect.Effect<void> =>
      Effect.gen(function* () {
        // A resident writer's idle timer stands down: the turn is what it
        // was waiting for (interruptible while it waits for the start lock
        // this caller may hold, so this never deadlocks).
        if (writer.idleClose !== undefined) yield* Fiber.interrupt(writer.idleClose)
        writer.idleClose = undefined
        writer.run = { turnId: turn.turnId, turn, requests: new Map(), finished: false }
      })

    /** The drain continuation: a provider failure leaves the prompt queued. */
    const startNextLogged = (id: string): Effect.Effect<void> =>
      startNext(id).pipe(
        Effect.asVoid,
        Effect.catch((error) =>
          Effect.logWarning("the next queued prompt could not start; it stays queued").pipe(
            Effect.annotateLogs({ threadId: id, reason: error.message }),
          ),
        ),
      )

    // --- Surface ownership (the header's one-process rule) ---------------------------

    /**
     * End the thread's TUI terminal, its last turn on disk first: the
     * settled re-read is the gate (Claude appends the assistant line after
     * the Stop hook that reported idle) — and it lands the turn in the copy
     * — then the kill. A read that fails leaves the TUI alive: unverified
     * is not "on disk". So does a TUI that went busy during the read (a
     * prompt typed into it meanwhile): "busy", and it keeps the thread
     * until its idle.
     */
    const endTui = (
      record: ThreadRecord,
      terminalId: string,
    ): Effect.Effect<
      "ended" | "busy",
      ProviderUnavailable | ProviderSessionConflict | ZmxUnavailable
    > =>
      Effect.gen(function* () {
        yield* rereadRecord(record)
        if (isBusy(liveActivity.get(record.id) ?? "unknown")) return "busy"
        yield* tui.end(terminalId)
        return "ended"
      })

    /** A native turn is about to start on a one-process provider: a live
     * TUI (idle — the caller checked the ledger) ends first. "busy" when
     * it is not idle after all: the turn waits for the observed idle. */
    const takeOverTui = (
      record: ThreadRecord,
    ): Effect.Effect<"taken" | "busy", ProviderUnavailable | ProviderSessionConflict> =>
      Effect.gen(function* () {
        const linked = yield* tui.linked(record)
        if (linked === undefined) return "taken"
        const outcome = yield* endTui(record, linked.id).pipe(
          Effect.catchTag("ZmxUnavailable", (error) =>
            Effect.fail(
              new ProviderUnavailable({
                agentId: record.agentId,
                reason: `the thread's TUI could not be ended: ${error.message}`,
              }),
            ),
          ),
        )
        return outcome === "ended" ? "taken" : "busy"
      })

    /**
     * The TUI takes the session over from a resident writer between turns:
     * the connection (and the process behind it) is gone before the TUI's
     * starts, and so is whatever it was doing — the ledger drops to idle,
     * because nothing can report on it any more (a stale busy would queue
     * every later native prompt behind work that no longer exists).
     */
    const handOff = (record: ThreadRecord, writer: Writer): Effect.Effect<void> =>
      closeWriter(record.id, writer).pipe(Effect.andThen(noteActivity(record, "idle")))

    /**
     * The native side is done (no turn running, nothing queued): a TUI
     * clients still want comes back on the thread's confirmed session — a
     * resident writer closes first (one live process). Best-effort — a
     * failed relaunch is logged, and the next open launches it. Under the
     * start lock so it cannot interleave with a prompt starting.
     */
    const relaunchTui = (id: string): Effect.Effect<void> =>
      withStartLock(id)(
        Effect.gen(function* () {
          const held = writers.get(id)
          if (!tuiWanted.has(id) || held?.run !== undefined || tuiOpening.has(id)) return
          const found = yield* repository.get(id)
          if (Option.isNone(found) || found.value.archivedAt !== undefined) return
          const record = found.value
          const adapter = adapterFor(record)
          if (adapter === undefined || adapter.sharedServer) return
          if (record.providerSessionId === undefined || record.confirmedAt === undefined) return
          // A waiting prompt drains first (or stays queued on a provider
          // failure); the TUI comes back only once the queue is empty.
          if (Option.isSome(yield* transcripts.peek(id))) return
          if ((yield* tui.linked(record)) !== undefined) return
          if (held !== undefined) yield* handOff(record, held)
          const { record: current } = yield* tui.reopen(record, adapter)
          yield* observe(current)
          yield* events.publish({ resource: "thread", id, change: "updated" })
        }).pipe(
          Effect.catch((error) =>
            Effect.logWarning("the TUI could not be relaunched; the next open launches it").pipe(
              Effect.annotateLogs({ threadId: id, reason: error.message }),
            ),
          ),
        ),
      )

    const openTui: ThreadRuntime["Service"]["openTui"] = (record, launch) =>
      Effect.gen(function* () {
        tuiWanted.add(record.id)
        tuiSettled.add(record.id)
        // The refusal, the linked re-check, the resident writer's close,
        // and the launch claim are one step under the start lock: a prompt
        // cannot start a run between them, and a relaunch (which holds the
        // lock while it launches) is seen as the live terminal it produced.
        const claimed = yield* withStartLock(record.id)(
          Effect.gen(function* () {
            const held = writers.get(record.id)
            const linked = yield* tui.linked(record)
            if (linked !== undefined) return linked
            if (!oneProcess(record)) return undefined
            if (held !== undefined) yield* handOff(record, held)
            tuiOpening.add(record.id)
            return undefined
          }),
        )
        if (claimed !== undefined) return claimed
        if (!oneProcess(record)) return yield* launch
        return yield* launch.pipe(
          Effect.ensuring(
            Effect.sync(() => tuiOpening.delete(record.id)).pipe(
              // Prompts that queued behind the launch drain now (native
              // wins: they take the fresh TUI over).
              Effect.andThen(startNextLogged(record.id)),
            ),
          ),
        )
      })

    // Under the start lock: a relaunch in flight finishes first (and is
    // then ended here), and none starts once the mark is gone.
    const closeTui: ThreadRuntime["Service"]["closeTui"] = (record) =>
      withStartLock(record.id)(
        Effect.gen(function* () {
          tuiWanted.delete(record.id)
          tuiSettled.add(record.id)
          if (!oneProcess(record)) return
          // Busy: the observed idle ends it (observedIdle below).
          if (isBusy(liveActivity.get(record.id) ?? "unknown")) return
          const linked = yield* tui.linked(record)
          if (linked === undefined) return
          yield* endTui(record, linked.id)
        }),
      )

    // --- Observation of TUI-driven sessions ---------------------------------------

    /**
     * The thread went idle under observation — a TUI turn's end (a native
     * turn's own idle never comes this way: observed activity is ignored
     * while the runtime drives): re-read the provider's history before the
     * queue drains, so the next turn's live items survive it; then end a
     * TUI no client wants any more (a Chat switch while it was busy) — the
     * settled read that just landed is its on-disk gate, so a failed read
     * leaves it running; then drain. The read and the end hold the start
     * lock: a prompt admitted meanwhile starts after them, so the replaced
     * copy can never wipe a native turn's fresh rows.
     */
    const observedIdle = (record: ThreadRecord): Effect.Effect<void> =>
      withStartLock(record.id)(
        Effect.gen(function* () {
          if (writers.has(record.id)) return
          const settled = yield* rereadRecord(record).pipe(
            Effect.as(true),
            Effect.catch((error) =>
              Effect.logDebug("re-read after observed idle failed").pipe(
                Effect.annotateLogs({ threadId: record.id, reason: error.message }),
                Effect.as(false),
              ),
            ),
          )
          if (!settled || !oneProcess(record) || tuiWanted.has(record.id)) return
          // A prompt typed into the TUI while the read was in flight keeps
          // the thread (the rule endTui applies); its own idle ends it.
          if (isBusy(liveActivity.get(record.id) ?? "unknown")) return
          const linked = yield* tui.linked(record)
          if (linked === undefined) return
          yield* tui
            .end(linked.id)
            .pipe(
              Effect.catch((error) =>
                Effect.logWarning("the closed TUI could not be ended at its idle").pipe(
                  Effect.annotateLogs({ threadId: record.id, reason: error.message }),
                ),
              ),
            )
        }),
      ).pipe(Effect.andThen(startNextLogged(record.id)))

    const unobserve = (id: string): Effect.Effect<void> =>
      Effect.gen(function* () {
        // The ledger entry goes with the observation — unless the runtime
        // holds a writer on the thread, whose own evidence stays authoritative.
        if (!writers.has(id)) liveActivity.delete(id)
        tuiWanted.delete(id)
        tuiSettled.delete(id)
        const observation = observed.get(id)
        if (observation === undefined) return
        observed.delete(id)
        yield* Scope.close(observation.scope, Exit.void)
      })

    const drainObserved = (record: ThreadRecord, event: AgentSessionEvent): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (event.type === "userPrompt") return yield* naming.notePrompt(record, event.text)
        // While the runtime holds a writer, its feed already carries every
        // item, every activity edge, and every settings report; the observer
        // copy of a shared server's fan-out would double the items, and a
        // one-process provider's dead TUI can only report its own SessionEnd.
        if (writers.has(record.id)) return
        if (event.type === "settings")
          return yield* adoptSettings(record, event.settings, undefined)
        if (event.type !== "activity") return yield* recordItem(record.id, event)
        const { previous } = yield* noteActivity(record, event.activity)
        // The busy→idle drop is the turn's end (forked: the drain must never
        // wait on the provider).
        if (previous !== undefined && isBusy(previous) && event.activity === "idle") {
          yield* observedIdle(record).pipe(Effect.forkIn(serviceScope))
        }
      })

    /**
     * Start (once) the thread's normalized session subscription. A TUI is
     * live or launching whenever this is called, so the FIRST observation
     * of a thread no client has spoken for marks the TUI wanted (a live TUI
     * found after a restart stays in charge until a client says otherwise).
     */
    const observe = (record: ThreadRecord): Effect.Effect<void> =>
      Effect.gen(function* () {
        // A chat thread is never observed (the header), whatever a read
        // finds linked to it.
        if (record.kind !== "tui") return
        const adapter = adapterFor(record)
        const providerSessionId = record.providerSessionId
        if (adapter === undefined || providerSessionId === undefined) return
        const existing = observed.get(record.id)
        if (existing?.providerSessionId === providerSessionId) return
        // A superseded session's subscription must never keep driving the
        // thread (its feed would confirm a session it never saw).
        if (existing !== undefined) yield* unobserve(record.id)
        // The re-check, unsafe fork, and claim are ONE synchronous step:
        // concurrent first reads of the same live thread (sidebar list +
        // detail get at relaunch) race exactly here, and a loser that
        // proceeded would leak its adapter subscription until shutdown.
        // Whoever claimed during the unobserve above wins, whatever
        // session it observes — the next read re-checks and heals.
        if (observed.get(record.id) !== undefined) return
        const child = Scope.forkUnsafe(serviceScope)
        const observation: Observation = { providerSessionId, scope: child }
        observed.set(record.id, observation)
        if (!tuiSettled.has(record.id)) {
          tuiWanted.add(record.id)
          tuiSettled.add(record.id)
        }
        const releaseClaim = Effect.gen(function* () {
          if (observed.get(record.id) !== observation) return
          observed.delete(record.id)
          yield* Scope.close(child, Exit.void)
        })
        yield* Effect.gen(function* () {
          // The subscription is established HERE, before the caller
          // proceeds — only the drain loop is forked. An unavailable
          // evidence source yields an empty feed: `unknown` on reads,
          // never a guess or a crash.
          const stream = yield* adapter
            .observeSession({ providerSessionId, providerMetadata: record.providerMetadata })
            .pipe(
              Effect.catchTag("AgentUnavailable", () =>
                Effect.succeed(Stream.empty as Stream.Stream<AgentSessionEvent>),
              ),
              Scope.provide(child),
            )
          yield* stream.pipe(
            Stream.runForEach((event) => drainObserved(record, event)),
            // A feed that ends on its own must not pin the entry (the next
            // read re-subscribes instead of trusting a dead stream) nor
            // leak its child scope into the service scope. An ended
            // observation also ends the refinement's wait — its turn-end
            // evidence is gone, so the catch-up must not park forever.
            Effect.ensuring(naming.noteFeedEnded(record).pipe(Effect.andThen(releaseClaim))),
            Effect.forkIn(child),
          )
        }).pipe(
          // A caller interrupted (a dropped read) or failing before the
          // drain fiber arms must not leave a claim with no consumer — that
          // would freeze the thread's activity until shutdown.
          Effect.onExit((exit) => (exit._tag === "Failure" ? releaseClaim : Effect.void)),
        )
      })

    // --- Startup ---------------------------------------------------------------------

    /**
     * Follow a turn still running on a shared provider server after a
     * restart: resume the session and drain its feed to the turn's end.
     * A session that is already idle has nothing to follow — the copy is
     * re-read once more, and a turn history still calls running is marked
     * interrupted.
     */
    const reattach = (record: ThreadRecord, turn: ThreadTurn): Effect.Effect<void> =>
      Effect.gen(function* () {
        const adapter = yield* requireAdapter(record)
        const providerSessionId = record.providerSessionId
        if (providerSessionId === undefined) return yield* markInterrupted(record, turn)
        const scope = Scope.forkUnsafe(serviceScope)
        const connection = yield* adapter
          .resumeSession({
            providerSessionId,
            cwd: record.workingDirectory,
            settings: record.settings,
          })
          .pipe(Scope.provide(scope))
        if (!isBusy(yield* connection.activity)) {
          yield* Scope.close(scope, Exit.void)
          yield* rereadRecord(record)
          const still = (yield* transcripts.runningTurns(record.id)).find(
            (entry) => entry.id === turn.id,
          )
          if (still !== undefined) yield* markInterrupted(record, still)
          return
        }
        // The turn's own pushed baseline died with the old process, so the
        // provider's first report after reattach is adopted as its word —
        // right unless a setting changed mid-turn before the restart, when
        // the row briefly follows the running turn again (accepted: the
        // window is a server restart inside a running turn).
        const writer = makeWriter(scope, connection)
        writer.run = {
          turnId: turn.id,
          turn: { turnId: turn.id },
          requests: new Map(),
          finished: false,
        }
        yield* startWriter(record, writer)
      }).pipe(
        Effect.catch((error) =>
          Effect.logWarning("could not reattach to a running turn; marking it interrupted").pipe(
            Effect.annotateLogs({ threadId: record.id, turnId: turn.id, reason: error.message }),
            Effect.andThen(markInterrupted(record, turn)),
          ),
        ),
      )

    const markInterrupted = (record: ThreadRecord, turn: ThreadTurn): Effect.Effect<void> =>
      recordTurn(
        record.id,
        { ...turn, status: "interrupted", endedAt: new Date().toISOString() },
        "native",
      )

    const recoverThread = (id: string): Effect.Effect<void> =>
      withStartLock(id)(
        Effect.gen(function* () {
          const found = yield* repository.get(id)
          if (Option.isNone(found)) return
          const record = found.value
          const running = yield* transcripts.runningTurns(id)
          if (running.length === 0) return
          yield* rereadRecord(record).pipe(
            Effect.catch((error) =>
              Effect.logWarning("startup re-read failed").pipe(
                Effect.annotateLogs({ threadId: id, reason: error.message }),
              ),
            ),
          )
          const still = yield* transcripts.runningTurns(id)
          const adapter = adapterFor(record)
          for (const turn of still) {
            // A shared provider server (the observation-outlives-TUI shape)
            // keeps running our turn across ATC's restart; anything else
            // died with the process.
            yield* adapter?.sharedServer === true
              ? reattach(record, turn)
              : markInterrupted(record, turn)
          }
        }),
      ).pipe(
        // Settled (or given up): prompts may start, beginning with the
        // ones that waited — outside the lock, which startNext takes.
        Effect.ensuring(Effect.sync(() => recovering.delete(id))),
        Effect.andThen(startNextLogged(id)),
      )

    yield* Effect.forEach([...recovering], recoverThread, { concurrency: 4 }).pipe(
      Effect.catchCause((cause) =>
        Effect.logWarning("startup thread recovery failed").pipe(
          Effect.annotateLogs({ cause: Cause.pretty(cause) }),
        ),
      ),
      Effect.forkScoped,
    )

    // --- Answer validation ---------------------------------------------------------------

    const validateAnswer = (request: ThreadRequest, answer: ThreadRequestAnswer): string | null => {
      if (request.kind !== answer.kind) return `the request expects a ${request.kind} answer`
      if (request.kind !== "question" || answer.kind !== "question") return null
      const known = new Map(request.questions.map((question) => [question.id, question]))
      const missing = request.questions.find((question) => !(question.id in answer.answers))
      if (missing !== undefined) return `question ${missing.id} needs an answer`
      for (const [id, chosen] of Object.entries(answer.answers)) {
        const question = known.get(id)
        if (question === undefined) return `unknown question ${id}`
        if (chosen.length === 0) return `question ${id} needs an answer`
        if (!question.multiSelect && chosen.length > 1) return `question ${id} takes one answer`
        if (!question.freeform) {
          const labels = new Set(question.options.map((option) => option.label))
          const stray = chosen.find((value) => !labels.has(value))
          if (stray !== undefined) return `"${stray}" is not an option of question ${id}`
        }
      }
      return null
    }

    const service: ThreadRuntime["Service"] = {
      prompt: (id, input) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          if (record.archivedAt !== undefined) {
            return yield* Effect.fail(new ThreadArchived({ threadId: id }))
          }
          yield* requireKind(record, "chat")
          yield* requireAdapter(record)
          // The attachments must be this thread's own before the prompt is
          // admitted — an unknown id is the caller's error, not a queued
          // prompt that can never start.
          const attachmentIds = input.attachments ?? []
          const resolved = yield* attachments.resolve(id, attachmentIds)
          // Whether THIS prompt is the one an immediate start would run.
          const first = runOf(id) === undefined && Option.isNone(yield* transcripts.peek(id))
          const queued = yield* transcripts.enqueue(id, input.prompt, attachmentIds)
          if (input.when === "now") {
            const joined = yield* steerRunning(id, queued, {
              text: input.prompt,
              attachments: resolved,
            })
            if (joined !== undefined) return joined
          }
          // The drain runs the OLDEST waiting prompt. When that is ours, a
          // provider that refuses un-admits it (the caller's error means
          // "not accepted"); an older prompt's failure just leaves ours
          // queued behind it (logged).
          const started = yield* startNext(id).pipe(
            Effect.catch((error) =>
              first
                ? transcripts
                    .deleteWaiting(id, queued.id)
                    .pipe(Effect.andThen(publishQueue(id)), Effect.andThen(Effect.fail(error)))
                : Effect.logWarning("an older queued prompt could not start; it stays queued").pipe(
                    Effect.annotateLogs({ threadId: id, reason: error.message }),
                    Effect.as(Option.none()),
                  ),
            ),
          )
          // The queue is published once per prompt, after startNext has
          // decided (ATC-214): a prompt that started at once was never in a
          // published queue — registerRun already published the queue as it
          // stands — and one that queued is published here, waiting.
          if (Option.isSome(started) && started.value.promptId === queued.id) {
            return { promptId: queued.id, turnId: started.value.turnId }
          }
          // A turn's end forks its own drain (finish); when that drain won
          // the start lock with this very prompt, the prompt has started —
          // answer with its turn rather than a wait that is already over.
          const startedMeanwhile = yield* transcripts.startedTurn(id, queued.id)
          if (Option.isSome(startedMeanwhile)) {
            return { promptId: queued.id, turnId: startedMeanwhile.value }
          }
          yield* publishQueue(id)
          return { promptId: queued.id }
        }).pipe(
          // A caller that vanished mid-decision (request interruption) leaves
          // the prompt admitted and durable: publish the queue so no client
          // is left blind to it.
          Effect.onInterrupt(() => publishQueue(id)),
        ),
      interrupt: (id) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          yield* requireKind(record, "chat")
          const writer = writers.get(id)
          const run = writer?.run
          // No run, or one already ending: idle is the goal state.
          if (writer === undefined || run === undefined || run.finished) return
          yield* writer.connection.interrupt(run.turn).pipe(
            // The turn ended on its own first: already the goal state.
            Effect.catchTag("AgentConflict", () => Effect.void),
            Effect.mapError(
              (error) =>
                new ProviderUnavailable({ agentId: record.agentId, reason: error.message }),
            ),
          )
        }),
      listRequests: (id) =>
        repository.require(id).pipe(Effect.map(() => [...(runOf(id)?.requests.values() ?? [])])),
      answerRequest: (id, requestId, answer) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          yield* requireKind(record, "chat")
          const writer = writers.get(id)
          const request = writer?.run?.requests.get(requestId)
          if (writer === undefined || request === undefined) {
            return yield* Effect.fail(new RequestNotFound({ threadId: id, requestId }))
          }
          const reason = validateAnswer(request, answer)
          if (reason !== null) {
            return yield* Effect.fail(new InvalidRequestAnswer({ threadId: id, requestId, reason }))
          }
          yield* writer.connection.respond(requestId, answer).pipe(
            // Resolved elsewhere between our lookup and the answer.
            Effect.catchTag("AgentConflict", () =>
              Effect.fail(new RequestNotFound({ threadId: id, requestId })),
            ),
            Effect.catchTags({
              AgentUnavailable: (error) =>
                Effect.fail(
                  new ProviderUnavailable({ agentId: record.agentId, reason: error.message }),
                ),
              AgentProtocolError: (error) =>
                Effect.fail(
                  new ProviderUnavailable({ agentId: record.agentId, reason: error.message }),
                ),
            }),
          )
        }),
      listQueue: (id) => repository.require(id).pipe(Effect.andThen(queuedPrompts(id))),
      deleteQueued: (id, promptId) =>
        withStartLock(id)(
          Effect.gen(function* () {
            yield* repository
              .require(id)
              .pipe(Effect.flatMap((record) => requireKind(record, "chat")))
            const removed = yield* transcripts.deleteWaiting(id, promptId)
            if (!removed) {
              return yield* Effect.fail(new QueuedPromptNotFound({ threadId: id, promptId }))
            }
            yield* publishQueue(id)
          }),
        ),
      transcript: (id, options) =>
        repository.require(id).pipe(
          Effect.andThen(
            transcripts.read(id, {
              before: options?.before,
              limit: options?.limit ?? TRANSCRIPT_PAGE,
            }),
          ),
        ),
      subscribe: (id, after) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          const queue = yield* Queue.make<ThreadEvent, Cause.Done>({
            capacity: SUBSCRIBER_CAPACITY,
          })
          // Registered BEFORE the replay read: anything published in
          // between waits in the queue, and the seq filter below drops
          // what the replay already covered. A stream that never runs
          // self-heals the way Events' do: its queue fills and publish
          // drops it.
          const set = hubs.get(id) ?? new Set()
          set.add(queue)
          hubs.set(id, set)
          const unsubscribe = Effect.sync(() => {
            set.delete(queue)
            if (set.size === 0 && hubs.get(id) === set) hubs.delete(id)
            Queue.endUnsafe(queue)
          })
          if (after === undefined) return Stream.fromQueue(queue).pipe(Stream.ensuring(unsubscribe))
          // A replay read that dies must not strand the registration.
          const { changes, counters } = yield* transcripts
            .changesAfter(id, after)
            .pipe(Effect.onExit((exit) => (exit._tag === "Failure" ? unsubscribe : Effect.void)))
          // A copy replaced since `after`: what was deleted cannot be
          // replayed, so the client is told to refetch instead. Only a tui
          // thread's copy is ever replaced — a chat thread's counters may
          // still carry a replacement from before it was a chat thread
          // (the one-time migration), which must not read as one now.
          const replay: ReadonlyArray<ThreadEvent> =
            record.kind === "tui" && counters.snapshotSeq > after
              ? [
                  {
                    type: "snapshot.invalidated",
                    seq: counters.seq,
                    snapshotVersion: counters.snapshotVersion,
                  },
                ]
              : changes.map(replayEvent)
          return Stream.concat(
            Stream.fromIterable(replay),
            Stream.fromQueue(queue).pipe(
              Stream.filter((event) => !("seq" in event) || event.seq > counters.seq),
            ),
          ).pipe(Stream.ensuring(unsubscribe))
        }),
      reread: (id) => repository.require(id).pipe(Effect.flatMap(rereadRecord)),
      activity: (id) => Effect.sync(() => liveActivity.get(id)),
      hasWriter: (id) => Effect.sync(() => writers.has(id)),
      noteActivity,
      observedIdle,
      observe,
      unobserve,
      observing: (record) =>
        Effect.sync(() => observed.get(record.id)?.providerSessionId === record.providerSessionId),
      openTui,
      closeTui,
      release: (id) =>
        withStartLock(id)(
          Effect.gen(function* () {
            const writer = writers.get(id)
            liveActivity.delete(id)
            yield* unobserve(id)
            const found = yield* repository.get(id)
            const run = writer?.run
            if (run !== undefined && !run.finished) {
              run.finished = true
              closeRequests(id, run)
              // The turn is over as far as ATC is concerned — the row must
              // not read running forever (nor pull the thread into every
              // startup pass).
              if (Option.isSome(found)) {
                yield* markInterrupted(found.value, { id: run.turnId, status: "running" })
              }
            }
            if (writer !== undefined) yield* closeWriter(id, writer)
            // Subscribers of a thread being put away or deleted are done.
            for (const queue of hubs.get(id) ?? []) Queue.endUnsafe(queue)
            hubs.delete(id)
          }),
        ),
    }
    return service
  })

export const layerWith = (options: ThreadRuntimeOptions) =>
  Layer.effect(ThreadRuntime)(make(options))

/** The production runtime (the default residency policy). */
export const layer = layerWith({})
