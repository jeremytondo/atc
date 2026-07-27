import { randomUUID } from "node:crypto";
import { spawn } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  AppServerClient,
  type JsonObject,
  type ProtocolMessage,
  objectAt,
  stringAt,
} from "./app-server-client.ts";
import { PendingServerRequest } from "./pending-server-request.ts";
import {
  requireString,
  requireThread,
  threadIdsFromList,
  threadIdsFromLoadedList,
  turnIdsContainingAgentMarker,
  verifyInputQuestion,
  verifyExactPwdApproval,
  verifyNoReplacementThreads,
  verifyServerRequestAttribution,
  verifyThreadContainsAgentMarker,
  verifyThreadHasNoTurns,
  verifyTurnEventAttribution,
  verifyWaitingStatus,
} from "./protocol-evidence.ts";

interface ProbeOptions {
  command:
    | "create"
    | "resume"
    | "dormant"
    | "zero-turn-recovery"
    | "invalid-resume"
    | "multiplex"
    | "tui-round-trip"
    | "input-request"
    | "permission-request"
    | "interrupt"
    | "observer-writer";
  cwd: string;
  marker?: string;
  sessionPath?: string;
  timeoutMs: number;
  waitMs: number;
  holdMs: number;
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
  attributedEventCount: number;
}

interface TurnRequestOptions {
  approvalPolicy?: "never" | "on-request";
  approvalsReviewer?: "user";
  sandboxPolicy?: JsonObject;
  collaborationMode?: JsonObject;
}

interface ActiveTurn {
  id: string;
  firstEventIndex: number;
  completed: Promise<ProtocolMessage>;
}

interface StartedThread {
  id: string;
  model: string;
}

interface DormantRunRecord {
  version: 1;
  provider: "codex";
  scenario: "dormant-zero-turn";
  threadId: string;
  marker: string;
  cwd: string;
  waitStartedAt: string;
  firstTurnStartedAt: string;
  requestedWaitMs: number;
  observedWaitMs: number;
  rawLog: string;
}

interface GateRunRecord {
  version: 1;
  provider: "codex";
  scenario:
    | "zero-turn-recovery"
    | "invalid-resume"
    | "shared-process-multiplexing"
    | "native-tui-round-trip"
    | "input-request"
    | "permission-request"
    | "active-turn-interruption"
    | "observer-writer";
  cwd: string;
  completedAt: string;
  evidence: JsonObject;
  rawLogs: string[];
}

const probeRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = resolve(probeRoot, "../..");
const runsRoot = resolve(probeRoot, "runs/codex");

export async function main(argv = process.argv.slice(2)): Promise<void> {
  const options = parseArgs(argv);
  if (options.command === "create") {
    await createScenario(options);
  } else if (options.command === "resume") {
    await resumeScenario(options);
  } else if (options.command === "dormant") {
    await dormantScenario(options);
  } else if (options.command === "zero-turn-recovery") {
    await zeroTurnRecoveryScenario(options);
  } else if (options.command === "invalid-resume") {
    await invalidResumeScenario(options);
  } else if (options.command === "multiplex") {
    await multiplexScenario(options);
  } else if (options.command === "tui-round-trip") {
    await tuiRoundTripScenario(options);
  } else if (options.command === "input-request") {
    await inputRequestScenario(options);
  } else if (options.command === "permission-request") {
    await permissionRequestScenario(options);
  } else if (options.command === "interrupt") {
    await interruptScenario(options);
  } else {
    await observerWriterScenario(options);
  }
}

