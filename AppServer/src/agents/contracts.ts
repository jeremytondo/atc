import type { Result } from "@/core/shared";

export type AgentProvider = "codex";
export type AgentRequestId = string | number;
export type AgentReasoningEffort = "none" | "minimal" | "low" | "medium" | "high" | "xhigh";

export type AgentEnvironmentSource = "inherited" | "login_probe" | "fallback";
export type AgentExecutableDiscoveryStatus = "found" | "missing";
export type AgentExecutableDiscoverySource = "environment" | "path" | "known-path" | "not-found";

export type AgentSessionState = "idle" | "connecting" | "ready" | "disconnecting" | "disconnected";

export type AgentDisconnectReason =
  | "requested_disconnect"
  | "app_socket_disconnected"
  | "provider_executable_missing"
  | "startup_failed"
  | "process_exited"
  | "request_timeout"
  | "malformed_output";

export type AgentEnvironmentDiagnostics = Readonly<{
  source: AgentEnvironmentSource;
  shellPath: string;
  probeError: string | null;
  pathDirectoryCount: number;
  homeDirectory: string | null;
}>;

export type AgentExecutableDiscovery = Readonly<{
  executableName: string;
  status: AgentExecutableDiscoveryStatus;
  resolvedPath: string | null;
  source: AgentExecutableDiscoverySource;
  baseEnvironmentSource: AgentEnvironmentSource;
  checkedPaths: readonly string[];
}>;

export type AgentModelReasoningEffort = Readonly<{
  reasoningEffort: AgentReasoningEffort;
  description?: string;
}>;

export type AgentModelSummary = Readonly<{
  id: string;
  model: string;
  displayName: string;
  hidden: boolean;
  defaultReasoningEffort?: AgentReasoningEffort;
  supportedReasoningEfforts: readonly AgentModelReasoningEffort[];
  inputModalities?: readonly string[];
  supportsPersonality?: boolean;
  isDefault: boolean;
}>;

export type AgentThreadExecutionStatus =
  | Readonly<{ type: "notLoaded" }>
  | Readonly<{ type: "idle" }>
  | Readonly<{ type: "active"; activeFlags: readonly string[] }>
  | Readonly<{ type: "systemError"; message?: string }>;

export type AgentThread = Readonly<{
  id: string;
  preview: string;
  createdAt: string;
  updatedAt: string;
  workspacePath: string;
  name: string | null;
  archived: boolean;
  status: AgentThreadExecutionStatus;
}>;

export type AgentTurnStatus =
  | Readonly<{ type: "inProgress" }>
  | Readonly<{ type: "awaitingInput" }>
  | Readonly<{ type: "completed" }>
  | Readonly<{ type: "failed"; message?: string }>
  | Readonly<{ type: "cancelled" }>
  | Readonly<{ type: "interrupted" }>;

export type AgentTurnTerminalError = Readonly<{
  message: string;
  providerError: unknown | null;
  additionalDetails: string | null;
}>;

