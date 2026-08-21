import { Context, Effect, Layer, Schema } from "effect"

// Client-only connection configuration. It deliberately does not reuse the
// server's AppConfig: this executable can run on a different machine and owns
// only an endpoint plus its optional bearer credential.

export const DEFAULT_ENDPOINT = "http://127.0.0.1:7331"

export class ConfigError extends Schema.TaggedErrorClass<ConfigError>()("ConfigError", {
  source: Schema.String,
  message: Schema.String,
}) {}

export class ClientConfig extends Context.Service<
  ClientConfig,
  {
    readonly endpoint: URL
    readonly token?: string | undefined
  }
>()("atc-tui/ClientConfig") {}

const nonEmpty = (value: string | undefined): string | undefined =>
  value !== undefined && value.trim() !== "" ? value.trim() : undefined

export const resolve = (
  endpointOverride: string | undefined,
  env: Readonly<Record<string, string | undefined>>,
): ClientConfig["Service"] | ConfigError => {
  const source = nonEmpty(endpointOverride) ?? nonEmpty(env["ATC_ENDPOINT"]) ?? DEFAULT_ENDPOINT
  if (!URL.canParse(source)) {
    return new ConfigError({
      source: "--endpoint / ATC_ENDPOINT",
      message: `invalid URL "${source}"`,
    })
  }

  const endpoint = new URL(source)
  if (endpoint.protocol !== "http:" && endpoint.protocol !== "https:") {
    return new ConfigError({
      source: "--endpoint / ATC_ENDPOINT",
      message: `endpoint must use http or https, got "${endpoint.protocol}"`,
    })
  }
  if (endpoint.username !== "" || endpoint.password !== "") {
    return new ConfigError({
      source: "--endpoint / ATC_ENDPOINT",
      message: "endpoint credentials are not allowed; use ATC_TOKEN",
    })
  }

  const token = nonEmpty(env["ATC_TOKEN"])
  return {
    endpoint,
    ...(token === undefined ? {} : { token }),
  }
}

export const load = (endpointOverride: string | undefined) => {
  const resolved = resolve(endpointOverride, process.env)
  return resolved instanceof ConfigError ? Effect.fail(resolved) : Effect.succeed(resolved)
}

export const layer = (endpointOverride: string | undefined) =>
  Layer.effect(ClientConfig)(load(endpointOverride))
