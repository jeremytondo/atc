import { describe, expect, test } from "bun:test";
import { createAppProtocolRuntime } from "@/app/protocol";
import {
  createFakeAgentRegistry,
  createFakeAgentSession,
  createTestAgentTurn,
} from "@/test-support/agents";
import { createSilentLogger } from "@/test-support/logger";
import { createTurnsModule } from "@/turns/turn.handlers";

const logger = createSilentLogger("error");
const connectionId = "connection-1";
const workspace = Object.freeze({
  id: "workspace-1",
  workspacePath: "/tmp/project",
  createdAt: "2026-04-10T09:00:00.000Z",
  lastOpenedAt: "2026-04-10T09:00:00.000Z",
});

describe("createTurnsModule", () => {
  test("binds approval notifications before interrupting a turn", async () => {
    const outbound: string[] = [];
    const runtime = createAppProtocolRuntime({ logger });
    const session = createFakeAgentSession();
    let approvalBindingCalls = 0;

    createTurnsModule({
      logger,
      registerMethod: runtime.registerMethod,
      sendNotification: runtime.sendNotification,
      registry: createFakeAgentRegistry(session),
      getOpenedWorkspace: () => workspace,
      loadedThreads: {
        isThreadLoadedForConnection: () => true,
        listLoadedThreadSubscribers: () => [connectionId],
      },
      ensureApprovalNotificationBinding: async () => {
        approvalBindingCalls += 1;
        return { ok: true, data: undefined };
      },
    });

    await initializeConnection(runtime, outbound);

    await runtime.handleIncomingText({
      connectionId,
      text: JSON.stringify({
        id: "req-turn-start",
        method: "turn/start",
        params: {
          threadId: "thread-1",
          prompt: "Start a turn",
        },
      }),
    });
    approvalBindingCalls = 0;
    outbound.length = 0;

    await runtime.handleIncomingText({
      connectionId,
      text: JSON.stringify({
        id: "req-turn-interrupt",
        method: "turn/interrupt",
        params: {
          threadId: "thread-1",
          turnId: "turn-1",
        },
      }),
    });

    expect(approvalBindingCalls).toBe(1);
    expect(session.interruptTurnCalls).toEqual([
      {
        requestId: "atelier-appserver:turn/interrupt:connection-1:req-turn-interrupt",
        params: {
          threadId: "thread-1",
          turnId: "turn-1",
        },
      },
    ]);
    expect(JSON.parse(outbound[0] ?? "null")).toEqual({
      id: "req-turn-interrupt",
      result: {
        turn: {
          id: "turn-1",
          status: {
            type: "interrupted",
          },
        },
      },
    });
  });

  test("clears the completed turn before running the completion callback", async () => {
    const outbound: string[] = [];
    const runtime = createAppProtocolRuntime({ logger });
    let startTurnCallCount = 0;
    const session = createFakeAgentSession({
      startTurn: async () => {
        startTurnCallCount += 1;
        return {
          ok: true,
          data: {
            turn: createTestAgentTurn({
              id: startTurnCallCount === 1 ? "turn-1" : "turn-2",
            }),
          },
        };
      },
    });

    createTurnsModule({
      logger,
      registerMethod: runtime.registerMethod,
      sendNotification: runtime.sendNotification,
      registry: createFakeAgentRegistry(session),
      getOpenedWorkspace: () => workspace,
      loadedThreads: {
        isThreadLoadedForConnection: () => true,
        listLoadedThreadSubscribers: () => [connectionId],
      },
      onTurnCompleted: async ({ threadId }) => {
        await runtime.handleIncomingText({
          connectionId,
          text: JSON.stringify({
            id: "req-turn-restart",
            method: "turn/start",
            params: {
              threadId,
              prompt: "Start the next turn",
            },
          }),
        });
      },
    });

    await initializeConnection(runtime, outbound);

    await runtime.handleIncomingText({
      connectionId,
      text: JSON.stringify({
        id: "req-turn-start",
        method: "turn/start",
        params: {
          threadId: "thread-1",
          prompt: "Start the first turn",
        },
      }),
    });
    outbound.length = 0;

    session.emitNotification({
      agentId: "codex",
      provider: "codex",
      receivedAt: "2026-04-12T10:00:00.000Z",
      rawMethod: "turn/completed",
      rawPayload: {},
      threadId: "thread-1",
      type: "turn",
      event: "completed",
      turn: {
        id: "turn-1",
        status: {
          type: "completed",
        },
      },
    });
    await waitForCondition(() => startTurnCallCount === 2 && outbound.length === 2);

    expect(startTurnCallCount).toBe(2);
    expect(outbound.map((message) => JSON.parse(message))).toEqual([
      {
        id: "req-turn-restart",
        result: {
          turn: {
            id: "turn-2",
            status: {
              type: "inProgress",
            },
          },
        },
      },
      {
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          turn: {
            id: "turn-1",
            status: {
              type: "completed",
            },
          },
        },
      },
    ]);
  });
});

const initializeConnection = async (
  runtime: ReturnType<typeof createAppProtocolRuntime>,
  outbound: string[],
): Promise<void> => {
  runtime.openConnection({
    connectionId,
    sendText: async (text) => {
      outbound.push(text);
    },
  });

  await runtime.handleIncomingText({
    connectionId,
    text: JSON.stringify({
      id: "req-initialize",
      method: "initialize",
      params: {
        clientInfo: {
          name: "AtelierCode Test",
          version: "0.1.0",
        },
      },
    }),
  });
};

const flushAsyncWork = async (): Promise<void> => {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
};

const waitForCondition = async (
  predicate: () => boolean,
  attempts = 20,
): Promise<void> => {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (predicate()) {
      return;
    }

    await flushAsyncWork();
  }

  throw new Error("Timed out waiting for asynchronous turn handling.");
};
