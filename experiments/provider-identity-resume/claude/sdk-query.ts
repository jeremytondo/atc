import { realpathSync } from "node:fs";
import { mkdir, open } from "node:fs/promises";
import { dirname } from "node:path";

import {
  listSessions,
  query,
  type Options,
  type SDKAssistantMessage,
  type SDKMessage,
  type SDKResultMessage,
  type SDKSystemMessage,
} from "@anthropic-ai/claude-agent-sdk";

export interface ClaudeTurnOptions {
  cwd: string;
  prompt: string;
  rawLogPath: string;
  timeoutMs: number;
  expectedSessionId?: string;
}

export interface ClaudeIdentityAvailability {
  sequence: number;
  type: string;
  subtype?: string;
}

export interface ClaudeTurnObservation {
  sessionId: string;
  cwd: string;
  claudeCodeVersion: string;
  firstIdentity: ClaudeIdentityAvailability;
  messageCount: number;
  resultText: string;
  stateTransitions: string[];
}

export interface ClaudeResumeFailureObservation {
  error: string;
  messageCount: number;
  observedSessionIds: string[];
  resultSubtype?: string;
  numTurns?: number;
  totalCostUsd?: number;
}

export async function runClaudeTurn(
  options: ClaudeTurnOptions,
): Promise<ClaudeTurnObservation> {
  let sessionId: string | undefined;
  let firstIdentity: ClaudeIdentityAvailability | undefined;
  let init: SDKSystemMessage | undefined;
  let result: SDKResultMessage | undefined;
  const stateTransitions: string[] = [];

  const messageCount = await consumeClaudeQuery(
    options,
    (sequence, message) => {
      const messageSessionId = sessionIdOf(message);
      if (messageSessionId !== undefined) {
        if (sessionId === undefined) {
          sessionId = messageSessionId;
          firstIdentity = {
            sequence,
            type: message.type,
            subtype: subtypeOf(message),
          };
        } else if (messageSessionId !== sessionId) {
          throw new Error(
            `Session identity changed within one query: expected ${sessionId}, received ${messageSessionId}`,
          );
        }

        if (
          options.expectedSessionId !== undefined &&
          messageSessionId !== options.expectedSessionId
        ) {
          throw new Error(
            `Identity mismatch after resume: expected ${options.expectedSessionId}, received ${messageSessionId}`,
          );
        }
      }

      if (message.type === "system" && message.subtype === "init") {
        init = message;
      } else if (
        message.type === "system" &&
        message.subtype === "session_state_changed"
      ) {
        stateTransitions.push(message.state);
      } else if (message.type === "result") {
        result = message;
      }
    },
  );

  if (sessionId === undefined || firstIdentity === undefined) {
    throw new Error("Claude Agent SDK emitted no durable session ID");
  }
  if (init === undefined) {
    throw new Error("Claude Agent SDK emitted no system/init message");
  }
  if (result === undefined) {
    throw new Error("Claude Agent SDK emitted no result message");
  }
  if (result.subtype !== "success") {
    throw new Error(
      `Claude turn failed with ${result.subtype}: ${result.errors.join("; ")}`,
    );
  }
  if (result.session_id !== sessionId) {
    throw new Error(
      `Result identity mismatch: expected ${sessionId}, received ${result.session_id}`,
    );
  }
  if (canonicalPath(init.cwd) !== canonicalPath(options.cwd)) {
    throw new Error(
      `Working-directory mismatch: expected ${options.cwd}, received ${init.cwd}`,
    );
  }
  if (init.permissionMode !== "dontAsk") {
    throw new Error(
      `Unsafe permission mode: expected dontAsk, received ${init.permissionMode}`,
    );
  }
  if (init.tools.length !== 0) {
    throw new Error(
      `Read-only probe expected no tools, but init exposed: ${init.tools.join(", ")}`,
    );
  }

  return {
    sessionId,
    cwd: canonicalPath(init.cwd),
    claudeCodeVersion: init.claude_code_version,
    firstIdentity,
    messageCount,
    resultText: result.result,
    stateTransitions,
  };
}