export function parseArgs(argv: string[]): ProbeOptions {
  const [command, ...rest] = argv;
  if (
    command !== "create" &&
    command !== "resume" &&
    command !== "dormant" &&
    command !== "zero-turn-recovery" &&
    command !== "invalid-resume" &&
    command !== "multiplex" &&
    command !== "tui-round-trip" &&
    command !== "input-request" &&
    command !== "permission-request" &&
    command !== "interrupt" &&
    command !== "observer-writer"
  ) {
    throw new Error(usage());
  }

  let cwd = repoRoot;
  let marker: string | undefined;
  let sessionPath: string | undefined;
  let timeoutMs = 300_000;
  let waitMs = 30_000;
  let holdMs = 2_000;

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
      case "--wait-seconds": {
        const seconds = Number(value);
        if (!Number.isFinite(seconds) || seconds <= 0) {
          throw new Error("--wait-seconds must be a positive number");
        }
        waitMs = seconds * 1_000;
        break;
      }
      case "--hold-seconds": {
        const seconds = Number(value);
        if (!Number.isFinite(seconds) || seconds <= 0) {
          throw new Error("--hold-seconds must be a positive number");
        }
        holdMs = seconds * 1_000;
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

  return {
    command,
    cwd: canonicalPath(cwd),
    marker,
    sessionPath,
    timeoutMs,
    waitMs,
    holdMs,
  };
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

export function verifyStartedThread(
  expectedCwd: string,
  result: JsonObject,
): JsonObject {
  const thread = requireThread(result, "thread/start");
  const responseCwd = requireString(thread, "cwd", "thread/start thread");
  if (canonicalPath(responseCwd) !== canonicalPath(expectedCwd)) {
    throw new Error(
      `thread/start cwd mismatch: requested ${expectedCwd}, received ${responseCwd}`,
    );
  }
  if (thread.ephemeral === true) {
    throw new Error("thread/start unexpectedly returned an ephemeral thread");
  }
  requireString(thread, "id", "thread/start thread");
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
    const threadId = await startDurableThread(client, options.cwd);

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

async function dormantScenario(options: ProbeOptions): Promise<void> {
  const marker =
    options.marker ?? `ATC-DORMANT-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.dormant-zero-turn.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.dormant-zero-turn.json`);

  console.log("Codex provider identity/resume POC — dormant zero-turn");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log(`requested dormant interval: ${options.waitMs}ms`);
  console.log(`raw provider log: ${relative(process.cwd(), rawLogPath)}`);
  console.log("safety: read-only sandbox, approval policy never");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath,
  });

  try {
    const threadId = await startDurableThread(client, options.cwd);
    const waitStartedAtMs = Date.now();
    console.log(
      `DORMANT: keeping thread ${threadId} turnless for ${options.waitMs}ms`,
    );
    await delay(options.waitMs);
    const firstTurnStartedAtMs = Date.now();
    const observedWaitMs = firstTurnStartedAtMs - waitStartedAtMs;
    if (observedWaitMs < options.waitMs) {
      throw new Error(
        `Dormant interval ended early: expected at least ${options.waitMs}ms, observed ${observedWaitMs}ms`,
      );
    }
    console.log(
      `DORMANT INTERVAL VERIFIED: observed ${observedWaitMs}ms without turn/start`,
    );

    const first = await runTurn(
      client,
      threadId,
      [
        "This is the first turn after a dormant zero-turn interval.",
        "Do not use tools, run commands, or modify files.",
        `Reply with exactly: DORMANT THREAD ACTIVE: ${marker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(first, "first post-dormancy turn");
    assertMarker(first, marker, "first post-dormancy turn");

    const record: DormantRunRecord = {
      version: 1,
      provider: "codex",
      scenario: "dormant-zero-turn",
      threadId,
      marker,
      cwd: options.cwd,
      waitStartedAt: new Date(waitStartedAtMs).toISOString(),
      firstTurnStartedAt: new Date(firstTurnStartedAtMs).toISOString(),
      requestedWaitMs: options.waitMs,
      observedWaitMs,
      rawLog: relative(dirname(resultPath), rawLogPath),
    };
    await writeFile(resultPath, `${JSON.stringify(record, null, 2)}\n`, {
      encoding: "utf8",
      flag: "wx",
    });

    console.log("");
    console.log(
      `PASS: thread ${threadId} accepted its first turn after remaining dormant for ${observedWaitMs}ms.`,
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await client.stop();
  }
}

async function zeroTurnRecoveryScenario(
  options: ProbeOptions,
): Promise<void> {
  const marker =
    options.marker ?? `ATC-RECOVERY-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const createLogPath = resolve(
    runsRoot,
    `${runId}.zero-turn-recovery.create.jsonl`,
  );
  const resumeLogPath = resolve(
    runsRoot,
    `${runId}.zero-turn-recovery.resume.jsonl`,
  );
  const resultPath = resolve(
    runsRoot,
    `${runId}.zero-turn-recovery.json`,
  );

  console.log("Codex provider identity/resume POC — zero-turn recovery");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log("safety: read-only sandbox, approval policy never");

  const creatingClient = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: createLogPath,
  });
  let threadId: string;
  try {
    threadId = await startDurableThread(creatingClient, options.cwd);
    console.log(
      `ZERO TURNS VERIFIED: stopping app-server before thread ${threadId} receives turn/start`,
    );
  } finally {
    await creatingClient.stop();
  }

  const resumingClient = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
  });
  try {
    let resumeResult: JsonObject;
    try {
      resumeResult = await resumingClient.request("thread/resume", {
        threadId,
        approvalPolicy: "never",
        sandbox: "read-only",
      });
    } catch (error) {
      const resumeError =
        error instanceof Error ? error.message : String(error);
      await writeGateRecord(resultPath, {
        version: 1,
        provider: "codex",
        scenario: "zero-turn-recovery",
        cwd: options.cwd,
        completedAt: new Date().toISOString(),
        evidence: {
          outcome: "fail",
          threadId,
          marker,
          resumeError,
          firstTurnStarted: false,
        },
        rawLogs: relativeLogs(resultPath, [createLogPath, resumeLogPath]),
      });
      console.log(`FAILURE ARTIFACT: ${relative(process.cwd(), resultPath)}`);
      throw error;
    }
    const thread = verifyResumedThread(
      sessionExpectation(threadId, marker, options.cwd, createLogPath),
      resumeResult,
    );
    verifyThreadHasNoTurns(thread, "zero-turn thread/resume response");
    console.log(
      `RECOVERY VERIFIED: fresh app-server resumed turnless thread ${threadId} with cwd=${requireString(thread, "cwd", "resumed thread")}`,
    );

    const first = await runTurn(
      resumingClient,
      threadId,
      [
        "This is the first turn after app-server restarted around a zero-turn thread.",
        "Do not use tools, run commands, or modify files.",
        `Reply with exactly: RECOVERED ZERO-TURN THREAD: ${marker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(first, "first recovered turn");
    assertMarker(first, marker, "first recovered turn");

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "zero-turn-recovery",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        outcome: "pass",
        threadId,
        marker,
        resumedWithTurnCount: 0,
        firstTurnId: first.id,
        firstTurnAttributedEventCount: first.attributedEventCount,
      },
      rawLogs: relativeLogs(resultPath, [createLogPath, resumeLogPath]),
    });

    console.log("");
    console.log(
      `PASS: fresh app-server recovered exact zero-turn thread ${threadId}, verified cwd, and completed its first turn.`,
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await resumingClient.stop();
  }
}

async function invalidResumeScenario(options: ProbeOptions): Promise<void> {
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.invalid-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.invalid-resume.json`);
  const invalidThreadId = randomUUID();

  console.log("Codex provider identity/resume POC — invalid resume safety");
  console.log(`cwd: ${options.cwd}`);
  console.log(`invalid thread id: ${invalidThreadId}`);
  console.log("safety: thread/resume only; no thread/start fallback");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath,
  });
  try {
    const before = await listAllThreadIds(client);
    const invalidError = await requireRequestError(
      () =>
        client.request("thread/resume", {
          threadId: invalidThreadId,
          approvalPolicy: "never",
          sandbox: "read-only",
        }),
      "invalid thread ID",
    );
    const missingError = await requireRequestError(
      () =>
        client.request("thread/resume", {
          approvalPolicy: "never",
          sandbox: "read-only",
        }),
      "missing thread ID",
    );
    const after = await listAllThreadIds(client);
    verifyNoReplacementThreads(before, after);

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "invalid-resume",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        invalidThreadId,
        beforeThreadIds: before,
        afterThreadIds: after,
        invalidError,
        missingError,
        replacementThreadCount: 0,
      },
      rawLogs: relativeLogs(resultPath, [rawLogPath]),
    });

    console.log(`EXPECTED ERROR (invalid): ${invalidError}`);
    console.log(`EXPECTED ERROR (missing): ${missingError}`);
    console.log(
      `THREAD LIST VERIFIED: ${before.length} before, ${after.length} after, no replacement IDs`,
    );
    console.log("");
    console.log(
      "PASS: invalid and missing resume IDs failed clearly without creating a replacement thread.",
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await client.stop();
  }
}

