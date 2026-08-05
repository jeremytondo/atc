import { Context, Effect, Layer, Option } from "effect"
import { ProjectHasTerminals, ProjectNotFound } from "../api/contract.ts"
import type {
  CreateProjectRequest,
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
  UpdateProjectRequest,
} from "../api/contract.ts"
import { Directories } from "../platform/directories.ts"
import { ProjectRepository } from "./projectRepository.ts"
import type { Project } from "./projectRepository.ts"
import { Terminals } from "../terminals/terminals.ts"

// The Projects domain service: the rules above the repository —
// canonicalize-on-create/update (identity is the symlink-resolved canonical
// path, validated before anything is stored) and the
// delete-restricted-while-terminals-exist guard. Handlers delegate here so
// every surface gets the same rules.

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
    /** Delete the record; restricted while the project owns terminals. */
    readonly delete: (id: string) => Effect.Effect<void, ProjectNotFound | ProjectHasTerminals>
  }
>()("app-server/Projects") {}

export const layer = Layer.effect(Projects)(
  Effect.gen(function* () {
    const repository = yield* ProjectRepository
    const directories = yield* Directories
    const terminals = yield* Terminals

    return {
      list: () => repository.list(),
      create: (input) =>
        Effect.gen(function* () {
          const canonical = yield* directories.canonicalize(input.defaultWorkingDirectory)
          return yield* repository.create({
            name: input.name,
            defaultWorkingDirectory: canonical,
          })
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
            onSome: Effect.succeed,
          })
        }),
      delete: (id) =>
        Effect.gen(function* () {
          // RESTRICT while terminals exist (tombstones included): the
          // simplest correct rule for a single-user app. Not transactional
          // with the delete below — a concurrently created terminal falls
          // back to the migration's FK RESTRICT (a defect, not a 409).
          const terminalCount = yield* terminals.countForProject(id)
          if (terminalCount > 0) {
            return yield* Effect.fail(new ProjectHasTerminals({ projectId: id, terminalCount }))
          }
          const deleted = yield* repository.delete(id)
          if (!deleted) {
            return yield* Effect.fail(new ProjectNotFound({ projectId: id }))
          }
        }),
    }
  }),
)
