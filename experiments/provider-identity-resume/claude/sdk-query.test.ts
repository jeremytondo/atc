import assert from "node:assert/strict";
import test from "node:test";

import {
  buildClaudeEnvironment,
  buildQueryOptions,
} from "./sdk-query.ts";

test("buildClaudeEnvironment preserves inherited values and enables lifecycle events", () => {
  assert.deepEqual(
    buildClaudeEnvironment({
      PATH: "/test/bin",
      CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS: "0",
    }),
    {
      PATH: "/test/bin",
      CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS: "1",
    },
  );
});

test("buildQueryOptions wires the exact expected session into resume", () => {
  const options = buildQueryOptions(
    {
      cwd: "/tmp/project",
      expectedSessionId: "session-123",
    },
    new AbortController(),
  );

  assert.equal(options.resume, "session-123");
});

test("buildQueryOptions disables tools and filesystem settings", () => {
  const options = buildQueryOptions(
    {
      cwd: "/tmp/project",
    },
    new AbortController(),
  );

  assert.deepEqual(options.tools, []);
  assert.deepEqual(options.allowedTools, []);
  assert.deepEqual(options.settingSources, []);
  assert.equal(options.permissionMode, "dontAsk");
  assert.equal(options.persistSession, true);
  assert.equal(options.env?.CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS, "1");
});

test("buildQueryOptions applies an interactive query policy without auto-allowing its tool", () => {
  const canUseTool = async () => ({
    behavior: "deny" as const,
    message: "test",
  });
  const options = buildQueryOptions(
    {
      cwd: "/tmp/project",
      queryPolicy: {
        tools: ["AskUserQuestion"],
        permissionMode: "default",
        maxTurns: 2,
        canUseTool,
      },
    },
    new AbortController(),
  );

  assert.deepEqual(options.tools, ["AskUserQuestion"]);
  assert.deepEqual(options.allowedTools, []);
  assert.equal(options.permissionMode, "default");
  assert.equal(options.maxTurns, 2);
  assert.equal(options.canUseTool, canUseTool);
  assert.deepEqual(options.settingSources, []);
});
