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
import { cloneTurnItem } from "@/turns/item-mapper";

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

export const mapCodexThreadItem = (item: CodexThreadItem): AgentTurnItem =>
  cloneTurnItem(item, {
    normalizeCommandExecutionExitCode: (exitCode) =>
      exitCode === null ? null : Math.trunc(exitCode),
  });

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
