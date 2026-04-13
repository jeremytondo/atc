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
import { cloneTurnItem } from "@/turns/item-mapper";
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

const mapPublicTurnItem = (item: AgentTurnItem): TurnItem => cloneTurnItem(item);
