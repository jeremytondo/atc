import { describe, expect, test } from "bun:test";
import type { AgentRegistry } from "@/agents/registry";
import { createAppProtocolRuntime } from "@/app/protocol";
import { createApprovalsModule } from "@/approvals/approval.handlers";
import { createFakeAgentRegistry, createFakeAgentSession } from "@/test-support/agents";
import { createSilentLogger } from "@/test-support/logger";

const logger = createSilentLogger("error");
const connectionId = "connection-1";

describe("createApprovalsModule", () => {
  test("maps agent lookup failures to protocol errors", async () => {
    const outbound: string[] = [];
    const runtime = createAppProtocolRuntime({ logger });
    createApprovalsModule({
      logger,
      registerMethod: runtime.registerMethod,
      sendNotification: runtime.sendNotification,
      registry: createAgentNotFoundRegistry(),
      loadedThreads: {
        isThreadLoadedForConnection: () => true,
        listLoadedThreadSubscribers: () => [connectionId],
      },
    });

    await initializeConnection(runtime, outbound);
    outbound.length = 0;

    await runtime.handleIncomingText({
      connectionId,
      text: JSON.stringify({
        id: "req-approval-resolve",
        method: "approval/resolve",
        params: {
          requestId: "approval-1",
          resolution: "approved",
        },
      }),
    });

    expect(JSON.parse(outbound[0] ?? "null")).toEqual({
      id: "req-approval-resolve",
      error: {
        code: -33016,
        message: "Requested agent is not configured",
        data: {
          code: "AGENT_NOT_FOUND",
          agentId: "missing-agent",
        },
      },
    });
  });

  test("emits the resolved notification before returning success", async () => {
    const outbound: string[] = [];
    const runtime = createAppProtocolRuntime({ logger });
    const session = createFakeAgentSession();
    const approvalsModule = createApprovalsModule({
      logger,
      registerMethod: runtime.registerMethod,
      sendNotification: runtime.sendNotification,
      registry: createFakeAgentRegistry(session),
      loadedThreads: {
        isThreadLoadedForConnection: () => true,
        listLoadedThreadSubscribers: () => [connectionId],
      },
    });

    await initializeConnection(runtime, outbound);
    await approvalsModule.ensureNotificationBinding();

    session.emitNotification({
      agentId: "codex",
      provider: "codex",
      receivedAt: "2026-04-12T10:00:00.000Z",
      rawMethod: "item/commandExecution/requestApproval",
      rawPayload: {
        threadId: "thread-1",
      },
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-1",
      type: "approval",
      event: "requested",
      requestId: "approval-1",
      approval: {
        requestId: "approval-1",
        kind: "commandExecution",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-1",
        supportedResolutions: ["approved", "declined", "cancelled"],
        approvalId: null,
        reason: null,
        command: "git status",
        cwd: "/tmp/project",
        commandActions: null,
        rawRequest: {},
      },
    });
    await flushAsyncWork();
    outbound.length = 0;

    await runtime.handleIncomingText({
      connectionId,
      text: JSON.stringify({
        id: "req-approval-resolve",
        method: "approval/resolve",
        params: {
          requestId: "approval-1",
          resolution: "approved",
        },
      }),
    });

    expect(outbound.map((message) => JSON.parse(message))).toEqual([
      {
        method: "approval/resolved",
        params: {
          threadId: "thread-1",
          approval: {
            requestId: "approval-1",
            kind: "commandExecution",
            threadId: "thread-1",
            turnId: "turn-1",
            itemId: "item-1",
            supportedResolutions: ["approved", "declined", "cancelled"],
            approvalId: null,
            reason: null,
            command: "git status",
            cwd: "/tmp/project",
            commandActions: null,
          },
          resolution: "approved",
        },
      },
      {
        id: "req-approval-resolve",
        result: {
          requestId: "approval-1",
          resolution: "approved",
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
};

const createAgentNotFoundRegistry = (): AgentRegistry => ({
  getDefaultAgentId: () => "missing-agent",
  listAgents: () => [],
  getAgent: () => undefined,
  getSession: async () => ({
    ok: false,
    error: {
      type: "agentNotFound",
      agentId: "missing-agent",
      message: 'Agent "missing-agent" is not configured.',
    },
  }),
  disconnectAll: async () => {},
});