async function multiplexScenario(options: ProbeOptions): Promise<void> {
  const markerA =
    options.marker ??
    `ATC-MULTIPLEX-A-${randomUUID().slice(0, 8).toUpperCase()}`;
  const markerB = `ATC-MULTIPLEX-B-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.multiplex.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.multiplex.json`);

  console.log("Codex provider identity/resume POC — shared-process multiplexing");
  console.log(`cwd: ${options.cwd}`);
  console.log(`markers: ${markerA}, ${markerB}`);
  console.log("safety: read-only sandbox, approval policy never");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath,
  });
  try {
    const threadA = await startDurableThread(client, options.cwd);
    const threadB = await startDurableThread(client, options.cwd);
    if (threadA === threadB) {
      throw new Error("thread/start returned the same ID for both threads");
    }

    const loaded = threadIdsFromLoadedList(
      await client.request("thread/loaded/list"),
    );
    for (const threadId of [threadA, threadB]) {
      if (!loaded.includes(threadId)) {
        throw new Error(
          `thread/loaded/list did not include created thread ${threadId}`,
        );
      }
    }
    console.log(
      `LOADED THREADS VERIFIED: ${threadA} and ${threadB} share one app-server`,
    );

    const turns = [];
    turns.push(
      await runMarkerTurn(
        client,
        threadA,
        markerA,
        "A1",
        options.timeoutMs,
      ),
    );
    turns.push(
      await runMarkerTurn(
        client,
        threadB,
        markerB,
        "B1",
        options.timeoutMs,
      ),
    );
    turns.push(
      await runRecallTurn(
        client,
        threadA,
        markerA,
        "A2",
        options.timeoutMs,
      ),
    );
    turns.push(
      await runRecallTurn(
        client,
        threadB,
        markerB,
        "B2",
        options.timeoutMs,
      ),
    );

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "shared-process-multiplexing",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadA,
        threadB,
        markerA,
        markerB,
        loadedThreadIds: loaded,
        turnIds: turns.map((turn) => turn.id),
        attributedEventCounts: turns.map(
          (turn) => turn.attributedEventCount,
        ),
      },
      rawLogs: relativeLogs(resultPath, [rawLogPath]),
    });

    console.log("");
    console.log(
      "PASS: two loaded threads completed A1 → B1 → A2 → B2 with exact marker continuity and per-turn event attribution.",
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await client.stop();
  }
}

async function tuiRoundTripScenario(options: ProbeOptions): Promise<void> {
  if (!process.stdin.isTTY || !process.stdout.isTTY) {
    throw new Error(
      "tui-round-trip requires an interactive terminal so the native Codex TUI can be observed and exited",
    );
  }

  const marker =
    options.marker ?? `ATC-TUI-${randomUUID().slice(0, 8).toUpperCase()}`;
  const seedMarker = `ATC-TUI-SEED-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const createLogPath = resolve(
    runsRoot,
    `${runId}.tui-round-trip.create.jsonl`,
  );
  const resumeLogPath = resolve(
    runsRoot,
    `${runId}.tui-round-trip.resume.jsonl`,
  );
  const resultPath = resolve(runsRoot, `${runId}.tui-round-trip.json`);

  console.log("Codex provider identity/resume POC — native TUI round trip");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log("safety: read-only sandbox, approval policy never");

  const creatingClient = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: createLogPath,
  });
  let threadId: string;
  try {
    threadId = await startDurableThread(creatingClient, options.cwd);
    const seed = await runMarkerTurn(
      creatingClient,
      threadId,
      seedMarker,
      "app-server seed",
      options.timeoutMs,
    );
    console.log(
      `APP-SERVER SEED VERIFIED: turn ${seed.id} made thread resumable before the TUI handoff`,
    );
  } finally {
    await creatingClient.stop();
  }

  console.log("");
  console.log(`OPENING NATIVE TUI FOR EXACT THREAD: ${threadId}`);
  console.log(
    "Wait for the exact marker response, then exit the TUI to continue verification.",
  );
  await runNativeTui(threadId, options.cwd, marker);

  const resumingClient = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
  });
  try {
    const resumeResult = await resumingClient.request("thread/resume", {
      threadId,
      approvalPolicy: "never",
      sandbox: "read-only",
    });
    const thread = verifyResumedThread(
      sessionExpectation(threadId, marker, options.cwd, createLogPath),
      resumeResult,
    );
    const matchingTuiTurns = verifyThreadContainsAgentMarker(
      thread,
      marker,
      "app-server resume after native TUI",
    );
    console.log(
      `TUI TURN VERIFIED IN RESUMED HISTORY: found marker ${marker}`,
    );

    const verification = await runTuiRecallTurn(
      resumingClient,
      threadId,
      marker,
      options.timeoutMs,
    );

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "native-tui-round-trip",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        marker,
        seedMarker,
        matchingTuiTurns,
        appServerVerificationTurnId: verification.id,
        appServerVerificationAttributedEventCount:
          verification.attributedEventCount,
      },
      rawLogs: relativeLogs(resultPath, [createLogPath, resumeLogPath]),
    });

    console.log("");
    console.log(
      `PASS: app-server thread ${threadId} round-tripped through the native TUI and back with its TUI turn intact.`,
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await resumingClient.stop();
  }
}

async function inputRequestScenario(options: ProbeOptions): Promise<void> {
  const marker =
    options.marker ?? `ATC-INPUT-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.input-request.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.input-request.json`);
  const pending = new PendingServerRequest("item/tool/requestUserInput");

  console.log("Codex provider identity/resume POC — input request");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log(`pending hold: ${options.holdMs}ms`);
  console.log("safety: request_user_input only; no command or file tools");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath,
    handleServerRequest: pending.handle,
    experimentalApi: true,
  });
  try {
    const startedThread = await startDurableThreadDetails(
      client,
      options.cwd,
    );
    const threadId = startedThread.id;
    const collaborationMode = await planCollaborationMode(
      client,
      startedThread.model,
    );
    const waitingStatus = waitForWaitingStatus(
      client,
      threadId,
      "waitingOnUserInput",
      options.timeoutMs,
    );
    const turnPromise = runTurn(
      client,
      threadId,
      [
        "This is a bounded provider input-state POC.",
        "Do not use commands, files, network access, or any tool except request_user_input.",
        "Call request_user_input exactly once with one question:",
        'id "choice", header "Choice", question "Choose the POC answer",',
        'and exactly two options in order: "Alpha" then "Beta".',
        `After receiving the answer, reply with exactly: INPUT RECEIVED: Alpha ${marker}`,
      ].join(" "),
      options.timeoutMs,
      {
        collaborationMode,
      },
    );

    const [request, statusMessage] = await Promise.all([
      pending.wait(options.timeoutMs),
      waitingStatus,
    ]);
    const turnId = verifyServerRequestAttribution(
      request,
      threadId,
      "item/tool/requestUserInput",
      "Codex input request",
    );
    verifyInputQuestion(request, "choice", ["Alpha", "Beta"]);
    verifyWaitingStatus(
      statusMessage,
      threadId,
      "waitingOnUserInput",
      "Codex input wait",
    );
    console.log(
      `NEEDS INPUT VERIFIED: turn ${turnId} emitted waitingOnUserInput with request #${String(request.id)}`,
    );

    await delay(options.holdMs);
    const respondedAt = new Date().toISOString();
    pending.respond({
      answers: {
        choice: {
          answers: ["Alpha"],
        },
      },
    });

    const result = await turnPromise;
    assertSuccessfulTurn(result, "input request turn");
    assertMarker(result, marker, "input request turn");
    if (!result.agentText.includes("INPUT RECEIVED: Alpha")) {
      throw new Error("Input request turn did not use the correlated answer");
    }

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "input-request",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        turnId,
        marker,
        requestId: request.id,
        requestMethod: request.method,
        requestSequence: request.sequence,
        requestReceivedAt: request.receivedAt,
        respondedAt,
        answer: "Alpha",
        waitingFlag: "waitingOnUserInput",
        turnStatus: result.status,
        attributedEventCount: result.attributedEventCount,
      },
      rawLogs: relativeLogs(resultPath, [rawLogPath]),
    });

    console.log("");
    console.log(
      "PASS: Codex exposed a correlated input request and authoritative waitingOnUserInput state, accepted the answer, and returned to a completed turn.",
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await client.stop();
  }
}

