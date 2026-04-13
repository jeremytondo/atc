import type {
  AgentNotification,
  AgentSession,
  AgentSessionLookupError,
  AgentTurnNotification,
} from "@/agents/contracts";
import { createAgentSessionUnavailableError, createProviderError } from "@/agents/protocol-errors";
import type { AgentRegistry } from "@/agents/registry";
import { createAgentRequestId } from "@/agents/request-id";
import type { Logger } from "@/app/logger";
import type { ProtocolDispatcher, ProtocolEngine, ProtocolNotification } from "@/core/protocol";
import {
  createSessionNotInitializedResult,
  createThreadNotLoadedForConnectionError,
  createWorkspaceNotOpenedResult,
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
import { createActiveTurnRegistry } from "@/turns/active-turn-registry";
import {
  type Turn,
  TurnInterruptParamsSchema,
  type TurnInterruptResult,
  TurnInterruptResultSchema,
  type TurnStartParams,
  TurnStartParamsSchema,
  type TurnStartResult,
  TurnStartResultSchema,
  TurnSteerParamsSchema,
  type TurnSteerResult,
  TurnSteerResultSchema,
} from "@/turns/schemas";
import {
  createActiveTurnConflictProtocolError,
  createActiveTurnMismatchProtocolError,
  createActiveTurnNotFoundProtocolError,
  createTurnsService,
  mapInvalidProviderPayloadToProtocolError,
  type TurnsServiceError,
} from "@/turns/service";
import type { Workspace } from "@/workspaces/schemas";

export type LoadedThreadAccess = Readonly<{
  isThreadLoadedForConnection: (
    input: Readonly<{ connectionId: string; threadId: string }>,
  ) => boolean;
  listLoadedThreadSubscribers: (threadId: string) => readonly string[];
}>;

export type TurnsModule = Readonly<{
  lifecycle: LifecycleComponent;
}>;

export type CreateTurnsModuleOptions = Readonly<{
  logger: Logger;
  registerMethod: ProtocolDispatcher["registerMethod"];
  sendNotification: ProtocolEngine["sendNotification"];
  registry: AgentRegistry;
  getOpenedWorkspace: (connectionId: string) => Workspace | undefined;
  loadedThreads: LoadedThreadAccess;
  ensureApprovalNotificationBinding?: () => Promise<Result<void, AgentSessionLookupError>>;
  onTurnCompleted?: (input: Readonly<{ threadId: string; turnId: string }>) => Promise<void> | void;
}>;

export const createTurnsModule = (options: CreateTurnsModuleOptions): TurnsModule => {
  const activeTurns = createActiveTurnRegistry();
  const service = createTurnsService({
    logger: options.logger,
    registry: options.registry,
    activeTurns,
  });

  let subscribedSession: AgentSession | undefined;
  let unsubscribeFromSession: (() => void) | undefined;
  let notificationChain = Promise.resolve();

  const resetSessionSubscription = (): void => {
    unsubscribeFromSession?.();
    unsubscribeFromSession = undefined;
    subscribedSession = undefined;
  };

  const enqueueNotification = (notification: AgentNotification): void => {
    notificationChain = notificationChain
      .catch(() => {})
      .then(() => forwardAgentNotification(notification));
  };

  const ensureSessionNotificationBinding = async (): Promise<
    Result<void, AgentSessionLookupError>
  > => {
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
      enqueueNotification(notification);
    });

    return ok(undefined);
  };

  const sendTurnNotification = async (
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
      options.logger.warn("Failed to send turn notification", {
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
      await sendTurnNotification(connectionId, threadId, notification);
    }
  };

  const warnIgnoredTurnNotification = (
    details: Readonly<{
      type: string;
      event: string;
      threadId: string;
      turnId?: string;
      itemId?: string;
    }>,
  ): void => {
    options.logger.warn("Ignored turn notification with mismatched active turn", {
      notificationType: details.type,
      event: details.event,
      threadId: details.threadId,
      ...(details.turnId !== undefined ? { turnId: details.turnId } : {}),
      ...(details.itemId !== undefined ? { itemId: details.itemId } : {}),
      activeTurnId: activeTurns.getActiveTurn(details.threadId)?.turn?.id ?? null,
    });
  };

  const forwardAgentNotification = async (notification: AgentNotification): Promise<void> => {
    switch (notification.type) {
      case "disconnect":
        activeTurns.clearAll();
        resetSessionSubscription();
        return;
      case "thread":
        if (notification.event === "closed" && notification.threadId !== undefined) {
          activeTurns.clearThread(notification.threadId);
        }
        return;
      case "turn":
        await handleTurnNotification(notification);
        return;
      case "item":
        await handleItemNotification(notification);
        return;
      case "message": {
        if (
          notification.threadId === undefined ||
          notification.turnId === undefined ||
          notification.itemId === undefined
        ) {
          return;
        }

        const wasRecorded = activeTurns.appendMessageText({
          threadId: notification.threadId,
          turnId: notification.turnId,
          itemId: notification.itemId,
          delta: notification.delta,
        });
        if (!wasRecorded) {
          warnIgnoredTurnNotification({
            type: notification.type,
            event: notification.event,
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
          });
          return;
        }

        await fanOutThreadNotification(notification.threadId, {
          method: "item/message/textDelta",
          params: {
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
            delta: notification.delta,
          },
        });
        return;
      }
      case "reasoning": {
        if (
          notification.threadId === undefined ||
          notification.turnId === undefined ||
          notification.itemId === undefined ||
          notification.delta === undefined
        ) {
          return;
        }

        // Only text deltas are surfaced live in this phase. Summary-part payloads
        // are normalized at the adapter boundary but intentionally not relayed yet.
        if (notification.event === "textDelta") {
          const wasRecorded = activeTurns.appendReasoningText({
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
            delta: notification.delta,
          });
          if (!wasRecorded) {
            warnIgnoredTurnNotification({
              type: notification.type,
              event: notification.event,
              threadId: notification.threadId,
              turnId: notification.turnId,
              itemId: notification.itemId,
            });
            return;
          }

          await fanOutThreadNotification(notification.threadId, {
            method: "item/reasoning/textDelta",
            params: {
              threadId: notification.threadId,
              turnId: notification.turnId,
              itemId: notification.itemId,
              delta: notification.delta,
            },
          });
        } else if (notification.event === "summaryTextDelta") {
          const wasRecorded = activeTurns.appendReasoningSummaryText({
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
            delta: notification.delta,
          });
          if (!wasRecorded) {
            warnIgnoredTurnNotification({
              type: notification.type,
              event: notification.event,
              threadId: notification.threadId,
              turnId: notification.turnId,
              itemId: notification.itemId,
            });
            return;
          }

          await fanOutThreadNotification(notification.threadId, {
            method: "item/reasoning/summaryTextDelta",
            params: {
              threadId: notification.threadId,
              turnId: notification.turnId,
              itemId: notification.itemId,
              delta: notification.delta,
            },
          });
        }
        return;
      }
      case "command": {
        if (
          notification.threadId === undefined ||
          notification.turnId === undefined ||
          notification.itemId === undefined
        ) {
          return;
        }

        const wasRecorded = activeTurns.appendCommandOutput({
          threadId: notification.threadId,
          turnId: notification.turnId,
          itemId: notification.itemId,
          delta: notification.delta,
        });
        if (!wasRecorded) {
          warnIgnoredTurnNotification({
            type: notification.type,
            event: notification.event,
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
          });
          return;
        }

        await fanOutThreadNotification(notification.threadId, {
          method: "item/commandExecution/outputDelta",
          params: {
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
            delta: notification.delta,
          },
        });
        return;
      }
      case "tool": {
        if (
          notification.threadId === undefined ||
          notification.turnId === undefined ||
          notification.itemId === undefined
        ) {
          return;
        }

        const wasRecorded = activeTurns.appendToolProgress({
          threadId: notification.threadId,
          turnId: notification.turnId,
          itemId: notification.itemId,
          message: notification.message,
        });
        if (!wasRecorded) {
          warnIgnoredTurnNotification({
            type: notification.type,
            event: notification.event,
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
          });
          return;
        }

        await fanOutThreadNotification(notification.threadId, {
          method: "item/tool/progress",
          params: {
            threadId: notification.threadId,
            turnId: notification.turnId,
            itemId: notification.itemId,
            message: notification.message,
          },
        });
        return;
      }
      case "plan": {
        if (notification.threadId === undefined || notification.turnId === undefined) {
          return;
        }

        // Plan updates are intentionally live-only in this phase. We forward the
        // latest provider event to loaded-thread subscribers, but do not fold
        // plan state into active-turn snapshots until recovery/reattachment work
        // gives the turns module a durable replay surface for them.
        await fanOutThreadNotification(notification.threadId, {
          method: "turn/plan/updated",
          params: {
            threadId: notification.threadId,
            turnId: notification.turnId,
            ...(notification.explanation !== undefined
              ? { explanation: notification.explanation }
              : {}),
            steps: [...notification.steps],
          },
        });
        return;
      }
      case "diff": {
        if (notification.threadId === undefined || notification.turnId === undefined) {
          return;
        }

        // Diff updates follow the same live-only tradeoff as plan updates in
        // this phase: they are streamed to active subscribers, but not stored
        // in active-turn snapshots yet.
        await fanOutThreadNotification(notification.threadId, {
          method: "turn/diff/updated",
          params: {
            threadId: notification.threadId,
            turnId: notification.turnId,
            diff: notification.diff,
            summary: [...notification.summary],
          },
        });
        return;
      }
      case "approval":
      case "error":
        return;
      default:
        return assertNever(notification, "Unhandled agent notification");
    }
  };

  const handleTurnNotification = async (notification: AgentTurnNotification): Promise<void> => {
    if (notification.threadId === undefined) {
      return;
    }

    const turn = mapTurnSummary(notification.turn);

    switch (notification.event) {
      case "started": {
        const wasRecorded = activeTurns.startTurn({
          threadId: notification.threadId,
          turn,
        });
        if (!wasRecorded) {
          warnIgnoredTurnNotification({
            type: notification.type,
            event: notification.event,
            threadId: notification.threadId,
            turnId: notification.turnId,
          });
          return;
        }

        await fanOutThreadNotification(notification.threadId, {
          method: "turn/started",
          params: {
            threadId: notification.threadId,
            turn,
          },
        });
        return;
      }
      case "completed": {
        const wasRecorded = activeTurns.recordTurnCompleted({
          threadId: notification.threadId,
          turn,
        });
        if (!wasRecorded) {
          warnIgnoredTurnNotification({
            type: notification.type,
            event: notification.event,
            threadId: notification.threadId,
            turnId: notification.turnId,
          });
          return;
        }

        await options.onTurnCompleted?.({
          threadId: notification.threadId,
          turnId: turn.id,
        });
        activeTurns.clearThread(notification.threadId);
        await fanOutThreadNotification(notification.threadId, {
          method: "turn/completed",
          params: {
            threadId: notification.threadId,
            turn,
          },
        });
        return;
      }
      default:
        return assertNever(notification.event, "Unhandled turn notification event");
    }
  };

  const handleItemNotification = async (
    notification: Extract<AgentNotification, { type: "item" }>,
  ): Promise<void> => {
    if (notification.threadId === undefined || notification.turnId === undefined) {
      return;
    }

    const item = notification.item;

    if (notification.event === "started") {
      const wasRecorded = activeTurns.recordItemStarted({
        threadId: notification.threadId,
        turnId: notification.turnId,
        item,
      });
      if (!wasRecorded) {
        warnIgnoredTurnNotification({
          type: notification.type,
          event: notification.event,
          threadId: notification.threadId,
          turnId: notification.turnId,
          itemId: notification.item.id,
        });
        return;
      }

      await fanOutThreadNotification(notification.threadId, {
        method: "item/started",
        params: {
          threadId: notification.threadId,
          turnId: notification.turnId,
          item,
        },
      });
      return;
    }

    const wasRecorded = activeTurns.recordItemCompleted({
      threadId: notification.threadId,
      turnId: notification.turnId,
      item,
    });
    if (!wasRecorded) {
      warnIgnoredTurnNotification({
        type: notification.type,
        event: notification.event,
        threadId: notification.threadId,
        turnId: notification.turnId,
        itemId: notification.item.id,
      });
      return;
    }

    await fanOutThreadNotification(notification.threadId, {
      method: "item/completed",
      params: {
        threadId: notification.threadId,
        turnId: notification.turnId,
        item,
      },
    });
  };

  options.registerMethod({
    method: "turn/start",
    paramsSchema: TurnStartParamsSchema,
    resultSchema: TurnStartResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      if (
        !options.loadedThreads.isThreadLoadedForConnection({
          connectionId,
          threadId: params.threadId,
        })
      ) {
        return err(createThreadNotLoadedForConnectionError(params.threadId));
      }

      const bindResult = await ensureSessionNotificationBinding();
      if (!bindResult.ok) {
        return err(mapTurnError(bindResult.error));
      }

      const approvalBindResult = await options.ensureApprovalNotificationBinding?.();
      if (approvalBindResult !== undefined && !approvalBindResult.ok) {
        return err(mapTurnError(approvalBindResult.error));
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "turn/start",
        requestId,
      });
      const result = await service.startTurn(
        agentRequestId,
        workspace,
        normalizeTurnStartParams(params),
      );
      return mapTurnResult(result);
    },
  });

  options.registerMethod({
    method: "turn/steer",
    paramsSchema: TurnSteerParamsSchema,
    resultSchema: TurnSteerResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      if (
        !options.loadedThreads.isThreadLoadedForConnection({
          connectionId,
          threadId: params.threadId,
        })
      ) {
        return err(createThreadNotLoadedForConnectionError(params.threadId));
      }

      const bindResult = await ensureSessionNotificationBinding();
      if (!bindResult.ok) {
        return err(mapTurnError(bindResult.error));
      }

      const approvalBindResult = await options.ensureApprovalNotificationBinding?.();
      if (approvalBindResult !== undefined && !approvalBindResult.ok) {
        return err(mapTurnError(approvalBindResult.error));
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "turn/steer",
        requestId,
      });
      const result = await service.steerTurn(agentRequestId, params);
      return mapTurnResult(result);
    },
  });

  options.registerMethod({
    method: "turn/interrupt",
    paramsSchema: TurnInterruptParamsSchema,
    resultSchema: TurnInterruptResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      if (
        !options.loadedThreads.isThreadLoadedForConnection({
          connectionId,
          threadId: params.threadId,
        })
      ) {
        return err(createThreadNotLoadedForConnectionError(params.threadId));
      }

      const bindResult = await ensureSessionNotificationBinding();
      if (!bindResult.ok) {
        return err(mapTurnError(bindResult.error));
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "turn/interrupt",
        requestId,
      });
      const result = await service.interruptTurn(agentRequestId, params);
      return mapTurnResult(result);
    },
  });

  return Object.freeze({
    lifecycle: Object.freeze({
      name: "module.turns",
      start: async () => {
        options.logger.info("Turns module ready");
      },
      stop: async (reason: string) => {
        activeTurns.clearAll();
        resetSessionSubscription();
        await notificationChain.catch(() => {});
        options.logger.info("Turns module stopped", { reason });
      },
    }),
  });
};