export type AgentTurnItem =
  | Readonly<{
      type: "userMessage";
      id: string;
      content: unknown[];
    }>
  | Readonly<{
      type: "agentMessage";
      id: string;
      text: string;
      phase: string | null;
    }>
  | Readonly<{
      type: "plan";
      id: string;
      text: string;
    }>
  | Readonly<{
      type: "reasoning";
      id: string;
      summary: string[];
      content: string[];
    }>
  | Readonly<{
      type: "commandExecution";
      id: string;
      command: string;
      cwd: string;
      processId: string | null;
      status: "inProgress" | "completed" | "failed" | "declined";
      commandActions: unknown[];
      aggregatedOutput: string | null;
      exitCode: number | null;
      durationMs: number | null;
    }>
  | Readonly<{
      type: "fileChange";
      id: string;
      changes: unknown[];
      status: "inProgress" | "completed" | "failed" | "declined";
    }>
  | Readonly<{
      type: "mcpToolCall";
      id: string;
      server: string;
      tool: string;
      status: "inProgress" | "completed" | "failed";
      arguments: unknown;
      result: unknown | null;
      error: unknown | null;
      durationMs: number | null;
    }>
  | Readonly<{
      type: "dynamicToolCall";
      id: string;
      tool: string;
      arguments: unknown;
      status: "inProgress" | "completed" | "failed";
      contentItems: unknown[] | null;
      success: boolean | null;
      durationMs: number | null;
    }>
  | Readonly<{
      type: "collabAgentToolCall";
      id: string;
      tool: "spawnAgent" | "sendInput" | "resumeAgent" | "wait" | "closeAgent";
      status: "inProgress" | "completed" | "failed";
      senderThreadId: string;
      receiverThreadIds: string[];
      prompt: string | null;
      agentsStates: Record<string, unknown>;
    }>
  | Readonly<{
      type: "webSearch";
      id: string;
      query: string;
      action:
        | Readonly<{ type: "search"; query: string | null; queries: string[] | null }>
        | Readonly<{ type: "openPage"; url: string | null }>
        | Readonly<{ type: "findInPage"; url: string | null; pattern: string | null }>
        | Readonly<{ type: "other" }>
        | null;
    }>
  | Readonly<{
      type: "imageView";
      id: string;
      path: string;
    }>
  | Readonly<{
      type: "imageGeneration";
      id: string;
      status: string;
      revisedPrompt: string | null;
      result: string;
    }>
  | Readonly<{
      type: "enteredReviewMode";
      id: string;
      review: string;
    }>
  | Readonly<{
      type: "exitedReviewMode";
      id: string;
      review: string;
    }>
  | Readonly<{
      type: "contextCompaction";
      id: string;
    }>;

export type AgentThreadSummary = Readonly<{
  id: string;
  preview: string;
  updatedAt: string;
  name: string | null;
  archived: boolean;
  status: AgentThreadExecutionStatus;
}>;

export type AgentTurnSummary = Readonly<{
  id: string;
  status: AgentTurnStatus;
}>;

export type AgentTurnDetail = Readonly<{
  id: string;
  status: AgentTurnStatus;
  items: readonly AgentTurnItem[];
  error: AgentTurnTerminalError | null;
}>;

export type AgentThreadDetail = AgentThread &
  Readonly<{
    turns: readonly AgentTurnDetail[];
  }>;

export type AgentPlanStep = Readonly<{
  step: string;
  status: "pending" | "in_progress" | "completed";
}>;

export type AgentDiffFileSummary = Readonly<{
  path: string;
  additions: number;
  deletions: number;
}>;

export type AgentApprovalKind = "commandExecution" | "fileChange" | "mcpElicitation" | "unknown";

export type AgentApprovalRequest = Readonly<{
  requestId: AgentRequestId;
  kind: AgentApprovalKind;
  threadId?: string;
  turnId?: string;
  itemId?: string;
  rawRequest: unknown;
}>;

export type AgentApprovalResolution =
  | "approved"
  | "approvedForSession"
  | "declined"
  | "cancelled"
  | "stale";

export type AgentNotificationBase = Readonly<{
  agentId: string;
  provider: AgentProvider;
  receivedAt: string;
  rawMethod: string;
  rawPayload?: unknown;
  threadId?: string;
  turnId?: string;
  itemId?: string;
}>;

export type AgentThreadNotification = AgentNotificationBase &
  Readonly<{
    type: "thread";
    event: "started" | "statusChanged" | "archived" | "unarchived" | "nameUpdated" | "closed";
    threadName?: string | null;
    thread: AgentThreadSummary;
  }>;

export type AgentTurnNotification = AgentNotificationBase &
  Readonly<{
    type: "turn";
    event: "started" | "completed";
    turn: AgentTurnSummary;
  }>;

export type AgentItemNotification = AgentNotificationBase &
  Readonly<{
    type: "item";
    event: "started" | "completed";
    item: AgentTurnItem;
  }>;

export type AgentMessageNotification = AgentNotificationBase &
  Readonly<{
    type: "message";
    event: "textDelta";
    delta: string;
  }>;

