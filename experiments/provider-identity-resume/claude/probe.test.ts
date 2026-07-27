import assert from "node:assert/strict";
import { mkdtempSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  assertSessionRecord,
  parseArgs,
  verifyContinuity,
} from "./probe.ts";

const session = {
  version: 1 as const,
  provider: "claude" as const,
  sessionId: "session-123",
  marker: "ATC-CLAUDE-TEST",
  cwd: "/tmp/project",
  createdAt: "2026-07-27T00:00:00.000Z",
  claudeCodeVersion: "2.1.220",
  identityFirstSeen: {
    sequence: 1,
    type: "system",
    subtype: "init",
  },
  createLogs: ["turn-1.jsonl", "turn-2.jsonl"],
};

test("parseArgs requires a session artifact for resume", () => {
  assert.throws(() => parseArgs(["resume"]), /requires --session/);
});

test("parseArgs canonicalizes the requested cwd", () => {
  const directory = realpathSync(mkdtempSync(join(tmpdir(), "atc-claude-")));
  const options = parseArgs(["create", "--cwd", directory]);
  assert.equal(options.cwd, directory);
  assert.equal(options.command, "create");
});

test("parseArgs configures a positive timeout", () => {
  const options = parseArgs(["create", "--timeout-seconds", "12.5"]);
  assert.equal(options.timeoutMs, 12_500);
});

test("parseArgs rejects a non-positive timeout", () => {
  assert.throws(
    () => parseArgs(["create", "--timeout-seconds", "0"]),
    /positive number/,
  );
});

test("assertSessionRecord accepts a complete Claude artifact", () => {
  assert.equal(assertSessionRecord(session).sessionId, session.sessionId);
});

test("assertSessionRecord rejects an incomplete artifact", () => {
  assert.throws(
    () => assertSessionRecord({ provider: "claude" }),
    /invalid shape/,
  );
});

test("verifyContinuity accepts exact identity and marker evidence", () => {
  assert.doesNotThrow(() =>
    verifyContinuity(
      {
        sessionId: session.sessionId,
        cwd: session.cwd,
        claudeCodeVersion: session.claudeCodeVersion,
        firstIdentity: session.identityFirstSeen,
        messageCount: 3,
        resultText: `MARKER STORED: ${session.marker}`,
        stateTransitions: ["running", "idle"],
      },
      session.sessionId,
      session.marker,
    ),
  );
});

test("verifyContinuity rejects a replacement session", () => {
  assert.throws(
    () =>
      verifyContinuity(
        {
          sessionId: "replacement-session",
          cwd: session.cwd,
          claudeCodeVersion: session.claudeCodeVersion,
          firstIdentity: session.identityFirstSeen,
          messageCount: 3,
          resultText: session.marker,
          stateTransitions: [],
        },
        session.sessionId,
        session.marker,
      ),
    /Identity mismatch/,
  );
});

test("verifyContinuity rejects missing marker context", () => {
  assert.throws(
    () =>
      verifyContinuity(
        {
          sessionId: session.sessionId,
          cwd: session.cwd,
          claudeCodeVersion: session.claudeCodeVersion,
          firstIdentity: session.identityFirstSeen,
          messageCount: 3,
          resultText: "I do not remember.",
          stateTransitions: [],
        },
        session.sessionId,
        session.marker,
      ),
    /Continuity check failed/,
  );
});
