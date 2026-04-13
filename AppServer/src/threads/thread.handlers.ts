import type { AgentNotification, AgentSession, AgentSessionLookupError } from "@/agents/contracts";
import { createAgentSessionUnavailableError, createProviderError } from "@/agents/protocol-errors";
import type { AgentRegistry } from "@/agents/registry";
import { createAgentRequestId } from "@/agents/request-id";
import type { Logger } from "@/app/logger";
import type { ProtocolDispatcher, ProtocolEngine, ProtocolNotification } from "@/core/protocol";
import {
  createSessionNotInitializedResult,
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
import { createLoadedThreadRegistry } from "@/threads/loaded-thread-registry";
import {
  ThreadArchiveParamsSchema,
  ThreadArchiveResultSchema,
  ThreadForkParamsSchema,
  ThreadForkResultSchema,
  ThreadListParamsSchema,
  ThreadListResultSchema,
  type ThreadReadParams,
  ThreadReadParamsSchema,
  ThreadReadResultSchema,
  type ThreadResumeParams,
  ThreadResumeParamsSchema,
  ThreadResumeResultSchema,
  ThreadSetNameParamsSchema,
  ThreadSetNameResultSchema,
  type ThreadStartParams,
  ThreadStartParamsSchema,
  ThreadStartResultSchema,
  ThreadUnarchiveParamsSchema,
  ThreadUnarchiveResultSchema,
} from "@/threads/schemas";
import {
  createThreadsService,
  mapInvalidProviderPayloadToProtocolError,
  type ThreadsServiceError,
} from "@/threads/service";
import type { ThreadsStore } from "@/threads/store";
import type { Workspace } from "@/workspaces/schemas";

export type ThreadsModule = Readonly<{
  lifecycle: LifecycleComponent;
  isThreadLoadedForConnection: (
    input: Readonly<{
      connectionId: string;
      threadId: string;
    }>,
  ) => boolean;
  listLoadedThreadSubscribers: (threadId: string) => readonly string[];
  handleConnectionClosed: (connectionId: string) => void;
  handleWorkspaceOpened: (
    input: Readonly<{
      connectionId: string;
      previousWorkspace: Workspace | undefined;
      workspace: Workspace;
    }>,
  ) => void;
}>;

export type CreateThreadsModuleOptions = Readonly<{
  logger: Logger;
  registerMethod: ProtocolDispatcher["registerMethod"];
  sendNotification: ProtocolEngine["sendNotification"];
  registry: AgentRegistry;
  store: ThreadsStore;
  getOpenedWorkspace: (connectionId: string) => Workspace | undefined;
  now?: () => string;
  onThreadClosed?: (threadId: string) => Promise<void> | void;
  onSessionDisconnected?: () => Promise<void> | void;
}>;

export const createThreadsModule = (options: CreateThreadsModuleOptions): ThreadsModule => {
  const service = createThreadsService({
    logger: options.logger,
    registry: options.registry,
    store: options.store,
    now: options.now,
  });
  const loadedThreads = createLoadedThreadRegistry();
  let subscribedSession: AgentSession | undefined;
  let unsubscribeFromSession: (() => void) | undefined;

  const resetSessionSubscription = (): void => {
    unsubscribeFromSession?.();
    unsubscribeFromSession = undefined;
    subscribedSession = undefined;
  };

  const sendThreadNotification = async (
    connectionId: string,
    threadId: string | undefined,
    notification: ProtocolNotification,
  ): Promise<void> => {
    try {
      await options.sendNotification({
        connectionId,
        notification,
      });
    } catch (error) {
      options.logger.warn("Failed to send thread notification", {
        connectionId,
        threadId: threadId ?? null,
        method: notification.method,
        error: getErrorMessage(error),
      });
    }
  };

  const fanOutThreadNotification = async (
    threadId: string,
    notification: ProtocolNotification,
    clearThreadAfterSend = false,
  ): Promise<void> => {
    const connectionIds = loadedThreads.listSubscribers(threadId);
    if (connectionIds.length === 0) {
      if (clearThreadAfterSend) {
        loadedThreads.clearThread(threadId);
      }
      return;
    }

    await Promise.all(
      connectionIds.map((connectionId) =>
        sendThreadNotification(connectionId, threadId, notification),
      ),
    );

    if (clearThreadAfterSend) {
      loadedThreads.clearThread(threadId);
    }
  };

  const forwardAgentNotification = async (notification: AgentNotification): Promise<void> => {
    if (notification.type === "disconnect") {
      await options.onSessionDisconnected?.();
      loadedThreads.clearAll();
      resetSessionSubscription();
      return;
    }

    if (notification.type !== "thread" || notification.threadId === undefined) {
      return;
    }

    switch (notification.event) {
      case "statusChanged":
        await fanOutThreadNotification(notification.threadId, {
          method: "thread/status/changed",
          params: {
            threadId: notification.threadId,
            status: notification.thread.status,
          },
        });
        return;
      case "closed":
        await options.onThreadClosed?.(notification.threadId);
        await fanOutThreadNotification(
          notification.threadId,
          {
            method: "thread/closed",
            params: {
              threadId: notification.threadId,
            },
          },
          true,
        );
        return;
      case "started":
        return;
      case "archived":
        await fanOutThreadNotification(notification.threadId, {
          method: "thread/archived",
          params: {
            threadId: notification.threadId,
          },
        });
        return;
      case "unarchived":
        await fanOutThreadNotification(notification.threadId, {
          method: "thread/unarchived",
          params: {
            threadId: notification.threadId,
          },
        });
        return;
      case "nameUpdated":
        await fanOutThreadNotification(notification.threadId, {
          method: "thread/name/updated",
          params: {
            threadId: notification.threadId,
            ...(notification.threadName !== undefined
              ? { threadName: notification.threadName }
              : {}),
          },
        });
        return;
      default:
        return;
    }
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
      void forwardAgentNotification(notification);
    });

    return ok(undefined);
  };

  options.registerMethod({
    method: "thread/list",
    paramsSchema: ThreadListParamsSchema,
    resultSchema: ThreadListResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/list",
        requestId,
      });
      const result = await service.listThreads(agentRequestId, workspace, params);
      return mapThreadResult(result, "thread/list");
    },
  });

  options.registerMethod({
    method: "thread/start",
    paramsSchema: ThreadStartParamsSchema,
    resultSchema: ThreadStartResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const bindResult = await ensureSessionNotificationBinding();
      if (!bindResult.ok) {
        return err(mapThreadError(bindResult.error, "thread/start"));
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/start",
        requestId,
      });
      const result = await service.startThread(
        agentRequestId,
        workspace,
        normalizeThreadStartParams(params),
      );

      if (!result.ok) {
        return mapThreadResult(result, "thread/start");
      }

      loadedThreads.markLoaded({
        connectionId,
        workspaceId: workspace.id,
        threadId: result.data.thread.id,
      });
      void sendThreadNotification(connectionId, result.data.thread.id, {
        method: "thread/started",
        params: {
          thread: result.data.thread,
        },
      });

      return ok(result.data);
    },
  });

  options.registerMethod({
    method: "thread/resume",
    paramsSchema: ThreadResumeParamsSchema,
    resultSchema: ThreadResumeResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const bindResult = await ensureSessionNotificationBinding();
      if (!bindResult.ok) {
        return err(mapThreadError(bindResult.error, "thread/resume"));
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/resume",
        requestId,
      });
      const result = await service.resumeThread(
        agentRequestId,
        workspace,
        normalizeThreadResumeParams(params),
      );

      if (!result.ok) {
        return mapThreadResult(result, "thread/resume");
      }

      loadedThreads.markLoaded({
        connectionId,
        workspaceId: workspace.id,
        threadId: result.data.thread.id,
      });

      return ok(result.data);
    },
  });

  options.registerMethod({
    method: "thread/fork",
    paramsSchema: ThreadForkParamsSchema,
    resultSchema: ThreadForkResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const bindResult = await ensureSessionNotificationBinding();
      if (!bindResult.ok) {
        return err(mapThreadError(bindResult.error, "thread/fork"));
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/fork",
        requestId,
      });
      const result = await service.forkThread(agentRequestId, workspace, params);

      if (!result.ok) {
        return mapThreadResult(result, "thread/fork");
      }

      loadedThreads.markLoaded({
        connectionId,
        workspaceId: workspace.id,
        threadId: result.data.thread.id,
      });

      return ok(result.data);
    },
  });

  options.registerMethod({
    method: "thread/read",
    paramsSchema: ThreadReadParamsSchema,
    resultSchema: ThreadReadResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/read",
        requestId,
      });
      const result = await service.readThread(
        agentRequestId,
        workspace,
        normalizeThreadReadParams(params),
      );
      return mapThreadResult(result, "thread/read");
    },
  });

  options.registerMethod({
    method: "thread/archive",
    paramsSchema: ThreadArchiveParamsSchema,
    resultSchema: ThreadArchiveResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/archive",
        requestId,
      });
      const result = await service.archiveThread(agentRequestId, workspace, params);
      return mapThreadResult(result, "thread/archive");
    },
  });

  options.registerMethod({
    method: "thread/unarchive",
    paramsSchema: ThreadUnarchiveParamsSchema,
    resultSchema: ThreadUnarchiveResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/unarchive",
        requestId,
      });
      const result = await service.unarchiveThread(agentRequestId, workspace, params);
      return mapThreadResult(result, "thread/unarchive");
    },
  });

  options.registerMethod({
    method: "thread/name/set",
    paramsSchema: ThreadSetNameParamsSchema,
    resultSchema: ThreadSetNameResultSchema,
    handler: async ({ connectionId, params, requestId, session }) => {
      if (!session.isInitialized()) {
        return createSessionNotInitializedResult();
      }

      const workspace = options.getOpenedWorkspace(connectionId);

      if (workspace === undefined) {
        return createWorkspaceNotOpenedResult();
      }

      const agentRequestId = createAgentRequestId({
        connectionId,
        method: "thread/name/set",
        requestId,
      });
      const result = await service.setThreadName(agentRequestId, workspace, params);
      return mapThreadResult(result, "thread/name/set");
    },
  });

  return Object.freeze({
    lifecycle: Object.freeze({
      name: "module.threads",
      start: async () => {
        options.logger.info("Threads module ready");
      },
      stop: async (reason: string) => {
        loadedThreads.clearAll();
        resetSessionSubscription();
        options.logger.info("Threads module stopped", { reason });
      },
    }),
    isThreadLoadedForConnection: ({ connectionId, threadId }) =>
      loadedThreads.isLoaded({ connectionId, threadId }),
    listLoadedThreadSubscribers: (threadId) => loadedThreads.listSubscribers(threadId),
    handleConnectionClosed: (connectionId) => {
      loadedThreads.clearConnection(connectionId);
    },
    handleWorkspaceOpened: ({ connectionId, previousWorkspace, workspace }) => {
      if (previousWorkspace?.id === workspace.id) {
        return;
      }

      loadedThreads.clearConnection(connectionId);
    },
  });
};

