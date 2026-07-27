import { randomUUID } from "node:crypto";
import { spawn } from "node:child_process";
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
import {
  requireString,
  requireThread,
  threadIdsFromList,
  threadIdsFromLoadedList,
  verifyNoReplacementThreads,
  verifyThreadContainsAgentMarker,
  verifyThreadHasNoTurns,
  verifyTurnEventAttribution,
} from "./protocol-evidence.ts";

interface ProbeOptions {
  command:
    | "create"
    | "resume"
    | "dormant"
    | "zero-turn-recovery"
    | "invalid-resume"
    | "multiplex"
    | "tui-round-trip";
  cwd: string;
  marker?: string;
  sessionPath?: string;
  timeoutMs: number;
  waitMs: number;
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
    | "native-tui-round-trip";
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
  } else {
    await tuiRoundTripScenario(options);
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
    command !== "tui-round-trip"
  ) {
    throw new Error(usage());
  }

  let cwd = repoRoot;
  let marker: string | undefined;
  let sessionPath: string | undefined;
  let timeoutMs = 300_000;
  let waitMs = 30_000;

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
    const resumeResult = await resumingClient.request("thread/resume", {
      threadId,
      approvalPolicy: "never",
      sandbox: "read-only",
    });
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

    const verification = await runRecallTurn(
      resumingClient,
      threadId,
      marker,
      "app-server post-TUI",
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
  const startResult = await client.request("thread/start", {
    cwd,
    approvalPolicy: "never",
    sandbox: "read-only",
    serviceName: "atc_provider_identity_resume_poc",
    ephemeral: false,
  });
  const thread = verifyStartedThread(cwd, startResult);
  const threadId = requireString(thread, "id", "thread/start thread");
  console.log(`IDENTITY AVAILABLE: thread/start response id=${threadId}`);
  return threadId;
}

async function runTurn(
  client: AppServerClient,
  threadId: string,
  prompt: string,
  timeoutMs: number,
): Promise<TurnResult> {
  const firstEventIndex = client.messageCount;
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
    const attributedEventCount = verifyTurnEventAttribution(
      client.messagesSince(firstEventIndex),
      threadId,
      turnId,
      `turn ${turnId}`,
    );
    return {
      id: completedId,
      status:
        stringAt(completedTurn, "status") ??
        throwValue("turn/completed notification did not include status"),
      agentText:
        stringAt(completedItem, "text") ?? agentTextFromTurn(completedTurn),
      attributedEventCount,
    };
  } catch (error) {
    void agentMessagePromise.catch(() => undefined);
    void completedPromise.catch(() => undefined);
    throw error;
  }
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
  ].join("\n");
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((error: unknown) => {
    console.error(`FAIL: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  });
}
