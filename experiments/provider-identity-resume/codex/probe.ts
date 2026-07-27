import { randomUUID } from "node:crypto";
import { existsSync, realpathSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  AppServerClient,
  type JsonObject,
  objectAt,
  stringAt,
} from "./app-server-client.ts";

interface ProbeOptions {
  command: "create" | "resume";
  cwd: string;
  marker?: string;
  sessionPath?: string;
  timeoutMs: number;
}

interface SessionRecord {
  version: 1;
  provider: "codex";
  threadId: string;
  marker: string;
  cwd: string;
  createdAt: string;
  createLog: string;
}

interface TurnResult {
  id: string;
  status: string;
  agentText: string;
}

const probeRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = resolve(probeRoot, "../..");
const runsRoot = resolve(probeRoot, "runs/codex");

export async function main(argv = process.argv.slice(2)): Promise<void> {
  const options = parseArgs(argv);
  if (options.command === "create") {
    await createScenario(options);
  } else {
    await resumeScenario(options);
  }
}

export function parseArgs(argv: string[]): ProbeOptions {
  const [command, ...rest] = argv;
  if (command !== "create" && command !== "resume") {
    throw new Error(usage());
  }

  let cwd = repoRoot;
  let marker: string | undefined;
  let sessionPath: string | undefined;
  let timeoutMs = 300_000;

  for (let index = 0; index < rest.length; index += 1) {
    const flag = rest[index];
    const value = rest[index + 1];
    if (value === undefined) {
      throw new Error(`Missing value for ${flag}\n\n${usage()}`);
    }

    switch (flag) {
      case "--cwd":
        cwd = canonicalPath(value);
        break;
      case "--marker":
        marker = value;
        break;
      case "--session":
        sessionPath = resolve(value);
        break;
      case "--timeout-seconds": {
        const seconds = Number(value);
        if (!Number.isFinite(seconds) || seconds <= 0) {
          throw new Error("--timeout-seconds must be a positive number");
        }
        timeoutMs = seconds * 1_000;
        break;
      }
      default:
        throw new Error(`Unknown option: ${flag}\n\n${usage()}`);
    }
    index += 1;
  }

  if (command === "resume" && sessionPath === undefined) {
    throw new Error(`resume requires --session <path>\n\n${usage()}`);
  }

  return { command, cwd: canonicalPath(cwd), marker, sessionPath, timeoutMs };
}

export function assertSessionRecord(value: unknown): SessionRecord {
  if (!isRecord(value)) {
    throw new Error("Session artifact must contain a JSON object");
  }
  if (
    value.version !== 1 ||
    value.provider !== "codex" ||
    typeof value.threadId !== "string" ||
    typeof value.marker !== "string" ||
    typeof value.cwd !== "string" ||
    typeof value.createdAt !== "string" ||
    typeof value.createLog !== "string"
  ) {
    throw new Error("Session artifact has an unsupported or invalid shape");
  }
  return value as unknown as SessionRecord;
}

export function verifyResumedThread(
  expected: SessionRecord,
  result: JsonObject,
): JsonObject {
  const thread = objectAt(result, "thread");
  if (thread === undefined) {
    throw new Error("thread/resume response did not include a thread object");
  }

  const actualId = stringAt(thread, "id");
  if (actualId !== expected.threadId) {
    throw new Error(
      `Identity mismatch after resume: expected ${expected.threadId}, received ${actualId ?? "no id"}`,
    );
  }

  const actualCwd = stringAt(thread, "cwd");
  if (actualCwd === undefined) {
    throw new Error("thread/resume response did not include cwd");
  }
  if (canonicalPath(actualCwd) !== canonicalPath(expected.cwd)) {
    throw new Error(
      `Working-directory mismatch after resume: expected ${expected.cwd}, received ${actualCwd}`,
    );
  }

  if (thread.ephemeral === true) {
    throw new Error("Resumed thread is ephemeral and therefore not durable");
  }
  return thread;
}