export type AgentReasoningNotification = AgentNotificationBase &
  Readonly<{
    type: "reasoning";
    event: "summaryTextDelta" | "summaryPartAdded" | "textDelta";
    delta?: string;
    summaryPart?: unknown;
  }>;

export type AgentCommandNotification = AgentNotificationBase &
  Readonly<{
    type: "command";
    event: "outputDelta";
    delta: string;
  }>;

export type AgentToolNotification = AgentNotificationBase &
  Readonly<{
    type: "tool";
    event: "progress";
    message: string;
  }>;

export type AgentPlanNotification = AgentNotificationBase &
  Readonly<{
    type: "plan";
    event: "updated";
    explanation?: string;
    steps: readonly AgentPlanStep[];
  }>;

export type AgentDiffNotification = AgentNotificationBase &
  Readonly<{
    type: "diff";
    event: "updated";
    diff: string;
    summary: readonly AgentDiffFileSummary[];
  }>;

export type AgentApprovalNotification = AgentNotificationBase &
  Readonly<{
    type: "approval";
    event: "requested" | "resolved";
    requestId: AgentRequestId;
    approval: AgentApprovalRequest;
    resolution?: AgentApprovalResolution;
  }>;

export type AgentDisconnectNotification = AgentNotificationBase &
  Readonly<{
    type: "disconnect";
    reason: AgentDisconnectReason;
    message: string;
    exitCode?: number | null;
    detail?: Record<string, unknown>;
  }>;

export type AgentErrorNotification = AgentNotificationBase &
  Readonly<{
    type: "error";
    code: string;
    message: string;
    detail?: unknown;
  }>;

export type AgentNotification =
  | AgentThreadNotification
  | AgentTurnNotification
  | AgentItemNotification
  | AgentMessageNotification
  | AgentReasoningNotification
  | AgentCommandNotification
  | AgentToolNotification
  | AgentPlanNotification
  | AgentDiffNotification
  | AgentApprovalNotification
  | AgentDisconnectNotification
  | AgentErrorNotification;

export type AgentSessionUnavailableError = Readonly<{
  type: "sessionUnavailable";
  agentId: string;
  provider: AgentProvider;
  code: "executableMissing" | "startupFailed" | "disconnected";
  message: string;
  executable?: AgentExecutableDiscovery;
  environment?: AgentEnvironmentDiagnostics;
  detail?: Record<string, unknown>;
}>;

export type AgentRemoteError = Readonly<{
  type: "remoteError";
  agentId: string;
  provider: AgentProvider;
  requestId: AgentRequestId;
  code: number;
  message: string;
  data?: unknown;
}>;

export type AgentInvalidMessageError = Readonly<{
  type: "invalidProviderMessage";
  agentId: string;
  provider: AgentProvider;
  message: string;
  detail?: Record<string, unknown>;
}>;

export type AgentOperationError =
  | AgentSessionUnavailableError
  | AgentRemoteError
  | AgentInvalidMessageError;

export type AgentSessionLookupError =
  | Readonly<{
      type: "agentNotFound";
      agentId: string;
      message: string;
    }>
  | AgentSessionUnavailableError;

export type AgentOperationResult<T> = Result<T, AgentOperationError>;
export type AgentSessionLookupResult<T> = Result<T, AgentSessionLookupError>;

export type AgentListModelsParams = Readonly<{
  limit?: number;
  includeHidden?: boolean;
}>;

export type AgentListModelsResult = Readonly<{
  models: readonly AgentModelSummary[];
  nextCursor: string | null;
}>;

export type AgentListThreadsParams = Readonly<{
  cursor?: string;
  limit?: number;
  archived?: boolean;
  workspacePath?: string;
}>;

export type AgentListThreadsResult = Readonly<{
  threads: readonly AgentThread[];
  nextCursor: string | null;
}>;

export type AgentThreadStartParams = Readonly<{
  workspacePath: string;
  title?: string;
  model?: string;
  reasoningEffort?: AgentReasoningEffort;
  approvalPolicy?: string;
  sandbox?: unknown;
}>;

