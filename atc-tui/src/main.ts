import { BunRuntime, BunServices } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, Runtime, Schema } from "effect"
import { Command, Flag } from "effect/unstable/cli"
import * as App from "./app.ts"
import * as AppServer from "./appServer.ts"
import * as Config from "./config.ts"
import * as Remote from "./remote.ts"
import * as Subprocess from "../../app-server/src/platform/subprocess.ts"
import * as TerminalAttachment from "./terminalAttachment.ts"

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

const zmxBin = Flag.optional(
  Flag.string("zmx-bin").pipe(
    Flag.withDescription("zmx executable (defaults to ATC_ZMX_EXECUTABLE, then zmx)"),
  ),
)

const zmxDir = Flag.optional(
  Flag.string("zmx-dir").pipe(
    Flag.withDescription("ATC's local zmx socket directory (defaults from XDG_STATE_HOME)"),
  ),
)

const remote = Flag.optional(
  Flag.string("remote").pipe(
    Flag.withDescription("SSH host or alias whose loopback App Server this TUI controls"),
  ),
)

const sshBin = Flag.optional(
  Flag.string("ssh-bin").pipe(Flag.withDescription("system SSH executable (defaults to ssh)")),
)

const remoteAtc = Flag.optional(
  Flag.string("remote-atc").pipe(
    Flag.withDescription("path to atc on the SSH host (defaults to .local/bin/atc)"),
  ),
)

const remotePort = Flag.integer("remote-port").pipe(
  Flag.withSchema(Config.Port),
  Flag.optional,
  Flag.withDescription("remote loopback App Server port (defaults to 7331)"),
)

const command = Command.make(
  "atc-tui",
  { endpoint, zmxBin, zmxDir, remote, sshBin, remoteAtc, remotePort },
  (options) =>
    App.run.pipe(
      Effect.provide(TerminalAttachment.layer),
      Effect.provide(AppServer.layer),
      Effect.provide(Remote.layer),
      Effect.provide(Subprocess.layer),
      Effect.provide(
        Config.layer({
          endpoint: Option.getOrUndefined(options.endpoint),
          zmxExecutable: Option.getOrUndefined(options.zmxBin),
          zmxDir: Option.getOrUndefined(options.zmxDir),
          remote: Option.getOrUndefined(options.remote),
          sshExecutable: Option.getOrUndefined(options.sshBin),
          remoteAtcExecutable: Option.getOrUndefined(options.remoteAtc),
          remotePort: Option.getOrUndefined(options.remotePort),
        }),
      ),
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
