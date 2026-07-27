import {
  type JsonObject,
  type ProtocolMessage,
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
  context: string,
): number {
  const attributed = messages.filter(
    (message) => typeof message.params?.threadId === "string",
  );
  if (attributed.length === 0) {
    throw new Error(`${context} emitted no thread-attributed notifications`);
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

export function verifyThreadContainsAgentMarker(
  thread: JsonObject,
  marker: string,
  context: string,
): number {
  if (!Array.isArray(thread.turns)) {
    throw new Error(`${context} did not include a turns array`);
  }

  const matchingTurns = thread.turns.filter((turn) => {
    if (!isObject(turn) || !Array.isArray(turn.items)) {
      return false;
    }
    return turn.items.some(
      (item) =>
        isObject(item) &&
        item.type === "agentMessage" &&
        typeof item.text === "string" &&
        item.text.includes(marker),
    );
  });
  if (matchingTurns.length === 0) {
    throw new Error(`${context} did not contain agent marker ${marker}`);
  }
  return matchingTurns.length;
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
