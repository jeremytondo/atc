import type { CodexThreadItem } from "@/agents/codex-adapter/protocol";
import type { AgentTurnItem } from "@/agents/contracts";
import { assertNever } from "@/core/shared";
import type { TurnItem } from "@/turns/schemas";

type SharedTurnItem = CodexThreadItem | AgentTurnItem | TurnItem;

type SharedWebSearchAction =
  | Readonly<{
      type: "search";
      query: string | null;
      queries: string[] | null;
    }>
  | Readonly<{
      type: "openPage";
      url: string | null;
    }>
  | Readonly<{
      type: "findInPage";
      url: string | null;
      pattern: string | null;
    }>
  | Readonly<{
      type: "other";
    }>;

type CloneTurnItemOptions = Readonly<{
  normalizeCommandExecutionExitCode?: (exitCode: number | null) => number | null;
}>;

export function cloneTurnItem(item: CodexThreadItem, options: CloneTurnItemOptions): AgentTurnItem;
export function cloneTurnItem(item: AgentTurnItem): AgentTurnItem;
export function cloneTurnItem(item: TurnItem): TurnItem;
export function cloneTurnItem(
  item: SharedTurnItem,
  options: CloneTurnItemOptions = {},
): AgentTurnItem | TurnItem {
  const normalizeCommandExecutionExitCode =
    options.normalizeCommandExecutionExitCode ?? ((exitCode: number | null) => exitCode);

  switch (item.type) {
    case "userMessage":
      return Object.freeze({
        type: "userMessage",
        id: item.id,
        content: [...item.content],
      });
    case "agentMessage":
      return Object.freeze({
        type: "agentMessage",
        id: item.id,
        text: item.text,
        phase: item.phase,
      });
    case "plan":
      return Object.freeze({
        type: "plan",
        id: item.id,
        text: item.text,
      });
    case "reasoning":
      return Object.freeze({
        type: "reasoning",
        id: item.id,
        summary: [...item.summary],
        content: [...item.content],
      });
    case "commandExecution":
      return Object.freeze({
        type: "commandExecution",
        id: item.id,
        command: item.command,
        cwd: item.cwd,
        processId: item.processId,
        status: item.status,
        commandActions: [...item.commandActions],
        aggregatedOutput: item.aggregatedOutput,
        exitCode: normalizeCommandExecutionExitCode(item.exitCode),
        durationMs: item.durationMs,
      });
    case "fileChange":
      return Object.freeze({
        type: "fileChange",
        id: item.id,
        changes: [...item.changes],
        status: item.status,
      });
    case "mcpToolCall":
      return Object.freeze({
        type: "mcpToolCall",
        id: item.id,
        server: item.server,
        tool: item.tool,
        status: item.status,
        arguments: item.arguments,
        result: item.result,
        error: item.error,
        durationMs: item.durationMs,
      });
    case "dynamicToolCall":
      return Object.freeze({
        type: "dynamicToolCall",
        id: item.id,
        tool: item.tool,
        arguments: item.arguments,
        status: item.status,
        contentItems: item.contentItems === null ? null : [...item.contentItems],
        success: item.success,
        durationMs: item.durationMs,
      });
    case "collabAgentToolCall":
      return Object.freeze({
        type: "collabAgentToolCall",
        id: item.id,
        tool: item.tool,
        status: item.status,
        senderThreadId: item.senderThreadId,
        receiverThreadIds: [...item.receiverThreadIds],
        prompt: item.prompt,
        agentsStates: { ...item.agentsStates },
      });
    case "webSearch":
      return Object.freeze({
        type: "webSearch",
        id: item.id,
        query: item.query,
        action: item.action === null ? null : cloneWebSearchAction(item.action),
      });
    case "imageView":
      return Object.freeze({
        type: "imageView",
        id: item.id,
        path: item.path,
      });
    case "imageGeneration":
      return Object.freeze({
        type: "imageGeneration",
        id: item.id,
        status: item.status,
        revisedPrompt: item.revisedPrompt,
        result: item.result,
      });
    case "enteredReviewMode":
      return Object.freeze({
        type: "enteredReviewMode",
        id: item.id,
        review: item.review,
      });
    case "exitedReviewMode":
      return Object.freeze({
        type: "exitedReviewMode",
        id: item.id,
        review: item.review,
      });
    case "contextCompaction":
      return Object.freeze({
        type: "contextCompaction",
        id: item.id,
      });
    default:
      return assertNever(item, "Unhandled turn item");
  }
}

const cloneWebSearchAction = (action: SharedWebSearchAction): SharedWebSearchAction => {
  switch (action.type) {
    case "search":
      return Object.freeze({
        type: "search",
        query: action.query,
        queries: action.queries === null ? null : [...action.queries],
      });
    case "openPage":
      return Object.freeze({
        type: "openPage",
        url: action.url,
      });
    case "findInPage":
      return Object.freeze({
        type: "findInPage",
        url: action.url,
        pattern: action.pattern,
      });
    case "other":
      return Object.freeze({
        type: "other",
      });
    default:
      return assertNever(action, "Unhandled web search action");
  }
};
