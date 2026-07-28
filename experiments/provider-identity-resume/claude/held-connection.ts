import { spawn } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { mkdir, open, readdir, readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  query,
  type Options,
  type SDKMessage,
  type SDKResultMessage,
  type SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk";

import {
  assistantText,
  buildClaudeEnvironment,
  listClaudeSessionIds,
  sessionIdOf,
} from "./sdk-query.ts";

/**
 * ATC-83 held-connection experiments.
 *
 * The ATC-68 POC proved that two concurrent Claude SDK writers form
 * divergent history. These scenarios test the narrower question: is a
 * passively held SDK `query()` safe while a separate `claude` process
 * (the same resume path the native TUI uses) drives the same session, and
 * does the held query observe anything while that happens?
 *
 *   passive-hold — a streaming query() seeds the session, then stays open
 *                  without sending while `claude -p --resume` drives two
 *                  turns. Records exactly what the held query observes,
 *                  closes it without writing, and verifies history
 *                  integrity with a fresh resume.
 *   stale-write  — same shape, but after the external turn the held query
 *                  sends one more message WITHOUT re-resuming.
 *                  Characterizes serialized (non-concurrent) writes from a
 *                  stale connection: context visibility and which history
 *                  survives.
 */

interface HeldOptions {
  command: "passive-hold" | "stale-write";
  cwd: string;
  timeoutMs: number;
  settleMs: number;
}

interface RecordedMessage {
  sequence: number;
  message: SDKMessage;
}

interface TurnCapture {
  resultSubtype: SDKResultMessage["subtype"];
  isError: boolean;
  text: string;
  firstSequence: number;
  lastSequence: number;
}

interface ExternalClaudeTurn {
  exitCode: number;
  durationMs: number;
  sessionId?: string;
  resultSubtype?: string;
  resultText?: string;
  rawStdout: string;
  rawStderr: string;
}

const probeRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = resolve(probeRoot, "../..");
const runsRoot = resolve(probeRoot, "runs/claude");

export async function main(argv = process.argv.slice(2)): Promise<void> {
  const options = parseArgs(argv);
  if (options.command === "passive-hold") {
    await passiveHoldScenario(options);
  } else {
    await staleWriteScenario(options);
  }
}

function parseArgs(argv: string[]): HeldOptions {
  const [command, ...rest] = argv;
  if (command !== "passive-hold" && command !== "stale-write") {
    throw new Error(usage());
  }
  let cwd = repoRoot;
  let timeoutMs = 300_000;
  let settleMs = 5_000;
  for (let index = 0; index < rest.length; index += 1) {
    const flag = rest[index];
    const value = rest[index + 1];
    if (value === undefined) {
      throw new Error(`Missing value for ${flag}\n\n${usage()}`);
    }
    switch (flag) {
      case "--cwd":
        cwd = resolve(value);
        break;
      case "--timeout-seconds":
        timeoutMs = requirePositive(value, flag) * 1_000;
        break;
      case "--settle-seconds":
        settleMs = requirePositive(value, flag) * 1_000;
        break;
      default:
        throw new Error(`Unknown option: ${flag}\n\n${usage()}`);
    }
    index += 1;
  }
  return {
    command,
    cwd: existsSync(cwd) ? realpathSync(cwd) : cwd,
    timeoutMs,
    settleMs,
  };
}

async function passiveHoldScenario(options: HeldOptions): Promise<void> {
  const runId = makeRunId();
  const suffix = runId.slice(-8).toUpperCase();
  const seedMarker = `ATC-CLAUDE-HELD-SEED-${suffix}`;
  const ext1Marker = `ATC-CLAUDE-HELD-EXT1-${suffix}`;
  const ext2Marker = `ATC-CLAUDE-HELD-EXT2-${suffix}`;
  const heldLogPath = resolve(runsRoot, `${runId}.held-passive.held-query.jsonl`);
  const resumeLogPath = resolve(runsRoot, `${runId}.held-passive.fresh-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.held-passive.json`);

  console.log("Claude held-connection experiment — passive-hold (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: no tools exposed, dontAsk permission mode");

  const held = await startHeldQuery(options.cwd, heldLogPath, options.timeoutMs);
  let heldObservation: Record<string, unknown>;
  let external1: ExternalClaudeTurn;
  let external2: ExternalClaudeTurn;
  let sessionFileLinesAfterSeed: number;
  let sessionFilePath: string;
  try {
    const seed = await held.sendAndAwaitResult(
      [
        "This is a read-only held-connection experiment.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${seedMarker}`,
        `Reply with exactly: MARKER STORED: ${seedMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    requireSuccess(seed, seedMarker, "seed turn");
    console.log(`session: ${held.sessionId()}`);

    sessionFilePath = await findSessionFile(held.sessionId());
    sessionFileLinesAfterSeed = await countLines(sessionFilePath);

    const watermark = held.messageCount();
    external1 = await runExternalClaudeTurn(
      held.sessionId(),
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "First repeat the continuity marker from earlier in this conversation,",
        `then remember a second marker: ${ext1Marker}.`,
        `Reply with exactly: EXTERNAL TURN ONE: <earlier marker> ${ext1Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.held-passive.external-1.log`),
    );
    requireExternalSuccess(external1, held.sessionId(), seedMarker, "external turn one");

    external2 = await runExternalClaudeTurn(
      held.sessionId(),
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "Repeat both continuity markers you have been told in this conversation,",
        `then remember a third marker: ${ext2Marker}.`,
        `Reply with exactly: EXTERNAL TURN TWO: <first marker> <second marker> ${ext2Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.held-passive.external-2.log`),
    );
    requireExternalSuccess(external2, held.sessionId(), ext1Marker, "external turn two");

    await delay(options.settleMs);
    const heldNew = held.messagesSince(watermark);
    heldObservation = {
      messageCount: heldNew.length,
      types: [...new Set(heldNew.map((entry) => labelOf(entry.message)))],
    };
    console.log(
      `held query observed ${heldNew.length} messages during external turns`,
    );
  } finally {
    await held.close();
  }

  const sessionFileLinesAfterExternal = await countLines(sessionFilePath);
  const sessionsInventory = await listClaudeSessionIds(options.cwd);

  const fresh = await runSingleTurnResume(
    held.sessionId(),
    options.cwd,
    [
      "Do not use tools, run commands, or modify files.",
      "Repeat every continuity marker you have been told in this conversation,",
      "oldest first, separated by spaces. Reply with exactly:",
      "ALL MARKERS: <markers>",
    ].join(" "),
    resumeLogPath,
    options.timeoutMs,
  );
  for (const marker of [seedMarker, ext1Marker, ext2Marker]) {
    if (!fresh.text.includes(marker)) {
      throw new Error(`fresh resume did not recall ${marker}: ${fresh.text}`);
    }
    console.log(`CONTINUITY VERIFIED: fresh resume recalled ${marker}`);
  }

  const sessionFileText = await readFile(sessionFilePath, "utf8");
  const record = {
    version: 1,
    provider: "claude",
    scenario: "held-connection-passive",
    cwd: options.cwd,
    completedAt: new Date().toISOString(),
    evidence: {
      sessionId: held.sessionId(),
      seedMarker,
      ext1Marker,
      ext2Marker,
      heldQueryDuringExternalTurns: heldObservation,
      externalTurns: [summarizeExternal(external1), summarizeExternal(external2)],
      sessionFile: {
        path: sessionFilePath,
        linesAfterSeed: sessionFileLinesAfterSeed,
        linesAfterExternalTurns: sessionFileLinesAfterExternal,
        containsSeedMarker: sessionFileText.includes(seedMarker),
        containsExt1Marker: sessionFileText.includes(ext1Marker),
        containsExt2Marker: sessionFileText.includes(ext2Marker),
      },
      sessionInventoryContainsHeldSession: sessionsInventory.includes(
        held.sessionId(),
      ),
      freshResume: {
        resultSubtype: fresh.resultSubtype,
        recallText: fresh.text,
      },
    },
    rawLogs: [heldLogPath, resumeLogPath].map((path) =>
      relative(dirname(resultPath), path),
    ),
  };
  await writeFile(resultPath, `${JSON.stringify(record, null, 2)}\n`, {
    encoding: "utf8",
    flag: "wx",
  });
  console.log(`PASS: passive-hold evidence written to ${relative(process.cwd(), resultPath)}`);
}

async function staleWriteScenario(options: HeldOptions): Promise<void> {
  const runId = makeRunId();
  const suffix = runId.slice(-8).toUpperCase();
  const seedMarker = `ATC-CLAUDE-STALE-SEED-${suffix}`;
  const ext1Marker = `ATC-CLAUDE-STALE-EXT1-${suffix}`;
  const staleMarker = `ATC-CLAUDE-STALE-HELD-${suffix}`;
  const heldLogPath = resolve(runsRoot, `${runId}.stale-write.held-query.jsonl`);
  const resumeLogPath = resolve(runsRoot, `${runId}.stale-write.fresh-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.stale-write.json`);

  console.log("Claude held-connection experiment — stale-write (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: no tools exposed, dontAsk permission mode");

  const held = await startHeldQuery(options.cwd, heldLogPath, options.timeoutMs);
  let staleTurn: TurnCapture;
  let external1: ExternalClaudeTurn;
  let heldObservation: Record<string, unknown>;
  try {
    const seed = await held.sendAndAwaitResult(
      [
        "This is a read-only held-connection experiment.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${seedMarker}`,
        `Reply with exactly: MARKER STORED: ${seedMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    requireSuccess(seed, seedMarker, "seed turn");
    console.log(`session: ${held.sessionId()}`);

    const watermark = held.messageCount();
    external1 = await runExternalClaudeTurn(
      held.sessionId(),
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "First repeat the continuity marker from earlier in this conversation,",
        `then remember a second marker: ${ext1Marker}.`,
        `Reply with exactly: EXTERNAL TURN ONE: <earlier marker> ${ext1Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.stale-write.external-1.log`),
    );
    requireExternalSuccess(external1, held.sessionId(), seedMarker, "external turn one");

    await delay(options.settleMs);
    const heldNew = held.messagesSince(watermark);
    heldObservation = {
      messageCount: heldNew.length,
      types: [...new Set(heldNew.map((entry) => labelOf(entry.message)))],
    };

    // Serialized stale write: the held query sends one more message WITHOUT
    // re-resuming after the external process advanced the session.
    staleTurn = await held.sendAndAwaitResult(
      [
        "Do not use tools, run commands, or modify files.",
        "Repeat every continuity marker you have been told in this conversation,",
        `then remember one more marker: ${staleMarker}.`,
        `Reply with exactly: STALE WRITE: <all earlier markers> ${staleMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    console.log(
      `stale write completed: subtype=${staleTurn.resultSubtype} sawExternalMarker=${String(staleTurn.text.includes(ext1Marker))}`,
    );
  } finally {
    await held.close();
  }

  const sessionFilePath = await findSessionFile(held.sessionId());
  const sessionFileText = await readFile(sessionFilePath, "utf8");

  const fresh = await runSingleTurnResume(
    held.sessionId(),
    options.cwd,
    [
      "Do not use tools, run commands, or modify files.",
      "Repeat every continuity marker you have been told in this conversation,",
      "oldest first, separated by spaces. Reply with exactly:",
      "ALL MARKERS: <markers>",
    ].join(" "),
    resumeLogPath,
    options.timeoutMs,
  );

  const record = {
    version: 1,
    provider: "claude",
    scenario: "held-connection-stale-write",
    cwd: options.cwd,
    completedAt: new Date().toISOString(),
    evidence: {
      sessionId: held.sessionId(),
      seedMarker,
      ext1Marker,
      staleMarker,
      heldQueryDuringExternalTurn: heldObservation,
      externalTurn: summarizeExternal(external1),
      staleTurn: {
        resultSubtype: staleTurn.resultSubtype,
        text: staleTurn.text,
        sawSeedMarker: staleTurn.text.includes(seedMarker),
        sawExternalMarker: staleTurn.text.includes(ext1Marker),
      },
      sessionFile: {
        path: sessionFilePath,
        containsSeedMarker: sessionFileText.includes(seedMarker),
        containsExternalMarker: sessionFileText.includes(ext1Marker),
        containsStaleMarker: sessionFileText.includes(staleMarker),
      },
      freshResume: {
        resultSubtype: fresh.resultSubtype,
        recallText: fresh.text,
        recalledSeedMarker: fresh.text.includes(seedMarker),
        recalledExternalMarker: fresh.text.includes(ext1Marker),
        recalledStaleMarker: fresh.text.includes(staleMarker),
      },
    },
    rawLogs: [heldLogPath, resumeLogPath].map((path) =>
      relative(dirname(resultPath), path),
    ),
  };
  await writeFile(resultPath, `${JSON.stringify(record, null, 2)}\n`, {
    encoding: "utf8",
    flag: "wx",
  });
  console.log(`stale-write evidence written to ${relative(process.cwd(), resultPath)}`);
}

interface HeldQuery {
  sessionId(): string;
  messageCount(): number;
  messagesSince(watermark: number): RecordedMessage[];
  sendAndAwaitResult(text: string, timeoutMs: number): Promise<TurnCapture>;
  close(): Promise<void>;
}

async function startHeldQuery(
  cwd: string,
  rawLogPath: string,
  timeoutMs: number,
): Promise<HeldQuery> {
  await mkdir(dirname(rawLogPath), { recursive: true });
  const rawLog = await open(rawLogPath, "wx");
  const abortController = new AbortController();

  const pendingInputs: SDKUserMessage[] = [];
  let releaseInput: (() => void) | undefined;
  let inputClosed = false;
  const inputStream: AsyncIterable<SDKUserMessage> = {
    async *[Symbol.asyncIterator]() {
      for (;;) {
        while (pendingInputs.length > 0) {
          yield pendingInputs.shift() as SDKUserMessage;
        }
        if (inputClosed) {
          return;
        }
        await new Promise<void>((resolveWait) => {
          releaseInput = resolveWait;
        });
      }
    },
  };

  const recorded: RecordedMessage[] = [];
  const messageWaiters = new Set<{
    predicate: (entry: RecordedMessage) => boolean;
    resolve: (entry: RecordedMessage) => void;
  }>();
  let streamError: Error | undefined;

  const queryOptions: Options = {
    abortController,
    allowedTools: [],
    cwd,
    env: buildClaudeEnvironment(process.env),
    maxTurns: 25,
    permissionMode: "dontAsk",
    persistSession: true,
    settingSources: [],
    tools: [],
  };
  const stream = query({ prompt: inputStream, options: queryOptions });

  const consumed = (async () => {
    try {
      for await (const message of stream) {
        const entry = { sequence: recorded.length + 1, message };
        recorded.push(entry);
        await rawLog.writeFile(`${JSON.stringify(message)}\n`);
        console.log(`← held #${entry.sequence} ${labelOf(message)}`);
        for (const waiter of messageWaiters) {
          if (waiter.predicate(entry)) {
            messageWaiters.delete(waiter);
            waiter.resolve(entry);
          }
        }
      }
    } catch (error) {
      streamError = error instanceof Error ? error : new Error(String(error));
    } finally {
      await rawLog.close();
    }
  })();

  function waitForMessage(
    predicate: (entry: RecordedMessage) => boolean,
    waitTimeoutMs: number,
  ): Promise<RecordedMessage> {
    const existing = recorded.find(predicate);
    if (existing !== undefined) {
      return Promise.resolve(existing);
    }
    return new Promise((resolveWait, rejectWait) => {
      const waiter = {
        predicate,
        resolve: (entry: RecordedMessage) => {
          clearTimeout(timer);
          resolveWait(entry);
        },
      };
      const timer = setTimeout(() => {
        messageWaiters.delete(waiter);
        rejectWait(
          streamError ??
            new Error(`Timed out after ${waitTimeoutMs}ms waiting for held query message`),
        );
      }, waitTimeoutMs);
      messageWaiters.add(waiter);
    });
  }

  const overallTimer = setTimeout(() => abortController.abort(), timeoutMs * 4);

  return {
    sessionId(): string {
      const withId = recorded
        .map((entry) => sessionIdOf(entry.message))
        .find((id): id is string => id !== undefined);
      if (withId === undefined) {
        throw new Error("held query has not emitted a session ID yet");
      }
      return withId;
    },
    messageCount(): number {
      return recorded.length;
    },
    messagesSince(watermark: number): RecordedMessage[] {
      return recorded.slice(watermark);
    },
    async sendAndAwaitResult(
      text: string,
      turnTimeoutMs: number,
    ): Promise<TurnCapture> {
      const firstSequence = recorded.length + 1;
      pendingInputs.push({
        type: "user",
        message: { role: "user", content: text },
        parent_tool_use_id: null,
      });
      releaseInput?.();
      const resultEntry = await waitForMessage(
        (entry) =>
          entry.sequence >= firstSequence && entry.message.type === "result",
        turnTimeoutMs,
      );
      const result = resultEntry.message as SDKResultMessage;
      const text_ = recorded
        .filter(
          (entry) =>
            entry.sequence >= firstSequence &&
            entry.sequence <= resultEntry.sequence &&
            entry.message.type === "assistant",
        )
        .map((entry) => assistantText(entry.message as never))
        .join("\n");
      return {
        resultSubtype: result.subtype,
        isError: result.subtype !== "success" && result.is_error,
        text: text_,
        firstSequence,
        lastSequence: resultEntry.sequence,
      };
    },
    async close(): Promise<void> {
      inputClosed = true;
      releaseInput?.();
      clearTimeout(overallTimer);
      const drainTimer = setTimeout(() => abortController.abort(), 15_000);
      try {
        await consumed;
      } finally {
        clearTimeout(drainTimer);
      }
      if (streamError !== undefined && !abortController.signal.aborted) {
        throw streamError;
      }
    },
  };
}

async function runExternalClaudeTurn(
  sessionId: string,
  cwd: string,
  prompt: string,
  logPath: string,
): Promise<ExternalClaudeTurn> {
  console.log(`→ external writer: claude -p --resume ${sessionId}`);
  const startedAt = Date.now();
  const child = spawn(
    "claude",
    ["-p", "--resume", sessionId, "--output-format", "json", prompt],
    { cwd, env: buildClaudeEnvironment(process.env), stdio: ["ignore", "pipe", "pipe"] },
  );
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk: Buffer) => {
    stdout += chunk.toString();
  });
  child.stderr.on("data", (chunk: Buffer) => {
    stderr += chunk.toString();
  });
  const exitCode = await new Promise<number>((resolveExit, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => resolveExit(code ?? -1));
  });
  const durationMs = Date.now() - startedAt;
  await mkdir(dirname(logPath), { recursive: true });
  await writeFile(
    logPath,
    `# claude -p --resume ${sessionId} (exit ${exitCode}, ${durationMs}ms)\n\n## stdout\n${stdout}\n\n## stderr\n${stderr}\n`,
    { encoding: "utf8", flag: "wx" },
  );
  console.log(`← external writer exited ${exitCode} after ${durationMs}ms`);

  let parsed: Record<string, unknown> | undefined;
  try {
    parsed = JSON.parse(stdout) as Record<string, unknown>;
  } catch {
    parsed = undefined;
  }
  return {
    exitCode,
    durationMs,
    sessionId:
      typeof parsed?.session_id === "string" ? parsed.session_id : undefined,
    resultSubtype:
      typeof parsed?.subtype === "string" ? parsed.subtype : undefined,
    resultText: typeof parsed?.result === "string" ? parsed.result : undefined,
    rawStdout: stdout,
    rawStderr: stderr,
  };
}

export function requireExternalSuccess(
  turn: ExternalClaudeTurn,
  expectedSessionId: string,
  expectedMarker: string,
  label: string,
): void {
  if (turn.exitCode !== 0) {
    throw new Error(
      `${label} failed with exit code ${turn.exitCode}: ${turn.rawStderr.slice(0, 500)}`,
    );
  }
  if (turn.sessionId !== expectedSessionId) {
    throw new Error(
      `${label} did not preserve session identity: expected ${expectedSessionId}, received ${turn.sessionId ?? "none"}`,
    );
  }
  if (turn.resultText === undefined || !turn.resultText.includes(expectedMarker)) {
    throw new Error(
      `${label} output did not include expected marker ${expectedMarker}`,
    );
  }
  console.log(`CONTINUITY VERIFIED: ${label} recalled ${expectedMarker}`);
}

function requireSuccess(
  turn: TurnCapture,
  marker: string,
  label: string,
): void {
  if (turn.resultSubtype !== "success") {
    throw new Error(`${label} failed with result ${turn.resultSubtype}`);
  }
  if (!turn.text.includes(marker)) {
    throw new Error(`${label} did not return marker ${marker}: ${turn.text}`);
  }
  console.log(`CONTINUITY VERIFIED: ${label} returned ${marker}`);
}

async function runSingleTurnResume(
  sessionId: string,
  cwd: string,
  prompt: string,
  rawLogPath: string,
  timeoutMs: number,
): Promise<TurnCapture & { sessionId: string }> {
  await mkdir(dirname(rawLogPath), { recursive: true });
  const rawLog = await open(rawLogPath, "wx");
  const abortController = new AbortController();
  const timer = setTimeout(() => abortController.abort(), timeoutMs);
  const stream = query({
    prompt,
    options: {
      abortController,
      allowedTools: [],
      cwd,
      env: buildClaudeEnvironment(process.env),
      maxTurns: 1,
      permissionMode: "dontAsk",
      persistSession: true,
      resume: sessionId,
      settingSources: [],
      tools: [],
    },
  });

  let sequence = 0;
  let result: SDKResultMessage | undefined;
  let text = "";
  let observedSessionId: string | undefined;
  try {
    for await (const message of stream) {
      sequence += 1;
      await rawLog.writeFile(`${JSON.stringify(message)}\n`);
      console.log(`← resume #${sequence} ${labelOf(message)}`);
      observedSessionId ??= sessionIdOf(message);
      const messageSessionId = sessionIdOf(message);
      if (messageSessionId !== undefined && messageSessionId !== sessionId) {
        throw new Error(
          `fresh resume changed identity: expected ${sessionId}, received ${messageSessionId}`,
        );
      }
      if (message.type === "assistant") {
        text += assistantText(message as never);
      } else if (message.type === "result") {
        result = message;
      }
    }
  } finally {
    clearTimeout(timer);
    await rawLog.close();
  }
  if (result === undefined || observedSessionId === undefined) {
    throw new Error("fresh resume emitted no result");
  }
  return {
    sessionId: observedSessionId,
    resultSubtype: result.subtype,
    isError: result.subtype !== "success" && result.is_error,
    text,
    firstSequence: 1,
    lastSequence: sequence,
  };
}

async function findSessionFile(sessionId: string): Promise<string> {
  const projectsRoot = join(homedir(), ".claude", "projects");
  const directories = await readdir(projectsRoot, { withFileTypes: true });
  const matches: string[] = [];
  for (const entry of directories) {
    if (!entry.isDirectory()) {
      continue;
    }
    const candidate = join(projectsRoot, entry.name, `${sessionId}.jsonl`);
    if (existsSync(candidate)) {
      matches.push(candidate);
    }
  }
  if (matches.length !== 1) {
    throw new Error(
      `Expected exactly one session file for ${sessionId}, found ${matches.length}`,
    );
  }
  return matches[0];
}

async function countLines(path: string): Promise<number> {
  const text = await readFile(path, "utf8");
  return text.split("\n").filter((line) => line.length > 0).length;
}

export function summarizeExternal(turn: ExternalClaudeTurn): Record<string, unknown> {
  return {
    exitCode: turn.exitCode,
    durationMs: turn.durationMs,
    sessionId: turn.sessionId,
    resultSubtype: turn.resultSubtype,
    resultText: turn.resultText,
  };
}

export function labelOf(message: SDKMessage): string {
  const subtype =
    "subtype" in message && typeof message.subtype === "string"
      ? `/${message.subtype}` : "";
  const state =
    message.type === "system" &&
    message.subtype === "session_state_changed"
      ? ` state=${message.state}` : "";
  return `${message.type}${subtype}${state}`;
}

function makeRunId(): string {
  return `${new Date().toISOString().replaceAll(/[:.]/g, "-")}-${Math.random()
    .toString(16)
    .slice(2, 10)}`;
}

async function delay(milliseconds: number): Promise<void> {
  await new Promise<void>((resolveDelay) =>
    setTimeout(resolveDelay, milliseconds),
  );
}

function requirePositive(value: string, flag: string): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`${flag} must be a positive number`);
  }
  return parsed;
}

function usage(): string {
  return [
    "Usage:",
    "  pnpm claude:held passive-hold [--cwd <path>] [--timeout-seconds <n>] [--settle-seconds <n>]",
    "  pnpm claude:held stale-write [--cwd <path>] [--timeout-seconds <n>] [--settle-seconds <n>]",
  ].join("\n");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error: unknown) => {
    console.error(
      `FAIL: ${error instanceof Error ? error.message : String(error)}`,
    );
    process.exitCode = 1;
  });
}
