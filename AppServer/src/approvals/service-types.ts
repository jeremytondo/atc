import type {
  AgentRemoteError,
  AgentSessionLookupError,
  AgentSessionUnavailableError,
} from "@/agents/contracts";
import type { AgentRegistry } from "@/agents/registry";
import type { ApprovalRegistry } from "@/approvals/approval-registry";
import type {
  ApprovalDecisionResolution,
  ApprovalRequest,
  ApprovalResolveParams,
  ApprovalResolveResult,
} from "@/approvals/schemas";

export type InvalidApprovalProviderPayloadError = Readonly<{
  type: "invalidProviderPayload";
  agentId: string;
  provider: string;
  operation: "approval/resolve";
  message: string;
  detail?: Record<string, unknown>;
}>;

export type ApprovalNotPendingError = Readonly<{
  type: "approvalNotPending";
  requestId: string | number;
  message: string;
}>;

export type UnsupportedApprovalResolutionError = Readonly<{
  type: "unsupportedApprovalResolution";
  requestId: string | number;
  resolution: ApprovalDecisionResolution;
  supportedResolutions: readonly ApprovalDecisionResolution[];
  message: string;
}>;

export type ApprovalsServiceError =
  | AgentSessionLookupError
  | AgentSessionUnavailableError
  | AgentRemoteError
  | InvalidApprovalProviderPayloadError
  | ApprovalNotPendingError
  | UnsupportedApprovalResolutionError;

export type ApprovalResolveServiceResult = ApprovalResolveResult &
  Readonly<{
    approval: ApprovalRequest;
  }>;

export type ApprovalsService = Readonly<{
  resolveApproval: (
    params: ApprovalResolveParams,
  ) => Promise<
    { ok: true; data: ApprovalResolveServiceResult } | { ok: false; error: ApprovalsServiceError }
  >;
}>;

export type CreateApprovalsServiceOptions = Readonly<{
  registry: AgentRegistry;
  approvals: ApprovalRegistry;
}>;
