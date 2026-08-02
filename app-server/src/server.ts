import { BunHttpServer } from "@effect/platform-bun"
import { Layer } from "effect"
import { HttpMiddleware, HttpRouter } from "effect/unstable/http"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./api.ts"
import { V1Handlers } from "./handlers.ts"
import * as LocalTrust from "./localTrust.ts"

/**
 * All HTTP routes with the local-trust guard applied, independent of any
 * listener. Requires the handler services (BuildInfo, ProjectRepository,
 * Directories).
 */
export const routes = Layer.mergeAll(
  HttpApiBuilder.layer(Api).pipe(Layer.provide(V1Handlers)),
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
    Layer.provideMerge(BunHttpServer.layer({ port: options.port, hostname: "127.0.0.1" })),
  )
