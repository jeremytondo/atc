import type { BuildInfo } from "../buildInfo.ts";
import { createApp } from "./app.ts";
import {
  onceShutdownSignal,
  startServer,
  type ListenOptions,
} from "./lifecycle.ts";

export interface ServeOptions {
  listen: ListenOptions;
  buildInfo: BuildInfo;
}

/**
 * Run the App Server in the foreground: bind the listener, then wait for
 * SIGINT/SIGTERM and shut the listener down cleanly.
 */
export async function runServe({
  listen,
  buildInfo,
}: ServeOptions): Promise<void> {
  const app = createApp({ buildInfo });
  const server = startServer(app, listen);
  console.log(`ATC App Server listening on ${server.url}`);

  const signal = await onceShutdownSignal();
  console.log(`Received ${signal}, shutting down`);
  await server.stop();
}
