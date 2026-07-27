import { randomUUID } from "node:crypto";
import { existsSync, realpathSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, relative, resolve } from "node:path";
import * as readline from "node:readline/promises";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

import type { PermissionResult } from "@anthropic-ai/claude-agent-sdk";

import {
  ClaudeControlTimeline,
  type ClaudeControlTimelineEvent,
} from "./control-timeline.ts";
import {
  listClaudeSessionIds,
  requireClaudeResumeFailure,
  runClaudeLifecycle,
  runClaudeTurn,
  type ClaudeIdentityAvailability,
  type ClaudeLifecycleObservation,
  type ClaudeLifecycleTransition,
  type ClaudeResumeFailureObservation,
  type ClaudeTurnObservation,
} from "./sdk-query.ts";

interface ProbeOptions {
  command:
    | "create"
    | "resume"
    | "invalid-resume"
    | "lifecycle"
    | "input-request"
    | "permission-request";
  cwd: string;
  marker?: string;
  sessionPath?: string;
  timeoutMs: number;
  lifecycleObservationMs: number;
  answer?: string;
  responseDelayMs: number;
  decision?: "allow" | "deny";
}

interface ClaudeSessionRecord {
  version: 1;
  provider: "claude";
  sessionId: string;
  marker: string;
  cwd: string;
  createdAt: string;
  claudeCodeVersion: string;
  identityFirstSeen: ClaudeIdentityAvailability;
  createLogs: string[];
}

const probeRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = resolve(probeRoot, "../..");
const runsRoot = resolve(probeRoot, "runs/claude");

export async function main(argv = process.argv.slice(2)): Promise<void> {
  const options = parseArgs(argv);
  if (options.command === "create") {
    await createScenario(options);
  } else if (options.command === "resume") {
    await resumeScenario(options);
  } else if (options.command === "invalid-resume") {
    await invalidResumeScenario(options);
  } else if (options.command === "lifecycle") {
    await lifecycleScenario(options);
  } else if (options.command === "input-request") {
    await inputRequestScenario(options);
  } else {
    await permissionRequestScenario(options);
  }
}

export function parseArgs(argv: string[]): ProbeOptions {
  const [command, ...rest] = argv;
  if (
    command !== "create" &&
    command !== "resume" &&
    command !== "invalid-resume" &&
    command !== "lifecycle" &&
    command !== "input-request" &&
    command !== "permission-request"
  ) {
    throw new Error(usage());
  }

  let cwd = repoRoot;
  let marker: string | undefined;
  let sessionPath: string | undefined;
  let timeoutMs = 300_000;
  let lifecycleObservationMs = 2_000;
  let answer: string | undefined;
  let responseDelayMs = 2_000;
  let decision: "allow" | "deny" | undefined;

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
      case "--answer":
        answer = value;
        break;
      case "--decision":
        if (value !== "allow" && value !== "deny") {
          throw new Error("--decision must be allow or deny");
        }
        decision = value;
        break;
      case "--timeout-seconds": {
        const seconds = Number(value);
        if (!Number.isFinite(seconds) || seconds <= 0) {
          throw new Error("--timeout-seconds must be a positive number");
        }
        timeoutMs = seconds * 1_000;
        break;
      }
      case "--observation-seconds": {
        const seconds = Number(value);
        if (!Number.isFinite(seconds) || seconds < 0) {
          throw new Error(
            "--observation-seconds must be a non-negative number",
          );
        }
        lifecycleObservationMs = seconds * 1_000;
        break;
      }
      case "--response-delay-seconds": {
        const seconds = Number(value);
        if (!Number.isFinite(seconds) || seconds < 0) {
          throw new Error(
            "--response-delay-seconds must be a non-negative number",
          );
        }
        responseDelayMs = seconds * 1_000;
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
    lifecycleObservationMs,
    answer,
    responseDelayMs,
    decision,
  };
}

export function assertSessionRecord(value: unknown): ClaudeSessionRecord {
  if (
    !isRecord(value) ||
    value.version !== 1 ||
    value.provider !== "claude" ||
    typeof value.sessionId !== "string" ||
    typeof value.marker !== "string" ||
    typeof value.cwd !== "string" ||
    typeof value.createdAt !== "string" ||
    typeof value.claudeCodeVersion !== "string" ||
    !isIdentityAvailability(value.identityFirstSeen) ||
    !Array.isArray(value.createLogs) ||
    !value.createLogs.every((entry) => typeof entry === "string")
  ) {
    throw new Error("Session artifact has an unsupported or invalid shape");
  }
  return value as unknown as ClaudeSessionRecord;
}

