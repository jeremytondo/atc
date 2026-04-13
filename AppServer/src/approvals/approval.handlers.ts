import type {
  AgentApprovalRequest,
  AgentNotification,
  AgentSession,
  AgentSessionLookupError,
} from "@/agents/contracts";
import {
  createAgentNotFoundProtocolError,
  createAgentSessionUnavailableError,
  createProviderError,
} from "@/agents/protocol-errors";
import type { AgentRegistry } from "@/agents/registry";
import type { Logger } from "@/app/logger";
import { type ApprovalRegistryRecord, createApprovalRegistry } from "@/approvals/approval-registry";
import {
  type ApprovalRequest,
  ApprovalResolveParamsSchema,
  type ApprovalResolveResult,
  ApprovalResolveResultSchema,
} from "@/approvals/schemas";
import {
  type ApprovalsServiceError,
  createApprovalsService,
  mapInvalidApprovalProviderPayloadToProtocolError,
} from "@/approvals/service";
import type { ProtocolDispatcher, ProtocolEngine, ProtocolNotification } from "@/core/protocol";
import {
  createApprovalNotPendingError,
  createSessionNotInitializedResult,
  createThreadNotLoadedForConnectionError,
  createUnsupportedApprovalResolutionError,
  isProtocolMethodError,
  type ProtocolMethodError,
} from "@/core/protocol/errors";
import {
  assertNever,
  err,
  getErrorMessage,
  type LifecycleComponent,
  ok,
  type Result,
} from "@/core/shared";

export type LoadedThreadAccess = Readonly<{
  isThreadLoadedForConnection: (
    input: Readonly<{ connectionId: string; threadId: string }>,
  ) => boolean;
  listLoadedThreadSubscribers: (threadId: string) => readonly string[];
}>;

export type ApprovalsModule = Readonly<{
  lifecycle: LifecycleComponent;
  ensureNotificationBinding: () => Promise<Result<void, AgentSessionLookupError>>;
  handleTurnCompleted: (input: Readonly<{ threadId: string; turnId: string }>) => Promise<void>;
  handleThreadClosed: (threadId: string) => Promise<void>;
  handleSessionDisconnected: () => Promise<void>;
}>;

export type CreateApprovalsModuleOptions = Readonly<{
  logger: Logger;
  registerMethod: ProtocolDispatcher["registerMethod"];
  sendNotification: ProtocolEngine["sendNotification"];
  registry: AgentRegistry;
  loadedThreads: LoadedThreadAccess;
}>;

