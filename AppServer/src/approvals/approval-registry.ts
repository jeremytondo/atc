import type { ApprovalDecisionResolution, ApprovalRequest } from "@/approvals/schemas";
import type { RequestId } from "@/core/protocol";

export type ApprovalRegistryRecord =
  | Readonly<{
      approval: ApprovalRequest;
      state: "pending";
    }>
  | Readonly<{
      approval: ApprovalRequest;
      state: "resolved";
      resolution: ApprovalDecisionResolution;
    }>;

export type ApprovalRegistry = Readonly<{
  recordRequested: (approval: ApprovalRequest) => ApprovalRegistryRecord;
  getPending: (requestId: RequestId) => ApprovalRegistryRecord | undefined;
  markResolved: (
    requestId: RequestId,
    resolution: ApprovalDecisionResolution,
  ) => ApprovalRegistryRecord | undefined;
  clearRequest: (requestId: RequestId) => ApprovalRegistryRecord | undefined;
  clearTurn: (
    input: Readonly<{ threadId: string; turnId: string }>,
  ) => readonly ApprovalRegistryRecord[];
  clearThread: (threadId: string) => readonly ApprovalRegistryRecord[];
  clearAll: () => readonly ApprovalRegistryRecord[];
}>;

export const createApprovalRegistry = (): ApprovalRegistry => {
  const recordsByRequestId = new Map<string, ApprovalRegistryRecord>();

  return Object.freeze({
    recordRequested: (approval) => {
      const record = Object.freeze({
        approval,
        state: "pending" as const,
      });
      recordsByRequestId.set(requestIdKey(approval.requestId), record);
      return record;
    },
    getPending: (requestId) => {
      const record = recordsByRequestId.get(requestIdKey(requestId));
      if (record?.state !== "pending") {
        return undefined;
      }

      return record;
    },
    markResolved: (requestId, resolution) => {
      const record = recordsByRequestId.get(requestIdKey(requestId));
      if (record?.state !== "pending") {
        return undefined;
      }

      const resolvedRecord = Object.freeze({
        approval: record.approval,
        state: "resolved" as const,
        resolution,
      });
      recordsByRequestId.set(requestIdKey(requestId), resolvedRecord);
      return resolvedRecord;
    },
    clearRequest: (requestId) => {
      const key = requestIdKey(requestId);
      const record = recordsByRequestId.get(key);
      if (record === undefined) {
        return undefined;
      }

      recordsByRequestId.delete(key);
      return record;
    },
    clearTurn: ({ threadId, turnId }) =>
      clearMatchingRecords(recordsByRequestId, (record) => {
        return record.approval.threadId === threadId && record.approval.turnId === turnId;
      }),
    clearThread: (threadId) =>
      clearMatchingRecords(recordsByRequestId, (record) => record.approval.threadId === threadId),
    clearAll: () => clearMatchingRecords(recordsByRequestId, () => true),
  });
};

const clearMatchingRecords = (
  recordsByRequestId: Map<string, ApprovalRegistryRecord>,
  predicate: (record: ApprovalRegistryRecord) => boolean,
): readonly ApprovalRegistryRecord[] => {
  const clearedRecords: ApprovalRegistryRecord[] = [];

  for (const [key, record] of recordsByRequestId.entries()) {
    if (!predicate(record)) {
      continue;
    }

    recordsByRequestId.delete(key);
    clearedRecords.push(record);
  }

  return Object.freeze(clearedRecords);
};

const requestIdKey = (requestId: RequestId): string => `${typeof requestId}:${String(requestId)}`;
