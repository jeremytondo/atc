import { BunHttpClient } from "@effect/platform-bun"
import { Console, Effect, FileSystem, Option, Runtime, Schedule, Schema } from "effect"
import * as Poll from "../platform/poll.ts"
import { Argument, Command, Flag } from "effect/unstable/cli"
import { HttpClient } from "effect/unstable/http"
import * as path from "node:path"
import * as Client from "../api/client.ts"
import * as LocalTrust from "../api/localTrust.ts"
import { AppConfig, ConfigLoadError } from "../platform/config.ts"
import * as Config from "../platform/config.ts"
import * as Server from "../server.ts"

// Shared CLI plumbing: the one-line stderr diagnostic contract, the base-URL
// resolution seam, and the clientCommand shape every API-backed command uses.
// Command modules (serve.ts, projects.ts, terminals.ts, gateway.ts) build on
// this; the root `atc` command is assembled in main.ts.

/** Failure already shown to the user; the marker stops runMain from logging it again. */
class ReportedError extends Schema.TaggedErrorClass<ReportedError>()("ReportedError", {
  message: Schema.String,
}) {
  override readonly [Runtime.errorReported] = false
}

/**
 * Print one friendly line to stderr and exit nonzero without a second report.
 * Multi-line causes (e.g. schema decode errors) are collapsed to keep the
 * documented one-line diagnostic contract.
 */
export const failReported = (message: string) => {
  const line = message.replace(/\s*\n\s*/g, " ")
  return Console.error(line).pipe(Effect.andThen(Effect.fail(new ReportedError({ message: line }))))
}

/** The catch tail for wrappers: an already-reported failure passes through
 * unchanged; anything else becomes the one-line diagnostic. */
export const reportOnce =
  (prefix: string) =>
  (error: unknown): Effect.Effect<never, ReportedError> =>
    error instanceof ReportedError
      ? Effect.fail(error)
      : failReported(`${prefix}: ${describeError(error)}`)

// Contract error classes carry human messages (contract.ts); the String fallback
// covers any Error subclass whose message is empty.
export const describeError = (error: unknown): string =>
  error instanceof ConfigLoadError
    ? `${error.source}: ${error.message}`
    : error instanceof Error && error.message !== ""
      ? error.message
      : String(error)

/**
 * The command tail shared by every settled-config command (serve, the
 * start/stop/status family, the service group): provide AppConfig and
 * collapse failures into the one-line diagnostic under `prefix`.
 */
export const withSettledConfig = (prefix: string) => {
  return <A, E, R>(effect: Effect.Effect<A, E, R>) =>
    effect.pipe(Effect.provide(Config.layer), Effect.catch(reportOnce(prefix)))
}

// --- Server liveness probing (shared by serve.ts and service.ts, whose two
// process families are otherwise deliberately independent) ---

// The wildcard binds are not connectable addresses; probe and report loopback
// for them. A specific address (loopback or a real interface) is reachable at
// itself from the same host.
export const reachableHost = (bind: string): string =>
  bind === "0.0.0.0" || bind === "::" || bind === "" ? "127.0.0.1" : bind

// A probe at a host the trust rule does not recognize as loopback is itself
// a remote request (api/localTrust.ts) and only passes with the bearer token.
export const probeNeedsToken = (host: string): boolean =>
  !LocalTrust.isLoopbackHost(Server.hostForUrl(host))

/** One bounded health probe against `host`; false on any failure. */
export const probeHealth = (host: string, port: number, timeoutMillis: number, token?: string) =>
  Effect.tryPromise(() =>
    fetch(`http://${Server.hostForUrl(host)}:${port}/api/v1/health`, {
      signal: AbortSignal.timeout(timeoutMillis),
      headers: token === undefined ? {} : { authorization: `Bearer ${token}` },
    }),
  ).pipe(
    Effect.map((response) => response.ok),
    Effect.catch(() => Effect.succeed(false)),
  )

/** Polls `check` until true or the deadline lapses. */
export const pollUntil = (
  deadlineMillis: number,
  intervalMillis: number,
  check: Effect.Effect<boolean>,
) =>
  Poll.pollUntil(check, {
    until: (settled) => settled,
    schedule: Schedule.spaced(intervalMillis).pipe(
      Schedule.upTo({ times: Math.ceil(deadlineMillis / intervalMillis) }),
    ),
  })

// Base-URL resolution seam for client commands: ATC_ENDPOINT (set in the
// environment of processes ATC launches) wins; otherwise the URL derives
// from the settled configuration (ATC_PORT > config.toml port > default) —
// the same pipeline the server reads, so port changes just work with zero
// connection flags. Remote endpoint addressing (endpoint + token) is later,
// auth-pass work and should change only this resolution, not the commands.
export const resolveBaseUrl = Effect.gen(function* () {
  const config = yield* AppConfig
  return config.endpoint !== undefined
    ? new URL(config.endpoint)
    : new URL(`http://127.0.0.1:${config.port}`)
})

