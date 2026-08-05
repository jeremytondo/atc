import { BunHttpClient } from "@effect/platform-bun"
import { Console, Effect, FileSystem, Runtime, Schema } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import { HttpClient } from "effect/unstable/http"
import * as path from "node:path"
import * as Client from "../client.ts"
import { AppConfig, ConfigLoadError, layer as appConfigLayer } from "../config.ts"

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

// Contract error classes carry human messages (api.ts); the String fallback
// covers any Error subclass whose message is empty.
export const describeError = (error: unknown): string =>
  error instanceof ConfigLoadError
    ? `${error.source}: ${error.message}`
    : error instanceof Error && error.message !== ""
      ? error.message
      : String(error)

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
    Effect.provide([BunHttpClient.layer, appConfigLayer]),
    Effect.catch((error) => failReported(`atc ${diagnosticName}: ${describeError(error)}`)),
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
  Command.make(diagnosticName.split(" ").at(-1)!, params, (parsed) =>
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

export const fs = Command.make("fs").pipe(
  Command.withDescription("Read-only filesystem operations on the server host"),
  Command.withSubcommands([fsCheck]),
)
