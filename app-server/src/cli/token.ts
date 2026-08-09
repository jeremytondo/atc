import { Console, Effect, FileSystem } from "effect"
import { Command } from "effect/unstable/cli"
import * as AuthToken from "../platform/authToken.ts"
import { AppConfig, layer as appConfigLayer } from "../platform/config.ts"
import * as Cli from "./cli.ts"

// `atc token` (ATC-148): the remote-access credential, managed locally.
// These are filesystem commands over the data dir's token file — not API
// operations — so they work with no server running and take no connection
// flags. Rotation takes effect immediately on a running server (the trust
// middleware re-reads the file per check); distributing the new token to
// remote clients means re-pasting it.

const printToken = <E>(
  diagnosticName: string,
  read: Effect.Effect<string, E, AppConfig | FileSystem.FileSystem>,
) =>
  read.pipe(
    Effect.flatMap((value) => Console.log(value)),
    Effect.provide(appConfigLayer),
    Effect.catch(Cli.reportOnce(`atc ${diagnosticName}`)),
  )

const rotate = Command.make("rotate", {}, () => printToken("token rotate", AuthToken.rotate)).pipe(
  Command.withDescription("Reissue the token and print it (the old token stops working)"),
)

export const token = Command.make("token", {}, () => printToken("token", AuthToken.ensure)).pipe(
  Command.withDescription("Print the remote-access bearer token, generating it if absent"),
  Command.withSubcommands([rotate]),
)
