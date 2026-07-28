import assert from "node:assert/strict";
import { test } from "node:test";

import {
  isUuid,
  observeHeldWindow,
  summarizeThreadRead,
} from "./held-connection.ts";

test("observeHeldWindow classifies thread-scoped messages", () => {
  const observation = observeHeldWindow(
    [
      { method: "thread/status/changed", params: { threadId: "t-1" } },
      { method: "mcpServer/startupStatus/updated", params: { threadId: "t-2" } },
      { id: 7, result: {} },
    ],
    "t-1",
  );
  assert.equal(observation.messageCount, 3);
  assert.deepEqual(observation.methods, [
    "thread/status/changed",
    "mcpServer/startupStatus/updated",
    "response",
  ]);
  assert.equal(observation.threadScopedCount, 1);
});

test("observeHeldWindow reports an empty window", () => {
  const observation = observeHeldWindow([], "t-1");
  assert.equal(observation.messageCount, 0);
  assert.equal(observation.threadScopedCount, 0);
  assert.deepEqual(observation.methods, []);
});

test("summarizeThreadRead reports turn count and marker visibility", () => {
  const summary = summarizeThreadRead(
    {
      ok: true,
      result: {
        thread: {
          turns: [
            { id: "turn-1", items: [{ type: "agentMessage", text: "M-ONE" }] },
            { id: "turn-2", items: [{ type: "agentMessage", text: "M-TWO" }] },
          ],
        },
      },
    },
    ["M-ONE", "M-TWO", "M-MISSING"],
  );
  assert.deepEqual(summary, {
    turnCount: 2,
    markerVisibility: {
      "M-ONE": true,
      "M-TWO": true,
      "M-MISSING": false,
    },
  });
});

test("summarizeThreadRead handles a rejected request wrapper", () => {
  const summary = summarizeThreadRead(
    { ok: false, error: "thread/read failed" },
    ["M-ONE"],
  );
  assert.deepEqual(summary, {
    turnCount: 0,
    markerVisibility: { "M-ONE": false },
  });
});

test("isUuid accepts provider turn IDs and rejects synthetic rollout turns", () => {
  assert.equal(isUuid("019fa9de-ab77-7843-9be7-34b063f194dd"), true);
  assert.equal(isUuid("rollout-36"), false);
  assert.equal(isUuid(""), false);
});
