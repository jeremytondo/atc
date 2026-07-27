import { realpathSync } from "node:fs";
import { mkdir, open } from "node:fs/promises";
import { dirname } from "node:path";

import {
  listSessions,
  query,
  type CanUseTool,
  type Options,
  type PermissionMode,
  type Query,
  type SDKAssistantMessage,
  type SDKControlInterruptResponse,
  type SDKMessage,
  type SDKResultMessage,
  type SDKSessionStateChangedMessage,
  type SDKSystemMessage,
  type SDKUserMessage,
} from "@anthropic-ai/claude-agent-sdk";

export interface ClaudeTurnOptions {
  cwd: string;
  prompt: string | AsyncIterable<SDKUserMessage>;
  rawLogPath: string;
  timeoutMs: number;
  expectedSessionId?: string;
  queryPolicy?: ClaudeQueryPolicy;
  onMessage?: (sequence: number, message: SDKMessage) => void;
  onQuery?: (query: Query) => void;
  acceptErrorResult?: boolean;
}

export interface ClaudeQueryPolicy {
  tools: string[];
  permissionMode: PermissionMode;
  maxTurns: number;
  allowedTools?: string[];
  canUseTool?: CanUseTool;
  settings?: Options["settings"];
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

export interface ClaudeLifecycleTransition {
  sequence: number;
  state: SDKSessionStateChangedMessage["state"];
}

export interface ClaudeMessageBoundary {
  sequence: number;
  type: string;
  subtype?: string;
}

export interface ClaudeLifecycleObservation
  extends Omit<ClaudeTurnObservation, "stateTransitions"> {
  capabilities: string[];
  firstActivity: ClaudeMessageBoundary;
  result: ClaudeMessageBoundary;
  stateTransitions: ClaudeLifecycleTransition[];
}

type ClaudeObservedQuery = Omit<
  ClaudeLifecycleObservation,
  "firstActivity" | "result"
>;

export interface ClaudeResumeFailureObservation {
  error: string;
  messageCount: number;
  observedSessionIds: string[];
  resultSubtype?: string;
  numTurns?: number;
  totalCostUsd?: number;
}

export interface ClaudeInterruptedTurnObservation {
  sessionId: string;
  cwd: string;
  claudeCodeVersion: string;
  capabilities: string[];
  firstIdentity: ClaudeIdentityAvailability;
  messageCount: number;
  stateTransitions: ClaudeLifecycleTransition[];
  abortedAssistantMessages: number;
  result: {
    sequence: number;
    subtype: SDKResultMessage["subtype"];
    isError: boolean;
    numTurns: number;
    totalCostUsd: number;
    errors: string[];
  };
  interruptReceipt?: SDKControlInterruptResponse;
}

export async function runClaudeTurn(
  options: ClaudeTurnOptions,
): Promise<ClaudeTurnObservation> {
  const observation = await observeClaudeQuery(options);
  return {
    ...observation,
    stateTransitions: observation.stateTransitions.map(
      (transition) => transition.state,
    ),
  };
}

export async function runClaudeLifecycle(
  options: Omit<ClaudeTurnOptions, "prompt"> & {
    prompt: string;
    postResultWaitMs: number;
  },
): Promise<ClaudeLifecycleObservation> {
  const input = createStreamingPrompt(options.prompt);
  let closeTimer: NodeJS.Timeout | undefined;
  let firstActivity: ClaudeMessageBoundary | undefined;
  let resultBoundary: ClaudeMessageBoundary | undefined;

  try {
    const observation = await observeClaudeQuery(
      {
        cwd: options.cwd,
        prompt: input.messages,
        rawLogPath: options.rawLogPath,
        timeoutMs: options.timeoutMs,
        expectedSessionId: options.expectedSessionId,
        queryPolicy: options.queryPolicy,
      },
      (sequence, message) => {
        options.onMessage?.(sequence, message);
        if (
          firstActivity === undefined &&
          !(
            message.type === "system" &&
            message.subtype === "init"
          ) &&
          message.type !== "rate_limit_event" &&
          message.type !== "result"
        ) {
          firstActivity = messageBoundary(sequence, message);
        }
        if (
          message.type === "system" &&
          message.subtype === "session_state_changed" &&
          message.state === "idle"
        ) {
          clearTimeout(closeTimer);
          input.close();
        } else if (message.type === "result") {
          resultBoundary = messageBoundary(sequence, message);
          closeTimer = setTimeout(
            () => input.close(),
            options.postResultWaitMs,
          );
        }
      },
    );
    if (firstActivity === undefined) {
      throw new Error(
        "Claude lifecycle probe emitted no activity message before its result",
      );
    }
    if (resultBoundary === undefined) {
      throw new Error("Claude lifecycle probe emitted no result boundary");
    }
    return {
      ...observation,
      firstActivity,
      result: resultBoundary,
    };
  } finally {
    clearTimeout(closeTimer);
    input.close();
  }
}

export async function runClaudeInterruptedTurn(
  options: Omit<ClaudeTurnOptions, "prompt" | "onQuery"> & {
    prompt: string;
    interruptDelayMs: number;
    postResultWaitMs: number;
    shouldInterrupt: (sequence: number, message: SDKMessage) => boolean;
    onInterruptRequest?: () => void;
    onInterruptResponse?: (
      receipt: SDKControlInterruptResponse | undefined,
    ) => void;
  },
): Promise<ClaudeInterruptedTurnObservation> {
  const input = createStreamingPrompt(options.prompt);
  let closeTimer: NodeJS.Timeout | undefined;
  let interruptTimer: NodeJS.Timeout | undefined;
  let queryHandle: Query | undefined;
  let interruptPromise:
    | Promise<SDKControlInterruptResponse | undefined>
    | undefined;
  let sessionId: string | undefined;
  let firstIdentity: ClaudeIdentityAvailability | undefined;
  let init: SDKSystemMessage | undefined;
  let result:
    | { sequence: number; message: SDKResultMessage }
    | undefined;
  let abortedAssistantMessages = 0;
  const stateTransitions: ClaudeLifecycleTransition[] = [];

  try {
    const messageCount = await consumeClaudeQuery(
      {
        ...options,
        prompt: input.messages,
        acceptErrorResult: true,
        onQuery: (query) => {
          queryHandle = query;
        },
      },
      (sequence, message) => {
        options.onMessage?.(sequence, message);
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
              `Session identity changed within interrupted query: expected ${sessionId}, received ${messageSessionId}`,
            );
          }
          if (
            options.expectedSessionId !== undefined &&
            messageSessionId !== options.expectedSessionId
          ) {
            throw new Error(
              `Identity mismatch during interruption: expected ${options.expectedSessionId}, received ${messageSessionId}`,
            );
          }
        }

        if (message.type === "system" && message.subtype === "init") {
          init = message;
        } else if (
          message.type === "system" &&
          message.subtype === "session_state_changed"
        ) {
          stateTransitions.push({ sequence, state: message.state });
          if (message.state === "idle") {
            clearTimeout(closeTimer);
            input.close();
          }
        } else if (message.type === "assistant" && message.aborted === true) {
          abortedAssistantMessages += 1;
        } else if (message.type === "result") {
          result = { sequence, message };
          closeTimer = setTimeout(
            () => input.close(),
            options.postResultWaitMs,
          );
        }

        if (
          interruptTimer === undefined &&
          interruptPromise === undefined &&
          options.shouldInterrupt(sequence, message)
        ) {
          if (queryHandle === undefined) {
            throw new Error(
              "Claude interruption trigger fired before query control was available",
            );
          }
          const control = queryHandle;
          interruptTimer = setTimeout(() => {
            options.onInterruptRequest?.();
            interruptPromise = control.interrupt().then((receipt) => {
              options.onInterruptResponse?.(receipt);
              return receipt;
            });
          }, options.interruptDelayMs);
        }
      },
    );

    if (interruptPromise === undefined) {
      throw new Error(
        "Claude turn ended before the bounded interrupt was issued",
      );
    }
    const interruptReceipt = await interruptPromise;
    if (
      sessionId === undefined ||
      firstIdentity === undefined ||
      init === undefined
    ) {
      throw new Error(
        "Interrupted Claude turn did not expose stable session initialization",
      );
    }
    if (result === undefined) {
      throw new Error("Interrupted Claude turn emitted no terminal result");
    }
    verifyClaudeInit(options, init);

    return {
      sessionId,
      cwd: canonicalPath(init.cwd),
      claudeCodeVersion: init.claude_code_version,
      capabilities: init.capabilities ?? [],
      firstIdentity,
      messageCount,
      stateTransitions,
      abortedAssistantMessages,
      result: {
        sequence: result.sequence,
        subtype: result.message.subtype,
        isError: result.message.is_error,
        numTurns: result.message.num_turns,
        totalCostUsd: result.message.total_cost_usd,
        errors:
          result.message.subtype === "success"
            ? []
            : result.message.errors,
      },
      interruptReceipt,
    };
  } finally {
    clearTimeout(closeTimer);
    clearTimeout(interruptTimer);
    input.close();
  }
}

