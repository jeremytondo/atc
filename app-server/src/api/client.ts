import type { Effect } from "effect"
import { HttpClient, HttpClientRequest } from "effect/unstable/http"
import { HttpApiClient } from "effect/unstable/httpapi"
import { Api } from "./contract.ts"

// The typed App Server client, derived from the same contract module the
// server routes with (contract.ts) — no generated artifact, so it can never go
// stale. This module depends only on the contract, never on server internals,
// so the CLI and future packages can consume it directly.

/**
 * Build a client for the App Server at `baseUrl`. Requires an `HttpClient`
 * (e.g. `BunHttpClient.layer`) in the environment.
 *
 * `token` is the bearer token non-loopback servers require (the documented
 * `bearerAuth` scheme); loopback servers ignore it.
 */
export const make = (options: {
  readonly baseUrl: string | URL
  readonly token?: string | undefined
}) =>
  HttpApiClient.make(Api, {
    baseUrl: options.baseUrl,
    transformClient:
      options.token === undefined
        ? undefined
        : HttpClient.mapRequest(HttpClientRequest.bearerToken(options.token)),
  })

export type AppServerClient = Effect.Success<ReturnType<typeof make>>
