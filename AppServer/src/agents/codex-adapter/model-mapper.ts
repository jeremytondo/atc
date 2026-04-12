import type {
  CodexModel,
  CodexThread,
  CodexThreadItem,
  CodexThreadStatus,
  CodexTurn,
  CodexTurnDetail,
  CodexTurnError,
} from "@/agents/codex-adapter/protocol";
import type {
  AgentModelSummary,
  AgentThread,
  AgentThreadDetail,
  AgentThreadExecutionStatus,
  AgentThreadSummary,
  AgentTurnDetail,
  AgentTurnItem,
  AgentTurnStatus,
  AgentTurnSummary,
  AgentTurnTerminalError,
} from "@/agents/contracts";

export const mapCodexModelSummary = (model: CodexModel): AgentModelSummary =>
  Object.freeze({
    id: model.id,
    model: model.model,
    displayName: model.displayName,
    hidden: model.hidden,
    defaultReasoningEffort: model.defaultReasoningEffort ?? undefined,
    supportedReasoningEfforts: model.supportedReasoningEfforts.map((effort) =>
      Object.freeze({
        reasoningEffort: effort.reasoningEffort,
        description: effort.description,
      }),
    ),
    inputModalities: model.inputModalities ? [...model.inputModalities] : undefined,
    supportsPersonality: model.supportsPersonality,
    isDefault: model.isDefault === true,
  });

export const mapCodexThreadStatus = (status: CodexThreadStatus): AgentThreadExecutionStatus => {
  switch (status.type) {
    case "notLoaded":
      return Object.freeze({ type: "notLoaded" });
    case "idle":
      return Object.freeze({ type: "idle" });
    case "systemError":
      return Object.freeze({
        type: "systemError",
        ...(status.error?.message ? { message: status.error.message } : {}),
      });
    case "active":
      return Object.freeze({
        type: "active",
        activeFlags: [...status.activeFlags],
      });
    default:
      return assertNever(status);
  }
};

export const mapCodexThreadSummary = (
  thread: CodexThread,
  options: { archived?: boolean } = {},
): AgentThreadSummary => mapAgentThreadSummary(mapCodexThread(thread, options));

export const mapCodexThread = (
  thread: CodexThread,
  options: { archived?: boolean } = {},
): AgentThread =>
  Object.freeze({
    id: thread.id,
    preview: thread.preview,
    createdAt: mapUnixTimestampToIso(thread.createdAt, "createdAt"),
    updatedAt: mapUnixTimestampToIso(thread.updatedAt, "updatedAt"),
    workspacePath: thread.cwd,
    name: thread.name,
    archived: options.archived ?? false,
    status: mapCodexThreadStatus(thread.status),
  });

export const mapCodexThreadDetail = (
  thread: CodexThread,
  options: { archived?: boolean } = {},
): AgentThreadDetail =>
  Object.freeze({
    ...mapCodexThread(thread, options),
    // Codex omits `turns` on responses that do not carry history.
    turns: (thread.turns ?? []).map((turn) => mapCodexTurnDetail(turn)),
  });

export const mapCodexTurnStatus = (turn: CodexTurn): AgentTurnStatus => {
  switch (turn.status) {
    case "completed":
      return Object.freeze({ type: "completed" });
    case "interrupted":
      return Object.freeze({ type: "interrupted" });
    case "failed":
      return Object.freeze({
        type: "failed",
        ...(turn.error?.message ? { message: turn.error.message } : {}),
      });
    case "inProgress":
      return Object.freeze({ type: "inProgress" });
    default:
      return assertNever(turn.status);
  }
};

export const mapCodexTurnSummary = (turn: CodexTurn): AgentTurnSummary =>
  Object.freeze({
    id: turn.id,
    status: mapCodexTurnStatus(turn),
  });

export const mapCodexTurnDetail = (turn: CodexTurnDetail): AgentTurnDetail =>
  Object.freeze({
    id: turn.id,
    status: mapCodexTurnStatus(turn),
    items: turn.items.map((item) => mapCodexThreadItem(item)),
    error: mapCodexTurnError(turn.error),
  });

export const mapCodexThreadItem = (item: CodexThreadItem): AgentTurnItem => {
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
        exitCode: item.exitCode === null ? null : Math.trunc(item.exitCode),
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
      return assertNever(item);
  }
};

const mapAgentThreadSummary = (thread: AgentThread): AgentThreadSummary =>
  Object.freeze({
    id: thread.id,
    preview: thread.preview,
    updatedAt: thread.updatedAt,
    name: thread.name,
    archived: thread.archived,
    status: thread.status,
  });

const assertNever = (value: never): never => {
  throw new Error(`Unhandled Codex mapping variant: ${JSON.stringify(value)}`);
};

const mapCodexTurnError = (
  error: CodexTurnError | null | undefined,
): AgentTurnTerminalError | null =>
  error === undefined || error === null
    ? null
    : Object.freeze({
        message: error.message,
        providerError: error.codexErrorInfo,
        additionalDetails: error.additionalDetails,
      });

const mapUnixTimestampToIso = (value: number, fieldName: "createdAt" | "updatedAt"): string => {
  if (!Number.isFinite(value) || value < 0) {
    throw new Error(`Codex thread ${fieldName} must be a non-negative unix timestamp.`);
  }

  return new Date(value * 1_000).toISOString();
};