export async function requireClaudeResumeFailure(
  options: ClaudeTurnOptions & { expectedSessionId: string },
): Promise<ClaudeResumeFailureObservation> {
  const observedSessionIds = new Set<string>();
  let result: SDKResultMessage | undefined;
  let messageCount = 0;
  let streamError: string | undefined;

  try {
    messageCount = await consumeClaudeQuery(options, (sequence, message) => {
      messageCount = sequence;
      const sessionId = sessionIdOf(message);
      if (sessionId !== undefined) {
        observedSessionIds.add(sessionId);
      }
      if (message.type === "result") {
        result = message;
      }
    });
  } catch (error) {
    streamError = errorMessage(error);
  }

  if (result?.subtype === "success") {
    throw new Error(
      `Invalid resume unexpectedly completed successfully as session ${result.session_id}`,
    );
  }

  const observation = {
    messageCount,
    observedSessionIds: [...observedSessionIds],
    resultSubtype: result?.subtype,
    numTurns: result?.num_turns,
    totalCostUsd: result?.total_cost_usd,
  };
  if (streamError !== undefined) {
    return {
      ...observation,
      error: streamError,
    };
  }
  if (result === undefined) {
    throw new Error(
      "Invalid resume completed without an explicit SDK error or result",
    );
  }

  return {
    ...observation,
    error: `Claude result ${result.subtype}: ${result.errors.join("; ")}`,
  };
}

export async function listClaudeSessionIds(cwd: string): Promise<string[]> {
  const pageSize = 100;
  const sessionIds = new Set<string>();

  for (let offset = 0; ; offset += pageSize) {
    const page = await listSessions({
      dir: cwd,
      includeProgrammatic: true,
      includeWorktrees: false,
      limit: pageSize,
      offset,
    });
    for (const session of page) {
      sessionIds.add(session.sessionId);
    }
    if (page.length < pageSize) {
      return [...sessionIds].sort();
    }
  }
}

export function buildQueryOptions(
  options: Pick<ClaudeTurnOptions, "cwd" | "expectedSessionId">,
  abortController: AbortController,
): Options {
  return {
    abortController,
    allowedTools: [],
    cwd: options.cwd,
    maxTurns: 1,
    permissionMode: "dontAsk",
    persistSession: true,
    resume: options.expectedSessionId,
    settingSources: [],
    tools: [],
  };
}

export function sessionIdOf(message: SDKMessage): string | undefined {
  return "session_id" in message && typeof message.session_id === "string"
    ? message.session_id
    : undefined;
}

export function assistantText(message: SDKAssistantMessage): string {
  return message.message.content
    .filter(
      (block): block is Extract<(typeof message.message.content)[number], { type: "text" }> =>
        block.type === "text",
    )
    .map((block) => block.text)
    .join("");
}

function printReadableMessage(sequence: number, message: SDKMessage): void {
  const subtype = subtypeOf(message);
  const session = sessionIdOf(message);
  const label = `${message.type}${subtype === undefined ? "" : `/${subtype}`}`;
  const identity = session === undefined ? "" : ` session=${session}`;

  if (message.type === "system" && message.subtype === "init") {
    console.log(
      `← #${sequence} ${label}${identity} cwd=${message.cwd} tools=${message.tools.length} mode=${message.permissionMode}`,
    );
    return;
  }
  if (message.type === "assistant") {
    const text = assistantText(message).trim();
    console.log(`← #${sequence} ${label}${identity}`);
    if (text.length > 0) {
      console.log(`  ${text}`);
    }
    return;
  }
  if (message.type === "result") {
    console.log(
      `← #${sequence} ${label}${identity} turns=${message.num_turns} cost=$${message.total_cost_usd.toFixed(6)}`,
    );
    return;
  }

  console.log(`← #${sequence} ${label}${identity}`);
}

async function consumeClaudeQuery(
  options: ClaudeTurnOptions,
  inspectMessage: (sequence: number, message: SDKMessage) => void,
): Promise<number> {
  await mkdir(dirname(options.rawLogPath), { recursive: true });
  const rawLog = await open(options.rawLogPath, "wx");
  const abortController = new AbortController();
  const timeout = setTimeout(() => abortController.abort(), options.timeoutMs);
  const stream = query({
    prompt: options.prompt,
    options: buildQueryOptions(options, abortController),
  });
  let sequence = 0;

  try {
    for await (const message of stream) {
      sequence += 1;
      await rawLog.writeFile(`${JSON.stringify(message)}\n`);
      printReadableMessage(sequence, message);
      inspectMessage(sequence, message);
    }
    return sequence;
  } catch (error) {
    stream.close();
    if (abortController.signal.aborted) {
      throw new Error(
        `Timed out after ${options.timeoutMs}ms waiting for Claude Agent SDK`,
        { cause: error },
      );
    }
    throw error;
  } finally {
    clearTimeout(timeout);
    await rawLog.close();
  }
}

function subtypeOf(message: SDKMessage): string | undefined {
  return "subtype" in message && typeof message.subtype === "string"
    ? message.subtype
    : undefined;
}

function canonicalPath(path: string): string {
  return realpathSync(path);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