export function verifyContinuity(
  observation: ClaudeTurnObservation,
  expectedSessionId: string,
  marker: string,
): void {
  if (observation.sessionId !== expectedSessionId) {
    throw new Error(
      `Identity mismatch: expected ${expectedSessionId}, received ${observation.sessionId}`,
    );
  }
  if (!observation.resultText.includes(marker)) {
    throw new Error(
      `Continuity check failed: Claude response did not contain ${marker}`,
    );
  }
}

export function requireResumeId(sessionId: string | undefined): string {
  if (sessionId === undefined || sessionId.trim() === "") {
    throw new Error(
      "Missing Claude resume ID; refusing to start a query that would create a new session",
    );
  }
  return sessionId;
}

export function verifyNoReplacementSessions(
  beforeSessionIds: string[],
  afterSessionIds: string[],
  invalidSessionId: string,
  attempt: ClaudeResumeFailureObservation,
): void {
  const before = new Set(beforeSessionIds);
  const added = afterSessionIds.filter((sessionId) => !before.has(sessionId));
  const unexpectedObserved = attempt.observedSessionIds.filter(
    (sessionId) => sessionId !== invalidSessionId,
  );

  if (unexpectedObserved.length > 0) {
    throw new Error(
      `Invalid resume emitted replacement session IDs: ${unexpectedObserved.join(", ")}`,
    );
  }
  if (added.length > 0) {
    throw new Error(
      `Invalid resume created replacement sessions: ${added.join(", ")}`,
    );
  }
}

export function verifyInvalidResumeFailure(
  invalidSessionId: string,
  attempt: ClaudeResumeFailureObservation,
): void {
  if (
    !attempt.error.includes(invalidSessionId) ||
    !/(conversation|session)/i.test(attempt.error)
  ) {
    throw new Error(
      `Invalid resume did not return a clear session-specific failure: ${attempt.error}`,
    );
  }
  if (attempt.resultSubtype === "success") {
    throw new Error("Invalid resume unexpectedly returned a successful result");
  }
}

export function verifyLifecycleTransitions(
  transitions: ClaudeLifecycleTransition[],
): void {
  const runningIndex = transitions.findIndex(
    (transition) => transition.state === "running",
  );
  const idleIndex = transitions.findIndex(
    (transition, index) =>
      index > runningIndex && transition.state === "idle",
  );

  if (runningIndex !== -1 && idleIndex === -1) {
    throw new Error(
      "Claude lifecycle probe emitted no idle event after running",
    );
  }
}

export function summarizeLifecycleSignals(
  observation: Pick<ClaudeLifecycleObservation, "stateTransitions">,
): {
  working: LifecycleSignalSummary;
  idle: LifecycleSignalSummary;
  needsInput: LifecycleSignalSummary;
} {
  return {
    working: summarizeLifecycleSignal(
      "running",
      observation.stateTransitions,
    ),
    idle: summarizeLifecycleSignal("idle", observation.stateTransitions),
    needsInput: summarizeLifecycleSignal(
      "requires_action",
      observation.stateTransitions,
    ),
  };
}

