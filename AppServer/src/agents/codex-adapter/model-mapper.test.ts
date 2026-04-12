import { describe, expect, test } from "bun:test";
import { mapCodexThread, mapCodexThreadDetail } from "@/agents/codex-adapter/model-mapper";

describe("mapCodexThread", () => {
  test("rejects negative provider timestamps instead of clamping them", () => {
    expect(() =>
      mapCodexThread({
        id: "thread-1",
        preview: "Thread preview",
        createdAt: -1,
        updatedAt: 1,
        name: null,
        status: { type: "idle" },
        cwd: "/tmp/project",
      }),
    ).toThrow("Codex thread createdAt must be a non-negative unix timestamp.");
  });

  test("maps typed turn history on thread detail responses", () => {
    expect(
      mapCodexThreadDetail({
        id: "thread-1",
        preview: "Thread preview",
        createdAt: 1,
        updatedAt: 2,
        name: null,
        status: { type: "idle" },
        cwd: "/tmp/project",
        turns: [
          {
            id: "turn-1",
            status: "completed",
            items: [
              {
                id: "item-1",
                type: "agentMessage",
                text: "History item",
                phase: null,
              },
            ],
            error: null,
          },
        ],
      }),
    ).toEqual({
      id: "thread-1",
      preview: "Thread preview",
      createdAt: "1970-01-01T00:00:01.000Z",
      updatedAt: "1970-01-01T00:00:02.000Z",
      workspacePath: "/tmp/project",
      name: null,
      archived: false,
      status: { type: "idle" },
      turns: [
        {
          id: "turn-1",
          status: { type: "completed" },
          items: [
            {
              id: "item-1",
              type: "agentMessage",
              text: "History item",
              phase: null,
            },
          ],
          error: null,
        },
      ],
    });
  });
});
