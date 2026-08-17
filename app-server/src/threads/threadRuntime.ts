import {
  Cause,
  Context,
  Effect,
  Exit,
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
  ThreadRequest,
  ThreadRequestAnswer,
  ThreadTurn,
} from "../agents/agentAdapter.ts"
import type * as Contract from "../api/contract.ts"
import {
  InvalidRequestAnswer,
  isAgentId,
  ProviderSessionConflict,
  ProviderUnavailable,
  QueuedPromptNotFound,
  RequestNotFound,
  ThreadArchived,
  ThreadNotFound,
} from "../api/contract.ts"
import { Events } from "../events/events.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"
import { TranscriptRepository } from "./transcriptRepository.ts"
import type { TranscriptChange, TurnSource } from "./transcriptRepository.ts"

export type ThreadEvent = typeof Contract.ThreadEvent.Type
export type ThreadTranscript = typeof Contract.ThreadTranscript.Type
export type QueuedPrompt = typeof Contract.QueuedPrompt.Type

// The Thread runtime (ATC-193): the server-owned run. It drives a thread's
// conversation through the AgentAdapter seam with no Terminal involved —
// prompts, the durable prompt queue, the turn loop, parked provider
// requests, the transcript projection with its per-thread event stream, and
// the live activity ledger every surface reads. Every surface (macOS, CLI,
// Linear) is a client of this one service; nothing here is provider-
// specific. Invariants:
//
//   - A prompt is admitted, always: it lands in the queue (durable), and if
//     the thread is idle it starts a turn at once — the one exception being
//     a provider that refuses that immediate start, which un-admits the
//     prompt so the caller's error means "not accepted". A thread busy
//     under a TUI (the ledger says so) queues the prompt and drains at the
//     observed idle: no busy error, no lock between surfaces. A turn opens a
//     writer connection, runs, and releases the connection at turn end —
//     between turns a thread holds nothing, and client connections never
//     matter to a run's lifetime. A run stays registered until its
//     connection has closed, so the next prompt never races the writer.
//   - The transcript copy is a projection of provider history, never the
//     authority: `seq` (allocated by the repository) orders durable changes
//     and replays them (subscribe after=seq); a re-read (`readHistory`)
//     replaces the copy wholesale and bumps snapshotVersion — triggered at
//     startup for threads mid-work and when a TUI-driven thread goes idle,
//     never for a turn ATC drove itself. An empty history never replaces
//     the copy (a swept Claude transcript keeps ATC's copy readable).
//   - Provider requests park in memory until answered through
//     `answerRequest`; they die with their turn or the process (a re-run
//     turn re-asks). Answers are validated against the request here, so
//     adapters only ever see well-formed answers.
//   - The activity ledger is the one place live activity lives (the
//     terminal-observation drain in threads.ts feeds it too): a first busy
//     confirms the thread, a busy→idle drop stamps last_finished_at
//     (unread, ATC-160), and every transition publishes the thread as
//     updated. Turn end forces idle — the connection is released, so no
//     later evidence can arrive on it. While the runtime drives a thread,
//     its ledger entry is authoritative and never re-derived.
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
//   - Recovery, prompt admission, run end, and release serialize per thread
//     behind one start lock; threads still recovering at startup queue
//     their prompts until recovery has settled them.

/** The most items one transcript read returns. */
const TRANSCRIPT_PAGE = 200

/** Per-subscriber backlog; overflow ends the stream (resubscribe after=seq). */
const SUBSCRIBER_CAPACITY = 512

/** One live turn ATC drives (or follows, after a restart). */
interface Run {
  readonly turnId: string
  readonly scope: Scope.Closeable
  readonly connection: AgentConnection
  readonly turn: AgentTurn
  readonly requests: Map<string, ThreadRequest>
  finished: boolean
}