async function createScenario(options: ProbeOptions): Promise<void> {
  const marker =
    options.marker ?? `ATC-CLAUDE-${randomUUID().slice(0, 8).toUpperCase()}`;
  const runId = makeRunId();
  const firstLogPath = resolve(runsRoot, `${runId}.create-turn-1.jsonl`);
  const secondLogPath = resolve(
    runsRoot,
    `${runId}.same-process-turn-2.jsonl`,
  );
  const sessionPath = resolve(runsRoot, `${runId}.session.json`);

  console.log("Claude Agent SDK provider identity/resume POC — create");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log("safety: no built-in tools, no settings sources, dontAsk mode");

  const first = await runClaudeTurn({
    cwd: options.cwd,
    prompt: [
      "This is a read-only provider identity POC.",
      "Do not use tools, run commands, or modify files.",
      `Remember this exact continuity marker for later turns: ${marker}`,
      `Reply with exactly: MARKER STORED: ${marker}`,
    ].join(" "),
    rawLogPath: firstLogPath,
    timeoutMs: options.timeoutMs,
  });
  if (!first.resultText.includes(marker)) {
    throw new Error(`First Claude turn did not return marker ${marker}`);
  }

  console.log(
    `IDENTITY CAPTURED: event #${first.firstIdentity.sequence} ${first.firstIdentity.type}/${first.firstIdentity.subtype ?? "unknown"} session_id=${first.sessionId}`,
  );
  console.log(`CWD VERIFIED: ${first.cwd}`);

  const second = await runClaudeTurn({
    cwd: options.cwd,
    expectedSessionId: first.sessionId,
    prompt: [
      "Without using tools, reply with only the exact continuity marker",
      "you were asked to remember earlier. Do not explain it.",
    ].join(" "),
    rawLogPath: secondLogPath,
    timeoutMs: options.timeoutMs,
  });
  verifyContinuity(second, first.sessionId, marker);

  const record: ClaudeSessionRecord = {
    version: 1,
    provider: "claude",
    sessionId: first.sessionId,
    marker,
    cwd: first.cwd,
    createdAt: new Date().toISOString(),
    claudeCodeVersion: first.claudeCodeVersion,
    identityFirstSeen: first.firstIdentity,
    createLogs: [
      relative(dirname(sessionPath), firstLogPath),
      relative(dirname(sessionPath), secondLogPath),
    ],
  };
  await writeFile(sessionPath, `${JSON.stringify(record, null, 2)}\n`, {
    flag: "wx",
  });

  console.log(
    `SAME-PROCESS CONTINUITY VERIFIED: second SDK query returned ${marker}`,
  );
  console.log(`session artifact: ${relative(process.cwd(), sessionPath)}`);
  console.log("PASS: two turns used the exact Claude session and cwd.");
  console.log("Resume from a fresh process with:");
  console.log(
    `pnpm claude resume --session '${relative(process.cwd(), sessionPath)}'`,
  );
}

async function resumeScenario(options: ProbeOptions): Promise<void> {
  const sessionPath = options.sessionPath;
  if (sessionPath === undefined) {
    throw new Error("resume requires a session artifact");
  }
  const record = assertSessionRecord(
    JSON.parse(await readFile(sessionPath, "utf8")) as unknown,
  );
  const runId = makeRunId();
  const rawLogPath = resolve(
    runsRoot,
    `${runId}.resume-${record.sessionId}.jsonl`,
  );

  console.log("Claude Agent SDK provider identity/resume POC — resume");
  console.log(`expected session_id: ${record.sessionId}`);
  console.log(`expected cwd: ${record.cwd}`);
  console.log("safety: no built-in tools, no settings sources, dontAsk mode");

  const resumed = await runClaudeTurn({
    cwd: canonicalPath(record.cwd),
    expectedSessionId: record.sessionId,
    prompt: [
      "Without using tools, reply with only the exact continuity marker",
      "you were asked to remember earlier. Do not explain it.",
    ].join(" "),
    rawLogPath,
    timeoutMs: options.timeoutMs,
  });
  verifyContinuity(resumed, record.sessionId, record.marker);

  console.log(
    `IDENTITY VERIFIED: resumed system/init and result returned session_id=${record.sessionId}`,
  );
  console.log(`CWD VERIFIED: ${resumed.cwd}`);
  console.log(`CONTINUITY VERIFIED: resumed turn returned ${record.marker}`);
  console.log(
    "PASS: a fresh SDK client process resumed the exact Claude session, cwd, and context.",
  );
}

