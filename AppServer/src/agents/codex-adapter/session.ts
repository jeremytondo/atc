import {
  mapCodexModelSummary,
  mapCodexThread,
  mapCodexThreadDetail,
  mapCodexTurnSummary,
} from "@/agents/codex-adapter/model-mapper";
import {
  mapCodexDisconnectNotification,
  mapCodexResolvedApproval,
  mapCodexServerRequest,
  mapCodexTransportNotification,
} from "@/agents/codex-adapter/notification-mapper";
import type {
  CodexAskForApproval,
  CodexClientNotification,
  CodexCommandExecutionApprovalDecision,
  CodexFileChangeApprovalDecision,
  CodexInitializeParams,
  CodexMcpServerElicitationRequestResponse,
  CodexModelListParams,
  CodexThreadArchiveParams,
  CodexThreadForkParams,
  CodexThreadListParams,
  CodexThreadReadParams,
  CodexThreadResumeParams,
  CodexThreadSetNameParams,
  CodexThreadStartParams,
  CodexThreadUnarchiveParams,
  CodexTurnInterruptParams,
  CodexTurnStartParams,
  CodexTurnSteerParams,
  CodexUserInput,
} from "@/agents/codex-adapter/protocol";
import {
  parseCodexConfiguredThreadResponse,
  parseCodexEmptyResponse,
  parseCodexInitializeResponse,
  parseCodexModelListResponse,
  parseCodexThreadListResponse,
  parseCodexThreadResponse,
  parseCodexTurnInterruptResponse,
  parseCodexTurnResponse,
  parseCodexTurnSteerResponse,
} from "@/agents/codex-adapter/protocol";
import {
  CodexAppServerTransport,
  type CodexTransport,
  type CodexTransportDisconnectInfo,
  CodexTransportError,
  CodexTransportRemoteError,
  type CodexTransportResponse,
} from "@/agents/codex-adapter/transport";
import type { CodexAgentConfig } from "@/agents/config";
import type {
  AgentApprovalRequest,
  AgentApprovalResolveParams,
  AgentApprovalResolveResult,
  AgentDisconnectReason,
  AgentExecutableDiscovery,
  AgentListModelsParams,
  AgentListModelsResult,
  AgentListThreadsParams,
  AgentListThreadsResult,
  AgentNotification,
  AgentOperationError,
  AgentOperationResult,
  AgentRequestId,
  AgentSession,
  AgentSessionLookupError,
  AgentSessionUnavailableError,
  AgentThreadArchiveParams,
  AgentThreadForkParams,
  AgentThreadMutationResult,
  AgentThreadReadParams,
  AgentThreadReadResult,
  AgentThreadResult,
  AgentThreadResumeParams,
  AgentThreadSetNameParams,
  AgentThreadStartParams,
  AgentThreadUnarchiveParams,
  AgentTurnInterruptParams,
  AgentTurnResult,
  AgentTurnStartParams,
  AgentTurnSteerParams,
} from "@/agents/contracts";
import { BaseEnvironmentResolver } from "@/agents/environment";
import { discoverExecutable } from "@/agents/executable-discovery";
import type { Logger } from "@/app/logger";
import { err, ok, type Result } from "@/core/shared";

const CODEX_INITIALIZE_REQUEST_ID = "atelier-appserver-initialize";
const CODEX_CLIENT_NAME = "AtelierCode App Server";
const CODEX_CLIENT_VERSION = "0.1.0";

export type CreateCodexAgentSessionOptions = Readonly<{
  agentId: string;
  config: CodexAgentConfig;
  logger: Logger;
  transport?: CodexTransport;
  environmentResolver?: BaseEnvironmentResolver;
}>;

