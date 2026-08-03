import { Effect, Option } from "effect"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api, ProjectHasTerminals, ProjectNotFound } from "./api.ts"
import { BuildInfo } from "./buildInfo.ts"
import { Directories } from "./directories.ts"
import { ProjectRepository } from "./projectRepository.ts"
import { attachTerminal } from "./terminalAttach.ts"
import { Terminals } from "./terminals.ts"

/** Implements the /api/v1 contract. App construction only — no listener. */
export const V1Handlers = HttpApiBuilder.group(
  Api,
  "v1",
  Effect.fnUntraced(function* (handlers) {
    const build = yield* BuildInfo
    const projects = yield* ProjectRepository
    const directories = yield* Directories
    const terminals = yield* Terminals
    // Attach bridges outlive their originating requests (Bun aborts the
    // request on protocol switch); they live in the handler layer's scope,
    // so server shutdown still reaps every bridge and its PTY client.
    const bridgeScope = yield* Effect.scope

    return handlers
      .handle("health", () => Effect.succeed({ status: "ok" } as const))
      .handle("version", () =>
        Effect.succeed({
          version: build.version,
          apiVersion: "v1",
          commit: build.commit,
          builtAt: build.builtAt,
        } as const),
      )
      .handle("listProjects", () => projects.list())
      .handle("createProject", ({ payload }) =>
        Effect.gen(function* () {
          const canonical = yield* directories.canonicalize(payload.defaultWorkingDirectory)
          return yield* projects.create({
            name: payload.name,
            defaultWorkingDirectory: canonical,
          })
        }),
      )
      .handle("getProject", ({ params }) => projects.require(params.projectId))
      .handle("updateProject", ({ params, payload }) =>
        Effect.gen(function* () {
          const canonical =
            payload.defaultWorkingDirectory !== undefined
              ? yield* directories.canonicalize(payload.defaultWorkingDirectory)
              : undefined
          const updated = yield* projects.update(params.projectId, {
            name: payload.name,
            defaultWorkingDirectory: canonical,
          })
          return yield* Option.match(updated, {
            onNone: () => Effect.fail(new ProjectNotFound({ projectId: params.projectId })),
            onSome: Effect.succeed,
          })
        }),
      )
      .handle("deleteProject", ({ params }) =>
        Effect.gen(function* () {
          // RESTRICT while terminals exist (tombstones included): the
          // simplest correct rule for a single-user app. Not transactional
          // with the delete below — a concurrently created terminal falls
          // back to the migration's FK RESTRICT (a defect, not a 409).
          const terminalCount = yield* terminals.countForProject(params.projectId)
          if (terminalCount > 0) {
            return yield* Effect.fail(
              new ProjectHasTerminals({ projectId: params.projectId, terminalCount }),
            )
          }
          const deleted = yield* projects.delete(params.projectId)
          if (!deleted) {
            return yield* Effect.fail(new ProjectNotFound({ projectId: params.projectId }))
          }
        }),
      )
      .handle("checkDirectory", ({ query }) => directories.check(query.path))
      .handle("listTerminals", ({ query }) => terminals.list(query.projectId))
      .handle("createTerminal", ({ payload }) => terminals.create(payload))
      .handle("getTerminal", ({ params }) => terminals.get(params.terminalId))
      .handle("updateTerminal", ({ params, payload }) =>
        terminals.update(params.terminalId, payload),
      )
      .handle("deleteTerminal", ({ params }) => terminals.delete(params.terminalId))
      .handleRaw("attachTerminal", attachTerminal({ terminals, bridgeScope }))
  }),
)
