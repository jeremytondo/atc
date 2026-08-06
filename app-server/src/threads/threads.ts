import { Context, Duration, Effect, Exit, Layer, Option, Scope, Stream } from "effect"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConflict,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentResumeFailed,
  AgentUnavailable,
  TuiLaunchSpec,
} from "../agents/agentAdapter.ts"
import { NESTED_SESSION_ENV_VARIABLES } from "../agents/agentAdapter.ts"
import {
  AGENT_IDS,
  ProviderSessionConflict,
  ProviderUnavailable,
  Thread as ThreadSchema,
  ThreadArchived,
  ThreadBusy,
  ThreadNotFound,
} from "../api/contract.ts"
import type {
  AgentId,
  CreateThreadRequest,
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
  ProjectNotFound,
  TerminalLaunchFailed,
  ZmxUnavailable,
} from "../api/contract.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "../projects/projectRepository.ts"
import { Terminals } from "../terminals/terminals.ts"
import type { Terminal } from "../terminals/terminals.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"

export type Thread = typeof ThreadSchema.Type

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
//   - The writer rule is a WRITE SEAT, not a terminal check (ATC-124
//     surface-agnostic follow-up): the live-linked-terminal check above IS
//     the V1 seat check, because a TUI terminal is the only holder that
//     can exist yet. Native mode adds the second holder type — the
//     adapter connection — here, so "open a TUI while native drives" and
//     "prompt natively while a TUI is live" become the same conflict;
//     nothing ever binds a Thread to one surface (sequential cross-surface
//     alternation stays supported).
//   - workingDirectory is validated, canonicalized, and immutable.
//   - activityState is in-memory evidence from the one normalized status
//     feed, never persisted; `unknown` when there is no evidence, and a
//     busy state whose driver (the seat holder: the linked terminal in
//     V1, the adapter connection in native mode) is gone re-derives
//     demand-driven from the adapter's reconciliation check on read. The
//     confirmed marker is persisted at the FIRST busy signal observed on
//     the feed — a submitted prompt is already durable provider history
//     worth protecting, and writing at onset (not busy→idle) keeps the
//     marker across a restart or observer gap mid-first-turn — the same
//     signal for every provider.
//   - delete kills the live linked TUI terminal first (the terminals
//     confirmed-kill rules), releases adapter-owned session resources,
//     then removes the row; ended linked terminals stay as unlinked
//     tombstones (FK SET NULL), and provider-owned conversation history
//     is never touched.

export interface ThreadsOptions {
  /** How long openTerminal waits for a fresh session's identity. */
  readonly identityTimeout?: Duration.Input
}

