import { OpenApi } from "effect/unstable/httpapi"
import { Api } from "./api.ts"

// The OpenAPI document derived from the contract. This module is pure — no
// server, no side effects — so generation works anywhere the contract compiles.

export const openApiDocument = OpenApi.fromApi(Api)

/**
 * The canonical serialized form of the document: 2-space indentation and a
 * trailing newline. `JSON.stringify` preserves the deterministic insertion
 * order `OpenApi.fromApi` produces, so identical source yields byte-identical
 * output. The artifact is generated output, so it sits in `.prettierignore` —
 * this serialization is the single formatting authority.
 */
export const openApiJson = JSON.stringify(openApiDocument, null, 2) + "\n"