async function invalidResumeScenario(options: ProbeOptions): Promise<void> {
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.invalid-resume.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.invalid-resume.json`);
  const beforeSessionIds = await listClaudeSessionIds(options.cwd);
  let invalidSessionId = randomUUID();
  while (beforeSessionIds.includes(invalidSessionId)) {
    invalidSessionId = randomUUID();
  }

  console.log("Claude Agent SDK provider identity/resume POC — invalid resume safety");
  console.log(`cwd: ${options.cwd}`);
  console.log(`invalid session_id: ${invalidSessionId}`);
  console.log("safety: resume only; missing IDs fail before query()");

  const missingError = captureExpectedError(
    () => requireResumeId(undefined),
    "missing resume ID",
  );
  const attempt = await requireClaudeResumeFailure({
    cwd: options.cwd,
    expectedSessionId: requireResumeId(invalidSessionId),
    prompt: [
      "This is a read-only invalid-resume POC.",
      "Do not use tools, run commands, or modify files.",
      "Reply with exactly: INVALID RESUME SHOULD NOT RUN",
    ].join(" "),
    rawLogPath,
    timeoutMs: options.timeoutMs,
  });
  verifyInvalidResumeFailure(invalidSessionId, attempt);
  const afterSessionIds = await listClaudeSessionIds(options.cwd);
  verifyNoReplacementSessions(
    beforeSessionIds,
    afterSessionIds,
    invalidSessionId,
    attempt,
  );

  const result = {
    version: 1,
    provider: "claude",
    scenario: "invalid-resume",
    cwd: options.cwd,
    completedAt: new Date().toISOString(),
    evidence: {
      invalidSessionId,
      beforeSessionIds,
      afterSessionIds,
      invalidError: attempt.error,
      missingError,
      observedSessionIds: attempt.observedSessionIds,
      messageCount: attempt.messageCount,
      resultSubtype: attempt.resultSubtype,
      numTurns: attempt.numTurns,
      totalCostUsd: attempt.totalCostUsd,
      replacementSessionCount: 0,
    },
    rawLogs: [relative(dirname(resultPath), rawLogPath)],
  };
  await writeFile(resultPath, `${JSON.stringify(result, null, 2)}\n`, {
    flag: "wx",
  });

  console.log(`EXPECTED ERROR (invalid): ${attempt.error}`);
  console.log(`EXPECTED ERROR (missing): ${missingError}`);
  console.log(
    `SESSION LIST VERIFIED: ${beforeSessionIds.length} before, ${afterSessionIds.length} after, no replacement IDs`,
  );
  console.log("");
  console.log(
    "PASS: invalid and missing resume IDs failed clearly without creating or accepting a replacement session.",
  );
  console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
}

async function lifecycleScenario(options: ProbeOptions): Promise<void> {
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.lifecycle.jsonl`);
  const resultPath = resolve(runsRoot, `${runId}.lifecycle.json`);
  const marker =
    options.marker ??
    `ATC-CLAUDE-LIFECYCLE-${randomUUID().slice(0, 8).toUpperCase()}`;

  console.log("Claude Agent SDK provider identity/resume POC — lifecycle signals");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log("mode: streaming input with a bounded post-result observation window");
  console.log("safety: no built-in tools, no settings sources, dontAsk mode");

  const observation = await runClaudeLifecycle({
    cwd: options.cwd,
    prompt: [
      "This is a read-only lifecycle-signal POC.",
      "Do not use tools, run commands, or modify files.",
      `Reply with exactly: LIFECYCLE COMPLETE: ${marker}`,
    ].join(" "),
    rawLogPath,
    timeoutMs: options.timeoutMs,
    postResultWaitMs: options.lifecycleObservationMs,
  });
  if (!observation.resultText.includes(marker)) {
    throw new Error(
      `Lifecycle turn completed without the expected marker ${marker}`,
    );
  }
  verifyLifecycleTransitions(observation.stateTransitions);
  const signals = summarizeLifecycleSignals(observation);

  const result = {
    version: 1,
    provider: "claude",
    scenario: "lifecycle",
    cwd: observation.cwd,
    sessionId: observation.sessionId,
    claudeCodeVersion: observation.claudeCodeVersion,
    completedAt: new Date().toISOString(),
    evidence: {
      inputMode: "streaming",
      marker,
      messageCount: observation.messageCount,
      capabilities: observation.capabilities,
      firstActivity: observation.firstActivity,
      result: observation.result,
      postResultObservationMs: options.lifecycleObservationMs,
      stateTransitions: observation.stateTransitions,
      signals,
      tools: [],
      permissionMode: "dontAsk",
      settingSources: [],
    },
    rawLogs: [relative(dirname(resultPath), rawLogPath)],
  };
  await writeFile(resultPath, `${JSON.stringify(result, null, 2)}\n`, {
    flag: "wx",
  });

  if (signals.working.status === "observed") {
    console.log(
      `WORKING SIGNAL OBSERVED: running at event #${signals.working.sequences.join(", #")}`,
    );
  } else {
    console.log(
      `WORKING STATE NOT OBSERVED: first activity was ${formatBoundary(observation.firstActivity)}.`,
    );
  }
  if (signals.idle.status === "observed") {
    console.log(
      `IDLE SIGNAL OBSERVED: idle at event #${signals.idle.sequences.join(", #")}`,
    );
  } else {
    console.log(
      `IDLE STATE NOT OBSERVED: successful turn completion was ${formatBoundary(observation.result)}.`,
    );
  }
  if (signals.needsInput.status === "observed") {
    console.log(
      `NEEDS_INPUT SIGNAL OBSERVED: requires_action at event #${signals.needsInput.sequences.join(", #")}`,
    );
  } else {
    console.log(
      "NEEDS_INPUT STATE NOT OBSERVED: requires_action needs a separate interactive-request scenario.",
    );
  }
  console.log("");
  console.log(
    "PASS: bounded streaming lifecycle evidence was captured without tools or repository changes.",
  );
  console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
}

