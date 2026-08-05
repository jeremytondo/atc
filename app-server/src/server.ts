import { BunHttpServer } from "@effect/platform-bun"
import { Layer } from "effect"
import { HttpMiddleware, HttpRouter, HttpServerResponse } from "effect/unstable/http"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./api.ts"
import * as ClaudeHooks from "./claudeHooks.ts"
import { V1Handlers } from "./handlers.ts"
import * as LocalTrust from "./localTrust.ts"
import { openApiJson } from "./openapi.ts"

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
 * All HTTP routes with the local-trust guard applied, independent of any
 * listener. Requires the handler services (BuildInfo, Projects, Directories,
 * Terminals, ClaudeHooks). The Claude hook webhook is an internal route
 * (claudeHooks.ts), deliberately outside the contract.
 */
export const routes = Layer.mergeAll(
  HttpApiBuilder.layer(Api).pipe(Layer.provide(V1Handlers)),
  openApiRoute,
  ClaudeHooks.route,
  LocalTrust.middleware,
)

/**
 * The full server: guarded routes on a loopback TCP listener, each request
 * wrapped in a tracer span (the correlation id source for logs). The layer's
 * scope owns the listener, so closing the scope (e.g. on SIGINT/SIGTERM
 * interruption) stops the server and releases the port.
 */
export const layer = (options: { readonly port: number }) =>
  HttpRouter.serve(routes, { middleware: HttpMiddleware.tracer }).pipe(
    Layer.provideMerge(
      BunHttpServer.layer({
        port: options.port,
        hostname: "127.0.0.1",
        // Bun never finishes a graceful stop once the server has initiated
        // a WebSocket close (the connection stays in its bookkeeping), so a
        // long grace period turns every shutdown after a terminal attach
        // into a hang. Requests are local and short; two seconds is plenty.
        gracefulShutdownTimeout: "2 seconds",
      }),
    ),
  )
