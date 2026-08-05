import { Console, Effect, Option } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import * as path from "node:path"
import * as AttachClient from "../attachClient.ts"
import { attachUrl, CLOSE_DETACH } from "../attachProtocol.ts"
import * as Cli from "./cli.ts"

// The `atc terminal` command group: thin client commands over the contract,
// plus the raw-mode attach loop (the one command with local TTY behavior).

const terminalIdArgument = Argument.string("terminal-id")

const projectFlag = Flag.string("project").pipe(Flag.withDescription("Project id"))

const terminalList = Cli.clientCommand(
  "terminal list",
  "List terminals (reconciled against the zmx inventory)",
  { project: Flag.optional(projectFlag) },
  (client, { project }) =>
    client.v1.listTerminals({
      query: Option.isSome(project) ? { projectId: project.value } : {},
    }),
)

const terminalGet = Cli.clientCommand(
  "terminal get",
  "Fetch one terminal by id",
  { terminalId: terminalIdArgument },
  (client, { terminalId }) => client.v1.getTerminal({ params: { terminalId } }),
)

const terminalCreate = Cli.clientCommand(
  "terminal create",
  "Create a terminal and start its zmx session (an interactive shell, or the given command argv)",
  {
    project: projectFlag,
    name: Flag.optional(Cli.nameFlag("Display label")),
    directory: Flag.optional(
      Cli.directoryFlag("Working directory (may be relative; defaults to the project's default)"),
    ),
    command: Argument.string("command").pipe(
      Argument.atLeast(0),
      Argument.withDescription("Exec-style argv to run instead of an interactive login shell"),
    ),
  },
  (client, { project, name, directory, command }) =>
    client.v1.createTerminal({
      payload: {
        projectId: project,
        ...(Option.isSome(name) ? { name: name.value } : {}),
        ...(Option.isSome(directory) ? { workingDirectory: path.resolve(directory.value) } : {}),
        ...(command.length > 0 ? { command } : {}),
      },
    }),
)

const terminalRename = Cli.clientCommand(
  "terminal rename",
  "Update a terminal's display label",
  { terminalId: terminalIdArgument, name: Cli.nameFlag("New display label") },
  (client, { terminalId, name }) =>
    client.v1.updateTerminal({ params: { terminalId }, payload: { name } }),
)

const terminalDelete = Cli.clientCommand(
  "terminal delete",
  "Delete a terminal: kill its zmx session, verify absence, remove the record",
  { terminalId: terminalIdArgument, yes: Cli.yesFlag },
  (client, { terminalId, yes }) =>
    yes
      ? client.v1.deleteTerminal({ params: { terminalId } })
      : Effect.fail(new Error("refusing to delete without --yes (kills the running session)")),
)

const terminalAttach = Command.make(
  "attach",
  { terminalId: terminalIdArgument },
  ({ terminalId }) =>
    Cli.withClient("terminal attach", (client, baseUrl) =>
      Effect.gen(function* () {
        // Pre-flight over the typed API: the WebSocket handshake cannot carry
        // the contract's diagnostics (a browser-style client only sees
        // "connection failed"), so unknown or ended terminals are reported
        // here, with the real error, before any socket opens.
        const terminal = yield* client.v1.getTerminal({ params: { terminalId } })
        if (terminal.status === "ended") {
          return yield* Effect.fail(new Error(`terminal ${terminalId} has ended`))
        }
        const size =
          process.stdout.isTTY === true
            ? { cols: process.stdout.columns, rows: process.stdout.rows }
            : undefined
        const result = yield* AttachClient.runAttach(attachUrl(baseUrl, terminalId, size))
        if (result.code === 1000 && result.reason === CLOSE_DETACH) {
          yield* Console.error("detached (session keeps running)")
        } else if (result.code === 1000) {
          yield* Console.error("terminal ended")
        } else {
          return yield* Effect.fail(
            new Error(`connection closed (${result.code} ${result.reason || "no reason"})`),
          )
        }
      }),
    ),
).pipe(
  Command.withDescription(
    "Attach this terminal to a live session in raw mode (detach with Ctrl-])",
  ),
)

export const terminal = Command.make("terminal").pipe(
  Command.withDescription("Manage durable, project-scoped terminals"),
  Command.withSubcommands([
    terminalList,
    terminalGet,
    terminalCreate,
    terminalRename,
    terminalDelete,
    terminalAttach,
  ]),
)
