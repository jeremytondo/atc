import { JsonSchema, Schema } from "effect"
import { OpenApi } from "effect/unstable/httpapi"
import {
  Api,
  ResourceChangedEvent,
  ThreadEvent,
  ThreadRequestAnswer,
  ThreadTranscript,
} from "./contract.ts"

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
 * The schemas documented as components even though no endpoint (yet) names
 * them directly: the SSE payloads, which data-mode `StreamSse` documents only
 * as the JSON-encoded string crossing the wire inside each `data:` line
 * (`ResourceChangedEventJsonEncoding`) — a `String` typealias in the pinned
 * Swift generator, leaving clients no type to decode into — and the Thread
 * runtime vocabulary (ATC-193), whose types clients build against ahead of
 * the routes that carry them.
 */
const VOCABULARY_SCHEMAS = [
  ResourceChangedEvent,
  ThreadEvent,
  ThreadTranscript,
  ThreadRequestAnswer,
]

/**
 * Add `VOCABULARY_SCHEMAS` — each with every named schema it references —
 * as ordinary components. Refs are rewritten to `#/components/schemas/…`
 * through the same conversion the API document itself uses, so a schema
 * that is ALSO reachable from an endpoint lands byte-identical; only a
 * same-named component with different content is a collision, and that
 * fails generation loudly rather than emitting a silently broken document.
 */
const withVocabularyComponents = (document: ReturnType<typeof OpenApi.fromApi>) => {
  const schemas: Record<string, unknown> = { ...document.components.schemas }
  for (const schema of VOCABULARY_SCHEMAS) {
    const draft = Schema.toJsonSchemaDocument(schema)
    const converted = JsonSchema.toMultiDocumentOpenApi3_1({
      dialect: "draft-2020-12",
      schemas: [draft.schema],
      definitions: draft.definitions,
    })
    for (const [name, definition] of Object.entries(converted.definitions)) {
      const existing = schemas[name]
      if (existing !== undefined && JSON.stringify(existing) !== JSON.stringify(definition)) {
        throw new Error(
          `the document already has a different ${name} component; refusing to overwrite it`,
        )
      }
      schemas[name] = definition
    }
  }
  return {
    ...document,
    components: { ...document.components, schemas },
  } as ReturnType<typeof OpenApi.fromApi>
}

/**
 * Stamp an OpenAPI `discriminator` on every component that is a `oneOf` of
 * `$ref`s whose targets all pin the same single-valued literal property
 * (`type`, `kind`, …). Effect renders tagged unions as plain `oneOf`; the
 * pinned Swift generator turns a bare `oneOf` into positional `case1/case2`
 * enums, and with a discriminator into one enum case per named variant.
 * Applied to components only — inline unions never occur in this contract,
 * and a schema the rule does not match is left exactly as generated.
 */
const withDiscriminators = (document: ReturnType<typeof OpenApi.fromApi>) => {
  const schemas = document.components.schemas as Record<string, Record<string, unknown>>
  const literalOf = (ref: string, key: string): string | undefined => {
    const target = schemas[ref.replace("#/components/schemas/", "")]
    const property = (target?.["properties"] as Record<string, unknown> | undefined)?.[key] as
      { const?: unknown; enum?: ReadonlyArray<unknown> } | undefined
    if (property === undefined) return undefined
    if (typeof property.const === "string") return property.const
    if (Array.isArray(property.enum) && property.enum.length === 1) {
      return typeof property.enum[0] === "string" ? property.enum[0] : undefined
    }
    return undefined
  }
  const stamped = Object.fromEntries(
    Object.entries(schemas).map(([name, schema]) => {
      const oneOf = schema["oneOf"]
      if (
        !Array.isArray(oneOf) ||
        oneOf.length === 0 ||
        !oneOf.every((member) => typeof (member as { $ref?: unknown }).$ref === "string")
      ) {
        return [name, schema]
      }
      const refs = (oneOf as ReadonlyArray<{ $ref: string }>).map((member) => member.$ref)
      for (const key of ["type", "kind"]) {
        const mapping = Object.fromEntries(
          refs.flatMap((ref) => {
            const value = literalOf(ref, key)
            return value === undefined ? [] : [[value, ref]]
          }),
        )
        if (Object.keys(mapping).length === refs.length) {
          return [name, { ...schema, discriminator: { propertyName: key, mapping } }]
        }
      }
      return [name, schema]
    }),
  )
  return {
    ...document,
    components: { ...document.components, schemas: stamped },
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
  withDiscriminators(withDataOnlySseResponse(withVocabularyComponents(OpenApi.fromApi(Api)))),
) as ReturnType<typeof OpenApi.fromApi>

/**
 * The canonical serialized form of the document: 2-space indentation and a
 * trailing newline. `JSON.stringify` preserves the deterministic insertion
 * order `OpenApi.fromApi` produces, so identical source yields byte-identical
 * output. The artifact is generated output, so it sits in `.prettierignore` —
 * this serialization is the single formatting authority.
 */
export const openApiJson = JSON.stringify(openApiDocument, null, 2) + "\n"
