import { spawn } from "node:child_process";
import { createWriteStream, existsSync, realpathSync } from "node:fs";
import { mkdir, readdir, readFile, stat, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { dirname } from "node:path";

import {
  AppServerClient,
  type JsonObject,
  type ProtocolMessage,
  objectAt,
  stringAt,
} from "./app-server-client.ts";
import {
  assertMarker,
  assertSuccessfulTurn,
  delay,
  makeRunId,
  runTurn,
  startDurableThread,
  turnIds,
  verifyResumedThread,
  writeGateRecord,
  type GateRunRecord,
} from "./probe.ts";
import { requireString } from "./protocol-evidence.ts";
import { WsAppServerClient } from "./ws-client.ts";

/**
 * ATC-83 held-connection experiments.
 *
 * The ATC-68 POC proved that two concurrent writers corrupt Codex turn
 * attribution. These scenarios test the narrower question: is a passively
 * held app-server connection safe (and useful) while a separate `codex`
 * process drives the same thread?
 *
 *   passive-hold  — app-server creates and seeds a thread, then holds its
 *                   connection without sending while `codex exec resume`
 *                   (the same separate-process resume path the native TUI
 *                   uses) drives two turns. Records exactly what the held
 *                   connection observes, then verifies history integrity
 *                   across an app-server restart.
 *   stale-write   — same shape, but after the external turn the held
 *                   connection sends one more turn WITHOUT re-resuming.
 *                   Characterizes serialized (non-concurrent) writes from a
 *                   stale connection: context visibility, success, and what
 *                   the persisted history looks like afterwards.
 */

interface HeldConnectionOptions {
  command:
    | "passive-hold"
    | "stale-write"
    | "poll-observe"
    | "passive-hold-wait"
    | "shared-server";
  cwd: string;
  timeoutMs: number;
  settleMs: number;
  awaitMarker?: string;
  awaitTimeoutMs: number;
}

interface ExternalTurnResult {
  exitCode: number;
  stdout: string;
  stderr: string;
  durationMs: number;
}

interface HeldWindowObservation {
  messageCount: number;
  methods: string[];
  threadScopedCount: number;
}

const probeRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = resolve(probeRoot, "../..");
const runsRoot = resolve(probeRoot, "runs/codex");

export async function main(argv = process.argv.slice(2)): Promise<void> {
  const options = parseArgs(argv);
  if (options.command === "passive-hold") {
    await passiveHoldScenario(options);
  } else if (options.command === "stale-write") {
    await staleWriteScenario(options);
  } else if (options.command === "poll-observe") {
    await pollObserveScenario(options);
  } else if (options.command === "passive-hold-wait") {
    await passiveHoldWaitScenario(options);
  } else {
    await sharedServerScenario(options);
  }
}

function parseArgs(argv: string[]): HeldConnectionOptions {
  const [command, ...rest] = argv;
  if (
    command !== "passive-hold" &&
    command !== "stale-write" &&
    command !== "poll-observe" &&
    command !== "passive-hold-wait" &&
    command !== "shared-server"
  ) {
    throw new Error(usage());
  }

  let cwd = repoRoot;
  let timeoutMs = 300_000;
  let settleMs = 5_000;
  let awaitMarker: string | undefined;
  let awaitTimeoutMs = 240_000;
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
      case "--await-marker":
        awaitMarker = value;
        break;
      case "--await-timeout-seconds":
        awaitTimeoutMs = requirePositive(value, flag) * 1_000;
        break;
      default:
        throw new Error(`Unknown option: ${flag}\n\n${usage()}`);
    }
    index += 1;
  }

  if (command === "passive-hold-wait" && awaitMarker === undefined) {
    throw new Error(`passive-hold-wait requires --await-marker\n\n${usage()}`);
  }

  return {
    command,
    cwd: existsSync(cwd) ? realpathSync(cwd) : cwd,
    timeoutMs,
    settleMs,
    awaitMarker,
    awaitTimeoutMs,
  };
}

