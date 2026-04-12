import { err, ok, type Result } from "@/core/shared";
import type { Turn, TurnItem } from "@/turns/schemas";

export type ActiveTurnConflictError = Readonly<{
  type: "activeTurnConflict";
  threadId: string;
  activeTurnId?: string;
  message: string;
}>;

export type ActiveTurnItemState = Readonly<{
  item?: TurnItem;
  messageText: string;
  reasoningText: string;
  reasoningSummaryText: string;
  commandOutput: string;
  toolProgress: readonly string[];
}>;

export type ActiveTurnSnapshot = Readonly<{
  threadId: string;
  turn?: Turn;
  items: readonly ActiveTurnItemState[];
}>;

export type ActiveTurnRegistry = Readonly<{
  reserveThread: (
    threadId: string,
  ) => Result<Readonly<{ release: () => void }>, ActiveTurnConflictError>;
  startTurn: (input: Readonly<{ threadId: string; turn: Turn }>) => boolean;
  recordTurnCompleted: (input: Readonly<{ threadId: string; turn: Turn }>) => boolean;
  recordItemStarted: (
    input: Readonly<{ threadId: string; turnId: string; item: TurnItem }>,
  ) => boolean;
  recordItemCompleted: (
    input: Readonly<{ threadId: string; turnId: string; item: TurnItem }>,
  ) => boolean;
  appendMessageText: (
    input: Readonly<{ threadId: string; turnId: string; itemId: string; delta: string }>,
  ) => boolean;
  appendReasoningText: (
    input: Readonly<{ threadId: string; turnId: string; itemId: string; delta: string }>,
  ) => boolean;
  appendReasoningSummaryText: (
    input: Readonly<{ threadId: string; turnId: string; itemId: string; delta: string }>,
  ) => boolean;
  appendCommandOutput: (
    input: Readonly<{ threadId: string; turnId: string; itemId: string; delta: string }>,
  ) => boolean;
  appendToolProgress: (
    input: Readonly<{ threadId: string; turnId: string; itemId: string; message: string }>,
  ) => boolean;
  getActiveTurn: (threadId: string) => ActiveTurnSnapshot | undefined;
  clearThread: (threadId: string) => ActiveTurnSnapshot | undefined;
  clearAll: () => void;
}>;

type MutableActiveTurnItemState = {
  item?: TurnItem;
  messageText: string;
  reasoningText: string;
  reasoningSummaryText: string;
  commandOutput: string;
  toolProgress: string[];
};

type MutableActiveTurnState = {
  turn?: Turn;
  itemsById: Map<string, MutableActiveTurnItemState>;
};