export const createApprovalsModule = (options: CreateApprovalsModuleOptions): ApprovalsModule => {
  const approvals = createApprovalRegistry();
  const service = createApprovalsService({
    registry: options.registry,
    approvals,
  });

  let subscribedSession: AgentSession | undefined;
  let unsubscribeFromSession: (() => void) | undefined;
  let notificationChain = Promise.resolve();

  const resetSessionSubscription = (): void => {
    unsubscribeFromSession?.();
    unsubscribeFromSession = undefined;
    subscribedSession = undefined;
  };

  const sendApprovalNotification = async (
    connectionId: string,
    threadId: string,
    notification: ProtocolNotification,
  ): Promise<void> => {
    try {
      await options.sendNotification({
        connectionId,
        notification,
      });
    } catch (error) {
      options.logger.warn("Failed to send approval notification", {
        connectionId,
        threadId,
        method: notification.method,
        error: getErrorMessage(error),
      });
    }
  };

  const fanOutThreadNotification = async (
    threadId: string,
    notification: ProtocolNotification,
  ): Promise<void> => {
    const connectionIds = options.loadedThreads.listLoadedThreadSubscribers(threadId);
    if (connectionIds.length === 0) {
      return;
    }

    for (const connectionId of connectionIds) {
      await sendApprovalNotification(connectionId, threadId, notification);
    }
  };

  const emitResolvedNotification = async (
    approval: ApprovalRequest,
    resolution: "approved" | "approvedForSession" | "declined" | "cancelled" | "stale",
  ): Promise<void> => {
    await fanOutThreadNotification(approval.threadId, {
      method: "approval/resolved",
      params: {
        threadId: approval.threadId,
        approval,
        resolution,
      },
    });
  };

  const emitStaleResolutions = async (
    clearedRecords: readonly ApprovalRegistryRecord[],
  ): Promise<void> => {
    for (const record of clearedRecords) {
      if (record.state !== "pending") {
        continue;
      }

      await emitResolvedNotification(record.approval, "stale");
    }
  };

  const forwardAgentNotification = async (notification: AgentNotification): Promise<void> => {
    if (notification.type !== "approval") {
      return;
    }

    switch (notification.event) {
      case "requested": {
        if (notification.approval.threadId === undefined) {
          return;
        }

        const approval = mapAgentApprovalRequest({
          ...notification.approval,
          threadId: notification.approval.threadId,
        });
        approvals.recordRequested(approval);
        await fanOutThreadNotification(approval.threadId, {
          method: "approval/requested",
          params: {
            threadId: approval.threadId,
            approval,
          },
        });
        return;
      }
      case "resolved": {
        const clearedRecord = approvals.clearRequest(notification.requestId);
        if (clearedRecord === undefined || clearedRecord.state === "resolved") {
          return;
        }

        await emitResolvedNotification(clearedRecord.approval, notification.resolution ?? "stale");
        return;
      }
      default:
        return;
    }
  };

  const ensureNotificationBinding = async (): Promise<Result<void, AgentSessionLookupError>> => {
    const sessionResult = await options.registry.getSession();
    if (!sessionResult.ok) {
      return err(sessionResult.error);
    }

    if (subscribedSession === sessionResult.data) {
      return ok(undefined);
    }

    resetSessionSubscription();
    subscribedSession = sessionResult.data;
    unsubscribeFromSession = sessionResult.data.subscribe((notification) => {
      notificationChain = notificationChain
        .catch(() => {})
        .then(() => forwardAgentNotification(notification));
    });

    return ok(undefined);
  };

  options.registerMethod({
    method: "approval/resolve",
    paramsSchema: ApprovalResolveParamsSchema,
    resultSchema: ApprovalResolveResultSchema,
    handler: async ({ connectionId, params, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const pendingApproval = approvals.getPending(params.requestId);
      if (
        pendingApproval !== undefined &&
        !options.loadedThreads.isThreadLoadedForConnection({
          connectionId,
          threadId: pendingApproval.approval.threadId,
        })
      ) {
        return err(createThreadNotLoadedForConnectionError(pendingApproval.approval.threadId));
      }

      const bindResult = await ensureNotificationBinding();
      if (!bindResult.ok) {
        return err(mapApprovalError(bindResult.error));
      }

      const result = await service.resolveApproval(params);
      if (!result.ok) {
        return err(mapApprovalError(result.error));
      }

      await emitResolvedNotification(result.data.approval, result.data.resolution);

      const resolveResult: ApprovalResolveResult = {
        requestId: result.data.requestId,
        resolution: result.data.resolution,
      };
      return ok(resolveResult);
    },
  });

  return Object.freeze({
    lifecycle: Object.freeze({
      name: "module.approvals",
      start: async () => {
        options.logger.info("Approvals module ready");
      },
      stop: async (reason: string) => {
        approvals.clearAll();
        resetSessionSubscription();
        await notificationChain.catch(() => {});
        options.logger.info("Approvals module stopped", { reason });
      },
    }),
    ensureNotificationBinding,
    handleTurnCompleted: async ({ threadId, turnId }) => {
      await emitStaleResolutions(approvals.clearTurn({ threadId, turnId }));
    },
    handleThreadClosed: async (threadId) => {
      await emitStaleResolutions(approvals.clearThread(threadId));
    },
    handleSessionDisconnected: async () => {
      await emitStaleResolutions(approvals.clearAll());
      resetSessionSubscription();
    },
  });
};

const mapAgentApprovalRequest = (
  approval: AgentApprovalRequest & Readonly<{ threadId: string }>,
): ApprovalRequest => {
  const base = {
    requestId: approval.requestId,
    threadId: approval.threadId,
    turnId: approval.turnId ?? null,
    itemId: approval.itemId ?? null,
    supportedResolutions: [...approval.supportedResolutions],
  };

  switch (approval.kind) {
    case "commandExecution":
      return Object.freeze({
        ...base,
        kind: "commandExecution" as const,
        approvalId: approval.approvalId,
        reason: approval.reason,
        command: approval.command,
        cwd: approval.cwd,
        commandActions: approval.commandActions === null ? null : [...approval.commandActions],
      });
    case "fileChange":
      return Object.freeze({
        ...base,
        kind: "fileChange" as const,
        reason: approval.reason,
        grantRoot: approval.grantRoot,
      });
    case "mcpElicitation":
      return Object.freeze({
        ...base,
        kind: "mcpElicitation" as const,
        serverName: approval.serverName,
        mode: approval.mode,
        message: approval.message,
        requestedSchema: approval.requestedSchema,
        url: approval.url,
        elicitationId: approval.elicitationId,
        metadata: approval.metadata,
      });
    case "unknown":
      return Object.freeze({
        ...base,
        kind: "unknown" as const,
      });
    default:
      return assertNever(approval, "Unhandled approval request");
  }
};

const mapApprovalError = (
  error: AgentSessionLookupError | ApprovalsServiceError,
): ProtocolMethodError => {
  if (isProtocolMethodError(error)) {
    return error;
  }

  switch (error.type) {
    case "sessionUnavailable":
      return createAgentSessionUnavailableError(error);
    case "remoteError":
      return createProviderError(error);
    case "invalidProviderPayload":
      return mapInvalidApprovalProviderPayloadToProtocolError(error);
    case "approvalNotPending":
      return createApprovalNotPendingError(error.requestId);
    case "unsupportedApprovalResolution":
      return createUnsupportedApprovalResolutionError(
        error.requestId,
        error.resolution,
        error.supportedResolutions,
      );
    case "agentNotFound":
      return createAgentNotFoundProtocolError(error.agentId);
    default:
      return assertNever(error, "Unhandled approval protocol error");
  }
};
