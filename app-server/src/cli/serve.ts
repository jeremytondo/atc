import { Effect, Layer, Option, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import { AppConfig, layer as appConfigLayer } from "../platform/config.ts"
import * as Server from "../server.ts"
import * as Cli from "./cli.ts"

// The serve command: settle configuration, then launch the closed production
// assembly (server.ts). The CLI carries zero domain-layer imports.

/** The one reusable port contract for CLI (and config/API) inputs. */
export const Port = Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 65535 }))

export const port = Flag.integer("port").pipe(
  Flag.withSchema(Port),
  Flag.optional,
  Flag.withDescription(
    "TCP port to listen on (loopback only; overrides ATC_PORT/config.toml/default)",
  ),
)

// System errors carry a syscall code (EADDRINUSE, EACCES, ...). Those are
// environment problems and become one friendly line; a migration failure is
// deliberately a defect that halts boot and gets the same treatment (the
// diagnostic names the migration); anything else is a bug and re-dies so it
// keeps the default loud report.
const isSystemError = (defect: unknown): defect is Error & { code: string } =>
  defect instanceof Error && typeof (defect as { code?: unknown }).code === "string"

const isMigrationError = (defect: unknown): defect is Error =>
  defect instanceof Error && (defect as { _tag?: unknown })._tag === "MigrationError"

export const serve = Command.make("serve", { port }, ({ port }) =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    // The flag level of the precedence rule: flags > env > file > defaults.
    // The override is folded back into the AppConfig the server stack sees,
    // so consumers of the settled port (e.g. the zmx adapter's ATC_ENDPOINT
    // injection) always read the port actually being served.
    const effective = { ...config, port: Option.getOrElse(port, () => config.port) }
    yield* Layer.launch(Server.production({ port: effective.port })).pipe(
      Effect.provideService(AppConfig, effective),
    )
  }).pipe(
    Effect.provide(appConfigLayer),
    Effect.catch((error) => Cli.failReported(`atc serve: ${Cli.describeError(error)}`)),
    Effect.catchDefect((defect) =>
      isSystemError(defect) || isMigrationError(defect)
        ? Cli.failReported(`atc serve: ${defect.message}`)
        : Effect.die(defect),
    ),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))
