import assert from "node:assert/strict";
import { mkdtempSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  assertSessionRecord,
  parseArgs,
  verifyResumedThread,
} from "./probe.ts";

const session = {
  version: 1 as const,
  provider: "codex" as const,
  threadId: "thread-123",
  marker: "ATC-CODEX-TEST",
  cwd: "/tmp/project",
  createdAt: "2026-07-27T00:00:00.000Z",
  createLog: "create.jsonl",
};

test("parseArgs requires a session artifact for resume", () => {
  assert.throws(() => parseArgs(["resume"]), /requires --session/);
});

test("parseArgs canonicalizes the requested cwd", () => {
  const directory = realpathSync(mkdtempSync(join(tmpdir(), "atc-poc-")));
  const options = parseArgs(["create", "--cwd", directory]);
  assert.equal(options.cwd, directory);
  assert.equal(options.command, "create");
});

test("assertSessionRecord rejects incomplete artifacts", () => {
  assert.throws(
    () => assertSessionRecord({ provider: "codex" }),
    /invalid shape/,
  );
});

test("verifyResumedThread accepts matching durable identity and cwd", () => {
  const result = verifyResumedThread(session, {
    thread: {
      id: session.threadId,
      cwd: session.cwd,
      ephemeral: false,
    },
  });
  assert.equal(result.id, session.threadId);
});

test("verifyResumedThread rejects replacement identity", () => {
  assert.throws(
    () =>
      verifyResumedThread(session, {
        thread: {
          id: "replacement-thread",
          cwd: session.cwd,
          ephemeral: false,
        },
      }),
    /Identity mismatch/,
  );
});

test("verifyResumedThread rejects cwd mismatch", () => {
  assert.throws(
    () =>
      verifyResumedThread(session, {
        thread: {
          id: session.threadId,
          cwd: "/tmp/other-project",
          ephemeral: false,
        },
      }),
    /Working-directory mismatch/,
  );
});

test("verifyResumedThread rejects ephemeral threads", () => {
  assert.throws(
    () =>
      verifyResumedThread(session, {
        thread: {
          id: session.threadId,
          cwd: session.cwd,
          ephemeral: true,
        },
      }),
    /not durable/,
  );
});
