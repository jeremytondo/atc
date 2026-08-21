import { Cause, Context, Effect, Exit, Layer, Option, Scope, Stream } from "effect"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import { isBusyActivity } from "../agents/agentAdapter.ts"
import type { AgentActivity, AgentAdapter, AgentSessionEvent } from "../agents/agentAdapter.ts"
import { isAgentId } from "../api/contract.ts"
import type {
  ProviderSessionConflict,
  ProviderUnavailable,
  ThreadNotFound,
  ZmxUnavailable,
} from "../api/contract.ts"
import { makeKeyedLock } from "../platform/keyedLock.ts"
import { ThreadNaming } from "./threadNaming.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"
import { ThreadRuntime } from "./threadRuntime.ts"
import { ThreadTui } from "./threadTui.ts"
import { TranscriptRepository } from "./transcriptRepository.ts"

// The Thread observer (ATC-226): how ATC follows a tui thread — one whose
// driver is the provider TUI in a terminal ATC launched. Its feed is the
// adapter's normalized subscription, drained into the runtime's ledger,
// transcript copy, and settings row, and into ThreadNaming. A chat thread
// is never observed: every entry point is a no-op for it. Invariants:
//
//   - One observation per thread, once per provider session, demand-driven
//     (`observe` on every read that finds a live terminal, and at launch):
//     a superseded session's subscription never keeps driving the thread,
//     and a feed that ends on its own releases its claim so the next read
//     re-subscribes instead of trusting a dead stream. The idle work a
//     feed spawns lives in its scope: `unobserve` ends both.
//   - The observed busy→idle drop is a turn's end: the copy is re-read
//     wholesale (`runtime.reread`). Re-reads serialize per thread, and a
//     terminal ends only with its last turn on disk: a close while idle
//     re-reads first (a read that fails fails the close), a close while
//     busy waits for the idle and ends the terminal once the re-read of
//     THAT idle has landed — a newer idle in between (the TUI worked again
//     meanwhile) re-arms the wait for its own re-read, and a read that
//     fails leaves the terminal running (nothing can re-read a TUI that is
//     gone). Showing the terminal again calls the close off.
//   - Nothing relaunches a terminal that ended: the next open does. At
//     startup, a tui thread whose copy still holds a running turn is
//     re-read once (the TUI may have worked while ATC was down).

export class ThreadObserver extends Context.Service<
  ThreadObserver,
  {
    /** Start (once) the thread's session observation. */
    readonly observe: (record: ThreadRecord) => Effect.Effect<void>
    /** End the thread's observation and the idle work it spawned. */
    readonly unobserve: (id: string) => Effect.Effect<void>
    /** Whether the record's current session is under observation. */
    readonly observing: (record: ThreadRecord) => Effect.Effect<boolean>
    /** One activity observation into the runtime's ledger — the feed's, or
     * a read's re-derivation — with the header's idle rule on top. */
    readonly noteActivity: (record: ThreadRecord, activity: AgentActivity) => Effect.Effect<void>
    /** A client stopped showing the terminal: end it now when idle (its
     * last turn re-read first), at its observed idle when busy. */
    readonly closeTui: (
      record: ThreadRecord,
    ) => Effect.Effect<
      void,
      ThreadNotFound | ProviderUnavailable | ProviderSessionConflict | ZmxUnavailable
    >
    /** A client opens the terminal: a close waiting on the idle is called
     * off first, and any idle work in flight has finished. */
    readonly cancelPendingClose: (id: string) => Effect.Effect<void>
  }
>()("app-server/ThreadObserver") {}

