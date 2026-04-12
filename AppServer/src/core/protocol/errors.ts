import type { ProtocolErrorData } from "@/core/protocol/schemas";
import { err, type Result } from "@/core/shared";

export const JSON_RPC_PARSE_ERROR = -32700;
export const JSON_RPC_INVALID_REQUEST_ERROR = -32600;
export const JSON_RPC_METHOD_NOT_FOUND_ERROR = -32601;
export const JSON_RPC_INVALID_PARAMS_ERROR = -32602;
export const JSON_RPC_INTERNAL_ERROR = -32603;

// Atelier-owned protocol errors reserve the `-33000` to `-33099` range.
// Keep the registry centralized in this module to avoid collisions as more
// feature methods add domain-specific protocol failures.
export const ATELIER_SESSION_ALREADY_INITIALIZED_ERROR = -33000;
export const ATELIER_SESSION_NOT_INITIALIZED_ERROR = -33001;
export const ATELIER_WORKSPACE_PATH_NOT_FOUND_ERROR = -33002;
export const ATELIER_WORKSPACE_PATH_NOT_DIRECTORY_ERROR = -33003;
export const ATELIER_AGENT_SESSION_UNAVAILABLE_ERROR = -33004;
export const ATELIER_PROVIDER_ERROR = -33005;
export const ATELIER_WORKSPACE_NOT_OPENED_ERROR = -33006;
export const ATELIER_THREAD_WORKSPACE_MISMATCH_ERROR = -33008;
export const ATELIER_INVALID_PROVIDER_PAYLOAD_ERROR = -33009;
export const ATELIER_THREAD_NOT_LOADED_ERROR = -33010;
export const ATELIER_ACTIVE_TURN_ALREADY_EXISTS_ERROR = -33011;

const PROTOCOL_METHOD_ERROR_BRAND = Symbol("ProtocolMethodError");

export type ProtocolMethodError = Readonly<{
  code: number;
  message: string;
  data?: ProtocolErrorData;
}> & {
  readonly [PROTOCOL_METHOD_ERROR_BRAND]: true;
};

export const createProtocolMethodError = (
  code: number,
  message: string,
  data?: ProtocolErrorData,
): ProtocolMethodError =>
  Object.freeze(
    brandProtocolMethodError(data === undefined ? { code, message } : { code, message, data }),
  );

export const isProtocolMethodError = (error: unknown): error is ProtocolMethodError =>
  typeof error === "object" &&
  error !== null &&
  PROTOCOL_METHOD_ERROR_BRAND in error &&
  (error as { [PROTOCOL_METHOD_ERROR_BRAND]?: unknown })[PROTOCOL_METHOD_ERROR_BRAND] === true;

export const createParseError = (): ProtocolMethodError =>
  createProtocolMethodError(JSON_RPC_PARSE_ERROR, "Parse error");

export const createInvalidRequestError = (): ProtocolMethodError =>
  createProtocolMethodError(JSON_RPC_INVALID_REQUEST_ERROR, "Invalid request");

export const createMethodNotFoundError = (): ProtocolMethodError =>
  createProtocolMethodError(JSON_RPC_METHOD_NOT_FOUND_ERROR, "Method not found");

export const createInvalidParamsError = (): ProtocolMethodError =>
  createProtocolMethodError(JSON_RPC_INVALID_PARAMS_ERROR, "Invalid params");

export const createInternalError = (): ProtocolMethodError =>
  createProtocolMethodError(
    JSON_RPC_INTERNAL_ERROR,
    "Internal error",
    Object.freeze({
      code: "INTERNAL_ERROR",
    }),
  );

export const createSessionAlreadyInitializedError = (): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_SESSION_ALREADY_INITIALIZED_ERROR,
    "Session already initialized",
    Object.freeze({
      code: "SESSION_ALREADY_INITIALIZED",
    }),
  );

export const createSessionAlreadyInitializedResult = (): Result<never, ProtocolMethodError> =>
  err(createSessionAlreadyInitializedError());

export const createSessionNotInitializedError = (): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_SESSION_NOT_INITIALIZED_ERROR,
    "Session not initialized",
    Object.freeze({
      code: "SESSION_NOT_INITIALIZED",
    }),
  );

