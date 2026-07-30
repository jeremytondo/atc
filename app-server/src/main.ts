// Source entrypoint for the atc executable (compiled standalone in ATC-88).
import { developmentBuildInfo } from "./buildInfo.ts";
import { createProgram } from "./cli/program.ts";
import { runServe } from "./server/serve.ts";

const buildInfo = developmentBuildInfo();

const program = createProgram({
  buildInfo,
  serve: ({ port }) =>
    runServe({ listen: { hostname: "127.0.0.1", port }, buildInfo }),
});

try {
  await program.parseAsync(process.argv);
} catch (error) {
  // Surface startup failures (e.g. port in use) as one line, not a stack trace.
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
