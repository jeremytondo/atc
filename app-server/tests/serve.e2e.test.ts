import { expect, test } from "bun:test";
import { fileURLToPath } from "node:url";
import packageJson from "../package.json";

const packageRoot = fileURLToPath(new URL("..", import.meta.url));

async function readUntil(
  stream: ReadableStream<Uint8Array>,
  pattern: RegExp,
): Promise<RegExpMatchArray> {
  const decoder = new TextDecoder();
  let seen = "";
  for await (const chunk of stream) {
    seen += decoder.decode(chunk, { stream: true });
    const match = seen.match(pattern);
    if (match) return match;
  }
  throw new Error(
    `stream ended before matching ${pattern}; output so far:\n${seen}`,
  );
}

test("the serve entrypoint serves health and version over TCP and exits cleanly on SIGTERM", async () => {
  const proc = Bun.spawn({
    cmd: [process.execPath, "src/main.ts", "serve", "--port", "0"],
    cwd: packageRoot,
    stdout: "pipe",
    stderr: "inherit",
  });

  try {
    const [, baseUrl] = await readUntil(
      proc.stdout,
      /listening on (http:\/\/\S+)/,
    );
    if (!baseUrl) throw new Error("no URL captured from the listening line");

    const health = await fetch(new URL("/api/v1/health", baseUrl), {
      signal: AbortSignal.timeout(5000),
    });
    expect(health.status).toBe(200);
    expect(await health.json()).toEqual({ status: "ok" });

    const version = await fetch(new URL("/api/v1/version", baseUrl), {
      signal: AbortSignal.timeout(5000),
    });
    expect(version.status).toBe(200);
    expect(await version.json()).toEqual({
      version: packageJson.version,
      apiVersion: "v1",
      commit: "development",
      builtAt: "development",
    });

    proc.kill("SIGTERM");
    expect(await proc.exited).toBe(0);
  } finally {
    proc.kill();
    await proc.exited;
  }
}, 15000);
