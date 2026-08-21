import { BunRuntime, BunServices } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, Runtime, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import * as App from "./app.ts"
import * as AppServer from "./appServer.ts"
import * as Attachment from "./attachment.ts"
import * as Config from "./config.ts"

// Temporary standalone entrypoint for the prototype. The production atc
// command remains unchanged.

class ReportedError extends Schema.TaggedErrorClass<ReportedError>()("ReportedError", {
  message: Schema.String,
}) {
  override readonly [Runtime.errorReported] = false
}

const endpoint = Flag.optional(
  Flag.string("endpoint").pipe(
    Flag.withDescription(
      "One App Server URL (defaults to ATC_ENDPOINT, then http://127.0.0.1:7331)",
    ),
  ),
)

const command = Command.make("atc-tui", { endpoint }, ({ endpoint }) =>
  App.run.pipe(
    Effect.provide(Attachment.layer),
    Effect.provide(AppServer.layer),
    Effect.provide(Config.layer(Option.getOrUndefined(endpoint))),
    Effect.catch((error) => {
      const message =
        error instanceof Config.ConfigError
          ? `${error.source}: ${error.message}`
          : error instanceof Error
            ? error.message
            : String(error)
      return Console.error(`atc-tui: ${message}`).pipe(
        Effect.andThen(Effect.fail(new ReportedError({ message }))),
      )
    }),
  ),
).pipe(Command.withDescription("Terminal-native ATC session orchestrator"))

Effect.gen(function* () {
  yield* Command.run(command, { version: "0.1.0" })
}).pipe(Effect.provide(Layer.mergeAll(BunServices.layer)), BunRuntime.runMain)
