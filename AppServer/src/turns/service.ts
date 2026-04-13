import type { AgentInvalidMessageError, AgentTurnStatus } from "@/agents/contracts";
import {
  createActiveTurnAlreadyExistsError,
  createActiveTurnMismatchError,
  createActiveTurnNotFoundError,
  createInvalidProviderPayloadError,
  type ProtocolMethodError,
} from "@/core/protocol/errors";
import { assertNever, err, ok } from "@/core/shared";
import type { ActiveTurnConflictError } from "@/turns/active-turn-registry";
import type {
  ActiveTurnMismatchError,
  ActiveTurnNotFoundError,
  CreateTurnsServiceOptions,
  InvalidTurnProviderPayloadError,
  TurnsService,
} from "@/turns/service-types";

export type {
  CreateTurnsServiceOptions,
  InvalidTurnProviderPayloadError,
  TurnsServiceError,
} from "@/turns/service-types";

export const createTurnsService = (options: CreateTurnsServiceOptions): TurnsService =>
  Object.freeze({
    startTurn: async (requestId, workspace, params) => {
      const sessionResult = await options.registry.getSession();

      if (!sessionResult.ok) {
        return err(sessionResult.error);
      }

      const reservationResult = options.activeTurns.reserveThread(params.threadId);

      if (!reservationResult.ok) {
        return err(reservationResult.error);
      }

      const session = sessionResult.data;
      const reservation = reservationResult.data;
      const startTurnResult = await session.startTurn(requestId, {
        threadId: params.threadId,
        prompt: params.prompt,
        cwd: workspace.workspacePath,
      });

      if (!startTurnResult.ok) {
        reservation.release();

        switch (startTurnResult.error.type) {
          case "sessionUnavailable":
          case "remoteError":
            return err(startTurnResult.error);
          case "invalidProviderMessage":
            return err(
              createInvalidProviderPayloadServiceError(startTurnResult.error, "turn/start"),
            );
          default:
            return assertNever(startTurnResult.error, "Unhandled turn/start service error");
        }
      }

      const turn = Object.freeze({
        id: startTurnResult.data.turn.id,
        status: mapTurnStatus(startTurnResult.data.turn.status),
      });
      const wasRecorded = options.activeTurns.startTurn({
        threadId: params.threadId,
        turn,
      });
      if (!wasRecorded) {
        options.logger.warn("Ignored turn/start response with mismatched active turn", {
          threadId: params.threadId,
          turnId: turn.id,
          activeTurnId: options.activeTurns.getActiveTurn(params.threadId)?.turn?.id ?? null,
        });
      }

      return ok({
        turn,
      });
    },
    steerTurn: async (requestId, params) => {
      const activeTurnResult = getValidatedActiveTurn(options, params.threadId, params.turnId);
      if (!activeTurnResult.ok) {
        return err(activeTurnResult.error);
      }

      const sessionResult = await options.registry.getSession();
      if (!sessionResult.ok) {
        return err(sessionResult.error);
      }

      const session = sessionResult.data;
      const steerTurnResult = await session.steerTurn(requestId, {
        threadId: params.threadId,
        turnId: params.turnId,
        prompt: params.prompt,
      });

      if (!steerTurnResult.ok) {
        switch (steerTurnResult.error.type) {
          case "sessionUnavailable":
          case "remoteError":
            return err(steerTurnResult.error);
          case "invalidProviderMessage":
            return err(
              createInvalidProviderPayloadServiceError(steerTurnResult.error, "turn/steer"),
            );
          default:
            return assertNever(steerTurnResult.error, "Unhandled turn/steer service error");
        }
      }

      const turn = Object.freeze({
        id: steerTurnResult.data.turn.id,
        status: mapTurnStatus(steerTurnResult.data.turn.status),
      });
      const wasRecorded = options.activeTurns.startTurn({
        threadId: params.threadId,
        turn,
      });
      if (!wasRecorded) {
        options.logger.warn("Ignored turn/steer response with mismatched active turn", {
          threadId: params.threadId,
          turnId: turn.id,
          activeTurnId: options.activeTurns.getActiveTurn(params.threadId)?.turn?.id ?? null,
        });
      }

      return ok({
        turn,
      });
    },
    interruptTurn: async (requestId, params) => {
      const activeTurnResult = getValidatedActiveTurn(options, params.threadId, params.turnId);
      if (!activeTurnResult.ok) {
        return err(activeTurnResult.error);
      }

      const sessionResult = await options.registry.getSession();
      if (!sessionResult.ok) {
        return err(sessionResult.error);
      }

      const session = sessionResult.data;
      const interruptTurnResult = await session.interruptTurn(requestId, {
        threadId: params.threadId,
        turnId: params.turnId,
      });

      if (!interruptTurnResult.ok) {
        switch (interruptTurnResult.error.type) {
          case "sessionUnavailable":
          case "remoteError":
            return err(interruptTurnResult.error);
          case "invalidProviderMessage":
            return err(
              createInvalidProviderPayloadServiceError(interruptTurnResult.error, "turn/interrupt"),
            );
          default:
            return assertNever(interruptTurnResult.error, "Unhandled turn/interrupt service error");
        }
      }

      const turn = Object.freeze({
        id: interruptTurnResult.data.turn.id,
        status: mapTurnStatus(interruptTurnResult.data.turn.status),
      });
      const wasRecorded = options.activeTurns.recordTurnCompleted({
        threadId: params.threadId,
        turn,
      });
      if (!wasRecorded) {
        options.logger.warn("Ignored turn/interrupt response with mismatched active turn", {
          threadId: params.threadId,
          turnId: turn.id,
          activeTurnId: options.activeTurns.getActiveTurn(params.threadId)?.turn?.id ?? null,
        });
      }

      return ok({
        turn,
      });
    },
  });

