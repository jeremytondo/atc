import { Option, Schema } from "effect"
import * as path from "node:path"
import type {
  HistoryTurn,
  ThreadItem,
  ThreadRequest,
  ThreadRequestAnswer,
  ThreadTurn,
} from "./agentAdapter.ts"
import { fileChangeTitle, toolOutcome } from "./agentAdapter.ts"

// Codex wire shapes → the contract vocabulary (ATC-193): pure functions over
// the codex 0.147 app-server protocol (`ThreadItem` from item/started,
// item/completed, and thread/resume turns; the item/* server requests). No
// transport, no state — codexAdapter.ts owns those. Every decoder is
// tolerant: an unrecognized or drifted payload is a silent skip (null), the
// same rule the adapter applies to every unknown notification, so a new
// Codex item type can never break a feed. Invariants:
//
//   - Item ids are Codex's own (`item.id`), turn ids the Codex turn id.
//     Live ids are the provider's message/exec ids; thread/resume history
//     re-numbers items positionally (`item-1`, `item-2`, … — probed live
//     2026-08-17 on 0.147) and omits some tool items (an `exec` custom tool
//     call recorded in the rollout did not come back as a commandExecution).
//     The Thread runtime therefore treats a re-read as a wholesale replacement, never a
//     merge by id.
//   - hookPrompt and the review-mode markers are deliberately not emitted;
//     every other unknown or exotic item type surfaces as a `toolCall`
//     named after the Codex type, so nothing the agent did disappears.
//   - Reasoning text is the SUMMARY (joined); raw reasoning content rides
//     providerMetadata. Live deltas therefore follow summaryTextDelta only.

const nullable = <S extends Schema.Top>(schema: S) => Schema.optional(Schema.NullOr(schema))

const ToolStatus = Schema.Literals(["inProgress", "completed", "failed", "declined"])

const codexToolStatus = (
  status: typeof ToolStatus.Type,
  failure: string | null,
): ReturnType<typeof toolOutcome> =>
  toolOutcome(
    status === "inProgress" ? "running" : status === "declined" ? "declined" : "completed",
    status === "failed" ? (failure ?? "failed") : failure,
  )

const ItemBase = Schema.Struct({ type: Schema.String, id: Schema.String })
const decodeItemBase = Schema.decodeUnknownOption(ItemBase)

const UserMessage = Schema.Struct({
  content: Schema.Array(
    Schema.Struct({ type: Schema.String, text: Schema.optional(Schema.String) }),
  ),
})
const AgentMessage = Schema.Struct({ text: Schema.String, phase: nullable(Schema.String) })
const Plan = Schema.Struct({ text: Schema.String })
const Reasoning = Schema.Struct({
  summary: Schema.Array(Schema.String),
  content: Schema.Array(Schema.String),
})
const CommandExecution = Schema.Struct({
  command: Schema.String,
  cwd: nullable(Schema.String),
  status: ToolStatus,
  aggregatedOutput: nullable(Schema.String),
  exitCode: nullable(Schema.Number),
  durationMs: nullable(Schema.Number),
})
const FileChange = Schema.Struct({
  changes: Schema.Array(
    Schema.Struct({
      path: Schema.String,
      kind: Schema.Struct({ type: Schema.Literals(["add", "delete", "update"]) }),
      diff: nullable(Schema.String),
    }),
  ),
  status: ToolStatus,
})
const McpToolCall = Schema.Struct({
  server: Schema.String,
  tool: Schema.String,
  status: ToolStatus,
  arguments: Schema.Unknown,
  result: Schema.optional(Schema.Unknown),
  error: nullable(Schema.Struct({ message: Schema.String })),
})
// The rest of the tool-like items share only an optional lifecycle status;
// their remaining fields become the toolCall input verbatim.
const GenericTool = Schema.Struct({ status: Schema.optional(Schema.Unknown) })

