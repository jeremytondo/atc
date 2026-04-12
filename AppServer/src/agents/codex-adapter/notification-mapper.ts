import { Value } from "@sinclair/typebox/value";
import {
  mapCodexThreadItem,
  mapCodexThreadStatus,
  mapCodexThreadSummary,
  mapCodexTurnSummary,
} from "@/agents/codex-adapter/model-mapper";
import {
  CodexAgentMessageDeltaNotificationSchema,
  CodexCommandExecutionOutputDeltaNotificationSchema,
  CodexMcpToolCallProgressNotificationSchema,
  CodexReasoningSummaryTextDeltaNotificationSchema,
  CodexReasoningTextDeltaNotificationSchema,
  CodexThreadItemSchema,
} from "@/agents/codex-adapter/protocol";
import type {
  CodexTransportDisconnectInfo,
  CodexTransportNotification,
  CodexTransportServerRequest,
} from "@/agents/codex-adapter/transport";
import type {
  AgentApprovalNotification,
  AgentApprovalRequest,
  AgentApprovalResolution,
  AgentDiffFileSummary,
  AgentNotification,
  AgentThreadSummary,
} from "@/agents/contracts";

type NotificationContext = Readonly<{
  agentId: string;
  provider: "codex";
  receivedAt?: string;
}>;

type PendingApprovalState = Readonly<{
  approval: AgentApprovalRequest;
  resolution?: AgentApprovalResolution;
}>;