export const createCodexAgentSession = async (
  options: CreateCodexAgentSessionOptions,
): Promise<Result<AgentSession, AgentSessionLookupError>> => {
  const environmentResolver =
    options.environmentResolver ??
    new BaseEnvironmentResolver({
      inheritedEnvironment: applyCodexEnvironmentOverrides(process.env, options.config),
    });
  const resolvedEnvironment = await environmentResolver.resolve();
  const executable = await discoverExecutable(
    {
      executableName: "codex",
      overrideEnvironmentVariable: "ATELIERCODE_CODEX_PATH",
    },
    {
      environment: resolvedEnvironment.environment,
      baseEnvironmentSource: resolvedEnvironment.diagnostics.source,
    },
  );

  if (executable.status !== "found") {
    return err({
      type: "sessionUnavailable",
      agentId: options.agentId,
      provider: "codex",
      code: "executableMissing",
      message: "Codex executable could not be discovered for this agent session.",
      executable,
      environment: resolvedEnvironment.diagnostics,
      detail: {
        checkedPaths: executable.checkedPaths,
      },
    });
  }

  const transport =
    options.transport ??
    new CodexAppServerTransport({
      executable,
      environment: resolvedEnvironment.environment,
    });

  try {
    await transport.connect();
  } catch (error) {
    return err(
      normalizeSessionStartupError(options.agentId, executable, resolvedEnvironment, error),
    );
  }

  return ok(
    createConnectedSession({
      agentId: options.agentId,
      logger: options.logger,
      transport,
      executable,
      environment: resolvedEnvironment,
    }),
  );
};

type ConnectedSessionOptions = Readonly<{
  agentId: string;
  logger: Logger;
  transport: CodexTransport;
  executable: AgentExecutableDiscovery;
  environment: Awaited<ReturnType<BaseEnvironmentResolver["resolve"]>>;
}>;

