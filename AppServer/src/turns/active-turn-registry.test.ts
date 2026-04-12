import { describe, expect, test } from "bun:test";
import { createActiveTurnRegistry } from "@/turns/active-turn-registry";

describe("active turn registry", () => {
  test("reconciles streamed item state against the final completed item payload", () => {
    const registry = createActiveTurnRegistry();

    registry.startTurn({
      threadId: "thread-1",
      turn: {
        id: "turn-1",
        status: {
          type: "inProgress",
        },
      },
    });
    registry.recordItemStarted({
      threadId: "thread-1",
      turnId: "turn-1",
      item: {
        id: "item-1",
        type: "agentMessage",
        text: "",
        phase: null,
      },
    });
    registry.appendMessageText({
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-1",
      delta: "Hello",
    });
    registry.appendMessageText({
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-1",
      delta: " world",
    });
    registry.recordItemCompleted({
      threadId: "thread-1",
      turnId: "turn-1",
      item: {
        id: "item-1",
        type: "agentMessage",
        text: "Hello world",
        phase: null,
      },
    });

    expect(registry.getActiveTurn("thread-1")).toEqual({
      threadId: "thread-1",
      turn: {
        id: "turn-1",
        status: {
          type: "inProgress",
        },
      },
      items: [
        {
          item: {
            id: "item-1",
            type: "agentMessage",
            text: "Hello world",
            phase: null,
          },
          messageText: "Hello world",
          reasoningText: "",
          reasoningSummaryText: "",
          commandOutput: "",
          toolProgress: [],
        },
      ],
    });
  });

  test("clears per-thread state", () => {
    const registry = createActiveTurnRegistry();

    registry.startTurn({
      threadId: "thread-1",
      turn: {
        id: "turn-1",
        status: {
          type: "inProgress",
        },
      },
    });

    expect(registry.clearThread("thread-1")).toEqual({
      threadId: "thread-1",
      turn: {
        id: "turn-1",
        status: {
          type: "inProgress",
        },
      },
      items: [],
    });
    expect(registry.getActiveTurn("thread-1")).toBeUndefined();
  });

  test("rejects reserving a second active turn for the same thread", () => {
    const registry = createActiveTurnRegistry();

    expect(registry.reserveThread("thread-1")).toEqual({
      ok: true,
      data: {
        release: expect.any(Function),
      },
    });
    expect(registry.reserveThread("thread-1")).toEqual({
      ok: false,
      error: {
        type: "activeTurnConflict",
        threadId: "thread-1",
        message: "Thread already has an active turn.",
      },
    });
  });

  test("ignores mismatched turn ids instead of clobbering active state", () => {
    const registry = createActiveTurnRegistry();

    registry.startTurn({
      threadId: "thread-1",
      turn: {
        id: "turn-1",
        status: {
          type: "inProgress",
        },
      },
    });
    registry.recordItemStarted({
      threadId: "thread-1",
      turnId: "turn-1",
      item: {
        id: "item-1",
        type: "agentMessage",
        text: "",
        phase: null,
      },
    });

    expect(
      registry.appendMessageText({
        threadId: "thread-1",
        turnId: "turn-2",
        itemId: "item-2",
        delta: "late stray",
      }),
    ).toBe(false);
    expect(
      registry.recordItemCompleted({
        threadId: "thread-1",
        turnId: "turn-2",
        item: {
          id: "item-2",
          type: "agentMessage",
          text: "",
          phase: null,
        },
      }),
    ).toBe(false);

    expect(registry.getActiveTurn("thread-1")).toEqual({
      threadId: "thread-1",
      turn: {
        id: "turn-1",
        status: {
          type: "inProgress",
        },
      },
      items: [
        {
          item: {
            id: "item-1",
            type: "agentMessage",
            text: "",
            phase: null,
          },
          messageText: "",
          reasoningText: "",
          reasoningSummaryText: "",
          commandOutput: "",
          toolProgress: [],
        },
      ],
    });
  });
});
