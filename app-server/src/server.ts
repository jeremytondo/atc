import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { HttpMiddleware, HttpRouter, HttpServer, HttpServerResponse } from "effect/unstable/http"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./api/contract.ts"
import * as AdminUi from "./adminUi/adminUi.ts"
import * as AgentRegistry from "./agents/agentRegistry.ts"
import * as AuthToken from "./platform/authToken.ts"
import * as ClaudeAdapter from "./agents/claudeAdapter.ts"
import * as ClaudeHooks from "./agents/claudeHooks.ts"
import * as CodexAdapter from "./agents/codexAdapter.ts"
import * as CodexServer from "./agents/codexServer.ts"
import * as Directories from "./platform/directories.ts"
import * as Events from "./events/events.ts"
import { V1Handlers } from "./api/handlers.ts"
import * as LocalTrust from "./api/localTrust.ts"
import * as Logging from "./platform/logging.ts"
import { openApiJson } from "./api/openapi.ts"
import * as Persistence from "./platform/persistence.ts"
import * as Tailscale from "./platform/tailscale.ts"
import * as ProjectRepository from "./projects/projectRepository.ts"
import * as Projects from "./projects/projects.ts"
import * as TerminalRepository from "./terminals/terminalRepository.ts"
import * as Terminals from "./terminals/terminals.ts"
import * as ThreadRepository from "./threads/threadRepository.ts"
import * as ThreadRuntime from "./threads/threadRuntime.ts"
import * as Threads from "./threads/threads.ts"
import * as TranscriptRepository from "./threads/transcriptRepository.ts"
import * as Zmx from "./terminals/zmxAdapter.ts"

// OpenAPI discovery (ATC-131): the contract-derived document at a stable
// path. Served from the canonical serialization (openapi.ts) so the response
// is byte-identical to the checked-in openapi.json — deliberately not
// HttpApiBuilder's openapiPath option, which would emit the pre-transform
// document and drift from the artifact.
const openApiRoute = HttpRouter.add(
  "GET",
  "/openapi.json",
  HttpServerResponse.text(openApiJson, { contentType: "application/json" }),
)

/**
 * All HTTP routes with the trust guard applied, independent of any listener.
 * Requires the handler services (BuildInfo, Projects, Directories, Terminals,
 * Threads, ClaudeHooks) plus AuthToken for the guard's bearer check. The
 * Claude hook webhook is an internal route (claudeHooks.ts), deliberately
 * outside the contract. The admin UI rides a GET wildcard, which the router
 * only consults after every static route misses.
 */
export const routes = Layer.mergeAll(
  HttpApiBuilder.layer(Api).pipe(Layer.provide(V1Handlers)),
  openApiRoute,
  ClaudeHooks.route,
  AdminUi.route,
  LocalTrust.middleware,
)

// Layer finalizers run dependents-first, so this must sit ABOVE the serve
// layer: on shutdown it ends every event subscriber stream before the
// listener's bounded graceful stop starts waiting on open responses. Left to
// the Events layer's own finalizer (a dependency, released after the stop),
// an open SSE response would hold the stop for the full timeout and die on
// the noisy forced-interrupt path instead.
//
// The "shutting down" line is registered LAST so it runs FIRST (finalizers
// are LIFO): the file log otherwise just stops on SIGINT/SIGTERM, leaving a
// supervising app unable to tell a clean exit from a kill.
const drainEventsBeforeStop = Layer.effectDiscard(
  Effect.gen(function* () {
    const events = yield* Events.Events
    yield* Effect.addFinalizer(events.close)
    yield* Effect.addFinalizer(() => Effect.logInfo("shutting down"))
  }),
)

/** IPv6 literals need brackets to form a valid URL host (`[::1]:7331`). */
export const hostForUrl = (host: string): string => (host.includes(":") ? `[${host}]` : host)

// The resolved listen address, logged once the listener is up: with `bind`
// configurable, the file log must record what the server actually bound.
const logListenAddress = Layer.effectDiscard(
  Effect.gen(function* () {
    const server = yield* HttpServer.HttpServer
    const address = server.address
    if (address._tag === "TcpAddress") {
      yield* Effect.logInfo(`listening on http://${hostForUrl(address.hostname)}:${address.port}`)
    }
  }),
)

/**
 * The full server: guarded routes on a TCP listener bound to `hostname`
 * (loopback by default; the `bind` setting opens it — see localTrust.ts for
 * the trust rule), each request wrapped in a tracer span (the correlation id
 * source for logs). The layer's scope owns the listener, so closing the
 * scope (e.g. on SIGINT/SIGTERM interruption) stops the server and releases
 * the port.
 */
export const layer = (options: { readonly port: number; readonly hostname?: string }) =>
  Layer.mergeAll(drainEventsBeforeStop, logListenAddress).pipe(
    Layer.provideMerge(HttpRouter.serve(routes, { middleware: HttpMiddleware.tracer })),
    Layer.provideMerge(
      BunHttpServer.layer({
        port: options.port,
        hostname: options.hostname ?? "127.0.0.1",
        // Bun never finishes a graceful stop once the server has initiated
        // a WebSocket close (the connection stays in its bookkeeping), so a
        // long grace period turns every shutdown after a terminal attach
        // into a hang. Requests are local and short; two seconds is plenty.
        gracefulShutdownTimeout: "2 seconds",
        // Bun's default idleTimeout (10 s) counts a request that has not yet
        // written response bytes as idle, which severs openThreadTerminal
        // mid-materialization — a cold provider launch legitimately runs up
        // to the 30 s identity timeout. Sixty seconds clears that with
        // margin; SSE heartbeats and attach pings keep long streams under it.
        idleTimeout: 60,
      }),
    ),
  )

/**
 * The closed production assembly: the full server over the real domain
 * layers. What remains open are the process-level services — AppConfig,
 * Subprocess, and the Bun runtime — supplied by the entrypoint.
 */
export const production = (options: { readonly port: number; readonly hostname?: string }) =>
  layer(options).pipe(
    Layer.provide(AuthToken.layer),
    // Keep the service in the route construction context: both the typed
    // server-info handler and the optional trust allowlist read one instance.
    Layer.provideMerge(Tailscale.layer),
    // Projects sits above Threads (its delete cascades through them), which
    // sits above the ThreadRuntime (activity ledger, transcript, turns) and
    // Terminals — each provide feeds everything composed so far.
    Layer.provide(Projects.layer),
    Layer.provide(Threads.layer),
    Layer.provide([Terminals.layer, ThreadRuntime.layer]),
    Layer.provide(AgentRegistry.layer),
    // Below Terminals (the deepest publisher) so one memoized Events instance
    // serves every domain service, the handlers, and the shutdown drain.
    Layer.provide(Events.layer),
    Layer.provide([CodexAdapter.layer, ClaudeAdapter.layer]),
    Layer.provide(CodexServer.layer),
    Layer.provide([
      ProjectRepository.layer,
      TerminalRepository.layer,
      ThreadRepository.layer,
      TranscriptRepository.layer,
      Directories.layer,
      Zmx.layer,
      ClaudeHooks.layer,
    ]),
    Layer.provide(Persistence.layer),
    Layer.provide(Logging.layer),
  )
