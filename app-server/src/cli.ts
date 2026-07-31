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
  Flag.withDescription("TCP port to listen on (loopback only)"),
)

/** Startup failure already shown to the user; the marker stops runMain from logging it again. */
class ServeStartupError extends Schema.TaggedErrorClass<ServeStartupError>()("ServeStartupError", {
  message: Schema.String,
}) {
  override readonly [Runtime.errorReported] = false
}

// Defects are only caught while the server layer is being built, so a bind
// failure becomes one friendly line while genuine runtime bugs still get the
// default loud reporting.
const serve = Command.make("serve", { port }, ({ port }) =>
  Effect.scoped(
    Effect.gen(function* () {
      yield* Layer.build(Server.layer({ port })).pipe(
        Effect.catchDefect((defect) => {
          const message = defect instanceof Error ? defect.message : String(defect)
          return Console.error(`atc serve: ${message}`).pipe(
            Effect.andThen(Effect.fail(new ServeStartupError({ message }))),
          )
        }),
      )
      yield* Effect.never
    }),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))

export const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([serve]),
)
