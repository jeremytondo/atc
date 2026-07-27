import assert from "node:assert/strict";
import test from "node:test";

import { buildQueryOptions } from "./sdk-query.ts";

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
});
