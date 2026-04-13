import { describe, expect, test } from "bun:test";
import { mapPublicThreadDetail } from "@/threads/response-mapper";

describe("mapPublicThreadDetail", () => {
  test("maps typed turn history without reusing mutable nested collections", () => {
    const sourceQueries = ["one", "two"];
    const sourceReceiverThreadIds = ["thread-2"];
    const sourceAgentsStates = {
      "thread-2": {
        status: "idle",
      },
    };

    const mapped = mapPublicThreadDetail(
      {
        id: "thread-1",
        preview: "Preview",
        createdAt: "2025-04-10T10:13:20.000Z",
        updatedAt: "2025-04-10T11:13:20.000Z",
        workspacePath: "/tmp/project",
        name: "Mapped thread",
        archived: false,
        status: { type: "idle" },
        turns: [
          {
            id: "turn-1",
            status: { type: "completed" },
            items: [
              {
                id: "item-web",
                type: "webSearch",
                query: "history",
                action: {
                  type: "search",
                  query: "history",
                  queries: sourceQueries,
                },
              },
              {
                id: "item-collab",
                type: "collabAgentToolCall",
                tool: "wait",
                status: "completed",
                senderThreadId: "thread-1",
                receiverThreadIds: sourceReceiverThreadIds,
                prompt: null,
                agentsStates: sourceAgentsStates,
              },
            ],
            error: null,
          },
        ],
      },
      {
        model: "gpt-5.4",
        reasoningEffort: "medium",
      },
    );

    expect(mapped.turns[0]?.items).toEqual([
      {
        id: "item-web",
        type: "webSearch",
        query: "history",
        action: {
          type: "search",
          query: "history",
          queries: ["one", "two"],
        },
      },
      {
        id: "item-collab",
        type: "collabAgentToolCall",
        tool: "wait",
        status: "completed",
        senderThreadId: "thread-1",
        receiverThreadIds: ["thread-2"],
        prompt: null,
        agentsStates: {
          "thread-2": {
            status: "idle",
          },
        },
      },
    ]);
    expect(mapped.turns[0]?.items[0]?.type).toBe("webSearch");
    if (
      mapped.turns[0]?.items[0]?.type !== "webSearch" ||
      mapped.turns[0].items[0].action === null
    ) {
      throw new Error("Expected a web search action.");
    }
    expect(mapped.turns[0].items[0].action.type).toBe("search");
    if (mapped.turns[0].items[0].action.type !== "search") {
      throw new Error("Expected a search action.");
    }
    expect(mapped.turns[0].items[0].action.queries).not.toBe(sourceQueries);
    expect(mapped.turns[0]?.items[1]?.type).toBe("collabAgentToolCall");
    if (mapped.turns[0]?.items[1]?.type !== "collabAgentToolCall") {
      throw new Error("Expected a collab agent tool call.");
    }
    expect(mapped.turns[0].items[1].receiverThreadIds).not.toBe(sourceReceiverThreadIds);
    expect(mapped.turns[0].items[1].agentsStates).not.toBe(sourceAgentsStates);
  });
});