export class ThreadRuntime extends Context.Service<
  ThreadRuntime,
  {
    /** Admit a prompt; `turnId` present ⇒ it started at once, else queued. */
    readonly prompt: (
      id: string,
      prompt: string,
    ) => Effect.Effect<
      { readonly promptId: string; readonly turnId?: string },
      ThreadNotFound | ThreadArchived | ProviderUnavailable | ProviderSessionConflict
    >
    /** Interrupt the running turn; an idle thread is a no-op. Queued prompts stay. */
    readonly interrupt: (id: string) => Effect.Effect<void, ThreadNotFound | ProviderUnavailable>
    readonly listRequests: (
      id: string,
    ) => Effect.Effect<ReadonlyArray<ThreadRequest>, ThreadNotFound>
    readonly answerRequest: (
      id: string,
      requestId: string,
      answer: ThreadRequestAnswer,
    ) => Effect.Effect<
      void,
      ThreadNotFound | RequestNotFound | InvalidRequestAnswer | ProviderUnavailable
    >
    readonly listQueue: (id: string) => Effect.Effect<ReadonlyArray<QueuedPrompt>, ThreadNotFound>
    readonly deleteQueued: (
      id: string,
      promptId: string,
    ) => Effect.Effect<void, ThreadNotFound | QueuedPromptNotFound>
    /** A page of the transcript (newest `limit`, or the page before `before`). */
    readonly transcript: (
      id: string,
      options?: { readonly before?: string | undefined; readonly limit?: number | undefined },
    ) => Effect.Effect<ThreadTranscript, ThreadNotFound>
    /**
     * The per-thread event stream. With `after`, every durable change past
     * that seq replays first (current row state), then live events follow;
     * without it the stream is live only. Scoped: closing unsubscribes.
     */
    readonly subscribe: (
      id: string,
      after?: number | undefined,
    ) => Effect.Effect<Stream.Stream<ThreadEvent>, ThreadNotFound, Scope.Scope>
    /** Replace the copy with provider history (see the header's rules). No
     * route calls this — the triggers are internal; it exists for tests
     * and manual repair. */
    readonly reread: (
      id: string,
    ) => Effect.Effect<void, ThreadNotFound | ProviderUnavailable | ProviderSessionConflict>

    // --- The seams threads.ts consumes -------------------------------------

    /** The ledger's current activity for a thread (undefined = no evidence). */
    readonly activity: (id: string) => Effect.Effect<AgentActivity | undefined>
    /** Whether the runtime currently drives (or follows) a turn on the thread. */
    readonly isDriving: (id: string) => Effect.Effect<boolean>
    /** Feed one activity observation through the ledger's rules (see header). */
    readonly noteActivity: (
      record: ThreadRecord,
      activity: AgentActivity,
    ) => Effect.Effect<{ readonly previous: AgentActivity | undefined }>
    /** Drop the ledger entry (an observation ended). */
    readonly clearActivity: (id: string) => Effect.Effect<void>
    /** Record an item observed on a TUI-driven session (the shared-server fan-out). */
    readonly recordObserved: (record: ThreadRecord, event: AgentItemEvent) => Effect.Effect<void>
    /** The thread went idle under observation: re-read unless the busy was ours. */
    readonly observedIdle: (record: ThreadRecord) => Effect.Effect<void>
    /** Release the run and end the thread's event streams (archive/delete):
     * the connection closes, its turn is recorded interrupted, and the
     * provider keeps its own state. */
    readonly release: (id: string) => Effect.Effect<void>
  }
>()("app-server/ThreadRuntime") {}

