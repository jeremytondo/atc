import assert from "node:assert/strict";
import test from "node:test";

import { ClaudeControlTimeline } from "./control-timeline.ts";

test("control timeline preserves callback and provider observation order", () => {
  const timeline = new ClaudeControlTimeline();
  timeline.recordRequest({
    requestId: "request-1",
    toolUseId: "tool-1",
    toolName: "AskUserQuestion",
    input: { questions: [] },
  });
  timeline.recordMessage(4, {
    type: "system",
    subtype: "session_state_changed",
    state: "requires_action",
    uuid: "00000000-0000-4000-8000-000000000001",
    session_id: "session-1",
  });
  timeline.recordResponse({
    requestId: "request-1",
    toolUseId: "tool-1",
    toolName: "AskUserQuestion",
    result: {
      behavior: "allow",
      updatedInput: { questions: [], answers: {} },
    },
  });

  assert.deepEqual(
    timeline.events.map((event) => ({
      sequence: event.sequence,
      source: event.source,
      event: event.event,
      state: "state" in event ? event.state : undefined,
    })),
    [
      {
        sequence: 1,
        source: "callback",
        event: "request",
        state: undefined,
      },
      {
        sequence: 2,
        source: "sdk",
        event: "message",
        state: "requires_action",
      },
      {
        sequence: 3,
        source: "callback",
        event: "response",
        state: undefined,
      },
    ],
  );
  assert.match(timeline.toJsonLines(), /"requires_action"/);
});
