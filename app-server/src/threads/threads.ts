import {
  Context,
  Duration,
  Effect,
  Exit,
  Layer,
  Option,
  Schedule,
  Scope,
  Semaphore,
  Stream,
} from "effect"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConflict,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentResumeFailed,
  AgentSessionEvent,
  AgentUnavailable,
  TuiLaunchSpec,
} from "../agents/agentAdapter.ts"
import { isBusyActivity, NESTED_SESSION_ENV_VARIABLES } from "../agents/agentAdapter.ts"
import {
  isAgentId,
  ProviderSessionConflict,
  ProviderUnavailable,
  ThreadArchived,
  ThreadBusy,
  ThreadNotFound,
} from "../api/contract.ts"
import type * as Contract from "../api/contract.ts"
import type {
  CreateThreadRequest,
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
  ProjectNotFound,
  TerminalLaunchFailed,
  ZmxUnavailable,
} from "../api/contract.ts"
import { Events } from "../events/events.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "../projects/projectRepository.ts"
import { Terminals } from "../terminals/terminals.ts"
import type { Terminal } from "../terminals/terminals.ts"
import { ThreadNaming } from "./threadNaming.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"
import { ThreadRuntime } from "./threadRuntime.ts"

export type Thread = typeof Contract.Thread.Type

// The Threads domain service (ATC-124): Threads are the primary unit of
// work — durable ATC identity separate from provider identity. Invariants:
//
//   - The boundary rule: this module contains zero provider branching and
//     reaches providers only through the AgentAdapter seam (via the
//     registry's adapterFor). Every provider difference is translated
//     inside adapters; the same columns are persisted at the same points
//     in the same flow for every agent.
//   - `create` is local-only: the durable row, no provider call. Identity
//     establishment is one internal transition (`materialize`, below) with
//     openTerminal as its first caller — a future native first-prompt is
//     the second caller, with identical persistence.
//   - openTerminal is idempotent (a live linked terminal is returned
//     as-is) and enforces at most one live linked TUI terminal per thread
//     (concurrent opens conflict). Confirmed sessions reopen their exact
//     id; unconfirmed ones re-materialize with fresh identity — there is
//     no history to protect yet.
//   - No lock between surfaces (ATC-193): a TUI may open while the runtime
//     drives a turn and a prompt may arrive while a TUI is live — the
//     provider taking one turn at a time is its own affair, and the only
//     guard is the in-progress state plus the queue. Nothing binds a Thread
//     to one surface.
//   - workingDirectory is validated, canonicalized, and immutable.
//   - activityState is in-memory evidence, never persisted, and lives in
//     the ThreadRuntime's activity ledger — fed by the terminal-observation
//     drain here and by native turns there, one set of rules (first busy
//     confirms, busy→idle stamps the finish, every transition publishes).
//     `unknown` when there is no evidence; a busy state whose driver is
//     gone re-derives demand-driven from the adapter's reconciliation
//     check on read, unless the runtime is driving the thread (its
//     evidence is authoritative) or a shared-server observation still
//     covers it. The confirmed marker is persisted at the FIRST busy signal
//     — a submitted prompt is already durable provider history worth
//     protecting — the same signal for every provider.
//   - The unread overlay (ATC-160) is derived, never stored as a flag:
//     last_finished_at is stamped at the observed busy→idle drop — the
//     ledger and the driver-gone re-derivation alike — last_viewed_at by
//     markViewed, and archived rows are never unread. The activity
//     vocabulary is untouched; "Done" is client-side display translation.
//   - Observed conversation items (the shared-server fan-out on a
//     TUI-driven Codex thread) feed the runtime's transcript copy, and an
//     observed busy→idle asks the runtime to re-read the provider's
//     history — the copy for turns ATC did not drive.
//   - Auto-naming lives in ThreadNaming (ATC-155/190/202); the drain feeds
//     it observed user prompts and its feed's end, judged on the record at
//     subscription start.
//   - archived threads are never pinned: pin refuses archived records, and
//     archive clears the pin in the same repository write so no client can
//     observe or restore an archived pin.
//   - archive suspends the thread's runtime (ATC-157): the live linked TUI
//     terminal is killed (the terminals confirmed-kill rules) and
//     adapter-owned session resources are released before archivedAt is
//     written, so an archived thread consumes no zmx session while the
//     provider conversation survives for an exact resume after unarchive.
//     Re-archive converges: a lingering live terminal (legacy rows from
//     before archive killed terminals) is killed on repeat; archive
//     refuses mid-turn (`unknown` is never guessed to be busy).
//   - archive, delete, and a launching openTerminal serialize per thread
//     behind a lazily created per-id semaphore: archive/delete queue
//     behind an in-flight open and then act on the resulting terminal, so
//     a concurrent open can never leave an archived thread with a live
//     terminal. Open-vs-open keeps its fail-fast conflict, and the
//     idempotent open return never queues.
//   - delete kills the live linked TUI terminal first (the terminals
//     confirmed-kill rules), releases adapter-owned session resources,
//     then removes the row; ended linked terminals stay as unlinked
//     tombstones (FK SET NULL), and provider-owned conversation history
//     is never touched.