async function permissionRequestScenario(
  options: ProbeOptions,
): Promise<void> {
  const marker =
    options.marker ??
    `ATC-PERMISSION-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.permission-request.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.permission-request.json`);
  const pending = new PendingServerRequest(
    "item/commandExecution/requestApproval",
  );

  console.log("Codex provider identity/resume POC — permission request");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log(`pending hold: ${options.holdMs}ms`);
  console.log("safety: exact pwd command only; no compound command or write");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath,
    handleServerRequest: pending.handle,
  });
  try {
    const threadId = await startDurableThread(client, options.cwd);
    const waitingStatus = waitForWaitingStatus(
      client,
      threadId,
      "waitingOnApproval",
      options.timeoutMs,
    );
    const turnPromise = runTurn(
      client,
      threadId,
      [
        "This is a bounded harmless permission POC.",
        "Use exec_command exactly once with cmd exactly \"pwd\",",
        `workdir exactly ${JSON.stringify(options.cwd)},`,
        'and sandbox_permissions exactly "require_escalated" so the client receives an approval request.',
        "Do not use shell wrappers, arguments, compound commands, or any other tool.",
        `After the command succeeds, reply with exactly: PERMISSION COMPLETE: ${options.cwd} ${marker}`,
      ].join(" "),
      options.timeoutMs,
      {
        approvalPolicy: "on-request",
        approvalsReviewer: "user",
        sandboxPolicy: {
          type: "readOnly",
          networkAccess: false,
        },
      },
    );

    const [request, statusMessage] = await Promise.all([
      pending.wait(options.timeoutMs),
      waitingStatus,
    ]);
    const turnId = verifyServerRequestAttribution(
      request,
      threadId,
      "item/commandExecution/requestApproval",
      "Codex permission request",
    );
    let command: string;
    try {
      command = verifyExactPwdApproval(request);
    } catch (error) {
      pending.reject("Permission probe accepts only the exact command pwd");
      void turnPromise.catch(() => undefined);
      throw error;
    }
    const requestCwd = requireString(
      request.params,
      "cwd",
      "Codex permission request",
    );
    if (canonicalPath(requestCwd) !== options.cwd) {
      pending.reject("Permission probe cwd mismatch");
      void turnPromise.catch(() => undefined);
      throw new Error(
        `Permission request cwd mismatch: expected ${options.cwd}, received ${requestCwd}`,
      );
    }
    verifyWaitingStatus(
      statusMessage,
      threadId,
      "waitingOnApproval",
      "Codex permission wait",
    );
    console.log(
      `PERMISSION WAIT VERIFIED: turn ${turnId} requested exact pwd with waitingOnApproval`,
    );

    await delay(options.holdMs);
    const respondedAt = new Date().toISOString();
    pending.respond({ decision: "accept" });

    const result = await turnPromise;
    assertSuccessfulTurn(result, "permission request turn");
    assertMarker(result, marker, "permission request turn");
    if (!result.agentText.includes(options.cwd)) {
      throw new Error(
        "Permission request turn did not return the expected working directory",
      );
    }

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "permission-request",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        turnId,
        marker,
        requestId: request.id,
        requestMethod: request.method,
        requestSequence: request.sequence,
        requestReceivedAt: request.receivedAt,
        respondedAt,
        command,
        commandCwd: requestCwd,
        decision: "accept",
        waitingFlag: "waitingOnApproval",
        turnStatus: result.status,
        attributedEventCount: result.attributedEventCount,
      },
      rawLogs: relativeLogs(resultPath, [rawLogPath]),
    });

    console.log("");
    console.log(
      "PASS: Codex exposed a correlated harmless approval request and authoritative waitingOnApproval state, then completed exact pwd after acceptance.",
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await client.stop();
  }
}