export class Threads extends Context.Service<
  Threads,
  {
    /** Listing, newest first; archived threads only on request. */
    readonly list: (options?: {
      readonly projectId?: string | undefined
      readonly archived?: boolean | undefined
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
    /** Idempotent; refused while the agent session is actively working. */
    readonly archive: (id: string) => Effect.Effect<Thread, ThreadNotFound | ThreadBusy>
    /** Idempotent. */
    readonly unarchive: (id: string) => Effect.Effect<Thread, ThreadNotFound>
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

const isAgentId = (id: string): id is typeof AgentId.Type =>
  (AGENT_IDS as ReadonlyArray<string>).includes(id)

export const layerWith = (options: ThreadsOptions) =>
  Layer.effect(Threads)(
    Effect.gen(function* () {
      const repository = yield* ThreadRepository
      const projects = yield* ProjectRepository
      const directories = yield* Directories
      const terminals = yield* Terminals
      const registry = yield* AgentRegistry
      // Observation fibers outlive their originating requests; they live in
      // the service's own scope so shutdown reaps every subscription.
      const serviceScope = yield* Effect.scope
      const identityTimeout = options.identityTimeout ?? "30 seconds"

      // Live activity evidence by thread id, fed by the per-session
      // subscriptions below. In-memory only — restart resets to no
      // evidence, and reads re-derive on demand.
      const liveActivity = new Map<string, AgentActivity>()
      /** The observed session per thread; the child scope closes it. */
      interface Observation {
        readonly providerSessionId: string
        readonly scope: Scope.Closeable
      }
      const observed = new Map<string, Observation>()
      /** Threads with an openTerminal in flight — the one-open guard. */
      const opening = new Set<string>()

      const adapterFor = (record: ThreadRecord): AgentAdapter | undefined =>
        isAgentId(record.agentId) ? registry.adapterFor(record.agentId) : undefined

      const isBusy = (activity: AgentActivity): boolean =>
        activity === "working" || activity === "needs_input"

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
          const child = yield* Scope.fork(serviceScope)
          const observation: Observation = { providerSessionId, scope: child }
          observed.set(record.id, observation)
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
                Effect.succeed(Stream.empty as Stream.Stream<AgentActivity>),
              ),
              Scope.provide(child),
            )
          let confirmed = record.confirmedAt !== undefined
          yield* stream.pipe(
            Stream.runForEach((activity) =>
              Effect.gen(function* () {
                liveActivity.set(record.id, activity)
                if (!confirmed && isBusy(activity)) {
                  confirmed = true
                  yield* repository.confirm(record.id)
                }
              }),
            ),
            // A feed that ends on its own must not pin the entry (the
            // next read re-subscribes instead of trusting a dead stream)
            // nor leak its child scope into the service scope.
            Effect.ensuring(
              Effect.gen(function* () {
                if (observed.get(record.id) !== observation) return
                observed.delete(record.id)
                yield* Scope.close(child, Exit.void)
              }),
            ),
            Effect.forkIn(child),
          )
        })

      const unobserve = (threadId: string): Effect.Effect<void> =>
        Effect.gen(function* () {
          liveActivity.delete(threadId)
          const observation = observed.get(threadId)
          if (observation === undefined) return
          observed.delete(threadId)
          yield* Scope.close(observation.scope, Exit.void)
        })

      /**
       * The activity snapshot for one read. A busy state whose driver (the
       * live linked terminal) is gone is stale: re-derive it from the
       * adapter's reconciliation check instead of reporting a dead turn.
       */
      const resolveActivity = (
        record: ThreadRecord,
        linked: string | undefined,
      ): Effect.Effect<AgentActivity> =>
        Effect.gen(function* () {
          if (record.providerSessionId === undefined) return "idle"
          const live = liveActivity.get(record.id) ?? "unknown"
          if (!isBusy(live) || linked !== undefined) return live
          const adapter = adapterFor(record)
          const providerSessionId = record.providerSessionId
          const checked =
            adapter === undefined
              ? ("unknown" as const)
              : yield* adapter
                  .checkSession({ providerSessionId })
                  .pipe(Effect.orElseSucceed(() => "unknown" as const))
          liveActivity.set(record.id, checked)
          return checked
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
        ...(linkedTerminalId !== undefined ? { linkedTerminalId } : {}),
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
          .list(record.projectId)
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
          // on the first read, so status flows again with no poller.
          if (linked !== undefined) yield* ensureObserved(record)
          const activity = yield* resolveActivity(record, linked?.id)
          return toThread(record, linked?.id, activity)
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
                  prepared.identity.pipe(Effect.mapError(mapAgentError(record))),
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

      const openTerminal: Threads["Service"]["openTerminal"] = (id) =>
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
          // Idempotent: one live linked TUI terminal per thread, returned
          // as-is (two TUIs into one conversation is the documented
          // concurrency hazard).
          const linked = yield* linkedFor(record)
          if (linked !== undefined) {
            yield* ensureObserved(record)
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
          opening.add(id)
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
          ).pipe(Effect.ensuring(Effect.sync(() => opening.delete(id))))
        })

      const del: Threads["Service"]["delete"] = (id) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          const linked = yield* linkedFor(record)
          if (linked !== undefined) {
            // Confirmed-kill rules live in Terminals.delete; a terminal that
            // vanished since the lookup is already the goal state.
            yield* terminals
              .delete(linked.id)
              .pipe(Effect.catchTag("TerminalNotFound", () => Effect.void))
          }
          const adapter = adapterFor(record)
          if (adapter !== undefined && record.providerSessionId !== undefined) {
            yield* adapter.releaseSession({
              providerSessionId: record.providerSessionId,
              providerMetadata: record.providerMetadata,
            })
          }
          yield* unobserve(id)
          yield* repository.delete(id)
        })

      return {
        list: (listOptions) =>
          Effect.gen(function* () {
            const records = yield* repository.list(listOptions?.projectId)
            const wanted = records.filter((record) =>
              listOptions?.archived === true
                ? record.archivedAt !== undefined
                : record.archivedAt === undefined,
            )
            if (wanted.length === 0) return []
            // One reconciled inventory pass for the whole page.
            const linked = new Map<string, Terminal>()
            for (const terminal of yield* terminals.list(listOptions?.projectId)) {
              // Newest first, first write wins — the same pick linkedFor makes.
              if (
                terminal.threadId !== undefined &&
                terminal.status === "live" &&
                !linked.has(terminal.threadId)
              ) {
                linked.set(terminal.threadId, terminal)
              }
            }
            return yield* Effect.forEach(wanted, (record) =>
              assemble(record, linked.get(record.id)),
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
            return toThread(record, undefined, "idle")
          }),
        update: (id, patch) =>
          // An empty patch changes nothing — including updatedAt (the same
          // rule the other repositories apply).
          patch.name === undefined
            ? repository.require(id).pipe(Effect.flatMap(assembleAlone))
            : repository
                .require(id)
                .pipe(
                  Effect.andThen(repository.rename(id, patch.name)),
                  Effect.flatMap(assembleAlone),
                ),
        archive: (id) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            const linked = yield* linkedFor(record)
            // Busy covers needs_input too: a turn parked on an approval is
            // still mid-turn.
            if (isBusy(yield* resolveActivity(record, linked?.id))) {
              return yield* Effect.fail(new ThreadBusy({ threadId: id }))
            }
            if (record.archivedAt !== undefined) return yield* assemble(record, linked)
            const updated = yield* repository.setArchived(id, true)
            return yield* assemble(updated, linked)
          }),
        unarchive: (id) =>
          Effect.gen(function* () {
            const record = yield* repository.require(id)
            if (record.archivedAt === undefined) return yield* assembleAlone(record)
            const updated = yield* repository.setArchived(id, false)
            return yield* assembleAlone(updated)
          }),
        delete: del,
        openTerminal,
      }
    }),
  )

export const layer = layerWith({})
