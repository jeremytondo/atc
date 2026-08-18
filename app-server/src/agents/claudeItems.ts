import type {
  PermissionResult,
  PermissionUpdate,
  SessionMessage,
} from "@anthropic-ai/claude-agent-sdk"
import { Option, Schema } from "effect"
import * as path from "node:path"
import type {
  ApprovalSubject,
  FileChange,
  HistoryTurn,
  ThreadItem,
  ThreadRequest,
  ThreadRequestAnswer,
} from "./agentAdapter.ts"
import { fileChangeTitle, toolOutcome } from "./agentAdapter.ts"

// Claude Agent SDK shapes → the contract vocabulary (ATC-193): pure functions
// over assistant/user message content blocks, `canUseTool` callbacks, and
// `getSessionMessages` history. No SDK calls, no state — claudeAdapter.ts
// owns those. Invariants:
//
//   - Tool items are keyed by the tool_use id (stable live and in history);
//     text and thinking blocks are keyed `${messageKey}:${blockIndex}` where
//     the key is the API message id while streaming and the SDK message
//     uuid otherwise. History ids therefore differ from live ids for text —
//     accepted: the Thread runtime replaces its copy wholesale on a re-read.
//   - Tool names decide the item shape: Bash → command; Edit / Write /
//     MultiEdit / NotebookEdit → fileChange; `mcp__<server>__<tool>` →
//     mcpCall; everything else → toolCall. A tool_result completes its item;
//     `tool_use_result` (live only — history drops it) supplies the Write
//     create-vs-update kind and the structured patch rendered as a diff.
//   - History is grouped into turns at each top-level user message that
//     carries text (tool_result-only and synthetic messages never open a
//     turn); the turn id is that message's uuid. A turn whose tool_use never
//     got a result reads as interrupted. getSessionMessages exposes system
//     entries without their subtype (probed 2026-08-17, still true at SDK
//     0.3.235), so compaction markers come only from the live
//     compact_boundary message.
//   - AskUserQuestion is a question request whose answers go back through
//     `updatedInput.answers` keyed by question text (what the tool expects);
//     every other canUseTool is an approval answered allow/deny, with
//     `acceptForSession` returning the SDK's own session-scoped suggestions.

const FILE_TOOLS = new Set(["Edit", "Write", "MultiEdit", "NotebookEdit"])
const MCP_PREFIX = /^mcp__([^_]+(?:_[^_]+)*)__(.+)$/

const TextBlock = Schema.Struct({ type: Schema.Literal("text"), text: Schema.String })
const ThinkingBlock = Schema.Struct({ type: Schema.Literal("thinking"), thinking: Schema.String })
const ToolUseBlock = Schema.Struct({
  type: Schema.Literal("tool_use"),
  id: Schema.String,
  name: Schema.String,
  input: Schema.Unknown,
})
const ToolResultBlock = Schema.Struct({
  type: Schema.Literal("tool_result"),
  tool_use_id: Schema.String,
  content: Schema.optional(Schema.Unknown),
  is_error: Schema.optional(Schema.Boolean),
})
const ContentBlock = Schema.Union([TextBlock, ThinkingBlock, ToolUseBlock, ToolResultBlock])
type ContentBlock = typeof ContentBlock.Type
const decodeBlock = Schema.decodeUnknownOption(ContentBlock)

/** The recognized blocks of a message `content` (string content is one text block). */
export const contentBlocks = (content: unknown): Array<ContentBlock> => {
  if (typeof content === "string") return content === "" ? [] : [{ type: "text", text: content }]
  if (!Array.isArray(content)) return []
  return content.flatMap((raw) => Option.toArray(decodeBlock(raw)))
}

/** Plain text of a tool_result `content` (string, or text blocks joined). */
const resultText = (content: unknown): string => {
  if (typeof content === "string") return content
  if (!Array.isArray(content)) return ""
  return content
    .flatMap((block) => {
      const candidate = block as { type?: unknown; text?: unknown }
      return candidate.type === "text" && typeof candidate.text === "string" ? [candidate.text] : []
    })
    .join("\n")
}

const record = (value: unknown): Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}

const stringField = (input: Record<string, unknown>, key: string): string | undefined => {
  const value = input[key]
  return typeof value === "string" ? value : undefined
}

