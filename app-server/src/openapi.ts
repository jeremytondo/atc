import { OpenApi } from "effect/unstable/httpapi"
import { Api } from "./api.ts"

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

export const openApiDocument = hoistSingleAllOf(OpenApi.fromApi(Api)) as ReturnType<
  typeof OpenApi.fromApi
>

/**
 * The canonical serialized form of the document: 2-space indentation and a
 * trailing newline. `JSON.stringify` preserves the deterministic insertion
 * order `OpenApi.fromApi` produces, so identical source yields byte-identical
 * output. The artifact is generated output, so it sits in `.prettierignore` —
 * this serialization is the single formatting authority.
 */
export const openApiJson = JSON.stringify(openApiDocument, null, 2) + "\n"