const createConnectedSession = (options: ConnectedSessionOptions): AgentSession => {
  const listeners = new Set<(notification: AgentNotification) => void>();
  const approvalsByRequestId = new Map<
    string,
    { approval: AgentApprovalRequest; resolution?: AgentApprovalResolveParams["resolution"] }
  >();
  let state: AgentSession["getState"] extends () => infer T ? T : never = "idle";
  let initializePromise: Promise<Result<void, AgentOperationError>> | null = null;
  let initialized = false;
  let latestDisconnect: CodexTransportDisconnectInfo | null = null;

  const emit = (notification: AgentNotification): void => {
    if (notification.type === "disconnect") {
      state = "disconnected";
      latestDisconnect = {
        reason: notification.reason,
        message: notification.message,
        ...(notification.exitCode !== undefined ? { exitCode: notification.exitCode } : {}),
        ...(notification.detail !== undefined ? { detail: notification.detail } : {}),
      };
      const disconnectListeners = [...listeners];
      for (const listener of disconnectListeners) {
        listener(notification);
      }
      approvalsByRequestId.clear();
      listeners.clear();
      return;
    }

    for (const listener of listeners) {
      listener(notification);
    }
  };

  options.transport.subscribe((event) => {
    switch (event.type) {
      case "notification": {
        if (event.notification.method === "serverRequest/resolved") {
          const params =
            typeof event.notification.params === "object" && event.notification.params !== null
              ? (event.notification.params as Record<string, unknown>)
              : null;
          const requestId = getRequestId(params?.requestId);

          if (requestId !== null) {
            const approval = approvalsByRequestId.get(requestIdKey(requestId));
            approvalsByRequestId.delete(requestIdKey(requestId));
            const resolvedApproval = mapCodexResolvedApproval(requestId, approval, {
              agentId: options.agentId,
              provider: "codex",
            });
            if (resolvedApproval !== null) {
              emit(resolvedApproval);
            }
          }
          return;
        }

        for (const notification of mapCodexTransportNotification(event.notification, {
          agentId: options.agentId,
          provider: "codex",
        })) {
          emit(notification);
        }
        return;
      }
      case "serverRequest":
        for (const notification of mapCodexServerRequest(event.request, {
          agentId: options.agentId,
          provider: "codex",
        })) {
          if (notification.type === "approval" && notification.event === "requested") {
            approvalsByRequestId.set(requestIdKey(notification.requestId), {
              approval: notification.approval,
            });
          }
          emit(notification);
        }
        return;
      case "disconnect":
        emit(
          mapCodexDisconnectNotification(event.disconnect, {
            agentId: options.agentId,
            provider: "codex",
          }),
        );
    }
  });

  const ensureInitialized = async (): Promise<Result<void, AgentOperationError>> => {
    if (initialized) {
      return ok(undefined);
    }

    if (initializePromise !== null) {
      return initializePromise;
    }

    state = "connecting";
    const pendingInitialization: Promise<Result<void, AgentOperationError>> = (async () => {
      try {
        const initializeParams: CodexInitializeParams = {
          clientInfo: {
            name: CODEX_CLIENT_NAME,
            title: null,
            version: CODEX_CLIENT_VERSION,
          },
          capabilities: {
            experimentalApi: true,
          },
        };
        parseCodexInitializeResponse(
          await options.transport.send({
            id: CODEX_INITIALIZE_REQUEST_ID,
            method: "initialize",
            params: initializeParams,
          }),
        );
        const initializedNotification: CodexClientNotification = {
          method: "initialized",
        };
        await options.transport.notify(initializedNotification);
        if (latestDisconnect !== null) {
          return err(buildInitializationDisconnectError(options, latestDisconnect));
        }
        initialized = true;
        state = "ready";
        options.logger.info("Codex session initialized");
        return ok(undefined);
      } catch (error) {
        state = "disconnected";
        return err(
          normalizeOperationError(
            options.agentId,
            options.executable,
            options.environment,
            CODEX_INITIALIZE_REQUEST_ID,
            error,
          ),
        );
      } finally {
        initializePromise = null;
      }
    })();
    initializePromise = pendingInitialization;

    return pendingInitialization;
  };

  const runOperation = async <T>(
    requestId: AgentRequestId,
    operation: () => Promise<T>,
  ): Promise<AgentOperationResult<T>> => {
    const initializationResult = await ensureInitialized();
    if (!initializationResult.ok) {
      return initializationResult;
    }

    try {
      return ok(await operation());
    } catch (error) {
      return err(
        normalizeOperationError(
          options.agentId,
          options.executable,
          options.environment,
          requestId,
          error,
        ),
      );
    }
  };

  const listModels = async (
    requestId: AgentRequestId,
    params: AgentListModelsParams,
  ): Promise<AgentOperationResult<AgentListModelsResult>> =>
    runOperation(requestId, async () => {
      const rawParams: CodexModelListParams = {
        limit: params.limit,
        includeHidden: params.includeHidden === true ? true : undefined,
      };
      const response = parseCodexModelListResponse(
        await options.transport.send({
          id: requestId,
          method: "model/list",
          params: rawParams,
        }),
      );

      return {
        models: response.data.map((model) => mapCodexModelSummary(model)),
        nextCursor: response.nextCursor,
      };
    });

  const listThreads = async (
    requestId: AgentRequestId,
    params: AgentListThreadsParams,
  ): Promise<AgentOperationResult<AgentListThreadsResult>> =>
    runOperation(requestId, async () => {
      const rawParams: CodexThreadListParams = {
        cursor: params.cursor,
        limit: params.limit,
        archived: params.archived,
        cwd: params.workspacePath,
      };
      const response = parseCodexThreadListResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/list",
          params: rawParams,
        }),
      );

      return {
        threads: response.data.map((thread) =>
          mapCodexThread(thread, {
            archived: params.archived === true,
          }),
        ),
        nextCursor: response.nextCursor,
      };
    });

  const startThread = async (
    requestId: AgentRequestId,
    params: AgentThreadStartParams,
  ): Promise<AgentOperationResult<AgentThreadResult>> =>
    runOperation(requestId, async () => {
      const response = parseCodexConfiguredThreadResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/start",
          params: buildThreadStartParams(params),
        }),
      );

      return {
        thread: mapCodexThread(response.thread),
        model: response.model,
        reasoningEffort: response.reasoningEffort,
      };
    });

  const resumeThread = async (
    requestId: AgentRequestId,
    params: AgentThreadResumeParams,
  ): Promise<AgentOperationResult<AgentThreadResult>> =>
    runOperation(requestId, async () => {
      const response = parseCodexConfiguredThreadResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/resume",
          params: buildThreadResumeParams(params),
        }),
      );

      return {
        thread: mapCodexThread(response.thread),
        model: response.model,
        reasoningEffort: response.reasoningEffort,
      };
    });

  const readThread = async (
    requestId: AgentRequestId,
    params: AgentThreadReadParams,
  ): Promise<AgentOperationResult<AgentThreadReadResult>> =>
    runOperation(requestId, async () => {
      const rawParams: CodexThreadReadParams = {
        threadId: params.threadId,
        includeTurns: params.includeTurns,
      };
      const response = parseCodexThreadResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/read",
          params: rawParams,
        }),
      );

      return {
        thread: mapCodexThreadDetail(response.thread, {
          archived: params.archived,
        }),
      };
    });

  const forkThread = async (
    requestId: AgentRequestId,
    params: AgentThreadForkParams,
  ): Promise<AgentOperationResult<AgentThreadResult>> =>
    runOperation(requestId, async () => {
      const response = parseCodexConfiguredThreadResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/fork",
          params: buildThreadForkParams(params),
        }),
      );

      return {
        thread: mapCodexThread(response.thread),
        model: response.model,
        reasoningEffort: response.reasoningEffort,
      };
    });

  const archiveThread = async (
    requestId: AgentRequestId,
    params: AgentThreadArchiveParams,
  ): Promise<AgentOperationResult<AgentThreadMutationResult>> =>
    runOperation(requestId, async () => {
      const rawParams: CodexThreadArchiveParams = {
        threadId: params.threadId,
      };
      parseCodexEmptyResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/archive",
          params: rawParams,
        }),
        "thread/archive response",
      );

      return {};
    });

  const unarchiveThread = async (
    requestId: AgentRequestId,
    params: AgentThreadUnarchiveParams,
  ): Promise<AgentOperationResult<AgentThreadResult>> =>
    runOperation(requestId, async () => {
      const rawParams: CodexThreadUnarchiveParams = {
        threadId: params.threadId,
      };
      const response = parseCodexThreadResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/unarchive",
          params: rawParams,
        }),
      );

      return {
        thread: mapCodexThread(response.thread),
      };
    });

  const setThreadName = async (
    requestId: AgentRequestId,
    params: AgentThreadSetNameParams,
  ): Promise<AgentOperationResult<AgentThreadMutationResult>> =>
    runOperation(requestId, async () => {
      const rawParams: CodexThreadSetNameParams = {
        threadId: params.threadId,
        name: params.name,
      };
      parseCodexEmptyResponse(
        await options.transport.send({
          id: requestId,
          method: "thread/name/set",
          params: rawParams,
        }),
        "thread/name/set response",
      );

      return {};
    });

  const startTurn = async (
    requestId: AgentRequestId,
    params: AgentTurnStartParams,
  ): Promise<AgentOperationResult<AgentTurnResult>> =>
    runOperation(requestId, async () => {
      const response = parseCodexTurnResponse(
        await options.transport.send({
          id: requestId,
          method: "turn/start",
          params: buildTurnStartParams(params),
        }),
      );

      return {
        turn: mapCodexTurnSummary(response.turn),
      };
    });

  const steerTurn = async (
    requestId: AgentRequestId,
    params: AgentTurnSteerParams,
  ): Promise<AgentOperationResult<AgentTurnResult>> =>
    runOperation(requestId, async () => {
      const response = parseCodexTurnSteerResponse(
        await options.transport.send({
          id: requestId,
          method: "turn/steer",
          params: buildTurnSteerParams(params),
        }),
      );

      return {
        turn: {
          id: response.turnId,
          status: { type: "inProgress" },
        },
      };
    });

  const interruptTurn = async (
    requestId: AgentRequestId,
    params: AgentTurnInterruptParams,
  ): Promise<AgentOperationResult<AgentTurnResult>> =>
    runOperation(requestId, async () => {
      parseCodexTurnInterruptResponse(
        await options.transport.send({
          id: requestId,
          method: "turn/interrupt",
          params: buildTurnInterruptParams(params),
        }),
      );

      return {
        turn: {
          id: params.turnId,
          status: { type: "interrupted" },
        },
      };
    });

  const resolveApproval = async (
    params: AgentApprovalResolveParams,
  ): Promise<AgentOperationResult<AgentApprovalResolveResult>> => {
    const initializationResult = await ensureInitialized();
    if (!initializationResult.ok) {
      return initializationResult;
    }

    const pendingApproval = approvalsByRequestId.get(requestIdKey(params.requestId));
    if (pendingApproval === undefined) {
      return err({
        type: "invalidProviderMessage",
        agentId: options.agentId,
        provider: "codex",
        message: `No pending approval exists for request ${String(params.requestId)}.`,
        detail: { requestId: params.requestId },
      });
    }

    pendingApproval.resolution = params.resolution;

    try {
      const response: CodexTransportResponse = {
        id: params.requestId,
        result: buildApprovalDecision(pendingApproval.approval, params),
      };
      await options.transport.respond(response);
      return ok({
        requestId: params.requestId,
        resolution: params.resolution,
      });
    } catch (error) {
      return err(
        normalizeOperationError(
          options.agentId,
          options.executable,
          options.environment,
          params.requestId,
          error,
        ),
      );
    }
  };

  return Object.freeze({
    agentId: options.agentId,
    provider: "codex",
    getState: () => state,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    listModels,
    listThreads,
    startThread,
    resumeThread,
    readThread,
    forkThread,
    archiveThread,
    unarchiveThread,
    setThreadName,
    startTurn,
    steerTurn,
    interruptTurn,
    resolveApproval,
    disconnect: async (reason?: AgentDisconnectReason) => {
      state = "disconnecting";
      await options.transport.disconnect(reason);
      state = "disconnected";
    },
  });
};

