import type { AgentRemoteError, AgentSessionUnavailableError } from "@/agents/contracts";
import {
  ATELIER_AGENT_NOT_FOUND_ERROR,
  ATELIER_AGENT_SESSION_UNAVAILABLE_ERROR,
  ATELIER_PROVIDER_ERROR,
  createProtocolMethodError,
  type ProtocolMethodError,
} from "@/core/protocol/errors";

export const createAgentSessionUnavailableError = (
  error: AgentSessionUnavailableError,
): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_AGENT_SESSION_UNAVAILABLE_ERROR,
    "Agent session unavailable",
    Object.freeze({
      code: "AGENT_SESSION_UNAVAILABLE",
      agentId: error.agentId,
      provider: error.provider,
      reason: error.code,
      message: error.message,
    }),
  );

export const createProviderError = (error: AgentRemoteError): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_PROVIDER_ERROR,
    "Provider error",
    Object.freeze({
      code: "PROVIDER_ERROR",
      agentId: error.agentId,
      provider: error.provider,
      providerCode: String(error.code),
      providerMessage: error.message,
    }),
  );

export const createAgentNotFoundProtocolError = (agentId: string): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_AGENT_NOT_FOUND_ERROR,
    "Requested agent is not configured",
    Object.freeze({
      code: "AGENT_NOT_FOUND",
      agentId,
    }),
  );