/**
 * The shared tail of every client-backed command: settled configuration,
 * HTTP client, and the one-line `atc <name>:` stderr diagnostic on any
 * config/request/decode failure.
 */
export const withCliContext = <E>(
  diagnosticName: string,
  effect: Effect.Effect<void, E, HttpClient.HttpClient | AppConfig | FileSystem.FileSystem>,
): Effect.Effect<void, ReportedError, FileSystem.FileSystem> =>
  effect.pipe(
    Effect.provide([BunHttpClient.layer, Config.layer]),
    Effect.catch(reportOnce(`atc ${diagnosticName}`)),
  )

/**
 * The one place a server connection is constructed: settled configuration,
 * base URL, contract-derived client, and the `withCliContext` diagnostic.
 * Remote endpoint addressing lands here, not in the commands.
 */
export const withClient = <E>(
  diagnosticName: string,
  use: (
    client: Client.AppServerClient,
    baseUrl: URL,
  ) => Effect.Effect<void, E, HttpClient.HttpClient>,
) =>
  withCliContext(
    diagnosticName,
    Effect.gen(function* () {
      const baseUrl = yield* resolveBaseUrl
      const client = yield* Client.make({ baseUrl })
      yield* use(client, baseUrl)
    }),
  )

/**
 * A CLI command backed by the contract-derived client: JSON result on
 * stdout, exit 0. `diagnosticName` is the full command path ("terminal
 * list"); the subcommand name is its last word, so the two can't drift.
 */
export const clientCommand = <Params extends Command.Command.Config, A>(
  diagnosticName: string,
  description: string,
  params: Params,
  call: (
    client: Client.AppServerClient,
    params: Command.Command.Config.Infer<Params>,
  ) => Effect.Effect<A, unknown>,
) =>
  Command.make(diagnosticName.split(" ").at(-1) ?? diagnosticName, params, (parsed) =>
    withClient(diagnosticName, (client) =>
      Effect.gen(function* () {
        const result = yield* call(client, parsed)
        // Void results (e.g. delete) print nothing — stdout stays pure JSON.
        if (result !== undefined) {
          yield* Console.log(JSON.stringify(result, null, 2))
        }
      }),
    ),
  ).pipe(Command.withDescription(description))

// Directory arguments may be relative (e.g. "."): the CLI shares a filesystem
// with the local server, so they resolve against the caller's cwd before the
// API sees them. The API itself takes server-host absolute paths only.
export const directoryFlag = (description: string) =>
  Flag.string("directory").pipe(Flag.withDescription(description))

export const nameFlag = (description: string) =>
  Flag.string("name").pipe(Flag.withDescription(description))

export const yesFlag = Flag.boolean("yes").pipe(
  Flag.withDescription("Confirm the deletion (required; the CLI never prompts)"),
)

/** Gate a destructive call behind --yes, naming the consequence refused. */
export const requireYes = <A, E>(
  yes: boolean,
  consequence: string,
  call: Effect.Effect<A, E>,
): Effect.Effect<A, E | Error> =>
  yes ? call : Effect.fail(new Error(`refusing to delete without --yes (${consequence})`))

export const projectFlag = Flag.string("project").pipe(Flag.withDescription("Project id"))

/**
 * Spread helper for the contract's absent-key convention: `{key: value}`
 * when the option is set (optionally mapped), `{}` otherwise.
 */
export function optionalKey<K extends string, A>(
  key: K,
  option: Option.Option<A>,
): Partial<Record<K, A>>
export function optionalKey<K extends string, A, B>(
  key: K,
  option: Option.Option<A>,
  map: (value: A) => B,
): Partial<Record<K, B>>
export function optionalKey<K extends string, A, B>(
  key: K,
  option: Option.Option<A>,
  map?: (value: A) => B,
): Partial<Record<K, A | B>> {
  return Option.isSome(option)
    ? ({ [key]: map === undefined ? option.value : map(option.value) } as Record<K, A | B>)
    : {}
}

export const health = clientCommand(
  "health",
  "Check that an App Server is reachable and healthy",
  {},
  (client) => client.v1.health(),
)

export const version = clientCommand(
  "version",
  "Report an App Server's build metadata and API version",
  {},
  (client) => client.v1.version(),
)

const fsCheck = clientCommand(
  "fs check",
  "Check a directory's health (available, missing, inaccessible, not_directory, unknown)",
  { path: Argument.string("path") },
  (client, args) => client.v1.checkDirectory({ query: { path: path.resolve(args.path) } }),
)

const fsList = clientCommand(
  "fs list",
  "List a directory's subdirectories (defaults to the server's home directory)",
  { path: Argument.optional(Argument.string("path")) },
  (client, args) =>
    client.v1.listDirectory({
      // The contract uses absent keys (not undefined/null) for omitted fields.
      query: optionalKey("path", args.path, path.resolve),
    }),
)

export const fs = Command.make("fs").pipe(
  Command.withDescription("Read-only filesystem operations on the server host"),
  Command.withSubcommands([fsCheck, fsList]),
)