const buildThreadStartParams = (params: AgentThreadStartParams): CodexThreadStartParams => ({
  cwd: params.workspacePath,
  model: params.model,
  approvalPolicy: normalizeApprovalPolicy(params.approvalPolicy),
  sandbox: undefined,
  experimentalRawEvents: false,
  persistExtendedHistory: true,
});

const buildThreadResumeParams = (params: AgentThreadResumeParams): CodexThreadResumeParams => ({
  threadId: params.threadId,
  cwd: params.workspacePath,
  model: params.model,
  approvalPolicy: normalizeApprovalPolicy(params.approvalPolicy),
  sandbox: undefined,
  persistExtendedHistory: true,
});

const buildThreadForkParams = (params: AgentThreadForkParams): CodexThreadForkParams => ({
  threadId: params.threadId,
  cwd: params.workspacePath,
  model: params.model,
  persistExtendedHistory: true,
});

const buildTurnStartParams = (params: AgentTurnStartParams): CodexTurnStartParams => ({
  threadId: params.threadId,
  input: [toTextInput(params.prompt)],
  cwd: params.cwd,
  model: params.model,
  effort: params.reasoningEffort,
});

const buildTurnSteerParams = (params: AgentTurnSteerParams): CodexTurnSteerParams => ({
  threadId: params.threadId,
  expectedTurnId: params.turnId,
  input: [toTextInput(params.prompt)],
});