/** A one-line title for any tool call, from the fields users recognize. */
export const toolTitle = (name: string, input: Record<string, unknown>): string => {
  const file = stringField(input, "file_path") ?? stringField(input, "notebook_path")
  switch (name) {
    case "Bash":
      return (stringField(input, "command") ?? "").split("\n")[0]?.trim() || "Bash"
    case "Read":
    case "Edit":
    case "Write":
    case "MultiEdit":
    case "NotebookEdit":
      return file === undefined ? name : `${name} ${path.basename(file)}`
    case "Glob":
    case "Grep":
      return stringField(input, "pattern") === undefined ? name : `${name} ${input["pattern"]}`
    case "Task":
    case "Agent":
      return stringField(input, "description") ?? name
    case "WebFetch":
    case "WebSearch":
      return stringField(input, "url") ?? stringField(input, "query") ?? name
    default: {
      const mcp = MCP_PREFIX.exec(name)
      return mcp === null ? name : `${mcp[1]} · ${mcp[2]}`
    }
  }
}

const fileChangesFor = (input: Record<string, unknown>): Array<FileChange> => {
  const file = stringField(input, "file_path") ?? stringField(input, "notebook_path")
  // Write overwrites as readily as it creates; the tool result says which
  // (`type: "create"`), so until it lands every change is an "update".
  return file === undefined ? [] : [{ path: file, kind: "update" }]
}

/**
 * The item a tool_use block opens. `pending` marks a call whose permission
 * request is already parked (canUseTool can land before the block).
 */
export const toolItem = (
  block: { readonly id: string; readonly name: string; readonly input: unknown },
  turnId: string,
  parentItemId: string | null,
  status: "pending" | "running",
): ThreadItem => {
  const input = record(block.input)
  const common = {
    id: block.id,
    turnId,
    ...(parentItemId !== null ? { parentItemId } : {}),
    title: toolTitle(block.name, input),
    ...toolOutcome(status, null),
  }
  if (block.name === "Bash") {
    const cwd = stringField(input, "cwd")
    return {
      type: "command",
      ...common,
      command: stringField(input, "command") ?? "",
      ...(cwd !== undefined ? { cwd } : {}),
    }
  }
  if (FILE_TOOLS.has(block.name)) {
    const changes = fileChangesFor(input)
    return {
      type: "fileChange",
      ...common,
      title: changes.length === 0 ? common.title : fileChangeTitle(changes),
      changes,
    }
  }
  const mcp = MCP_PREFIX.exec(block.name)
  if (mcp !== null) {
    return { type: "mcpCall", ...common, server: mcp[1]!, tool: mcp[2]!, arguments: block.input }
  }
  return { type: "toolCall", ...common, name: block.name, input: block.input }
}

// A structured patch is the `diff` library's hunk list; rendered back to
// unified-diff hunks so clients get one diff format from both providers.
const StructuredPatch = Schema.Array(
  Schema.Struct({
    oldStart: Schema.Number,
    oldLines: Schema.Number,
    newStart: Schema.Number,
    newLines: Schema.Number,
    lines: Schema.Array(Schema.String),
  }),
)
const decodeStructuredPatch = Schema.decodeUnknownOption(StructuredPatch)

const renderPatch = (patch: typeof StructuredPatch.Type): string =>
  patch
    .map(
      (hunk) =>
        `@@ -${hunk.oldStart},${hunk.oldLines} +${hunk.newStart},${hunk.newLines} @@\n` +
        hunk.lines.join("\n"),
    )
    .join("\n")

/**
 * Complete a tool item from its tool_result block plus the SDK's structured
 * `tool_use_result` (absent in history). Unknown item shapes pass through
 * with only their status updated.
 */
