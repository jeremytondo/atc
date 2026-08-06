import { Context, Effect, Layer, Option } from "effect"
import type { Scope } from "effect"
import {
  ProjectNotFound,
  Terminal as TerminalSchema,
  TerminalLaunchFailed,
  TerminalNotFound,
  ZmxUnavailable,
} from "../api/contract.ts"
import type {
  CreateTerminalRequest,
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
} from "../api/contract.ts"
import { Events } from "../events/events.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "../projects/projectRepository.ts"
import { sessionNameForTerminalId, TerminalAdapter } from "./terminalAdapter.ts"
import type {
  SessionConnection,
  SessionNotFound,
  SessionOperationFailed,
  SessionSize,
} from "./terminalAdapter.ts"
import { TerminalRepository } from "./terminalRepository.ts"
import type { TerminalRecord } from "./terminalRepository.ts"

export type Terminal = typeof TerminalSchema.Type

// The Terminals domain service (ATC-130): orchestrates the repository, the
// TerminalAdapter, and directory validation under the ATC-122 reconciliation
// invariants:
//
//   - Authoritative absence: only a successful, complete inventory taken
//     *after* the row snapshot may mark a terminal ended (a row that was
//     live before the inventory and absent from it is genuinely dead; the
//     reverse order would tombstone a create that completed in between).
//     Inventory unavailable ⇒ stored state untouched; operations requiring
//     certainty fail with the retryable ZmxUnavailable.
//   - Demand-driven: reconciliation runs at startup (during layer build,
//     before the API can serve any request) and on list/read — attach
//     reconciles through the bridge's pre-flight `get`, so `attach` itself
//     is a bare pass-through. No background watcher.
//   - `starting` rows are internal and owned by their in-flight create (or
//     by startup recovery after a crash). Request-time reconciliation never
//     touches them, which is why the startup pass and the request-time pass
//     can never be one function: resolving `starting` rows is only safe
//     while no create can be in flight.
//   - Orphan cleanup: sessions in ATC's private socket dir that no stored
//     terminal claims are provably ours and killed (best-effort) at startup.

/**
 * Contract shape for a repository record. Callers have already excluded
 * `starting` (list filters, requireRecord rejects); the status here can
 * only be live or ended.
 */
const toTerminal = (record: TerminalRecord): Terminal => ({
  ...record,
  status: record.status === "ended" ? "ended" : "live",
})

export class Terminals extends Context.Service<
  Terminals,
  {
    /** Reconciled listing; an unavailable inventory returns stored state. */
    readonly list: (projectId?: string) => Effect.Effect<ReadonlyArray<Terminal>>
    /** Reconciled read. */
    readonly get: (id: string) => Effect.Effect<Terminal, TerminalNotFound>
    /**
     * Create the record and start the zmx session (two-phase). `threadId`
     * and `env` are internal extensions used by the Threads domain's
     * openTerminal — never accepted from the public contract.
     */
    readonly create: (
      input: typeof CreateTerminalRequest.Type & {
        readonly threadId?: string | undefined
        readonly env?: Record<string, string | undefined> | undefined
      },
    ) => Effect.Effect<
      Terminal,
      | ProjectNotFound
      | DirectoryUnavailable
      | DirectoryCheckTimedOut
      | ZmxUnavailable
      | TerminalLaunchFailed
    >
    /** Patch mutable fields; an empty patch changes nothing, updatedAt included. */
    readonly update: (
      id: string,
      patch: { readonly name?: string | undefined },
    ) => Effect.Effect<Terminal, TerminalNotFound>
    /** Kill if present → verify absence → remove the record. */
    readonly delete: (id: string) => Effect.Effect<void, TerminalNotFound | ZmxUnavailable>
    /** Open a live attach connection onto the terminal's session. */
    readonly attach: (
      id: string,
      size: SessionSize,
    ) => Effect.Effect<
      SessionConnection,
      ZmxUnavailable | SessionNotFound | SessionOperationFailed,
      Scope.Scope
    >
    /**
     * Authoritative-absence check for the attach bridge: true iff a
     * complete inventory omits the terminal's session, in which case the
     * tombstone is written. Unavailable inventories fail retryably.
     */
    readonly confirmEnded: (id: string) => Effect.Effect<boolean, ZmxUnavailable>
  }
>()("app-server/Terminals") {}

