import { Command, InvalidArgumentError } from "commander";
import type { BuildInfo } from "../buildInfo.ts";

/**
 * Temporary development default, distinct from the Go server's 7331 so both
 * implementations can run side by side.
 *
 * Delete this rather than maintain it. Two things retire it: the real listener
 * default arrives with App Server configuration (flags > env > config file >
 * defaults), and the side-by-side reason disappears entirely when the Go
 * server is removed at cutover. Until then nothing else should come to depend
 * on 7332 — it is a development convenience, not a documented port.
 */
export const DEFAULT_DEV_PORT = 7332;

export interface ProgramDependencies {
  buildInfo: BuildInfo;
  serve: (options: { port: number }) => Promise<void>;
}

/** Build the atc CLI: command registration and parsing only, no side effects. */
export function createProgram({
  buildInfo,
  serve,
}: ProgramDependencies): Command {
  const program = new Command("atc")
    .description("ATC App Server")
    .version(buildInfo.version);

  program
    .command("serve")
    .description("Run the App Server in the foreground on loopback TCP")
    .option(
      "--port <port>",
      "TCP port to listen on (0 for an ephemeral port)",
      parsePort,
      DEFAULT_DEV_PORT,
    )
    .action(async (options: { port: number }) => {
      await serve({ port: options.port });
    });

  return program;
}

function parsePort(value: string): number {
  // Digits only: Number() alone would accept "", "0x50", "1e4", " 8080 ", …
  if (!/^\d{1,5}$/.test(value) || Number(value) > 65535) {
    throw new InvalidArgumentError(
      "port must be an integer between 0 and 65535.",
    );
  }
  return Number(value);
}
