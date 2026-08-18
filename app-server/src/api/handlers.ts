import { Effect, Schema, Stream } from "effect"
import { HttpServerResponse } from "effect/unstable/http"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api, ResourceChangedEvent, ThreadEvent } from "./contract.ts"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import { Events, HEARTBEAT } from "../events/events.ts"
import { BuildInfo } from "../platform/buildInfo.ts"
import { Directories } from "../platform/directories.ts"
import * as Tailscale from "../platform/tailscale.ts"
import { Projects } from "../projects/projects.ts"
import { attachTerminal } from "../terminals/terminalAttach.ts"
import { Terminals } from "../terminals/terminals.ts"
import { ThreadRuntime } from "../threads/threadRuntime.ts"
import { Threads } from "../threads/threads.ts"

/** One SSE frame per contract event: the exact wire shape StreamSse encodes. */
const encodeEventJson = Schema.encodeEffect(Schema.fromJsonString(ResourceChangedEvent))
const encodeThreadEventJson = Schema.encodeEffect(Schema.fromJsonString(ThreadEvent))

/**
 * The data-only SSE response both stream endpoints share: an opening
 * `: connected` comment (see subscribeEvents below for why), then `data:`
 * frames, with heartbeat ticks as comments. `frames` already carries the
 * heartbeat marker interleaved (both feeds ride the Events heartbeat).
 */
const sseResponse = (frames: Stream.Stream<string, unknown>) =>
  HttpServerResponse.stream(
    Stream.concat(Stream.succeed(": connected\n\n"), frames).pipe(Stream.encodeText, Stream.orDie),
    { contentType: "text/event-stream" },
  )

/**
 * Implements the /api/v1 contract. App construction only — no listener, and
 * pure delegation: every rule lives in the domain services.
 */
export const V1Handlers = HttpApiBuilder.group(
  Api,
  "v1",
  Effect.fnUntraced(function* (handlers) {
    const build = yield* BuildInfo
    const tailscale = yield* Tailscale.Tailscale
    const projects = yield* Projects
    const directories = yield* Directories
    const terminals = yield* Terminals
    const threads = yield* Threads
    const runtime = yield* ThreadRuntime
    const agents = yield* AgentRegistry
    const events = yield* Events
    // Attach bridges outlive their originating requests (Bun aborts the
    // request on protocol switch); they live in the handler layer's scope,
    // so server shutdown still reaps every bridge and its PTY client.
    const bridgeScope = yield* Effect.scope

    return (
      handlers
        .handle("health", () => Effect.succeed({ status: "ok" } as const))
        .handle("serverInfo", () =>
          Effect.map(tailscale.status, (status) => ({ tailscale: status })),
        )
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
        .handle("listTerminals", ({ query }) =>
          terminals.list({ projectId: query.projectId, threadId: query.threadId }),
        )
        .handle("createTerminal", ({ payload }) => terminals.create(payload))
        .handle("getTerminal", ({ params }) => terminals.get(params.terminalId))
        .handle("updateTerminal", ({ params, payload }) =>
          terminals.update(params.terminalId, payload),
        )
        .handle("deleteTerminal", ({ params }) => terminals.delete(params.terminalId))
        .handle("listThreads", ({ query }) =>
          threads.list({
            projectId: query.projectId,
            archived:
              query.archived === "true" ? "archived" : query.archived === "all" ? "all" : "active",
          }),
        )
        .handle("createThread", ({ payload }) => threads.create(payload))
        .handle("getThread", ({ params }) => threads.get(params.threadId))
        .handle("updateThread", ({ params, payload }) => threads.update(params.threadId, payload))
        .handle("archiveThread", ({ params }) => threads.archive(params.threadId))
        .handle("unarchiveThread", ({ params }) => threads.unarchive(params.threadId))
        .handle("pinThread", ({ params }) => threads.pin(params.threadId))
        .handle("unpinThread", ({ params }) => threads.unpin(params.threadId))
        .handle("markThreadViewed", ({ params }) => threads.markViewed(params.threadId))
        .handle("deleteThread", ({ params }) => threads.delete(params.threadId))
        .handle("openThreadTerminal", ({ params }) => threads.openTerminal(params.threadId))
        .handle("closeThreadTerminal", ({ params }) => threads.closeTerminal(params.threadId))
        .handle("promptThread", ({ params, payload }) =>
          runtime.prompt(params.threadId, payload.prompt),
        )
        .handle("getThreadTranscript", ({ params, query }) =>
          runtime.transcript(params.threadId, { before: query.before, limit: query.limit }),
        )
        .handle("interruptThread", ({ params }) => runtime.interrupt(params.threadId))
        .handle("listThreadRequests", ({ params }) => runtime.listRequests(params.threadId))
        .handle("answerThreadRequest", ({ params, payload }) =>
          runtime.answerRequest(params.threadId, params.requestId, payload),
        )
        .handle("listThreadQueue", ({ params }) => runtime.listQueue(params.threadId))
        .handle("deleteQueuedPrompt", ({ params }) =>
          runtime.deleteQueued(params.threadId, params.promptId),
        )
        .handle("listAgents", () => agents.list())
        .handle("getAgent", ({ params }) => agents.get(params.agentId))
        .handle("listAgentModels", ({ params }) => agents.models(params.agentId))
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
            // The reconcile-before-register ordering lives in subscribe();
            // the handler only names the refresh a new client deserves.
            const feed = yield* events.subscribe({ reconcile: terminals.list() })
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
            return sseResponse(encoded)
          }),
        )
        // The per-thread stream (ATC-193), framed exactly like /events. Its
        // heartbeat and its shutdown ordering come from the Events service's
        // heartbeat-only feed: it ends when Events closes
        // (drainEventsBeforeStop), which ends this response before the
        // listener's bounded stop — the same guarantee /events has.
        .handleRaw("subscribeThreadEvents", ({ params, query }) =>
          Effect.gen(function* () {
            const feed = yield* runtime.subscribe(params.threadId, query.after)
            const heartbeats = yield* events.heartbeats()
            const encoded = Stream.merge(
              feed.pipe(
                Stream.mapEffect((event) =>
                  Effect.map(encodeThreadEventJson(event), (json) => `data: ${json}\n\n`),
                ),
              ),
              heartbeats.pipe(Stream.map(() => ": heartbeat\n\n")),
              { haltStrategy: "either" },
            )
            return sseResponse(encoded)
          }),
        )
        .handleRaw("attachTerminal", attachTerminal({ terminals, bridgeScope }))
    )
  }),
)
