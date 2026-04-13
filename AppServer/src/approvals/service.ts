import type { AgentInvalidMessageError } from "@/agents/contracts";
import type {
  ApprovalNotPendingError,
  ApprovalsService,
  CreateApprovalsServiceOptions,
  InvalidApprovalProviderPayloadError,
  UnsupportedApprovalResolutionError,
} from "@/approvals/service-types";
import {
  createInvalidProviderPayloadError,
  type ProtocolMethodError,
} from "@/core/protocol/errors";
import { assertNever, err, ok } from "@/core/shared";

export type {
  ApprovalNotPendingError,
  ApprovalResolveServiceResult,
  ApprovalsServiceError,
  CreateApprovalsServiceOptions,
  InvalidApprovalProviderPayloadError,
  UnsupportedApprovalResolutionError,
} from "@/approvals/service-types";

export const createApprovalsService = (options: CreateApprovalsServiceOptions): ApprovalsService =>
  Object.freeze({
    resolveApproval: async (params) => {
      const pendingApproval = options.approvals.getPending(params.requestId);
      if (pendingApproval === undefined) {
        return err<ApprovalNotPendingError>({
          type: "approvalNotPending",
          requestId: params.requestId,
          message: "Approval request is not pending.",
        });
      }

      if (!pendingApproval.approval.supportedResolutions.includes(params.resolution)) {
        return err<UnsupportedApprovalResolutionError>({
          type: "unsupportedApprovalResolution",
          requestId: params.requestId,
          resolution: params.resolution,
          supportedResolutions: pendingApproval.approval.supportedResolutions,
          message: "Approval resolution is not supported for this request.",
        });
      }

      const sessionResult = await options.registry.getSession();
      if (!sessionResult.ok) {
        return err(sessionResult.error);
      }

      const resolveResult = await sessionResult.data.resolveApproval({
        requestId: params.requestId,
        resolution: params.resolution,
      });
      if (!resolveResult.ok) {
        switch (resolveResult.error.type) {
          case "sessionUnavailable":
          case "remoteError":
            return err(resolveResult.error);
          case "invalidProviderMessage":
            return err(createInvalidProviderPayloadServiceError(resolveResult.error));
          default:
            return assertNever(resolveResult.error, "Unhandled approval/resolve service error");
        }
      }

      options.approvals.markResolved(params.requestId, params.resolution);

      return ok({
        approval: pendingApproval.approval,
        requestId: resolveResult.data.requestId,
        resolution: resolveResult.data.resolution,
      });
    },
  });

export const mapInvalidApprovalProviderPayloadToProtocolError = (
  error: InvalidApprovalProviderPayloadError,
): ProtocolMethodError =>
  createInvalidProviderPayloadError({
    agentId: error.agentId,
    provider: error.provider,
    operation: error.operation,
    providerMessage: error.message,
  });

const createInvalidProviderPayloadServiceError = (
  error: AgentInvalidMessageError,
): InvalidApprovalProviderPayloadError =>
  Object.freeze({
    type: "invalidProviderPayload",
    agentId: error.agentId,
    provider: error.provider,
    operation: "approval/resolve",
    message: error.message,
    ...(error.detail ? { detail: error.detail } : {}),
  });
