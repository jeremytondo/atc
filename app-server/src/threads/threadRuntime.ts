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
import { Events } from "../events/events.ts"
import { makeKeyedLock } from "../platform/keyedLock.ts"
import { Attachments } from "./attachments.ts"
import { ThreadNaming } from "./threadNaming.ts"
import { requireKind, ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"
import { applyProviderSettings } from "./threadSettings.ts"
import type { ThreadSettings } from "./threadSettings.ts"
import { TranscriptRepository } from "./transcriptRepository.ts"
import type { QueuedPromptRecord, TranscriptChange, TurnSource } from "./transcriptRepository.ts"

export type ThreadEvent = typeof Contract.ThreadEvent.Type
export type ThreadTranscript = typeof Contract.ThreadTranscript.Type
export type QueuedPrompt = typeof Contract.QueuedPrompt.Type

// The Thread runtime (ATC-193): the server-owned run, and everything live
// about a chat thread's provider session. It drives a thread's conversation
// through the AgentAdapter seam with no Terminal involved — prompts, the
// durable prompt queue, the turn loop, parked provider requests, the
// transcript with its per-thread event stream, and the live activity
// ledger every surface reads. Every surface (macOS, CLI, Linear) is a
// client of this one service; nothing here is provider-specific beyond the
// seam's `sharedServer` capability. A tui thread is never driven here —
// prompt, interrupt, answer, and the queue refuse it (ThreadKindMismatch,
// ATC-224); the ThreadObserver follows it through the seams at the end of
// the service. Invariants:
//
//   - A prompt is admitted, always: it lands in the queue (durable), and if
//     the thread is idle it starts a turn at once — the one exception being
//     a provider that refuses that immediate start, which un-admits the
//     prompt so the caller's error means "not accepted" (a Codex thread
//     mid-turn in another Codex client is the case that matters: nothing
//     queues behind a turn ATC cannot see). A prompt marked `now`
//     (ATC-216) is instead handed to the turn running on our writer — the
//     seam's `steer` — and recorded against that turn; with no such turn,
//     or one that ended first, it is an ordinary prompt. A turn runs on a
//     writer connection the runtime holds; client connections never
//     matter to a run's lifetime. A closing writer stays registered until
//     its connection has closed, so the next prompt never races it.
//   - Connection lifetime is not turn lifetime (ATC-207): the Run ends at
//     turn end, the writer need not. A one-process provider's writer stays
//     resident after a successful turn — the next prompt starts on it
//     rather than re-spawning and resuming — and while held its feed owns
//     the ledger; it closes when the provider ends it, on release and
//     shutdown, and idle residency is bounded (`armIdleClose`). A
//     shared-server provider's connection is a cheap subscription: it
//     holds nothing between turns. A turn the provider starts by itself on
//     a held one-process connection (Claude's root loop woken by a finished
//     background task) is ATC's run: adopted at its turnStarted, ended by
//     `finish` like a prompted turn, its requests answerable; a prompt
//     admitted meanwhile waits for it (the connection refuses a second
//     turn) and drains at its end.
//   - A chat thread's transcript is ATC's own append-only record of the
//     turns it drove: never re-read from the provider, never replaced —
//     not at idle, not when a connection fails, not at startup — so
//     `snapshot.invalidated` never reaches a chat thread's stream, and a
//     turn run on the session from outside ATC leaves no trace here. Only
//     a tui thread's copy is ever replaced (`reread`, the observer's
//     trigger), and only its subscribers are ever told to refetch.
//   - A confirmed provider session the provider no longer has
//     (AgentResumeFailed on the resume, or on Claude's first turn after it)
//     never bricks the thread: the turn starts a fresh session, the
//     transcript is kept, and one `notice` item opens the turn.
//   - Provider requests park in memory until answered through
//     `answerRequest`; they die with their turn or the process (a re-run
//     turn re-asks). Answers are validated against the request here, so
//     adapters only ever see well-formed answers.
//   - Settings (ATC-205): every turn starts with the thread's stored
//     settings (re-read under the start lock, so a change made while a
//     prompt waited is what the turn runs with); the adapter pushes only
//     what differs from the live session. The provider's own word on its
//     session settings — the seam's `settings` events — is adopted into
//     the row as it arrives (provider state wins), except that a report merely
//     confirming what the writer's last turn pushed is not news
//     (applyProviderSettings): a confirmation that lands after the user
//     changed a setting again must not roll that change back.
//   - The activity ledger is the one place live activity lives (the
//     observer feeds it too): a first busy confirms the thread, a busy→idle drop stamps
//     last_finished_at (unread, ATC-160), and every transition publishes
//     the thread as updated. Turn end forces idle (the turn is done; ATC-160
//     stamps it); a resident connection's later evidence — background work
//     re-busying the thread, then its idle — applies on top, and a
//     connection that ends forces idle again, since nothing can report on
//     it any more. The ledger also feeds ThreadNaming every busy/idle edge,
//     whichever surface produced it (ATC-202).
//   - Startup: a chat thread with a turn still running on a shared
//     provider server (Codex) is reattached and followed to its end; any
//     other still-running turn is marked interrupted; then queues drain.
//     A provider that is unavailable at startup leaves prompts queued — the
//     next prompt on that thread retries the drain.
//   - Live-only events (text.delta, request.*, queue.updated) carry no seq
//     and are never replayed; a replay re-sends changed rows as
//     item.updated / turn.started|completed with the row's current state,
//     in seq order.
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

    // --- The seams threads.ts and the observer consume --------------------

    /** Replace a tui thread's copy with provider history (the header's
     * rules; a no-op on a chat thread). The observer's idle re-read and
     * startup are the triggers; no route calls this. */
    readonly reread: (
      id: string,
    ) => Effect.Effect<void, ThreadNotFound | ProviderUnavailable | ProviderSessionConflict>
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
    /** An item of a tui thread's turn, from the observer's feed, into the
     * copy (and onto the thread's stream). */
    readonly recordObservedItem: (threadId: string, event: AgentItemEvent) => Effect.Effect<void>
    /** A provider settings report from the observer's feed, adopted over
     * the row as it stands (the header's settings bullet). */
    readonly adoptSettings: (
      record: ThreadRecord,
      reported: ProviderSettings,
    ) => Effect.Effect<void>
    /** Release the run and the thread's event streams (archive/delete): the
     * connection closes, its turn is recorded interrupted, and the provider
     * keeps its own state. */
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
    // Writers and their drain fibers outlive their originating requests;
    // they live in the service scope so shutdown releases every connection.
    const serviceScope = yield* Effect.scope
    const residentIdleTimeout = options.residentIdleTimeout ?? "10 minutes"
    const streamingFlushInterval = options.streamingFlushInterval ?? "250 millis"

    const writers = new Map<string, Writer>()
    /** The turn the runtime drives on a thread, if any. */
    const runOf = (id: string): Run | undefined => writers.get(id)?.run
    const hubs = new Map<string, Set<Queue.Queue<ThreadEvent, Cause.Done>>>()
    const liveActivity = new Map<string, AgentActivity>()
    // Threads the startup pass has not settled yet: their prompts wait.
    const recovering = new Set(yield* transcripts.threadsNeedingAttention())
    /** Per-thread serialization of "is a run active → start the next prompt". */
    const withStartLock = makeKeyedLock().withLock

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
     * header's residency rule applies). */
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
     * again by the drain. Undefined when no turn of ours runs or the turn
     * ended first: the prompt stays waiting for the ordinary drain.
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
     * returns before the first's finalizers are done, and the next prompt
     * must not resume on a process still shutting down.
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
     * next queued prompt (on a fresh connection) run in the service scope. Uninterruptible from the begin to the fork:
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
     * the Run leaves the writer. Callers hold the writer's lock (every
     * caller is the drain): the final flush below runs unsynchronized. A shared-server provider's writer drops
     * with it (nothing is held between turns), forcing the ledger idle —
     * nothing can report on it any more. A one-process provider's stays
     * resident — the next queued prompt starts on it, the idle timer
     * bounds it — and the ledger takes the
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
        yield* startNextLogged(record.id).pipe(Effect.forkIn(serviceScope))
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
          // bullet). On a shared server it is another client's — a chat
          // thread's transcript takes nothing from it (the header).
          if (run === undefined && oneProcess(record)) {
            return adoptTurn(record, writer, { turnId: event.turnId })
          }
          return Effect.logDebug("a turn another client started on the session is ignored").pipe(
            Effect.annotateLogs({ threadId: record.id, turnId: event.turnId }),
          )
        }
        case "turnCompleted":
          return run !== undefined && event.turnId === run.turnId
            ? finish(record, writer, run, event.outcome, event.detail)
            : Effect.void
      }
    }

    /**
     * The feed ended on its own — cleanly (the provider ended the session:
     * a non-success turn on Claude, a resident child that exited) or with
     * a failure (transport loss). With a run still open the turn ends
     * failed, with the reason, and the text streamed so far is kept (the
     * last flush, as `finish` does); the transcript takes only what ATC
     * saw (the header), nothing is re-read. Either way nothing can report
     * on the connection any more: the writer drops, and the next prompt
     * resumes afresh.
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
        yield* Effect.logWarning("the turn's connection ended before the turn did").pipe(
          Effect.annotateLogs({
            threadId: record.id,
            turnId: run.turnId,
            reason: reason ?? "the feed ended without a turn end",
          }),
        )
        yield* dropWriter(record.id, writer)
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
     * running, not recovering, not archived. A resident writer takes the
     * turn without reopening the session; one the provider has ended since
     * closes here and a fresh resume follows.
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

    // --- Startup ---------------------------------------------------------------------

    /**
     * Follow a turn still running on a shared provider server after a
     * restart: resume the session and drain its feed to the turn's end.
     * A session that is already idle has nothing to follow: the turn is
     * marked interrupted (whatever it emitted while ATC was down is not in
     * the transcript, by design).
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
          return yield* markInterrupted(record, turn)
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
          // A tui thread is the observer's to settle.
          if (record.kind !== "chat") return
          const running = yield* transcripts.runningTurns(id)
          if (running.length === 0) return
          const adapter = adapterFor(record)
          for (const turn of running) {
            // A shared provider server keeps running our turn across ATC's
            // restart; anything else died with the process.
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
      recordObservedItem: recordItem,
      adoptSettings: (record, reported) => adoptSettings(record, reported, undefined),
      release: (id) =>
        withStartLock(id)(
          Effect.gen(function* () {
            const writer = writers.get(id)
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
            // After the close: a late event on the feed can no longer
            // re-insert the entry.
            liveActivity.delete(id)
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
