import { Hono } from "hono";
import type { BuildInfo } from "../buildInfo.ts";

export const API_VERSION = "v1";

interface HealthResponse {
  status: "ok";
}

interface VersionResponse {
  version: string;
  apiVersion: typeof API_VERSION;
  commit: string;
  builtAt: string;
}

/** The public v1 API routes, mounted by the application under /api/v1. */
export function createV1Routes(buildInfo: BuildInfo): Hono {
  const v1 = new Hono();

  v1.get("/health", (c) => c.json<HealthResponse>({ status: "ok" }));

  v1.get("/version", (c) =>
    c.json<VersionResponse>({
      version: buildInfo.version,
      apiVersion: API_VERSION,
      commit: buildInfo.commit,
      builtAt: buildInfo.builtAt,
    }),
  );

  return v1;
}