const buildTurnInterruptParams = (params: AgentTurnInterruptParams): CodexTurnInterruptParams => ({
  threadId: params.threadId,
  turnId: params.turnId,
});

const toTextInput = (prompt: string): CodexUserInput => ({
  type: "text",
  text: prompt,
  text_elements: [],
});

const normalizeApprovalPolicy = (value: string | undefined): CodexAskForApproval | undefined => {
  switch (value) {
    case "untrusted":
    case "on-failure":
    case "on-request":
    case "never":
      return value;
    default:
      return undefined;
  }
};

const buildApprovalDecision = (
  approval: AgentApprovalRequest,
  params: AgentApprovalResolveParams,
):
  | CodexCommandExecutionApprovalDecision
  | CodexFileChangeApprovalDecision
  | CodexMcpServerElicitationRequestResponse => {
  switch (approval.kind) {
    case "commandExecution":
      return mapCommandApprovalResolution(params.resolution);
    case "fileChange":
      return mapFileChangeApprovalResolution(params.resolution);
    case "mcpElicitation":
      return mapMcpElicitationResolution(params.resolution);
    case "unknown":
      return mapFileChangeApprovalResolution(params.resolution);
  }
};

const mapCommandApprovalResolution = (
  resolution: AgentApprovalResolveParams["resolution"],
): CodexCommandExecutionApprovalDecision => {
  switch (resolution) {
    case "approved":
      return "accept";
    case "approvedForSession":
      return "acceptForSession";
    case "declined":
      return "decline";
    case "cancelled":
    case "stale":
      return "cancel";
  }
};

