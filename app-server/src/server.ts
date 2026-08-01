import { BunHttpServer } from "@effect/platform-bun"
import { Layer } from "effect"
import { HttpRouter } from "effect/unstable/http"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./api.ts"
import { V1Handlers } from "./handlers.ts"

/** All HTTP routes, independent of any listener. Requires BuildInfo. */
export const routes = HttpApiBuilder.layer(Api).pipe(Layer.provide(V1Handlers))

/**
 * The full server: routes on a loopback TCP listener. The layer's scope owns
 * the listener, so closing the scope (e.g. on SIGINT/SIGTERM interruption)
 * stops the server and releases the port.
 */
export const layer = (options: { readonly port: number }) =>
  HttpRouter.serve(routes).pipe(
    Layer.provideMerge(BunHttpServer.layer({ port: options.port, hostname: "127.0.0.1" })),
  )
