import { Schema } from "effect"
import { OpenApi } from "effect/unstable/httpapi"
import { Api, ResourceChangedEvent } from "./contract.ts"

// The OpenAPI document derived from the contract. This module is pure — no
// server, no side effects — so generation works anywhere the contract compiles.

/**
 * Hoist single-fragment `allOf` wrappers into their parent schema, e.g.
 * `{ type: "string", allOf: [{ minLength: 1 }] }` becomes
 * `{ type: "string", minLength: 1 }` — identical JSON Schema semantics.
 *
 * Effect renders every schema check as an `allOf` fragment, and the pinned
 * Swift OpenAPI generator turns any `allOf`-wrapped property into a nested
 * single-value wrapper type (`NamePayload(value1:)`) instead of a plain
 * `String`. Hoisting is applied only when nothing collides, so a future
 * genuinely-composite `allOf` passes through untouched.
 */
const hoistSingleAllOf = (node: unknown): unknown => {
  if (Array.isArray(node)) return node.map(hoistSingleAllOf)
  if (typeof node !== "object" || node === null) return node
  const schema = Object.fromEntries(
    Object.entries(node).map(([key, value]) => [key, hoistSingleAllOf(value)]),
  )
  const allOf = schema["allOf"]
  if (
    Array.isArray(allOf) &&
    allOf.length === 1 &&
    typeof allOf[0] === "object" &&
    allOf[0] !== null &&
    !Array.isArray(allOf[0]) &&
    Object.keys(allOf[0]).every((key) => !(key in schema) || key === "allOf")
  ) {
    const { allOf: _, ...rest } = schema
    return { ...rest, ...(allOf[0] as Record<string, unknown>) }
  }
  return schema
}

/**
 * Emit the SSE payload schema as an ordinary component. Data-mode `StreamSse`
 * documents its payload as the JSON-encoded string that crosses the wire
 * inside each `data:` line (`ResourceChangedEventJsonEncoding`), which the
 * pinned Swift generator can only render as a `String` typealias — leaving
 * Swift clients no generated type to decode that JSON into. The added plain
 * component fills that gap. Both guards below fail generation loudly rather
 * than emit a silently broken document: a nested named schema would carry a
 * dangling `#/$defs/` ref, and a same-named component from the contract
 * would be clobbered.
 */
const withSsePayloadComponents = (document: ReturnType<typeof OpenApi.fromApi>) => {
  const { definitions } = Schema.toJsonSchemaDocument(ResourceChangedEvent)
  const names = Object.keys(definitions)
  if (names.length !== 1) {
    throw new Error(
      `the SSE payload schema must stay flat (no nested named schemas); got components ${names.join(", ")}`,
    )
  }
  if ("ResourceChangedEvent" in document.components.schemas) {
    throw new Error(
      "the document already has a ResourceChangedEvent component; refusing to overwrite it",
    )
  }
  return {
    ...document,
    components: {
      ...document.components,
      schemas: {
        ...document.components.schemas,
        ResourceChangedEvent: definitions["ResourceChangedEvent"],
      },
    },
  } as ReturnType<typeof OpenApi.fromApi>
}

/**
 * Replace the generated `/events` 200 with the shape that actually crosses
 * the wire. The generator documents StreamSse's decoded frame envelope
 * ({id, event, data}, all required), but the server emits data-only frames
 * (`data: <json>`) plus `: connected` / `: heartbeat` comment lines — a
 * strict decoder generated from the envelope would reject every real frame.
 * The replacement declares the per-frame data payload (the JSON-encoded
 * ResourceChangedEvent string) and states the full framing protocol in the
 * response description; openapi.test.ts pins this documented shape to the
 * emitted bytes. The guard fails generation loudly if the endpoint moves.
 */
const withDataOnlySseResponse = (document: ReturnType<typeof OpenApi.fromApi>) => {
  const paths = document.paths as Record<string, { get?: { responses?: Record<string, unknown> } }>
  const operation = paths["/api/v1/events"]?.get
  if (operation?.responses?.["200"] === undefined) {
    throw new Error("no GET /api/v1/events 200 response to replace; did the endpoint move?")
  }
  const response = {
    description:
      "A data-only SSE byte stream. Each event is a single `data:` line whose payload is a " +
      "JSON-encoded ResourceChangedEvent (see that component schema); frames never carry `id:` " +
      "or `event:` lines. Comment lines are protocol control, ignored by conforming SSE " +
      "parsers: the stream opens with `: connected` (the resync handshake) and a `: heartbeat` " +
      "follows roughly every 25 seconds. Example:\n\n" +
      "```\n" +
      ": connected\n\n" +
      ": heartbeat\n\n" +
      'data: {"resource":"thread","id":"0198aaf1","change":"updated"}\n' +
      "```",
    content: {
      "text/event-stream": {
        schema: { $ref: "#/components/schemas/ResourceChangedEventJsonEncoding" },
      },
    },
  }
  return {
    ...document,
    paths: {
      ...paths,
      "/api/v1/events": {
        ...paths["/api/v1/events"],
        get: { ...operation, responses: { ...operation.responses, "200": response } },
      },
    },
  } as unknown as ReturnType<typeof OpenApi.fromApi>
}

export const openApiDocument = hoistSingleAllOf(
  withDataOnlySseResponse(withSsePayloadComponents(OpenApi.fromApi(Api))),
) as ReturnType<typeof OpenApi.fromApi>

/**
 * The canonical serialized form of the document: 2-space indentation and a
 * trailing newline. `JSON.stringify` preserves the deterministic insertion
 * order `OpenApi.fromApi` produces, so identical source yields byte-identical
 * output. The artifact is generated output, so it sits in `.prettierignore` —
 * this serialization is the single formatting authority.
 */
export const openApiJson = JSON.stringify(openApiDocument, null, 2) + "\n"