export const layer = Layer.effect(ThreadObserver)(
  Effect.gen(function* () {
    const registry = yield* AgentRegistry
    const repository = yield* ThreadRepository
    const transcripts = yield* TranscriptRepository
    const runtime = yield* ThreadRuntime
    const naming = yield* ThreadNaming
    const tui = yield* ThreadTui
    const serviceScope = yield* Effect.scope

    const adapterFor = (record: ThreadRecord): AgentAdapter | undefined =>
      isAgentId(record.agentId) ? registry.adapterFor(record.agentId) : undefined

    /** The observed session per thread; the child scope closes it and the
     * idle work it spawned. */
    interface Observation {
      readonly providerSessionId: string
      readonly scope: Scope.Closeable
    }
    const observed = new Map<string, Observation>()
    /** Threads whose terminal ends at the next observed idle. */
    const closeAtIdle = new Set<string>()
    /** Idle edges seen per thread: a re-read knows whether a newer idle
     * has landed since the one that started it. */
    const idleEdges = new Map<string, number>()
    const idleLock = makeKeyedLock()

    const busy = (id: string): Effect.Effect<boolean> =>
      runtime.activity(id).pipe(Effect.map((activity) => isBusyActivity(activity ?? "unknown")))

    const endLinked = (record: ThreadRecord): Effect.Effect<void, ZmxUnavailable> =>
      Effect.gen(function* () {
        const linked = yield* tui.linked(record)
        if (linked === undefined) return
        yield* tui.end(linked.id)
      })

    /** The idle's re-read, then the close it gates (the header). */
    const observedIdle = (record: ThreadRecord, edge: number): Effect.Effect<void> =>
      idleLock.withLock(record.id)(
        Effect.gen(function* () {
          const settled = yield* runtime.reread(record.id).pipe(
            Effect.as(true),
            Effect.catch((error) =>
              Effect.logDebug("re-read after observed idle failed").pipe(
                Effect.annotateLogs({ threadId: record.id, reason: error.message }),
                Effect.as(false),
              ),
            ),
          )
          if (!settled || !closeAtIdle.has(record.id)) return
          // A newer idle (or a turn in progress) since this one: its own
          // re-read is the gate now.
          if (idleEdges.get(record.id) !== edge || (yield* busy(record.id))) return
          closeAtIdle.delete(record.id)
          yield* endLinked(record).pipe(
            Effect.catch((error) =>
              Effect.logWarning("the closed TUI could not be ended at its idle").pipe(
                Effect.annotateLogs({ threadId: record.id, reason: error.message }),
              ),
            ),
          )
        }),
      )

    const noteActivity = (record: ThreadRecord, activity: AgentActivity): Effect.Effect<void> =>
      Effect.gen(function* () {
        const { previous } = yield* runtime.noteActivity(record, activity)
        if (record.kind !== "tui") return
        if (previous === undefined || !isBusyActivity(previous) || activity !== "idle") return
        const edge = (idleEdges.get(record.id) ?? 0) + 1
        idleEdges.set(record.id, edge)
        // Forked (the drain must never wait on the provider), into the
        // observation's scope when one holds the thread so `unobserve`
        // ends it too.
        yield* observedIdle(record, edge).pipe(
          Effect.forkIn(observed.get(record.id)?.scope ?? serviceScope),
        )
      })

    // The idle lock entry stays (bounded by thread ids): idle work forked
    // outside the observation (a discovered idle) may still hold it.
    const unobserve = (id: string): Effect.Effect<void> =>
      Effect.gen(function* () {
        closeAtIdle.delete(id)
        idleEdges.delete(id)
        const observation = observed.get(id)
        if (observation === undefined) return
        observed.delete(id)
        yield* Scope.close(observation.scope, Exit.void)
      })

    const drain = (record: ThreadRecord, event: AgentSessionEvent): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (event.type === "userPrompt") return yield* naming.notePrompt(record, event.text)
        if (event.type === "settings") return yield* runtime.adoptSettings(record, event.settings)
        if (event.type === "activity") return yield* noteActivity(record, event.activity)
        yield* runtime.recordObservedItem(record.id, event)
      })

    const observe = (record: ThreadRecord): Effect.Effect<void> =>
      Effect.gen(function* () {
        if (record.kind !== "tui") return
        const adapter = adapterFor(record)
        const providerSessionId = record.providerSessionId
        if (adapter === undefined || providerSessionId === undefined) return
        const existing = observed.get(record.id)
        if (existing?.providerSessionId === providerSessionId) return
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
            Stream.runForEach((event) => drain(record, event)),
            // An ended observation also ends the refinement's wait — its
            // turn-end evidence is gone, so the catch-up must not park
            // forever.
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

    // Startup: the tui threads whose copy still holds a running turn.
    yield* Effect.forEach(
      yield* transcripts.threadsNeedingAttention(),
      (id) =>
        Effect.gen(function* () {
          const record = yield* repository.get(id)
          if (Option.isNone(record) || record.value.kind !== "tui") return
          yield* runtime
            .reread(id)
            .pipe(
              Effect.catch((error) =>
                Effect.logWarning("startup re-read failed").pipe(
                  Effect.annotateLogs({ threadId: id, reason: error.message }),
                ),
              ),
            )
        }),
      { concurrency: 4 },
    ).pipe(
      Effect.catchCause((cause) =>
        Effect.logWarning("startup tui re-read failed").pipe(
          Effect.annotateLogs({ cause: Cause.pretty(cause) }),
        ),
      ),
      Effect.forkScoped,
    )

    return {
      observe,
      unobserve,
      observing: (record) =>
        Effect.sync(() => observed.get(record.id)?.providerSessionId === record.providerSessionId),
      noteActivity,
      closeTui: (record) =>
        Effect.gen(function* () {
          if (record.kind !== "tui") return
          if (yield* busy(record.id)) {
            closeAtIdle.add(record.id)
            return
          }
          closeAtIdle.delete(record.id)
          yield* idleLock.withLock(record.id)(
            runtime.reread(record.id).pipe(Effect.andThen(endLinked(record))),
          )
        }),
      // Under the idle lock: an idle's re-read and close in flight finish
      // first, so the caller's own linked check sees the outcome.
      cancelPendingClose: (id) => idleLock.withLock(id)(Effect.sync(() => closeAtIdle.delete(id))),
    }
  }),
)
