import { Effect, Option } from "effect"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api, ProjectNotFound } from "./api.ts"
import { BuildInfo } from "./buildInfo.ts"
import { Directories } from "./directories.ts"
import { ProjectRepository } from "./projectRepository.ts"

/** Implements the /api/v1 contract. App construction only — no listener. */
export const V1Handlers = HttpApiBuilder.group(
  Api,
  "v1",
  Effect.fnUntraced(function* (handlers) {
    const build = yield* BuildInfo
    const projects = yield* ProjectRepository
    const directories = yield* Directories

    const requireProject = <A>(projectId: string, found: Option.Option<A>) =>
      Option.match(found, {
        onNone: () => Effect.fail(new ProjectNotFound({ projectId })),
        onSome: Effect.succeed,
      })

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
      .handle("getProject", ({ params }) =>
        projects
          .get(params.projectId)
          .pipe(Effect.flatMap((found) => requireProject(params.projectId, found))),
      )
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
          return yield* requireProject(params.projectId, updated)
        }),
      )
      .handle("deleteProject", ({ params }) =>
        projects
          .delete(params.projectId)
          .pipe(
            Effect.flatMap((deleted) =>
              deleted
                ? Effect.void
                : Effect.fail(new ProjectNotFound({ projectId: params.projectId })),
            ),
          ),
      )
      .handle("checkDirectory", ({ query }) => directories.check(query.path))
  }),
)
