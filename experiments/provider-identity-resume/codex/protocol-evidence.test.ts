import assert from "node:assert/strict";
import test from "node:test";

import {
  threadIdsFromList,
  threadIdsFromLoadedList,
  verifyNoReplacementThreads,
  verifyThreadContainsAgentMarker,
  verifyThreadHasNoTurns,
  verifyTurnEventAttribution,
} from "./protocol-evidence.ts";

test("extracts IDs from thread list responses", () => {
  assert.deepEqual(
    threadIdsFromList({ data: [{ id: "thread-a" }, { id: "thread-b" }] }),
    ["thread-a", "thread-b"],
  );
  assert.deepEqual(
    threadIdsFromLoadedList({ data: ["thread-a", "thread-b"] }),
    ["thread-a", "thread-b"],
  );
});

test("detects replacement threads after a failed resume", () => {
  assert.doesNotThrow(() =>
    verifyNoReplacementThreads(["thread-a"], ["thread-a"]),
  );
  assert.throws(
    () =>
      verifyNoReplacementThreads(
        ["thread-a"],
        ["thread-a", "replacement-thread"],
      ),
    /replacement thread/,
  );
});

test("requires a zero-turn thread before recovery", () => {
  assert.doesNotThrow(() =>
    verifyThreadHasNoTurns({ turns: [] }, "recovered thread"),
  );
  assert.throws(
    () =>
      verifyThreadHasNoTurns(
        { turns: [{ id: "unexpected-turn" }] },
        "recovered thread",
      ),
    /unexpectedly included 1 turn/,
  );
});

test("verifies every turn notification is attributed to one thread", () => {
  const events = [
    {
      method: "turn/started",
      params: { threadId: "thread-a" },
    },
    {
      method: "item/completed",
      params: { threadId: "thread-a" },
    },
    {
      method: "turn/completed",
      params: { threadId: "thread-a" },
    },
  ];
  assert.equal(
    verifyTurnEventAttribution(events, "thread-a", "turn a"),
    events.length,
  );
  assert.throws(
    () =>
      verifyTurnEventAttribution(
        [
          ...events,
          {
            method: "item/completed",
            params: { threadId: "thread-b" },
          },
        ],
        "thread-a",
        "turn a",
      ),
    /another thread/,
  );
});

test("finds a native TUI marker in resumed agent history", () => {
  const matchingTurns = verifyThreadContainsAgentMarker(
    {
      turns: [
        {
          items: [
            {
              type: "agentMessage",
              text: "NATIVE TUI ROUND TRIP: ATC-TUI-TEST",
            },
          ],
        },
      ],
    },
    "ATC-TUI-TEST",
    "resumed thread",
  );
  assert.equal(matchingTurns, 1);
  assert.throws(
    () =>
      verifyThreadContainsAgentMarker(
        { turns: [{ items: [] }] },
        "ATC-TUI-MISSING",
        "resumed thread",
      ),
    /did not contain agent marker/,
  );
});