export const layer = Layer.effect(ThreadRuntime)(
  Effect.gen(function* () {
    const repository = yield* ThreadRepository
    const transcripts = yield* TranscriptRepository
    const registry = yield* AgentRegistry
    const events = yield* Events
    // Runs and their drain fibers outlive their originating requests; they
    // live in the service scope so shutdown releases every connection.
    const serviceScope = yield* Effect.scope

    const runs = new Map<string, Run>()
    const hubs = new Map<string, Set<Queue.Queue<ThreadEvent, Cause.Done>>>()
    const liveActivity = new Map<string, AgentActivity>()
    // Threads the startup pass has not settled yet: their prompts wait.
    const recovering = new Set(yield* transcripts.threadsNeedingAttention())
    /** Threads whose latest busy spell was a native turn: the observed idle
     * that ends it is not a TUI turn's end (observedIdle consumes the mark). */
    const nativeBusy = new Set<string>()
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
        const seq = yield* transcripts.upsertItem(threadId, event.item)
        const type =
          event.type === "itemStarted"
            ? "item.started"
            : event.type === "itemUpdated"
              ? "item.updated"
              : "item.completed"
        publish(threadId, { type, seq, item: event.item })
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

    const publishQueue = (threadId: string): Effect.Effect<void> =>
      transcripts
        .listWaiting(threadId)
        .pipe(Effect.map((prompts) => publish(threadId, { type: "queue.updated", prompts })))

    const replayEvent = (change: TranscriptChange): ThreadEvent =>
      change.change.kind === "item"
        ? { type: "item.updated", seq: change.seq, item: change.change.item }
        : {
            type: change.change.turn.status === "running" ? "turn.started" : "turn.completed",
            seq: change.seq,
            turn: change.change.turn,
          }

    /** Replace the copy with what the provider has; empty never wipes. */
    const rereadRecord = (
      record: ThreadRecord,
    ): Effect.Effect<void, ProviderUnavailable | ProviderSessionConflict> =>
      Effect.gen(function* () {
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
        if (isBusy(activity)) {
          if (runs.has(record.id)) nativeBusy.add(record.id)
          else nativeBusy.delete(record.id)
          // The first busy confirms (idempotent in the repository): a
          // submitted prompt is durable provider history worth protecting.
          if (previous === undefined || !isBusy(previous)) yield* repository.confirm(record.id)
        }
        // The busy→idle drop IS the turn-finished signal (ATC-160): stamp
        // before publishing so the refetch already sees the thread unread.
        if (previous !== undefined && isBusy(previous) && activity === "idle") {
          yield* repository.markFinished(record.id)
        }
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
     * The run's last act: close the connection, deregister only once it is
     * closed (a prompt arriving meanwhile queues rather than racing the
     * writer), then start the next queued prompt. Runs in the service scope
     * — the callers sit on the run's own drain fiber, which the scope close
     * interrupts.
     */
    const closeRun = (
      record: ThreadRecord,
      run: Run,
      then: Effect.Effect<void>,
    ): Effect.Effect<void> =>
      Scope.close(run.scope, Exit.void).pipe(
        Effect.andThen(
          Effect.sync(() => {
            if (runs.get(record.id) === run) runs.delete(record.id)
          }),
        ),
        Effect.andThen(then),
        Effect.andThen(startNextLogged(record.id)),
        Effect.forkIn(serviceScope),
      )

    /**
     * End a run: the turn row takes its outcome, parked requests close, the
     * ledger drops to idle (the connection is going away — see the header),
     * and the run closes.
     */
    const finish = (
      record: ThreadRecord,
      run: Run,
      status: "completed" | "interrupted" | "failed",
      detail?: string,
    ): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (run.finished) return
        run.finished = true
        closeRequests(record.id, run)
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
        yield* noteActivity(record, "idle")
        yield* closeRun(record, run, Effect.void)
      })

    const handleEvent = (
      record: ThreadRecord,
      run: Run,
      event: AgentEvent,
    ): Effect.Effect<void> => {
      switch (event.type) {
        case "activity":
          // After the turn ended, lingering busy evidence on this feed (a
          // subagent still winding down) must not re-busy a thread whose
          // connection is closing — nothing on it would ever report idle.
          return run.finished && isBusy(event.activity)
            ? Effect.void
            : noteActivity(record, event.activity).pipe(Effect.asVoid)
        case "itemStarted":
        case "itemUpdated":
        case "itemCompleted":
          return recordItem(record.id, event)
        case "textDelta":
          return Effect.sync(() =>
            publish(record.id, { type: "text.delta", itemId: event.itemId, delta: event.delta }),
          )
        case "requestOpened":
          return Effect.sync(() => {
            run.requests.set(event.request.id, event.request)
            publish(record.id, { type: "request.opened", request: event.request })
          })
        case "requestClosed":
          return Effect.sync(() => {
            if (!run.requests.delete(event.requestId)) return
            publish(record.id, { type: "request.closed", requestId: event.requestId })
          })
        case "turnStarted":
          // Another writer's turn on a connection we hold (a TUI mid-run):
          // observed, not ours.
          return event.turnId === run.turnId
            ? Effect.void
            : recordTurn(
                record.id,
                { id: event.turnId, status: "running", startedAt: new Date().toISOString() },
                "observed",
              )
        case "turnCompleted":
          return event.turnId === run.turnId
            ? finish(record, run, event.outcome, event.detail)
            : recordTurn(
                record.id,
                { id: event.turnId, status: event.outcome, endedAt: new Date().toISOString() },
                "observed",
              )
      }
    }

    /**
     * The feed died with no truthful turn end (transport loss): ATC no
     * longer knows how the turn is doing — the provider may still be
     * running it. Drop the run without guessing an outcome, and re-read the
     * provider's history for the truth; a turn history still calls running
     * is the startup pass's job if the provider stays unreachable.
     */
    const abandon = (record: ThreadRecord, run: Run, reason: string): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (run.finished) return
        run.finished = true
        closeRequests(record.id, run)
        yield* noteActivity(record, "unknown")
        yield* Effect.logWarning("the turn's connection failed; re-reading provider history").pipe(
          Effect.annotateLogs({ threadId: record.id, turnId: run.turnId, reason }),
        )
        yield* closeRun(
          record,
          run,
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
     * Register a run and fork its drain. A feed that FAILS is abandoned
     * (above); so is one that ENDS without a turn end while the run is
     * still open — the provider dropped the session under us (a closed
     * connection's drain is interrupted before its feed ends, so ATC's own
     * closes never take this path).
     */
    const startRun = (record: ThreadRecord, run: Run): Effect.Effect<void> =>
      Effect.gen(function* () {
        runs.set(record.id, run)
        // The connection's snapshot seeds the ledger so a read racing the
        // first feed event already sees what the provider says.
        yield* noteActivity(record, yield* run.connection.activity)
        yield* run.connection.events.pipe(
          Stream.runForEach((event) => handleEvent(record, run, event)),
          Effect.andThen(
            Effect.suspend(() =>
              run.finished
                ? Effect.void
                : abandon(record, run, "the feed ended without a turn end"),
            ),
          ),
          Effect.catch((error) => abandon(record, run, error.message)),
          Effect.forkIn(run.scope),
        )
      })

    /**
     * Start the oldest waiting prompt if the thread can take one: no run
     * registered, not recovering, not archived, and not busy under another
     * surface (the ledger's word — a TUI turn in progress queues us until
     * the observed idle drains). A confirmed thread resumes its exact
     * session; anything else creates a fresh one (the second `materialize`
     * caller ATC-124 anticipated): identity is persisted — and confirmed,
     * the prompt is durable provider history — before the run is
     * registered. Under the per-thread start lock, with the record re-read
     * inside it, so two prompts cannot both start and an archive or delete
     * that won the lock first is honored.
     */
    const startNext = (
      id: string,
    ): Effect.Effect<
      Option.Option<{ readonly promptId: string; readonly turnId: string }>,
      ProviderUnavailable | ProviderSessionConflict
    > =>
      withStartLock(id)(
        Effect.gen(function* () {
          if (runs.has(id) || recovering.has(id)) return Option.none()
          const found = yield* repository.get(id)
          if (Option.isNone(found) || found.value.archivedAt !== undefined) return Option.none()
          const record = found.value
          if (isBusy(liveActivity.get(id) ?? "unknown")) return Option.none()
          const next = yield* transcripts.peek(id)
          if (Option.isNone(next)) return Option.none()
          const adapter = yield* requireAdapter(record)
          const scope = Scope.forkUnsafe(serviceScope)
          const started = yield* Effect.gen(function* () {
            const cwd = record.workingDirectory
            if (record.providerSessionId !== undefined && record.confirmedAt !== undefined) {
              const connection = yield* adapter.resumeSession({
                providerSessionId: record.providerSessionId,
                cwd,
              })
              const turn = yield* connection.startTurn(next.value.prompt)
              return { connection, turn, record }
            }
            // Unconfirmed (none, or zero completed turns): a fresh session,
            // as openTerminal's materialize does — there is no history to
            // protect, and the superseded session's adapter resources go.
            if (record.providerSessionId !== undefined) {
              yield* adapter.releaseSession({
                providerSessionId: record.providerSessionId,
                providerMetadata: record.providerMetadata,
              })
            }
            const session = yield* adapter.createSession({ cwd, input: next.value.prompt })
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
            return { connection: session.connection, turn: session.turn, record: adopted }
          }).pipe(
            Scope.provide(scope),
            Effect.mapError(mapAgentError(record)),
            Effect.onExit((exit) =>
              exit._tag === "Failure" ? Scope.close(scope, Exit.void) : Effect.void,
            ),
          )
          const turnId = started.turn.turnId
          const turn: ThreadTurn = {
            id: turnId,
            status: "running",
            promptId: next.value.id,
            startedAt: new Date().toISOString(),
          }
          // From here the connection is ours until the run owns it: no
          // interruption (a dropped request) may land between the two, or
          // the writer would stay open with nobody to close it.
          yield* Effect.uninterruptible(
            Effect.gen(function* () {
              const seq = yield* transcripts.beginTurn(record.id, next.value.id, turn)
              publish(record.id, { type: "turn.started", seq, turn })
              yield* publishQueue(record.id)
              yield* startRun(started.record, {
                turnId,
                scope,
                connection: started.connection,
                turn: started.turn,
                requests: new Map(),
                finished: false,
              })
            }).pipe(
              Effect.onExit((exit) =>
                exit._tag === "Failure" && !runs.has(record.id)
                  ? Scope.close(scope, Exit.void)
                  : Effect.void,
              ),
            ),
          )
          return Option.some({ promptId: next.value.id, turnId })
        }),
      )

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
          .resumeSession({ providerSessionId, cwd: record.workingDirectory })
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
        yield* startRun(record, {
          turnId: turn.id,
          scope,
          connection,
          turn: { turnId: turn.id },
          requests: new Map(),
          finished: false,
        })
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
            yield* adapter?.observationOutlivesTui === true
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

    return {
      prompt: (id, prompt) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          if (record.archivedAt !== undefined) {
            return yield* Effect.fail(new ThreadArchived({ threadId: id }))
          }
          yield* requireAdapter(record)
          // Whether THIS prompt is the one an immediate start would run.
          const first = !runs.has(id) && Option.isNone(yield* transcripts.peek(id))
          const queued = yield* transcripts.enqueue(id, prompt)
          yield* publishQueue(id)
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
          return Option.isSome(started) && started.value.promptId === queued.id
            ? { promptId: queued.id, turnId: started.value.turnId }
            : { promptId: queued.id }
        }),
      interrupt: (id) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          const run = runs.get(id)
          // No run, or one already ending: idle is the goal state.
          if (run === undefined || run.finished) return
          yield* run.connection.interrupt(run.turn).pipe(
            // The turn ended on its own first: already the goal state.
            Effect.catchTag("AgentConflict", () => Effect.void),
            Effect.mapError(
              (error) =>
                new ProviderUnavailable({ agentId: record.agentId, reason: error.message }),
            ),
          )
        }),
      listRequests: (id) =>
        repository.require(id).pipe(Effect.map(() => [...(runs.get(id)?.requests.values() ?? [])])),
      answerRequest: (id, requestId, answer) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          const run = runs.get(id)
          const request = run?.requests.get(requestId)
          if (run === undefined || request === undefined) {
            return yield* Effect.fail(new RequestNotFound({ threadId: id, requestId }))
          }
          const reason = validateAnswer(request, answer)
          if (reason !== null) {
            return yield* Effect.fail(new InvalidRequestAnswer({ threadId: id, requestId, reason }))
          }
          yield* run.connection.respond(requestId, answer).pipe(
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
      listQueue: (id) => repository.require(id).pipe(Effect.andThen(transcripts.listWaiting(id))),
      deleteQueued: (id, promptId) =>
        withStartLock(id)(
          Effect.gen(function* () {
            yield* repository.require(id)
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
              limit: Math.max(1, Math.min(options?.limit ?? TRANSCRIPT_PAGE, TRANSCRIPT_PAGE)),
            }),
          ),
        ),
      subscribe: (id, after) =>
        Effect.gen(function* () {
          yield* repository.require(id)
          const queue = yield* Queue.make<ThreadEvent, Cause.Done>({
            capacity: SUBSCRIBER_CAPACITY,
          })
          // Registered BEFORE the replay read: anything published in
          // between waits in the queue, and the seq filter below drops
          // what the replay already covered.
          yield* Effect.acquireRelease(
            Effect.sync(() => {
              const set = hubs.get(id) ?? new Set()
              set.add(queue)
              hubs.set(id, set)
            }),
            () =>
              Effect.sync(() => {
                const set = hubs.get(id)
                set?.delete(queue)
                if (set?.size === 0) hubs.delete(id)
                Queue.endUnsafe(queue)
              }),
          )
          if (after === undefined) return Stream.fromQueue(queue)
          const { changes, counters } = yield* transcripts.changesAfter(id, after)
          // A copy replaced since `after`: what was deleted cannot be
          // replayed, so the client is told to refetch instead.
          const replay: ReadonlyArray<ThreadEvent> =
            counters.snapshotSeq > after
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
          )
        }),
      reread: (id) => repository.require(id).pipe(Effect.flatMap(rereadRecord)),
      activity: (id) => Effect.sync(() => liveActivity.get(id)),
      isDriving: (id) => Effect.sync(() => runs.has(id)),
      noteActivity,
      clearActivity: (id) =>
        Effect.sync(() => {
          liveActivity.delete(id)
          nativeBusy.delete(id)
        }),
      // While the runtime drives, the writer feed already carries every
      // item; the observer copy of the same fan-out would double it.
      recordObserved: (record, event) =>
        runs.has(record.id) ? Effect.void : recordItem(record.id, event),
      observedIdle: (record) =>
        Effect.gen(function* () {
          if (runs.has(record.id)) return
          // Our own turn's idle is not a TUI turn ending: the copy is live.
          // A TUI turn's end is what the re-read is for — before the queue
          // drains, so the new turn's live items survive it.
          if (!nativeBusy.delete(record.id)) {
            yield* rereadRecord(record).pipe(
              Effect.catch((error) =>
                Effect.logDebug("re-read after observed idle failed").pipe(
                  Effect.annotateLogs({ threadId: record.id, reason: error.message }),
                ),
              ),
            )
          }
          yield* startNextLogged(record.id)
        }),
      release: (id) =>
        withStartLock(id)(
          Effect.gen(function* () {
            const run = runs.get(id)
            liveActivity.delete(id)
            nativeBusy.delete(id)
            if (run !== undefined) {
              run.finished = true
              closeRequests(id, run)
              // The turn is over as far as ATC is concerned — the row must
              // not read running forever (nor pull the thread into every
              // startup pass).
              const found = yield* repository.get(id)
              if (Option.isSome(found)) {
                yield* markInterrupted(found.value, { id: run.turnId, status: "running" })
              }
              yield* Scope.close(run.scope, Exit.void)
              runs.delete(id)
            }
            // Subscribers of a thread being put away or deleted are done.
            for (const queue of hubs.get(id) ?? []) Queue.endUnsafe(queue)
            hubs.delete(id)
          }),
        ),
    }
  }),
)