async function inputRequestScenario(options: ProbeOptions): Promise<void> {
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.input-request.jsonl`);
  const timelinePath = resolve(
    runsRoot,
    `${runId}.input-request.timeline.jsonl`,
  );
  const resultPath = resolve(runsRoot, `${runId}.input-request.json`);
  const marker =
    options.marker ??
    `ATC-CLAUDE-INPUT-${randomUUID().slice(0, 8).toUpperCase()}`;
  const timeline = new ClaudeControlTimeline();
  let selectedAnswer: string | undefined;

  console.log("Claude Agent SDK provider identity/resume POC — input request");
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log("tool exposure: AskUserQuestion only");
  console.log("safety: no filesystem, shell, network, or settings tools");

  let observation: ClaudeLifecycleObservation;
  try {
    observation = await runClaudeLifecycle({
      cwd: options.cwd,
      prompt: [
        "This is a provider input-request POC.",
        "Use AskUserQuestion exactly once before replying.",
        'Ask exactly: "Which marker path should ATC use?"',
        'Offer exactly two single-select options: "Alpha" and "Beta".',
        "Do not use any other tool.",
        `After the answer, reply with: INPUT RECEIVED: <answer> ${marker}`,
      ].join(" "),
      rawLogPath,
      timeoutMs: options.timeoutMs,
      postResultWaitMs: options.lifecycleObservationMs,
      queryPolicy: {
        tools: ["AskUserQuestion"],
        permissionMode: "default",
        maxTurns: 2,
        canUseTool: async (toolName, input, callbackOptions) => {
          if (toolName !== "AskUserQuestion") {
            throw new Error(`Unexpected interactive tool request: ${toolName}`);
          }
          timeline.recordRequest({
            requestId: callbackOptions.requestId,
            toolUseId: callbackOptions.toolUseID,
            toolName,
            input,
          });

          selectedAnswer =
            options.answer ?? await promptForQuestionAnswer(input);
          if (options.responseDelayMs > 0) {
            await delay(options.responseDelayMs, undefined, {
              signal: callbackOptions.signal,
            });
          }
          const result = buildAskUserQuestionResult(input, selectedAnswer);
          timeline.recordResponse({
            requestId: callbackOptions.requestId,
            toolUseId: callbackOptions.toolUseID,
            toolName,
            result,
          });
          return result;
        },
      },
      onMessage: (sequence, message) => {
        timeline.recordMessage(sequence, message);
      },
    });
  } finally {
    await writeFile(timelinePath, timeline.toJsonLines(), { flag: "wx" });
  }

  if (selectedAnswer === undefined) {
    throw new Error("Claude did not request an AskUserQuestion answer");
  }
  if (
    !observation.resultText.includes(marker) ||
    !observation.resultText.includes(selectedAnswer)
  ) {
    throw new Error(
      `Input-request result did not preserve answer ${selectedAnswer} and marker ${marker}`,
    );
  }
  verifyNeedsInputTransitions(observation.stateTransitions);
  verifyBlockingCallbackTimeline(timeline.events);

  const signals = summarizeLifecycleSignals(observation);
  const result = {
    version: 1,
    provider: "claude",
    scenario: "input-request",
    cwd: observation.cwd,
    sessionId: observation.sessionId,
    claudeCodeVersion: observation.claudeCodeVersion,
    completedAt: new Date().toISOString(),
    evidence: {
      inputMode: "streaming",
      marker,
      answer: selectedAnswer,
      messageCount: observation.messageCount,
      stateTransitions: observation.stateTransitions,
      signals,
      tools: ["AskUserQuestion"],
      permissionMode: "default",
      settingSources: [],
      callbackEvents: timeline.events.filter(
        (event) => event.source === "callback",
      ),
    },
    rawLogs: [
      relative(dirname(resultPath), rawLogPath),
      relative(dirname(resultPath), timelinePath),
    ],
  };
  await writeFile(resultPath, `${JSON.stringify(result, null, 2)}\n`, {
    flag: "wx",
  });

  console.log(
    `NEEDS_INPUT SIGNAL OBSERVED: requires_action at event #${signals.needsInput.sequences.join(", #")}`,
  );
  console.log(`ANSWER ACCEPTED: ${selectedAnswer}`);
  console.log("");
  console.log(
    "PASS: AskUserQuestion blocked for input and resumed the exact session without repository-capable tools.",
  );
  console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
}

