import assert from "node:assert/strict";
import { mkdtempSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  assertSessionRecord,
  parseArgs,
  requireResumeId,
  verifyInvalidResumeFailure,
  verifyNoReplacementSessions,
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

test("parseArgs accepts the invalid-resume gate command", () => {
  assert.equal(parseArgs(["invalid-resume"]).command, "invalid-resume");
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

test("requireResumeId rejects missing IDs before starting the SDK", () => {
  assert.throws(
    () => requireResumeId(undefined),
    /refusing to start a query that would create a new session/,
  );
  assert.throws(() => requireResumeId("  "), /Missing Claude resume ID/);
});

test("requireResumeId accepts an explicit ID", () => {
  assert.equal(requireResumeId("session-123"), "session-123");
});

test("verifyNoReplacementSessions accepts unchanged provider inventory", () => {
  assert.doesNotThrow(() =>
    verifyNoReplacementSessions(
      ["session-1", "session-2"],
      ["session-1", "session-2"],
      "invalid-session",
      {
        error: "No conversation found",
        messageCount: 0,
        observedSessionIds: [],
        resultSubtype: "error_during_execution",
      },
    ),
  );
});

test("verifyNoReplacementSessions rejects a newly listed session", () => {
  assert.throws(
    () =>
      verifyNoReplacementSessions(
        ["session-1"],
        ["session-1", "invalid-session"],
        "invalid-session",
        {
          error: "No conversation found",
          messageCount: 1,
          observedSessionIds: ["invalid-session"],
          resultSubtype: "error_during_execution",
        },
      ),
    /created replacement sessions: invalid-session/,
  );
});

test("verifyNoReplacementSessions rejects a different emitted identity", () => {
  assert.throws(
    () =>
      verifyNoReplacementSessions(
        ["session-1"],
        ["session-1"],
        "invalid-session",
        {
          error: "Identity mismatch",
          messageCount: 1,
          observedSessionIds: ["replacement-session"],
          resultSubtype: "error_during_execution",
        },
      ),
    /emitted replacement session IDs: replacement-session/,
  );
});

test("verifyInvalidResumeFailure accepts a session-specific error", () => {
  assert.doesNotThrow(() =>
    verifyInvalidResumeFailure("invalid-session", {
      error: "No conversation found with session ID: invalid-session",
      messageCount: 1,
      observedSessionIds: ["invalid-session"],
      resultSubtype: "error_during_execution",
      numTurns: 0,
      totalCostUsd: 0,
    }),
  );
});

test("verifyInvalidResumeFailure rejects an unrelated provider error", () => {
  assert.throws(
    () =>
      verifyInvalidResumeFailure("invalid-session", {
        error: "Authentication failed",
        messageCount: 0,
        observedSessionIds: [],
      }),
    /did not return a clear session-specific failure/,
  );
});