export const mapInvalidProviderPayloadToProtocolError = (
  error: InvalidTurnProviderPayloadError,
): ProtocolMethodError =>
  createInvalidProviderPayloadError({
    agentId: error.agentId,
    provider: error.provider,
    operation: error.operation,
    providerMessage: error.message,
  });

export const createActiveTurnConflictProtocolError = (
  error: ActiveTurnConflictError,
): ProtocolMethodError => createActiveTurnAlreadyExistsError(error.threadId, error.activeTurnId);

export const createActiveTurnNotFoundProtocolError = (
  error: ActiveTurnNotFoundError,
): ProtocolMethodError => createActiveTurnNotFoundError(error.threadId);

export const createActiveTurnMismatchProtocolError = (
  error: ActiveTurnMismatchError,
): ProtocolMethodError =>
  createActiveTurnMismatchError(error.threadId, error.requestedTurnId, error.activeTurnId);

const createInvalidProviderPayloadServiceError = (
  error: AgentInvalidMessageError,
  operation: InvalidTurnProviderPayloadError["operation"],
): InvalidTurnProviderPayloadError =>
  Object.freeze({
    type: "invalidProviderPayload",
    agentId: error.agentId,
    provider: error.provider,
    operation,
    message: error.message,
    ...(error.detail ? { detail: error.detail } : {}),
  });

const getValidatedActiveTurn = (
  options: CreateTurnsServiceOptions,
  threadId: string,
  requestedTurnId: string,
) => {
  const activeTurn = options.activeTurns.getActiveTurn(threadId)?.turn;

  if (
    activeTurn === undefined ||
    (activeTurn.status.type !== "inProgress" && activeTurn.status.type !== "awaitingInput")
  ) {
    return err<ActiveTurnNotFoundError>({
      type: "activeTurnNotFound",
      threadId,
      message: "Thread does not have an active turn.",
    });
  }

  if (activeTurn.id !== requestedTurnId) {
    return err<ActiveTurnMismatchError>({
      type: "activeTurnMismatch",
      threadId,
      requestedTurnId,
      activeTurnId: activeTurn.id,
      message: "Requested turn is not the active turn.",
    });
  }

  return ok(activeTurn);
};

const mapTurnStatus = (status: AgentTurnStatus) => {
  switch (status.type) {
    case "inProgress":
      return Object.freeze({ type: "inProgress" as const });
    case "awaitingInput":
      return Object.freeze({ type: "awaitingInput" as const });
    case "completed":
      return Object.freeze({ type: "completed" as const });
    case "cancelled":
      return Object.freeze({ type: "cancelled" as const });
    case "interrupted":
      return Object.freeze({ type: "interrupted" as const });
    case "failed":
      return Object.freeze({
        type: "failed" as const,
        ...(status.message ? { message: status.message } : {}),
      });
    default:
      return assertNever(status, "Unhandled turn status");
  }
};
