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