export const createSessionNotInitializedResult = (): Result<never, ProtocolMethodError> =>
  err(createSessionNotInitializedError());

export const createWorkspacePathNotFoundError = (workspacePath: string): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_WORKSPACE_PATH_NOT_FOUND_ERROR,
    "Workspace path does not exist",
    Object.freeze({
      code: "WORKSPACE_PATH_NOT_FOUND",
      workspacePath,
    }),
  );

export const createWorkspacePathNotFoundResult = (
  workspacePath: string,
): Result<never, ProtocolMethodError> => err(createWorkspacePathNotFoundError(workspacePath));

export const createWorkspacePathNotDirectoryError = (workspacePath: string): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_WORKSPACE_PATH_NOT_DIRECTORY_ERROR,
    "Workspace path is not a directory",
    Object.freeze({
      code: "WORKSPACE_PATH_NOT_DIRECTORY",
      workspacePath,
    }),
  );

export const createWorkspacePathNotDirectoryResult = (
  workspacePath: string,
): Result<never, ProtocolMethodError> => err(createWorkspacePathNotDirectoryError(workspacePath));

export const createWorkspaceNotOpenedError = (): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_WORKSPACE_NOT_OPENED_ERROR,
    "Workspace not opened",
    Object.freeze({
      code: "WORKSPACE_NOT_OPENED",
    }),
  );

export const createWorkspaceNotOpenedResult = (): Result<never, ProtocolMethodError> =>
  err(createWorkspaceNotOpenedError());

export const createThreadWorkspaceMismatchError = (
  threadId: string,
  openedWorkspacePath: string,
  threadWorkspacePath: string,
): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_THREAD_WORKSPACE_MISMATCH_ERROR,
    "Thread does not belong to the opened workspace",
    Object.freeze({
      code: "THREAD_WORKSPACE_MISMATCH",
      threadId,
      openedWorkspacePath,
      threadWorkspacePath,
    }),
  );

export const createThreadWorkspaceMismatchResult = (
  threadId: string,
  openedWorkspacePath: string,
  threadWorkspacePath: string,
): Result<never, ProtocolMethodError> =>
  err(createThreadWorkspaceMismatchError(threadId, openedWorkspacePath, threadWorkspacePath));

export const createInvalidProviderPayloadError = (
  input: Readonly<{
    agentId: string;
    provider: string;
    operation: string;
    providerMessage: string;
  }>,
): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_INVALID_PROVIDER_PAYLOAD_ERROR,
    "Provider returned an invalid payload",
    Object.freeze({
      code: "INVALID_PROVIDER_PAYLOAD",
      agentId: input.agentId,
      provider: input.provider,
      operation: input.operation,
      providerMessage: input.providerMessage,
    }),
  );

export const createThreadNotLoadedForConnectionError = (threadId: string): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_THREAD_NOT_LOADED_ERROR,
    "Thread is not loaded for this connection",
    Object.freeze({
      code: "THREAD_NOT_LOADED",
      threadId,
    }),
  );

export const createThreadNotLoadedForConnectionResult = (
  threadId: string,
): Result<never, ProtocolMethodError> => err(createThreadNotLoadedForConnectionError(threadId));

export const createActiveTurnAlreadyExistsError = (
  threadId: string,
  activeTurnId?: string,
): ProtocolMethodError =>
  createProtocolMethodError(
    ATELIER_ACTIVE_TURN_ALREADY_EXISTS_ERROR,
    "Thread already has an active turn",
    Object.freeze({
      code: "TURN_ALREADY_ACTIVE",
      threadId,
      ...(activeTurnId ? { activeTurnId } : {}),
    }),
  );

export const createActiveTurnAlreadyExistsResult = (
  threadId: string,
  activeTurnId?: string,
): Result<never, ProtocolMethodError> =>
  err(createActiveTurnAlreadyExistsError(threadId, activeTurnId));

const brandProtocolMethodError = <T extends { code: number; message: string }>(
  error: T,
): T & { readonly [PROTOCOL_METHOD_ERROR_BRAND]: true } => {
  Object.defineProperty(error, PROTOCOL_METHOD_ERROR_BRAND, {
    value: true,
    enumerable: false,
  });

  return error as T & { readonly [PROTOCOL_METHOD_ERROR_BRAND]: true };
};
