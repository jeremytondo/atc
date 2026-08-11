import { Argument, Command, Flag } from "effect/unstable/cli"
import * as path from "node:path"
import * as Cli from "./cli.ts"

// The `atc project` command group: thin client commands over the contract.

const projectDirectoryFlag = Cli.directoryFlag(
  "Project default working directory (must already exist; may be relative)",
)

const projectIdArgument = Argument.string("project-id")

const projectList = Cli.clientCommand("project list", "List all projects", {}, (client) =>
  client.v1.listProjects(),
)

const projectGet = Cli.clientCommand(
  "project get",
  "Fetch one project by id",
  { projectId: projectIdArgument },
  (client, { projectId }) => client.v1.getProject({ params: { projectId } }),
)

const projectCreate = Cli.clientCommand(
  "project create",
  "Create a project (the directory must already exist; ATC never creates it)",
  { name: Cli.nameFlag("Project name"), directory: projectDirectoryFlag },
  (client, { name, directory }) =>
    client.v1.createProject({
      payload: { name, defaultWorkingDirectory: path.resolve(directory) },
    }),
)

const projectUpdate = Cli.clientCommand(
  "project update",
  "Update a project's name and/or default working directory",
  {
    projectId: projectIdArgument,
    name: Flag.optional(Cli.nameFlag("Project name")),
    directory: Flag.optional(projectDirectoryFlag),
  },
  (client, { projectId, name, directory }) =>
    client.v1.updateProject({
      params: { projectId },
      // The contract uses absent keys (not undefined/null) for omitted fields.
      payload: {
        ...Cli.optionalKey("name", name),
        ...Cli.optionalKey("defaultWorkingDirectory", directory, path.resolve),
      },
    }),
)

const projectDelete = Cli.clientCommand(
  "project delete",
  "Delete a project and every thread and terminal it owns (never touches the filesystem)",
  { projectId: projectIdArgument, yes: Cli.yesFlag },
  (client, { projectId, yes }) =>
    Cli.requireYes(
      yes,
      "also deletes the project's threads and terminals, never the directory",
      client.v1.deleteProject({ params: { projectId } }),
    ),
)

export const project = Command.make("project").pipe(
  Command.withDescription("Manage projects"),
  Command.withSubcommands([projectList, projectGet, projectCreate, projectUpdate, projectDelete]),
)