async function permissionRequestScenario(options: ProbeOptions): Promise<void> {
  const runId = makeRunId();
  const rawLogPath = resolve(runsRoot, `${runId}.permission-request.jsonl`);
  const timelinePath = resolve(
    runsRoot,
    `${runId}.permission-request.timeline.jsonl`,
  );
  const resultPath = resolve(runsRoot, `${runId}.permission-request.json`);
  const marker =
    options.marker ??
    `ATC-CLAUDE-PERMISSION-${randomUUID().slice(0, 8).toUpperCase()}`;
  const timeline = new ClaudeControlTimeline();
  let permissionDecision: "allow" | "deny" | undefined;

  console.log(
    "Claude Agent SDK provider identity/resume POC — permission request",
  );
  console.log(`cwd: ${options.cwd}`);
  console.log(`marker: ${marker}`);
  console.log("tool exposure: Bash only; inline ask rule: Bash(pwd)");
  console.log("safety: the callback rejects every command except exact pwd");

  let observation: ClaudeLifecycleObservation;
  try {
    observation = await runClaudeLifecycle({
      cwd: options.cwd,
      prompt: [
        "This is a harmless provider permission-request POC.",
        "Use Bash exactly once with the exact command pwd.",
        "Do not combine it with another command or use shell operators.",
        "After the tool result, reply with:",
        `PERMISSION COMPLETE: <absolute working directory> ${marker}`,
      ].join(" "),
      rawLogPath,
      timeoutMs: options.timeoutMs,
      postResultWaitMs: options.lifecycleObservationMs,
      queryPolicy: {
        tools: ["Bash"],
        permissionMode: "default",
        maxTurns: 2,
        settings: {
          permissions: {
            ask: ["Bash(pwd)"],
          },
        },
        canUseTool: async (toolName, input, callbackOptions) => {
          requireExactPwdRequest(toolName, input);
          timeline.recordRequest({
            requestId: callbackOptions.requestId,
            toolUseId: callbackOptions.toolUseID,
            toolName,
            input,
          });

          permissionDecision =
            options.decision ?? await promptForPermission(input);
          if (options.responseDelayMs > 0) {
            await delay(options.responseDelayMs, undefined, {
              signal: callbackOptions.signal,
            });
          }
          const result = buildPermissionResult(input, permissionDecision);
          timeline.recordResponse({
            requestId: callbackOptions.requestId,
            toolUseId: callbackOptions.toolUseID,
            toolName,
            result,
          });
          return result;
        },
      },
      onMessage: (sequence, message) => {
        timeline.recordMessage(sequence, message);
      },
    });
  } finally {
    await writeFile(timelinePath, timeline.toJsonLines(), { flag: "wx" });
  }

  if (permissionDecision === undefined) {
    throw new Error("Claude did not emit a Bash permission request");
  }
  if (!observation.resultText.includes(marker)) {
    throw new Error(
      `Permission-request result did not preserve marker ${marker}`,
    );
  }
  if (
    permissionDecision === "allow" &&
    !observation.resultText.includes(options.cwd)
  ) {
    throw new Error(
      `Allowed pwd result did not report expected cwd ${options.cwd}`,
    );
  }
  verifyNeedsInputTransitions(observation.stateTransitions);
  verifyBlockingCallbackTimeline(timeline.events);

  const signals = summarizeLifecycleSignals(observation);
  const result = {
    version: 1,
    provider: "claude",
    scenario: "permission-request",
    cwd: observation.cwd,
    sessionId: observation.sessionId,
    claudeCodeVersion: observation.claudeCodeVersion,
    completedAt: new Date().toISOString(),
    evidence: {
      inputMode: "streaming",
      marker,
      decision: permissionDecision,
      messageCount: observation.messageCount,
      stateTransitions: observation.stateTransitions,
      signals,
      tools: ["Bash"],
      command: "pwd",
      permissionMode: "default",
      permissionAskRules: ["Bash(pwd)"],
      settingSources: [],
      callbackEvents: timeline.events.filter(
        (event) => event.source === "callback",
      ),
    },
    rawLogs: [
      relative(dirname(resultPath), rawLogPath),
      relative(dirname(resultPath), timelinePath),
    ],
  };
  await writeFile(resultPath, `${JSON.stringify(result, null, 2)}\n`, {
    flag: "wx",
  });

  console.log(
    `PERMISSION SIGNAL OBSERVED: requires_action at event #${signals.needsInput.sequences.join(", #")}`,
  );
  console.log(`DECISION APPLIED: ${permissionDecision}`);
  console.log("");
  console.log(
    "PASS: exact pwd required approval and the same session returned to idle without modifying the repository.",
  );
  console.log(`result artifact: ${relative(process.cwd(), resultPath)}`);
}

