import { BunHttpClient } from "@effect/platform-bun"
import { Console, Effect, Layer, Runtime, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import { DEFAULT_PORT } from "./api.ts"
import * as Client from "./client.ts"
import * as Server from "./server.ts"
import { smoke } from "./smoke.ts"

/** The one reusable port contract for CLI (and later config/API) inputs. */
export const Port = Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 65535 }))

export const port = Flag.integer("port").pipe(
  Flag.withDefault(DEFAULT_PORT),
  Flag.withSchema(Port),
  Flag.withDescription(`TCP port to listen on (loopback only, default ${DEFAULT_PORT})`),
)

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
const failReported = (message: string) => {
  const line = message.replace(/\s*\n\s*/g, " ")
  return Console.error(line).pipe(Effect.andThen(Effect.fail(new ReportedError({ message: line }))))
}

// System errors carry a syscall code (EADDRINUSE, EACCES, ...). Those are
// environment problems and become one friendly line; anything else is a bug
// and re-dies so it keeps the default loud report.
const isSystemError = (defect: unknown): defect is Error & { code: string } =>
  defect instanceof Error && typeof (defect as { code?: unknown }).code === "string"

const serve = Command.make("serve", { port }, ({ port }) =>
  Layer.launch(Server.layer({ port })).pipe(
    Effect.catchDefect((defect) =>
      isSystemError(defect) ? failReported(`atc serve: ${defect.message}`) : Effect.die(defect),
    ),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))

const url = Flag.string("url").pipe(
  Flag.withSchema(Schema.URLFromString),
  Flag.withDescription(`Base URL of the App Server (e.g. http://127.0.0.1:${DEFAULT_PORT})`),
)

// Base-URL resolution seam for client commands: the explicit --url flag is the
// only source today. Connection profiles and configuration precedence are
// later work and should change only this resolution, not the commands.
const resolveBaseUrl = (flags: { readonly url: URL }): URL => flags.url

/**
 * A CLI command backed by the contract-derived client: JSON result on stdout,
 * exit 0; any request/decode failure becomes one `atc <name>:` line on stderr
 * and exit 1.
 */
const clientCommand = <A>(
  name: string,
  description: string,
  call: (client: Client.AppServerClient) => Effect.Effect<A, Error>,
) =>
  Command.make(name, { url }, (flags) =>
    Effect.gen(function* () {
      const client = yield* Client.make({ baseUrl: resolveBaseUrl(flags) })
      const result = yield* call(client)
      yield* Console.log(JSON.stringify(result, null, 2))
    }).pipe(
      Effect.catch((error) => failReported(`atc ${name}: ${error.message}`)),
      Effect.provide(BunHttpClient.layer),
    ),
  ).pipe(Command.withDescription(description))

const health = clientCommand(
  "health",
  "Check that an App Server is reachable and healthy",
  (client) => client.v1.health(),
)

const version = clientCommand(
  "version",
  "Report an App Server's build metadata and API version",
  (client) => client.v1.version(),
)

export const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([serve, health, version, smoke]),
)
