import assert from "node:assert/strict";
import { test } from "node:test";

import {
  labelOf,
  requireExternalSuccess,
  summarizeExternal,
} from "./held-connection.ts";

const externalTurn = {
  exitCode: 0,
  durationMs: 2_899,
  sessionId: "b8d11031-856c-4454-b16e-4f8a31761bd0",
  resultSubtype: "success",
  resultText: "EXTERNAL TURN ONE: SEED-MARKER EXT1-MARKER",
  rawStdout: "{}",
  rawStderr: "",
};

test("requireExternalSuccess accepts a same-session marker-recalling turn", () => {
  requireExternalSuccess(
    externalTurn,
    "b8d11031-856c-4454-b16e-4f8a31761bd0",
    "SEED-MARKER",
    "external turn",
  );
});

test("requireExternalSuccess rejects a forked session identity", () => {
  assert.throws(
    () =>
      requireExternalSuccess(
        { ...externalTurn, sessionId: "another-session" },
        "b8d11031-856c-4454-b16e-4f8a31761bd0",
        "SEED-MARKER",
        "external turn",
      ),
    /did not preserve session identity/,
  );
});

test("requireExternalSuccess rejects a missing marker and nonzero exit", () => {
  assert.throws(
    () =>
      requireExternalSuccess(
        { ...externalTurn, resultText: "no markers here" },
        "b8d11031-856c-4454-b16e-4f8a31761bd0",
        "SEED-MARKER",
        "external turn",
      ),
    /did not include expected marker/,
  );
  assert.throws(
    () =>
      requireExternalSuccess(
        { ...externalTurn, exitCode: 1 },
        "b8d11031-856c-4454-b16e-4f8a31761bd0",
        "SEED-MARKER",
        "external turn",
      ),
    /failed with exit code 1/,
  );
});

test("summarizeExternal keeps only the reviewable fields", () => {
  assert.deepEqual(summarizeExternal(externalTurn), {
    exitCode: 0,
    durationMs: 2_899,
    sessionId: "b8d11031-856c-4454-b16e-4f8a31761bd0",
    resultSubtype: "success",
    resultText: "EXTERNAL TURN ONE: SEED-MARKER EXT1-MARKER",
  });
});

test("labelOf renders session-state labels with their state", () => {
  assert.equal(
    labelOf({
      type: "system",
      subtype: "session_state_changed",
      state: "idle",
    } as never),
    "system/session_state_changed state=idle",
  );
  assert.equal(labelOf({ type: "assistant" } as never), "assistant");
});
