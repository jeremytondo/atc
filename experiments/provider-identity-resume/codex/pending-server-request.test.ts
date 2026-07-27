import assert from "node:assert/strict";
import test from "node:test";

import { PendingServerRequest } from "./pending-server-request.ts";

const request = {
  id: 7,
  method: "item/tool/requestUserInput",
  params: { threadId: "thread-a" },
  sequence: 12,
  receivedAt: "2026-07-27T00:00:00.000Z",
};

test("holds a matching server request until a correlated response is ready", async () => {
  const pending = new PendingServerRequest(request.method);
  const responsePromise = pending.handle(request);

  assert.deepEqual(await pending.wait(100), request);
  pending.respond({ answers: { choice: { answers: ["Alpha"] } } });

  assert.deepEqual(await responsePromise, {
    answers: { choice: { answers: ["Alpha"] } },
  });
});

test("rejects an unexpected server request method", async () => {
  const pending = new PendingServerRequest(
    "item/commandExecution/requestApproval",
  );
  await assert.rejects(() => pending.handle(request), /Expected server request/);
});

test("allows a captured request to fail closed", async () => {
  const pending = new PendingServerRequest(request.method);
  const responsePromise = pending.handle(request);

  await pending.wait(100);
  pending.reject("unsafe request");

  await assert.rejects(() => responsePromise, /unsafe request/);
});
