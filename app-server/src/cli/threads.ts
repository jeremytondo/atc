import { Console, Effect, Option, Schema, Stream } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import * as path from "node:path"
import type * as Contract from "../api/contract.ts"
import { AGENT_IDS } from "../api/contract.ts"
import * as Cli from "./cli.ts"
import { attachAndReport } from "./terminals.ts"

// The `atc thread` command group: thin client commands over the contract,
// plus the commands that earn a name by local TTY or streaming behavior —
// `thread open` (open-terminal + raw attach) and the runtime set (ATC-193):
// `prompt --follow` / `follow` stream the per-thread events as JSON lines
// until the turn ends (or forever); `answer` composes a request answer from
// flags. Threads are the primary unit of work, so they get the full named
// set (ATC-124); everything else stays `atc api`.

type ThreadEvent = typeof Contract.ThreadEvent.Type

/** Print each event as one JSON line; stop when `done` says so (or the stream ends). */
const printEvents = (
  events: Stream.Stream<ThreadEvent, unknown>,
  done: (event: ThreadEvent) => boolean = () => false,
) =>
  events.pipe(
    Stream.takeUntil(done),
    Stream.runForEach((event) => Console.log(JSON.stringify(event))),
  )

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

const threadClose = Cli.clientCommand(
  "thread close",
  "Close the thread's TUI: hand the thread back to Chat/API prompts (a Claude TUI ends once its turn is on disk; a Codex TUI keeps running)",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.closeThreadTerminal({ params: { threadId } }),
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

const threadPrompt = Command.make(
  "prompt",
  {
    threadId: threadIdArgument,
    text: Argument.string("text"),
    follow: Flag.boolean("follow").pipe(
      Flag.withDescription("Stream the thread's events (JSON lines) until this prompt's turn ends"),
    ),
  },
  ({ threadId, text, follow }) =>
    Cli.withClient("thread prompt", (client) =>
      Effect.gen(function* () {
        // Subscribe BEFORE prompting so no event of the turn is missed.
        const events = follow
          ? yield* client.v1.subscribeThreadEvents({ params: { threadId }, query: {} })
          : undefined
        const admitted = yield* client.v1.promptThread({
          params: { threadId },
          payload: { prompt: text },
        })
        // Without --follow the admission is the result; with it, stdout is
        // a pure ThreadEvent stream (the turn.started event names the
        // prompt) that ends when this prompt's turn does.
        if (events === undefined) return yield* Console.log(JSON.stringify(admitted))
        yield* printEvents(
          events,
          (event) =>
            event.type === "turn.completed" &&
            (event.turn.id === admitted.turnId || event.turn.promptId === admitted.promptId),
        )
      }),
    ),
).pipe(
  Command.withDescription(
    "Prompt the thread (queued if a turn is running); --follow streams the thread's events (JSON lines) until this prompt's turn ends",
  ),
)

const threadFollow = Command.make(
  "follow",
  {
    threadId: threadIdArgument,
    after: Flag.optional(
      Flag.integer("after").pipe(
        Flag.withDescription("Replay every durable change after this seq before going live"),
      ),
    ),
  },
  ({ threadId, after }) =>
    Cli.withClient("thread follow", (client) =>
      Effect.gen(function* () {
        const events = yield* client.v1.subscribeThreadEvents({
          params: { threadId },
          query: Cli.optionalKey("after", after),
        })
        yield* printEvents(events)
      }),
    ),
).pipe(Command.withDescription("Stream the thread's events as JSON lines (Ctrl-C to stop)"))

const threadInterrupt = Cli.clientCommand(
  "thread interrupt",
  "Interrupt the running turn (queued prompts stay queued)",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.interruptThread({ params: { threadId } }),
)

const threadTranscript = Cli.clientCommand(
  "thread transcript",
  "Read the thread's transcript (newest page; --before pages older)",
  {
    threadId: threadIdArgument,
    limit: Flag.optional(Flag.integer("limit").pipe(Flag.withDescription("Most items to return"))),
    before: Flag.optional(
      Flag.string("before").pipe(Flag.withDescription("Return items before this item id")),
    ),
  },
  (client, { threadId, limit, before }) =>
    client.v1.getThreadTranscript({
      params: { threadId },
      query: { ...Cli.optionalKey("limit", limit), ...Cli.optionalKey("before", before) },
    }),
)

const threadRequests = Cli.clientCommand(
  "thread requests",
  "List the thread's pending provider requests (approvals and questions)",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.listThreadRequests({ params: { threadId } }),
)

const DECISIONS = ["accept", "acceptForSession", "decline", "cancel"] as const

// The contract's answer values are arrays of labels (a freeform answer is a
// one-element array); a bare string is the natural way to type a freeform
// answer, so accept it too and normalize to the contract shape.
export const decodeAnswers = (json: string) =>
  Schema.decodeUnknownEffect(
    Schema.fromJsonString(
      Schema.Record(Schema.String, Schema.Union([Schema.Array(Schema.String), Schema.String])),
    ),
  )(json).pipe(
    Effect.map((decoded) =>
      Object.fromEntries(
        Object.entries(decoded).map(([id, chosen]) => [
          id,
          typeof chosen === "string" ? [chosen] : chosen,
        ]),
      ),
    ),
  )

const threadAnswer = Command.make(
  "answer",
  {
    threadId: threadIdArgument,
    requestId: Argument.string("request-id"),
    decision: Flag.optional(
      Flag.choice("decision", DECISIONS).pipe(
        Flag.withDescription(
          "Approval decision (cancel = decline and interrupt the turn); exclusive with --answers",
        ),
      ),
    ),
    answers: Flag.optional(
      Flag.string("answers").pipe(
        Flag.withDescription(
          'Question answers as JSON, question id → chosen labels or freeform text ({"q0":["blue"]} or {"q0":"a freeform answer"}); "-" reads the JSON from stdin (use it for secret answers, which must never sit in argv)',
        ),
      ),
    ),
  },
  ({ threadId, requestId, decision, answers }) =>
    Cli.withClient("thread answer", (client) =>
      Effect.gen(function* () {
        if (Option.isSome(decision) === Option.isSome(answers)) {
          return yield* Effect.fail(new Error("pass exactly one of --decision or --answers"))
        }
        if (Option.isSome(decision)) {
          return yield* client.v1.answerThreadRequest({
            params: { threadId, requestId },
            payload: { kind: "approval", decision: decision.value },
          })
        }
        const raw = Option.getOrElse(answers, () => "-")
        const json = raw === "-" ? yield* Effect.promise(() => Bun.stdin.text()) : raw
        const decoded = yield* decodeAnswers(json).pipe(
          Effect.mapError(
            (error) => new Error(`--answers must be a JSON object: ${error.message}`),
          ),
        )
        yield* client.v1.answerThreadRequest({
          params: { threadId, requestId },
          payload: { kind: "question", answers: decoded },
        })
      }),
    ),
).pipe(
  Command.withDescription(
    "Answer a pending request: --decision for an approval, --answers (JSON, or - for stdin) for a question",
  ),
)

const threadQueue = Cli.clientCommand(
  "thread queue",
  "List the prompts still waiting to run, oldest first",
  { threadId: threadIdArgument },
  (client, { threadId }) => client.v1.listThreadQueue({ params: { threadId } }),
)

export const thread = Command.make("thread").pipe(
  Command.withDescription("Manage durable agent threads (the primary unit of work)"),
  Command.withSubcommands([
    threadList,
    threadGet,
    threadCreate,
    threadUpdate,
    threadOpen,
    threadClose,
    threadPrompt,
    threadFollow,
    threadInterrupt,
    threadTranscript,
    threadRequests,
    threadAnswer,
    threadQueue,
    threadArchive,
    threadUnarchive,
    threadDelete,
  ]),
)
