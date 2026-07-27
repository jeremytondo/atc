import assert from "node:assert/strict";
import { mkdtempSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  assertSessionRecord,
  buildAskUserQuestionResult,
  buildPermissionResult,
  parseArgs,
  requireResumeId,
  requireExactPwdRequest,
  summarizeLifecycleSignals,
  verifyInvalidResumeFailure,
  verifyBlockingCallbackTimeline,
  verifyLifecycleTransitions,
  verifyNeedsInputTransitions,
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

test("parseArgs accepts the lifecycle gate command", () => {
  assert.equal(parseArgs(["lifecycle"]).command, "lifecycle");
});

test("parseArgs accepts deterministic input-request answers", () => {
  const options = parseArgs([
    "input-request",
    "--answer",
    "Alpha",
    "--response-delay-seconds",
    "1.5",
  ]);
  assert.equal(options.command, "input-request");
  assert.equal(options.answer, "Alpha");
  assert.equal(options.responseDelayMs, 1_500);
});

test("parseArgs accepts deterministic permission decisions", () => {
  const options = parseArgs([
    "permission-request",
    "--decision",
    "allow",
  ]);
  assert.equal(options.command, "permission-request");
  assert.equal(options.decision, "allow");
});

test("parseArgs rejects unsupported permission decisions", () => {
  assert.throws(
    () => parseArgs(["permission-request", "--decision", "always"]),
    /allow or deny/,
  );
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

test("parseArgs configures the lifecycle observation window", () => {
  const options = parseArgs([
    "lifecycle",
    "--observation-seconds",
    "1.5",
  ]);
  assert.equal(options.lifecycleObservationMs, 1_500);
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

test("verifyLifecycleTransitions accepts ordered running then idle evidence", () => {
  assert.doesNotThrow(() =>
    verifyLifecycleTransitions([
      { sequence: 2, state: "running" },
      { sequence: 6, state: "idle" },
    ]),
  );
});

test("verifyLifecycleTransitions accepts absent explicit state evidence", () => {
  assert.doesNotThrow(() => verifyLifecycleTransitions([]));
});

test("verifyLifecycleTransitions rejects running without a later idle", () => {
  assert.throws(
    () =>
      verifyLifecycleTransitions([
        { sequence: 2, state: "idle" },
        { sequence: 3, state: "running" },
      ]),
    /no idle event after running/,
  );
});

test("summarizeLifecycleSignals distinguishes observed and unobserved states", () => {
  assert.deepEqual(
    summarizeLifecycleSignals({
      stateTransitions: [
        { sequence: 2, state: "running" },
        { sequence: 6, state: "idle" },
      ],
    }),
    {
      working: {
        providerState: "running",
        status: "observed",
        sequences: [2],
      },
      idle: {
        providerState: "idle",
        status: "observed",
        sequences: [6],
      },
      needsInput: {
        providerState: "requires_action",
        status: "not_observed",
        sequences: [],
      },
    },
  );
});

test("buildAskUserQuestionResult returns the correlated answer shape", () => {
  assert.deepEqual(
    buildAskUserQuestionResult(
      {
        questions: [
          {
            question: "Which path?",
            header: "Path",
            options: [
              { label: "Alpha", description: "First" },
              { label: "Beta", description: "Second" },
            ],
            multiSelect: false,
          },
        ],
      },
      "Alpha",
    ),
    {
      behavior: "allow",
      updatedInput: {
        questions: [
          {
            question: "Which path?",
            header: "Path",
            options: [
              { label: "Alpha", description: "First" },
              { label: "Beta", description: "Second" },
            ],
            multiSelect: false,
          },
        ],
        answers: {
          "Which path?": "Alpha",
        },
      },
    },
  );
});

test("permission result allows or denies only exact pwd", () => {
  assert.doesNotThrow(() =>
    requireExactPwdRequest("Bash", {
      command: "pwd",
      description: "Print working directory",
    }),
  );
  assert.deepEqual(buildPermissionResult({ command: "pwd" }, "allow"), {
    behavior: "allow",
    updatedInput: { command: "pwd" },
  });
  assert.deepEqual(buildPermissionResult({ command: "pwd" }, "deny"), {
    behavior: "deny",
    message: "User denied the harmless ATC permission probe.",
  });
  assert.throws(
    () => requireExactPwdRequest("Bash", { command: "pwd && ls" }),
    /Unsafe permission probe request/,
  );
  assert.throws(
    () => requireExactPwdRequest("Read", { command: "pwd" }),
    /Unsafe permission probe request/,
  );
});

test("verifyNeedsInputTransitions requires requires_action followed by idle", () => {
  assert.doesNotThrow(() =>
    verifyNeedsInputTransitions([
      { sequence: 1, state: "running" },
      { sequence: 4, state: "requires_action" },
      { sequence: 6, state: "running" },
      { sequence: 9, state: "idle" },
    ]),
  );
  assert.throws(
    () =>
      verifyNeedsInputTransitions([
        { sequence: 1, state: "running" },
        { sequence: 4, state: "idle" },
      ]),
    /no requires_action/,
  );
});

test("verifyBlockingCallbackTimeline requires needs_input while callback is pending", () => {
  assert.doesNotThrow(() =>
    verifyBlockingCallbackTimeline([
      {
        sequence: 1,
        observedAt: "2026-07-27T00:00:00.000Z",
        source: "callback",
        event: "request",
        requestId: "request-1",
        toolUseId: "tool-1",
        toolName: "AskUserQuestion",
        input: {},
      },
      {
        sequence: 2,
        observedAt: "2026-07-27T00:00:01.000Z",
        source: "sdk",
        event: "message",
        messageSequence: 4,
        type: "system",
        subtype: "session_state_changed",
        state: "requires_action",
      },
      {
        sequence: 3,
        observedAt: "2026-07-27T00:00:02.000Z",
        source: "callback",
        event: "response",
        requestId: "request-1",
        toolUseId: "tool-1",
        toolName: "AskUserQuestion",
        result: {
          behavior: "allow",
          updatedInput: {},
        },
      },
      {
        sequence: 4,
        observedAt: "2026-07-27T00:00:03.000Z",
        source: "sdk",
        event: "message",
        messageSequence: 5,
        type: "system",
        subtype: "session_state_changed",
        state: "running",
      },
    ]),
  );
  assert.doesNotThrow(() =>
    verifyBlockingCallbackTimeline([
      {
        sequence: 1,
        observedAt: "2026-07-27T00:00:00.000Z",
        source: "sdk",
        event: "message",
        messageSequence: 4,
        type: "system",
        subtype: "session_state_changed",
        state: "requires_action",
      },
      {
        sequence: 2,
        observedAt: "2026-07-27T00:00:01.000Z",
        source: "callback",
        event: "request",
        requestId: "request-1",
        toolUseId: "tool-1",
        toolName: "Bash",
        input: { command: "pwd" },
      },
      {
        sequence: 3,
        observedAt: "2026-07-27T00:00:02.000Z",
        source: "callback",
        event: "response",
        requestId: "request-1",
        toolUseId: "tool-1",
        toolName: "Bash",
        result: {
          behavior: "allow",
          updatedInput: { command: "pwd" },
        },
      },
      {
        sequence: 4,
        observedAt: "2026-07-27T00:00:03.000Z",
        source: "sdk",
        event: "message",
        messageSequence: 5,
        type: "system",
        subtype: "session_state_changed",
        state: "running",
      },
    ]),
  );
});