async function interruptScenario(options: ProbeOptions): Promise<void> {
  const marker =
    options.marker ??
    `ATC-INTERRUPT-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const interruptLogPath = resolve(runsRoot, `${runId}.interrupt.jsonl`);
  const resumeLogPath = resolve(runsRoot, `${runId}.interrupt.resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.interrupt.json`);

  console.log("Codex provider identity/resume POC — active turn interruption");
  console.log(`cwd: ${options.cwd}`);
  console.log(`seed marker: ${marker}`);
  console.log(`active hold: ${options.holdMs}ms`);
  console.log("safety: bounded sleep 30 command in read-only sandbox");

  const client = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: interruptLogPath,
  });
  let threadId: string;
  let interruptedTurnId: string;
  let interruptedEventCount: number;
  let commandItemId: string;
  let command: string;
  let interruptRequestedAt: string;
  let interruptCompletedAt: string;
  try {
    threadId = await startDurableThread(client, options.cwd);
    const seed = await runMarkerTurn(
      client,
      threadId,
      marker,
      "interrupt seed",
      options.timeoutMs,
    );
    console.log(
      `SEED VERIFIED: turn ${seed.id} materialized resumable history`,
    );

    const commandStarted = client.waitForNotification(
      "item/started",
      (params) => {
        const item = objectAt(params, "item");
        return (
          params.threadId === threadId &&
          stringAt(item, "type") === "commandExecution" &&
          stringAt(item, "command")?.includes("sleep 30") === true
        );
      },
      options.timeoutMs,
    );
    const activeStatus = client.waitForNotification(
      "thread/status/changed",
      (params) =>
        params.threadId === threadId &&
        objectAt(params, "status")?.type === "active",
      options.timeoutMs,
    );
    const activeTurn = await startTurn(
      client,
      threadId,
      [
        "This is a bounded active-turn interruption POC.",
        'Use exec_command exactly once with cmd exactly "sleep 30",',
        `workdir exactly ${JSON.stringify(options.cwd)}, and do not run it in the background.`,
        "Do not use any other tool or modify files.",
        "After the command finishes, reply with INTERRUPT COMMAND FINISHED.",
      ].join(" "),
      options.timeoutMs,
    );
    interruptedTurnId = activeTurn.id;

    const [commandMessage] = await Promise.all([
      commandStarted,
      activeStatus,
    ]);
    const commandItem = objectAt(commandMessage.params, "item");
    commandItemId = requireString(
      commandItem,
      "id",
      "interrupt command item",
    );
    command = requireString(
      commandItem,
      "command",
      "interrupt command item",
    );
    if (commandMessage.params?.turnId !== interruptedTurnId) {
      throw new Error(
        `Interrupt command item belonged to ${String(commandMessage.params?.turnId)} instead of ${interruptedTurnId}`,
      );
    }
    console.log(
      `ACTIVE TURN VERIFIED: turn ${interruptedTurnId} is running command ${JSON.stringify(command)}`,
    );

    await delay(options.holdMs);
    interruptRequestedAt = new Date().toISOString();
    await client.request("turn/interrupt", {
      threadId,
      turnId: interruptedTurnId,
    });
    interruptCompletedAt = new Date().toISOString();
    console.log(
      `INTERRUPT RECEIPT: turn/interrupt accepted for ${interruptedTurnId}`,
    );

    const completedMessage = await activeTurn.completed;
    const completedTurn = objectAt(completedMessage.params, "turn");
    const completedId = requireString(
      completedTurn,
      "id",
      "interrupted turn/completed",
    );
    if (completedId !== interruptedTurnId) {
      throw new Error(
        `Interrupted turn identity mismatch: expected ${interruptedTurnId}, received ${completedId}`,
      );
    }
    const interruptedStatus = requireString(
      completedTurn,
      "status",
      "interrupted turn/completed",
    );
    if (interruptedStatus !== "interrupted") {
      throw new Error(
        `Interrupted turn completed with status ${interruptedStatus}`,
      );
    }
    interruptedEventCount = verifyTurnEventAttribution(
      client.messagesSince(activeTurn.firstEventIndex),
      threadId,
      interruptedTurnId,
      `interrupted turn ${interruptedTurnId}`,
    );
    console.log(
      `INTERRUPTED STATE VERIFIED: turn/completed status=${interruptedStatus}`,
    );
  } finally {
    await client.stop();
  }

  const resumeClient = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
  });
  try {
    const resumedThread = verifyResumedThread(
      sessionExpectation(threadId, marker, options.cwd, interruptLogPath),
      await resumeClient.request("thread/resume", {
        threadId,
        approvalPolicy: "never",
        sandbox: "read-only",
      }),
    );
    const interruptedTurns = Array.isArray(resumedThread.turns)
      ? resumedThread.turns.filter(
          (turn) =>
            isRecord(turn) &&
            turn.id === interruptedTurnId &&
            turn.status === "interrupted",
        ).length
      : 0;
    if (interruptedTurns !== 1) {
      throw new Error(
        `Resumed history did not contain interrupted turn ${interruptedTurnId}`,
      );
    }
    const verification = await runRecallTurn(
      resumeClient,
      threadId,
      marker,
      "post-interrupt resume",
      options.timeoutMs,
    );

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "active-turn-interruption",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        seedMarker: marker,
        interruptedTurnId,
        commandItemId,
        command,
        interruptRequestedAt,
        interruptCompletedAt,
        interruptedStatus: "interrupted",
        interruptedEventCount,
        resumedInterruptedTurnCount: interruptedTurns,
        resumeVerificationTurnId: verification.id,
        resumeVerificationAttributedEventCount:
          verification.attributedEventCount,
      },
      rawLogs: relativeLogs(resultPath, [
        interruptLogPath,
        resumeLogPath,
      ]),
    });

    console.log("");
    console.log(
      "PASS: Codex interrupted a provably active command turn, persisted status interrupted, and resumed the exact thread and context in a fresh process.",
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await resumeClient.stop();
  }
}

async function observerWriterScenario(
  options: ProbeOptions,
): Promise<void> {
  const seedMarker =
    options.marker ??
    `ATC-OBSERVER-SEED-${randomUUID().slice(0, 8).toUpperCase()}`;
  const markerA = `ATC-OBSERVER-A-${randomUUID().slice(0, 8).toUpperCase()}`;
  const markerB = `ATC-OBSERVER-B-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const writerALogPath = resolve(
    runsRoot,
    `${runId}.observer-writer.a.jsonl`,
  );
  const clientBLogPath = resolve(
    runsRoot,
    `${runId}.observer-writer.b.jsonl`,
  );
  const resumeLogPath = resolve(
    runsRoot,
    `${runId}.observer-writer.resume.jsonl`,
  );
  const resultPath = resolve(runsRoot, `${runId}.observer-writer.json`);
  const pendingA = new PendingServerRequest("item/tool/requestUserInput");

  console.log("Codex provider identity/resume POC — observer and writer");
  console.log(`cwd: ${options.cwd}`);
  console.log(`markers: ${seedMarker}, ${markerA}, ${markerB}`);
  console.log(
    "safety: writer A waits on structured input; writer B uses a no-tool marker turn",
  );

  const writerA = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: writerALogPath,
    handleServerRequest: pendingA.handle,
    experimentalApi: true,
  });
  let clientB: AppServerClient | undefined;
  let threadId: string;
  let turnAId: string;
  let writerAResult: TurnResult;
  let clientBResumeError: string | undefined;
  let clientBResumeTurnIds: string[] = [];
  let writerBStartError: string | undefined;
  let writerBTurnId: string | undefined;
  let writerBStatus: string | undefined;
  let writerBText = "";
  let clientBSawWriterAEvents = false;
  try {
    const startedThread = await startDurableThreadDetails(
      writerA,
      options.cwd,
    );
    threadId = startedThread.id;
    await runMarkerTurn(
      writerA,
      threadId,
      seedMarker,
      "observer seed",
      options.timeoutMs,
    );
    const collaborationMode = await planCollaborationMode(
      writerA,
      startedThread.model,
    );

    const waitingA = waitForWaitingStatus(
      writerA,
      threadId,
      "waitingOnUserInput",
      options.timeoutMs,
    );
    const turnAPromise = runTurn(
      writerA,
      threadId,
      [
        "This is writer A in a controlled second-client POC.",
        "Do not use commands, files, network access, or any tool except request_user_input.",
        "Call request_user_input exactly once with one question:",
        'id "continue", header "Continue", question "Release writer A",',
        'and exactly two options in order: "Alpha" then "Beta".',
        `After receiving Alpha, reply with exactly: WRITER A: ${markerA}`,
      ].join(" "),
      options.timeoutMs,
      {
        collaborationMode,
      },
    );
    const [requestA, waitingAMessage] = await Promise.all([
      pendingA.wait(options.timeoutMs),
      waitingA,
    ]);
    turnAId = verifyServerRequestAttribution(
      requestA,
      threadId,
      "item/tool/requestUserInput",
      "writer A input request",
    );
    verifyInputQuestion(requestA, "continue", ["Alpha", "Beta"]);
    verifyWaitingStatus(
      waitingAMessage,
      threadId,
      "waitingOnUserInput",
      "writer A wait",
    );
    console.log(
      `WRITER A HELD: turn ${turnAId} is waitingOnUserInput`,
    );

    clientB = await AppServerClient.start({
      cwd: options.cwd,
      rawLogPath: clientBLogPath,
      requestTimeoutMs: Math.min(options.timeoutMs, 30_000),
    });
    let clientBResumeResult: JsonObject | undefined;
    try {
      clientBResumeResult = await clientB.request("thread/resume", {
        threadId,
        approvalPolicy: "never",
        sandbox: "read-only",
      });
      const resumed = verifyResumedThread(
        sessionExpectation(
          threadId,
          seedMarker,
          options.cwd,
          writerALogPath,
        ),
        clientBResumeResult,
      );
      clientBResumeTurnIds = turnIds(resumed);
      console.log(
        `SECOND CLIENT RESUMED: exact thread ${threadId} with ${clientBResumeTurnIds.length} visible turn(s)`,
      );
    } catch (error) {
      clientBResumeError =
        error instanceof Error ? error.message : String(error);
      console.log(`SECOND CLIENT RESUME REJECTED: ${clientBResumeError}`);
    }

    const observerEventIndex = clientB.messageCount;
    let writerBActive: ActiveTurn | undefined;
    if (clientBResumeResult !== undefined) {
      try {
        writerBActive = await startTurn(
          clientB,
          threadId,
          [
            "This is writer B in a controlled concurrent-writer POC.",
            "Do not use tools, commands, files, or network access.",
            `Reply with exactly: WRITER B: ${markerB}`,
          ].join(" "),
          options.timeoutMs,
        );
        writerBTurnId = writerBActive.id;
        console.log(
          `SECOND WRITER ACCEPTED: turn/start returned ${writerBTurnId} while writer A was pending`,
        );
      } catch (error) {
        writerBStartError =
          error instanceof Error ? error.message : String(error);
        console.log(`SECOND WRITER REJECTED: ${writerBStartError}`);
      }
    }

    await delay(options.holdMs);
    pendingA.respond({
      answers: {
        continue: {
          answers: ["Alpha"],
        },
      },
    });
    writerAResult = await turnAPromise;
    assertSuccessfulTurn(writerAResult, "writer A turn");
    assertMarker(writerAResult, markerA, "writer A turn");

    if (writerBActive !== undefined) {
      const completedB = await writerBActive.completed;
      const completedTurnB = objectAt(completedB.params, "turn");
      const completedBId = requireString(
        completedTurnB,
        "id",
        "writer B turn/completed",
      );
      if (completedBId !== writerBActive.id) {
        throw new Error(
          `Writer B turn identity mismatch: expected ${writerBActive.id}, received ${completedBId}`,
        );
      }
      writerBStatus = requireString(
        completedTurnB,
        "status",
        "writer B turn/completed",
      );
      writerBText = agentTextFromMessages(
        clientB.messagesSince(writerBActive.firstEventIndex),
        threadId,
        writerBActive.id,
      );
      console.log(
        `SECOND WRITER TERMINAL STATE: ${completedBId} status=${writerBStatus}`,
      );
    }

    await delay(Math.min(options.holdMs, 2_000));
    clientBSawWriterAEvents = clientB
      .messagesSince(observerEventIndex)
      .some(
        (message) =>
          message.params?.threadId === threadId &&
          (message.params?.turnId === turnAId ||
            stringAt(objectAt(message.params, "turn"), "id") === turnAId),
      );
    console.log(
      `LIVE OBSERVER VISIBILITY: ${clientBSawWriterAEvents ? "writer A events received" : "no writer A events received"}`,
    );
  } finally {
    await writerA.stop();
    await clientB?.stop();
  }

  const resumeClient = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
  });
  try {
    const resumedThread = verifyResumedThread(
      sessionExpectation(threadId, seedMarker, options.cwd, writerALogPath),
      await resumeClient.request("thread/resume", {
        threadId,
        approvalPolicy: "never",
        sandbox: "read-only",
      }),
    );
    const freshResumeTurnIds = turnIds(resumedThread);
    const freshResumeHasWriterA = threadHasAgentMarker(
      resumedThread,
      markerA,
    );
    const freshResumeHasWriterB = threadHasAgentMarker(
      resumedThread,
      markerB,
    );
    const writerAMarkerTurnIds = turnIdsContainingAgentMarker(
      resumedThread,
      markerA,
    );
    const writerBMarkerTurnIds = turnIdsContainingAgentMarker(
      resumedThread,
      markerB,
    );
    const markersShareTurn = writerAMarkerTurnIds.some((turnId) =>
      writerBMarkerTurnIds.includes(turnId),
    );
    if (!freshResumeHasWriterA) {
      throw new Error(
        "Fresh resume lost the completed writer A turn from the primary client",
      );
    }
    const verification = await runObserverRecallTurn(
      resumeClient,
      threadId,
      markerA,
      options.timeoutMs,
    );

    await writeGateRecord(resultPath, {
      version: 1,
      provider: "codex",
      scenario: "observer-writer",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        seedMarker,
        writerAMarker: markerA,
        writerBMarker: markerB,
        writerATurnId: turnAId,
        writerAStatus: writerAResult.status,
        clientBResumeError: clientBResumeError ?? null,
        clientBResumeTurnIds,
        clientBSawWriterAEvents,
        writerBStartError: writerBStartError ?? null,
        writerBTurnId: writerBTurnId ?? null,
        writerBStatus: writerBStatus ?? null,
        writerBReturnedMarker: writerBText.includes(markerB),
        freshResumeTurnIds,
        freshResumeHasWriterA,
        freshResumeHasWriterB,
        writerAMarkerTurnIds,
        writerBMarkerTurnIds,
        markersShareTurn,
        resumeVerificationTurnId: verification.id,
      },
      rawLogs: relativeLogs(resultPath, [
        writerALogPath,
        clientBLogPath,
        resumeLogPath,
      ]),
    });

    console.log("");
    console.log(
      "PASS: recorded Codex second-client observer visibility, concurrent writer behavior, and exact fresh-process resume state.",
    );
    console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
  } finally {
    await resumeClient.stop();
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

async function startDurableThread(
  client: AppServerClient,
  cwd: string,
): Promise<string> {
  return (await startDurableThreadDetails(client, cwd)).id;
}

async function startDurableThreadDetails(
  client: AppServerClient,
  cwd: string,
): Promise<StartedThread> {
  const startResult = await client.request("thread/start", {
    cwd,
    approvalPolicy: "never",
    sandbox: "read-only",
    serviceName: "atc_provider_identity_resume_poc",
    ephemeral: false,
  });
  const thread = verifyStartedThread(cwd, startResult);
  const threadId = requireString(thread, "id", "thread/start thread");
  const model = requireString(
    startResult,
    "model",
    "thread/start response",
  );
  console.log(`IDENTITY AVAILABLE: thread/start response id=${threadId}`);
  return { id: threadId, model };
}

async function runTurn(
  client: AppServerClient,
  threadId: string,
  prompt: string,
  timeoutMs: number,
  options: TurnRequestOptions = {},
): Promise<TurnResult> {
  const activeTurn = await startTurn(
    client,
    threadId,
    prompt,
    timeoutMs,
    options,
  );
  const completedMessage = await activeTurn.completed;
  const completedTurn = objectAt(completedMessage.params, "turn");
  const completedId = requireString(
    completedTurn,
    "id",
    "turn/completed notification",
  );
  if (completedId !== activeTurn.id) {
    throw new Error(
      `Turn identity mismatch: turn/start returned ${activeTurn.id}, turn/completed returned ${completedId}`,
    );
  }

  const turnMessages = client.messagesSince(activeTurn.firstEventIndex);
  const attributedEventCount = verifyTurnEventAttribution(
    turnMessages,
    threadId,
    activeTurn.id,
    `turn ${activeTurn.id}`,
  );
  const completedAgentText = agentTextFromMessages(
    turnMessages,
    threadId,
    activeTurn.id,
  );
  return {
    id: completedId,
    status:
      stringAt(completedTurn, "status") ??
      throwValue("turn/completed notification did not include status"),
    agentText:
      completedAgentText.length > 0
        ? completedAgentText
        : agentTextFromTurn(completedTurn),
    attributedEventCount,
  };
}

function agentTextFromMessages(
  messages: ProtocolMessage[],
  threadId: string,
  turnId: string,
): string {
  return messages
    .filter(
      (message) =>
        message.method === "item/completed" &&
        message.params?.threadId === threadId &&
        message.params?.turnId === turnId &&
        stringAt(objectAt(message.params, "item"), "type") ===
          "agentMessage",
    )
    .map((message) => stringAt(objectAt(message.params, "item"), "text"))
    .filter((text): text is string => text !== undefined)
    .join("\n");
}

async function startTurn(
  client: AppServerClient,
  threadId: string,
  prompt: string,
  timeoutMs: number,
  options: TurnRequestOptions = {},
): Promise<ActiveTurn> {
  const firstEventIndex = client.messageCount;
  let expectedTurnId: string | undefined;
  const completed = client.waitForNotification(
    "turn/completed",
    (params) =>
      params.threadId === threadId &&
      stringAt(objectAt(params, "turn"), "id") === expectedTurnId,
    timeoutMs,
  );
  try {
    const result = await client.request("turn/start", {
      threadId,
      input: [{ type: "text", text: prompt, text_elements: [] }],
      cwd: undefined,
      approvalPolicy: options.approvalPolicy ?? "never",
      approvalsReviewer: options.approvalsReviewer,
      sandboxPolicy: options.sandboxPolicy ?? {
        type: "readOnly",
        networkAccess: false,
      },
      collaborationMode: options.collaborationMode,
    });
    const initialTurn = objectAt(result, "turn");
    expectedTurnId = requireString(
      initialTurn,
      "id",
      "turn/start response",
    );
    return {
      id: expectedTurnId,
      firstEventIndex,
      completed,
    };
  } catch (error) {
    void completed.catch(() => undefined);
    throw error;
  }
}

function waitForWaitingStatus(
  client: AppServerClient,
  threadId: string,
  flag: "waitingOnApproval" | "waitingOnUserInput",
  timeoutMs: number,
): Promise<ProtocolMessage> {
  return client.waitForNotification(
    "thread/status/changed",
    (params) => {
      const status = objectAt(params, "status");
      return (
        params.threadId === threadId &&
        status?.type === "active" &&
        Array.isArray(status.activeFlags) &&
        status.activeFlags.includes(flag)
      );
    },
    timeoutMs,
  );
}

async function runMarkerTurn(
  client: AppServerClient,
  threadId: string,
  marker: string,
  label: string,
  timeoutMs: number,
): Promise<TurnResult> {
  const result = await runTurn(
    client,
    threadId,
    [
      `This is multiplex turn ${label}.`,
      "Do not use tools, run commands, or modify files.",
      `Remember this thread-specific marker: ${marker}`,
      `Reply with exactly: ${label} STORED: ${marker}`,
    ].join(" "),
    timeoutMs,
  );
  assertSuccessfulTurn(result, label);
  assertMarker(result, marker, label);
  return result;
}

async function runRecallTurn(
  client: AppServerClient,
  threadId: string,
  marker: string,
  label: string,
  timeoutMs: number,
): Promise<TurnResult> {
  const result = await runTurn(
    client,
    threadId,
    [
      `This is continuity turn ${label}.`,
      "Without using tools, running commands, or modifying files,",
      "repeat the exact marker from the earlier turn in this thread.",
      `Prefix the response with ${label}:`,
    ].join(" "),
    timeoutMs,
  );
  assertSuccessfulTurn(result, label);
  assertMarker(result, marker, label);
  return result;
}

async function runTuiRecallTurn(
  client: AppServerClient,
  threadId: string,
  marker: string,
  timeoutMs: number,
): Promise<TurnResult> {
  const label = "app-server post-TUI";
  const result = await runTurn(
    client,
    threadId,
    [
      "Without using tools, running commands, or modifying files,",
      "repeat the exact marker from the earlier native-TUI response",
      "whose prefix was NATIVE TUI ROUND TRIP:.",
      "Do not repeat the app-server seed marker.",
      "Prefix the response with APP-SERVER AFTER TUI:",
    ].join(" "),
    timeoutMs,
  );
  assertSuccessfulTurn(result, label);
  assertMarker(result, marker, label);
  return result;
}

async function runObserverRecallTurn(
  client: AppServerClient,
  threadId: string,
  marker: string,
  timeoutMs: number,
): Promise<TurnResult> {
  const label = "observer post-resume";
  const result = await runTurn(
    client,
    threadId,
    [
      "Without using tools, commands, files, or network access,",
      "repeat the exact marker from the earlier response whose prefix was WRITER A:.",
      "Do not return the observer seed or the WRITER B marker.",
      "Prefix the response with OBSERVER POST-RESUME:",
    ].join(" "),
    timeoutMs,
  );
  assertSuccessfulTurn(result, label);
  assertMarker(result, marker, label);
  return result;
}

async function listAllThreadIds(client: AppServerClient): Promise<string[]> {
  const ids: string[] = [];
  let cursor: string | undefined;
  do {
    const result = await client.request("thread/list", {
      cursor,
      limit: 100,
      sourceKinds: [],
      archived: false,
    });
    ids.push(...threadIdsFromList(result));
    cursor =
      typeof result.nextCursor === "string" ? result.nextCursor : undefined;
  } while (cursor !== undefined);
  return ids;
}

async function planCollaborationMode(
  client: AppServerClient,
  defaultModel: string,
): Promise<JsonObject> {
  const result = await client.request("collaborationMode/list");
  if (!Array.isArray(result.data)) {
    throw new Error(
      "collaborationMode/list response did not include a data array",
    );
  }
  const plan = result.data.find(
    (mode) => isRecord(mode) && mode.mode === "plan",
  );
  if (!isRecord(plan)) {
    throw new Error(
      "collaborationMode/list did not advertise a Plan mode",
    );
  }
  const model =
    typeof plan.model === "string" ? plan.model : defaultModel;
  const reasoningEffort =
    typeof plan.reasoning_effort === "string"
      ? plan.reasoning_effort
      : null;
  console.log(
    `PLAN MODE AVAILABLE: model=${model} reasoning=${reasoningEffort ?? "default"}`,
  );
  return {
    mode: "plan",
    settings: {
      model,
      reasoning_effort: reasoningEffort,
      developer_instructions: null,
    },
  };
}

async function requireRequestError(
  operation: () => Promise<JsonObject>,
  context: string,
): Promise<string> {
  try {
    await operation();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (message.trim().length === 0) {
      throw new Error(`${context} failed without a clear error`);
    }
    return message;
  }
  throw new Error(`${context} unexpectedly succeeded`);
}

async function runNativeTui(
  threadId: string,
  cwd: string,
  marker: string,
): Promise<void> {
  const prompt = [
    "This turn is the native TUI leg of a provider identity round-trip.",
    "Do not use tools, run commands, or modify files.",
    `Reply with exactly: NATIVE TUI ROUND TRIP: ${marker}`,
  ].join(" ");
  const child = spawn(
    "codex",
    [
      "resume",
      "--include-non-interactive",
      "--sandbox",
      "read-only",
      "--ask-for-approval",
      "never",
      "--cd",
      cwd,
      "--no-alt-screen",
      threadId,
      prompt,
    ],
    {
      cwd,
      env: process.env,
      stdio: "inherit",
    },
  );
  const exitCode = await new Promise<number | null>((resolveExit, reject) => {
    child.once("error", reject);
    child.once("exit", resolveExit);
  });
  if (exitCode !== 0) {
    throw new Error(`native Codex TUI exited with code ${String(exitCode)}`);
  }
}

function sessionExpectation(
  threadId: string,
  marker: string,
  cwd: string,
  createLogPath: string,
): SessionRecord {
  return {
    version: 1,
    provider: "codex",
    threadId,
    marker,
    cwd,
    createdAt: new Date().toISOString(),
    createLog: createLogPath,
  };
}

async function writeGateRecord(
  path: string,
  record: GateRunRecord,
): Promise<void> {
  await writeFile(path, `${JSON.stringify(record, null, 2)}\n`, {
    encoding: "utf8",
    flag: "wx",
  });
}

function relativeLogs(resultPath: string, paths: string[]): string[] {
  return paths.map((path) => relative(dirname(resultPath), path));
}

function turnIds(thread: JsonObject): string[] {
  if (!Array.isArray(thread.turns)) {
    throw new Error("Thread did not include a turns array");
  }
  return thread.turns.map((turn, index) => {
    if (!isRecord(turn)) {
      throw new Error(`Thread turn ${index} was not an object`);
    }
    return requireString(turn, "id", `thread turn ${index}`);
  });
}

function threadHasAgentMarker(thread: JsonObject, marker: string): boolean {
  try {
    verifyThreadContainsAgentMarker(thread, marker, "resumed thread");
    return true;
  } catch {
    return false;
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

function canonicalPath(path: string): string {
  const absolute = resolve(path);
  return existsSync(absolute) ? realpathSync(absolute) : absolute;
}

function makeRunId(): string {
  return `${new Date().toISOString().replaceAll(/[:.]/g, "-")}-${randomUUID().slice(0, 8)}`;
}

async function delay(milliseconds: number): Promise<void> {
  await new Promise<void>((resolveDelay) =>
    setTimeout(resolveDelay, milliseconds),
  );
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
    "  pnpm codex dormant [--cwd <path>] [--marker <text>] [--wait-seconds <n>] [--timeout-seconds <n>]",
    "  pnpm codex zero-turn-recovery [--cwd <path>] [--marker <text>] [--timeout-seconds <n>]",
    "  pnpm codex invalid-resume [--cwd <path>]",
    "  pnpm codex multiplex [--cwd <path>] [--marker <thread-a-marker>] [--timeout-seconds <n>]",
    "  pnpm codex tui-round-trip [--cwd <path>] [--marker <text>] [--timeout-seconds <n>]",
    "  pnpm codex input-request [--cwd <path>] [--marker <text>] [--hold-seconds <n>] [--timeout-seconds <n>]",
    "  pnpm codex permission-request [--cwd <path>] [--marker <text>] [--hold-seconds <n>] [--timeout-seconds <n>]",
    "  pnpm codex interrupt [--cwd <path>] [--marker <seed-marker>] [--hold-seconds <n>] [--timeout-seconds <n>]",
    "  pnpm codex observer-writer [--cwd <path>] [--marker <seed-marker>] [--hold-seconds <n>] [--timeout-seconds <n>]",
  ].join("\n");
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((error: unknown) => {
    console.error(`FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  });
}
