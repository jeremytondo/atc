import { Context, Effect, Layer, Schema } from "effect"
import type * as AppServer from "./appServer.ts"
import * as Config from "./config.ts"
import * as Remote from "./remote.ts"

// Terminal attachment is the only point where the manager releases its TTY.
// Local mode hands it straight to zmx. Remote mode hands it to system SSH,
// which runs the installed `atc terminal attach` beside the App Server. Exit
// 255 is the OpenSSH connection-failure contract: retry the same terminal;
// every clean exit is an intentional return to the manager.

export class AttachError extends Schema.TaggedErrorClass<AttachError>()("AttachError", {
  terminalId: Schema.String,
  reason: Schema.String,
}) {
  override get message(): string {
    return `attach terminal ${this.terminalId}: ${this.reason}`
  }
}

export interface AttachSpec {
  readonly executable: string
  readonly args: ReadonlyArray<string>
  readonly env: Readonly<Record<string, string>>
}

export interface AttachExit {
  readonly exitCode: number
  readonly signalCode: string | null
}

export type AttachRunner = (spec: AttachSpec) => Effect.Effect<AttachExit, AttachError>
export type ReconnectWait = (millis: number) => Effect.Effect<void>

export interface RemoteAttachOptions {
  readonly runner?: AttachRunner | undefined
  readonly wait?: ReconnectWait | undefined
  readonly onReconnect?: ((delayMillis: number) => Effect.Effect<void>) | undefined
}

export const attachEnvironment = (
  environment: Readonly<Record<string, string | undefined>>,
  zmxDir?: string,
): Record<string, string> => ({
  ...Object.fromEntries(
    Object.entries(environment).filter(
      (entry): entry is [string, string] =>
        entry[1] !== undefined && entry[0] !== "ZMX_SESSION" && entry[0] !== "ZMX_SESSION_PREFIX",
    ),
  ),
  ...(zmxDir === undefined ? {} : { ZMX_DIR: zmxDir }),
})

export const shellQuote = (value: string): string =>
  value === "" ? "''" : `'${value.replaceAll("'", `'"'"'`)}'`

export const localAttachSpec = (
  config: Config.ClientConfig["Service"],
  connection: Config.LocalConnection,
  terminal: Pick<AppServer.Terminal, "id" | "sessionName">,
): AttachSpec => ({
  executable: connection.zmxExecutable,
  args: ["attach", terminal.sessionName],
  env: attachEnvironment(config.environment, connection.zmxDir),
})

export const remoteAttachSpec = (
  config: Config.ClientConfig["Service"],
  connection: Config.RemoteConnection,
  terminal: Pick<AppServer.Terminal, "id" | "sessionName">,
): AttachSpec => ({
  executable: connection.sshExecutable,
  args: [
    "-tt",
    ...Remote.connectionArgs,
    connection.host,
    [connection.remoteAtcExecutable, "terminal", "attach", terminal.id].map(shellQuote).join(" "),
  ],
  env: attachEnvironment(config.environment),
})

const run =
  (terminalId: string): AttachRunner =>
  (spec) =>
    Effect.scoped(
      Effect.gen(function* () {
        const failure = (reason: string) => new AttachError({ terminalId, reason })
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
              failure(
                error instanceof Error && error.message !== "" ? error.message : String(error),
              ),
          }),
          (owned) =>
            Effect.promise(async () => {
              if (owned.exitCode === null) {
                try {
                  owned.kill("SIGTERM")
                } catch {
                  // The client exited between the exitCode check and kill.
                }
                const escalate = setTimeout(() => {
                  try {
                    owned.kill("SIGKILL")
                  } catch {
                    // The client exited during the grace period.
                  }
                }, 2_000)
                await owned.exited.catch(() => undefined)
                clearTimeout(escalate)
                return
              }
              await owned.exited.catch(() => undefined)
            }),
        )

        const exitCode = yield* Effect.tryPromise({
          try: () => child.exited,
          catch: (error) =>
            failure(error instanceof Error && error.message !== "" ? error.message : String(error)),
        })
        return { exitCode, signalCode: child.signalCode }
      }),
    )

const reconnectDelay = (attempt: number): number => Math.min(8_000, 1_000 * 2 ** attempt)

export const attachRemote = (
  config: Config.ClientConfig["Service"],
  connection: Config.RemoteConnection,
  terminal: AppServer.Terminal,
  options: RemoteAttachOptions = {},
): Effect.Effect<void, AttachError> => {
  const runner = options.runner ?? run(terminal.id)
  const wait = options.wait ?? ((millis) => Effect.sleep(`${millis} millis`))
  const onReconnect =
    options.onReconnect ??
    ((delayMillis: number) =>
      Effect.sync(() => {
        process.stderr.write(
          `\natc-tui: connection to ${connection.host} lost; reconnecting in ${delayMillis / 1_000}s (Ctrl-C to stop)\n`,
        )
      }))
  const loop = (attempt: number): Effect.Effect<void, AttachError> =>
    runner(remoteAttachSpec(config, connection, terminal)).pipe(
      Effect.flatMap((result) => {
        if (result.signalCode !== null) {
          return Effect.fail(
            new AttachError({
              terminalId: terminal.id,
              reason: `SSH terminated by ${result.signalCode}`,
            }),
          )
        }
        if (result.exitCode === 0) return Effect.void
        if (result.exitCode !== 255) {
          return Effect.fail(
            new AttachError({
              terminalId: terminal.id,
              reason: `remote attach exited with code ${result.exitCode}`,
            }),
          )
        }
        const delay = reconnectDelay(attempt)
        return onReconnect(delay).pipe(
          Effect.andThen(wait(delay)),
          Effect.andThen(loop(attempt + 1)),
        )
      }),
    )
  return loop(0)
}

const attachLocal = (
  config: Config.ClientConfig["Service"],
  connection: Config.LocalConnection,
  terminal: AppServer.Terminal,
) =>
  run(terminal.id)(localAttachSpec(config, connection, terminal)).pipe(
    Effect.flatMap((result) => {
      if (result.signalCode !== null) {
        return Effect.fail(
          new AttachError({
            terminalId: terminal.id,
            reason: `zmx terminated by ${result.signalCode}`,
          }),
        )
      }
      return result.exitCode === 0
        ? Effect.void
        : Effect.fail(
            new AttachError({
              terminalId: terminal.id,
              reason: `zmx exited with code ${result.exitCode}`,
            }),
          )
    }),
  )

export class TerminalAttachment extends Context.Service<
  TerminalAttachment,
  {
    readonly attach: (terminal: AppServer.Terminal) => Effect.Effect<void, AttachError>
  }
>()("atc-tui/TerminalAttachment") {}

export const layer = Layer.effect(
  TerminalAttachment,
  Effect.gen(function* () {
    const config = yield* Config.ClientConfig
    const connection = config.connection
    return TerminalAttachment.of({
      attach: (terminal) =>
        connection.type === "local"
          ? attachLocal(config, connection, terminal)
          : attachRemote(config, connection, terminal),
    })
  }),
)