async function passiveHoldScenario(
  options: HeldConnectionOptions,
): Promise<void> {
  const runId = makeRunId();
  const seedMarker = `ATC-CODEX-HELD-SEED-${runId.slice(-8).toUpperCase()}`;
  const ext1Marker = `ATC-CODEX-HELD-EXT1-${runId.slice(-8).toUpperCase()}`;
  const ext2Marker = `ATC-CODEX-HELD-EXT2-${runId.slice(-8).toUpperCase()}`;
  const heldLogPath = resolve(runsRoot, `${runId}.held-passive.app-server.jsonl`);
  const resumeLogPath = resolve(runsRoot, `${runId}.held-passive.fresh-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.held-passive.json`);

  console.log("Codex held-connection experiment — passive-hold (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: read-only sandbox, approval policy never");

  const held = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: heldLogPath,
    requestTimeoutMs: options.timeoutMs,
  });

  let threadId: string;
  let heldWindow: HeldWindowObservation;
  let threadReadAfterExternal: JsonObject;
  let unsubscribeResult: JsonObject;
  let rolloutBefore: number;
  let rolloutAfter: number;
  let rolloutPath: string;
  try {
    threadId = await startDurableThread(held, options.cwd);
    const seed = await runTurn(
      held,
      threadId,
      [
        "This is a read-only held-connection experiment.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${seedMarker}`,
        `Reply with exactly: MARKER STORED: ${seedMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(seed, "seed turn");
    assertMarker(seed, seedMarker, "seed turn");

    rolloutPath = await findRolloutPath(threadId);
    rolloutBefore = (await stat(rolloutPath)).size;

    // The held connection now goes passive: it sends nothing while a
    // separate codex process (the TUI-equivalent resume path) drives turns.
    const watermark = held.messageCount;

    const external1 = await runExternalTurn(
      threadId,
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "First repeat the continuity marker from earlier in this conversation,",
        `then remember a second marker: ${ext1Marker}.`,
        `Reply with exactly: EXTERNAL TURN ONE: <earlier marker> ${ext1Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.held-passive.external-1.log`),
    );
    requireExternalSuccess(external1, seedMarker, "external turn one");

    const external2 = await runExternalTurn(
      threadId,
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "Repeat both continuity markers you have been told in this conversation,",
        `then remember a third marker: ${ext2Marker}.`,
        `Reply with exactly: EXTERNAL TURN TWO: <first marker> <second marker> ${ext2Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.held-passive.external-2.log`),
    );
    requireExternalSuccess(external2, ext1Marker, "external turn two");

    await delay(options.settleMs);
    heldWindow = observeHeldWindow(held.messagesSince(watermark), threadId);
    console.log(
      `held connection observed ${heldWindow.messageCount} messages during external turns (${heldWindow.threadScopedCount} thread-scoped)`,
    );

    rolloutAfter = (await stat(rolloutPath)).size;

    // Does the held (still-loaded) thread see the external turns on read?
    threadReadAfterExternal = await recordRequest(held, "thread/read", {
      threadId,
      includeTurns: true,
    });

    unsubscribeResult = await recordRequest(held, "thread/unsubscribe", {
      threadId,
    });
  } finally {
    await held.stop();
  }

  // App-server restart: a fresh process must resume the exact thread with
  // seed and both external turns intact, and continue the conversation.
  const fresh = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
    requestTimeoutMs: options.timeoutMs,
  });
  let resumeEvidence: JsonObject;
  try {
    const resumeResult = await fresh.request("thread/resume", {
      threadId,
      cwd: options.cwd,
    });
    const thread = verifyResumedThread(
      { version: 1, provider: "codex", threadId, marker: seedMarker, cwd: options.cwd, createdAt: "", createLog: "" },
      resumeResult,
    );
    const resumedTurnIds = turnIds(thread);
    const threadText = JSON.stringify(thread);
    const recall = await runTurn(
      fresh,
      threadId,
      [
        "Do not use tools, run commands, or modify files.",
        "Repeat every continuity marker you have been told in this conversation,",
        "oldest first, separated by spaces. Reply with exactly:",
        "ALL MARKERS: <markers>",
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(recall, "post-restart recall turn");
    assertMarker(recall, seedMarker, "post-restart recall turn");
    assertMarker(recall, ext1Marker, "post-restart recall turn");
    assertMarker(recall, ext2Marker, "post-restart recall turn");

    resumeEvidence = {
      turnCount: resumedTurnIds.length,
      turnIds: resumedTurnIds,
      historyContainsSeedMarker: threadText.includes(seedMarker),
      historyContainsExt1Marker: threadText.includes(ext1Marker),
      historyContainsExt2Marker: threadText.includes(ext2Marker),
      recallText: recall.agentText,
    };
  } finally {
    await fresh.stop();
  }

  const record: GateRunRecord = {
    version: 1,
    provider: "codex",
    scenario: "held-connection-passive",
    cwd: options.cwd,
    completedAt: new Date().toISOString(),
    evidence: {
      threadId,
      seedMarker,
      ext1Marker,
      ext2Marker,
      heldConnectionDuringExternalTurns: { ...heldWindow },
      rolloutFile: {
        path: rolloutPath,
        sizeAfterSeed: rolloutBefore,
        sizeAfterExternalTurns: rolloutAfter,
      },
      threadReadAfterExternal: summarizeThreadRead(
        threadReadAfterExternal,
        [seedMarker, ext1Marker, ext2Marker],
      ),
      unsubscribeResult,
      freshResumeAfterRestart: resumeEvidence,
    },
    rawLogs: [heldLogPath, resumeLogPath].map((path) =>
      relative(dirname(resultPath), path),
    ),
  };
  await writeGateRecord(resultPath, record);
  console.log(`PASS: passive-hold evidence written to ${relative(process.cwd(), resultPath)}`);
}

async function staleWriteScenario(
  options: HeldConnectionOptions,
): Promise<void> {
  const runId = makeRunId();
  const seedMarker = `ATC-CODEX-STALE-SEED-${runId.slice(-8).toUpperCase()}`;
  const ext1Marker = `ATC-CODEX-STALE-EXT1-${runId.slice(-8).toUpperCase()}`;
  const staleMarker = `ATC-CODEX-STALE-HELD-${runId.slice(-8).toUpperCase()}`;
  const heldLogPath = resolve(runsRoot, `${runId}.stale-write.app-server.jsonl`);
  const resumeLogPath = resolve(runsRoot, `${runId}.stale-write.fresh-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.stale-write.json`);

  console.log("Codex held-connection experiment — stale-write (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: read-only sandbox, approval policy never");

  const held = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: heldLogPath,
    requestTimeoutMs: options.timeoutMs,
  });

  let threadId: string;
  let heldWindow: HeldWindowObservation;
  let staleTurnEvidence: JsonObject;
  try {
    threadId = await startDurableThread(held, options.cwd);
    const seed = await runTurn(
      held,
      threadId,
      [
        "This is a read-only held-connection experiment.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${seedMarker}`,
        `Reply with exactly: MARKER STORED: ${seedMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(seed, "seed turn");
    assertMarker(seed, seedMarker, "seed turn");

    const watermark = held.messageCount;
    const external1 = await runExternalTurn(
      threadId,
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "First repeat the continuity marker from earlier in this conversation,",
        `then remember a second marker: ${ext1Marker}.`,
        `Reply with exactly: EXTERNAL TURN ONE: <earlier marker> ${ext1Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.stale-write.external-1.log`),
    );
    requireExternalSuccess(external1, seedMarker, "external turn one");

    await delay(options.settleMs);
    heldWindow = observeHeldWindow(held.messagesSince(watermark), threadId);

    // Serialized stale write: the held connection sends one turn WITHOUT
    // re-resuming after the external process advanced the thread.
    const stale = await runTurn(
      held,
      threadId,
      [
        "Do not use tools, run commands, or modify files.",
        "Repeat every continuity marker you have been told in this conversation,",
        `then remember one more marker: ${staleMarker}.`,
        `Reply with exactly: STALE WRITE: <all earlier markers> ${staleMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    staleTurnEvidence = {
      turnId: stale.id,
      status: stale.status,
      agentText: stale.agentText,
      sawSeedMarker: stale.agentText.includes(seedMarker),
      sawExternalMarker: stale.agentText.includes(ext1Marker),
    };
    console.log(
      `stale write completed: status=${stale.status} sawExternalMarker=${String(staleTurnEvidence.sawExternalMarker)}`,
    );
  } finally {
    await held.stop();
  }

  const fresh = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
    requestTimeoutMs: options.timeoutMs,
  });
  let resumeEvidence: JsonObject;
  try {
    const resumeResult = await fresh.request("thread/resume", {
      threadId,
      cwd: options.cwd,
    });
    const thread = verifyResumedThread(
      { version: 1, provider: "codex", threadId, marker: seedMarker, cwd: options.cwd, createdAt: "", createLog: "" },
      resumeResult,
    );
    const resumedTurnIds = turnIds(thread);
    const threadText = JSON.stringify(thread);
    resumeEvidence = {
      turnCount: resumedTurnIds.length,
      turnIds: resumedTurnIds,
      syntheticTurnIds: resumedTurnIds.filter((id) => !isUuid(id)),
      historyContainsSeedMarker: threadText.includes(seedMarker),
      historyContainsExternalMarker: threadText.includes(ext1Marker),
      historyContainsStaleMarker: threadText.includes(staleMarker),
    };
    console.log(
      `fresh resume: turns=${resumedTurnIds.length} synthetic=${JSON.stringify(resumeEvidence.syntheticTurnIds)}`,
    );
  } finally {
    await fresh.stop();
  }

  const record: GateRunRecord = {
    version: 1,
    provider: "codex",
    scenario: "held-connection-stale-write",
    cwd: options.cwd,
    completedAt: new Date().toISOString(),
    evidence: {
      threadId,
      seedMarker,
      ext1Marker,
      staleMarker,
      heldConnectionDuringExternalTurn: { ...heldWindow },
      staleTurn: staleTurnEvidence,
      freshResume: resumeEvidence,
    },
    rawLogs: [heldLogPath, resumeLogPath].map((path) =>
      relative(dirname(resultPath), path),
    ),
  };
  await writeGateRecord(resultPath, record);
  console.log(`stale-write evidence written to ${relative(process.cwd(), resultPath)}`);
}

/**
 * Real-TUI variant of passive-hold. Seeds a thread through the held
 * connection, prints the thread ID, then waits for an EXTERNAL process —
 * a human or an expect-driven `codex resume` TUI started by the caller —
 * to write --await-marker into the rollout. While waiting, it records
 * everything the held connection observes, then verifies integrity with a
 * fresh resume, exactly like passive-hold.
 */
async function passiveHoldWaitScenario(
  options: HeldConnectionOptions,
): Promise<void> {
  const runId = makeRunId();
  const seedMarker = `ATC-CODEX-TUIHOLD-SEED-${runId.slice(-8).toUpperCase()}`;
  const externalMarker = options.awaitMarker as string;
  const heldLogPath = resolve(runsRoot, `${runId}.tui-hold.app-server.jsonl`);
  const resumeLogPath = resolve(runsRoot, `${runId}.tui-hold.fresh-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.tui-hold.json`);

  console.log("Codex held-connection experiment — passive-hold-wait (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: read-only sandbox, approval policy never");

  const held = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: heldLogPath,
    requestTimeoutMs: options.timeoutMs,
  });

  let threadId: string;
  let heldWindow: HeldWindowObservation;
  let threadReadAfterExternal: JsonObject;
  let waitedMs = 0;
  try {
    threadId = await startDurableThread(held, options.cwd);
    const seed = await runTurn(
      held,
      threadId,
      [
        "This is a read-only held-connection experiment.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${seedMarker}`,
        `Reply with exactly: MARKER STORED: ${seedMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(seed, "seed turn");
    assertMarker(seed, seedMarker, "seed turn");

    const rolloutPath = await findRolloutPath(threadId);
    const watermark = held.messageCount;
    console.log(`THREAD_ID: ${threadId}`);
    console.log(`SEED_MARKER: ${seedMarker}`);
    console.log(
      `waiting up to ${options.awaitTimeoutMs}ms for ${externalMarker} to appear in the rollout...`,
    );

    const waitStartedAt = Date.now();
    let found = false;
    while (Date.now() - waitStartedAt < options.awaitTimeoutMs) {
      const text = await readFileOrEmpty(rolloutPath);
      if (text.includes(externalMarker)) {
        found = true;
        break;
      }
      await delay(2_000);
    }
    waitedMs = Date.now() - waitStartedAt;
    if (!found) {
      throw new Error(
        `external marker ${externalMarker} never appeared in ${rolloutPath}`,
      );
    }
    console.log(`external marker observed in rollout after ${waitedMs}ms`);

    await delay(options.settleMs);
    heldWindow = observeHeldWindow(held.messagesSince(watermark), threadId);
    console.log(
      `held connection observed ${heldWindow.messageCount} messages during the external TUI turn (methods: ${heldWindow.methods.join(", ") || "none"})`,
    );

    threadReadAfterExternal = await recordRequest(held, "thread/read", {
      threadId,
      includeTurns: true,
    });
  } finally {
    await held.stop();
  }

  const fresh = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: resumeLogPath,
    requestTimeoutMs: options.timeoutMs,
  });
  let resumeEvidence: JsonObject;
  try {
    const resumeResult = await fresh.request("thread/resume", {
      threadId,
      cwd: options.cwd,
    });
    const thread = verifyResumedThread(
      { version: 1, provider: "codex", threadId, marker: seedMarker, cwd: options.cwd, createdAt: "", createLog: "" },
      resumeResult,
    );
    const resumedTurnIds = turnIds(thread);
    const threadText = JSON.stringify(thread);
    const recall = await runTurn(
      fresh,
      threadId,
      [
        "Do not use tools, run commands, or modify files.",
        "Repeat every continuity marker you have been told in this conversation,",
        "oldest first, separated by spaces. Reply with exactly:",
        "ALL MARKERS: <markers>",
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(recall, "post-restart recall turn");
    assertMarker(recall, seedMarker, "post-restart recall turn");
    assertMarker(recall, externalMarker, "post-restart recall turn");

    resumeEvidence = {
      turnCount: resumedTurnIds.length,
      turnIds: resumedTurnIds,
      syntheticTurnIds: resumedTurnIds.filter((id) => !isUuid(id)),
      historyContainsSeedMarker: threadText.includes(seedMarker),
      historyContainsExternalMarker: threadText.includes(externalMarker),
      recallText: recall.agentText,
    };
  } finally {
    await fresh.stop();
  }

  const record: GateRunRecord = {
    version: 1,
    provider: "codex",
    scenario: "held-connection-passive-tui",
    cwd: options.cwd,
    completedAt: new Date().toISOString(),
    evidence: {
      threadId,
      seedMarker,
      externalMarker,
      externalWriter: "native codex resume TUI (caller-driven)",
      waitedMs,
      heldConnectionDuringExternalTurn: { ...heldWindow },
      threadReadAfterExternal: summarizeThreadRead(threadReadAfterExternal, [
        seedMarker,
        externalMarker,
      ]),
      freshResumeAfterRestart: resumeEvidence,
    },
    rawLogs: [heldLogPath, resumeLogPath].map((path) =>
      relative(dirname(resultPath), path),
    ),
  };
  await writeGateRecord(resultPath, record);
  console.log(
    `PASS: passive-hold-wait evidence written to ${relative(process.cwd(), resultPath)}`,
  );
}

/**
 * Two clients of ONE `codex app-server --listen ws://...` process — the
 * topology where ATC runs the app-server and the TUI attaches to it with
 * `codex resume --remote`. Client A (the "adapter") creates and seeds a
 * thread, then holds passively. Client B resumes the same thread on the
 * same server — the documented "rejoin a running thread" path — and drives
 * a turn. Measures whether the server fans B's turn events out to A.
 * With --await-marker, a second leg waits for a caller-driven
 * `codex resume --remote` TUI turn and measures A's fan-out again.
 */
async function sharedServerScenario(
  options: HeldConnectionOptions,
): Promise<void> {
  const runId = makeRunId();
  const seedMarker = `ATC-CODEX-SHARED-SEED-${runId.slice(-8).toUpperCase()}`;
  const extMarker = `ATC-CODEX-SHARED-EXT-${runId.slice(-8).toUpperCase()}`;
  const observerLogPath = resolve(runsRoot, `${runId}.shared-server.observer.jsonl`);
  const writerLogPath = resolve(runsRoot, `${runId}.shared-server.writer.jsonl`);
  const serverLogPath = resolve(runsRoot, `${runId}.shared-server.server.log`);
  const resultPath = resolve(runsRoot, `${runId}.shared-server.json`);
  const port = 17_000 + Math.floor(Math.random() * 1_000);
  const url = `ws://127.0.0.1:${port}`;

  console.log("Codex held-connection experiment — shared-server (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: read-only sandbox, approval policy never");
  console.log(`starting dedicated codex app-server --listen ${url}`);

  await mkdir(dirname(serverLogPath), { recursive: true });
  const serverLog = createWriteStream(serverLogPath, { flags: "wx" });
  const server = spawn("codex", ["app-server", "--listen", url], {
    cwd: options.cwd,
    env: process.env,
    stdio: ["ignore", "pipe", "pipe"],
  });
  server.stdout.pipe(serverLog);
  server.stderr.pipe(serverLog);
  try {
    await waitForReady(`http://127.0.0.1:${port}/readyz`, 20_000);
    console.log("app-server ready");

    const observer = await WsAppServerClient.connect({
      url,
      clientName: "atc-observer",
      rawLogPath: observerLogPath,
      requestTimeoutMs: options.timeoutMs,
    });

    let threadId: string;
    let heldWindow: HeldWindowObservation;
    let observerSawWriterTurn: JsonObject;
    let writerTurnEvidence: JsonObject;
    let tuiLeg: JsonObject | undefined;
    try {
      threadId = await startDurableThread(observer, options.cwd);
      const seed = await runTurn(
        observer,
        threadId,
        [
          "This is a read-only held-connection experiment.",
          "Do not use tools, run commands, or modify files.",
          `Remember this exact continuity marker for later turns: ${seedMarker}`,
          `Reply with exactly: MARKER STORED: ${seedMarker}`,
        ].join(" "),
        options.timeoutMs,
      );
      assertSuccessfulTurn(seed, "seed turn");
      assertMarker(seed, seedMarker, "seed turn");

      const watermark = observer.messageCount;
      const writer = await WsAppServerClient.connect({
        url,
        clientName: "atc-writer",
        rawLogPath: writerLogPath,
        requestTimeoutMs: options.timeoutMs,
      });
      try {
        const resumeResult = await writer.request("thread/resume", {
          threadId,
          cwd: options.cwd,
        });
        const resumedThread = verifyResumedThread(
          { version: 1, provider: "codex", threadId, marker: seedMarker, cwd: options.cwd, createdAt: "", createLog: "" },
          resumeResult,
        );
        console.log(
          `writer rejoined running thread with ${turnIds(resumedThread).length} existing turns`,
        );

        const external = await runTurn(
          writer,
          threadId,
          [
            "Do not use tools, run commands, or modify files.",
            "First repeat the continuity marker from earlier in this conversation,",
            `then remember a second marker: ${extMarker}.`,
            `Reply with exactly: EXTERNAL TURN ONE: <earlier marker> ${extMarker}`,
          ].join(" "),
          options.timeoutMs,
        );
        assertSuccessfulTurn(external, "writer-client turn");
        assertMarker(external, seedMarker, "writer-client turn");
        assertMarker(external, extMarker, "writer-client turn");
        writerTurnEvidence = {
          turnId: external.id,
          status: external.status,
          agentText: external.agentText,
        };
      } finally {
        await writer.stop();
      }

      await delay(options.settleMs);
      const heldMessages = observer.messagesSince(watermark);
      heldWindow = observeHeldWindow(heldMessages, threadId);
      observerSawWriterTurn = summarizeFanOut(heldMessages, threadId, extMarker);
      console.log(
        `observer during writer turn: ${heldWindow.messageCount} messages, ${JSON.stringify(observerSawWriterTurn)}`,
      );

      if (options.awaitMarker !== undefined) {
        const tuiMarker = options.awaitMarker;
        const tuiWatermark = observer.messageCount;
        const rolloutPath = await findRolloutPath(threadId);
        console.log(`THREAD_ID: ${threadId}`);
        console.log(`PORT: ${port}`);
        console.log(
          `waiting up to ${options.awaitTimeoutMs}ms for ${tuiMarker} in the rollout...`,
        );
        const waitStartedAt = Date.now();
        let found = false;
        while (Date.now() - waitStartedAt < options.awaitTimeoutMs) {
          const text = await readFileOrEmpty(rolloutPath);
          if (text.includes(tuiMarker)) {
            found = true;
            break;
          }
          await delay(2_000);
        }
        if (!found) {
          throw new Error(`TUI marker ${tuiMarker} never appeared in rollout`);
        }
        await delay(options.settleMs);
        const tuiMessages = observer.messagesSince(tuiWatermark);
        tuiLeg = {
          marker: tuiMarker,
          waitedMs: Date.now() - waitStartedAt,
          heldWindow: { ...observeHeldWindow(tuiMessages, threadId) },
          fanOut: summarizeFanOut(tuiMessages, threadId, tuiMarker),
        };
        console.log(`observer during TUI turn: ${JSON.stringify(tuiLeg.fanOut)}`);
      }
    } finally {
      await observer.stop();
    }

    const record: GateRunRecord = {
      version: 1,
      provider: "codex",
      scenario: "held-connection-shared-server",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        seedMarker,
        extMarker,
        transport: `dedicated codex app-server --listen ${url}`,
        heldConnectionDuringWriterTurn: { ...heldWindow },
        observerSawWriterTurn,
        writerTurn: writerTurnEvidence,
        tuiLeg: tuiLeg ?? "not exercised",
      },
      rawLogs: [observerLogPath, writerLogPath].map((path) =>
        relative(dirname(resultPath), path),
      ),
    };
    await writeGateRecord(resultPath, record);
    console.log(
      `PASS: shared-server evidence written to ${relative(process.cwd(), resultPath)}`,
    );
  } finally {
    server.kill("SIGTERM");
    serverLog.end();
  }
}

export function summarizeFanOut(
  messages: ProtocolMessage[],
  threadId: string,
  marker: string,
): JsonObject {
  return {
    turnStarted: messages.some(
      (message) =>
        message.method === "turn/started" &&
        message.params?.threadId === threadId,
    ),
    turnCompleted: messages.some(
      (message) =>
        message.method === "turn/completed" &&
        message.params?.threadId === threadId,
    ),
    itemCompleted: messages.some(
      (message) =>
        message.method === "item/completed" &&
        message.params?.threadId === threadId,
    ),
    statusChanges: messages.filter(
      (message) =>
        message.method === "thread/status/changed" &&
        message.params?.threadId === threadId,
    ).length,
    sawMarkerInEvents: JSON.stringify(messages).includes(marker),
  };
}

async function waitForReady(url: string, timeoutMs: number): Promise<void> {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) {
        return;
      }
    } catch {
      // Server not accepting connections yet.
    }
    await delay(500);
  }
  throw new Error(`app-server did not become ready at ${url}`);
}

async function readFileOrEmpty(path: string): Promise<string> {
  try {
    return await readFile(path, "utf8");
  } catch {
    return "";
  }
}

/**
 * Can the held connection poll thread/read to see an in-progress external
 * turn (near-live activity), or do external turns only become visible after
 * completion? Polls every pollIntervalMs while `codex exec resume` drives
 * one turn and records how the read-back evolves.
 */
async function pollObserveScenario(
  options: HeldConnectionOptions,
): Promise<void> {
  const runId = makeRunId();
  const seedMarker = `ATC-CODEX-POLL-SEED-${runId.slice(-8).toUpperCase()}`;
  const ext1Marker = `ATC-CODEX-POLL-EXT1-${runId.slice(-8).toUpperCase()}`;
  const heldLogPath = resolve(runsRoot, `${runId}.poll-observe.app-server.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.poll-observe.json`);
  const pollIntervalMs = 2_000;

  console.log("Codex held-connection experiment — poll-observe (ATC-83)");
  console.log(`cwd: ${options.cwd}`);
  console.log("safety: read-only sandbox, approval policy never");

  const held = await AppServerClient.start({
    cwd: options.cwd,
    rawLogPath: heldLogPath,
    requestTimeoutMs: options.timeoutMs,
  });

  try {
    const threadId = await startDurableThread(held, options.cwd);
    const seed = await runTurn(
      held,
      threadId,
      [
        "This is a read-only held-connection experiment.",
        "Do not use tools, run commands, or modify files.",
        `Remember this exact continuity marker for later turns: ${seedMarker}`,
        `Reply with exactly: MARKER STORED: ${seedMarker}`,
      ].join(" "),
      options.timeoutMs,
    );
    assertSuccessfulTurn(seed, "seed turn");
    assertMarker(seed, seedMarker, "seed turn");

    const polls: JsonObject[] = [];
    const startedAt = Date.now();
    let externalDone = false;
    const externalPromise = runExternalTurn(
      threadId,
      options.cwd,
      [
        "Do not use tools, run commands, or modify files.",
        "First repeat the continuity marker from earlier in this conversation,",
        `then remember a second marker: ${ext1Marker}.`,
        `Reply with exactly: EXTERNAL TURN ONE: <earlier marker> ${ext1Marker}`,
      ].join(" "),
      resolve(runsRoot, `${runId}.poll-observe.external-1.log`),
    ).finally(() => {
      externalDone = true;
    });

    while (!externalDone) {
      const read = await recordRequest(held, "thread/read", {
        threadId,
        includeTurns: true,
      });
      const summary = summarizeThreadRead(read, [seedMarker, ext1Marker]);
      const status = threadStatusOf(read);
      polls.push({
        atMs: Date.now() - startedAt,
        externalDone,
        status,
        ...summary,
      });
      console.log(
        `poll @${Date.now() - startedAt}ms: turns=${String(summary.turnCount)} status=${status ?? "unknown"} ext1Visible=${String(
          (summary.markerVisibility as Record<string, boolean>)[ext1Marker],
        )}`,
      );
      await delay(pollIntervalMs);
    }
    const external = await externalPromise;
    requireExternalSuccess(external, seedMarker, "external turn one");

    await delay(options.settleMs);
    const finalRead = await recordRequest(held, "thread/read", {
      threadId,
      includeTurns: true,
    });
    const finalSummary = summarizeThreadRead(finalRead, [seedMarker, ext1Marker]);
    polls.push({
      atMs: Date.now() - startedAt,
      externalDone: true,
      status: threadStatusOf(finalRead),
      ...finalSummary,
    });
    console.log(
      `final poll: turns=${String(finalSummary.turnCount)} ext1Visible=${String(
        (finalSummary.markerVisibility as Record<string, boolean>)[ext1Marker],
      )}`,
    );

    const record: GateRunRecord = {
      version: 1,
      provider: "codex",
      scenario: "held-connection-poll-observe",
      cwd: options.cwd,
      completedAt: new Date().toISOString(),
      evidence: {
        threadId,
        seedMarker,
        ext1Marker,
        pollIntervalMs,
        externalTurnDurationMs: external.durationMs,
        polls,
      },
      rawLogs: [relative(dirname(resultPath), heldLogPath)],
    };
    await writeGateRecord(resultPath, record);
    console.log(
      `poll-observe evidence written to ${relative(process.cwd(), resultPath)}`,
    );
  } finally {
    await held.stop();
  }
}

export function threadStatusOf(read: JsonObject): string | undefined {
  const inner = objectAt(read, "result") ?? read;
  const thread = objectAt(inner, "thread") ?? inner;
  const status = thread.status;
  if (typeof status === "string") {
    return status;
  }
  return stringAt(objectAt(thread, "status"), "type");
}

async function recordRequest(
  client: AppServerClient,
  method: string,
  params: JsonObject,
): Promise<JsonObject> {
  try {
    const result = await client.request(method, params);
    console.log(`${method} accepted on held connection`);
    return { ok: true, result };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.log(`${method} rejected on held connection: ${message}`);
    return { ok: false, error: message };
  }
}

export function observeHeldWindow(
  messages: ProtocolMessage[],
  threadId: string,
): HeldWindowObservation {
  return {
    messageCount: messages.length,
    methods: [...new Set(messages.map((message) => message.method ?? "response"))],
    threadScopedCount: messages.filter(
      (message) => message.params?.threadId === threadId,
    ).length,
  };
}

export function summarizeThreadRead(
  result: JsonObject,
  markers: string[],
): JsonObject {
  const inner = objectAt(result, "result") ?? result;
  const thread = objectAt(inner, "thread") ?? inner;
  const turns = Array.isArray(thread.turns) ? thread.turns : [];
  const text = JSON.stringify(result);
  return {
    turnCount: turns.length,
    markerVisibility: Object.fromEntries(
      markers.map((marker) => [marker, text.includes(marker)]),
    ),
  };
}

async function runExternalTurn(
  threadId: string,
  cwd: string,
  prompt: string,
  logPath: string,
): Promise<ExternalTurnResult> {
  console.log(`→ external writer: codex exec resume ${threadId}`);
  const startedAt = Date.now();
  const child = spawn(
    "codex",
    [
      "exec",
      "--sandbox",
      "read-only",
      "--skip-git-repo-check",
      "--cd",
      cwd,
      "resume",
      threadId,
      prompt,
    ],
    { cwd, env: process.env, stdio: ["ignore", "pipe", "pipe"] },
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
    `# codex exec resume ${threadId} (exit ${exitCode}, ${durationMs}ms)\n\n## stdout\n${stdout}\n\n## stderr\n${stderr}\n`,
    { encoding: "utf8", flag: "wx" },
  );
  console.log(`← external writer exited ${exitCode} after ${durationMs}ms`);
  return { exitCode, stdout, stderr, durationMs };
}

function requireExternalSuccess(
  result: ExternalTurnResult,
  expectedMarker: string,
  label: string,
): void {
  if (result.exitCode !== 0) {
    throw new Error(
      `${label} failed with exit code ${result.exitCode}: ${result.stderr.slice(0, 500)}`,
    );
  }
  if (!result.stdout.includes(expectedMarker)) {
    throw new Error(
      `${label} output did not include expected marker ${expectedMarker}`,
    );
  }
  console.log(`CONTINUITY VERIFIED: ${label} recalled ${expectedMarker}`);
}

async function findRolloutPath(threadId: string): Promise<string> {
  const sessionsRoot = join(homedir(), ".codex", "sessions");
  const matches: string[] = [];
  await walk(sessionsRoot, matches, threadId);
  if (matches.length !== 1) {
    throw new Error(
      `Expected exactly one rollout file for ${threadId}, found ${matches.length}`,
    );
  }
  return matches[0];
}

async function walk(
  directory: string,
  matches: string[],
  threadId: string,
): Promise<void> {
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      await walk(path, matches, threadId);
    } else if (entry.name.includes(threadId)) {
      matches.push(path);
    }
  }
}

export function isUuid(value: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
    value,
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
    "  pnpm codex:held passive-hold [--cwd <path>] [--timeout-seconds <n>] [--settle-seconds <n>]",
    "  pnpm codex:held stale-write [--cwd <path>] [--timeout-seconds <n>] [--settle-seconds <n>]",
    "  pnpm codex:held poll-observe [--cwd <path>] [--timeout-seconds <n>] [--settle-seconds <n>]",
  ].join("\n");
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((error: unknown) => {
    console.error(
      `FAIL: ${error instanceof Error ? error.message : String(error)}`,
    );
    process.exitCode = 1;
  });
}
