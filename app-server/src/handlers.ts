import { Effect } from "effect"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./api.ts"
import { BuildInfo } from "./buildInfo.ts"
import { Directories } from "./directories.ts"
import { Projects } from "./projects.ts"
import { attachTerminal } from "./terminalAttach.ts"
import { Terminals } from "./terminals.ts"

/**
 * Implements the /api/v1 contract. App construction only — no listener, and
 * pure delegation: every rule lives in the domain services.
 */
export const V1Handlers = HttpApiBuilder.group(
  Api,
  "v1",
  Effect.fnUntraced(function* (handlers) {
    const build = yield* BuildInfo
    const projects = yield* Projects
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
      .handle("createProject", ({ payload }) => projects.create(payload))
      .handle("getProject", ({ params }) => projects.get(params.projectId))
      .handle("updateProject", ({ params, payload }) => projects.update(params.projectId, payload))
      .handle("deleteProject", ({ params }) => projects.delete(params.projectId))
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
