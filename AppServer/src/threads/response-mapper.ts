import type {
  AgentReasoningEffort,
  AgentThread,
  AgentThreadDetail,
  AgentTurnDetail,
  AgentTurnItem,
  AgentTurnStatus,
} from "@/agents/contracts";
import type { Logger } from "@/app/logger";
import { assertNever } from "@/core/shared";
import type { Thread, ThreadDetail, ThreadExecutionStatus } from "@/threads/schemas";
import type { WorkspaceThreadLink } from "@/threads/store";
import type { TurnDetail, TurnItem, TurnStatus, TurnTerminalError } from "@/turns/schemas";
import type { Workspace } from "@/workspaces/schemas";

export type ThreadDefaults = Readonly<{
  model: string | null;
  reasoningEffort: AgentReasoningEffort | null;
}>;

export const resolveThreadDefaults = (
  result: Readonly<{
    model?: string;
    reasoningEffort?: AgentReasoningEffort | null;
  }>,
  params: Readonly<{
    model?: string;
    reasoningEffort?: AgentReasoningEffort;
  }>,
  existingLink?: WorkspaceThreadLink,
): ThreadDefaults =>
  Object.freeze({
    model: params.model ?? existingLink?.model ?? result.model ?? null,
    reasoningEffort:
      params.reasoningEffort ?? existingLink?.reasoningEffort ?? result.reasoningEffort ?? null,
  });

export const getThreadDefaults = (link: WorkspaceThreadLink | undefined): ThreadDefaults =>
  Object.freeze({
    model: link?.model ?? null,
    reasoningEffort: link?.reasoningEffort ?? null,
  });

export const warnOnThreadDefaultsMismatch = (
  logger: Logger,
  input: Readonly<{
    operation: string;
    workspace: Workspace;
    threadId: string;
    providerModel?: string;
    providerReasoningEffort?: AgentReasoningEffort | null;
    defaults: ThreadDefaults;
  }>,
): void => {
  if (
    input.defaults.model === (input.providerModel ?? null) &&
    input.defaults.reasoningEffort === (input.providerReasoningEffort ?? null)
  ) {
    return;
  }

  logger.warn("Resolved thread defaults differ from provider response", {
    operation: input.operation,
    workspaceId: input.workspace.id,
    workspacePath: input.workspace.workspacePath,
    threadId: input.threadId,
    resolvedModel: input.defaults.model,
    providerModel: input.providerModel ?? null,
    resolvedReasoningEffort: input.defaults.reasoningEffort,
    providerReasoningEffort: input.providerReasoningEffort ?? null,
  });
};

export const mapPublicThread = (thread: AgentThread, defaults: ThreadDefaults): Thread =>
  Object.freeze({
    id: thread.id,
    preview: thread.preview,
    createdAt: thread.createdAt,
    updatedAt: thread.updatedAt,
    name: thread.name,
    archived: thread.archived,
    model: defaults.model,
    reasoningEffort: defaults.reasoningEffort,
    status: mapPublicThreadStatus(thread.status),
  });

export const mapPublicThreadDetail = (
  thread: AgentThreadDetail,
  defaults: ThreadDefaults,
): ThreadDetail =>
  Object.freeze({
    id: thread.id,
    preview: thread.preview,
    createdAt: thread.createdAt,
    updatedAt: thread.updatedAt,
    name: thread.name,
    archived: thread.archived,
    model: defaults.model,
    reasoningEffort: defaults.reasoningEffort,
    status: mapPublicThreadStatus(thread.status),
    turns: thread.turns.map((turn) => mapPublicTurnDetail(turn)),
  });

const mapPublicThreadStatus = (status: AgentThread["status"]): ThreadExecutionStatus => {
  switch (status.type) {
    case "notLoaded":
      return Object.freeze({ type: "notLoaded" });
    case "idle":
      return Object.freeze({ type: "idle" });
    case "active":
      return Object.freeze({
        type: "active",
        activeFlags: [...status.activeFlags],
      });
    case "systemError":
      return Object.freeze({
        type: "systemError",
        ...(status.message ? { message: status.message } : {}),
      });
    default:
      return assertNever(status, "Unhandled public thread status");
  }
};

const mapPublicTurnDetail = (turn: AgentTurnDetail): TurnDetail =>
  Object.freeze({
    id: turn.id,
    status: mapPublicTurnStatus(turn.status),
    items: turn.items.map((item) => mapPublicTurnItem(item)),
    error: mapPublicTurnTerminalError(turn.error),
  });

const mapPublicTurnTerminalError = (error: AgentTurnDetail["error"]): TurnTerminalError | null =>
  error === null
    ? null
    : Object.freeze({
        message: error.message,
        providerError: error.providerError,
        additionalDetails: error.additionalDetails,
      });

const mapPublicTurnStatus = (status: AgentTurnStatus): TurnStatus => {
  switch (status.type) {
    case "inProgress":
      return Object.freeze({ type: "inProgress" });
    case "awaitingInput":
      return Object.freeze({ type: "awaitingInput" });
    case "completed":
      return Object.freeze({ type: "completed" });
    case "cancelled":
      return Object.freeze({ type: "cancelled" });
    case "interrupted":
      return Object.freeze({ type: "interrupted" });
    case "failed":
      return Object.freeze({
        type: "failed",
        ...(status.message ? { message: status.message } : {}),
      });
    default:
      return assertNever(status, "Unhandled public turn status");
  }
};

const mapPublicTurnItem = (item: AgentTurnItem): TurnItem => {
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
        exitCode: item.exitCode,
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
        action:
          item.action === null
            ? null
            : item.action.type === "search"
              ? Object.freeze({
                  type: "search",
                  query: item.action.query,
                  queries: item.action.queries === null ? null : [...item.action.queries],
                })
              : item.action.type === "openPage"
                ? Object.freeze({
                    type: "openPage",
                    url: item.action.url,
                  })
                : item.action.type === "findInPage"
                  ? Object.freeze({
                      type: "findInPage",
                      url: item.action.url,
                      pattern: item.action.pattern,
                    })
                  : Object.freeze({
                      type: "other",
                    }),
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
      return assertNever(item, "Unhandled public turn item");
  }
};