export const completeToolItem = (
  item: ThreadItem,
  result: { readonly content?: unknown; readonly is_error?: boolean | undefined },
  toolUseResult: unknown,
): ThreadItem => {
  const text = resultText(result.content)
  const failed = result.is_error === true
  const lifecycle = toolOutcome("completed", failed ? text || "tool failed" : null)
  switch (item.type) {
    case "command":
      return { ...item, ...lifecycle, ...(text !== "" ? { output: text } : {}) }
    case "fileChange": {
      const structured = record(toolUseResult)
      const patch = decodeStructuredPatch(structured["structuredPatch"])
      const diff =
        Option.isSome(patch) && patch.value.length > 0 ? renderPatch(patch.value) : undefined
      const changes: Array<FileChange> = item.changes.map((change) => ({
        ...change,
        ...(structured["type"] === "create" ? { kind: "add" as const } : {}),
        ...(diff !== undefined ? { diff } : {}),
      }))
      return { ...item, ...lifecycle, changes, title: fileChangeTitle(changes) }
    }
    case "mcpCall":
      return {
        ...item,
        ...lifecycle,
        ...(result.content !== undefined ? { result: result.content } : {}),
      }
    case "toolCall":
      return {
        ...item,
        ...lifecycle,
        ...(toolUseResult !== undefined
          ? { output: toolUseResult }
          : text !== ""
            ? { output: text }
            : {}),
      }
    default:
      return item
  }
}

/** A text or thinking item; streaming items start with `complete: false`. */
export const textItem = (
  id: string,
  turnId: string,
  kind: "text" | "thinking",
  text: string,
  complete: boolean,
  parentItemId: string | null,
): ThreadItem => ({
  type: kind === "text" ? "assistantText" : "reasoning",
  id,
  turnId,
  ...(parentItemId !== null ? { parentItemId } : {}),
  text,
  complete,
})

// --- Requests ---------------------------------------------------------------

const AskUserQuestionInput = Schema.Struct({
  questions: Schema.Array(
    Schema.Struct({
      question: Schema.String,
      header: Schema.optional(Schema.String),
      options: Schema.optional(
        Schema.Array(
          Schema.Struct({ label: Schema.String, description: Schema.optional(Schema.String) }),
        ),
      ),
      multiSelect: Schema.optional(Schema.Boolean),
    }),
  ),
})
const decodeAskUserQuestion = Schema.decodeUnknownOption(AskUserQuestionInput)

/** The canUseTool callback options the mapping reads (a subset of the SDK's). */
interface PermissionContext {
  readonly requestId: string
  readonly toolUseID: string
  readonly title?: string | undefined
  readonly decisionReason?: string | undefined
}

/** Normalize one canUseTool call into a ThreadRequest. */
export const mapRequest = (
  toolName: string,
  input: Record<string, unknown>,
  context: PermissionContext,
  turnId: string,
  openedAt: string,
): ThreadRequest => {
  const common = { id: context.requestId, turnId, itemId: context.toolUseID, openedAt }
  if (toolName === "AskUserQuestion") {
    const decoded = decodeAskUserQuestion(input)
    const questions = Option.isSome(decoded) ? decoded.value.questions : []
    return {
      kind: "question",
      ...common,
      // Question ids are minted from position; the answer maps them back to
      // the question text the tool keys its answers by.
      questions: questions.map((question, index) => ({
        id: `q${index}`,
        header: question.header ?? "",
        question: question.question,
        options: (question.options ?? []).map((option) => ({
          label: option.label,
          description: option.description ?? "",
        })),
        multiSelect: question.multiSelect ?? false,
        freeform: true,
        secret: false,
      })),
    }
  }
  const subject: ApprovalSubject =
    toolName === "Bash"
      ? {
          type: "command",
          command: stringField(input, "command") ?? "",
          ...(stringField(input, "cwd") !== undefined ? { cwd: stringField(input, "cwd")! } : {}),
        }
      : FILE_TOOLS.has(toolName)
        ? { type: "fileChange", changes: fileChangesFor(input) }
        : { type: "tool", name: toolName, input }
  return {
    kind: "approval",
    ...common,
    title: context.title ?? toolTitle(toolName, input),
    ...(context.decisionReason !== undefined ? { reason: context.decisionReason } : {}),
    subject,
  }
}

