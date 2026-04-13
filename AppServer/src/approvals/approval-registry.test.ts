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

  test("clears a single request without disturbing others", () => {
    const registry = createApprovalRegistry();
    registry.recordRequested(createCommandApproval("approval-1"));
    registry.recordRequested(createCommandApproval("approval-2"));

    expect(registry.clearRequest("approval-1")).toEqual({
      approval: createCommandApproval("approval-1"),
      state: "pending",
    });
    expect(registry.getPending("approval-1")).toBeUndefined();
    expect(registry.getPending("approval-2")).toEqual({
      approval: createCommandApproval("approval-2"),
      state: "pending",
    });
  });

  test("clears approvals for an entire thread", () => {
    const registry = createApprovalRegistry();
    registry.recordRequested(createCommandApproval("approval-1"));
    registry.recordRequested(
      Object.freeze({
        ...createCommandApproval("approval-2"),
        threadId: "thread-2",
      }),
    );

    expect(registry.clearThread("thread-1")).toEqual([
      {
        approval: createCommandApproval("approval-1"),
        state: "pending",
      },
    ]);
    expect(registry.getPending("approval-1")).toBeUndefined();
    expect(registry.getPending("approval-2")).toEqual({
      approval: Object.freeze({
        ...createCommandApproval("approval-2"),
        threadId: "thread-2",
      }),
      state: "pending",
    });
  });

  test("clears all tracked approvals", () => {
    const registry = createApprovalRegistry();
    registry.recordRequested(createCommandApproval("approval-1"));
    registry.recordRequested(createCommandApproval("approval-2"));

    expect(registry.clearAll()).toEqual([
      {
        approval: createCommandApproval("approval-1"),
        state: "pending",
      },
      {
        approval: createCommandApproval("approval-2"),
        state: "pending",
      },
    ]);
    expect(registry.getPending("approval-1")).toBeUndefined();
    expect(registry.getPending("approval-2")).toBeUndefined();
  });

  test("treats numeric and string request ids as distinct keys", () => {
    const registry = createApprovalRegistry();
    registry.recordRequested(
      Object.freeze({
        ...createCommandApproval("123"),
        requestId: 123,
      }),
    );
    registry.recordRequested(createCommandApproval("123"));

    expect(registry.getPending(123)).toEqual({
      approval: Object.freeze({
        ...createCommandApproval("123"),
        requestId: 123,
      }),
      state: "pending",
    });
    expect(registry.getPending("123")).toEqual({
      approval: createCommandApproval("123"),
      state: "pending",
    });
  });
});