const mapFileChangeApprovalResolution = (
  resolution: AgentApprovalResolveParams["resolution"],
): CodexFileChangeApprovalDecision => {
  switch (resolution) {
    case "approved":
      return "accept";
    case "approvedForSession":
      return "acceptForSession";
    case "declined":
      return "decline";
    case "cancelled":
    case "stale":
      return "cancel";
  }
};

const mapMcpElicitationResolution = (
  resolution: AgentApprovalResolveParams["resolution"],
): CodexMcpServerElicitationRequestResponse => ({
  action:
    resolution === "approved" || resolution === "approvedForSession"
      ? "accept"
      : resolution === "declined"
        ? "decline"
        : "cancel",
  content: null,
  _meta: null,
});

const applyCodexEnvironmentOverrides = (
  environment: Readonly<Record<string, string | undefined>>,
  config: CodexAgentConfig,
): Record<string, string | undefined> => ({
  ...environment,
  ...(config.executablePath ? { ATELIERCODE_CODEX_PATH: config.executablePath } : {}),
  ...(config.environment ?? {}),
});

const normalizeSessionStartupError = (
  agentId: string,
  executable: AgentExecutableDiscovery,
  environment: Awaited<ReturnType<BaseEnvironmentResolver["resolve"]>>,
  error: unknown,
): AgentSessionLookupError => {
  if (error instanceof CodexTransportError) {
    return {
      type: "sessionUnavailable",
      agentId,
      provider: "codex",
      code: error.code === "provider_executable_missing" ? "executableMissing" : "startupFailed",
      message: error.message,
      executable,
      environment: environment.diagnostics,
      detail: error.detail,
    };
  }

  return {
    type: "sessionUnavailable",
    agentId,
    provider: "codex",
    code: "startupFailed",
    message: error instanceof Error ? error.message : "Codex session startup failed.",
    executable,
    environment: environment.diagnostics,
  };
};

const buildInitializationDisconnectError = (
  options: ConnectedSessionOptions,
  disconnect: CodexTransportDisconnectInfo | null,
): AgentSessionUnavailableError => ({
  type: "sessionUnavailable",
  agentId: options.agentId,
  provider: "codex",
  code: disconnect?.reason === "provider_executable_missing" ? "executableMissing" : "disconnected",
  message: disconnect?.message ?? "Codex transport disconnected during initialization.",
  executable: options.executable,
  environment: options.environment.diagnostics,
  ...(disconnect?.detail ? { detail: disconnect.detail } : {}),
});

const normalizeOperationError = (
  agentId: string,
  executable: AgentExecutableDiscovery,
  environment: Awaited<ReturnType<BaseEnvironmentResolver["resolve"]>>,
  requestId: AgentRequestId,
  error: unknown,
): AgentOperationError => {
  if (error instanceof CodexTransportRemoteError) {
    return {
      type: "remoteError",
      agentId,
      provider: "codex",
      requestId: error.requestId,
      code: error.code,
      message: error.message,
      data: error.data,
    };
  }

  if (error instanceof CodexTransportError) {
    return {
      type: "sessionUnavailable",
      agentId,
      provider: "codex",
      code:
        error.code === "provider_executable_missing"
          ? "executableMissing"
          : error.code === "process_exited" || error.code === "request_timeout"
            ? "disconnected"
            : "startupFailed",
      message: error.message,
      executable,
      environment: environment.diagnostics,
      detail: error.detail,
    };
  }

  return {
    type: "invalidProviderMessage",
    agentId,
    provider: "codex",
    message: `Codex operation ${String(requestId)} failed unexpectedly.`,
    detail: {
      error: error instanceof Error ? error.message : String(error),
    },
  };
};

const requestIdKey = (requestId: string | number): string =>
  `${typeof requestId}:${String(requestId)}`;

const getRequestId = (value: unknown): string | number | null =>
  typeof value === "string" ? value : Number.isInteger(value) ? (value as number) : null;
