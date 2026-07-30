import type { Hono } from "hono";

/** How long stop() waits for in-flight requests before force-closing. */
const DEFAULT_SHUTDOWN_GRACE_MS = 5000;

export interface ListenOptions {
  hostname: string;
  /** TCP port; 0 asks the OS for an ephemeral port. */
  port: number;
  /** Drain window for stop(); tests shorten it to keep hung-request coverage fast. */
  shutdownGraceMs?: number;
}

export interface RunningServer {
  /** The bound address, with the real port when an ephemeral one was requested. */
  url: URL;
  stop(): Promise<void>;
}

/** Bind the application to a TCP listener. */
export function startServer(
  app: Hono,
  {
    hostname,
    port,
    shutdownGraceMs = DEFAULT_SHUTDOWN_GRACE_MS,
  }: ListenOptions,
): RunningServer {
  // Bun offers no bounded graceful stop: stop() waits on in-flight requests
  // indefinitely, and stop(true) is ignored while that wait is pending. So
  // shutdown is built the other way around — reject new requests here, drain
  // in-flight ones within the grace window, then force-close once.
  let stopping = false;
  const server = Bun.serve({
    hostname,
    port,
    fetch: (request) =>
      stopping
        ? new Response("Server is shutting down", { status: 503 })
        : app.fetch(request),
  });
  return {
    url: server.url,
    stop: async () => {
      stopping = true;
      const deadline = Date.now() + shutdownGraceMs;
      while (server.pendingRequests > 0 && Date.now() < deadline) {
        await Bun.sleep(10);
      }
      await server.stop(true);
    },
  };
}

export type ShutdownSignal = "SIGINT" | "SIGTERM";

/** Resolves with the first SIGINT or SIGTERM, removing both listeners. */
export function onceShutdownSignal(): Promise<ShutdownSignal> {
  return new Promise((resolve) => {
    const onSigint = () => {
      process.off("SIGTERM", onSigterm);
      resolve("SIGINT");
    };
    const onSigterm = () => {
      process.off("SIGINT", onSigint);
      resolve("SIGTERM");
    };
    process.once("SIGINT", onSigint);
    process.once("SIGTERM", onSigterm);
  });
}