const decoders = {
  userMessage: Schema.decodeUnknownOption(UserMessage),
  agentMessage: Schema.decodeUnknownOption(AgentMessage),
  plan: Schema.decodeUnknownOption(Plan),
  reasoning: Schema.decodeUnknownOption(Reasoning),
  commandExecution: Schema.decodeUnknownOption(CommandExecution),
  fileChange: Schema.decodeUnknownOption(FileChange),
  mcpToolCall: Schema.decodeUnknownOption(McpToolCall),
  generic: Schema.decodeUnknownOption(GenericTool),
}

const NOT_EMITTED = new Set(["hookPrompt", "enteredReviewMode", "exitedReviewMode"])

/** Item lifecycle phase the notification carries; history reads are final. */
type Phase = "started" | "completed"

/** A short display title for the exotic tool-like items. */
const genericTitle = (type: string, input: Record<string, unknown>): string => {
  switch (type) {
    case "webSearch":
      return typeof input["query"] === "string" ? `Search: ${input["query"]}` : "Web search"
    case "imageView":
      return typeof input["path"] === "string"
        ? `View ${path.basename(input["path"])}`
        : "View image"
    case "subAgentActivity":
      return typeof input["kind"] === "string" ? `Subagent ${input["kind"]}` : "Subagent"
    case "collabAgentToolCall":
      return typeof input["tool"] === "string" ? `Agent ${input["tool"]}` : "Agent"
    case "dynamicToolCall":
      return typeof input["tool"] === "string" ? input["tool"] : type
    default:
      return type
  }
}

/**
 * Normalize one Codex item. `phase` decides the lifecycle of items that
 * carry no status of their own (text completes with the notification;
 * status-less tools run until completed).
 */
export const mapItem = (raw: unknown, turnId: string, phase: Phase): ThreadItem | null => {
  const base = decodeItemBase(raw)
  if (Option.isNone(base) || NOT_EMITTED.has(base.value.type)) return null
  const { id, type } = base.value
  const common = { id, turnId }
  switch (type) {
    case "userMessage": {
      const decoded = decoders.userMessage(raw)
      if (Option.isNone(decoded)) return null
      const text = decoded.value.content
        .flatMap((block) => (block.type === "text" && block.text !== undefined ? [block.text] : []))
        .join("\n")
      return { type: "userMessage", ...common, text }
    }
    case "agentMessage": {
      const decoded = decoders.agentMessage(raw)
      if (Option.isNone(decoded)) return null
      const phaseTag = decoded.value.phase
      return {
        type: "assistantText",
        ...common,
        text: decoded.value.text,
        complete: phase === "completed",
        ...(typeof phaseTag === "string"
          ? { providerMetadata: { codex: { phase: phaseTag } } }
          : {}),
      }
    }
    case "plan": {
      const decoded = decoders.plan(raw)
      if (Option.isNone(decoded)) return null
      return {
        type: "assistantText",
        ...common,
        text: decoded.value.text,
        complete: phase === "completed",
        providerMetadata: { codex: { plan: true } },
      }
    }
    case "reasoning": {
      const decoded = decoders.reasoning(raw)
      if (Option.isNone(decoded)) return null
      const { summary, content } = decoded.value
      return {
        type: "reasoning",
        ...common,
        text: (summary.length > 0 ? summary : content).join("\n\n"),
        complete: phase === "completed",
        ...(content.length > 0 && summary.length > 0
          ? { providerMetadata: { codex: { content } } }
          : {}),
      }
    }
    case "commandExecution": {
      const decoded = decoders.commandExecution(raw)
      if (Option.isNone(decoded)) return null
      const value = decoded.value
      const output = value.aggregatedOutput ?? undefined
      return {
        type: "command",
        ...common,
        title: value.command.split("\n")[0]?.trim() ?? value.command,
        ...codexToolStatus(value.status, null),
        command: value.command,
        ...(typeof value.cwd === "string" ? { cwd: value.cwd } : {}),
        ...(output !== undefined ? { output } : {}),
        ...(typeof value.exitCode === "number" ? { exitCode: value.exitCode } : {}),
        ...(typeof value.durationMs === "number"
          ? { providerMetadata: { codex: { durationMs: value.durationMs } } }
          : {}),
      }
    }
    case "fileChange": {
      const decoded = decoders.fileChange(raw)
      if (Option.isNone(decoded)) return null
      const changes = decoded.value.changes.map((change) => ({
        path: change.path,
        kind: change.kind.type,
        ...(typeof change.diff === "string" ? { diff: change.diff } : {}),
      }))
      return {
        type: "fileChange",
        ...common,
        title: fileChangeTitle(changes),
        ...codexToolStatus(decoded.value.status, null),
        changes,
      }
    }
    case "mcpToolCall": {
      const decoded = decoders.mcpToolCall(raw)
      if (Option.isNone(decoded)) return null
      const value = decoded.value
      return {
        type: "mcpCall",
        ...common,
        title: `${value.server} · ${value.tool}`,
        ...codexToolStatus(value.status, value.error?.message ?? null),
        server: value.server,
        tool: value.tool,
        arguments: value.arguments,
        ...(value.result !== undefined && value.result !== null ? { result: value.result } : {}),
      }
    }
    case "contextCompaction":
      return { type: "compaction", ...common }
    default: {
      const decoded = decoders.generic(raw)
      if (Option.isNone(decoded)) return null
      const { type: _type, id: _id, status, ...input } = raw as Record<string, unknown>
      const known = Schema.decodeUnknownOption(ToolStatus)(status)
      const lifecycle = Option.isSome(known)
        ? codexToolStatus(known.value, null)
        : toolOutcome(phase === "completed" ? "completed" : "running", null)
      return {
        type: "toolCall",
        ...common,
        title: genericTitle(type, input),
        ...lifecycle,
        name: type,
        input,
      }
    }
  }
}