export const layer = Layer.effect(Terminals)(
  Effect.gen(function* () {
    const repository = yield* TerminalRepository
    const adapter = yield* TerminalAdapter
    const projects = yield* ProjectRepository
    const directories = yield* Directories
    const events = yield* Events

    const presentSessionNames = adapter
      .listSessions()
      .pipe(Effect.map((sessions) => new Set(sessions.map((s) => s.name))))

    /**
     * The one tombstoning step both reconciliation passes share: mark every
     * live row whose session a complete inventory omits as ended (one batch
     * statement) and return the records with the tombstones applied.
     */
    const tombstoneAbsent = (
      records: ReadonlyArray<TerminalRecord>,
      present: ReadonlySet<string>,
    ): Effect.Effect<ReadonlyArray<TerminalRecord>> =>
      Effect.gen(function* () {
        const deadRecords = records.filter(
          (r) => r.status === "live" && !present.has(sessionNameForTerminalId(r.id)),
        )
        if (deadRecords.length === 0) return records
        const dead = new Set(deadRecords.map((r) => r.id))
        const endedAt = yield* repository.markEnded([...dead])
        yield* Effect.forEach(deadRecords, (record) =>
          events.publish({ resource: "terminal", id: record.id, change: "updated" }),
        )
        // A dead TUI terminal also changes its thread's derived fields
        // (linkedTerminalId, activity re-derivation), so the thread
        // publishes alongside — the event names no thread otherwise.
        yield* Effect.forEach(new Set(deadRecords.flatMap((r) => r.threadId ?? [])), (id) =>
          events.publish({ resource: "thread", id, change: "updated" }),
        )
        return records.map((record) =>
          dead.has(record.id)
            ? { ...record, status: "ended" as const, endedAt, updatedAt: endedAt }
            : record,
        )
      })

    /**
     * Request-time pass: reconcile and return the post-reconcile records, so
     * list/get never re-query what was just read. Rows are read first — see
     * the header's authoritative-absence bullet. An unavailable inventory
     * leaves stored state untouched and returns it as-is.
     */
    const reconciledRecords: Effect.Effect<ReadonlyArray<TerminalRecord>> = Effect.gen(
      function* () {
        const records = yield* repository.list()
        const present = yield* presentSessionNames.pipe(
          Effect.catchTag("ZmxUnavailable", () => Effect.succeed(undefined)),
        )
        if (present === undefined) return records
        return yield* tombstoneAbsent(records, present)
      },
    )

    // Startup pass: additionally resolves crash-mid-create rows and cleans
    // orphans — safe only here, while no create can be in flight. Runs
    // during layer build, so no request can observe pre-reconcile state.
    // Skipped wholesale when the inventory is unavailable.
    const reconcileAtStartup = Effect.gen(function* () {
      const records = yield* repository.list()
      const sessions = yield* adapter.listSessions()
      const present = new Set(sessions.map((s) => s.name))
      const reachable = new Set(sessions.filter((s) => s.reachable).map((s) => s.name))
      yield* tombstoneAbsent(records, present)
      const claimed = new Set<string>()
      for (const record of records) {
        const sessionName = sessionNameForTerminalId(record.id)
        switch (record.status) {
          case "live":
            if (present.has(sessionName)) claimed.add(sessionName)
            break
          case "starting":
            // Crash mid-create: adopt the session if it made it — only a
            // *reachable* entry proves it did (ATC-122). An unreachable
            // match is neither alive nor absent: the row stays `starting`
            // (hidden, resolved at a later startup) and the session is
            // claimed so orphan cleanup does not kill it. Absent means the
            // failed launch leaves no record.
            if (reachable.has(sessionName)) {
              yield* repository.markLive(record.id)
              yield* events.publish({ resource: "terminal", id: record.id, change: "created" })
              claimed.add(sessionName)
            } else if (present.has(sessionName)) {
              claimed.add(sessionName)
              yield* Effect.logWarning("starting terminal left unresolved").pipe(
                Effect.annotateLogs({ terminalId: record.id, reason: "session unreachable" }),
              )
            } else {
              yield* repository.delete(record.id)
            }
            break
          case "ended":
            break
        }
      }
      // Everything in ATC's private dir is provably ours: sessions no live
      // record claims (foreign names included) are orphans. Best-effort —
      // a kill that cannot be verified is logged and retried next startup.
      // Kills are independent and mostly sleep in verification polling, so
      // a bounded batch keeps a crash's worth of orphans off the boot time.
      yield* Effect.forEach(
        sessions.filter((session) => !claimed.has(session.name)),
        (session) =>
          adapter
            .killSession(session.name)
            .pipe(
              Effect.catch((error) =>
                Effect.logWarning("orphan zmx session not cleaned").pipe(
                  Effect.annotateLogs({ session: session.name, reason: error.message }),
                ),
              ),
            ),
        { concurrency: 4, discard: true },
      )
    })

    yield* reconcileAtStartup.pipe(
      Effect.catchTag("ZmxUnavailable", (error) =>
        Effect.logWarning("startup terminal reconciliation skipped").pipe(
          Effect.annotateLogs({ reason: error.reason }),
        ),
      ),
    )

    // starting rows belong to their in-flight create, so they are invisible.
    const requireRecord = (id: string) =>
      repository.require(id).pipe(
        Effect.filterOrFail(
          (record) => record.status !== "starting",
          () => new TerminalNotFound({ terminalId: id }),
        ),
      )

    const create: Terminals["Service"]["create"] = (input) =>
      Effect.gen(function* () {
        const project = yield* projects.require(input.projectId)
        const canonical = yield* directories.canonicalize(
          input.workingDirectory ?? project.defaultWorkingDirectory,
        )
        // Two-phase: the starting row exists before the session so a crash
        // mid-create is resolvable either way at the next startup.
        const record = yield* repository.create({
          projectId: input.projectId,
          threadId: input.threadId,
          name: input.name,
          command: input.command,
          initialWorkingDirectory: canonical,
        })
        const sessionName = sessionNameForTerminalId(record.id)
        // On any non-success exit — typed failure, defect, or interruption
        // (a client abort interrupts the request fiber) — the launch leaves
        // no record and, best-effort, no session.
        const cleanup = adapter
          .killSession(sessionName)
          .pipe(Effect.ignore, Effect.andThen(repository.delete(record.id)))
        const live = yield* adapter
          .createSession({
            name: sessionName,
            cwd: canonical,
            command: input.command,
            env: input.env,
          })
          .pipe(
            Effect.catchTag("SessionOperationFailed", (error) =>
              Effect.fail(new TerminalLaunchFailed({ reason: error.reason })),
            ),
            Effect.andThen(repository.markLive(record.id)),
            Effect.onExit((exit) => (exit._tag === "Failure" ? cleanup : Effect.void)),
          )
        yield* events.publish({ resource: "terminal", id: live.id, change: "created" })
        // A live terminal created into a thread immediately changes that
        // thread's derived linkedTerminalId; openTerminal's own event lands
        // only after the identity wait, far too late for this transition.
        if (input.threadId !== undefined) {
          yield* events.publish({ resource: "thread", id: input.threadId, change: "updated" })
        }
        return toTerminal(live)
      })

    const del: Terminals["Service"]["delete"] = (id) =>
      Effect.gen(function* () {
        const record = yield* requireRecord(id)
        // Kill requiring certainty: a kill that cannot verify death is
        // reported retryably rather than deleting a record whose session
        // may still be alive.
        yield* adapter
          .killSession(sessionNameForTerminalId(record.id))
          .pipe(
            Effect.catchTag("SessionOperationFailed", (error) =>
              Effect.fail(new ZmxUnavailable({ reason: error.reason })),
            ),
          )
        yield* repository.delete(id)
        yield* events.publish({ resource: "terminal", id, change: "deleted" })
        // Deleting a thread's TUI terminal changes that thread's derived
        // linkedTerminalId (see tombstoneAbsent).
        if (record.threadId !== undefined) {
          yield* events.publish({ resource: "thread", id: record.threadId, change: "updated" })
        }
      })

    return {
      list: (projectId) =>
        reconciledRecords.pipe(
          Effect.map((records) =>
            records
              .filter(
                (record) =>
                  record.status !== "starting" &&
                  (projectId === undefined || record.projectId === projectId),
              )
              .map(toTerminal),
          ),
        ),
      get: (id) =>
        reconciledRecords.pipe(
          Effect.flatMap((records) => {
            const record = records.find((r) => r.id === id)
            return record === undefined || record.status === "starting"
              ? Effect.fail(new TerminalNotFound({ terminalId: id }))
              : Effect.succeed(toTerminal(record))
          }),
        ),
      create,
      update: (id, patch) =>
        // An empty patch changes nothing — including updatedAt (the same
        // rule ProjectRepository.update applies).
        patch.name === undefined
          ? requireRecord(id).pipe(Effect.map(toTerminal))
          : requireRecord(id).pipe(
              Effect.andThen(repository.rename(id, patch.name)),
              Effect.tap(() => events.publish({ resource: "terminal", id, change: "updated" })),
              Effect.map(toTerminal),
            ),
      delete: del,
      attach: (id, size) => adapter.attachSession(sessionNameForTerminalId(id), size),
      confirmEnded: (id) =>
        Effect.gen(function* () {
          const present = yield* presentSessionNames
          if (present.has(sessionNameForTerminalId(id))) return false
          // Publish only the actual live→ended transition: a repeat confirm
          // of an already-ended (or since-deleted) terminal changes nothing.
          const record = yield* repository.get(id)
          yield* repository.markEnded([id])
          if (Option.isSome(record) && record.value.status === "live") {
            yield* events.publish({ resource: "terminal", id, change: "updated" })
            if (record.value.threadId !== undefined) {
              yield* events.publish({
                resource: "thread",
                id: record.value.threadId,
                change: "updated",
              })
            }
          }
          return true
        }),
    }
  }),
)
