import { expect, test } from "bun:test";
import type { BuildInfo } from "../src/buildInfo.ts";
import { createApp } from "../src/server/app.ts";

const buildInfo: BuildInfo = {
  version: "1.2.3",
  commit: "abc1234",
  builtAt: "2026-01-01T00:00:00Z",
};

test("GET /api/v1/health returns ok without a listener", async () => {
  const res = await createApp({ buildInfo }).request("/api/v1/health");
  expect(res.status).toBe(200);
  expect(res.headers.get("content-type")).toStartWith("application/json");
  expect(await res.json()).toEqual({ status: "ok" });
});

test("GET /api/v1/version returns injected build metadata", async () => {
  const res = await createApp({ buildInfo }).request("/api/v1/version");
  expect(res.status).toBe(200);
  expect(await res.json()).toEqual({
    version: "1.2.3",
    apiVersion: "v1",
    commit: "abc1234",
    builtAt: "2026-01-01T00:00:00Z",
  });
});

test("unknown routes return 404", async () => {
  const res = await createApp({ buildInfo }).request("/api/v1/nope");
  expect(res.status).toBe(404);
});
