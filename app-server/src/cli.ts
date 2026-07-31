import { Console, Effect, Layer, Runtime, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import * as Server from "./server.ts"

// Temporary dev default so the Go server on 7331 can run alongside.
export const DEFAULT_PORT = 7332

export const port = Flag.integer("port").pipe(
  Flag.withDefault(DEFAULT_PORT),
  Flag.filter(
    (value) => value >= 1 && value <= 65535,
    () => "port must be between 1 and 65535",
  ),
  Flag.withDescription(`TCP port to listen on (loopback only, default ${DEFAULT_PORT})`),
)

/** Startup failure already shown to the user; the marker stops runMain from logging it again. */
class ServeStartupError extends Schema.TaggedErrorClass<ServeStartupError>()("ServeStartupError", {
  message: Schema.String,
}) {
  override readonly [Runtime.errorReported] = false
}

// System errors carry a syscall code (EADDRINUSE, EACCES, ...). Those are
// environment problems and become one friendly line; anything else is a bug
// and re-dies so it keeps the default loud report.
const isSystemError = (defect: unknown): defect is Error & { code: string } =>
  defect instanceof Error && typeof (defect as { code?: unknown }).code === "string"

const serve = Command.make("serve", { port }, ({ port }) =>
  Layer.launch(Server.layer({ port })).pipe(
    Effect.catchDefect((defect) =>
      isSystemError(defect)
        ? Console.error(`atc serve: ${defect.message}`).pipe(
            Effect.andThen(Effect.fail(new ServeStartupError({ message: defect.message }))),
          )
        : Effect.die(defect),
    ),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))

export const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([serve]),
)
