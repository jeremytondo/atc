import type { AgentAdapter } from "@/agents/adapter";
import type {
  AgentListModelsParams,
  AgentListModelsResult,
  AgentListThreadsParams,
  AgentListThreadsResult,
  AgentNotification,
  AgentOperationResult,
  AgentReasoningEffort,
  AgentRequestId,
  AgentSession,
  AgentSessionLookupError,
  AgentSessionState,
  AgentThread,
  AgentThreadArchiveParams,
  AgentThreadDetail,
  AgentThreadForkParams,
  AgentThreadMutationResult,
  AgentThreadReadParams,
  AgentThreadReadResult,
  AgentThreadResult,
  AgentThreadSetNameParams,
  AgentThreadUnarchiveParams,
  AgentTurnDetail,
  AgentTurnItem,
  AgentTurnResult,
} from "@/agents/contracts";
import type { AgentRegistry } from "@/agents/registry";

export type FakeAgentSession = AgentSession &
  Readonly<{
    emitNotification: (notification: AgentNotification) => void;
    listModelsCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentListModelsParams;
    }>[];
    listThreadsCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentListThreadsParams;
    }>[];
    startThreadCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: Readonly<{
        workspacePath: string;
        title?: string;
        model?: string;
        reasoningEffort?: AgentReasoningEffort;
        approvalPolicy?: string;
        sandbox?: unknown;
      }>;
    }>[];
    resumeThreadCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: Readonly<{
        threadId: string;
        workspacePath: string;
        model?: string;
        reasoningEffort?: AgentReasoningEffort;
        approvalPolicy?: string;
        sandbox?: unknown;
      }>;
    }>[];
    readThreadCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadReadParams;
    }>[];
    forkThreadCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadForkParams;
    }>[];
    archiveThreadCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadArchiveParams;
    }>[];
    unarchiveThreadCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadUnarchiveParams;
    }>[];
    setThreadNameCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadSetNameParams;
    }>[];
    startTurnCalls: readonly Readonly<{
      requestId: AgentRequestId;
      params: Readonly<{
        threadId: string;
        prompt: string;
        model?: string;
        reasoningEffort?: AgentReasoningEffort;
        cwd?: string;
      }>;
    }>[];
  }>;