export interface ThreadsOptions {
  /** How long openTerminal waits for a fresh session's identity. */
  readonly identityTimeout?: Duration.Input
  /** Initial delay before the identity wait's first TUI-liveness check;
   * subsequent checks back off from it (see watchForEarlyDeath). */
  readonly launchWatchInterval?: Duration.Input
}

export class Threads extends Context.Service<
  Threads,
  {
    /** Listing, newest first; active-only unless a wider archive scope is asked for. */
    readonly list: (options?: {
      readonly projectId?: string | undefined
      readonly archived?: "active" | "archived" | "all" | undefined
    }) => Effect.Effect<ReadonlyArray<Thread>>
    readonly get: (id: string) => Effect.Effect<Thread, ThreadNotFound>
    /** Write the durable record; no provider call (see the header). */
    readonly create: (
      input: typeof CreateThreadRequest.Type,
    ) => Effect.Effect<Thread, ProjectNotFound | DirectoryUnavailable | DirectoryCheckTimedOut>
    /** Patch mutable fields; an empty patch changes nothing, updatedAt included. */
    readonly update: (
      id: string,
      patch: { readonly name?: string | undefined },
    ) => Effect.Effect<Thread, ThreadNotFound>
    /** Kill the live linked terminal and release adapter runtime resources,
     * then set archivedAt (idempotent and convergent — see the header);
     * refused while the agent session is actively working. */
    readonly archive: (
      id: string,
    ) => Effect.Effect<Thread, ThreadNotFound | ThreadBusy | ZmxUnavailable>
    /** Idempotent. */
    readonly unarchive: (id: string) => Effect.Effect<Thread, ThreadNotFound>
    /** Idempotent; archived threads cannot be pinned. */
    readonly pin: (id: string) => Effect.Effect<Thread, ThreadNotFound | ThreadArchived>
    /** Idempotent. */
    readonly unpin: (id: string) => Effect.Effect<Thread, ThreadNotFound>
    /** Stamp the viewed marker, clearing `unread`; a no-op unless unread. */
    readonly markViewed: (id: string) => Effect.Effect<Thread, ThreadNotFound>
    /** Kill the live linked terminal and release adapter resources, then
     * remove the record. */
    readonly delete: (id: string) => Effect.Effect<void, ThreadNotFound | ZmxUnavailable>
    /** Open (or return) the thread's TUI terminal — the header's workflow. */
    readonly openTerminal: (
      id: string,
    ) => Effect.Effect<
      Terminal,
      | ThreadNotFound
      | ThreadArchived
      | ProviderUnavailable
      | ProviderSessionConflict
      | DirectoryUnavailable
      | DirectoryCheckTimedOut
      | ZmxUnavailable
      | TerminalLaunchFailed
    >
  }
>()("app-server/Threads") {}

