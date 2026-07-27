import {
  type JsonObject,
  type ProtocolMessage,
  type ServerRequest,
  isObject,
  objectAt,
  stringAt,
} from "./app-server-client.ts";

export function threadIdsFromList(result: JsonObject): string[] {
  return objectIds(result.data, "thread/list response");
}

export function threadIdsFromLoadedList(result: JsonObject): string[] {
  const data = result.data;
  if (!Array.isArray(data) || data.some((value) => typeof value !== "string")) {
    throw new Error("thread/loaded/list response did not include string IDs");
  }
  return [...data];
}

export function verifyNoReplacementThreads(
  before: string[],
  after: string[],
): void {
  const beforeSet = new Set(before);
  const added = after.filter((threadId) => !beforeSet.has(threadId));
  if (added.length > 0) {
    throw new Error(
      `Invalid resume attempt created replacement thread(s): ${added.join(", ")}`,
    );
  }
}

export function verifyThreadHasNoTurns(
  thread: JsonObject,
  context: string,
): void {
  if (!Array.isArray(thread.turns)) {
    throw new Error(`${context} did not include a turns array`);
  }
  if (thread.turns.length !== 0) {
    throw new Error(
      `${context} unexpectedly included ${thread.turns.length} turn(s)`,
    );
  }
}

export function verifyTurnEventAttribution(
  messages: ProtocolMessage[],
  expectedThreadId: string,
  expectedTurnId: string,
  context: string,
): number {
  const attributed = messages.filter((message) => {
    const notificationTurn = objectAt(message.params, "turn");
    return (
      message.params?.turnId === expectedTurnId ||
      stringAt(notificationTurn, "id") === expectedTurnId
    );
  });
  const missingThreadIds = attributed.filter(
    (message) => typeof message.params?.threadId !== "string",
  );
  if (attributed.length === 0) {
    throw new Error(`${context} emitted no turn-attributed notifications`);
  }
  if (missingThreadIds.length > 0) {
    throw new Error(
      `${context} emitted turn event(s) without threadId: ${missingThreadIds
        .map((message) => message.method ?? "unknown")
        .join(", ")}`,
    );
  }

  const mismatches = attributed.filter(
    (message) => message.params?.threadId !== expectedThreadId,
  );
  if (mismatches.length > 0) {
    throw new Error(
      `${context} received event(s) attributed to another thread: ${mismatches
        .map(
          (message) =>
            `${message.method ?? "unknown"}:${String(message.params?.threadId)}`,
        )
        .join(", ")}`,
    );
  }

  for (const requiredMethod of ["turn/started", "turn/completed"]) {
    if (!attributed.some((message) => message.method === requiredMethod)) {
      throw new Error(`${context} did not emit ${requiredMethod}`);
    }
  }
  return attributed.length;
}

export function verifyWaitingStatus(
  message: ProtocolMessage,
  expectedThreadId: string,
  expectedFlag: "waitingOnApproval" | "waitingOnUserInput",
  context: string,
): void {
  if (message.method !== "thread/status/changed") {
    throw new Error(`${context} was not a thread/status/changed event`);
  }
  if (message.params?.threadId !== expectedThreadId) {
    throw new Error(
      `${context} was attributed to ${String(message.params?.threadId)} instead of ${expectedThreadId}`,
    );
  }
  const status = objectAt(message.params, "status");
  if (status?.type !== "active" || !Array.isArray(status.activeFlags)) {
    throw new Error(`${context} did not report an active thread status`);
  }
  if (!status.activeFlags.includes(expectedFlag)) {
    throw new Error(`${context} did not include ${expectedFlag}`);
  }
}

export function verifyServerRequestAttribution(
  request: ServerRequest,
  expectedThreadId: string,
  expectedMethod: string,
  context: string,
): string {
  if (request.method !== expectedMethod) {
    throw new Error(
      `${context} expected ${expectedMethod}, received ${request.method}`,
    );
  }
  if (request.params.threadId !== expectedThreadId) {
    throw new Error(
      `${context} was attributed to ${String(request.params.threadId)} instead of ${expectedThreadId}`,
    );
  }
  return requireString(request.params, "turnId", context);
}

export function verifyInputQuestion(
  request: ServerRequest,
  expectedQuestionId: string,
  expectedLabels: string[],
): void {
  const questions = request.params.questions;
  if (!Array.isArray(questions) || questions.length !== 1) {
    throw new Error(
      `Input request expected exactly one question, received ${Array.isArray(questions) ? questions.length : "no questions array"}`,
    );
  }
  const question = questions[0];
  if (!isObject(question) || question.id !== expectedQuestionId) {
    throw new Error(
      `Input request did not contain question ${expectedQuestionId}`,
    );
  }
  if (!Array.isArray(question.options)) {
    throw new Error("Input request question did not contain options");
  }
  const labels = question.options.map((option) =>
    isObject(option)
      ? requireString(option, "label", "input request option")
      : "",
  );
  if (
    labels.length !== expectedLabels.length ||
    labels.some((label, index) => label !== expectedLabels[index])
  ) {
    throw new Error(
      `Input request option mismatch: expected ${expectedLabels.join(", ")}, received ${labels.join(", ")}`,
    );
  }
}

export function verifyExactPwdApproval(request: ServerRequest): string {
  const command = requireString(
    request.params,
    "command",
    "Codex permission request",
  );
  if (command !== "pwd" && command !== "/bin/zsh -lc pwd") {
    throw new Error(
      `Permission request command mismatch: received ${command}`,
    );
  }
  const actions = request.params.commandActions;
  if (
    !Array.isArray(actions) ||
    actions.length !== 1 ||
    !isObject(actions[0]) ||
    actions[0].command !== "pwd"
  ) {
    throw new Error(
      "Permission request did not contain exactly one structured pwd action",
    );
  }
  return command;
}

export function verifyThreadContainsAgentMarker(
  thread: JsonObject,
  marker: string,
  context: string,
): number {
  const matchingTurnIds = turnIdsContainingAgentMarker(thread, marker);
  if (matchingTurnIds.length === 0) {
    throw new Error(`${context} did not contain agent marker ${marker}`);
  }
  return matchingTurnIds.length;
}

export function turnIdsContainingAgentMarker(
  thread: JsonObject,
  marker: string,
): string[] {
  if (!Array.isArray(thread.turns)) {
    throw new Error("Thread did not include a turns array");
  }

  return thread.turns.flatMap((turn) => {
    if (!isObject(turn) || !Array.isArray(turn.items)) {
      return [];
    }
    const matches = turn.items.some(
      (item) =>
        isObject(item) &&
        item.type === "agentMessage" &&
        typeof item.text === "string" &&
        item.text.includes(marker),
    );
    return matches
      ? [requireString(turn, "id", "thread turn containing marker")]
      : [];
  });
}

export function requireThread(
  result: JsonObject,
  method: string,
): JsonObject {
  const thread = objectAt(result, "thread");
  if (thread === undefined) {
    throw new Error(`${method} response did not include a thread object`);
  }
  return thread;
}

export function requireString(
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

function objectIds(value: unknown, context: string): string[] {
  if (!Array.isArray(value)) {
    throw new Error(`${context} did not include an array`);
  }
  return value.map((item, index) => {
    if (!isObject(item)) {
      throw new Error(`${context} item ${index} was not an object`);
    }
    return requireString(item, "id", `${context} item ${index}`);
  });
}
