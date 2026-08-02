import { BunHttpClient } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, Runtime, Schema } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import * as path from "node:path"
import * as Client from "./client.ts"
import { AppConfig, ConfigLoadError, layer as appConfigLayer } from "./config.ts"
import * as Directories from "./directories.ts"
import * as Logging from "./logging.ts"
import * as Persistence from "./persistence.ts"
import * as ProjectRepository from "./projectRepository.ts"
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
    const effectivePort = Option.getOrElse(port, () => config.port)
    yield* Layer.launch(
      Server.layer({ port: effectivePort }).pipe(
        Layer.provide([ProjectRepository.layer, Directories.layer]),
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
 * A CLI command backed by the contract-derived client: JSON result on stdout,
 * exit 0; any config/request/decode failure becomes one `atc <name>:` line on
 * stderr and exit 1.
 */
const clientCommand = <const Name extends string, Params extends Command.Command.Config, A>(
  name: Name,
  description: string,
  params: Params,
  call: (
    client: Client.AppServerClient,
    params: Command.Command.Config.Infer<Params>,
  ) => Effect.Effect<A, unknown>,
  diagnosticName = name,
) =>
  Command.make(name, params, (parsed) =>
    Effect.gen(function* () {
      const baseUrl = yield* resolveBaseUrl
      const client = yield* Client.make({ baseUrl })
      const result = yield* call(client, parsed)
      // Void results (e.g. delete) print nothing — stdout stays pure JSON.
      if (result !== undefined) {
        yield* Console.log(JSON.stringify(result, null, 2))
      }
    }).pipe(
      Effect.provide([BunHttpClient.layer, appConfigLayer]),
      Effect.catch((error) => failReported(`atc ${diagnosticName}: ${describeError(error)}`)),
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
const directoryFlag = Flag.string("directory").pipe(
  Flag.withDescription("Project default working directory (must already exist; may be relative)"),
)

const nameFlag = Flag.string("name").pipe(Flag.withDescription("Project name"))

const projectIdArgument = Argument.string("project-id")

const projectList = clientCommand(
  "list",
  "List all projects",
  {},
  (client) => client.v1.listProjects(),
  "project list",
)

const projectGet = clientCommand(
  "get",
  "Fetch one project by id",
  { projectId: projectIdArgument },
  (client, { projectId }) => client.v1.getProject({ params: { projectId } }),
  "project get",
)

const projectCreate = clientCommand(
  "create",
  "Create a project (the directory must already exist; ATC never creates it)",
  { name: nameFlag, directory: directoryFlag },
  (client, { name, directory }) =>
    client.v1.createProject({
      payload: { name, defaultWorkingDirectory: path.resolve(directory) },
    }),
  "project create",
)

const projectUpdate = clientCommand(
  "update",
  "Update a project's name and/or default working directory",
  {
    projectId: projectIdArgument,
    name: Flag.optional(nameFlag),
    directory: Flag.optional(directoryFlag),
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
  "project update",
)

const yesFlag = Flag.boolean("yes").pipe(
  Flag.withDescription("Confirm the deletion (required; the CLI never prompts)"),
)

const projectDelete = clientCommand(
  "delete",
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
  "project delete",
)

const project = Command.make("project").pipe(
  Command.withDescription("Manage projects"),
  Command.withSubcommands([projectList, projectGet, projectCreate, projectUpdate, projectDelete]),
)

const fsCheck = clientCommand(
  "check",
  "Check a directory's health (available, missing, inaccessible, not_directory, unknown)",
  { path: Argument.string("path") },
  (client, args) => client.v1.checkDirectory({ query: { path: path.resolve(args.path) } }),
  "fs check",
)

const fs = Command.make("fs").pipe(
  Command.withDescription("Read-only filesystem operations on the server host"),
  Command.withSubcommands([fsCheck]),
)

export const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([serve, health, version, project, fs, smoke]),
)
