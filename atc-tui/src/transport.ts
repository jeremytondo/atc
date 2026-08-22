import { BunHttpClient } from "@effect/platform-bun"
import { Effect } from "effect"
import * as Config from "./config.ts"

// HTTP and SSE share one transport decision. Local mode uses ordinary fetch;
// remote mode keeps the URL (and therefore Host header) pointed at the App
// Server's loopback origin while Bun connects through SSH's private Unix
// socket. No TCP listener is exposed on the laptop.

export const fetchOptions = (config: Config.ClientConfig["Service"]): BunFetchRequestInit =>
  config.connection.type === "remote" ? { unix: config.connection.socketPath } : {}

export const fetch = (
  config: Config.ClientConfig["Service"],
  input: Parameters<typeof globalThis.fetch>[0],
  init?: BunFetchRequestInit,
): Promise<Response> => globalThis.fetch(input, { ...fetchOptions(config), ...init })

export const provideFetchOptions = <Success, Error, Requirements>(
  effect: Effect.Effect<Success, Error, Requirements>,
  config: Config.ClientConfig["Service"],
): Effect.Effect<Success, Error, Requirements> =>
  effect.pipe(Effect.provideService(BunHttpClient.RequestInit, fetchOptions(config)))
