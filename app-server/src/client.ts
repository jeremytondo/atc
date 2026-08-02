import type { Effect } from "effect"
import { HttpApiClient } from "effect/unstable/httpapi"
import { Api } from "./api.ts"

// The typed App Server client, derived from the same contract module the
// server routes with (api.ts) — no generated artifact, so it can never go
// stale. This module depends only on the contract, never on server internals,
// so the CLI and future packages can consume it directly.

/**
 * Build a client for the App Server at `baseUrl`. Requires an `HttpClient`
 * (e.g. `BunHttpClient.layer`) in the environment.
 */
export const make = (options: { readonly baseUrl: string | URL }) =>
  HttpApiClient.make(Api, { baseUrl: options.baseUrl })

export type AppServerClient = Effect.Success<ReturnType<typeof make>>
