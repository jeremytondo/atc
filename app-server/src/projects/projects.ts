import { Context, Effect, Layer, Option } from "effect"
import { ProjectNotFound } from "../api/contract.ts"
import type {
  CreateProjectRequest,
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
  UpdateProjectRequest,
  ZmxUnavailable,
} from "../api/contract.ts"
import { Events } from "../events/events.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "./projectRepository.ts"
import type { Project } from "./projectRepository.ts"
import { Terminals } from "../terminals/terminals.ts"
import { TerminalRepository } from "../terminals/terminalRepository.ts"
import { Threads } from "../threads/threads.ts"
import { ThreadRepository } from "../threads/threadRepository.ts"

// The Projects domain service: the rules above the repository —
// canonicalize-on-create/update (identity is the symlink-resolved canonical
// path, validated before anything is stored) and the delete cascade to every
// owned thread and terminal. Handlers delegate here so every surface gets
// the same rules.

export class Projects extends Context.Service<
  Projects,
  {
    readonly list: () => Effect.Effect<ReadonlyArray<Project>>
    /** Validate and canonicalize the directory, then create the record. */
    readonly create: (
      input: typeof CreateProjectRequest.Type,
    ) => Effect.Effect<Project, DirectoryUnavailable | DirectoryCheckTimedOut>
    readonly get: (id: string) => Effect.Effect<Project, ProjectNotFound>
    /** Patch the provided fields, canonicalizing a new directory first. */
    readonly update: (
      id: string,
      patch: typeof UpdateProjectRequest.Type,
    ) => Effect.Effect<Project, ProjectNotFound | DirectoryUnavailable | DirectoryCheckTimedOut>
    /** Delete the project, cascading to every owned thread and terminal. */
    readonly delete: (id: string) => Effect.Effect<void, ProjectNotFound | ZmxUnavailable>
  }
>()("app-server/Projects") {}

export const layer = Layer.effect(Projects)(
  Effect.gen(function* () {
    const repository = yield* ProjectRepository
    const directories = yield* Directories
    const terminals = yield* Terminals
    const terminalRepository = yield* TerminalRepository
    const threads = yield* Threads
    const threadRepository = yield* ThreadRepository
    const events = yield* Events

    return {
      list: () => repository.list(),
      create: (input) =>
        Effect.gen(function* () {
          const canonical = yield* directories.canonicalize(input.defaultWorkingDirectory)
          const created = yield* repository.create({
            name: input.name,
            defaultWorkingDirectory: canonical,
          })
          yield* events.publish({ resource: "project", id: created.id, change: "created" })
          return created
        }),
      get: (id) => repository.require(id),
      update: (id, patch) =>
        Effect.gen(function* () {
          const canonical =
            patch.defaultWorkingDirectory !== undefined
              ? yield* directories.canonicalize(patch.defaultWorkingDirectory)
              : undefined
          const updated = yield* repository.update(id, {
            name: patch.name,
            defaultWorkingDirectory: canonical,
          })
          return yield* Option.match(updated, {
            onNone: () => Effect.fail(new ProjectNotFound({ projectId: id })),
            onSome: (project) =>
              // An empty patch changed nothing (the repository short-circuits
              // it), so it publishes nothing.
              patch.name === undefined && canonical === undefined
                ? Effect.succeed(project)
                : events
                    .publish({ resource: "project", id, change: "updated" })
                    .pipe(Effect.as(project)),
          })
        }),
      delete: (id) =>
        Effect.gen(function* () {
          // Sequential, best-effort cascade: threads first (a thread delete
          // kills its live linked terminal and releases provider state; its
          // ended linked tombstones survive unlinked), then every remaining
          // terminal — `starting` rows and `ended` tombstones included. A
          // ZmxUnavailable mid-cascade surfaces to the caller with children
          // already deleted staying deleted; retrying the project delete
          // finishes the job. Not transactional — child deletes do external
          // I/O (zmx kill, hook-file removal) — so a child created
          // concurrently mid-cascade falls back to the migration's FK
          // RESTRICT (a defect, not a modeled failure).
          const ownedThreads = yield* threadRepository.list(id)
          yield* Effect.forEach(
            ownedThreads,
            (thread) =>
              // A child that vanished since its listing is the goal state.
              threads.delete(thread.id).pipe(Effect.catchTag("ThreadNotFound", () => Effect.void)),
            { discard: true },
          )
          const ownedTerminals = yield* terminalRepository.list(id)
          yield* Effect.forEach(
            ownedTerminals,
            (terminal) =>
              terminals
                .delete(terminal.id)
                .pipe(Effect.catchTag("TerminalNotFound", () => Effect.void)),
            { discard: true },
          )
          const deleted = yield* repository.delete(id)
          if (!deleted) {
            return yield* Effect.fail(new ProjectNotFound({ projectId: id }))
          }
          yield* events.publish({ resource: "project", id, change: "deleted" })
        }),
    }
  }),
)
