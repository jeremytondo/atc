import { Effect } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import * as path from "node:path"
import { AGENT_IDS } from "../api/contract.ts"
import * as Cli from "./cli.ts"
import { attachAndReport } from "./terminals.ts"

// The `atc thread` command group: thin client commands over the contract,
// plus `thread open` — open-terminal + raw attach, the one command here
// with local TTY behavior. Threads are the primary unit of work, so they
// get the full named set (ATC-124).

const threadIdArgument = Argument.string("thread-id")

const threadList = Cli.clientCommand(
  "thread list",
  "List threads (archived threads only with --archived)",
  {
    project: Flag.optional(Cli.projectFlag),
    archived: Flag.boolean("archived").pipe(
      Flag.withDescription("List archived threads instead of active ones"),
    ),
  },
  (client, { project, archived }) =>
    client.v1.listThreads({
      query: {
        ...Cli.optionalKey("projectId", project),
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
    project: Cli.projectFlag,
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
        ...Cli.optionalKey("name", name),
        ...Cli.optionalKey("workingDirectory", directory, path.resolve),
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

const threadOpen = Command.make("open", { threadId: threadIdArgument }, ({ threadId }) =>
  Cli.withClient("thread open", (client, baseUrl) =>
    Effect.gen(function* () {
      // Open-terminal + raw-TTY attach in one command: the terminal-first
      // thread workflow's front door.
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId } })
      yield* attachAndReport(client, baseUrl, terminal.id)
    }),
  ),
).pipe(
  Command.withDescription(
    "Open the thread's TUI terminal (launching the agent if needed) and attach in raw mode",
  ),
)

const threadDelete = Cli.clientCommand(
  "thread delete",
  "Delete a thread: kill its live linked terminal, remove the record (provider history is untouched)",
  { threadId: threadIdArgument, yes: Cli.yesFlag },
  (client, { threadId, yes }) =>
    Cli.requireYes(
      yes,
      "kills the thread's terminal",
      client.v1.deleteThread({ params: { threadId } }),
    ),
)

export const thread = Command.make("thread").pipe(
  Command.withDescription("Manage durable agent threads (the primary unit of work)"),
  Command.withSubcommands([
    threadList,
    threadGet,
    threadCreate,
    threadUpdate,
    threadOpen,
    threadArchive,
    threadUnarchive,
    threadDelete,
  ]),
)