export const createActiveTurnRegistry = (): ActiveTurnRegistry => {
  const statesByThreadId = new Map<string, MutableActiveTurnState>();

  const getOrCreateThreadState = (threadId: string): MutableActiveTurnState => {
    const existing = statesByThreadId.get(threadId);

    if (existing !== undefined) {
      return existing;
    }

    const state: MutableActiveTurnState = {
      itemsById: new Map(),
    };
    statesByThreadId.set(threadId, state);
    return state;
  };

  const ensureObservedTurn = (
    threadId: string,
    turnId: string,
  ): MutableActiveTurnState | undefined => {
    const state = getOrCreateThreadState(threadId);

    if (state.turn === undefined) {
      state.turn = Object.freeze({
        id: turnId,
        status: Object.freeze({ type: "inProgress" as const }),
      });
      return state;
    }

    if (state.turn.id !== turnId) {
      return undefined;
    }

    return state;
  };

  const getOrCreateItemState = (
    threadId: string,
    turnId: string,
    itemId: string,
  ): MutableActiveTurnItemState | undefined => {
    const state = ensureObservedTurn(threadId, turnId);

    if (state === undefined) {
      return undefined;
    }

    const existing = state.itemsById.get(itemId);

    if (existing !== undefined) {
      return existing;
    }

    const itemState: MutableActiveTurnItemState = {
      messageText: "",
      reasoningText: "",
      reasoningSummaryText: "",
      commandOutput: "",
      toolProgress: [],
    };
    state.itemsById.set(itemId, itemState);
    return itemState;
  };

  const toSnapshot = (threadId: string, state: MutableActiveTurnState): ActiveTurnSnapshot =>
    Object.freeze({
      threadId,
      ...(state.turn !== undefined ? { turn: state.turn } : {}),
      items: [...state.itemsById.values()].map((item) =>
        Object.freeze({
          ...(item.item !== undefined ? { item: item.item } : {}),
          messageText: item.messageText,
          reasoningText: item.reasoningText,
          reasoningSummaryText: item.reasoningSummaryText,
          commandOutput: item.commandOutput,
          toolProgress: Object.freeze([...item.toolProgress]),
        }),
      ),
    });

  return Object.freeze({
    reserveThread: (threadId) => {
      const existing = statesByThreadId.get(threadId);

      if (existing !== undefined) {
        return err({
          type: "activeTurnConflict",
          threadId,
          ...(existing.turn !== undefined ? { activeTurnId: existing.turn.id } : {}),
          message: "Thread already has an active turn.",
        });
      }

      const state: MutableActiveTurnState = {
        itemsById: new Map(),
      };
      statesByThreadId.set(threadId, state);

      return ok(
        Object.freeze({
          release: () => {
            const current = statesByThreadId.get(threadId);

            if (current === state && current.turn === undefined) {
              statesByThreadId.delete(threadId);
            }
          },
        }),
      );
    },
    startTurn: ({ threadId, turn }) => {
      const state = getOrCreateThreadState(threadId);
      if (state.turn !== undefined && state.turn.id !== turn.id) {
        return false;
      }

      state.turn = turn;
      return true;
    },
    recordTurnCompleted: ({ threadId, turn }) => {
      const state = ensureObservedTurn(threadId, turn.id);
      if (state === undefined) {
        return false;
      }

      state.turn = turn;
      return true;
    },
    recordItemStarted: ({ threadId, turnId, item }) => {
      const itemState = getOrCreateItemState(threadId, turnId, item.id);
      if (itemState === undefined) {
        return false;
      }

      itemState.item = item;
      return true;
    },
    recordItemCompleted: ({ threadId, turnId, item }) => {
      const itemState = getOrCreateItemState(threadId, turnId, item.id);
      if (itemState === undefined) {
        return false;
      }

      itemState.item = item;
      reconcileCompletedItemState(itemState, item);
      return true;
    },
    appendMessageText: ({ threadId, turnId, itemId, delta }) => {
      const itemState = getOrCreateItemState(threadId, turnId, itemId);
      if (itemState === undefined) {
        return false;
      }

      itemState.messageText += delta;
      if (itemState.item?.type === "agentMessage") {
        itemState.item = Object.freeze({
          ...itemState.item,
          text: itemState.messageText,
        });
      }
      return true;
    },
    appendReasoningText: ({ threadId, turnId, itemId, delta }) => {
      const itemState = getOrCreateItemState(threadId, turnId, itemId);
      if (itemState === undefined) {
        return false;
      }

      itemState.reasoningText += delta;
      if (itemState.item?.type === "reasoning") {
        itemState.item = Object.freeze({
          ...itemState.item,
          content: [itemState.reasoningText],
        });
      }
      return true;
    },
    appendReasoningSummaryText: ({ threadId, turnId, itemId, delta }) => {
      const itemState = getOrCreateItemState(threadId, turnId, itemId);
      if (itemState === undefined) {
        return false;
      }

      itemState.reasoningSummaryText += delta;
      if (itemState.item?.type === "reasoning") {
        itemState.item = Object.freeze({
          ...itemState.item,
          summary: [itemState.reasoningSummaryText],
        });
      }
      return true;
    },
    appendCommandOutput: ({ threadId, turnId, itemId, delta }) => {
      const itemState = getOrCreateItemState(threadId, turnId, itemId);
      if (itemState === undefined) {
        return false;
      }

      itemState.commandOutput += delta;
      if (itemState.item?.type === "commandExecution") {
        itemState.item = Object.freeze({
          ...itemState.item,
          aggregatedOutput: itemState.commandOutput,
        });
      }
      return true;
    },
    appendToolProgress: ({ threadId, turnId, itemId, message }) => {
      const itemState = getOrCreateItemState(threadId, turnId, itemId);
      if (itemState === undefined) {
        return false;
      }

      itemState.toolProgress.push(message);
      return true;
    },
    getActiveTurn: (threadId) => {
      const state = statesByThreadId.get(threadId);
      return state === undefined ? undefined : toSnapshot(threadId, state);
    },
    clearThread: (threadId) => {
      const state = statesByThreadId.get(threadId);

      if (state === undefined) {
        return undefined;
      }

      statesByThreadId.delete(threadId);
      return toSnapshot(threadId, state);
    },
    clearAll: () => {
      statesByThreadId.clear();
    },
  });
};

const reconcileCompletedItemState = (
  itemState: MutableActiveTurnItemState,
  item: TurnItem,
): void => {
  switch (item.type) {
    case "agentMessage":
      itemState.messageText = item.text;
      return;
    case "reasoning":
      itemState.reasoningText = item.content.join("");
      itemState.reasoningSummaryText = item.summary.join("");
      return;
    case "commandExecution":
      itemState.commandOutput = item.aggregatedOutput ?? "";
      return;
    default:
      return;
  }
};
