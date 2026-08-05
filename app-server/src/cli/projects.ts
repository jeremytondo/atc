import { Effect, Option } from "effect"
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
        ...(Option.isSome(name) ? { name: name.value } : {}),
        ...(Option.isSome(directory)
          ? { defaultWorkingDirectory: path.resolve(directory.value) }
          : {}),
      },
    }),
)

const projectDelete = Cli.clientCommand(
  "project delete",
  "Delete a project record (never touches the filesystem)",
  { projectId: projectIdArgument, yes: Cli.yesFlag },
  (client, { projectId, yes }) =>
    yes
      ? client.v1.deleteProject({ params: { projectId } })
      : Effect.fail(
          new Error(
            "refusing to delete without --yes (deletes the project record only, never the directory)",
          ),
        ),
)

export const project = Command.make("project").pipe(
  Command.withDescription("Manage projects"),
  Command.withSubcommands([projectList, projectGet, projectCreate, projectUpdate, projectDelete]),
)
