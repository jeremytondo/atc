import { Schema } from "effect"
import { HttpApi, HttpApiEndpoint, HttpApiGroup, OpenApi } from "effect/unstable/httpapi"

// The public HTTP contract. The server implementation (handlers.ts), the
// checked-in OpenAPI document (openapi.ts), and typed clients all derive from
// this module. Authoring conventions (pinned operation ids, schema
// identifiers, descriptions) live in AGENTS.md "OpenAPI Contract".

/** Default TCP port of a locally running App Server. */
export const DEFAULT_PORT = 7332

export const HealthResponse = Schema.Struct({
  status: Schema.Literal("ok"),
}).annotate({
  identifier: "HealthResponse",
  description: "Result of the liveness probe.",
})

export const VersionResponse = Schema.Struct({
  version: Schema.String.annotate({ description: "App Server release version." }),
  apiVersion: Schema.Literal("v1"),
  commit: Schema.String.annotate({
    description: 'Commit the executable was built from ("dev" when running from source).',
  }),
  builtAt: Schema.String.annotate({
    description: 'Build timestamp of the executable ("dev" when running from source).',
  }),
}).annotate({
  identifier: "VersionResponse",
  description: "Build and contract version information for the running server.",
})

export class V1 extends HttpApiGroup.make("v1")
  .add(
    HttpApiEndpoint.get("health", "/health", { success: HealthResponse })
      .annotate(OpenApi.Identifier, "getHealth")
      .annotate(OpenApi.Description, "Liveness probe: confirms the server is accepting requests."),
    HttpApiEndpoint.get("version", "/version", { success: VersionResponse })
      .annotate(OpenApi.Identifier, "getVersion")
      .annotate(OpenApi.Description, "Report the server's build metadata and API version."),
  )
  // .prefix only applies to endpoints added above it — add new endpoints
  // before this line so they stay under /api/v1.
  .prefix("/api/v1") {}

// The OpenAPI version is the contract version, not the build version, so this
// module stays client-safe with no dependency on server internals.
export class Api extends HttpApi.make("atc")
  .add(V1)
  .annotateMerge(
    OpenApi.annotations({
      title: "ATC App Server API",
      version: "v1",
      servers: [
        {
          url: "http://127.0.0.1:{port}",
          description: "Local App Server",
          variables: { port: { default: `${DEFAULT_PORT}` } },
        },
      ],
    }),
  ) {}