export type AgentThreadResumeParams = Readonly<{
  threadId: string;
  workspacePath: string;
  model?: string;
  reasoningEffort?: AgentReasoningEffort;
  approvalPolicy?: string;
  sandbox?: unknown;
}>;

export type AgentThreadReadParams = Readonly<{
  threadId: string;
  includeTurns: boolean;
  archived?: boolean;
}>;

export type AgentThreadForkParams = Readonly<{
  threadId: string;
  workspacePath: string;
  model?: string;
}>;

export type AgentThreadArchiveParams = Readonly<{
  threadId: string;
}>;

export type AgentThreadUnarchiveParams = Readonly<{
  threadId: string;
}>;

export type AgentThreadSetNameParams = Readonly<{
  threadId: string;
  name: string;
}>;

export type AgentThreadResult = Readonly<{
  thread: AgentThread;
  model?: string;
  reasoningEffort?: AgentReasoningEffort | null;
}>;

export type AgentThreadReadResult = Readonly<{
  thread: AgentThreadDetail;
}>;

export type AgentThreadMutationResult = Record<string, never>;

export type AgentTurnStartParams = Readonly<{
  threadId: string;
  prompt: string;
  model?: string;
  reasoningEffort?: AgentReasoningEffort;
  cwd?: string;
}>;

export type AgentTurnSteerParams = Readonly<{
  threadId: string;
  turnId: string;
  prompt: string;
}>;

export type AgentTurnInterruptParams = Readonly<{
  threadId: string;
  turnId: string;
}>;

export type AgentTurnResult = Readonly<{
  turn: AgentTurnSummary;
}>;

export type AgentApprovalResolveParams = Readonly<{
  requestId: AgentRequestId;
  resolution: AgentApprovalResolution;
}>;

export type AgentApprovalResolveResult = Readonly<{
  requestId: AgentRequestId;
  resolution: AgentApprovalResolution;
}>;

export type AgentSession = Readonly<{
  agentId: string;
  provider: AgentProvider;
  getState: () => AgentSessionState;
  subscribe: (listener: (notification: AgentNotification) => void) => () => void;
  listModels: (
    requestId: AgentRequestId,
    params: AgentListModelsParams,
  ) => Promise<AgentOperationResult<AgentListModelsResult>>;
  listThreads: (
    requestId: AgentRequestId,
    params: AgentListThreadsParams,
  ) => Promise<AgentOperationResult<AgentListThreadsResult>>;
  startThread: (
    requestId: AgentRequestId,
    params: AgentThreadStartParams,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  resumeThread: (
    requestId: AgentRequestId,
    params: AgentThreadResumeParams,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  readThread: (
    requestId: AgentRequestId,
    params: AgentThreadReadParams,
  ) => Promise<AgentOperationResult<AgentThreadReadResult>>;
  forkThread: (
    requestId: AgentRequestId,
    params: AgentThreadForkParams,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  archiveThread: (
    requestId: AgentRequestId,
    params: AgentThreadArchiveParams,
  ) => Promise<AgentOperationResult<AgentThreadMutationResult>>;
  unarchiveThread: (
    requestId: AgentRequestId,
    params: AgentThreadUnarchiveParams,
  ) => Promise<AgentOperationResult<AgentThreadResult>>;
  setThreadName: (
    requestId: AgentRequestId,
    params: AgentThreadSetNameParams,
  ) => Promise<AgentOperationResult<AgentThreadMutationResult>>;
  startTurn: (
    requestId: AgentRequestId,
    params: AgentTurnStartParams,
  ) => Promise<AgentOperationResult<AgentTurnResult>>;
  steerTurn: (
    requestId: AgentRequestId,
    params: AgentTurnSteerParams,
  ) => Promise<AgentOperationResult<AgentTurnResult>>;
  interruptTurn: (
    requestId: AgentRequestId,
    params: AgentTurnInterruptParams,
  ) => Promise<AgentOperationResult<AgentTurnResult>>;
  resolveApproval: (
    params: AgentApprovalResolveParams,
  ) => Promise<AgentOperationResult<AgentApprovalResolveResult>>;
  disconnect: (reason?: AgentDisconnectReason) => Promise<void>;
}>;
