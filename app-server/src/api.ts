import { Schema } from "effect"
import { HttpApi, HttpApiEndpoint, HttpApiGroup, OpenApi } from "effect/unstable/httpapi"
import { buildInfo } from "./buildInfo.ts"

// The public HTTP contract. The server implementation (handlers.ts), the
// OpenAPI document, and future typed clients all derive from this module.

export const HealthResponse = Schema.Struct({
  status: Schema.Literal("ok"),
})

export const VersionResponse = Schema.Struct({
  version: Schema.String,
  apiVersion: Schema.Literal("v1"),
  commit: Schema.String,
  builtAt: Schema.String,
})

export class V1 extends HttpApiGroup.make("v1")
  .add(
    HttpApiEndpoint.get("health", "/health", { success: HealthResponse }),
    HttpApiEndpoint.get("version", "/version", { success: VersionResponse }),
  )
  // .prefix only applies to endpoints added above it — add new endpoints
  // before this line so they stay under /api/v1.
  .prefix("/api/v1") {}

export class Api extends HttpApi.make("atc")
  .add(V1)
  .annotateMerge(
    OpenApi.annotations({ title: "ATC App Server API", version: buildInfo.version }),
  ) {}