export type CreateFakeAgentSessionOptions = Readonly<{
  agentId?: string;
  state?: AgentSessionState;
  listModels?: (
    requestId: AgentRequestId,
    params: AgentListModelsParams,
  ) => Promise<AgentOperationResult<AgentListModelsResult>>;
  listThreads?: (
    requestId: AgentRequestId,
    params: AgentListThreadsParams,
  ) => Promise<AgentOperationResult<AgentListThreadsResult>>;
  startThread?: (
    requestId: AgentRequestId,
    params: Readonly<{
      workspacePath: string;
      title?: string;
      model?: string;
      reasoningEffort?: AgentReasoningEffort;
      approvalPolicy?: string;
      sandbox?: unknown;
    }>,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  resumeThread?: (
    requestId: AgentRequestId,
    params: Readonly<{
      threadId: string;
      workspacePath: string;
      model?: string;
      reasoningEffort?: AgentReasoningEffort;
      approvalPolicy?: string;
      sandbox?: unknown;
    }>,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  readThread?: (
    requestId: AgentRequestId,
    params: AgentThreadReadParams,
  ) => Promise<AgentOperationResult<AgentThreadReadResult>>;
  forkThread?: (
    requestId: AgentRequestId,
    params: AgentThreadForkParams,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  archiveThread?: (
    requestId: AgentRequestId,
    params: AgentThreadArchiveParams,
  ) => Promise<AgentOperationResult<AgentThreadMutationResult>>;
  unarchiveThread?: (
    requestId: AgentRequestId,
    params: AgentThreadUnarchiveParams,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  setThreadName?: (
    requestId: AgentRequestId,
    params: AgentThreadSetNameParams,
  ) => Promise<AgentOperationResult<AgentThreadMutationResult>>;
  startTurn?: (
    requestId: AgentRequestId,
    params: Readonly<{
      threadId: string;
      prompt: string;
      model?: string;
      reasoningEffort?: AgentReasoningEffort;
      cwd?: string;
    }>,
  ) => Promise<AgentOperationResult<AgentTurnResult>>;
}>;

export const createFakeAgentSession = (
  options: CreateFakeAgentSessionOptions = {},
): FakeAgentSession => {
  let state = options.state ?? "ready";
  const listeners = new Set<(notification: AgentNotification) => void>();
  const listModelsCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentListModelsParams;
    }>
  > = [];
  const listThreadsCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentListThreadsParams;
    }>
  > = [];
  const startThreadCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: Readonly<{
        workspacePath: string;
        title?: string;
        model?: string;
        reasoningEffort?: AgentReasoningEffort;
        approvalPolicy?: string;
        sandbox?: unknown;
      }>;
    }>
  > = [];
  const resumeThreadCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: Readonly<{
        threadId: string;
        workspacePath: string;
        model?: string;
        reasoningEffort?: AgentReasoningEffort;
        approvalPolicy?: string;
        sandbox?: unknown;
      }>;
    }>
  > = [];
  const readThreadCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadReadParams;
    }>
  > = [];
  const forkThreadCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadForkParams;
    }>
  > = [];
  const archiveThreadCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadArchiveParams;
    }>
  > = [];
  const unarchiveThreadCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadUnarchiveParams;
    }>
  > = [];
  const setThreadNameCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: AgentThreadSetNameParams;
    }>
  > = [];
  const startTurnCalls: Array<
    Readonly<{
      requestId: AgentRequestId;
      params: Readonly<{
        threadId: string;
        prompt: string;
        model?: string;
        reasoningEffort?: AgentReasoningEffort;
        cwd?: string;
      }>;
    }>
  > = [];

  return {
    agentId: options.agentId ?? "codex",
    provider: "codex",
    getState: () => state,
    subscribe: (listener: (notification: AgentNotification) => void) => {
      listeners.add(listener);

      return () => {
        listeners.delete(listener);
      };
    },
    listModels: async (requestId, params) => {
      listModelsCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.listModels?.(requestId, params)) ?? {
          ok: true,
          data: {
            models: [],
            nextCursor: null,
          },
        }
      );
    },
    listThreads: async (requestId, params) => {
      listThreadsCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.listThreads?.(requestId, params)) ?? {
          ok: true,
          data: {
            threads: [],
            nextCursor: null,
          },
        }
      );
    },
    startThread: async (requestId, params) => {
      startThreadCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.startThread?.(requestId, params)) ?? {
          ok: true,
          data: {
            thread: createTestAgentThread({
              workspacePath: params.workspacePath,
            }),
            model: params.model ?? "gpt-5.4",
            reasoningEffort: params.reasoningEffort ?? "medium",
          },
        }
      );
    },
    resumeThread: async (requestId, params) => {
      resumeThreadCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.resumeThread?.(requestId, params)) ?? {
          ok: true,
          data: {
            thread: createTestAgentThread({
              id: params.threadId,
              workspacePath: params.workspacePath,
            }),
            model: params.model ?? "gpt-5.4",
            reasoningEffort: params.reasoningEffort ?? "medium",
          },
        }
      );
    },
    readThread: async (requestId, params) => {
      readThreadCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.readThread?.(requestId, params)) ?? {
          ok: true,
          data: {
            thread: createTestAgentThreadDetail(),
          },
        }
      );
    },
    forkThread: async (requestId, params) => {
      forkThreadCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.forkThread?.(requestId, params)) ?? {
          ok: true,
          data: {
            thread: createTestAgentThread({
              id: `fork-${params.threadId}`,
              workspacePath: params.workspacePath,
            }),
            model: params.model ?? "gpt-5.4",
            reasoningEffort: "medium",
          },
        }
      );
    },
    archiveThread: async (requestId, params) => {
      archiveThreadCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.archiveThread?.(requestId, params)) ?? {
          ok: true,
          data: {},
        }
      );
    },
    unarchiveThread: async (requestId, params) => {
      unarchiveThreadCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.unarchiveThread?.(requestId, params)) ?? {
          ok: true,
          data: {
            thread: createTestAgentThread({
              id: params.threadId,
            }),
          },
        }
      );
    },
    setThreadName: async (requestId, params) => {
      setThreadNameCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.setThreadName?.(requestId, params)) ?? {
          ok: true,
          data: {},
        }
      );
    },
    startTurn: async (requestId, params) => {
      startTurnCalls.push(
        Object.freeze({
          requestId,
          params,
        }),
      );

      return (
        (await options.startTurn?.(requestId, params)) ?? {
          ok: true,
          data: {
            turn: createTestAgentTurn(),
          },
        }
      );
    },
    steerTurn: async () => {
      throw new Error("steerTurn should not be called in this test.");
    },
    interruptTurn: async () => {
      throw new Error("interruptTurn should not be called in this test.");
    },
    resolveApproval: async () => {
      throw new Error("resolveApproval should not be called in this test.");
    },
    disconnect: async () => {
      state = "disconnected";
    },
    emitNotification: (notification) => {
      for (const listener of listeners) {
        listener(notification);
      }
    },
    listModelsCalls,
    listThreadsCalls,
    startThreadCalls,
    resumeThreadCalls,
    readThreadCalls,
    forkThreadCalls,
    archiveThreadCalls,
    unarchiveThreadCalls,
    setThreadNameCalls,
    startTurnCalls,
  };
};

