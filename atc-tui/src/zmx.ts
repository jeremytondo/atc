import { Context, Effect, Layer, Schema } from "effect"
import * as AppServer from "./appServer.ts"
import * as Config from "./config.ts"

// Local zmx attachment boundary. The App Server owns session creation and
// returns the derived session name; this client gives zmx the real TTY and
// waits for its native Ctrl-\\ detach to return. No terminal bytes pass
// through the TUI process.

export class AttachError extends Schema.TaggedErrorClass<AttachError>()("AttachError", {
  sessionName: Schema.String,
  reason: Schema.String,
}) {
  override get message(): string {
    return `zmx attach ${this.sessionName}: ${this.reason}`
  }
}

export interface AttachSpec {
  readonly executable: string
  readonly args: ReadonlyArray<string>
  readonly env: Readonly<Record<string, string>>
}

export const attachEnvironment = (
  environment: Readonly<Record<string, string | undefined>>,
  zmxDir: string,
): Record<string, string> => ({
  ...Object.fromEntries(
    Object.entries(environment).filter(
      (entry): entry is [string, string] =>
        entry[1] !== undefined && entry[0] !== "ZMX_SESSION" && entry[0] !== "ZMX_SESSION_PREFIX",
    ),
  ),
  ZMX_DIR: zmxDir,
})

export const attachSpec = (
  config: Pick<Config.ClientConfig["Service"], "environment" | "zmxDir" | "zmxExecutable">,
  terminal: Pick<AppServer.Terminal, "sessionName">,
): AttachSpec => ({
  executable: config.zmxExecutable,
  args: ["attach", terminal.sessionName],
  env: attachEnvironment(config.environment, config.zmxDir),
})

const attach = (
  config: Config.ClientConfig["Service"],
  terminal: AppServer.Terminal,
): Effect.Effect<void, AttachError> =>
  Effect.scoped(
    Effect.gen(function* () {
      const spec = attachSpec(config, terminal)
      const failure = (reason: string) =>
        new AttachError({ sessionName: terminal.sessionName, reason })

      const child = yield* Effect.acquireRelease(
        Effect.try({
          try: () =>
            Bun.spawn([spec.executable, ...spec.args], {
              env: spec.env,
              stdin: "inherit",
              stdout: "inherit",
              stderr: "inherit",
            }),
          catch: (error) =>
            failure(error instanceof Error && error.message !== "" ? error.message : String(error)),
        }),
        (owned) =>
          Effect.promise(async () => {
            if (owned.exitCode === null) {
              try {
                owned.kill("SIGTERM")
              } catch {
                // The client exited between the exitCode check and kill.
              }
            }
            await owned.exited.catch(() => undefined)
          }),
      )

      const exitCode = yield* Effect.tryPromise({
        try: () => child.exited,
        catch: (error) =>
          failure(error instanceof Error && error.message !== "" ? error.message : String(error)),
      })
      if (child.signalCode !== null) {
        return yield* Effect.fail(failure(`client terminated by ${child.signalCode}`))
      }
      if (exitCode !== 0) {
        return yield* Effect.fail(failure(`client exited with code ${exitCode}`))
      }
    }),
  )

export class Zmx extends Context.Service<
  Zmx,
  {
    readonly attach: (terminal: AppServer.Terminal) => Effect.Effect<void, AttachError>
  }
>()("atc-tui/Zmx") {}

export const layer = Layer.effect(
  Zmx,
  Effect.gen(function* () {
    const config = yield* Config.ClientConfig
    return Zmx.of({ attach: (terminal) => attach(config, terminal) })
  }),
)