/** The PermissionResult that answers `request` — kinds already matched. */
export const permissionResult = (
  request: ThreadRequest,
  answer: ThreadRequestAnswer,
  input: Record<string, unknown>,
  suggestions: ReadonlyArray<PermissionUpdate>,
): PermissionResult => {
  if (answer.kind === "question" && request.kind === "question") {
    const answers = Object.fromEntries(
      request.questions.flatMap((question) => {
        const chosen = answer.answers[question.id]
        return chosen === undefined ? [] : [[question.question, chosen.join(", ")]]
      }),
    )
    return { behavior: "allow", updatedInput: { ...input, answers } }
  }
  if (answer.kind !== "approval") return { behavior: "deny", message: "declined" }
  switch (answer.decision) {
    case "accept":
      return { behavior: "allow" }
    case "acceptForSession": {
      const updatedPermissions: Array<PermissionUpdate> =
        suggestions.length > 0
          ? [...suggestions]
          : request.kind === "approval" && request.subject.type === "tool"
            ? [
                {
                  type: "addRules",
                  rules: [{ toolName: request.subject.name }],
                  behavior: "allow",
                  destination: "session",
                },
              ]
            : []
      return { behavior: "allow", updatedPermissions }
    }
    case "decline":
      return { behavior: "deny", message: "declined" }
    case "cancel":
      return { behavior: "deny", message: "cancelled", interrupt: true }
  }
}

// --- History ----------------------------------------------------------------

/** The user-text test that opens a turn: string content, or a text block. */
const userText = (content: unknown): string | null => {
  const blocks = contentBlocks(content)
  const texts = blocks.flatMap((block) => (block.type === "text" ? [block.text] : []))
  return texts.length === 0 ? null : texts.join("\n")
}

const messageTimestamp = (message: SessionMessage): string | undefined => {
  const value = (message as { timestamp?: unknown }).timestamp
  return typeof value === "string" ? value : undefined
}

/** getSessionMessages output → HistoryTurn[] (see the header's grouping rule). */
export const mapHistory = (messages: ReadonlyArray<SessionMessage>): ReadonlyArray<HistoryTurn> => {
  interface OpenTurn {
    readonly id: string
    readonly startedAt: string | undefined
    endedAt: string | undefined
    readonly items: Array<ThreadItem>
    readonly tools: Map<string, number>
  }
  const turns: Array<OpenTurn> = []
  let current: OpenTurn | null = null
  for (const message of messages) {
    if (message.parent_tool_use_id !== null) continue
    const content = (message.message as { content?: unknown } | null)?.content
    const stamp = messageTimestamp(message)
    if (message.type === "user") {
      const text = userText(content)
      if (text !== null) {
        current = {
          id: message.uuid,
          startedAt: stamp,
          endedAt: stamp,
          items: [],
          tools: new Map(),
        }
        turns.push(current)
        current.items.push({
          type: "userMessage",
          id: `${message.uuid}:0`,
          turnId: current.id,
          text,
        })
      }
      if (current === null) continue
      current.endedAt = stamp ?? current.endedAt
      for (const block of contentBlocks(content)) {
        if (block.type !== "tool_result") continue
        const index = current.tools.get(block.tool_use_id)
        if (index === undefined) continue
        current.items[index] = completeToolItem(current.items[index]!, block, undefined)
      }
      continue
    }
    if (message.type !== "assistant" || current === null) continue
    current.endedAt = stamp ?? current.endedAt
    contentBlocks(content).forEach((block, index) => {
      if (current === null) return
      const id = `${message.uuid}:${index}`
      switch (block.type) {
        case "text":
          current.items.push(textItem(id, current.id, "text", block.text, true, null))
          return
        case "thinking":
          current.items.push(textItem(id, current.id, "thinking", block.thinking, true, null))
          return
        case "tool_use":
          current.tools.set(block.id, current.items.length)
          current.items.push(toolItem(block, current.id, null, "running"))
          return
        default:
          return
      }
    })
  }
  return turns.map((turn) => {
    // A tool call that never got its result is a turn cut short.
    const unfinished = turn.items.some((item) => "status" in item && item.status === "running")
    return {
      turn: {
        id: turn.id,
        status: unfinished ? "interrupted" : "completed",
        ...(turn.startedAt !== undefined ? { startedAt: turn.startedAt } : {}),
        ...(turn.endedAt !== undefined ? { endedAt: turn.endedAt } : {}),
      },
      items: turn.items.map((item) =>
        "status" in item && item.status === "running"
          ? { ...item, ...toolOutcome("completed", "no result recorded") }
          : item,
      ),
    }
  })
}