export const layerWith = (options: ThreadsOptions) =>
  Layer.effect(Threads)(
    Effect.gen(function* () {
      const repository = yield* ThreadRepository
      const projects = yield* ProjectRepository
      const directories = yield* Directories
      const terminals = yield* Terminals
      const registry = yield* AgentRegistry
      const events = yield* Events
      const runtime = yield* ThreadRuntime
      const naming = yield* ThreadNaming
      // Observation fibers outlive their originating requests; they live in
      // the service's own scope so shutdown reaps every subscription.
      const serviceScope = yield* Effect.scope
      const identityTimeout = options.identityTimeout ?? "30 seconds"
      const launchWatchInterval = options.launchWatchInterval ?? "500 millis"

      /** The observed session per thread; the child scope closes it. */
      interface Observation {
        readonly providerSessionId: string
        readonly scope: Scope.Closeable
      }
      const observed = new Map<string, Observation>()
      /** Threads with an openTerminal in flight — the one-open guard. */
      const opening = new Set<string>()
      /**
       * Per-thread lifecycle serialization (the header's serialization
       * bullet). Entries are dropped when the thread row is deleted; a
       * waiter still queued on a dropped semaphore just finds the row gone.
       */
      const lifecycleLocks = new Map<string, Semaphore.Semaphore>()
      const withLifecycleLock =
        (id: string) =>
        <A, E, R>(effect: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> =>
          Effect.suspend(() => {
            const lock = lifecycleLocks.get(id) ?? Semaphore.makeUnsafe(1)
            lifecycleLocks.set(id, lock)
            return lock.withPermit(effect)
          })

      const adapterFor = (record: ThreadRecord): AgentAdapter | undefined =>
        isAgentId(record.agentId) ? registry.adapterFor(record.agentId) : undefined

      const isBusy = isBusyActivity

      /**
       * The unread overlay (ATC-160): a finish no client has viewed yet.
       * Server-derived so every client renders one boolean; the raw stamps
       * stay server-only (like confirmedAt). Archived rows are never unread
       * — archive is a deliberate put-away, not something to surface. The
       * strict `<` is exact, not a tie-break: the repository lands each
       * stamp strictly ordered against the opposing one, so write order —
       * never clock resolution — decides unread (a finish and a view in the
       * same millisecond once made a later finish invisible forever).
       */
      const isUnread = (record: ThreadRecord): boolean =>
        record.archivedAt === undefined &&
        record.lastFinishedAt !== undefined &&
        (record.lastViewedAt === undefined || record.lastViewedAt < record.lastFinishedAt)

      /**
       * Start (once) the thread's normalized activity subscription. The
       * consumer keeps the in-memory snapshot current and persists the
       * confirmed marker at the first busy evidence it sees (the header's
       * confirmation invariant).
       */
      const ensureObserved = (record: ThreadRecord): Effect.Effect<void> =>
        Effect.gen(function* () {
          const adapter = adapterFor(record)
          const providerSessionId = record.providerSessionId
          if (adapter === undefined || providerSessionId === undefined) return
          const existing = observed.get(record.id)
          if (existing?.providerSessionId === providerSessionId) return
          // A superseded session's subscription must never keep driving
          // the thread (its feed would confirm a session it never saw).
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
              .observeSession({
                providerSessionId,
                providerMetadata: record.providerMetadata,
              })
              .pipe(
                Effect.catchTag("AgentUnavailable", () =>
                  Effect.succeed(Stream.empty as Stream.Stream<AgentSessionEvent>),
                ),
                Scope.provide(child),
              )
            yield* stream.pipe(
              Stream.runForEach((event) =>
                Effect.gen(function* () {
                  if (event.type === "userPrompt") {
                    yield* naming.notePrompt(record, event.text)
                    return
                  }
                  // Conversation items observed on the shared server feed
                  // the runtime's transcript copy (ATC-193).
                  if (event.type !== "activity") {
                    yield* runtime.recordObserved(record, event)
                    return
                  }
                  const activity = event.activity
                  // The ledger applies the confirm / finish / publish rules.
                  const { previous } = yield* runtime.noteActivity(record, activity)
                  // The busy→idle drop is the turn's end — for a turn ATC
                  // did not drive, the moment to re-read the provider's
                  // history (forked: the drain must never wait on the
                  // provider).
                  if (previous !== undefined && isBusy(previous) && activity === "idle") {
                    yield* runtime.observedIdle(record).pipe(Effect.forkIn(serviceScope))
                  }
                }),
              ),
              // A feed that ends on its own must not pin the entry (the
              // next read re-subscribes instead of trusting a dead stream)
              // nor leak its child scope into the service scope. An ended
              // observation also ends the refinement's wait — its turn-end
              // evidence is gone, so the catch-up must not park forever.
              Effect.ensuring(naming.noteFeedEnded(record).pipe(Effect.andThen(releaseClaim))),
              Effect.forkIn(child),
            )
          }).pipe(
            // A caller interrupted (a dropped read) or failing before the
            // drain fiber arms must not leave a claim with no consumer —
            // that would freeze the thread's activity until shutdown.
            Effect.onExit((exit) => (exit._tag === "Failure" ? releaseClaim : Effect.void)),
          )
        })

      const unobserve = (threadId: string): Effect.Effect<void> =>
        Effect.gen(function* () {
          yield* runtime.clearActivity(threadId)
          const observation = observed.get(threadId)
          if (observation === undefined) return
          observed.delete(threadId)
          yield* Scope.close(observation.scope, Exit.void)
        })

      /**
       * The activity snapshot for one read. A busy state whose driver (the
       * live linked terminal) is gone is presumed stale and re-derived
       * from the adapter's reconciliation check — unless a live observation
       * whose feed outlives the TUI still covers the session (the
       * shared-server short-circuit below), in which case the feed is the
       * evidence and no re-derivation is owed. Returns the record alongside
       * the activity: a finish discovered here stamps the row, and the read
       * that discovered it must itself present the refreshed state.
       */
      const resolveActivity = (
        record: ThreadRecord,
        linked: string | undefined,
      ): Effect.Effect<{ readonly activity: AgentActivity; readonly record: ThreadRecord }> =>
        Effect.gen(function* () {
          if (record.providerSessionId === undefined) return { activity: "idle" as const, record }
          const live = (yield* runtime.activity(record.id)) ?? "unknown"
          if (!isBusy(live) || linked !== undefined) return { activity: live, record }
          // The runtime's own turn is the evidence — never re-derived.
          if (yield* runtime.isDriving(record.id)) return { activity: live, record }
          const adapter = adapterFor(record)
          // A live observation whose feed outlives the TUI (shared-server
          // providers) is already authoritative — it drives liveActivity —
          // so a busy snapshot under it is current, not a dead turn to
          // re-derive. Hooks-fed observations die silently with the TUI,
          // so their busy states must still re-derive below (for Codex the
          // re-derivation costs paginated provider walks; skipping it here
          // is what keeps the listing hot path off the provider).
          if (
            adapter?.observationOutlivesTui === true &&
            observed.get(record.id)?.providerSessionId === record.providerSessionId
          ) {
            return { activity: live, record }
          }
          const providerSessionId = record.providerSessionId
          const checked =
            adapter === undefined
              ? ("unknown" as const)
              : yield* adapter
                  .checkSession({ providerSessionId })
                  .pipe(Effect.orElseSucceed(() => "unknown" as const))
          // The ledger stamps a finish discovered here (the busy snapshot
          // was real evidence and the check says the turn is over) and
          // publishes the change to every subscriber. Loop-safe: the busy
          // snapshot is gone, so the next event-triggered read returns at
          // the short-circuit above without publishing. The record is
          // re-read so the discovering response itself presents the thread
          // unread — never a stale `unread: false` racing the refetch.
          yield* runtime.noteActivity(record, checked)
          if (checked === "idle") {
            // A turn ended under observation, discovered here: the same
            // moment the drain loop reports (re-read, drain the queue).
            yield* runtime.observedIdle(record).pipe(Effect.forkIn(serviceScope))
          }
          const current =
            checked === "idle"
              ? yield* repository.get(record.id).pipe(Effect.map(Option.getOrElse(() => record)))
              : record
          return { activity: checked, record: current }
        })

      const toThread = (
        record: ThreadRecord,
        linkedTerminalId: string | undefined,
        activityState: AgentActivity,
      ): Thread => ({
        id: record.id,
        projectId: record.projectId,
        // Permissive read (see the repository header); a foreign slug fails
        // response encoding for that row, never the domain logic.
        agentId: record.agentId as Thread["agentId"],
        ...(record.name !== undefined ? { name: record.name } : {}),
        workingDirectory: record.workingDirectory,
        activityState,
        unread: isUnread(record),
        ...(linkedTerminalId !== undefined ? { linkedTerminalId } : {}),
        ...(record.pinnedAt !== undefined ? { pinnedAt: record.pinnedAt } : {}),
        ...(record.archivedAt !== undefined ? { archivedAt: record.archivedAt } : {}),
        createdAt: record.createdAt,
        updatedAt: record.updatedAt,
      })

      /**
       * The thread's live linked terminal, if any, from the reconciled
       * terminals listing — a terminal that just died stops being "linked"
       * the moment the inventory says so, not at its next explicit read.
       */
      const linkedFor = (record: ThreadRecord): Effect.Effect<Terminal | undefined> =>
        terminals
          .list({ projectId: record.projectId })
          .pipe(
            Effect.map((list) => list.find((t) => t.threadId === record.id && t.status === "live")),
          )

      /**
       * Assemble one Thread from an already-looked-up linked terminal —
       * listing callers batch one reconciled inventory pass for the whole
       * page instead of one per record.
       */
      const assemble = (
        record: ThreadRecord,
        linked: Terminal | undefined,
      ): Effect.Effect<Thread> =>
        Effect.gen(function* () {
          // Demand-driven recovery: a restart under a live TUI re-observes
          // on the first read, so status flows again with no poller. NOT
          // while an open is in flight: a mid-materialize row still names
          // the superseded session, and a read subscribing to it could
          // confirm a session the thread no longer references — the open's
          // own ensureObserved covers the fresh identity.
          if (linked !== undefined && !opening.has(record.id)) yield* ensureObserved(record)
          const resolved = yield* resolveActivity(record, linked?.id)
          return toThread(resolved.record, linked?.id, resolved.activity)
        })

      /** `assemble` for single-record callers (one listing pass of its own). */
      const assembleAlone = (record: ThreadRecord): Effect.Effect<Thread> =>
        linkedFor(record).pipe(Effect.flatMap((linked) => assemble(record, linked)))

      const mapAgentError =
        (record: ThreadRecord) =>
        (
          error:
            | AgentUnavailable
            | AgentProtocolError
            | AgentIdentityMismatch
            | AgentResumeFailed
            | AgentConflict,
        ) =>
          error._tag === "AgentUnavailable" || error._tag === "AgentProtocolError"
            ? new ProviderUnavailable({ agentId: record.agentId, reason: error.message })
            : new ProviderSessionConflict({ threadId: record.id, reason: error.message })

      /**
       * Fail once the reconciled inventory says the launched TUI terminal
       * died — the classic first-run failure (a missing provider login)
       * would otherwise park the open for the full identity bound with a
       * generic timeout. Each check is a multiplexer inventory, so the
       * poll backs off from `launchWatchInterval` to a 5-second ceiling.
       * Claude never reaches the race — its identity resolves immediately.
       */
      const watchForEarlyDeath = (
        record: ThreadRecord,
        terminalId: string,
      ): Effect.Effect<never, ProviderUnavailable> => {
        const requireAlive = Effect.gen(function* () {
          const current = yield* terminals
            .get(terminalId)
            .pipe(Effect.catchTag("TerminalNotFound", () => Effect.succeed(undefined)))
          if (current === undefined || current.status === "ended") {
            return yield* Effect.fail(
              new ProviderUnavailable({
                agentId: record.agentId,
                reason:
                  `the ${record.agentId} TUI exited before its session was established; ` +
                  `run it manually in the thread's directory to see why (a missing provider login is the usual cause)`,
              }),
            )
          }
        })
        // The deliberate initial delay, then checks backing off from twice
        // that interval to a 5-second ceiling — the loop only ever exits by
        // failing (the launch wait races it against identity resolution).
        // The repeat wraps ONLY the check: wrapping the initial sleep too
        // would add it to every scheduled delay.
        return Effect.sleep(launchWatchInterval).pipe(
          Effect.andThen(
            requireAlive.pipe(
              Effect.repeat({
                schedule: Schedule.min([
                  Schedule.exponential(
                    Duration.times(Duration.fromInputUnsafe(launchWatchInterval), 2),
                  ),
                  Schedule.spaced("5 seconds"),
                ]),
              }),
            ),
          ),
          Effect.andThen(Effect.never),
        )
      }

      /** Launch a TUI terminal for the thread from an adapter launch spec,
       * with the nested-session markers scrubbed uniformly. */
      const createTuiTerminal = (record: ThreadRecord, spec: TuiLaunchSpec) =>
        terminals
          .create({
            projectId: record.projectId,
            threadId: record.id,
            ...(record.name !== undefined ? { name: record.name } : {}),
            command: spec.command,
            workingDirectory: record.workingDirectory,
            env: {
              ...spec.env,
              ...Object.fromEntries(NESTED_SESSION_ENV_VARIABLES.map((name) => [name, undefined])),
            },
          })
          // The owning project cannot vanish under a live thread (FK).
          .pipe(Effect.catchTag("ProjectNotFound", (error) => Effect.die(error)))

      /**
       * The single identity-establishment transition: launch fresh via the
       * uniform prepareTuiSession contract, await verified identity
       * (bounded), persist it. openTerminal is its first caller; a native
       * first-prompt is the future second one.
       */
      const materialize = (record: ThreadRecord, adapter: AgentAdapter) =>
        Effect.scoped(
          Effect.gen(function* () {
            // A superseded unconfirmed session is dead to ATC: stop its
            // feed (it must never confirm the successor) and release its
            // adapter resources before minting the replacement.
            if (record.providerSessionId !== undefined) {
              yield* unobserve(record.id)
              yield* adapter.releaseSession({
                providerSessionId: record.providerSessionId,
                providerMetadata: record.providerMetadata,
              })
            }
            const prepared = yield* adapter
              .prepareTuiSession({ cwd: record.workingDirectory })
              .pipe(Effect.mapError(mapAgentError(record)))
            const terminal = yield* createTuiTerminal(record, prepared.launchSpec)
            const cleanupTerminal = terminals
              .delete(terminal.id)
              .pipe(Effect.catch(() => Effect.void))
            yield* Effect.uninterruptibleMask((restore) =>
              Effect.gen(function* () {
                // The identity await stays interruptible (openTerminal's
                // bound must be able to cancel it), but the mask leaves no
                // window where interruption can land between resolution and
                // the release bracket below.
                const identity = yield* restore(
                  Effect.raceFirst(
                    prepared.identity.pipe(Effect.mapError(mapAgentError(record))),
                    watchForEarlyDeath(record, terminal.id),
                  ),
                )
                // From resolution until a thread row owns the identity, the
                // fresh session is OURS: every non-success — the row deleted
                // mid-open (the ATC-139 delete-vs-starting-terminal window),
                // a persistence defect, or interruption — must release the
                // adapter's session resources; nothing else will.
                let adopted = false
                yield* restore(
                  Effect.gen(function* () {
                    const still = yield* repository.get(record.id)
                    if (Option.isNone(still)) {
                      return yield* Effect.fail(new ThreadNotFound({ threadId: record.id }))
                    }
                    const updated = yield* repository.setProviderSession(
                      record.id,
                      identity.providerSessionId,
                      identity.providerMetadata ?? null,
                    )
                    adopted = true
                    yield* ensureObserved(updated)
                  }),
                ).pipe(
                  Effect.onExit((exit) =>
                    exit._tag === "Failure" && !adopted
                      ? adapter.releaseSession({
                          providerSessionId: identity.providerSessionId,
                          providerMetadata: identity.providerMetadata,
                        })
                      : Effect.void,
                  ),
                )
              }),
            ).pipe(
              // On ANY non-success — failure, defect, or interruption (a
              // dropped client cancels the request fiber) — the terminal
              // must not survive as a live TUI the thread adopted nothing
              // from.
              Effect.onExit((exit) => (exit._tag === "Failure" ? cleanupTerminal : Effect.void)),
            )
            return terminal
          }),
        )

      /** Reopen a confirmed session: exact identity, mismatches fail closed. */
      const reopen = (record: ThreadRecord, adapter: AgentAdapter) =>
        Effect.gen(function* () {
          const providerSessionId = record.providerSessionId ?? ""
          const launch = yield* adapter
            .tuiLaunch({
              providerSessionId,
              cwd: record.workingDirectory,
              providerMetadata: record.providerMetadata,
            })
            .pipe(Effect.mapError(mapAgentError(record)))
          // Deleted mid-open: adopt nothing (the metadata write would die
          // on the vanished row, the terminal would violate the FK).
          const still = yield* repository.get(record.id)
          if (Option.isNone(still)) {
            return yield* Effect.fail(new ThreadNotFound({ threadId: record.id }))
          }
          const current =
            launch.providerMetadata !== undefined &&
            launch.providerMetadata !== record.providerMetadata
              ? yield* repository.setProviderMetadata(record.id, launch.providerMetadata)
              : record
          const terminal = yield* createTuiTerminal(current, launch.launchSpec)
          yield* ensureObserved(current)
          return terminal
        })

      /**
       * The launching path, run under the thread's lifecycle lock with the
       * caller already holding the thread's `opening` claim. The record and
       * linked-terminal checks are repeated here: a queued-ahead archive or
       * delete may have changed the world while this caller waited.
       */
      const openUnderLock = (id: string): ReturnType<Threads["Service"]["openTerminal"]> =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          if (record.archivedAt !== undefined) {
            return yield* Effect.fail(new ThreadArchived({ threadId: id }))
          }
          const adapter = adapterFor(record)
          if (adapter === undefined) {
            return yield* Effect.fail(
              new ProviderUnavailable({
                agentId: record.agentId,
                reason: `this build knows no agent "${record.agentId}"`,
              }),
            )
          }
          // Defense in depth: nothing else can have linked a live terminal
          // while this caller held the claim, but returning one beats
          // double-launching if that ever changes.
          const linked = yield* linkedFor(record)
          if (linked !== undefined) return linked
          return yield* (
            record.providerSessionId !== undefined && record.confirmedAt !== undefined
              ? reopen(record, adapter)
              : // Unconfirmed (none, or zero completed turns): re-materialize
                // with fresh identity — there is no history to protect. The
                // bound covers the WHOLE flow (launch-lock queueing and
                // terminal creation included), so a slow provider can never
                // hang the caller past the window.
                materialize(record, adapter).pipe(
                  Effect.timeoutOrElse({
                    duration: identityTimeout,
                    orElse: () =>
                      Effect.fail(
                        new ProviderUnavailable({
                          agentId: record.agentId,
                          reason: `the provider session was not established within ${Duration.format(Duration.fromInputUnsafe(identityTimeout))}; retry`,
                        }),
                      ),
                  }),
                )
          ).pipe(
            // The open changed the thread (linked terminal, and possibly
            // provider identity); the idempotent return above changed nothing.
            Effect.tap(() => events.publish({ resource: "thread", id, change: "updated" })),
          )
        })

      const openTerminal: Threads["Service"]["openTerminal"] = (id) =>
        Effect.gen(function* () {
          // Fast paths stay ahead of the lifecycle lock: the idempotent
          // return must not wait behind an in-flight open's identity await,
          // and open-vs-open keeps its fail-fast conflict instead of
          // queueing a second launch.
          const record = yield* repository.require(id)
          if (record.archivedAt !== undefined) {
            return yield* Effect.fail(new ThreadArchived({ threadId: id }))
          }
          const linked = yield* linkedFor(record)
          if (linked !== undefined) {
            if (!opening.has(id)) yield* ensureObserved(record)
            return linked
          }
          if (opening.has(id)) {
            return yield* Effect.fail(
              new ProviderSessionConflict({
                threadId: id,
                reason: "an open of this thread's terminal is already in progress",
              }),
            )
          }
          // The claim lands HERE, synchronously with the check above and
          // before enqueueing: a simultaneous second open must fail fast at
          // that check rather than silently queue behind this launch for
          // the full identity await. The claim spans the lock wait too.
          opening.add(id)
          return yield* withLifecycleLock(id)(openUnderLock(id)).pipe(
            Effect.ensuring(Effect.sync(() => opening.delete(id))),
          )
        })

      const del: Threads["Service"]["delete"] = (id) =>
        withLifecycleLock(id)(
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            const linked = yield* linkedFor(record)
            if (linked !== undefined) {
              // Confirmed-kill rules live in Terminals.delete; a terminal
              // that vanished since the lookup is already the goal state.
              yield* terminals
                .delete(linked.id)
                .pipe(Effect.catchTag("TerminalNotFound", () => Effect.void))
            }
            yield* runtime.release(id)
            yield* naming.release(id)
            const adapter = adapterFor(record)
            if (adapter !== undefined && record.providerSessionId !== undefined) {
              yield* adapter.releaseSession({
                providerSessionId: record.providerSessionId,
                providerMetadata: record.providerMetadata,
              })
            }
            yield* unobserve(id)
            // Ended linked terminals survive as unlinked tombstones (FK SET
            // NULL) — their client-visible threadId changes with the delete,
            // so each publishes alongside the thread's own event.
            const orphaned = (yield* terminals.list({ projectId: record.projectId })).filter(
              (terminal) => terminal.threadId === id,
            )
            yield* repository.delete(id)
            lifecycleLocks.delete(id)
            yield* events.publish({ resource: "thread", id, change: "deleted" })
            yield* Effect.forEach(orphaned, (terminal) =>
              events.publish({ resource: "terminal", id: terminal.id, change: "updated" }),
            )
          }),
        )

      return {
        list: (listOptions) =>
          Effect.gen(function* () {
            const records = yield* repository.list(listOptions?.projectId)
            const scope = listOptions?.archived ?? "active"
            const wanted = records.filter(
              (record) =>
                scope === "all" ||
                (scope === "archived"
                  ? record.archivedAt !== undefined
                  : record.archivedAt === undefined),
            )
            if (wanted.length === 0) return []
            // One reconciled inventory pass for the whole page.
            const linked = new Map<string, Terminal>()
            for (const terminal of yield* terminals.list({ projectId: listOptions?.projectId })) {
              // Newest first, first write wins — the same pick linkedFor makes.
              if (
                terminal.threadId !== undefined &&
                terminal.status === "live" &&
                !linked.has(terminal.threadId)
              ) {
                linked.set(terminal.threadId, terminal)
              }
            }
            // Bounded fan-out: assembly can hit the provider (resolveActivity
            // re-derivation), so a page of stale-busy records must neither
            // serialize behind each other nor stampede the adapter.
            return yield* Effect.forEach(
              wanted,
              (record) => assemble(record, linked.get(record.id)),
              { concurrency: 8 },
            )
          }),
        get: (id) => repository.require(id).pipe(Effect.flatMap(assembleAlone)),
        create: (input) =>
          Effect.gen(function* () {
            const project = yield* projects.require(input.projectId)
            const canonical = yield* directories.canonicalize(
              input.workingDirectory ?? project.defaultWorkingDirectory,
            )
            const record = yield* repository.create({
              projectId: input.projectId,
              agentId: input.agentId,
              name: input.name,
              workingDirectory: canonical,
            })
            yield* events.publish({ resource: "thread", id: record.id, change: "created" })
            return toThread(record, undefined, "idle")
          }),
        update: (id, patch) =>
          // An empty patch changes nothing — including updatedAt (the same
          // rule the other repositories apply).
          patch.name === undefined
            ? repository.require(id).pipe(Effect.flatMap(assembleAlone))
            : repository.require(id).pipe(
                Effect.andThen(repository.rename(id, patch.name)),
                Effect.tap(() => events.publish({ resource: "thread", id, change: "updated" })),
                Effect.flatMap(assembleAlone),
              ),
        archive: (id) =>
          withLifecycleLock(id)(
            Effect.gen(function* () {
              const record = yield* repository.require(id)
              const linked = yield* linkedFor(record)
              // Repeat archive with nothing live left is a pure no-op; with
              // a lingering live terminal (a legacy row) it falls through to
              // the kill below and converges.
              if (record.archivedAt !== undefined && linked === undefined) {
                return yield* assemble(record, undefined)
              }
              // Busy covers needs_input too: a turn parked on an approval is
              // still mid-turn.
              if (isBusy((yield* resolveActivity(record, linked?.id)).activity)) {
                return yield* Effect.fail(new ThreadBusy({ threadId: id }))
              }
              if (linked !== undefined) {
                // Confirmed-kill rules live in Terminals.delete: a kill that
                // cannot verify death fails ZmxUnavailable and the thread
                // stays active; a terminal that vanished since the lookup is
                // already the goal state.
                yield* terminals
                  .delete(linked.id)
                  .pipe(Effect.catchTag("TerminalNotFound", () => Effect.void))
              }
              // Suspend the runtime only after terminal death is confirmed:
              // release adapter-owned session resources (Claude revokes its
              // hook plumbing; the next tuiLaunch recreates it) and stop
              // observation. The provider conversation itself is untouched,
              // so unarchive + open resumes it exactly.
              yield* runtime.release(id)
              yield* naming.release(id)
              const adapter = adapterFor(record)
              if (adapter !== undefined && record.providerSessionId !== undefined) {
                yield* adapter.releaseSession({
                  providerSessionId: record.providerSessionId,
                  providerMetadata: record.providerMetadata,
                })
              }
              yield* unobserve(id)
              if (record.archivedAt !== undefined) return yield* assemble(record, undefined)
              const updated = yield* repository.setArchived(id, true)
              yield* events.publish({ resource: "thread", id, change: "updated" })
              return yield* assemble(updated, undefined)
            }),
          ),
        unarchive: (id) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            if (record.archivedAt === undefined) return yield* assembleAlone(record)
            const updated = yield* repository.setArchived(id, false)
            yield* events.publish({ resource: "thread", id, change: "updated" })
            return yield* assembleAlone(updated)
          }),
        pin: (id) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            if (record.archivedAt !== undefined) {
              return yield* Effect.fail(new ThreadArchived({ threadId: id }))
            }
            if (record.pinnedAt !== undefined) return yield* assembleAlone(record)
            const updated = yield* repository.setPinned(id, true)
            yield* events.publish({ resource: "thread", id, change: "updated" })
            return yield* assembleAlone(updated)
          }),
        unpin: (id) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            if (record.pinnedAt === undefined) return yield* assembleAlone(record)
            const updated = yield* repository.setPinned(id, false)
            yield* events.publish({ resource: "thread", id, change: "updated" })
            return yield* assembleAlone(updated)
          }),
        markViewed: (id) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            // The no-op guard is what lets clients stamp on EVERY view:
            // an already-read thread costs no write and — crucially — no
            // publish, so routine opens never trigger a fleet-wide refetch.
            if (!isUnread(record)) return yield* assembleAlone(record)
            const updated = yield* repository.markViewed(id)
            // Clients stamp automatically, so a view racing a delete is an
            // expected condition — the endpoint's typed 404, not a defect.
            if (Option.isNone(updated)) {
              return yield* Effect.fail(new ThreadNotFound({ threadId: id }))
            }
            yield* events.publish({ resource: "thread", id, change: "updated" })
            return yield* assembleAlone(updated.value)
          }),
        delete: del,
        openTerminal,
      }
    }),
  )

export const layer = layerWith({})
