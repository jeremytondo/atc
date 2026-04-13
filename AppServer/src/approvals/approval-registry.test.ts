import { describe, expect, test } from "bun:test";
import { createApprovalRegistry } from "@/approvals/approval-registry";
import type { ApprovalCommandExecutionRequest } from "@/approvals/schemas";

const createCommandApproval = (requestId: string): ApprovalCommandExecutionRequest => ({
  requestId,
  kind: "commandExecution" as const,
  threadId: "thread-1",
  turnId: "turn-1",
  itemId: "item-1",
  supportedResolutions: ["approved", "declined", "cancelled"],
  approvalId: null,
  reason: null,
  command: "git status",
  cwd: "/tmp/project",
  commandActions: null,
});

describe("createApprovalRegistry", () => {
  test("tracks pending approvals and resolves them by request id", () => {
    const registry = createApprovalRegistry();
    registry.recordRequested(createCommandApproval("approval-1"));

    expect(registry.getPending("approval-1")).toEqual({
      approval: createCommandApproval("approval-1"),
      state: "pending",
    });

    expect(registry.markResolved("approval-1", "approved")).toEqual({
      approval: createCommandApproval("approval-1"),
      state: "resolved",
      resolution: "approved",
    });
    expect(registry.getPending("approval-1")).toBeUndefined();
  });

  test("clears only approvals scoped to the completed turn", () => {
    const registry = createApprovalRegistry();
    registry.recordRequested(createCommandApproval("approval-1"));
    registry.recordRequested(
      Object.freeze({
        ...createCommandApproval("approval-2"),
        turnId: "turn-2",
      }),
    );

    expect(registry.clearTurn({ threadId: "thread-1", turnId: "turn-1" })).toEqual([
      {
        approval: createCommandApproval("approval-1"),
        state: "pending",
      },
    ]);
    expect(registry.getPending("approval-1")).toBeUndefined();
    expect(registry.getPending("approval-2")).toEqual({
      approval: Object.freeze({
        ...createCommandApproval("approval-2"),
        turnId: "turn-2",
      }),
      state: "pending",
    });
  });
});
