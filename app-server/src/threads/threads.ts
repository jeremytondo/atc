import { Context, Effect, Layer } from "effect"
import type { AgentActivity } from "../agents/agentAdapter.ts"
import { Thread as ThreadSchema, ThreadBusy } from "../api/contract.ts"
import type {
  CreateThreadRequest,
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
  ProjectNotFound,
  ThreadNotFound,
  ZmxUnavailable,
} from "../api/contract.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "../projects/projectRepository.ts"
import { Terminals } from "../terminals/terminals.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"

export type Thread = typeof ThreadSchema.Type

// The Threads domain service (ATC-124): Threads are the primary unit of
// work — durable ATC identity separate from provider identity. Invariants:
//
//   - The boundary rule: this module contains zero provider branching and
//     imports nothing provider-specific — only the AgentAdapter seam's
//     vocabulary. Every provider difference is translated inside adapters.
//   - `create` is local-only: the durable row, no provider call. The
//     provider session materializes on the thread's first interaction
//     (ATC-141 wires the first caller); until then providerSessionId is
//     absent and stays out of every public schema.
//   - workingDirectory is validated, canonicalized, and immutable.
//   - activityState is in-memory evidence, never persisted: a thread with
//     no provider session is idle by construction; with one, the live
//     feed's last word, `unknown` when there is no evidence.
//   - delete kills the live linked TUI terminal first (the terminals
//     confirmed-kill rules), then removes the row; ended linked terminals
//     stay as unlinked tombstones (FK SET NULL), and provider-owned
//     conversation history is never touched. Known gap: a linked terminal
//     still `starting` is invisible here (it belongs to its in-flight
//     create), so ATC-141's openTerminal re-checks the thread still exists
//     before marking its terminal live.

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
    /** Kill the live linked terminal if present, then remove the record. */
    readonly delete: (id: string) => Effect.Effect<void, ThreadNotFound | ZmxUnavailable>
  }
>()("app-server/Threads") {}

export const layer = Layer.effect(Threads)(
  Effect.gen(function* () {
    const repository = yield* ThreadRepository
    const projects = yield* ProjectRepository
    const directories = yield* Directories
    const terminals = yield* Terminals

    // No provider session ⇒ nothing can be running; with one, this PR has
    // no evidence source yet — ATC-141 wires the live feed in.
    const activityFor = (record: ThreadRecord): AgentActivity =>
      record.providerSessionId === undefined ? "idle" : "unknown"

    const toThread = (record: ThreadRecord, linkedTerminalId: string | undefined): Thread => ({
      id: record.id,
      projectId: record.projectId,
      // Permissive read (see the repository header); a foreign slug fails
      // response encoding for that row, never the domain logic.
      agentId: record.agentId as Thread["agentId"],
      ...(record.name !== undefined ? { name: record.name } : {}),
      workingDirectory: record.workingDirectory,
      activityState: activityFor(record),
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
    const linkedFor = (record: ThreadRecord): Effect.Effect<string | undefined> =>
      terminals
        .list(record.projectId)
        .pipe(
          Effect.map(
            (list) => list.find((t) => t.threadId === record.id && t.status === "live")?.id,
          ),
        )

    const withLinked = (record: ThreadRecord): Effect.Effect<Thread> =>
      linkedFor(record).pipe(Effect.map((linked) => toThread(record, linked)))

    return {
      list: (options) =>
        Effect.gen(function* () {
          const records = yield* repository.list(options?.projectId)
          const wanted = records.filter((record) =>
            options?.archived === true
              ? record.archivedAt !== undefined
              : record.archivedAt === undefined,
          )
          if (wanted.length === 0) return []
          const linked = new Map<string, string>()
          for (const terminal of yield* terminals.list(options?.projectId)) {
            // Newest first, first write wins — the same pick linkedFor makes.
            if (
              terminal.threadId !== undefined &&
              terminal.status === "live" &&
              !linked.has(terminal.threadId)
            ) {
              linked.set(terminal.threadId, terminal.id)
            }
          }
          return wanted.map((record) => toThread(record, linked.get(record.id)))
        }),
      get: (id) => repository.require(id).pipe(Effect.flatMap(withLinked)),
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
          return toThread(record, undefined)
        }),
      update: (id, patch) =>
        // An empty patch changes nothing — including updatedAt (the same
        // rule the other repositories apply).
        patch.name === undefined
          ? repository.require(id).pipe(Effect.flatMap(withLinked))
          : repository
              .require(id)
              .pipe(Effect.andThen(repository.rename(id, patch.name)), Effect.flatMap(withLinked)),
      archive: (id) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          if (activityFor(record) === "working") {
            return yield* Effect.fail(new ThreadBusy({ threadId: id }))
          }
          if (record.archivedAt !== undefined) return yield* withLinked(record)
          return yield* repository.setArchived(id, true).pipe(Effect.flatMap(withLinked))
        }),
      unarchive: (id) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          if (record.archivedAt === undefined) return yield* withLinked(record)
          return yield* repository.setArchived(id, false).pipe(Effect.flatMap(withLinked))
        }),
      delete: (id) =>
        Effect.gen(function* () {
          const record = yield* repository.require(id)
          const linked = yield* linkedFor(record)
          if (linked !== undefined) {
            // Confirmed-kill rules live in Terminals.delete; a terminal that
            // vanished since the lookup is already the goal state.
            yield* terminals
              .delete(linked)
              .pipe(Effect.catchTag("TerminalNotFound", () => Effect.void))
          }
          yield* repository.delete(id)
        }),
    }
  }),
)