async function createScenario(options: ProbeOptions): Promise<void> {
  const marker =
    options.marker ?? `ATC-CODEX-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.create.jsonl`);
  const sessionPath = resolve(runsRoot, `${runId}.session.json`);

  console.log("Codex provider identity/resume POC — create");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log(`raw provider log: ${relative(process.cwd(), rawLogPath)}`);
  console.log("safety: read-only sandbox, approval policy never");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath,
  });

  try {
    const startResult = await client.request("thread/start", {
      cwd: options.cwd,
      approvalPolicy: "never",
      sandbox: "read-only",
      serviceName: "atc_provider_identity_resume_poc",
      ephemeral: false,
    });
    const thread = requireThread(startResult, "thread/start");
    const threadId = requireString(thread, "id", "thread/start thread");
    const responseCwd = requireString(thread, "cwd", "thread/start thread");
    if (canonicalPath(responseCwd) !== options.cwd) {
      throw new Error(
        `thread/start cwd mismatch: requested ${options.cwd}, received ${responseCwd}`,
      );
    }
    if (thread.ephemeral === true) {
      throw new Error("thread/start unexpectedly returned an ephemeral thread");
    }
    console.log(`IDENTITY AVAILABLE: thread/start response id=${threadId}`);

    const first = await runTurn(
      client,
      threadId,
      [
        "This is a read-only provider identity POC.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${marker}`,
        `Reply with exactly: MARKER STORED: ${marker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(first, "first turn");
    assertMarker(first, marker, "first turn");

    const second = await runTurn(
      client,
      threadId,
      [
        "Without using tools, running commands, or modifying files,",
        "repeat the exact continuity marker from my previous message.",
        "Prefix the response with SAME PROCESS:",
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(second, "same-process turn");
    assertMarker(second, marker, "same-process turn");

    const record: SessionRecord = {
      version: 1,
      provider: "codex",
      threadId,
      marker,
      cwd: options.cwd,
      createdAt: new Date().toISOString(),
      createLog: relative(dirname(sessionPath), rawLogPath),
    };
    await writeFile(sessionPath, `${JSON.stringify(record, null, 2)}\n`, {
      encoding: "utf8",
      flag: "wx",
    });

    console.log("");
    console.log("PASS: created a durable thread and completed two turns.");
    console.log(`session artifact: ${relative(process.cwd(), sessionPath)}`);
    console.log("Run the fresh-process resume check:");
    console.log(
      `pnpm codex resume --session ${shellQuote(relative(process.cwd(), sessionPath))}`,
    );
  } finally {
    await client.stop();
  }
}

async function resumeScenario(options: ProbeOptions): Promise<void> {
  const sessionPath = options.sessionPath;
  if (sessionPath === undefined) {
    throw new Error("resume requires a session artifact");
  }
  const record = assertSessionRecord(
    JSON.parse(await readFile(sessionPath, "utf8")) as unknown,
  );
  const expectedCwd = canonicalPath(record.cwd);
  const runId = makeRunId();
  const rawLogPath = resolve(
    runsRoot,
    `${runId}.resume-${record.threadId}.jsonl`,
  );

  console.log("Codex provider identity/resume POC — resume");
  console.log(`expected thread: ${record.threadId}`);
  console.log(`expected cwd: ${expectedCwd}`);
  console.log(`expected marker: ${record.marker}`);
  console.log(`raw provider log: ${relative(process.cwd(), rawLogPath)}`);
  console.log("safety: resume only; no fallback thread creation");

  const client = await AppServerClient.start({
    cwd: expectedCwd,
    rawLogPath,
  });

  try {
    const resumeResult = await client.request("thread/resume", {
      threadId: record.threadId,
      approvalPolicy: "never",
      sandbox: "read-only",
    });
    const thread = verifyResumedThread(
      { ...record, cwd: expectedCwd },
      resumeResult,
    );
    console.log(
      `IDENTITY VERIFIED: thread/resume returned id=${requireString(thread, "id", "resumed thread")}`,
    );
    console.log(
      `CWD VERIFIED: ${requireString(thread, "cwd", "resumed thread")}`,
    );

    const resumed = await runTurn(
      client,
      record.threadId,
      [
        "Without using tools, running commands, or modifying files,",
        "repeat the exact continuity marker from earlier in this conversation.",
        "Prefix the response with RESUMED:",
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(resumed, "resumed turn");
    assertMarker(resumed, record.marker, "resumed turn");

    console.log("");
    console.log(
      "PASS: a fresh app-server process resumed the exact thread, cwd, and context.",
    );
  } finally {
    await client.stop();
  }
}

async function runTurn(
  client: AppServerClient,
  threadId: string,
  prompt: string,
  timeoutMs: number,
): Promise<TurnResult> {
  const agentMessagePromise = client.waitForNotification(
    "item/completed",
    (params) =>
      params.threadId === threadId &&
      stringAt(objectAt(params, "item"), "type") === "agentMessage",
    timeoutMs,
  );
  const completedPromise = client.waitForNotification(
    "turn/completed",
    (params) => params.threadId === threadId,
    timeoutMs,
  );
  try {
    const result = await client.request("turn/start", {
      threadId,
      input: [{ type: "text", text: prompt, text_elements: [] }],
      cwd: undefined,
      approvalPolicy: "never",
      sandboxPolicy: { type: "readOnly", networkAccess: false },
    });
    const initialTurn = objectAt(result, "turn");
    const turnId = requireString(initialTurn, "id", "turn/start response");

    const [agentMessage, completedMessage] = await Promise.all([
      agentMessagePromise,
      completedPromise,
    ]);
    const completedTurn = objectAt(completedMessage.params, "turn");
    const completedId = requireString(
      completedTurn,
      "id",
      "turn/completed notification",
    );
    if (completedId !== turnId) {
      throw new Error(
        `Turn identity mismatch: turn/start returned ${turnId}, turn/completed returned ${completedId}`,
      );
    }

    const completedItem = objectAt(agentMessage.params, "item");
    return {
      id: completedId,
      status:
        stringAt(completedTurn, "status") ??
        throwValue("turn/completed notification did not include status"),
      agentText:
        stringAt(completedItem, "text") ?? agentTextFromTurn(completedTurn),
    };
  } catch (error) {
    void agentMessagePromise.catch(() => undefined);
    void completedPromise.catch(() => undefined);
    throw error;
  }
}

function agentTextFromTurn(turn: JsonObject | undefined): string {
  const items = turn?.items;
  if (!Array.isArray(items)) {
    return "";
  }
  return items
    .filter(
      (item): item is JsonObject =>
        isRecord(item) &&
        item.type === "agentMessage" &&
        typeof item.text === "string",
    )
    .map((item) => item.text)
    .join("\n");
}

function assertSuccessfulTurn(result: TurnResult, label: string): void {
  if (result.status !== "completed") {
    throw new Error(
      `${label} ${result.id} finished with status ${result.status}`,
    );
  }
}

function assertMarker(
  result: TurnResult,
  marker: string,
  label: string,
): void {
  if (!result.agentText.includes(marker)) {
    throw new Error(
      `${label} ${result.id} did not return continuity marker ${marker}`,
    );
  }
  console.log(`CONTINUITY VERIFIED: ${label} returned ${marker}`);
}

function requireThread(result: JsonObject, method: string): JsonObject {
  const thread = objectAt(result, "thread");
  if (thread === undefined) {
    throw new Error(`${method} response did not include a thread object`);
  }
  return thread;
}

function requireString(
  object: JsonObject | undefined,
  key: string,
  context: string,
): string {
  const value = stringAt(object, key);
  if (value === undefined) {
    throw new Error(`${context} did not include string field ${key}`);
  }
  return value;
}

function canonicalPath(path: string): string {
  const absolute = resolve(path);
  return existsSync(absolute) ? realpathSync(absolute) : absolute;
}

function makeRunId(): string {
  return `${new Date().toISOString().replaceAll(/[:.]/g, "-")}-${randomUUID().slice(0, 8)}`;
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function throwValue(message: string): never {
  throw new Error(message);
}

function usage(): string {
  return [
    "Usage:",
    "  pnpm codex create [--cwd <path>] [--marker <text>] [--timeout-seconds <n>]",
    "  pnpm codex resume --session <path> [--timeout-seconds <n>]",
  ].join("\n");
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((error: unknown) => {
    console.error(`FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  });
}