const normalizeThreadStartParams = (params: ThreadStartParams): ThreadStartParams =>
  Object.freeze({
    ...(params.model ? { model: params.model } : {}),
    ...(params.reasoningEffort ? { reasoningEffort: params.reasoningEffort } : {}),
  });

const normalizeThreadResumeParams = (params: ThreadResumeParams): ThreadResumeParams =>
  Object.freeze({
    threadId: params.threadId,
    ...(params.model ? { model: params.model } : {}),
    ...(params.reasoningEffort ? { reasoningEffort: params.reasoningEffort } : {}),
  });

const normalizeThreadReadParams = (params: ThreadReadParams): ThreadReadParams =>
  Object.freeze({
    threadId: params.threadId,
    includeTurns: params.includeTurns ?? false,
  });

const mapThreadResult = <TResult>(
  result: Result<TResult, AgentSessionLookupError | ThreadsServiceError>,
  method: string,
): Result<TResult, ProtocolMethodError> => {
  if (result.ok) {
    return ok(result.data);
  }

  return err(mapThreadError(result.error, method));
};

const mapThreadError = (
  error: AgentSessionLookupError | ThreadsServiceError,
  method: string,
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
      return mapInvalidProviderPayloadToProtocolError(error);
    case "agentNotFound":
      throw new Error(error.message);
    default:
      return assertNever(error, `Unhandled ${method} protocol error`);
  }
};
