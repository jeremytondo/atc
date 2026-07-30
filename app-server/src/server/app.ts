import { Hono } from "hono";
import type { BuildInfo } from "../buildInfo.ts";
import { API_VERSION, createV1Routes } from "./routes.ts";

/** Everything the HTTP application depends on; injected so tests control it. */
export interface AppDependencies {
  buildInfo: BuildInfo;
}

/** Build the complete HTTP application without opening a listener. */
export function createApp({ buildInfo }: AppDependencies): Hono {
  const app = new Hono();
  app.route(`/api/${API_VERSION}`, createV1Routes(buildInfo));
  return app;
}