export const createFakeAgentRegistry = (
  session: AgentSession,
): AgentRegistry &
  Readonly<{
    requestedAgentIds: readonly (string | undefined)[];
  }> => {
  const requestedAgentIds: Array<string | undefined> = [];

  return {
    requestedAgentIds,
    getDefaultAgentId: () => "codex",
    listAgents: () => [
      {
        id: "codex",
        provider: "codex",
      },
    ],
    getAgent: (agentId) =>
      agentId === "codex"
        ? {
            id: "codex",
            provider: "codex",
          }
        : undefined,
    getSession: async (agentId) => {
      requestedAgentIds.push(agentId);
      return {
        ok: true,
        data: session,
      };
    },
    disconnectAll: async () => {},
  };
};

export type CreateFakeAgentAdapterOptions = Readonly<{
  createSessionResult?:
    | Readonly<{
        ok: true;
        data: AgentSession;
      }>
    | Readonly<{
        ok: false;
        error: AgentSessionLookupError;
      }>;
  session?: AgentSession;
}>;

export const createFakeAgentAdapter = (
  options: CreateFakeAgentAdapterOptions = {},
): AgentAdapter => ({
  provider: "codex",
  createSession: async () =>
    options.createSessionResult ?? {
      ok: true,
      data: options.session ?? createFakeAgentSession(),
    },
});

export const createTestAgentModel = (
  overrides: Partial<
    Readonly<{
      id: string;
      model: string;
      displayName: string;
      hidden: boolean;
      isDefault: boolean;
      defaultReasoningEffort: "none" | "minimal" | "low" | "medium" | "high" | "xhigh";
      supportsPersonality: boolean;
    }>
  > = {},
) =>
  Object.freeze({
    id: overrides.id ?? "gpt-5.4",
    model: overrides.model ?? "gpt-5.4",
    displayName: overrides.displayName ?? "GPT-5.4",
    hidden: overrides.hidden ?? false,
    defaultReasoningEffort: overrides.defaultReasoningEffort ?? ("medium" as const),
    supportedReasoningEfforts: [
      {
        reasoningEffort: overrides.defaultReasoningEffort ?? ("medium" as const),
        description:
          (overrides.defaultReasoningEffort ?? "medium") === "high"
            ? "Deeper reasoning"
            : "Balanced",
      },
    ],
    inputModalities: ["text"],
    supportsPersonality: overrides.supportsPersonality ?? true,
    isDefault: overrides.isDefault ?? false,
  });

export const createTestAgentThread = (
  overrides: Partial<
    Readonly<{
      id: string;
      preview: string;
      createdAt: string;
      updatedAt: string;
      workspacePath: string;
      name: string | null;
      archived: boolean;
      status: AgentThread["status"];
    }>
  > = {},
) =>
  Object.freeze({
    id: overrides.id ?? "thread-1",
    preview: overrides.preview ?? "Thread preview",
    createdAt: overrides.createdAt ?? "2026-04-10T10:00:00.000Z",
    updatedAt: overrides.updatedAt ?? "2026-04-10T11:00:00.000Z",
    workspacePath: overrides.workspacePath ?? "/tmp/project",
    name: overrides.name ?? null,
    archived: overrides.archived ?? false,
    status: overrides.status ?? ({ type: "idle" } as const),
  });

export const createTestAgentThreadDetail = (
  overrides: Partial<
    Readonly<{
      id: string;
      preview: string;
      createdAt: string;
      updatedAt: string;
      workspacePath: string;
      name: string | null;
      archived: boolean;
      status: AgentThread["status"];
      turns: readonly AgentTurnDetail[];
    }>
  > = {},
): AgentThreadDetail =>
  Object.freeze({
    ...createTestAgentThread(overrides),
    turns: overrides.turns ?? [],
  });

export const createTestAgentTurn = (
  overrides: Partial<
    Readonly<{
      id: string;
      status: AgentTurnResult["turn"]["status"];
    }>
  > = {},
) =>
  Object.freeze({
    id: overrides.id ?? "turn-1",
    status: overrides.status ?? ({ type: "inProgress" } as const),
  });

export const createTestAgentTurnDetail = (
  overrides: Partial<
    Readonly<{
      id: string;
      status: AgentTurnResult["turn"]["status"];
      items: readonly AgentTurnItem[];
      error: AgentTurnDetail["error"];
    }>
  > = {},
): AgentTurnDetail =>
  Object.freeze({
    id: overrides.id ?? "turn-1",
    status: overrides.status ?? ({ type: "completed" } as const),
    items: overrides.items ?? [],
    error: overrides.error ?? null,
  });

export const createTestAgentItem = (
  overrides: Partial<
    Readonly<{
      id: string;
      text: string;
    }>
  > = {},
): AgentTurnItem =>
  Object.freeze({
    type: "agentMessage",
    id: overrides.id ?? "item-1",
    text: overrides.text ?? "",
    phase: null,
  });
