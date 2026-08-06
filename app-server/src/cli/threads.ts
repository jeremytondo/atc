import { Effect, Option } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import * as path from "node:path"
import { AGENT_IDS } from "../api/contract.ts"
import * as Cli from "./cli.ts"

// The `atc thread` command group: thin client commands over the contract.
// Threads are the primary unit of work, so they get the full named set
// (ATC-124); `thread open` (open-terminal + attach) arrives with the
// openTerminal workflow.

const threadIdArgument = Argument.string("thread-id")

const projectFlag = Flag.string("project").pipe(Flag.withDescription("Project id"))

const threadList = Cli.clientCommand(
  "thread list",
  "List threads (archived threads only with --archived)",
  {
    project: Flag.optional(projectFlag),
    archived: Flag.boolean("archived").pipe(
      Flag.withDescription("List archived threads instead of active ones"),
    ),
  },
  (client, { project, archived }) =>
    client.v1.listThreads({
      query: {
        ...(Option.isSome(project) ? { projectId: project.value } : {}),
        ...(archived ? { archived: "true" as const } : {}),
      },
    }),
)

const threadGet = Cli.clientCommand(
  "thread get",
  "Fetch one thread by id",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.getThread({ params: { threadId } }),
)

const threadCreate = Cli.clientCommand(
  "thread create",
  "Create a thread (local record only; the agent session starts on first use)",
  {
    project: projectFlag,
    agent: Flag.choice("agent", AGENT_IDS).pipe(
      Flag.withDescription("Agent to converse with (codex or claude-code)"),
    ),
    name: Flag.optional(Cli.nameFlag("Display label")),
    directory: Flag.optional(
      Cli.directoryFlag("Working directory (may be relative; defaults to the project's default)"),
    ),
  },
  (client, { project, agent, name, directory }) =>
    client.v1.createThread({
      payload: {
        projectId: project,
        agentId: agent,
        ...(Option.isSome(name) ? { name: name.value } : {}),
        ...(Option.isSome(directory) ? { workingDirectory: path.resolve(directory.value) } : {}),
      },
    }),
)

const threadUpdate = Cli.clientCommand(
  "thread update",
  "Update a thread's display label (the only mutable field)",
  { threadId: threadIdArgument, name: Cli.nameFlag("New display label") },
  (client, { threadId, name }) =>
    client.v1.updateThread({ params: { threadId }, payload: { name } }),
)

const threadArchive = Cli.clientCommand(
  "thread archive",
  "Archive a thread (refused while its agent session is working)",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.archiveThread({ params: { threadId } }),
)

const threadUnarchive = Cli.clientCommand(
  "thread unarchive",
  "Restore an archived thread",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.unarchiveThread({ params: { threadId } }),
)

const threadDelete = Cli.clientCommand(
  "thread delete",
  "Delete a thread: kill its live linked terminal, remove the record (provider history is untouched)",
  { threadId: threadIdArgument, yes: Cli.yesFlag },
  (client, { threadId, yes }) =>
    yes
      ? client.v1.deleteThread({ params: { threadId } })
      : Effect.fail(new Error("refusing to delete without --yes (kills the thread's terminal)")),
)

export const thread = Command.make("thread").pipe(
  Command.withDescription("Manage durable agent threads (the primary unit of work)"),
  Command.withSubcommands([
    threadList,
    threadGet,
    threadCreate,
    threadUpdate,
    threadArchive,
    threadUnarchive,
    threadDelete,
  ]),
)
