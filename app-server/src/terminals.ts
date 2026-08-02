import { Context, Effect, Layer, Option } from "effect"
import type { Scope } from "effect"
import {
  ProjectNotFound,
  Terminal as TerminalSchema,
  TerminalLaunchFailed,
  TerminalNotFound,
  ZmxUnavailable,
} from "./api.ts"
import type { CreateTerminalRequest, DirectoryCheckTimedOut, DirectoryUnavailable } from "./api.ts"
import { Directories } from "./directories.ts"
import { ProjectRepository } from "./projectRepository.ts"
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
//     before the API can serve any request) and on list/read/attach — no
//     background watcher.
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
  id: record.id,
  projectId: record.projectId,
  ...(record.name !== undefined ? { name: record.name } : {}),
  ...(record.command !== undefined ? { command: record.command } : {}),
  initialWorkingDirectory: record.initialWorkingDirectory,
  status: record.status === "ended" ? "ended" : "live",
  createdAt: record.createdAt,
  updatedAt: record.updatedAt,
  ...(record.endedAt !== undefined ? { endedAt: record.endedAt } : {}),
})

export class Terminals extends Context.Service<
  Terminals,
  {
    /** Reconciled listing; an unavailable inventory returns stored state. */
    readonly list: (projectId?: string) => Effect.Effect<ReadonlyArray<Terminal>>
    /** Reconciled read. */
    readonly get: (id: string) => Effect.Effect<Terminal, TerminalNotFound>
    /** Create the record and start the zmx session (two-phase). */
    readonly create: (
      input: typeof CreateTerminalRequest.Type,
    ) => Effect.Effect<
      Terminal,
      | ProjectNotFound
      | DirectoryUnavailable
      | DirectoryCheckTimedOut
      | ZmxUnavailable
      | TerminalLaunchFailed
    >
    readonly rename: (id: string, name: string) => Effect.Effect<Terminal, TerminalNotFound>
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
    /** Every record (tombstones included) — the project-deletion guard. */
    readonly countForProject: (projectId: string) => Effect.Effect<number>
  }
>()("app-server/Terminals") {}

export const layer = Layer.effect(Terminals)(
  Effect.gen(function* () {
    const repository = yield* TerminalRepository
    const adapter = yield* TerminalAdapter
    const projects = yield* ProjectRepository
    const directories = yield* Directories

    const presentSessionNames = adapter
      .listSessions()
      .pipe(Effect.map((sessions) => new Set(sessions.map((s) => s.name))))

    /**
     * Tombstone every live row whose session a complete inventory omits.
     * Rows are read first — see the header's authoritative-absence bullet.
     */
    const reconcile = Effect.gen(function* () {
      const records = yield* repository.list()
      const present = yield* presentSessionNames
      for (const record of records) {
        if (record.status === "live" && !present.has(sessionNameForTerminalId(record.id))) {
          yield* repository.markEnded(record.id)
        }
      }
    })

    const reconcileBestEffort = reconcile.pipe(Effect.catchTag("ZmxUnavailable", () => Effect.void))

    // Startup pass: additionally resolves crash-mid-create rows and cleans
    // orphans — safe only here, while no create can be in flight. Runs
    // during layer build, so no request can observe pre-reconcile state.
    // Skipped wholesale when the inventory is unavailable.
    const reconcileAtStartup = Effect.gen(function* () {
      const records = yield* repository.list()
      const sessions = yield* adapter.listSessions()
      const present = new Set(sessions.map((s) => s.name))
      const reachable = new Set(sessions.filter((s) => s.reachable).map((s) => s.name))
      const claimed = new Set<string>()
      for (const record of records) {
        const sessionName = sessionNameForTerminalId(record.id)
        switch (record.status) {
          case "live":
            if (!present.has(sessionName)) yield* repository.markEnded(record.id)
            else claimed.add(sessionName)
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
      for (const session of sessions) {
        if (!claimed.has(session.name)) {
          yield* adapter
            .killSession(session.name)
            .pipe(
              Effect.catch((error) =>
                Effect.logWarning("orphan zmx session not cleaned").pipe(
                  Effect.annotateLogs({ session: session.name, reason: error.message }),
                ),
              ),
            )
        }
      }
    })

    yield* reconcileAtStartup.pipe(
      Effect.catchTag("ZmxUnavailable", (error) =>
        Effect.logWarning("startup terminal reconciliation skipped").pipe(
          Effect.annotateLogs({ reason: error.reason }),
        ),
      ),
    )

    const requireRecord = (id: string) =>
      repository.get(id).pipe(
        Effect.flatMap((found) =>
          Option.match(found, {
            // starting rows belong to their in-flight create.
            onNone: () => Effect.fail(new TerminalNotFound({ terminalId: id })),
            onSome: (record) =>
              record.status === "starting"
                ? Effect.fail(new TerminalNotFound({ terminalId: id }))
                : Effect.succeed(record),
          }),
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
          .createSession({ name: sessionName, cwd: canonical, command: input.command })
          .pipe(
            Effect.catchTag("SessionOperationFailed", (error) =>
              Effect.fail(new TerminalLaunchFailed({ reason: error.reason })),
            ),
            Effect.andThen(Effect.suspend(() => repository.markLive(record.id))),
            Effect.onExit((exit) => (exit._tag === "Failure" ? cleanup : Effect.void)),
          )
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
      })

    return {
      list: (projectId) =>
        reconcileBestEffort.pipe(
          Effect.andThen(repository.list(projectId)),
          Effect.map((records) =>
            records.filter((record) => record.status !== "starting").map(toTerminal),
          ),
        ),
      get: (id) =>
        reconcileBestEffort.pipe(Effect.andThen(requireRecord(id)), Effect.map(toTerminal)),
      create,
      rename: (id, name) =>
        requireRecord(id).pipe(Effect.andThen(repository.rename(id, name)), Effect.map(toTerminal)),
      delete: del,
      attach: (id, size) => adapter.attachSession(sessionNameForTerminalId(id), size),
      confirmEnded: (id) =>
        Effect.gen(function* () {
          const present = yield* presentSessionNames
          if (present.has(sessionNameForTerminalId(id))) return false
          yield* repository.markEnded(id)
          return true
        }),
      countForProject: (projectId) => repository.countForProject(projectId),
    }
  }),
)
