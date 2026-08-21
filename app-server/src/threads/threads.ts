import { Context, Duration, Effect, Layer, Option, Schedule, Semaphore } from "effect"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConflict,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentResumeFailed,
  AgentUnavailable,
} from "../agents/agentAdapter.ts"
import { isBusyActivity } from "../agents/agentAdapter.ts"
import {
  InvalidThreadSettings,
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
  ThreadSettingsPatch,
  ZmxUnavailable,
} from "../api/contract.ts"
import { SqlClient } from "effect/unstable/sql"
import { Events } from "../events/events.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "../projects/projectRepository.ts"
import { Terminals } from "../terminals/terminals.ts"
import type { Terminal } from "../terminals/terminals.ts"
import { ThreadNaming } from "./threadNaming.ts"
import { Attachments } from "./attachments.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"
import { ThreadRuntime } from "./threadRuntime.ts"
import { applySettingsPatch, sameSettings } from "./threadSettings.ts"
import type { ThreadSettings } from "./threadSettings.ts"
import { ThreadTui } from "./threadTui.ts"

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
//     drives a turn and a prompt may arrive while a TUI is live — the only
//     guard is the in-progress state plus the queue, and nothing binds a
//     Thread to one surface. Which PROCESS runs the session is the
//     runtime's affair (ATC-203): openTerminal and closeTerminal only tell
//     it what clients show (`openTui` / `closeTui`), and it hands a
//     one-process provider's session between the TUI and its own turns.
//   - workingDirectory is validated, canonicalized, and immutable.
//   - activityState is in-memory evidence, never persisted, and lives in
//     the ThreadRuntime's activity ledger — fed by its observation of a
//     TUI-driven session (started here on demand: `runtime.observe`
//     whenever a read finds a live linked TUI, or an open launches one) and
//     by its native turns, one set of rules (first busy confirms, busy→idle
//     stamps the finish, every transition publishes). `unknown` when there
//     is no evidence; a busy state whose driver is gone re-derives
//     demand-driven from the adapter's reconciliation check on read, unless
//     the runtime is driving the thread (its evidence is authoritative) or a
//     shared-server observation still covers it. The confirmed marker is
//     persisted at the FIRST busy signal — a submitted prompt is already
//     durable provider history worth protecting — the same signal for every
//     provider.
//   - The unread overlay (ATC-160) is derived, never stored as a flag:
//     last_finished_at is stamped at the observed busy→idle drop — the
//     ledger and the driver-gone re-derivation alike — last_viewed_at by
//     markViewed, and archived rows are never unread. The activity
//     vocabulary is untouched; "Done" is client-side display translation.
//   - Auto-naming lives in ThreadNaming (ATC-155/190/202), fed by the
//     runtime's observation and turns.
//   - Settings (ATC-205) are stored state the runtime hands to the adapter
//     at every turn start; a change here never touches a provider — it
//     takes effect at the next turn, and a turn in flight is never
//     disturbed. Only a model or reasoning change consults the agent's
//     model catalog (validation and the per-model reasoning rule,
//     threadSettings.ts). Every change writes through to the agent's
//     defaults, which `create` reads — a new thread starts as the last
//     changed one ended. Provider-side changes are adopted by the runtime
//     (its `settings` events), never here.
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
      patch: {
        readonly name?: string | undefined
        readonly settings?: typeof ThreadSettingsPatch.Type | undefined
      },
    ) => Effect.Effect<Thread, ThreadNotFound | InvalidThreadSettings | ProviderUnavailable>
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
    /** Open (or return) the thread's TUI terminal — the header's workflow.
     * ThreadBusy: a one-process provider's native turn is running; the
     * runtime launches the TUI when it ends. */
    readonly openTerminal: (
      id: string,
    ) => Effect.Effect<
      Terminal,
      | ThreadNotFound
      | ThreadArchived
      | ThreadBusy
      | ProviderUnavailable
      | ProviderSessionConflict
      | DirectoryUnavailable
      | DirectoryCheckTimedOut
      | ZmxUnavailable
      | TerminalLaunchFailed
    >
    /** The client stopped showing the TUI: hand the thread back to the
     * native side (`runtime.closeTui` — the runtime's ownership rules). */
    readonly closeTerminal: (
      id: string,
    ) => Effect.Effect<
      Thread,
      ThreadNotFound | ProviderUnavailable | ProviderSessionConflict | ZmxUnavailable
    >
  }
>()("app-server/Threads") {}

