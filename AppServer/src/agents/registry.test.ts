import { describe, expect, test } from "bun:test";
import type { AgentNotification, AgentSession } from "@/agents/contracts";
import { createAgentRegistry } from "@/agents/registry";

describe("createAgentRegistry", () => {
  test("creates sessions lazily and reuses the cached default session", async () => {
    let createCount = 0;
    const session = createFakeSession("codex");
    const registry = createAgentRegistry({
      defaultAgentId: "codex",
      agents: [
        {
          id: "codex",
          provider: "codex",
        },
      ],
      createSession: async () => {
        createCount += 1;
        return {
          ok: true,
          data: session,
        };
      },
    });

    expect(createCount).toBe(0);

    const firstSession = await registry.getSession();
    const secondSession = await registry.getSession("codex");

    expect(firstSession.ok).toBe(true);
    expect(secondSession.ok).toBe(true);
    expect(createCount).toBe(1);
    expect(firstSession.ok && secondSession.ok && firstSession.data === secondSession.data).toBe(
      true,
    );
  });

  test("drops cached sessions after disconnect notifications and recreates them on demand", async () => {
    let createCount = 0;
    const createdSessions: Array<ReturnType<typeof createFakeSession>> = [];
    const registry = createAgentRegistry({
      defaultAgentId: "codex",
      agents: [
        {
          id: "codex",
          provider: "codex",
        },
      ],
      createSession: async () => {
        createCount += 1;
        const session = createFakeSession("codex");
        createdSessions.push(session);
        return {
          ok: true,
          data: session,
        };
      },
    });

    const firstLookup = await registry.getSession();
    expect(firstLookup.ok).toBe(true);
    expect(createCount).toBe(1);

    if (!firstLookup.ok) {
      throw new Error("Expected the first session lookup to succeed.");
    }

    emitDisconnect(createdSessions[0]);

    const secondLookup = await registry.getSession();
    expect(secondLookup.ok).toBe(true);
    expect(createCount).toBe(2);
    expect(firstLookup.ok && secondLookup.ok && firstLookup.data === secondLookup.data).toBe(false);
  });

  test("does not reuse sessions that are disconnecting", async () => {
    let createCount = 0;
    const createdSessions: Array<ReturnType<typeof createFakeSession>> = [];
    const registry = createAgentRegistry({
      defaultAgentId: "codex",
      agents: [
        {
          id: "codex",
          provider: "codex",
        },
      ],
      createSession: async () => {
        createCount += 1;
        const session = createFakeSession("codex");
        createdSessions.push(session);
        return {
          ok: true,
          data: session,
        };
      },
    });

    const firstLookup = await registry.getSession();
    expect(firstLookup.ok).toBe(true);
    createdSessions[0].setState("disconnecting");

    const secondLookup = await registry.getSession();

    expect(secondLookup.ok).toBe(true);
    expect(createCount).toBe(2);
    expect(firstLookup.ok && secondLookup.ok && firstLookup.data === secondLookup.data).toBe(false);
  });

  test("unsubscribes registry lifecycle listeners after disconnect", async () => {
    const session = createFakeSession("codex");
    const registry = createAgentRegistry({
      defaultAgentId: "codex",
      agents: [
        {
          id: "codex",
          provider: "codex",
        },
      ],
      createSession: async () => ({
        ok: true,
        data: session,
      }),
    });

    const firstLookup = await registry.getSession();
    expect(firstLookup.ok).toBe(true);
    expect(session.getListenerCount()).toBe(1);

    emitDisconnect(session);

    expect(session.getListenerCount()).toBe(0);
  });
});

const createFakeSession = (
  agentId: string,
): AgentSession & {
  emit: (notification: AgentNotification) => void;
  getListenerCount: () => number;
  setState: (state: AgentSession["getState"] extends () => infer T ? T : never) => void;
} => {
  let state: AgentSession["getState"] extends () => infer T ? T : never = "ready";
  const listeners = new Set<(notification: AgentNotification) => void>();

  return {
    agentId,
    provider: "codex",
    getState: () => state,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    listModels: async () => ({ ok: true, data: { models: [], nextCursor: null } }),
    listThreads: async () => ({ ok: true, data: { threads: [], nextCursor: null } }),
    startThread: async () => ({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "",
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
          workspacePath: "/tmp/project",
          name: null,
          archived: false,
          status: { type: "idle" },
        },
      },
    }),
    resumeThread: async () => ({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "",
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
          workspacePath: "/tmp/project",
          name: null,
          archived: false,
          status: { type: "idle" },
        },
      },
    }),
    readThread: async () => ({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "",
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
          workspacePath: "/tmp/project",
          name: null,
          archived: false,
          status: { type: "idle" },
          turns: [],
        },
      },
    }),
    forkThread: async () => ({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "",
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
          workspacePath: "/tmp/project",
          name: null,
          archived: false,
          status: { type: "idle" },
        },
      },
    }),
    archiveThread: async () => ({
      ok: true,
      data: {},
    }),
    unarchiveThread: async () => ({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "",
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
          workspacePath: "/tmp/project",
          name: null,
          archived: false,
          status: { type: "idle" },
        },
      },
    }),
    setThreadName: async () => ({
      ok: true,
      data: {},
    }),
    startTurn: async () => ({
      ok: true,
      data: { turn: { id: "turn-1", status: { type: "inProgress" } } },
    }),
    steerTurn: async () => ({
      ok: true,
      data: { turn: { id: "turn-1", status: { type: "inProgress" } } },
    }),
    interruptTurn: async () => ({
      ok: true,
      data: { turn: { id: "turn-1", status: { type: "interrupted" } } },
    }),
    resolveApproval: async (params) => ({
      ok: true,
      data: { requestId: params.requestId, resolution: params.resolution },
    }),
    disconnect: async () => {
      state = "disconnected";
    },
    getListenerCount: () => listeners.size,
    setState: (nextState) => {
      state = nextState;
    },
    emit: (notification) => {
      if (notification.type === "disconnect") {
        state = "disconnected";
      }

      for (const listener of listeners) {
        listener(notification);
      }
    },
  };
};

const emitDisconnect = (session: ReturnType<typeof createFakeSession>): void => {
  session.emit({
    agentId: session.agentId,
    provider: session.provider,
    receivedAt: new Date().toISOString(),
    rawMethod: "disconnect",
    type: "disconnect",
    reason: "process_exited",
    message: "The provider exited.",
  });
};