export const mapCodexTransportNotification = (
  notification: CodexTransportNotification,
  context: NotificationContext,
): readonly AgentNotification[] => {
  const receivedAt = context.receivedAt ?? new Date().toISOString();
  const base = {
    agentId: context.agentId,
    provider: context.provider,
    receivedAt,
    rawMethod: notification.method,
    rawPayload: notification.params,
  } as const;
  const params = isPlainObject(notification.params) ? notification.params : null;

  switch (notification.method) {
    case "thread/started":
      return params && isPlainObject(params.thread)
        ? [
            {
              ...base,
              type: "thread",
              event: "started",
              threadId: typeof params.thread.id === "string" ? params.thread.id : undefined,
              thread: mapCodexThreadSummary(params.thread as never),
            },
          ]
        : [buildMalformedMessage(base, "thread/started")];
    case "thread/status/changed":
      return params && typeof params.threadId === "string" && isPlainObject(params.status)
        ? [
            {
              ...base,
              type: "thread",
              event: "statusChanged",
              threadId: params.threadId,
              thread: buildPartialThreadSummary(
                params.threadId,
                receivedAt,
                false,
                mapCodexThreadStatus(params.status as never),
              ),
            },
          ]
        : [buildMalformedMessage(base, "thread/status/changed")];
    case "thread/archived":
      return params && typeof params.threadId === "string"
        ? [
            {
              ...base,
              type: "thread",
              event: "archived",
              threadId: params.threadId,
              thread: buildPartialThreadSummary(params.threadId, receivedAt, true, {
                type: "notLoaded",
              }),
            },
          ]
        : [buildMalformedMessage(base, "thread/archived")];
    case "thread/unarchived":
      return params && typeof params.threadId === "string"
        ? [
            {
              ...base,
              type: "thread",
              event: "unarchived",
              threadId: params.threadId,
              thread: buildPartialThreadSummary(params.threadId, receivedAt, false, {
                type: "notLoaded",
              }),
            },
          ]
        : [buildMalformedMessage(base, "thread/unarchived")];
    case "thread/name/updated":
      return params &&
        typeof params.threadId === "string" &&
        (params.threadName === undefined ||
          typeof params.threadName === "string" ||
          params.threadName === null)
        ? [
            {
              ...base,
              type: "thread",
              event: "nameUpdated",
              threadId: params.threadId,
              ...(params.threadName !== undefined ? { threadName: params.threadName } : {}),
              thread: buildPartialThreadSummary(
                params.threadId,
                receivedAt,
                false,
                { type: "notLoaded" },
                params.threadName ?? null,
              ),
            },
          ]
        : [buildMalformedMessage(base, "thread/name/updated")];
    case "thread/closed":
      return params && typeof params.threadId === "string"
        ? [
            {
              ...base,
              type: "thread",
              event: "closed",
              threadId: params.threadId,
              thread: buildPartialThreadSummary(params.threadId, receivedAt, false, {
                type: "notLoaded",
              }),
            },
          ]
        : [buildMalformedMessage(base, "thread/closed")];
    case "turn/started":
      return params &&
        typeof params.threadId === "string" &&
        isPlainObject(params.turn) &&
        typeof params.turn.id === "string"
        ? [
            {
              ...base,
              type: "turn",
              event: "started",
              threadId: params.threadId,
              turnId: params.turn.id,
              turn: mapCodexTurnSummary(params.turn as never),
            },
          ]
        : [buildMalformedMessage(base, "turn/started")];
    case "turn/completed":
      return params &&
        typeof params.threadId === "string" &&
        isPlainObject(params.turn) &&
        typeof params.turn.id === "string"
        ? [
            {
              ...base,
              type: "turn",
              event: "completed",
              threadId: params.threadId,
              turnId: params.turn.id,
              turn: mapCodexTurnSummary(params.turn as never),
            },
          ]
        : [buildMalformedMessage(base, "turn/completed")];
    case "item/started":
    case "item/completed":
      return params &&
        typeof params.threadId === "string" &&
        typeof params.turnId === "string" &&
        Value.Check(CodexThreadItemSchema, params.item)
        ? [
            {
              ...base,
              type: "item",
              event: notification.method === "item/started" ? "started" : "completed",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.item.id,
              item: mapCodexThreadItem(params.item),
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "item/agentMessage/delta":
      return params && Value.Check(CodexAgentMessageDeltaNotificationSchema, params)
        ? [
            {
              ...base,
              type: "message",
              event: "textDelta",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.itemId,
              delta: params.delta,
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "item/reasoning/summaryTextDelta":
      return params && Value.Check(CodexReasoningSummaryTextDeltaNotificationSchema, params)
        ? [
            {
              ...base,
              type: "reasoning",
              event: "summaryTextDelta",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.itemId,
              delta: params.delta,
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "item/reasoning/textDelta":
      return params && Value.Check(CodexReasoningTextDeltaNotificationSchema, params)
        ? [
            {
              ...base,
              type: "reasoning",
              event: "textDelta",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.itemId,
              delta: params.delta,
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "item/commandExecution/outputDelta":
      return params && Value.Check(CodexCommandExecutionOutputDeltaNotificationSchema, params)
        ? [
            {
              ...base,
              type: "command",
              event: "outputDelta",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.itemId,
              delta: params.delta,
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "item/mcpToolCall/progress":
      return params && Value.Check(CodexMcpToolCallProgressNotificationSchema, params)
        ? [
            {
              ...base,
              type: "tool",
              event: "progress",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.itemId,
              message: params.message,
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "item/reasoning/summaryPartAdded":
      return params &&
        typeof params.threadId === "string" &&
        typeof params.turnId === "string" &&
        typeof params.itemId === "string"
        ? [
            {
              ...base,
              type: "reasoning",
              event: "summaryPartAdded",
              threadId: params.threadId,
              turnId: params.turnId,
              itemId: params.itemId,
              summaryPart: params,
            },
          ]
        : [buildMalformedMessage(base, notification.method)];
    case "turn/plan/updated":
      return params &&
        typeof params.threadId === "string" &&
        typeof params.turnId === "string" &&
        Array.isArray(params.plan)
        ? [
            {
              ...base,
              type: "plan",
              event: "updated",
              threadId: params.threadId,
              turnId: params.turnId,
              ...(typeof params.explanation === "string"
                ? { explanation: params.explanation }
                : {}),
              steps: params.plan.flatMap((step) => {
                if (
                  !isPlainObject(step) ||
                  typeof step.step !== "string" ||
                  typeof step.status !== "string"
                ) {
                  return [];
                }

                return [
                  {
                    step: step.step,
                    status: mapPlanStepStatus(step.status),
                  },
                ];
              }),
            },
          ]
        : [buildMalformedMessage(base, "turn/plan/updated")];
    case "turn/diff/updated":
      return params &&
        typeof params.threadId === "string" &&
        typeof params.turnId === "string" &&
        typeof params.diff === "string"
        ? [
            {
              ...base,
              type: "diff",
              event: "updated",
              threadId: params.threadId,
              turnId: params.turnId,
              diff: params.diff,
              summary: summarizeDiff(params.diff),
            },
          ]
        : [buildMalformedMessage(base, "turn/diff/updated")];
    case "error":
      return params && typeof params.error === "object" && params.error !== null
        ? [
            {
              ...base,
              type: "error",
              code:
                isPlainObject(params.error) && typeof params.error.code === "string"
                  ? params.error.code
                  : "provider_error",
              message:
                isPlainObject(params.error) && typeof params.error.message === "string"
                  ? params.error.message
                  : "Codex emitted an error notification.",
              detail: params.error,
              threadId: typeof params.threadId === "string" ? params.threadId : undefined,
              turnId: typeof params.turnId === "string" ? params.turnId : undefined,
            },
          ]
        : [buildMalformedMessage(base, "error")];
    case "serverRequest/resolved":
      return [];
    default:
      return [];
  }
};

export const mapCodexServerRequest = (
  request: CodexTransportServerRequest,
  context: NotificationContext,
): readonly AgentNotification[] => {
  const receivedAt = context.receivedAt ?? new Date().toISOString();
  const base = {
    agentId: context.agentId,
    provider: context.provider,
    receivedAt,
    rawMethod: request.method,
    rawPayload: request.params,
  } as const;
  const params = isPlainObject(request.params) ? request.params : null;

  switch (request.method) {
    case "item/commandExecution/requestApproval":
    case "item/fileChange/requestApproval":
    case "mcpServer/elicitation/request":
      if (params === null) {
        return [buildMalformedMessage(base, request.method)];
      }

      return [
        {
          ...base,
          type: "approval",
          event: "requested",
          requestId: request.id,
          threadId: typeof params.threadId === "string" ? params.threadId : undefined,
          turnId: typeof params.turnId === "string" ? params.turnId : undefined,
          itemId: typeof params.itemId === "string" ? params.itemId : undefined,
          approval: {
            requestId: request.id,
            kind: mapApprovalKind(request.method),
            threadId: typeof params.threadId === "string" ? params.threadId : undefined,
            turnId: typeof params.turnId === "string" ? params.turnId : undefined,
            itemId: typeof params.itemId === "string" ? params.itemId : undefined,
            rawRequest: request,
          },
        },
      ];
    default:
      return [
        {
          ...base,
          type: "error",
          code: "unsupported_provider_request",
          message: `Codex server request ${request.method} is not supported by the App Server yet.`,
          detail: { requestId: request.id, method: request.method },
        },
      ];
  }
};

export const mapCodexResolvedApproval = (
  requestId: string | number,
  pendingApproval: PendingApprovalState | undefined,
  context: NotificationContext,
): AgentApprovalNotification | null => {
  if (pendingApproval === undefined) {
    return null;
  }

  return {
    agentId: context.agentId,
    provider: context.provider,
    receivedAt: context.receivedAt ?? new Date().toISOString(),
    rawMethod: "serverRequest/resolved",
    threadId: pendingApproval.approval.threadId,
    turnId: pendingApproval.approval.turnId,
    itemId: pendingApproval.approval.itemId,
    type: "approval",
    event: "resolved",
    requestId,
    approval: pendingApproval.approval,
    resolution: pendingApproval.resolution ?? "stale",
  };
};

export const mapCodexDisconnectNotification = (
  disconnect: CodexTransportDisconnectInfo,
  context: NotificationContext,
): AgentNotification => ({
  agentId: context.agentId,
  provider: context.provider,
  receivedAt: context.receivedAt ?? new Date().toISOString(),
  rawMethod: "disconnect",
  type: "disconnect",
  reason: disconnect.reason,
  message: disconnect.message,
  ...(disconnect.exitCode !== undefined ? { exitCode: disconnect.exitCode } : {}),
  ...(disconnect.detail ? { detail: disconnect.detail } : {}),
});

const buildMalformedMessage = (
  base: Readonly<{
    agentId: string;
    provider: "codex";
    receivedAt: string;
    rawMethod: string;
    rawPayload?: unknown;
    threadId?: string;
    turnId?: string;
    itemId?: string;
  }>,
  method: string,
): AgentNotification => ({
  ...base,
  type: "error",
  code: "malformed_provider_notification",
  message: `Codex notification ${method} did not match the expected shape.`,
  detail: base.rawPayload,
});

const buildPartialThreadSummary = (
  threadId: string,
  updatedAt: string,
  archived: boolean,
  status: AgentThreadSummary["status"],
  name: string | null = null,
): AgentThreadSummary =>
  Object.freeze({
    id: threadId,
    preview: "",
    updatedAt,
    name,
    archived,
    status,
  });

const mapApprovalKind = (method: string): AgentApprovalRequest["kind"] => {
  switch (method) {
    case "item/commandExecution/requestApproval":
      return "commandExecution";
    case "item/fileChange/requestApproval":
      return "fileChange";
    case "mcpServer/elicitation/request":
      return "mcpElicitation";
    default:
      return "unknown";
  }
};

const summarizeDiff = (diff: string): readonly AgentDiffFileSummary[] => {
  const filesByPath = new Map<string, { additions: number; deletions: number }>();
  let currentPath: string | null = null;

  for (const line of diff.split("\n")) {
    if (line.startsWith("diff --git ")) {
      const match = /^diff --git a\/(.+?) b\/(.+)$/.exec(line);
      currentPath = match?.[2] ?? null;
      if (currentPath !== null && !filesByPath.has(currentPath)) {
        filesByPath.set(currentPath, { additions: 0, deletions: 0 });
      }
      continue;
    }

    if (currentPath === null || line.startsWith("+++ ") || line.startsWith("--- ")) {
      continue;
    }

    const summary = filesByPath.get(currentPath);
    if (summary === undefined) {
      continue;
    }

    if (line.startsWith("+")) {
      summary.additions += 1;
    } else if (line.startsWith("-")) {
      summary.deletions += 1;
    }
  }

  return [...filesByPath.entries()].map(([path, counts]) =>
    Object.freeze({
      path,
      additions: counts.additions,
      deletions: counts.deletions,
    }),
  );
};

const isPlainObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const mapPlanStepStatus = (value: string): "pending" | "in_progress" | "completed" => {
  switch (value) {
    case "completed":
      return "completed";
    case "inProgress":
      return "in_progress";
    default:
      return "pending";
  }
};