export const layerWith = (options: ThreadsOptions) =>
  Layer.effect(Threads)(
    Effect.gen(function* () {
      const repository = yield* ThreadRepository
      const attachments = yield* Attachments
      const projects = yield* ProjectRepository
      const directories = yield* Directories
      const terminals = yield* Terminals
      const registry = yield* AgentRegistry
      const events = yield* Events
      const runtime = yield* ThreadRuntime
      const naming = yield* ThreadNaming
      const tui = yield* ThreadTui
      // No SQL is written here: the client is held for `withTransaction`
      // alone, so the settings write and its defaults write-through commit
      // together (both repositories run on this one client).
      const sql = yield* SqlClient.SqlClient
      // Forked work (a discovered finish's re-read) outlives its request;
      // it lives in the service's own scope.
      const serviceScope = yield* Effect.scope
      const identityTimeout = options.identityTimeout ?? "30 seconds"
      const launchWatchInterval = options.launchWatchInterval ?? "500 millis"

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
          // The runtime's own connection is the evidence — never re-derived.
          if (yield* runtime.hasWriter(record.id)) return { activity: live, record }
          const adapter = adapterFor(record)
          // A live observation whose feed outlives the TUI (shared-server
          // providers) is already authoritative — it drives liveActivity —
          // so a busy snapshot under it is current, not a dead turn to
          // re-derive. Hooks-fed observations die silently with the TUI,
          // so their busy states must still re-derive below (for Codex the
          // re-derivation costs paginated provider walks; skipping it here
          // is what keeps the listing hot path off the provider).
          if (adapter?.sharedServer === true && (yield* runtime.observing(record))) {
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
        settings: record.settings,
        activityState,
        unread: isUnread(record),
        ...(linkedTerminalId !== undefined ? { linkedTerminalId } : {}),
        ...(record.pinnedAt !== undefined ? { pinnedAt: record.pinnedAt } : {}),
        ...(record.archivedAt !== undefined ? { archivedAt: record.archivedAt } : {}),
        createdAt: record.createdAt,
        updatedAt: record.updatedAt,
      })

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
          // own observe covers the fresh identity.
          if (linked !== undefined && !opening.has(record.id)) yield* runtime.observe(record)
          const resolved = yield* resolveActivity(record, linked?.id)
          return toThread(resolved.record, linked?.id, resolved.activity)
        })

      /** `assemble` for single-record callers (one listing pass of its own). */
      const assembleAlone = (record: ThreadRecord): Effect.Effect<Thread> =>
        tui.linked(record).pipe(Effect.flatMap((linked) => assemble(record, linked)))

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
              yield* runtime.unobserve(record.id)
              yield* adapter.releaseSession({
                providerSessionId: record.providerSessionId,
                providerMetadata: record.providerMetadata,
              })
            }
            const prepared = yield* adapter
              .prepareTuiSession({ cwd: record.workingDirectory })
              .pipe(Effect.mapError(mapAgentError(record)))
            const terminal = yield* tui.create(record, prepared.launchSpec)
            const cleanupTerminal = tui.end(terminal.id).pipe(Effect.catch(() => Effect.void))
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
                    yield* runtime.observe(updated)
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
          const launched = yield* tui.reopen(record, adapter)
          yield* runtime.observe(launched.record)
          return launched.terminal
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
          const launch =
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
          // The runtime's ownership rules wrap the launch (the header) —
          // including the last "already live" check, so a relaunch the
          // runtime made while this caller waited is returned, never
          // doubled.
          return yield* runtime.openTui(record, launch).pipe(
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
          const linked = yield* tui.linked(record)
          if (linked !== undefined) {
            if (!opening.has(id)) yield* runtime.observe(record)
            // Already live — still an open: the TUI is wanted again (a
            // pending Chat hand-off is called off).
            return yield* runtime.openTui(record, Effect.succeed(linked))
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

      const closeTerminal: Threads["Service"]["closeTerminal"] = (id) =>
        withLifecycleLock(id)(
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            yield* runtime.closeTui(record)
            return yield* assembleAlone(record)
          }),
        )

      const del: Threads["Service"]["delete"] = (id) =>
        withLifecycleLock(id)(
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            // The runtime first: it waits out a TUI relaunch in flight and
            // forbids another, so the kill below sees every terminal.
            // Confirmed-kill rules live in Terminals.delete; a terminal that
            // vanished since the lookup is already the goal state.
            yield* runtime.release(id)
            const linked = yield* tui.linked(record)
            if (linked !== undefined) yield* tui.end(linked.id)
            yield* naming.release(id)
            const adapter = adapterFor(record)
            if (adapter !== undefined && record.providerSessionId !== undefined) {
              yield* adapter.releaseSession({
                providerSessionId: record.providerSessionId,
                providerMetadata: record.providerMetadata,
              })
            }
            // Ended linked terminals survive as unlinked tombstones (FK SET
            // NULL) — their client-visible threadId changes with the delete,
            // so each publishes alongside the thread's own event.
            const orphaned = (yield* terminals.list({ projectId: record.projectId })).filter(
              (terminal) => terminal.threadId === id,
            )
            yield* repository.delete(id)
            // The rows cascaded with the thread; the bytes on disk are ours
            // to remove (ATC-216).
            yield* attachments.purge(id)
            lifecycleLocks.delete(id)
            yield* events.publish({ resource: "thread", id, change: "deleted" })
            yield* Effect.forEach(orphaned, (terminal) =>
              events.publish({ resource: "terminal", id: terminal.id, change: "updated" }),
            )
          }),
        )

      /**
       * Resolve the settings half of an `update` (the header's settings
       * bullet) BEFORE anything is written: the catalog is consulted only
       * when the patch touches model or reasoning, so an access or mode
       * change never waits on — or fails with — the provider, and a
       * rejection leaves the row (name included) untouched.
       */
      const resolveSettings = (
        record: ThreadRecord,
        patch: typeof ThreadSettingsPatch.Type,
      ): Effect.Effect<ThreadSettings, InvalidThreadSettings | ProviderUnavailable> =>
        Effect.gen(function* () {
          const needsCatalog = patch.model !== undefined || patch.reasoning !== undefined
          const catalog = needsCatalog
            ? yield* registry.models(record.agentId).pipe(
                Effect.catchTag("AgentNotFound", () =>
                  Effect.fail(
                    new ProviderUnavailable({
                      agentId: record.agentId,
                      reason: `this build knows no agent "${record.agentId}"`,
                    }),
                  ),
                ),
              )
            : null
          const applied = applySettingsPatch(record.settings, patch, catalog)
          if ("rejected" in applied) {
            return yield* Effect.fail(
              new InvalidThreadSettings({ threadId: record.id, reason: applied.rejected }),
            )
          }
          return applied.settings
        })

      /**
       * Apply and store a settings patch: resolve it against the row as it
       * stands, write only while the row still stands there (the
       * repository's compare-and-swap), and on losing that race — a
       * provider report or another patch landed in between — re-read and
       * re-apply the patch on top, so a partial patch never overwrites a
       * change it did not make. A patch that resolves to what the row
       * already holds is a no-op: no write, no defaults write-through, no
       * event (a re-pick of the checked entry must never reset another
       * thread's future defaults). The write-through to the agent's
       * defaults carries the thread's whole settings, so a new thread
       * starts exactly as this one now stands; it commits with the row.
       */
      const patchSettings = (
        record: ThreadRecord,
        patch: typeof ThreadSettingsPatch.Type,
      ): Effect.Effect<
        { readonly record: ThreadRecord; readonly changed: boolean },
        ThreadNotFound | InvalidThreadSettings | ProviderUnavailable
      > =>
        Effect.gen(function* () {
          let current = record
          for (let attempt = 0; attempt < 8; attempt++) {
            const settings = yield* resolveSettings(current, patch)
            if (sameSettings(settings, current.settings)) return { record: current, changed: false }
            const written = yield* sql
              .withTransaction(
                Effect.gen(function* () {
                  const updated = yield* repository.setSettingsIfUnchanged(
                    current.id,
                    current.settings,
                    settings,
                  )
                  if (Option.isSome(updated) && isAgentId(current.agentId)) {
                    yield* registry.setDefaults(current.agentId, settings)
                  }
                  return updated
                }),
                // A transaction failure is a database failure: a defect, like
                // the repositories' own.
              )
              .pipe(Effect.orDie)
            if (Option.isSome(written)) return { record: written.value, changed: true }
            // Lost the race (or the row vanished: require's typed 404).
            current = yield* repository.require(record.id)
          }
          return yield* Effect.die(new Error(`thread ${record.id}: settings never settled`))
        })

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
              settings: yield* registry.defaults(input.agentId),
            })
            yield* events.publish({ resource: "thread", id: record.id, change: "created" })
            return toThread(record, undefined, "idle")
          }),
        update: (id, patch) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            // Settings are validated BEFORE the rename lands (a rejection
            // leaves the row untouched, name included); a patch that
            // changes nothing changes nothing — updatedAt included, the
            // rule the other repositories apply — and publishes nothing.
            const settled =
              patch.settings === undefined || Object.keys(patch.settings).length === 0
                ? { record, changed: false }
                : yield* patchSettings(record, patch.settings)
            const renamed =
              patch.name === undefined ? settled.record : yield* repository.rename(id, patch.name)
            if (!settled.changed && patch.name === undefined) {
              return yield* assembleAlone(record)
            }
            yield* events.publish({ resource: "thread", id, change: "updated" })
            return yield* assembleAlone(renamed)
          }),
        archive: (id) =>
          withLifecycleLock(id)(
            Effect.gen(function* () {
              const record = yield* repository.require(id)
              const linked = yield* tui.linked(record)
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
              // Suspend the runtime first: it waits out a TUI relaunch in
              // flight and forbids another, so the kill sees every terminal
              // (its run and observation are gone either way — the busy
              // check above already found nothing running). Then the
              // confirmed-kill rules of Terminals.delete: a kill that cannot
              // verify death fails ZmxUnavailable and the thread stays
              // active (a re-archive converges); a terminal that vanished
              // since the lookup is already the goal state. Then release
              // adapter-owned session resources (Claude revokes its hook
              // plumbing; the next tuiLaunch recreates it). The provider
              // conversation itself is untouched, so unarchive + open
              // resumes it exactly.
              yield* runtime.release(id)
              const live = yield* tui.linked(record)
              if (live !== undefined) yield* tui.end(live.id)
              yield* naming.release(id)
              const adapter = adapterFor(record)
              if (adapter !== undefined && record.providerSessionId !== undefined) {
                yield* adapter.releaseSession({
                  providerSessionId: record.providerSessionId,
                  providerMetadata: record.providerMetadata,
                })
              }
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
        closeTerminal,
      }
    }),
  )

export const layer = layerWith({})
