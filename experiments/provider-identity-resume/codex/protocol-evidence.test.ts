import assert from "node:assert/strict";
import test from "node:test";

import {
  threadIdsFromList,
  threadIdsFromLoadedList,
  turnIdsContainingAgentMarker,
  verifyInputQuestion,
  verifyExactPwdApproval,
  verifyNoReplacementThreads,
  verifyServerRequestAttribution,
  verifyThreadContainsAgentMarker,
  verifyThreadHasNoTurns,
  verifyTurnEventAttribution,
  verifyWaitingStatus,
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
      params: { threadId: "thread-a", turn: { id: "turn-a" } },
    },
    {
      method: "item/completed",
      params: { threadId: "thread-a", turnId: "turn-a" },
    },
    {
      method: "turn/completed",
      params: { threadId: "thread-a", turn: { id: "turn-a" } },
    },
  ];
  assert.equal(
    verifyTurnEventAttribution(events, "thread-a", "turn-a", "turn a"),
    events.length,
  );
  assert.throws(
    () =>
      verifyTurnEventAttribution(
        [
          ...events,
          {
            method: "item/completed",
            params: { threadId: "thread-b", turnId: "turn-a" },
          },
        ],
        "thread-a",
        "turn-a",
        "turn a",
      ),
    /another thread/,
  );
});

test("ignores lifecycle events for another loaded thread", () => {
  const eventCount = verifyTurnEventAttribution(
    [
      {
        method: "mcpServer/startupStatus/updated",
        params: { threadId: "thread-b" },
      },
      {
        method: "turn/started",
        params: { threadId: "thread-a", turn: { id: "turn-a" } },
      },
      {
        method: "turn/completed",
        params: { threadId: "thread-a", turn: { id: "turn-a" } },
      },
    ],
    "thread-a",
    "turn-a",
    "turn a",
  );
  assert.equal(eventCount, 2);
});

test("finds a native TUI marker in resumed agent history", () => {
  const matchingTurns = verifyThreadContainsAgentMarker(
    {
      turns: [
        {
          id: "native-turn",
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
  assert.deepEqual(
    turnIdsContainingAgentMarker(
      {
        turns: [
          {
            id: "turn-a",
            items: [
              { type: "agentMessage", text: "marker ATC-TUI-TEST" },
            ],
          },
        ],
      },
      "ATC-TUI-TEST",
    ),
    ["turn-a"],
  );
  assert.throws(
    () =>
      verifyThreadContainsAgentMarker(
        { turns: [{ id: "missing-turn", items: [] }] },
        "ATC-TUI-MISSING",
        "resumed thread",
      ),
    /did not contain agent marker/,
  );
});

test("verifies provider-owned waiting status flags", () => {
  const event = {
    method: "thread/status/changed",
    params: {
      threadId: "thread-a",
      status: {
        type: "active",
        activeFlags: ["waitingOnUserInput"],
      },
    },
  };
  assert.doesNotThrow(() =>
    verifyWaitingStatus(
      event,
      "thread-a",
      "waitingOnUserInput",
      "input wait",
    ),
  );
  assert.throws(
    () =>
      verifyWaitingStatus(
        event,
        "thread-a",
        "waitingOnApproval",
        "approval wait",
      ),
    /waitingOnApproval/,
  );
});

test("verifies server request and turn attribution", () => {
  const request = {
    id: 9,
    method: "item/tool/requestUserInput",
    params: {
      threadId: "thread-a",
      turnId: "turn-a",
      itemId: "item-a",
    },
    sequence: 14,
    receivedAt: "2026-07-27T00:00:00.000Z",
  };
  assert.equal(
    verifyServerRequestAttribution(
      request,
      "thread-a",
      "item/tool/requestUserInput",
      "input request",
    ),
    "turn-a",
  );
  assert.throws(
    () =>
      verifyServerRequestAttribution(
        request,
        "thread-b",
        "item/tool/requestUserInput",
        "input request",
      ),
    /thread-b/,
  );
});

test("verifies the exact input question and ordered options", () => {
  const request = {
    id: 9,
    method: "item/tool/requestUserInput",
    params: {
      questions: [
        {
          id: "choice",
          options: [
            { label: "Alpha", description: "first" },
            { label: "Beta", description: "second" },
          ],
        },
      ],
    },
    sequence: 14,
    receivedAt: "2026-07-27T00:00:00.000Z",
  };
  assert.doesNotThrow(() =>
    verifyInputQuestion(request, "choice", ["Alpha", "Beta"]),
  );
  assert.throws(
    () => verifyInputQuestion(request, "choice", ["Beta", "Alpha"]),
    /option mismatch/,
  );
});

test("verifies exact pwd approval despite provider shell normalization", () => {
  const request = {
    id: 0,
    method: "item/commandExecution/requestApproval",
    params: {
      command: "/bin/zsh -lc pwd",
      commandActions: [{ type: "unknown", command: "pwd" }],
    },
    sequence: 18,
    receivedAt: "2026-07-27T00:00:00.000Z",
  };
  assert.equal(verifyExactPwdApproval(request), "/bin/zsh -lc pwd");
  assert.throws(
    () =>
      verifyExactPwdApproval({
        ...request,
        params: {
          command: "/bin/zsh -lc 'pwd; whoami'",
          commandActions: [{ type: "unknown", command: "pwd; whoami" }],
        },
      }),
    /command mismatch/,
  );
});