export function requireExactPwdRequest(
  toolName: string,
  input: Record<string, unknown>,
): void {
  if (toolName !== "Bash" || input.command !== "pwd") {
    throw new Error(
      `Unsafe permission probe request: expected Bash pwd, received ${toolName} ${String(input.command)}`,
    );
  }
}

export function buildPermissionResult(
  input: Record<string, unknown>,
  decision: "allow" | "deny",
): PermissionResult {
  requireExactPwdRequest("Bash", input);
  return decision === "allow"
    ? {
        behavior: "allow",
        updatedInput: input,
      }
    : {
        behavior: "deny",
        message: "User denied the harmless ATC permission probe.",
      };
}

async function promptForPermission(
  input: Record<string, unknown>,
): Promise<"allow" | "deny"> {
  console.log("");
  console.log(`Claude requests Bash command: ${String(input.command)}`);
  const terminal = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  try {
    const response = (
      await terminal.question("Allow this exact command? (y/N): ")
    )
      .trim()
      .toLowerCase();
    return response === "y" || response === "yes" ? "allow" : "deny";
  } finally {
    terminal.close();
  }
}

export function buildAskUserQuestionResult(
  input: Record<string, unknown>,
  answer: string,
): PermissionResult {
  const questions = questionsOf(input);
  const question = questions[0];
  if (questions.length !== 1 || question === undefined) {
    throw new Error(
      `Expected exactly one AskUserQuestion question, received ${questions.length}`,
    );
  }
  const selectedAnswer = answer.trim();
  if (selectedAnswer.length === 0) {
    throw new Error("AskUserQuestion answer must not be blank");
  }

  return {
    behavior: "allow",
    updatedInput: {
      questions: input.questions,
      answers: {
        [question.question]: selectedAnswer,
      },
    },
  };
}

export function verifyNeedsInputTransitions(
  transitions: ClaudeLifecycleTransition[],
): void {
  const needsInputIndex = transitions.findIndex(
    (transition) => transition.state === "requires_action",
  );
  if (needsInputIndex === -1) {
    throw new Error(
      "Claude input-request probe emitted no requires_action state",
    );
  }
  const idleAfterInput = transitions
    .slice(needsInputIndex + 1)
    .some((transition) => transition.state === "idle");
  if (!idleAfterInput) {
    throw new Error(
      "Claude input-request probe emitted no idle state after requires_action",
    );
  }
}

export function verifyBlockingCallbackTimeline(
  events: ClaudeControlTimelineEvent[],
): void {
  const requests = events.filter(
    (event) => event.source === "callback" && event.event === "request",
  );
  const responses = events.filter(
    (event) => event.source === "callback" && event.event === "response",
  );
  if (requests.length !== 1 || responses.length !== 1) {
    throw new Error(
      `Expected one callback request and response, received ${requests.length} and ${responses.length}`,
    );
  }
  const request = requests[0];
  const response = responses[0];
  if (request === undefined || response === undefined) {
    throw new Error("Missing callback request or response");
  }
  if (
    request.requestId !== response.requestId ||
    request.toolUseId !== response.toolUseId
  ) {
    throw new Error("Callback response correlation did not match its request");
  }
  const requiresAction = events.find(
    (event) =>
      event.source === "sdk" &&
      event.event === "message" &&
      event.state === "requires_action",
  );
  const stateAfterAction = events.find(
    (event) =>
      requiresAction !== undefined &&
      event.sequence > requiresAction.sequence &&
      event.source === "sdk" &&
      event.event === "message" &&
      event.subtype === "session_state_changed" &&
      event.state !== "requires_action",
  );
  if (
    requiresAction === undefined ||
    request.sequence >= response.sequence ||
    requiresAction.sequence >= response.sequence ||
    stateAfterAction === undefined ||
    stateAfterAction.sequence <= request.sequence ||
    stateAfterAction.sequence <= response.sequence
  ) {
    throw new Error(
      "The callback interval did not overlap the provider requires_action interval",
    );
  }
}