const normalizeTurnStartParams = (params: TurnStartParams): TurnStartParams =>
  Object.freeze({
    threadId: params.threadId,
    prompt: params.prompt,
  });

const mapTurnSummary = (turn: AgentTurnNotification["turn"]): Turn =>
  Object.freeze({
    id: turn.id,
    status:
      turn.status.type === "failed"
        ? Object.freeze({
            type: "failed" as const,
            ...(turn.status.message ? { message: turn.status.message } : {}),
          })
        : Object.freeze({ type: turn.status.type }),
  });

const mapTurnResult = <TResult extends TurnStartResult | TurnSteerResult | TurnInterruptResult>(
  result: Result<TResult, AgentSessionLookupError | TurnsServiceError>,
): Result<TResult, ProtocolMethodError> => {
  if (result.ok) {
    return ok(result.data);
  }

  return err(mapTurnError(result.error));
};

const mapTurnError = (error: AgentSessionLookupError | TurnsServiceError): ProtocolMethodError => {
  if (isProtocolMethodError(error)) {
    return error;
  }

  switch (error.type) {
    case "sessionUnavailable":
      return createAgentSessionUnavailableError(error);
    case "remoteError":
      return createProviderError(error);
    case "invalidProviderPayload":
      return mapInvalidProviderPayloadToProtocolError(error);
    case "activeTurnConflict":
      return createActiveTurnConflictProtocolError(error);
    case "activeTurnNotFound":
      return createActiveTurnNotFoundProtocolError(error);
    case "activeTurnMismatch":
      return createActiveTurnMismatchProtocolError(error);
    case "agentNotFound":
      throw new Error(error.message);
    default:
      return assertNever(error, "Unhandled turn protocol error");
  }
};
