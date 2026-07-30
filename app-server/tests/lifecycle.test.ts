import { expect, test } from "bun:test";
import { Hono } from "hono";
import { createApp } from "../src/server/app.ts";
import { onceShutdownSignal, startServer } from "../src/server/lifecycle.ts";

const app = createApp({
  buildInfo: {
    version: "1.2.3",
    commit: "abc1234",
    builtAt: "2026-01-01T00:00:00Z",
  },
});

test("startServer binds an ephemeral loopback port and stop() releases it", async () => {
  const server = startServer(app, { hostname: "127.0.0.1", port: 0 });
  const port = Number(server.url.port);
  expect(server.url.hostname).toBe("127.0.0.1");
  expect(port).toBeGreaterThan(0);

  const res = await fetch(new URL("/api/v1/health", server.url));
  expect(res.status).toBe(200);
  expect(await res.json()).toEqual({ status: "ok" });

  await server.stop();

  // The strongest observable proof the listener is gone: the port can be
  // bound again immediately.
  const rebound = startServer(app, { hostname: "127.0.0.1", port });
  expect(Number(rebound.url.port)).toBe(port);
  await rebound.stop();
});

/** A Hono app whose /hang route never responds, plus proof it was entered. */
function hangingApp() {
  let entered!: () => void;
  const requestEntered = new Promise<void>((resolve) => {
    entered = resolve;
  });
  const app = new Hono();
  app.get("/hang", () => {
    entered();
    return new Promise<Response>(() => {});
  });
  return { app, requestEntered };
}

test("stop() force-closes a request that outlives the shutdown grace window", async () => {
  const { app: hanging, requestEntered } = hangingApp();
  const server = startServer(hanging, {
    hostname: "127.0.0.1",
    port: 0,
    shutdownGraceMs: 50,
  });
  const pending = fetch(new URL("/hang", server.url)).catch(() => "aborted");
  // Wait until the request is provably in the handler, so stop() exercises
  // the drain-then-force-close path rather than seeing an idle server.
  await requestEntered;

  await server.stop();
  expect(await pending).toBe("aborted");
});

test("stop() rejects requests that arrive while in-flight ones drain", async () => {
  const { app: hanging, requestEntered } = hangingApp();
  const server = startServer(hanging, {
    hostname: "127.0.0.1",
    port: 0,
    shutdownGraceMs: 200,
  });
  const pending = fetch(new URL("/hang", server.url)).catch(() => "aborted");
  await requestEntered;

  const stopped = server.stop();
  // The hanging request holds stop() in its grace window; new work must be
  // turned away instead of being served normally.
  const late = await fetch(new URL("/hang", server.url));
  expect(late.status).toBe(503);

  await stopped;
  expect(await pending).toBe("aborted");
});

test("onceShutdownSignal resolves on the first signal and removes both listeners", async () => {
  const sigintBefore = process.listenerCount("SIGINT");
  const sigtermBefore = process.listenerCount("SIGTERM");

  const signal = onceShutdownSignal();
  expect(process.listenerCount("SIGINT")).toBe(sigintBefore + 1);
  expect(process.listenerCount("SIGTERM")).toBe(sigtermBefore + 1);

  process.emit("SIGINT");
  expect(await signal).toBe("SIGINT");
  expect(process.listenerCount("SIGINT")).toBe(sigintBefore);
  expect(process.listenerCount("SIGTERM")).toBe(sigtermBefore);
});