async function promptForQuestionAnswer(
  input: Record<string, unknown>,
): Promise<string> {
  const questions = questionsOf(input);
  const question = questions[0];
  if (questions.length !== 1 || question === undefined) {
    throw new Error(
      `Expected exactly one AskUserQuestion question, received ${questions.length}`,
    );
  }

  console.log("");
  console.log(`${question.header}: ${question.question}`);
  question.options.forEach((option, index) => {
    console.log(`  ${index + 1}. ${option.label} — ${option.description}`);
  });

  const terminal = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  try {
    const response = (await terminal.question("Your choice: ")).trim();
    const optionIndex = Number(response) - 1;
    return Number.isInteger(optionIndex) &&
      optionIndex >= 0 &&
      optionIndex < question.options.length
      ? question.options[optionIndex]?.label ?? response
      : response;
  } finally {
    terminal.close();
  }
}

function questionsOf(input: Record<string, unknown>): Array<{
  question: string;
  header: string;
  options: Array<{ label: string; description: string }>;
}> {
  if (!Array.isArray(input.questions)) {
    throw new Error("AskUserQuestion input did not contain questions");
  }
  return input.questions.map((value) => {
    if (
      !isRecord(value) ||
      typeof value.question !== "string" ||
      typeof value.header !== "string" ||
      !Array.isArray(value.options)
    ) {
      throw new Error("AskUserQuestion question had an invalid shape");
    }
    const options = value.options.map((option) => {
      if (
        !isRecord(option) ||
        typeof option.label !== "string" ||
        typeof option.description !== "string"
      ) {
        throw new Error("AskUserQuestion option had an invalid shape");
      }
      return {
        label: option.label,
        description: option.description,
      };
    });
    if (options.length < 2) {
      throw new Error("AskUserQuestion requires at least two options");
    }
    return {
      question: value.question,
      header: value.header,
      options,
    };
  });
}

interface LifecycleSignalSummary {
  providerState: ClaudeLifecycleTransition["state"];
  status: "observed" | "not_observed";
  sequences: number[];
}

function summarizeLifecycleSignal(
  providerState: ClaudeLifecycleTransition["state"],
  transitions: ClaudeLifecycleTransition[],
): LifecycleSignalSummary {
  const sequences = transitions
    .filter((transition) => transition.state === providerState)
    .map((transition) => transition.sequence);
  return {
    providerState,
    status: sequences.length > 0 ? "observed" : "not_observed",
    sequences,
  };
}

function formatBoundary(boundary: {
  sequence: number;
  type: string;
  subtype?: string;
}): string {
  return `event #${boundary.sequence} ${boundary.type}${boundary.subtype === undefined ? "" : `/${boundary.subtype}`}`;
}

function canonicalPath(path: string): string {
  const absolute = resolve(path);
  if (!existsSync(absolute)) {
    throw new Error(`Working directory does not exist: ${absolute}`);
  }
  return realpathSync(absolute);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isIdentityAvailability(
  value: unknown,
): value is ClaudeIdentityAvailability {
  return (
    isRecord(value) &&
    typeof value.sequence === "number" &&
    typeof value.type === "string" &&
    (value.subtype === undefined || typeof value.subtype === "string")
  );
}

function makeRunId(): string {
  return `${new Date().toISOString().replaceAll(":", "-").replace(".", "-")}-${randomUUID().slice(0, 8)}`;
}

function captureExpectedError(
  action: () => unknown,
  label: string,
): string {
  try {
    action();
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
  throw new Error(`Expected ${label} to fail`);
}

function usage(): string {
  return [
    "Usage:",
    "  pnpm claude create [--cwd <path>] [--marker <value>] [--timeout-seconds <n>]",
    "  pnpm claude resume --session <path> [--timeout-seconds <n>]",
    "  pnpm claude invalid-resume [--cwd <path>] [--timeout-seconds <n>]",
    "  pnpm claude lifecycle [--cwd <path>] [--marker <value>] [--observation-seconds <n>] [--timeout-seconds <n>]",
    "  pnpm claude input-request [--cwd <path>] [--marker <value>] [--answer <value>] [--response-delay-seconds <n>] [--timeout-seconds <n>]",
    "  pnpm claude permission-request [--cwd <path>] [--marker <value>] [--decision <allow|deny>] [--response-delay-seconds <n>] [--timeout-seconds <n>]",
  ].join("\n");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
