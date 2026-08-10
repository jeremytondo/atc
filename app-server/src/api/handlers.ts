import { Effect, Schema, Stream } from "effect"
import { HttpServerResponse } from "effect/unstable/http"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api, ResourceChangedEvent } from "./contract.ts"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import { Events, HEARTBEAT } from "../events/events.ts"
import { BuildInfo } from "../platform/buildInfo.ts"
import { Directories } from "../platform/directories.ts"
import { Projects } from "../projects/projects.ts"
import { attachTerminal } from "../terminals/terminalAttach.ts"
import { Terminals } from "../terminals/terminals.ts"
import { Threads } from "../threads/threads.ts"

/** One SSE frame per contract event: the exact wire shape StreamSse encodes. */
const encodeEventJson = Schema.encodeEffect(Schema.fromJsonString(ResourceChangedEvent))

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
    const agents = yield* AgentRegistry
    const events = yield* Events
    // Attach bridges outlive their originating requests (Bun aborts the
    // request on protocol switch); they live in the handler layer's scope,
    // so server shutdown still reaps every bridge and its PTY client.
    const bridgeScope = yield* Effect.scope

    return (
      handlers
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
        .handle("updateProject", ({ params, payload }) =>
          projects.update(params.projectId, payload),
        )
        .handle("deleteProject", ({ params }) => projects.delete(params.projectId))
        .handle("checkDirectory", ({ query }) => directories.check(query.path))
        .handle("listDirectory", ({ query }) => directories.list(query.path))
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
        .handle("pinThread", ({ params }) => threads.pin(params.threadId))
        .handle("unpinThread", ({ params }) => threads.unpin(params.threadId))
        .handle("deleteThread", ({ params }) => threads.delete(params.threadId))
        .handle("openThreadTerminal", ({ params }) => threads.openTerminal(params.threadId))
        .handle("listAgents", () => agents.list())
        .handle("getAgent", ({ params }) => agents.get(params.agentId))
        // handleRaw instead of the typed handler for one reason: this stream
        // emits nothing until the first change, and Bun's fetch (every Bun
        // client, the contract TS client included) does not resolve a
        // chunked response until its first body byte — so the opening SSE
        // comment is what lets subscribers observe "stream open" and start
        // their resync. Conforming SSE parsers ignore comment lines, and
        // events are encoded through the contract schema, so the wire shape
        // is exactly what the typed StreamSse pipeline produces.
        .handleRaw("subscribeEvents", () =>
          Effect.gen(function* () {
            // Reconcile (the "a client showed up" refresh) BEFORE
            // registering: a queue registered first would sit in the
            // subscriber set until overflow if the fallible reconcile
            // failed — or the request died during it — before the stream's
            // cleanup was armed. Registration still precedes the first
            // response byte, so a client that resyncs the moment it sees
            // the comment observes the reconciled state and misses nothing.
            yield* terminals.list()
            const feed = yield* events.subscribe()
            // Heartbeat ticks become SSE comments: ignored by conforming
            // parsers, but they keep idle-timeout-prone transports alive and
            // surface dead sockets to the server via the failing write.
            const encoded = feed.pipe(
              Stream.mapEffect((item) =>
                item === HEARTBEAT
                  ? Effect.succeed(": heartbeat\n\n")
                  : Effect.map(encodeEventJson(item), (json) => `data: ${json}\n\n`),
              ),
            )
            return HttpServerResponse.stream(
              Stream.concat(Stream.succeed(": connected\n\n"), encoded).pipe(
                Stream.encodeText,
                Stream.orDie,
              ),
              { contentType: "text/event-stream" },
            )
          }),
        )
        .handleRaw("attachTerminal", attachTerminal({ terminals, bridgeScope }))
    )
  }),
)