async function observeClaudeQuery(
  options: ClaudeTurnOptions,
  inspectMessage?: (sequence: number, message: SDKMessage) => void,
): Promise<ClaudeObservedQuery> {
  let sessionId: string | undefined;
  let firstIdentity: ClaudeIdentityAvailability | undefined;
  let init: SDKSystemMessage | undefined;
  let result: SDKResultMessage | undefined;
  const stateTransitions: ClaudeLifecycleTransition[] = [];

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
        stateTransitions.push({
          sequence,
          state: message.state,
        });
      } else if (message.type === "result") {
        result = message;
      }
      inspectMessage?.(sequence, message);
      options.onMessage?.(sequence, message);
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
  verifyClaudeInit(options, init);

  return {
    sessionId,
    cwd: canonicalPath(init.cwd),
    claudeCodeVersion: init.claude_code_version,
    capabilities: init.capabilities ?? [],
    firstIdentity,
    messageCount,
    resultText: result.result,
    stateTransitions,
  };
}

function messageBoundary(
  sequence: number,
  message: SDKMessage,
): ClaudeMessageBoundary {
  return {
    sequence,
    type: message.type,
    subtype: subtypeOf(message),
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
  options: Pick<
    ClaudeTurnOptions,
    "cwd" | "expectedSessionId" | "queryPolicy"
  >,
  abortController: AbortController,
): Options {
  const policy = queryPolicyOf(options);
  return {
    abortController,
    allowedTools: policy.allowedTools ?? [],
    canUseTool: policy.canUseTool,
    cwd: options.cwd,
    env: buildClaudeEnvironment(process.env),
    maxTurns: policy.maxTurns,
    permissionMode: policy.permissionMode,
    persistSession: true,
    resume: options.expectedSessionId,
    settingSources: [],
    settings: policy.settings,
    tools: policy.tools,
  };
}

function queryPolicyOf(
  options: Pick<ClaudeTurnOptions, "queryPolicy">,
): ClaudeQueryPolicy {
  return (
    options.queryPolicy ?? {
      tools: [],
      permissionMode: "dontAsk",
      maxTurns: 1,
    }
  );
}

function verifyClaudeInit(
  options: Pick<ClaudeTurnOptions, "cwd" | "queryPolicy">,
  init: SDKSystemMessage,
): void {
  if (canonicalPath(init.cwd) !== canonicalPath(options.cwd)) {
    throw new Error(
      `Working-directory mismatch: expected ${options.cwd}, received ${init.cwd}`,
    );
  }
  const policy = queryPolicyOf(options);
  if (init.permissionMode !== policy.permissionMode) {
    throw new Error(
      `Permission mode mismatch: expected ${policy.permissionMode}, received ${init.permissionMode}`,
    );
  }
  if (!sameStringSet(init.tools, policy.tools)) {
    throw new Error(
      `Tool exposure mismatch: expected ${policy.tools.join(", ") || "none"}, received ${init.tools.join(", ") || "none"}`,
    );
  }
}

function sameStringSet(left: string[], right: string[]): boolean {
  return (
    left.length === right.length &&
    left.every((value) => right.includes(value))
  );
}

export function buildClaudeEnvironment(
  inheritedEnvironment: NodeJS.ProcessEnv,
): NodeJS.ProcessEnv {
  return {
    ...inheritedEnvironment,
    CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS: "1",
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
  if (
    message.type === "system" &&
    message.subtype === "session_state_changed"
  ) {
    console.log(
      `← #${sequence} ${label}${identity} state=${message.state}`,
    );
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

function createStreamingPrompt(prompt: string): {
  messages: AsyncIterable<SDKUserMessage>;
  close: () => void;
} {
  let release: (() => void) | undefined;
  let closed = false;
  const closedPromise = new Promise<void>((resolve) => {
    release = resolve;
  });

  return {
    messages: {
      async *[Symbol.asyncIterator]() {
        yield {
          type: "user",
          message: {
            role: "user",
            content: prompt,
          },
          parent_tool_use_id: null,
        };
        await closedPromise;
      },
    },
    close() {
      if (!closed) {
        closed = true;
        release?.();
      }
    },
  };
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
  options.onQuery?.(stream);
  let sequence = 0;
  let emittedResult = false;

  try {
    for await (const message of stream) {
      sequence += 1;
      emittedResult ||= message.type === "result";
      await rawLog.writeFile(`${JSON.stringify(message)}\n`);
      printReadableMessage(sequence, message);
      inspectMessage(sequence, message);
    }
    return sequence;
  } catch (error) {
    stream.close();
    if (options.acceptErrorResult === true && emittedResult) {
      return sequence;
    }
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
