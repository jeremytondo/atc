import type {
  AgentRemoteError,
  AgentRequestId,
  AgentSessionLookupError,
  AgentSessionUnavailableError,
} from "@/agents/contracts";
import type { AgentRegistry } from "@/agents/registry";
import type { Logger } from "@/app/logger";
import type { ActiveTurnConflictError, ActiveTurnRegistry } from "@/turns/active-turn-registry";
import type {
  TurnInterruptParams,
  TurnInterruptResult,
  TurnStartParams,
  TurnStartResult,
  TurnSteerParams,
  TurnSteerResult,
} from "@/turns/schemas";
import type { Workspace } from "@/workspaces/schemas";

export type InvalidTurnProviderPayloadError = Readonly<{
  type: "invalidProviderPayload";
  agentId: string;
  provider: string;
  operation: "turn/start" | "turn/steer" | "turn/interrupt";
  message: string;
  detail?: Record<string, unknown>;
}>;

export type ActiveTurnNotFoundError = Readonly<{
  type: "activeTurnNotFound";
  threadId: string;
  message: string;
}>;

export type ActiveTurnMismatchError = Readonly<{
  type: "activeTurnMismatch";
  threadId: string;
  requestedTurnId: string;
  activeTurnId?: string;
  message: string;
}>;

export type TurnsServiceError =
  | AgentSessionLookupError
  | AgentSessionUnavailableError
  | AgentRemoteError
  | ActiveTurnConflictError
  | ActiveTurnNotFoundError
  | ActiveTurnMismatchError
  | InvalidTurnProviderPayloadError;

export type TurnsService = Readonly<{
  startTurn: (
    requestId: AgentRequestId,
    workspace: Workspace,
    params: TurnStartParams,
  ) => Promise<{ ok: true; data: TurnStartResult } | { ok: false; error: TurnsServiceError }>;
  steerTurn: (
    requestId: AgentRequestId,
    params: TurnSteerParams,
  ) => Promise<{ ok: true; data: TurnSteerResult } | { ok: false; error: TurnsServiceError }>;
  interruptTurn: (
    requestId: AgentRequestId,
    params: TurnInterruptParams,
  ) => Promise<{ ok: true; data: TurnInterruptResult } | { ok: false; error: TurnsServiceError }>;
}>;

export type CreateTurnsServiceOptions = Readonly<{
  logger: Logger;
  registry: AgentRegistry;
  activeTurns: ActiveTurnRegistry;
}>;
