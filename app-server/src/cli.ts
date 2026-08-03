import { BunHttpClient } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, Runtime, Schema } from "effect"
import type { FileSystem } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import type { HttpClient } from "effect/unstable/http"
import * as path from "node:path"
import * as AttachClient from "./attachClient.ts"
import { attachUrl, CLOSE_DETACH } from "./attachProtocol.ts"
import * as Client from "./client.ts"
import { AppConfig, ConfigLoadError, layer as appConfigLayer } from "./config.ts"
import * as Directories from "./directories.ts"
import * as Logging from "./logging.ts"
import * as Persistence from "./persistence.ts"
import * as ProjectRepository from "./projectRepository.ts"
import * as Server from "./server.ts"
import { smoke } from "./smoke.ts"
import * as TerminalRepository from "./terminalRepository.ts"
import * as Terminals from "./terminals.ts"
import * as Zmx from "./zmxAdapter.ts"

/** The one reusable port contract for CLI (and config/API) inputs. */
export const Port = Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 65535 }))

export const port = Flag.integer("port").pipe(
  Flag.withSchema(Port),
  Flag.optional,
  Flag.withDescription(
    "TCP port to listen on (loopback only; overrides ATC_PORT/config.toml/default)",
  ),
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

// Contract error classes carry human messages (api.ts); the String fallback
// covers any Error subclass whose message is empty.
const describeError = (error: unknown): string =>
  error instanceof ConfigLoadError
    ? `${error.source}: ${error.message}`
    : error instanceof Error && error.message !== ""
      ? error.message
      : String(error)

// System errors carry a syscall code (EADDRINUSE, EACCES, ...). Those are
// environment problems and become one friendly line; a migration failure is
// deliberately a defect that halts boot and gets the same treatment (the
// diagnostic names the migration); anything else is a bug and re-dies so it
// keeps the default loud report.
const isSystemError = (defect: unknown): defect is Error & { code: string } =>
  defect instanceof Error && typeof (defect as { code?: unknown }).code === "string"

const isMigrationError = (defect: unknown): defect is Error =>
  defect instanceof Error && (defect as { _tag?: unknown })._tag === "MigrationError"

const serve = Command.make("serve", { port }, ({ port }) =>
  Effect.gen(function* () {
    const config = yield* AppConfig
    // The flag level of the precedence rule: flags > env > file > defaults.
    const effectivePort = Option.getOrElse(port, () => config.port)
    yield* Layer.launch(
      Server.layer({ port: effectivePort }).pipe(
        Layer.provide(Terminals.layer),
        Layer.provide([
          ProjectRepository.layer,
          TerminalRepository.layer,
          Directories.layer,
          Zmx.layer,
        ]),
        Layer.provide(Persistence.layer),
        Layer.provide(Logging.layer),
      ),
    )
  }).pipe(
    Effect.provide(appConfigLayer),
    Effect.catch((error) => failReported(`atc serve: ${describeError(error)}`)),
    Effect.catchDefect((defect) =>
      isSystemError(defect) || isMigrationError(defect)
        ? failReported(`atc serve: ${defect.message}`)
        : Effect.die(defect),
    ),
  ),
).pipe(Command.withDescription("Run the App Server in the foreground until interrupted"))

// Base-URL resolution seam for client commands: derived from the settled
// configuration (ATC_PORT > config.toml port > default) — the same pipeline
// the server reads, so port changes just work with zero connection flags.
// Remote endpoint addressing (endpoint + token) is later, auth-pass work and
// should change only this resolution, not the commands.
const resolveBaseUrl = Effect.gen(function* () {
  const config = yield* AppConfig
  return new URL(`http://127.0.0.1:${config.port}`)
})

/**
 * The shared tail of every client-backed command: settled configuration,
 * HTTP client, and the one-line `atc <name>:` stderr diagnostic on any
 * config/request/decode failure.
 */
const withCliContext = <E>(
  diagnosticName: string,
  effect: Effect.Effect<void, E, HttpClient.HttpClient | AppConfig>,
): Effect.Effect<void, ReportedError, FileSystem.FileSystem> =>
  effect.pipe(
    Effect.provide([BunHttpClient.layer, appConfigLayer]),
    Effect.catch((error) => failReported(`atc ${diagnosticName}: ${describeError(error)}`)),
  )

/**
 * The one place a server connection is constructed: settled configuration,
 * base URL, contract-derived client, and the `withCliContext` diagnostic.
 * Remote endpoint addressing lands here, not in the commands.
 */
const withClient = <E>(
  diagnosticName: string,
  use: (
    client: Client.AppServerClient,
    baseUrl: URL,
  ) => Effect.Effect<void, E, HttpClient.HttpClient>,
) =>
  withCliContext(
    diagnosticName,
    Effect.gen(function* () {
      const baseUrl = yield* resolveBaseUrl
      const client = yield* Client.make({ baseUrl })
      yield* use(client, baseUrl)
    }),
  )

/**
 * A CLI command backed by the contract-derived client: JSON result on
 * stdout, exit 0. `diagnosticName` is the full command path ("terminal
 * list"); the subcommand name is its last word, so the two can't drift.
 */
const clientCommand = <Params extends Command.Command.Config, A>(
  diagnosticName: string,
  description: string,
  params: Params,
  call: (
    client: Client.AppServerClient,
    params: Command.Command.Config.Infer<Params>,
  ) => Effect.Effect<A, unknown>,
) =>
  Command.make(diagnosticName.split(" ").at(-1)!, params, (parsed) =>
    withClient(diagnosticName, (client) =>
      Effect.gen(function* () {
        const result = yield* call(client, parsed)
        // Void results (e.g. delete) print nothing — stdout stays pure JSON.
        if (result !== undefined) {
          yield* Console.log(JSON.stringify(result, null, 2))
        }
      }),
    ),
  ).pipe(Command.withDescription(description))

const health = clientCommand(
  "health",
  "Check that an App Server is reachable and healthy",
  {},
  (client) => client.v1.health(),
)

const version = clientCommand(
  "version",
  "Report an App Server's build metadata and API version",
  {},
  (client) => client.v1.version(),
)

// Directory arguments may be relative (e.g. "."): the CLI shares a filesystem
// with the local server, so they resolve against the caller's cwd before the
// API sees them. The API itself takes server-host absolute paths only.
const directoryFlag = (description: string) =>
  Flag.string("directory").pipe(Flag.withDescription(description))

const nameFlag = (description: string) =>
  Flag.string("name").pipe(Flag.withDescription(description))

const projectDirectoryFlag = directoryFlag(
  "Project default working directory (must already exist; may be relative)",
)

const projectIdArgument = Argument.string("project-id")

const projectList = clientCommand("project list", "List all projects", {}, (client) =>
  client.v1.listProjects(),
)

const projectGet = clientCommand(
  "project get",
  "Fetch one project by id",
  { projectId: projectIdArgument },
  (client, { projectId }) => client.v1.getProject({ params: { projectId } }),
)

const projectCreate = clientCommand(
  "project create",
  "Create a project (the directory must already exist; ATC never creates it)",
  { name: nameFlag("Project name"), directory: projectDirectoryFlag },
  (client, { name, directory }) =>
    client.v1.createProject({
      payload: { name, defaultWorkingDirectory: path.resolve(directory) },
    }),
)

const projectUpdate = clientCommand(
  "project update",
  "Update a project's name and/or default working directory",
  {
    projectId: projectIdArgument,
    name: Flag.optional(nameFlag("Project name")),
    directory: Flag.optional(projectDirectoryFlag),
  },
  (client, { projectId, name, directory }) =>
    client.v1.updateProject({
      params: { projectId },
      // The contract uses absent keys (not undefined/null) for omitted fields.
      payload: {
        ...(Option.isSome(name) ? { name: name.value } : {}),
        ...(Option.isSome(directory)
          ? { defaultWorkingDirectory: path.resolve(directory.value) }
          : {}),
      },
    }),
)

const yesFlag = Flag.boolean("yes").pipe(
  Flag.withDescription("Confirm the deletion (required; the CLI never prompts)"),
)

const projectDelete = clientCommand(
  "project delete",
  "Delete a project record (never touches the filesystem)",
  { projectId: projectIdArgument, yes: yesFlag },
  (client, { projectId, yes }) =>
    yes
      ? client.v1.deleteProject({ params: { projectId } })
      : Effect.fail(
          new Error(
            "refusing to delete without --yes (deletes the project record only, never the directory)",
          ),
        ),
)

const project = Command.make("project").pipe(
  Command.withDescription("Manage projects"),
  Command.withSubcommands([projectList, projectGet, projectCreate, projectUpdate, projectDelete]),
)

const terminalIdArgument = Argument.string("terminal-id")

const projectFlag = Flag.string("project").pipe(Flag.withDescription("Project id"))

const terminalList = clientCommand(
  "terminal list",
  "List terminals (reconciled against the zmx inventory)",
  { project: Flag.optional(projectFlag) },
  (client, { project }) =>
    client.v1.listTerminals({
      query: Option.isSome(project) ? { projectId: project.value } : {},
    }),
)

const terminalGet = clientCommand(
  "terminal get",
  "Fetch one terminal by id",
  { terminalId: terminalIdArgument },
  (client, { terminalId }) => client.v1.getTerminal({ params: { terminalId } }),
)

const terminalCreate = clientCommand(
  "terminal create",
  "Create a terminal and start its zmx session (an interactive shell, or the given command argv)",
  {
    project: projectFlag,
    name: Flag.optional(nameFlag("Display label")),
    directory: Flag.optional(
      directoryFlag("Working directory (may be relative; defaults to the project's default)"),
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

const terminalRename = clientCommand(
  "terminal rename",
  "Update a terminal's display label",
  { terminalId: terminalIdArgument, name: nameFlag("New display label") },
  (client, { terminalId, name }) =>
    client.v1.updateTerminal({ params: { terminalId }, payload: { name } }),
)

const terminalDelete = clientCommand(
  "terminal delete",
  "Delete a terminal: kill its zmx session, verify absence, remove the record",
  { terminalId: terminalIdArgument, yes: yesFlag },
  (client, { terminalId, yes }) =>
    yes
      ? client.v1.deleteTerminal({ params: { terminalId } })
      : Effect.fail(new Error("refusing to delete without --yes (kills the running session)")),
)

const terminalAttach = Command.make(
  "attach",
  { terminalId: terminalIdArgument },
  ({ terminalId }) =>
    withClient("terminal attach", (client, baseUrl) =>
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

const terminal = Command.make("terminal").pipe(
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

const fsCheck = clientCommand(
  "fs check",
  "Check a directory's health (available, missing, inaccessible, not_directory, unknown)",
  { path: Argument.string("path") },
  (client, args) => client.v1.checkDirectory({ query: { path: path.resolve(args.path) } }),
)

const fs = Command.make("fs").pipe(
  Command.withDescription("Read-only filesystem operations on the server host"),
  Command.withSubcommands([fsCheck]),
)

export const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([serve, health, version, project, terminal, fs, smoke]),
)