// --- Server requests -------------------------------------------------------

const RequestParams = Schema.Struct({
  turnId: Schema.String,
  itemId: Schema.optional(Schema.String),
})
const CommandApproval = Schema.Struct({
  command: nullable(Schema.String),
  cwd: nullable(Schema.String),
  reason: nullable(Schema.String),
})
const FileChangeApproval = Schema.Struct({ reason: nullable(Schema.String) })
const UserInput = Schema.Struct({
  questions: Schema.Array(
    Schema.Struct({
      id: Schema.String,
      header: Schema.String,
      question: Schema.String,
      isOther: Schema.Boolean,
      isSecret: Schema.Boolean,
      options: nullable(
        Schema.Array(Schema.Struct({ label: Schema.String, description: Schema.String })),
      ),
    }),
  ),
})
const decodeRequestParams = Schema.decodeUnknownOption(RequestParams)
const decodeCommandApproval = Schema.decodeUnknownOption(CommandApproval)
const decodeFileChangeApproval = Schema.decodeUnknownOption(FileChangeApproval)
const decodeUserInput = Schema.decodeUnknownOption(UserInput)

/**
 * Normalize one server request into a ThreadRequest, or null for a method
 * the seam does not surface (permissions, MCP elicitation, tool calls —
 * those keep the adapter's conservative reject). `pendingItem` is the live
 * item the request names, when the adapter has it: a fileChange approval
 * carries no changes of its own, so the pending item supplies them.
 */
