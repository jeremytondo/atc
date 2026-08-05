import { BunHttpClient } from "@effect/platform-bun"
import { Console, Effect, FileSystem, Layer, Option, Runtime, Schema } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import { HttpClient, HttpClientRequest } from "effect/unstable/http"
import * as path from "node:path"
import * as AttachClient from "./attachClient.ts"
import { attachUrl, CLOSE_DETACH } from "./attachProtocol.ts"
import * as Client from "./client.ts"
import { AppConfig, ConfigLoadError, CONTEXT_VARIABLES, layer as appConfigLayer } from "./config.ts"
import * as Server from "./server.ts"
import { smoke } from "./smoke.ts"

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
    // The override is folded back into the AppConfig the server stack sees,
    // so consumers of the settled port (e.g. the zmx adapter's ATC_ENDPOINT
    // injection) always read the port actually being served.
    const effective = { ...config, port: Option.getOrElse(port, () => config.port) }
    yield* Layer.launch(Server.production({ port: effective.port })).pipe(
      Effect.provideService(AppConfig, effective),
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

// Base-URL resolution seam for client commands: ATC_ENDPOINT (set in the
// environment of processes ATC launches) wins; otherwise the URL derives
// from the settled configuration (ATC_PORT > config.toml port > default) —
// the same pipeline the server reads, so port changes just work with zero
// connection flags. Remote endpoint addressing (endpoint + token) is later,
// auth-pass work and should change only this resolution, not the commands.
const resolveBaseUrl = Effect.gen(function* () {
  const config = yield* AppConfig
  return config.endpoint !== undefined
    ? new URL(config.endpoint)
    : new URL(`http://127.0.0.1:${config.port}`)
})

/**
 * The shared tail of every client-backed command: settled configuration,
 * HTTP client, and the one-line `atc <name>:` stderr diagnostic on any
 * config/request/decode failure.
 */
const withCliContext = <E>(
  diagnosticName: string,
  effect: Effect.Effect<void, E, HttpClient.HttpClient | AppConfig | FileSystem.FileSystem>,
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

// --- The agent gateway (ATC-131) ---------------------------------------
//
// The HTTP API is the complete, canonical App Server interface; `atc api`
// gives agents and scripts method-and-path access to all of it, so a curated
// command only exists where it genuinely improves on raw API access. The
// gateway is a plain HTTP passthrough over the same base-URL seam the
// curated commands use — the contract-derived client exposes only typed
// per-operation functions, and the server validates every request against
// the contract regardless of origin.

const API_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const

const methodArgument = Argument.string("method").pipe(
  Argument.mapTryCatch(
    (raw) => {
      const method = raw.toUpperCase() as (typeof API_METHODS)[number]
      if (!API_METHODS.includes(method)) throw new Error()
      return method
    },
    () => `method must be one of ${API_METHODS.join(", ")} (case-insensitive)`,
  ),
  Argument.withDescription(`HTTP method: ${API_METHODS.join(", ")} (case-insensitive)`),
)

/**
 * Join a server-relative path onto the configured base URL. Strict and
 * fallible: network-path forms (`//host`, and `/\host` — the URL parser
 * treats backslash as slash) would silently re-target the request to
 * another origin, so they are rejected up front and the resolved origin is
 * verified unchanged. An endpoint with a path prefix (e.g.
 * `http://host:1234/prefix`) keeps it, matching the typed client's joining.
 */
const requestUrl = (baseUrl: URL, requestPath: string): Effect.Effect<URL, Error> => {
  if (!requestPath.startsWith("/") || /^\/[/\\]/.test(requestPath)) {
    return Effect.fail(
      new Error(
        `path must be server-relative (start with /, but not // or /\\), got "${requestPath}"`,
      ),
    )
  }
  const prefix = baseUrl.pathname.replace(/\/$/, "")
  return Effect.try({
    try: () => new URL(baseUrl.origin + prefix + requestPath),
    catch: () => new Error(`cannot build a request URL from "${requestPath}"`),
  }).pipe(
    Effect.filterOrFail(
      (url) => url.origin === baseUrl.origin,
      () => new Error(`path "${requestPath}" escapes the configured endpoint`),
    ),
  )
}

/** Request body source: a file path, or "-" for stdin. */
const readRequestBody = (source: string) =>
  source === "-"
    ? Effect.tryPromise({
        try: () => Bun.stdin.text(),
        catch: () => new Error("cannot read the request body from stdin"),
      })
    : Effect.gen(function* () {
        const fileSystem = yield* FileSystem.FileSystem
        return yield* fileSystem.readFileString(source)
      })

const api = Command.make(
  "api",
  {
    method: methodArgument,
    path: Argument.string("path").pipe(
      Argument.withDescription("Server-relative request path (e.g. /api/v1/projects)"),
    ),
    input: Flag.string("input").pipe(
      Flag.optional,
      Flag.withDescription("JSON request body: a file path, or - to read stdin"),
    ),
  },
  ({ method, path: requestPath, input }) =>
    withCliContext(
      "api",
      Effect.gen(function* () {
        const baseUrl = yield* resolveBaseUrl
        const url = yield* requestUrl(baseUrl, requestPath)
        const client = yield* HttpClient.HttpClient
        const body = Option.isSome(input) ? yield* readRequestBody(input.value) : undefined
        const request = HttpClientRequest.make(method)(url, {
          acceptJson: true,
        }).pipe((base) =>
          body === undefined ? base : HttpClientRequest.bodyText(base, body, "application/json"),
        )
        const response = yield* client.execute(request)
        const text = yield* response.text
        if (response.status >= 200 && response.status < 300) {
          // Success bodies pass through unchanged (plus the trailing
          // newline); an empty response prints nothing.
          if (text !== "") yield* Console.log(text)
          return
        }
        // API failures: the machine-readable error body, then the one-line
        // diagnostic — both on stderr, exit 1 like every other failure.
        if (text !== "") yield* Console.error(text)
        return yield* Effect.fail(new Error(`server returned HTTP ${response.status}`))
      }),
    ),
).pipe(
  Command.withDescription(
    "Send one request to any App Server API endpoint (the generic gateway; see /openapi.json)",
  ),
)

const jsonFlag = Flag.boolean("json").pipe(
  Flag.withDescription("Emit machine-readable JSON instead of text"),
)

const context = Command.make("context", { json: jsonFlag }, ({ json }) =>
  withCliContext(
    "context",
    Effect.gen(function* () {
      const config = yield* AppConfig
      // Reported in vocabulary order, present variables only — absent ones
      // are omitted entirely, never invented.
      if (json) {
        yield* Console.log(JSON.stringify(config.context, null, 2))
        return
      }
      for (const name of CONTEXT_VARIABLES) {
        const value = config.context[name]
        if (value !== undefined) yield* Console.log(`${name}=${value}`)
      }
    }),
  ),
).pipe(
  Command.withDescription(
    "Report the ATC_* agent-context variables present in this process's environment",
  ),
)

/**
 * The stable capability description for agents. Version the shape: additions
 * bump nothing, breaking changes bump capabilitiesVersion.
 */
const CAPABILITIES = {
  capabilitiesVersion: 1,
  api: {
    command: "atc api <method> <path> [--input <file|->]",
    description:
      "Complete access to the canonical App Server HTTP API: every operation via GET, POST, PUT, PATCH, or DELETE, JSON body from a file or stdin, the raw JSON response on stdout.",
    example: "atc api GET /api/v1/projects",
  },
  openapi: {
    path: "/openapi.json",
    description:
      "The full OpenAPI document (every operation and schema), served by the App Server.",
    example: "atc api GET /openapi.json",
  },
  context: {
    command: "atc context --json",
    description: "The ATC_* context variables present in this process.",
    variables: CONTEXT_VARIABLES,
  },
  workflows: [
    { command: "atc serve", description: "Run the App Server in the foreground." },
    {
      command: "atc project create --name <name> --directory <dir>",
      description: "Create a project (relative directories resolve against the caller's cwd).",
    },
    {
      command: "atc terminal create --project <id> [command...]",
      description: "Create a durable terminal and start its session immediately.",
    },
    {
      command: "atc terminal attach <terminal-id>",
      description: "Attach the local TTY to a live terminal session (detach with Ctrl-]).",
    },
  ],
} as const

const capabilities = Command.make("capabilities", { json: jsonFlag }, ({ json }) =>
  json
    ? Console.log(JSON.stringify(CAPABILITIES, null, 2))
    : Console.log(
        [
          `${CAPABILITIES.api.command} — ${CAPABILITIES.api.description}`,
          `${CAPABILITIES.openapi.example} — ${CAPABILITIES.openapi.description}`,
          `${CAPABILITIES.context.command} — ${CAPABILITIES.context.description}`,
          ...CAPABILITIES.workflows.map((w) => `${w.command} — ${w.description}`),
        ].join("\n"),
      ),
).pipe(Command.withDescription("Describe the CLI's agent-facing capabilities (stable, versioned)"))

export const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([
    serve,
    api,
    context,
    capabilities,
    health,
    version,
    project,
    terminal,
    fs,
    smoke,
  ]),
)
