import { Effect } from "effect"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./contract.ts"
import { BuildInfo } from "../platform/buildInfo.ts"
import { Directories } from "../platform/directories.ts"
import { Projects } from "../projects/projects.ts"
import { attachTerminal } from "../terminals/terminalAttach.ts"
import { Terminals } from "../terminals/terminals.ts"
import { Threads } from "../threads/threads.ts"

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
    const threads = yield* Threads
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
      .handle("listThreads", ({ query }) =>
        threads.list({ projectId: query.projectId, archived: query.archived === "true" }),
      )
      .handle("createThread", ({ payload }) => threads.create(payload))
      .handle("getThread", ({ params }) => threads.get(params.threadId))
      .handle("updateThread", ({ params, payload }) => threads.update(params.threadId, payload))
      .handle("archiveThread", ({ params }) => threads.archive(params.threadId))
      .handle("unarchiveThread", ({ params }) => threads.unarchive(params.threadId))
      .handle("deleteThread", ({ params }) => threads.delete(params.threadId))
      .handleRaw("attachTerminal", attachTerminal({ terminals, bridgeScope }))
  }),
)