export const mapRequest = (
  method: string,
  requestId: string,
  params: unknown,
  pendingItem: ThreadItem | undefined,
  openedAt: string,
): ThreadRequest | null => {
  const base = decodeRequestParams(params)
  if (Option.isNone(base)) return null
  const common = {
    id: requestId,
    turnId: base.value.turnId,
    ...(base.value.itemId !== undefined ? { itemId: base.value.itemId } : {}),
    openedAt,
  }
  switch (method) {
    case "item/commandExecution/requestApproval": {
      const decoded = decodeCommandApproval(params)
      if (Option.isNone(decoded)) return null
      const command =
        decoded.value.command ??
        (pendingItem?.type === "command" ? pendingItem.command : undefined) ??
        ""
      const cwd =
        decoded.value.cwd ?? (pendingItem?.type === "command" ? pendingItem.cwd : undefined)
      return {
        kind: "approval",
        ...common,
        title: command === "" ? "Run a command" : (command.split("\n")[0]?.trim() ?? command),
        ...(typeof decoded.value.reason === "string" ? { reason: decoded.value.reason } : {}),
        subject: { type: "command", command, ...(cwd !== undefined ? { cwd } : {}) },
      }
    }
    case "item/fileChange/requestApproval": {
      const decoded = decodeFileChangeApproval(params)
      if (Option.isNone(decoded)) return null
      const changes = pendingItem?.type === "fileChange" ? pendingItem.changes : []
      return {
        kind: "approval",
        ...common,
        title: changes.length === 0 ? "Apply file changes" : fileChangeTitle(changes),
        ...(typeof decoded.value.reason === "string" ? { reason: decoded.value.reason } : {}),
        subject: { type: "fileChange", changes },
      }
    }
    case "item/tool/requestUserInput": {
      const decoded = decodeUserInput(params)
      if (Option.isNone(decoded)) return null
      return {
        kind: "question",
        ...common,
        questions: decoded.value.questions.map((question) => ({
          id: question.id,
          header: question.header,
          question: question.question,
          options: question.options ?? [],
          multiSelect: false,
          // No options at all means a typed answer is the only answer.
          freeform: question.isOther || question.options === null || question.options === undefined,
          secret: question.isSecret,
        })),
      }
    }
    default:
      return null
  }
}

/** The JSON-RPC `result` that answers `request` — kinds already matched. */
export const answerResult = (answer: ThreadRequestAnswer): unknown =>
  answer.kind === "approval"
    ? { decision: answer.decision }
    : {
        answers: Object.fromEntries(
          Object.entries(answer.answers).map(([id, answers]) => [id, { answers }]),
        ),
      }

// --- History ---------------------------------------------------------------

const HistoryTurns = Schema.Array(
  Schema.Struct({
    id: Schema.String,
    items: Schema.Array(Schema.Unknown),
    status: Schema.Literals(["inProgress", "completed", "interrupted", "failed"]),
    error: nullable(Schema.Struct({ message: Schema.String })),
    startedAt: nullable(Schema.Number),
    completedAt: nullable(Schema.Number),
  }),
)
export const decodeHistoryTurns = Schema.decodeUnknownEffect(HistoryTurns)

/** Codex stamps turns in unix seconds; the wire wants ISO. */
const isoTimestamp = (value: number | null | undefined): string | undefined =>
  typeof value === "number" && Number.isFinite(value)
    ? new Date(value * 1000).toISOString()
    : undefined

/** thread/resume turns → HistoryTurn[] (unix-second stamps become ISO). */
export const mapHistory = (turns: typeof HistoryTurns.Type): ReadonlyArray<HistoryTurn> =>
  turns.map((turn) => {
    const startedAt = isoTimestamp(turn.startedAt)
    const endedAt = isoTimestamp(turn.completedAt)
    const status: ThreadTurn["status"] = turn.status === "inProgress" ? "running" : turn.status
    return {
      turn: {
        id: turn.id,
        status,
        ...(status === "failed" && turn.error ? { error: turn.error.message } : {}),
        ...(startedAt !== undefined ? { startedAt } : {}),
        ...(endedAt !== undefined ? { endedAt } : {}),
      },
      items: turn.items.flatMap((raw) => {
        const item = mapItem(raw, turn.id, "completed")
        return item === null ? [] : [item]
      }),
    }
  })
