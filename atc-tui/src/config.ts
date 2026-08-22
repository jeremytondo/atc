import { Context, Effect, Layer, Schema } from "effect"
import * as os from "node:os"
import * as path from "node:path"

// The prototype uses the App Server as its control plane but attaches to the
// same machine's ATC-owned zmx namespace directly. Explicit zmx flags cover a
// server started under a different environment without expanding the HTTP API.

export const DEFAULT_ENDPOINT = "http://127.0.0.1:7331"
export const DEFAULT_ZMX_EXECUTABLE = "zmx"

export interface Overrides {
  readonly endpoint?: string | undefined
  readonly zmxExecutable?: string | undefined
  readonly zmxDir?: string | undefined
}

export class ConfigError extends Schema.TaggedErrorClass<ConfigError>()("ConfigError", {
  source: Schema.String,
  message: Schema.String,
}) {}

export class ClientConfig extends Context.Service<
  ClientConfig,
  {
    readonly endpoint: URL
    readonly token?: string | undefined
    readonly zmxExecutable: string
    readonly zmxDir: string
    readonly environment: Readonly<Record<string, string | undefined>>
  }
>()("atc-tui/ClientConfig") {}

const nonEmpty = (value: string | undefined): string | undefined =>
  value !== undefined && value.trim() !== "" ? value.trim() : undefined

export const defaultZmxDir = (
  env: Readonly<Record<string, string | undefined>>,
  fallbackHome = os.homedir(),
): string => {
  const stateHome = nonEmpty(env["XDG_STATE_HOME"])
  const home = nonEmpty(env["HOME"]) ?? fallbackHome
  return path.join(stateHome ?? path.join(home, ".local", "state"), "atc", "terminals")
}

export const resolve = (
  overrides: Overrides,
  env: Readonly<Record<string, string | undefined>>,
): ClientConfig["Service"] | ConfigError => {
  const endpointSource =
    nonEmpty(overrides.endpoint) ?? nonEmpty(env["ATC_ENDPOINT"]) ?? DEFAULT_ENDPOINT
  if (!URL.canParse(endpointSource)) {
    return new ConfigError({
      source: "--endpoint / ATC_ENDPOINT",
      message: `invalid URL "${endpointSource}"`,
    })
  }

  const endpoint = new URL(endpointSource)
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
  if (endpoint.pathname !== "/" || endpoint.search !== "" || endpoint.hash !== "") {
    return new ConfigError({
      source: "--endpoint / ATC_ENDPOINT",
      message: "endpoint must be an origin without a path, query, or fragment",
    })
  }

  const zmxExecutable =
    nonEmpty(overrides.zmxExecutable) ??
    nonEmpty(env["ATC_ZMX_EXECUTABLE"]) ??
    DEFAULT_ZMX_EXECUTABLE
  if (zmxExecutable.includes("/") && !path.isAbsolute(zmxExecutable)) {
    return new ConfigError({
      source: "--zmx-bin / ATC_ZMX_EXECUTABLE",
      message: `zmx executable must be a bare name or absolute path, got "${zmxExecutable}"`,
    })
  }

  const zmxDir = nonEmpty(overrides.zmxDir) ?? defaultZmxDir(env)
  if (!path.isAbsolute(zmxDir)) {
    return new ConfigError({
      source: "--zmx-dir",
      message: `zmx directory must be absolute, got "${zmxDir}"`,
    })
  }

  const token = nonEmpty(env["ATC_TOKEN"])
  return {
    endpoint,
    ...(token === undefined ? {} : { token }),
    zmxExecutable,
    zmxDir,
    environment: env,
  }
}

export const load = (overrides: Overrides) => {
  const resolved = resolve(overrides, process.env)
  return resolved instanceof ConfigError ? Effect.fail(resolved) : Effect.succeed(resolved)
}

export const layer = (overrides: Overrides) => Layer.effect(ClientConfig)(load(overrides))
