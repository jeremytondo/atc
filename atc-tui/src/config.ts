import { Context, Effect, Layer, Schema } from "effect"
import { randomUUID } from "node:crypto"
import * as os from "node:os"
import * as path from "node:path"

// One connection decision drives both control and terminal transports. Local
// mode talks to the loopback App Server and its zmx namespace directly. Remote
// mode keeps the App Server private: SSH forwards its loopback HTTP port and a
// separate interactive SSH process runs the remote `atc terminal attach`.

export const DEFAULT_ENDPOINT = "http://127.0.0.1:7331"
export const DEFAULT_ZMX_EXECUTABLE = "zmx"
export const DEFAULT_SSH_EXECUTABLE = "ssh"
export const DEFAULT_REMOTE_ATC_EXECUTABLE = ".local/bin/atc"
export const DEFAULT_REMOTE_PORT = 7331
export const Port = Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 65535 }))

export interface Overrides {
  readonly endpoint?: string | undefined
  readonly zmxExecutable?: string | undefined
  readonly zmxDir?: string | undefined
  readonly remote?: string | undefined
  readonly sshExecutable?: string | undefined
  readonly remoteAtcExecutable?: string | undefined
  readonly remotePort?: number | undefined
}

export class ConfigError extends Schema.TaggedErrorClass<ConfigError>()("ConfigError", {
  source: Schema.String,
  message: Schema.String,
}) {}

export interface LocalConnection {
  readonly type: "local"
  readonly zmxExecutable: string
  readonly zmxDir: string
}

export interface RemoteConnection {
  readonly type: "remote"
  readonly host: string
  readonly sshExecutable: string
  readonly remoteAtcExecutable: string
  readonly remotePort: number
  readonly socketPath: string
}

export type Connection = LocalConnection | RemoteConnection

export class ClientConfig extends Context.Service<
  ClientConfig,
  {
    readonly endpoint: URL
    readonly token?: string | undefined
    readonly connection: Connection
    readonly environment: Readonly<Record<string, string | undefined>>
  }
>()("atc-tui/ClientConfig") {}

const nonEmpty = (value: string | undefined): string | undefined =>
  value !== undefined && value.trim() !== "" ? value.trim() : undefined

const validExecutable = (value: string): boolean => !value.includes("/") || path.isAbsolute(value)

const validPort = (value: number): boolean =>
  Number.isInteger(value) && value >= 1 && value <= 65535

const remoteConnection = (
  overrides: Overrides,
  remote: string,
  socketPath: string,
): RemoteConnection | ConfigError => {
  if (remote.startsWith("-") || /[\t\r\n ]/.test(remote)) {
    return new ConfigError({
      source: "--remote",
      message: `SSH host must be one host or alias without whitespace, got "${remote}"`,
    })
  }

  const sshExecutable = nonEmpty(overrides.sshExecutable) ?? DEFAULT_SSH_EXECUTABLE
  if (!validExecutable(sshExecutable)) {
    return new ConfigError({
      source: "--ssh-bin",
      message: `SSH executable must be a bare name or absolute path, got "${sshExecutable}"`,
    })
  }

  const remoteAtcExecutable =
    nonEmpty(overrides.remoteAtcExecutable) ?? DEFAULT_REMOTE_ATC_EXECUTABLE
  const remotePort = overrides.remotePort ?? DEFAULT_REMOTE_PORT
  if (!validPort(remotePort)) {
    return new ConfigError({ source: "--remote-port", message: `invalid port ${remotePort}` })
  }

  return {
    type: "remote",
    host: remote,
    sshExecutable,
    remoteAtcExecutable,
    remotePort,
    socketPath,
  }
}

/** Short absolute path: macOS Unix-domain sockets have a small path limit. */
export const makeRemoteSocketPath = (pid = process.pid, nonce = randomUUID().slice(0, 8)): string =>
  `/tmp/atc-tui-${pid}-${nonce}.sock`

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
  remoteSocketPath = makeRemoteSocketPath(),
): ClientConfig["Service"] | ConfigError => {
  const remote = nonEmpty(overrides.remote)
  if (remote !== undefined) {
    const connection = remoteConnection(overrides, remote, remoteSocketPath)
    if (connection instanceof ConfigError) return connection
    return {
      endpoint: new URL(`http://127.0.0.1:${connection.remotePort}`),
      connection,
      environment: env,
    }
  }

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
  if (!validExecutable(zmxExecutable)) {
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
    connection: { type: "local", zmxExecutable, zmxDir },
    environment: env,
  }
}

export const load = (overrides: Overrides) => {
  const resolved = resolve(overrides, process.env)
  return resolved instanceof ConfigError ? Effect.fail(resolved) : Effect.succeed(resolved)
}

export const layer = (overrides: Overrides) => Layer.effect(ClientConfig)(load(overrides))
