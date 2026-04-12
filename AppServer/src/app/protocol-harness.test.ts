import { afterEach, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { AgentAdapter } from "@/agents";
import { createAgentsModule } from "@/agents";
import { createCodexAgentAdapter } from "@/agents/codex-adapter";
import { createLogger } from "@/app/logger";
import {
  APP_SERVER_USER_AGENT,
  createAppProtocolRuntime,
  createAppTransportComponent,
} from "@/app/protocol";
import { type AppServer, createAppServer, type SignalRegistrar } from "@/app/server";
import { createApprovalsModulePlaceholder } from "@/approvals";
import { type AppDatabase, createStoreBootstrap } from "@/core/store";
import {
  createFakeAgentAdapter,
  createFakeAgentSession,
  createTestAgentItem,
  createTestAgentModel,
  createTestAgentThread,
  createTestAgentThreadDetail,
  createTestAgentTurn,
  createTestAgentTurnDetail,
} from "@/test-support/agents";
import { getAvailablePort } from "@/test-support/network";
import { createSqliteThreadsStore, createThreadsModule, type ThreadsStore } from "@/threads";
import { createTurnsModule } from "@/turns";
import { createSqliteWorkspacesStore, createWorkspacesModule } from "@/workspaces";

const runningServers: AppServer[] = [];
const temporaryDirectories: string[] = [];

afterEach(async () => {
  while (runningServers.length > 0) {
    const server = runningServers.pop();

    if (server === undefined) {
      continue;
    }

    try {
      await server.stop("test-cleanup");
    } catch {
      // Ignore cleanup failures so the original test error stays visible.
    }
  }

  while (temporaryDirectories.length > 0) {
    const directory = temporaryDirectories.pop();

    if (directory === undefined) {
      continue;
    }

    await rm(directory, { force: true, recursive: true });
  }
});

describe("App Server protocol harness", () => {
  test("initializes over a real websocket connection without an extra initialized notification", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        id: "req-1",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode Test",
            version: "0.1.0",
          },
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-1",
        result: {
          userAgent: APP_SERVER_USER_AGENT,
        },
      });
      await expect(client.nextMessage(150)).rejects.toThrow("Timed out waiting for message");
    } finally {
      await client.close();
    }
  });

  test("maps invalid json to a parse error", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendText("{");

      await expect(client.nextMessage()).resolves.toEqual({
        id: null,
        error: {
          code: -32700,
          message: "Parse error",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps invalid envelopes to invalid request", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        method: 123,
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: null,
        error: {
          code: -32600,
          message: "Invalid request",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps unknown methods to method not found", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        id: "req-unknown",
        method: "thread/unknown",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-unknown",
        error: {
          code: -32601,
          message: "Method not found",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps invalid initialize params to invalid params", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        id: "req-invalid-params",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode Test",
          },
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-invalid-params",
        error: {
          code: -32602,
          message: "Invalid params",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("rejects duplicate initialize requests on the same connection", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      const initializeRequest = {
        id: "req-initialize-1",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode Test",
            version: "0.1.0",
          },
        },
      };

      client.sendJson(initializeRequest);
      await client.nextMessage();

      client.sendJson({
        ...initializeRequest,
        id: "req-initialize-2",
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-initialize-2",
        error: {
          code: -33000,
          message: "Session already initialized",
          data: {
            code: "SESSION_ALREADY_INITIALIZED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("returns models after initialize", async () => {
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listModelsResult: {
            ok: true,
            data: {
              models: [
                createTestAgentModel({
                  id: "gpt-5.4",
                  model: "gpt-5.4",
                  displayName: "GPT-5.4",
                  isDefault: true,
                }),
              ],
              nextCursor: "cursor-2",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {
          limit: 20,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        result: {
          models: [
            {
              id: "gpt-5.4",
              model: "gpt-5.4",
              displayName: "GPT-5.4",
              hidden: false,
              defaultReasoningEffort: "medium",
              supportedReasoningEfforts: [
                {
                  reasoningEffort: "medium",
                  description: "Balanced",
                },
              ],
              inputModalities: ["text"],
              supportsPersonality: true,
              isDefault: true,
            },
          ],
          nextCursor: "cursor-2",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps protocol request IDs to agent request IDs before calling the adapter", async () => {
    const session = createFakeAgentSession({
      listModels: async () => ({
        ok: true,
        data: {
          models: [createTestAgentModel()],
          nextCursor: null,
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {},
      });

      await client.nextMessage();

      expect(session.listModelsCalls).toHaveLength(1);
      expect(session.listModelsCalls[0]?.requestId).not.toBe("req-model-list");
      expect(session.listModelsCalls[0]?.requestId).toEqual(
        expect.stringMatching(/^atelier-appserver:model\/list:.*:req-model-list$/),
      );
    } finally {
      await client.close();
    }
  });

  test("rejects model/list before initialize", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        error: {
          code: -33001,
          message: "Session not initialized",
          data: {
            code: "SESSION_NOT_INITIALIZED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps malformed model/list params to invalid params", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {
          limit: "twenty",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        error: {
          code: -32602,
          message: "Invalid params",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("filters hidden models by default", async () => {
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listModelsResult: {
            ok: true,
            data: {
              models: [
                createTestAgentModel(),
                createTestAgentModel({
                  id: "gpt-5.4-hidden",
                  model: "gpt-5.4-hidden",
                  displayName: "GPT-5.4 Hidden",
                  hidden: true,
                  defaultReasoningEffort: "high",
                  supportsPersonality: false,
                }),
              ],
              nextCursor: null,
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        result: {
          models: [createTestAgentModel()],
          nextCursor: null,
        },
      });
    } finally {
      await client.close();
    }
  });

  test("includes hidden models when requested", async () => {
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listModelsResult: {
            ok: true,
            data: {
              models: [
                createTestAgentModel(),
                createTestAgentModel({
                  id: "gpt-5.4-hidden",
                  model: "gpt-5.4-hidden",
                  displayName: "GPT-5.4 Hidden",
                  hidden: true,
                  defaultReasoningEffort: "high",
                  supportsPersonality: false,
                }),
              ],
              nextCursor: null,
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {
          includeHidden: true,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        result: {
          models: [
            createTestAgentModel(),
            createTestAgentModel({
              id: "gpt-5.4-hidden",
              model: "gpt-5.4-hidden",
              displayName: "GPT-5.4 Hidden",
              hidden: true,
              defaultReasoningEffort: "high",
              supportsPersonality: false,
            }),
          ],
          nextCursor: null,
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps agent session startup failures to agent session unavailable", async () => {
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          createSessionResult: {
            ok: false,
            error: {
              type: "sessionUnavailable",
              agentId: "codex",
              provider: "codex",
              code: "startupFailed",
              message: "Codex failed to start.",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        error: {
          code: -33004,
          message: "Agent session unavailable",
          data: {
            code: "AGENT_SESSION_UNAVAILABLE",
            agentId: "codex",
            provider: "codex",
            reason: "startupFailed",
            message: "Codex failed to start.",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps provider remote errors to provider error", async () => {
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listModelsResult: {
            ok: false,
            error: {
              type: "remoteError",
              agentId: "codex",
              provider: "codex",
              requestId: "req-model-list",
              code: 418,
              message: "Provider said no.",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-model-list",
        method: "model/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-model-list",
        error: {
          code: -33005,
          message: "Provider error",
          data: {
            code: "PROVIDER_ERROR",
            agentId: "codex",
            provider: "codex",
            providerCode: "418",
            providerMessage: "Provider said no.",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("opens a workspace after initialize", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      const workspacePath = await createWorkspaceDirectory();
      const canonicalWorkspacePath = await realpath(workspacePath);

      client.sendJson({
        id: "req-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode Test",
            version: "0.1.0",
          },
        },
      });
      await client.nextMessage();

      client.sendJson({
        id: "req-workspace-open",
        method: "workspace/open",
        params: {
          workspacePath,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-workspace-open",
        result: {
          workspace: {
            id: expect.any(String),
            workspacePath: canonicalWorkspacePath,
            createdAt: expect.any(String),
            lastOpenedAt: expect.any(String),
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("rejects workspace/open before initialize", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      const workspacePath = await createWorkspaceDirectory();

      client.sendJson({
        id: "req-workspace-open",
        method: "workspace/open",
        params: {
          workspacePath,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-workspace-open",
        error: {
          code: -33001,
          message: "Session not initialized",
          data: {
            code: "SESSION_NOT_INITIALIZED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps malformed workspace/open params to invalid params", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        id: "req-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode Test",
            version: "0.1.0",
          },
        },
      });
      await client.nextMessage();

      client.sendJson({
        id: "req-workspace-open",
        method: "workspace/open",
        params: {
          workspacePath: 123,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-workspace-open",
        error: {
          code: -32602,
          message: "Invalid params",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("returns the same workspace identity for repeat opens of the same canonical directory", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      const workspacePath = await createWorkspaceDirectory();
      const canonicalWorkspacePath = await realpath(workspacePath);
      const aliasWorkspacePath = join(workspacePath, ".");

      client.sendJson({
        id: "req-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode Test",
            version: "0.1.0",
          },
        },
      });
      await client.nextMessage();

      client.sendJson({
        id: "req-workspace-open-1",
        method: "workspace/open",
        params: {
          workspacePath,
        },
      });
      const firstResponse = (await client.nextMessage()) as {
        readonly result: {
          readonly workspace: {
            readonly id: string;
            readonly workspacePath: string;
          };
        };
      };

      client.sendJson({
        id: "req-workspace-open-2",
        method: "workspace/open",
        params: {
          workspacePath: aliasWorkspacePath,
        },
      });
      const secondResponse = (await client.nextMessage()) as typeof firstResponse;

      expect(firstResponse.result.workspace.workspacePath).toBe(canonicalWorkspacePath);
      expect(secondResponse.result.workspace.workspacePath).toBe(canonicalWorkspacePath);
      expect(secondResponse.result.workspace.id).toBe(firstResponse.result.workspace.id);
    } finally {
      await client.close();
    }
  });

  test("starts a thread, returns resolved defaults, and emits thread/started", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const session = createFakeAgentSession({
      startThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-started",
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-start",
        method: "thread/start",
        params: {},
      });

      const messages = [await client.nextMessage(), await client.nextMessage()];
      expect(messages).toContainEqual({
        id: "req-thread-start",
        result: {
          thread: {
            id: "thread-started",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: null,
            archived: false,
            model: "gpt-5.4",
            reasoningEffort: "medium",
            status: {
              type: "idle",
            },
          },
        },
      });
      expect(messages).toContainEqual({
        method: "thread/started",
        params: {
          thread: {
            id: "thread-started",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: null,
            archived: false,
            model: "gpt-5.4",
            reasoningEffort: "medium",
            status: {
              type: "idle",
            },
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("thread/start still succeeds when bookkeeping writes fail and keeps loaded-thread forwarding", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const session = createFakeAgentSession({
      startThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-best-effort",
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
      threadsStoreFactory: (getDatabase) =>
        createFailingUpsertThreadsStore(createSqliteThreadsStore(getDatabase)),
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-start",
        method: "thread/start",
        params: {},
      });

      const messages = [await client.nextMessage(), await client.nextMessage()];
      expect(messages).toContainEqual({
        id: "req-thread-start",
        result: {
          thread: expect.objectContaining({
            id: "thread-best-effort",
            model: "gpt-5.4",
            reasoningEffort: "medium",
          }),
        },
      });
      expect(messages).toContainEqual({
        method: "thread/started",
        params: {
          thread: expect.objectContaining({
            id: "thread-best-effort",
          }),
        },
      });

      session.emitNotification(
        createThreadStatusChangedNotification({
          threadId: "thread-best-effort",
          status: { type: "active", activeFlags: ["running"] },
        }),
      );

      await expect(client.nextMessage()).resolves.toEqual({
        method: "thread/status/changed",
        params: {
          threadId: "thread-best-effort",
          status: {
            type: "active",
            activeFlags: ["running"],
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("thread/read does not subscribe the connection to live thread notifications", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const session = createFakeAgentSession({
      readThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: "thread-read-only",
            workspacePath: canonicalWorkspacePath,
          }),
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "thread-read-only",
          includeTurns: false,
        },
      });
      await client.nextMessage();

      session.emitNotification(
        createThreadStatusChangedNotification({
          threadId: "thread-read-only",
          status: { type: "active", activeFlags: ["running"] },
        }),
      );

      await expect(client.nextMessage(150)).rejects.toThrow("Timed out waiting for message");
    } finally {
      await client.close();
    }
  });

  test("forks a thread, returns configured thread data, and subscribes the forked thread", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const session = createFakeAgentSession({
      readThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
        },
      }),
      forkThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-forked",
            workspacePath: canonicalWorkspacePath,
            name: "Forked thread",
          }),
          model: params.model ?? "gpt-5.4-mini",
          reasoningEffort: "high",
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-fork",
        method: "thread/fork",
        params: {
          threadId: "thread-source",
          model: "gpt-5.4-mini",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-fork",
        result: {
          thread: {
            id: "thread-forked",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: "Forked thread",
            archived: false,
            model: "gpt-5.4-mini",
            reasoningEffort: "high",
            status: {
              type: "idle",
            },
          },
        },
      });

      session.emitNotification(
        createThreadStatusChangedNotification({
          threadId: "thread-forked",
          status: { type: "active", activeFlags: ["running"] },
        }),
      );

      await expect(client.nextMessage()).resolves.toEqual({
        method: "thread/status/changed",
        params: {
          threadId: "thread-forked",
          status: {
            type: "active",
            activeFlags: ["running"],
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("fans out loaded-thread status notifications only to subscribed connections and cleans up on close", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const clientA = await connectProtocolClient(harness.port);
    const clientB = await connectProtocolClient(harness.port);
    const bystander = await connectProtocolClient(harness.port);

    try {
      await initializeClient(clientA);
      await initializeClient(clientB);
      await initializeClient(bystander);
      await openWorkspaceClient(clientA, workspacePath);
      await openWorkspaceClient(clientB, workspacePath);
      await openWorkspaceClient(bystander, workspacePath);

      clientA.sendJson({
        id: "req-thread-resume-a",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      clientB.sendJson({
        id: "req-thread-resume-b",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });

      await clientA.nextMessage();
      await clientB.nextMessage();

      session.emitNotification(
        createThreadStatusChangedNotification({
          threadId: "thread-live",
          status: { type: "active", activeFlags: ["running"] },
        }),
      );

      await expect(clientA.nextMessage()).resolves.toEqual({
        method: "thread/status/changed",
        params: {
          threadId: "thread-live",
          status: {
            type: "active",
            activeFlags: ["running"],
          },
        },
      });
      await expect(clientB.nextMessage()).resolves.toEqual({
        method: "thread/status/changed",
        params: {
          threadId: "thread-live",
          status: {
            type: "active",
            activeFlags: ["running"],
          },
        },
      });
      await expect(bystander.nextMessage(150)).rejects.toThrow("Timed out waiting for message");

      await clientA.close();
      session.emitNotification(
        createThreadStatusChangedNotification({
          threadId: "thread-live",
          status: { type: "idle" },
        }),
      );

      await expect(clientB.nextMessage()).resolves.toEqual({
        method: "thread/status/changed",
        params: {
          threadId: "thread-live",
          status: {
            type: "idle",
          },
        },
      });
      await expect(bystander.nextMessage(150)).rejects.toThrow("Timed out waiting for message");
    } finally {
      await clientB.close();
      await bystander.close();
    }
  });

  test("fans out loaded-thread mutation notifications only to subscribed connections", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const clientA = await connectProtocolClient(harness.port);
    const clientB = await connectProtocolClient(harness.port);
    const bystander = await connectProtocolClient(harness.port);

    try {
      await initializeClient(clientA);
      await initializeClient(clientB);
      await initializeClient(bystander);
      await openWorkspaceClient(clientA, workspacePath);
      await openWorkspaceClient(clientB, workspacePath);
      await openWorkspaceClient(bystander, workspacePath);

      clientA.sendJson({
        id: "req-thread-resume-a",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      clientB.sendJson({
        id: "req-thread-resume-b",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });

      await clientA.nextMessage();
      await clientB.nextMessage();

      session.emitNotification(
        createThreadArchivedNotification({
          threadId: "thread-live",
        }),
      );

      await expect(clientA.nextMessage()).resolves.toEqual({
        method: "thread/archived",
        params: {
          threadId: "thread-live",
        },
      });
      await expect(clientB.nextMessage()).resolves.toEqual({
        method: "thread/archived",
        params: {
          threadId: "thread-live",
        },
      });
      await expect(bystander.nextMessage(150)).rejects.toThrow("Timed out waiting for message");

      session.emitNotification(
        createThreadNameUpdatedNotification({
          threadId: "thread-live",
          threadName: "Renamed thread",
        }),
      );

      await expect(clientA.nextMessage()).resolves.toEqual({
        method: "thread/name/updated",
        params: {
          threadId: "thread-live",
          threadName: "Renamed thread",
        },
      });
      await expect(clientB.nextMessage()).resolves.toEqual({
        method: "thread/name/updated",
        params: {
          threadId: "thread-live",
          threadName: "Renamed thread",
        },
      });
      await expect(bystander.nextMessage(150)).rejects.toThrow("Timed out waiting for message");
    } finally {
      await clientA.close();
      await clientB.close();
      await bystander.close();
    }
  });

  test("rejects turn/start when the thread is not loaded for the calling connection", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const session = createFakeAgentSession();
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-turn-start",
        method: "turn/start",
        params: {
          threadId: "thread-not-loaded",
          prompt: "Try to start a turn",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-turn-start",
        error: {
          code: -33010,
          message: "Thread is not loaded for this connection",
          data: {
            code: "THREAD_NOT_LOADED",
            threadId: "thread-not-loaded",
          },
        },
      });
      expect(session.startTurnCalls).toHaveLength(0);
    } finally {
      await client.close();
    }
  });

  test("rejects starting a second active turn on the same thread", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    let turnCounter = 0;
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
      startTurn: async () => {
        turnCounter += 1;

        return {
          ok: true,
          data: {
            turn: createTestAgentTurn({
              id: `turn-${turnCounter}`,
            }),
          },
        };
      },
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-resume",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      await client.nextMessage();

      client.sendJson({
        id: "req-turn-start-1",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Start the first turn",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-turn-start-1",
        result: {
          turn: {
            id: "turn-1",
            status: {
              type: "inProgress",
            },
          },
        },
      });

      client.sendJson({
        id: "req-turn-start-2",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Start another turn",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-turn-start-2",
        error: {
          code: -33011,
          message: "Thread already has an active turn",
          data: {
            code: "TURN_ALREADY_ACTIVE",
            threadId: "thread-live",
            activeTurnId: "turn-1",
          },
        },
      });
      expect(session.startTurnCalls).toHaveLength(1);
    } finally {
      await client.close();
    }
  });

  test("fans out turn and item notifications in order only to subscribed clients and clears active turns on completion", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    let turnCounter = 0;
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
      startTurn: async () => {
        turnCounter += 1;

        return {
          ok: true,
          data: {
            turn: createTestAgentTurn({
              id: `turn-${turnCounter}`,
            }),
          },
        };
      },
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const clientA = await connectProtocolClient(harness.port);
    const clientB = await connectProtocolClient(harness.port);
    const bystander = await connectProtocolClient(harness.port);

    try {
      await initializeClient(clientA);
      await initializeClient(clientB);
      await initializeClient(bystander);
      await openWorkspaceClient(clientA, workspacePath);
      await openWorkspaceClient(clientB, workspacePath);
      await openWorkspaceClient(bystander, workspacePath);

      clientA.sendJson({
        id: "req-thread-resume-a",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      clientB.sendJson({
        id: "req-thread-resume-b",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });

      await clientA.nextMessage();
      await clientB.nextMessage();

      clientA.sendJson({
        id: "req-turn-start",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Ship the event stream",
        },
      });

      await expect(clientA.nextMessage()).resolves.toEqual({
        id: "req-turn-start",
        result: {
          turn: {
            id: "turn-1",
            status: {
              type: "inProgress",
            },
          },
        },
      });

      const streamedNotifications = [
        createTurnStartedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
        }),
        createPlanUpdatedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
        }),
        createDiffUpdatedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
        }),
        createItemStartedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-message",
          itemType: "agentMessage",
        }),
        createMessageDeltaNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-message",
          delta: "Working on it...",
        }),
        createItemCompletedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-message",
          item: buildHarnessItem("agentMessage", "item-message", "completed"),
        }),
        createItemStartedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-reasoning",
          itemType: "reasoning",
        }),
        createReasoningTextDeltaNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-reasoning",
          delta: "Thinking...",
        }),
        createReasoningSummaryTextDeltaNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-reasoning",
          delta: "Short summary",
        }),
        createItemCompletedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-reasoning",
          item: buildHarnessItem("reasoning", "item-reasoning", "completed"),
        }),
        createItemStartedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-command",
          itemType: "commandExecution",
        }),
        createCommandOutputDeltaNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-command",
          delta: "stdout line\n",
        }),
        createItemCompletedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-command",
          item: buildHarnessItem("commandExecution", "item-command", "completed"),
        }),
        createItemStartedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-tool",
          itemType: "mcpToolCall",
        }),
        createToolProgressNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-tool",
          message: "Fetching results",
        }),
        createItemCompletedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-tool",
          item: buildHarnessItem("mcpToolCall", "item-tool", "completed"),
        }),
        createTurnCompletedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
        }),
      ] as const;

      for (const notification of streamedNotifications) {
        session.emitNotification(notification);
      }

      const clientAMessages = await Promise.all(
        streamedNotifications.map(() => clientA.nextMessage()),
      );
      const clientBMessages = await Promise.all(
        streamedNotifications.map(() => clientB.nextMessage()),
      );

      expect(clientAMessages).toEqual([
        {
          method: "turn/started",
          params: {
            threadId: "thread-live",
            turn: {
              id: "turn-1",
              status: {
                type: "inProgress",
              },
            },
          },
        },
        {
          method: "turn/plan/updated",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            explanation: "Ship it",
            steps: [
              {
                step: "Inspect state",
                status: "completed",
              },
              {
                step: "Write patch",
                status: "in_progress",
              },
            ],
          },
        },
        {
          method: "turn/diff/updated",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            diff: ["diff --git a/src/example.ts b/src/example.ts", "+added"].join("\n"),
            summary: [
              {
                path: "src/example.ts",
                additions: 1,
                deletions: 0,
              },
            ],
          },
        },
        {
          method: "item/started",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("agentMessage", "item-message", "started"),
          },
        },
        {
          method: "item/message/textDelta",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-message",
            delta: "Working on it...",
          },
        },
        {
          method: "item/completed",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("agentMessage", "item-message", "completed"),
          },
        },
        {
          method: "item/started",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("reasoning", "item-reasoning", "started"),
          },
        },
        {
          method: "item/reasoning/textDelta",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-reasoning",
            delta: "Thinking...",
          },
        },
        {
          method: "item/reasoning/summaryTextDelta",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-reasoning",
            delta: "Short summary",
          },
        },
        {
          method: "item/completed",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("reasoning", "item-reasoning", "completed"),
          },
        },
        {
          method: "item/started",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("commandExecution", "item-command", "started"),
          },
        },
        {
          method: "item/commandExecution/outputDelta",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-command",
            delta: "stdout line\n",
          },
        },
        {
          method: "item/completed",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("commandExecution", "item-command", "completed"),
          },
        },
        {
          method: "item/started",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("mcpToolCall", "item-tool", "started"),
          },
        },
        {
          method: "item/tool/progress",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-tool",
            message: "Fetching results",
          },
        },
        {
          method: "item/completed",
          params: {
            threadId: "thread-live",
            turnId: "turn-1",
            item: buildHarnessItem("mcpToolCall", "item-tool", "completed"),
          },
        },
        {
          method: "turn/completed",
          params: {
            threadId: "thread-live",
            turn: {
              id: "turn-1",
              status: {
                type: "completed",
              },
            },
          },
        },
      ]);
      expect(clientBMessages).toEqual(clientAMessages);
      await expect(bystander.nextMessage(150)).rejects.toThrow("Timed out waiting for message");

      clientA.sendJson({
        id: "req-turn-start-2",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Start another turn after completion",
        },
      });

      await expect(clientA.nextMessage()).resolves.toEqual({
        id: "req-turn-start-2",
        result: {
          turn: {
            id: "turn-2",
            status: {
              type: "inProgress",
            },
          },
        },
      });
    } finally {
      await clientA.close();
      await clientB.close();
      await bystander.close();
    }
  });

  test("preserves terminal turn status on turn/completed and allows a follow-up turn", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    let turnCounter = 0;
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
      startTurn: async () => {
        turnCounter += 1;

        return {
          ok: true,
          data: {
            turn: createTestAgentTurn({
              id: `turn-${turnCounter}`,
            }),
          },
        };
      },
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-resume",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      await client.nextMessage();

      client.sendJson({
        id: "req-turn-start-1",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Start a turn that will fail",
        },
      });
      await client.nextMessage();

      session.emitNotification(
        createTurnCompletedNotification({
          threadId: "thread-live",
          turnId: "turn-1",
          status: {
            type: "failed",
            message: "provider failed",
          },
        }),
      );

      await expect(client.nextMessage()).resolves.toEqual({
        method: "turn/completed",
        params: {
          threadId: "thread-live",
          turn: {
            id: "turn-1",
            status: {
              type: "failed",
              message: "provider failed",
            },
          },
        },
      });

      client.sendJson({
        id: "req-turn-start-2",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Start the follow-up turn",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-turn-start-2",
        result: {
          turn: {
            id: "turn-2",
            status: {
              type: "inProgress",
            },
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("preserves pre-response turn notifications during the turn/start reservation window", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    let session!: ReturnType<typeof createFakeAgentSession>;
    session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
      startTurn: async () => {
        session.emitNotification(
          createTurnStartedNotification({
            threadId: "thread-live",
            turnId: "turn-1",
          }),
        );
        session.emitNotification(
          createItemStartedNotification({
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-message",
            itemType: "agentMessage",
          }),
        );
        session.emitNotification(
          createMessageDeltaNotification({
            threadId: "thread-live",
            turnId: "turn-1",
            itemId: "item-message",
            delta: "Working on it...",
          }),
        );

        return {
          ok: true,
          data: {
            turn: createTestAgentTurn({
              id: "turn-1",
            }),
          },
        };
      },
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-resume",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      await client.nextMessage();

      client.sendJson({
        id: "req-turn-start",
        method: "turn/start",
        params: {
          threadId: "thread-live",
          prompt: "Start a turn",
        },
      });

      const messages = await Promise.all([
        client.nextMessage(),
        client.nextMessage(),
        client.nextMessage(),
        client.nextMessage(),
      ]);

      expect(messages[0]).toEqual({
        method: "turn/started",
        params: {
          threadId: "thread-live",
          turn: {
            id: "turn-1",
            status: {
              type: "inProgress",
            },
          },
        },
      });
      expect(messages).toContainEqual({
        method: "item/started",
        params: {
          threadId: "thread-live",
          turnId: "turn-1",
          item: buildHarnessItem("agentMessage", "item-message", "started"),
        },
      });
      expect(messages).toContainEqual({
        method: "item/message/textDelta",
        params: {
          threadId: "thread-live",
          turnId: "turn-1",
          itemId: "item-message",
          delta: "Working on it...",
        },
      });
      expect(messages).toContainEqual({
        id: "req-turn-start",
        result: {
          turn: {
            id: "turn-1",
            status: {
              type: "inProgress",
            },
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("clears loaded-thread subscriptions when the connection switches workspaces", async () => {
    const firstWorkspacePath = await createWorkspaceDirectory();
    const secondWorkspacePath = await createWorkspaceDirectory();
    const canonicalFirstWorkspacePath = await realpath(firstWorkspacePath);
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: canonicalFirstWorkspacePath,
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, firstWorkspacePath);

      client.sendJson({
        id: "req-thread-resume",
        method: "thread/resume",
        params: {
          threadId: "thread-live",
        },
      });
      await client.nextMessage();

      await openWorkspaceClient(client, secondWorkspacePath);

      session.emitNotification(
        createThreadStatusChangedNotification({
          threadId: "thread-live",
          status: { type: "active", activeFlags: ["running"] },
        }),
      );

      await expect(client.nextMessage(150)).rejects.toThrow("Timed out waiting for message");
    } finally {
      await client.close();
    }
  });

  test("keeps thread/read archive projections consistent after archive and unarchive mutations", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    let archived = false;
    const session = createFakeAgentSession({
      readThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: params.threadId,
            workspacePath: canonicalWorkspacePath,
            archived,
            name: archived ? "Archived thread" : "Live thread",
          }),
        },
      }),
      archiveThread: async () => {
        archived = true;
        return {
          ok: true,
          data: {},
        };
      },
      unarchiveThread: async (_requestId, params) => {
        archived = false;
        return {
          ok: true,
          data: {
            thread: createTestAgentThread({
              id: params.threadId,
              workspacePath: canonicalWorkspacePath,
              archived: false,
              name: "Live thread",
            }),
          },
        };
      },
    });
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeAgentAdapter({
          session,
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-archive",
        method: "thread/archive",
        params: {
          threadId: "thread-1",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-archive",
        result: {},
      });

      client.sendJson({
        id: "req-thread-read-archived",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read-archived",
        result: {
          thread: {
            id: "thread-1",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: "Archived thread",
            archived: true,
            model: null,
            reasoningEffort: null,
            status: {
              type: "idle",
            },
            turns: [],
          },
        },
      });

      client.sendJson({
        id: "req-thread-unarchive",
        method: "thread/unarchive",
        params: {
          threadId: "thread-1",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-unarchive",
        result: {
          thread: {
            id: "thread-1",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: "Live thread",
            archived: false,
            model: null,
            reasoningEffort: null,
            status: {
              type: "idle",
            },
          },
        },
      });

      client.sendJson({
        id: "req-thread-read-live",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read-live",
        result: {
          thread: {
            id: "thread-1",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: "Live thread",
            archived: false,
            model: null,
            reasoningEffort: null,
            status: {
              type: "idle",
            },
            turns: [],
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("lists threads after initialize and workspace/open", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listThreadsResult: {
            ok: true,
            data: {
              threads: [
                createTestAgentThread({
                  id: "thread-1",
                  preview: "Ship thread browsing",
                  createdAt: "2026-04-10T10:00:00.000Z",
                  updatedAt: "2026-04-10T11:00:00.000Z",
                  workspacePath: canonicalWorkspacePath,
                  name: "Thread browsing",
                  archived: false,
                  status: { type: "active", activeFlags: ["running"] },
                }),
              ],
              nextCursor: "cursor-2",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {
          limit: 20,
          archived: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        result: {
          threads: [
            {
              id: "thread-1",
              preview: "Ship thread browsing",
              createdAt: "2026-04-10T10:00:00.000Z",
              updatedAt: "2026-04-10T11:00:00.000Z",
              name: "Thread browsing",
              archived: false,
              model: null,
              reasoningEffort: null,
              status: {
                type: "active",
                activeFlags: ["running"],
              },
            },
          ],
          nextCursor: "cursor-2",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("thread/list still succeeds when bookkeeping writes fail", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listThreadsResult: {
            ok: true,
            data: {
              threads: [
                createTestAgentThread({
                  id: "thread-best-effort",
                  workspacePath: canonicalWorkspacePath,
                }),
              ],
              nextCursor: null,
            },
          },
        }),
      ],
      threadsStoreFactory: (getDatabase) =>
        createFailingUpsertThreadsStore(createSqliteThreadsStore(getDatabase)),
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        result: {
          threads: [
            {
              id: "thread-best-effort",
              preview: "Thread preview",
              createdAt: "2026-04-10T10:00:00.000Z",
              updatedAt: "2026-04-10T11:00:00.000Z",
              name: null,
              archived: false,
              model: null,
              reasoningEffort: null,
              status: {
                type: "idle",
              },
            },
          ],
          nextCursor: null,
        },
      });
    } finally {
      await client.close();
    }
  });

  test("rejects thread/list before initialize", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        error: {
          code: -33001,
          message: "Session not initialized",
          data: {
            code: "SESSION_NOT_INITIALIZED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("rejects thread/list before workspace/open", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        error: {
          code: -33006,
          message: "Workspace not opened",
          data: {
            code: "WORKSPACE_NOT_OPENED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps malformed thread/list params to invalid params", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {
          limit: "twenty",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        error: {
          code: -32602,
          message: "Invalid params",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("preserves archive filtering and nextCursor on thread/list", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listThreadsResult: {
            ok: true,
            data: {
              threads: [
                createTestAgentThread({
                  id: "thread-archived",
                  workspacePath: canonicalWorkspacePath,
                  archived: true,
                }),
              ],
              nextCursor: "archived-cursor",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {
          archived: true,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        result: {
          threads: [
            {
              id: "thread-archived",
              preview: "Thread preview",
              createdAt: "2026-04-10T10:00:00.000Z",
              updatedAt: "2026-04-10T11:00:00.000Z",
              name: null,
              archived: true,
              model: null,
              reasoningEffort: null,
              status: {
                type: "idle",
              },
            },
          ],
          nextCursor: "archived-cursor",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("reads thread metadata after initialize and workspace/open", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          readThreadResult: {
            ok: true,
            data: {
              thread: createTestAgentThreadDetail({
                id: "thread-1",
                preview: "Read me",
                workspacePath: canonicalWorkspacePath,
                name: "Readable thread",
                status: { type: "systemError", message: "Provider disconnected" },
              }),
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read",
        result: {
          thread: {
            id: "thread-1",
            preview: "Read me",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: "Readable thread",
            archived: false,
            model: null,
            reasoningEffort: null,
            status: {
              type: "systemError",
              message: "Provider disconnected",
            },
            turns: [],
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("rejects thread/read before workspace/open", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read",
        error: {
          code: -33006,
          message: "Workspace not opened",
          data: {
            code: "WORKSPACE_NOT_OPENED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps malformed thread/read params to invalid params", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: 123,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read",
        error: {
          code: -32602,
          message: "Invalid params",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("rejects thread/archive before workspace/open", async () => {
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);

      client.sendJson({
        id: "req-thread-archive",
        method: "thread/archive",
        params: {
          threadId: "thread-1",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-archive",
        error: {
          code: -33006,
          message: "Workspace not opened",
          data: {
            code: "WORKSPACE_NOT_OPENED",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps malformed thread/name/set params to invalid params", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const harness = await createProtocolHarness();
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-name",
        method: "thread/name/set",
        params: {
          threadId: "thread-1",
          name: "",
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-name",
        error: {
          code: -32602,
          message: "Invalid params",
        },
      });
    } finally {
      await client.close();
    }
  });

  test("returns turn history for thread/read includeTurns=true", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const canonicalWorkspacePath = await realpath(workspacePath);
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          readThreadResult: {
            ok: true,
            data: {
              thread: createTestAgentThreadDetail({
                id: "thread-1",
                preview: "Read me",
                workspacePath: canonicalWorkspacePath,
                turns: [
                  createTestAgentTurnDetail({
                    id: "turn-1",
                    items: [createTestAgentItem({ id: "item-1", text: "History item" })],
                  }),
                ],
              }),
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: true,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read",
        result: {
          thread: {
            id: "thread-1",
            preview: "Read me",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: null,
            archived: false,
            model: null,
            reasoningEffort: null,
            status: {
              type: "idle",
            },
            turns: [
              {
                id: "turn-1",
                status: {
                  type: "completed",
                },
                items: [
                  {
                    id: "item-1",
                    type: "agentMessage",
                    text: "History item",
                    phase: null,
                  },
                ],
                error: null,
              },
            ],
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps provider not-found behavior through the existing provider error path", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          readThreadResult: {
            ok: false,
            error: {
              type: "remoteError",
              agentId: "codex",
              provider: "codex",
              requestId: "req-thread-read",
              code: 404,
              message: "Thread not found",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "missing-thread",
          includeTurns: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read",
        error: {
          code: -33005,
          message: "Provider error",
          data: {
            code: "PROVIDER_ERROR",
            agentId: "codex",
            provider: "codex",
            providerCode: "404",
            providerMessage: "Thread not found",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("maps malformed thread/list provider payloads to a stable protocol error", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          listThreadsResult: {
            ok: false,
            error: {
              type: "invalidProviderMessage",
              agentId: "codex",
              provider: "codex",
              message: "Malformed thread list payload.",
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-list",
        method: "thread/list",
        params: {},
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-list",
        error: {
          code: -33009,
          message: "Provider returned an invalid payload",
          data: {
            code: "INVALID_PROVIDER_PAYLOAD",
            agentId: "codex",
            provider: "codex",
            operation: "thread/list",
            providerMessage: "Malformed thread list payload.",
          },
        },
      });
    } finally {
      await client.close();
    }
  });

  test("returns a workspace mismatch error when thread/read crosses workspace boundaries", async () => {
    const workspacePath = await createWorkspaceDirectory();
    const harness = await createProtocolHarness({
      agentAdapters: [
        createFakeProtocolAgentAdapter({
          readThreadResult: {
            ok: true,
            data: {
              thread: createTestAgentThreadDetail({
                id: "thread-1",
                workspacePath: "/tmp/other-project",
              }),
            },
          },
        }),
      ],
    });
    const client = await connectProtocolClient(harness.port);

    try {
      await initializeClient(client);
      await openWorkspaceClient(client, workspacePath);

      client.sendJson({
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      });

      await expect(client.nextMessage()).resolves.toEqual({
        id: "req-thread-read",
        error: {
          code: -33008,
          message: "Thread does not belong to the opened workspace",
          data: {
            code: "THREAD_WORKSPACE_MISMATCH",
            threadId: "thread-1",
            openedWorkspacePath: await realpath(workspacePath),
            threadWorkspacePath: "/tmp/other-project",
          },
        },
      });
    } finally {
      await client.close();
    }
  });
});

type ProtocolTestClient = Readonly<{
  sendText: (text: string) => void;
  sendJson: (value: unknown) => void;
  nextMessage: (timeoutMs?: number) => Promise<unknown>;
  close: () => Promise<void>;
}>;

const createProtocolHarness = async (
  options: Readonly<{
    agentAdapters?: readonly AgentAdapter[];
    threadsStoreFactory?: (getDatabase: () => AppDatabase) => ThreadsStore;
  }> = {},
): Promise<Readonly<{ port: number }>> => {
  const port = await getAvailablePort();
  const configDirectory = await createTemporaryDirectory("atelier-appserver-protocol-");
  const config = {
    configPath: join(configDirectory, "appserver.config.json"),
    port,
    databasePath: "./var/test.sqlite",
    logLevel: "info" as const,
    agents: createTestAgentsConfig(),
  };
  const logger = createLogger({
    level: "error",
    write: () => {},
  });
  const appProtocolRuntime = createAppProtocolRuntime({
    logger,
  });
  const storeBootstrap = createStoreBootstrap({
    config,
    logger: logger.withContext({ component: "core.store" }),
  });
  let threadsModule: ReturnType<typeof createThreadsModule> | undefined;
  const workspacesModule = createWorkspacesModule({
    logger: logger.withContext({ component: "module.workspaces" }),
    registerMethod: appProtocolRuntime.registerMethod,
    store: createSqliteWorkspacesStore(storeBootstrap.getDatabase),
    onWorkspaceOpened: ({ connectionId, previousWorkspace, workspace }) => {
      threadsModule?.handleWorkspaceOpened({
        connectionId,
        previousWorkspace,
        workspace,
      });
    },
  });
  const agentsModule = createAgentsModule({
    config: config.agents,
    logger: logger.withContext({ component: "module.agents" }),
    adapters: options.agentAdapters ?? [
      createCodexAgentAdapter({
        logger: logger.withContext({ component: "agents.codex" }),
      }),
    ],
    registerMethod: appProtocolRuntime.registerMethod,
  });
  threadsModule = createThreadsModule({
    logger: logger.withContext({ component: "module.threads" }),
    registerMethod: appProtocolRuntime.registerMethod,
    sendNotification: appProtocolRuntime.sendNotification,
    registry: agentsModule.registry,
    store:
      options.threadsStoreFactory?.(storeBootstrap.getDatabase) ??
      createSqliteThreadsStore(storeBootstrap.getDatabase),
    getOpenedWorkspace: workspacesModule.getOpenedWorkspace,
  });
  const finalizedThreadsModule = threadsModule;
  const turnsModule = createTurnsModule({
    logger: logger.withContext({ component: "module.turns" }),
    registerMethod: appProtocolRuntime.registerMethod,
    sendNotification: appProtocolRuntime.sendNotification,
    registry: agentsModule.registry,
    getOpenedWorkspace: workspacesModule.getOpenedWorkspace,
    loadedThreads: {
      isThreadLoadedForConnection: finalizedThreadsModule.isThreadLoadedForConnection,
      listLoadedThreadSubscribers: finalizedThreadsModule.listLoadedThreadSubscribers,
    },
  });
  const transportComponent = createAppTransportComponent({
    config,
    logger,
    protocol: appProtocolRuntime,
    onConnectionClosed: [
      ({ connectionId }) => {
        finalizedThreadsModule.handleConnectionClosed(connectionId);
        workspacesModule.handleConnectionClosed(connectionId);
      },
    ],
  });
  const server = await createAppServer({
    config,
    logger,
    signalRegistrar: createSignalRegistrar(),
    components: [
      appProtocolRuntime.protocolComponent,
      storeBootstrap.lifecycle,
      agentsModule.lifecycle,
      workspacesModule.lifecycle,
      finalizedThreadsModule.lifecycle,
      turnsModule.lifecycle,
      createApprovalsModulePlaceholder(),
      transportComponent,
    ],
  });

  await server.start();
  runningServers.push(server);

  return Object.freeze({ port });
};

const createTestAgentsConfig = () => ({
  defaultAgent: "codex",
  enabled: [
    {
      id: "codex",
      provider: "codex" as const,
    },
  ],
});

const createFailingUpsertThreadsStore = (store: ThreadsStore): ThreadsStore =>
  Object.freeze({
    getWorkspaceThreadLink: store.getWorkspaceThreadLink,
    listWorkspaceThreadLinks: store.listWorkspaceThreadLinks,
    upsertWorkspaceThreadLinks: async () => {
      throw new Error("bookkeeping write failed");
    },
  });

const createWorkspaceDirectory = async (): Promise<string> => {
  const rootDirectory = await createTemporaryDirectory("atelier-appserver-workspace-");
  const workspacePath = join(rootDirectory, "workspace");

  await mkdir(workspacePath, { recursive: true });

  return workspacePath;
};

const initializeClient = async (client: ProtocolTestClient): Promise<void> => {
  client.sendJson({
    id: "req-initialize",
    method: "initialize",
    params: {
      clientInfo: {
        name: "AtelierCode Test",
        version: "0.1.0",
      },
    },
  });

  await expect(client.nextMessage()).resolves.toEqual({
    id: "req-initialize",
    result: {
      userAgent: APP_SERVER_USER_AGENT,
    },
  });
};

const openWorkspaceClient = async (
  client: ProtocolTestClient,
  workspacePath: string,
): Promise<void> => {
  client.sendJson({
    id: "req-workspace-open",
    method: "workspace/open",
    params: {
      workspacePath,
    },
  });

  await expect(client.nextMessage()).resolves.toMatchObject({
    id: "req-workspace-open",
    result: {
      workspace: {
        id: expect.any(String),
        workspacePath: await realpath(workspacePath),
      },
    },
  });
};

const connectProtocolClient = async (port: number): Promise<ProtocolTestClient> => {
  const socket = new WebSocket(`ws://127.0.0.1:${port}/`);
  const bufferedMessages: unknown[] = [];
  const pendingMessages: Array<{
    resolve: (message: unknown) => void;
    reject: (error: Error) => void;
    timer: ReturnType<typeof setTimeout>;
  }> = [];

  socket.addEventListener("message", (event) => {
    const text = toMessageText(event.data);
    const message = JSON.parse(text) as unknown;
    const nextMessage = pendingMessages.shift();

    if (nextMessage === undefined) {
      bufferedMessages.push(message);
      return;
    }

    clearTimeout(nextMessage.timer);
    nextMessage.resolve(message);
  });

  socket.addEventListener("close", () => {
    while (pendingMessages.length > 0) {
      const pendingMessage = pendingMessages.shift();

      if (pendingMessage === undefined) {
        continue;
      }

      clearTimeout(pendingMessage.timer);
      pendingMessage.reject(new Error("WebSocket closed before a message arrived"));
    }
  });

  await waitForSocketOpen(socket);

  return Object.freeze({
    sendText: (text) => {
      socket.send(text);
    },
    sendJson: (value) => {
      socket.send(JSON.stringify(value));
    },
    nextMessage: (timeoutMs = 1_000) => {
      const bufferedMessage = bufferedMessages.shift();

      if (bufferedMessage !== undefined) {
        return Promise.resolve(bufferedMessage);
      }

      return new Promise<unknown>((resolve, reject) => {
        const timer = setTimeout(() => {
          reject(new Error("Timed out waiting for message"));
        }, timeoutMs);

        pendingMessages.push({
          resolve,
          reject,
          timer,
        });
      });
    },
    close: async () => {
      if (socket.readyState === WebSocket.CLOSED) {
        return;
      }

      const closePromise = new Promise<void>((resolve) => {
        socket.addEventListener(
          "close",
          () => {
            resolve();
          },
          { once: true },
        );
      });

      socket.close();
      await closePromise;
    },
  });
};

const waitForSocketOpen = async (socket: WebSocket): Promise<void> => {
  if (socket.readyState === WebSocket.OPEN) {
    return;
  }

  await new Promise<void>((resolve, reject) => {
    const onOpen = () => {
      cleanup();
      resolve();
    };
    const onError = () => {
      cleanup();
      reject(new Error("WebSocket failed to open"));
    };
    const cleanup = () => {
      socket.removeEventListener("open", onOpen);
      socket.removeEventListener("error", onError);
    };

    socket.addEventListener("open", onOpen, { once: true });
    socket.addEventListener("error", onError, { once: true });
  });
};

const createFakeProtocolAgentAdapter = (options: {
  createSessionResult?:
    | Readonly<{
        ok: true;
        data: ReturnType<typeof createFakeAgentSession>;
      }>
    | Readonly<{
        ok: false;
        error: {
          type: "sessionUnavailable";
          agentId: string;
          provider: "codex";
          code: "executableMissing" | "startupFailed" | "disconnected";
          message: string;
        };
      }>;
  listModelsResult?:
    | Readonly<{
        ok: true;
        data: {
          models: readonly ReturnType<typeof createTestAgentModel>[];
          nextCursor: string | null;
        };
      }>
    | Readonly<{
        ok: false;
        error:
          | Readonly<{
              type: "remoteError";
              agentId: string;
              provider: "codex";
              requestId: string | number;
              code: number;
              message: string;
            }>
          | Readonly<{
              type: "invalidProviderMessage";
              agentId: string;
              provider: "codex";
              message: string;
            }>;
      }>;
  listThreadsResult?:
    | Readonly<{
        ok: true;
        data: {
          threads: readonly ReturnType<typeof createTestAgentThread>[];
          nextCursor: string | null;
        };
      }>
    | Readonly<{
        ok: false;
        error:
          | Readonly<{
              type: "remoteError";
              agentId: string;
              provider: "codex";
              requestId: string | number;
              code: number;
              message: string;
            }>
          | Readonly<{
              type: "invalidProviderMessage";
              agentId: string;
              provider: "codex";
              message: string;
            }>;
      }>;
  readThreadResult?:
    | Readonly<{
        ok: true;
        data: {
          thread: ReturnType<typeof createTestAgentThreadDetail>;
        };
      }>
    | Readonly<{
        ok: false;
        error:
          | Readonly<{
              type: "remoteError";
              agentId: string;
              provider: "codex";
              requestId: string | number;
              code: number;
              message: string;
            }>
          | Readonly<{
              type: "invalidProviderMessage";
              agentId: string;
              provider: "codex";
              message: string;
            }>;
      }>;
}): AgentAdapter =>
  createFakeAgentAdapter({
    createSessionResult: options.createSessionResult,
    session: createFakeAgentSession({
      listModels: async () =>
        options.listModelsResult ?? {
          ok: true,
          data: {
            models: [createTestAgentModel()],
            nextCursor: null,
          },
        },
      listThreads: async () =>
        options.listThreadsResult ?? {
          ok: true,
          data: {
            threads: [createTestAgentThread()],
            nextCursor: null,
          },
        },
      readThread: async () =>
        options.readThreadResult ?? {
          ok: true,
          data: {
            thread: createTestAgentThreadDetail(),
          },
        },
    }),
  });

const createThreadStatusChangedNotification = (input: {
  threadId: string;
  status: Readonly<{ type: "idle" }> | Readonly<{ type: "active"; activeFlags: readonly string[] }>;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "thread/status/changed",
  threadId: input.threadId,
  type: "thread" as const,
  event: "statusChanged" as const,
  thread: {
    id: input.threadId,
    preview: "",
    updatedAt: "2026-04-10T12:00:00.000Z",
    name: null,
    archived: false,
    status: input.status,
  },
});

const createThreadArchivedNotification = (input: { threadId: string }) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "thread/archived",
  threadId: input.threadId,
  type: "thread" as const,
  event: "archived" as const,
  thread: {
    id: input.threadId,
    preview: "",
    updatedAt: "2026-04-10T12:00:00.000Z",
    name: null,
    archived: true,
    status: { type: "notLoaded" } as const,
  },
});

const createThreadNameUpdatedNotification = (input: {
  threadId: string;
  threadName: string | null;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "thread/name/updated",
  threadId: input.threadId,
  type: "thread" as const,
  event: "nameUpdated" as const,
  threadName: input.threadName,
  thread: {
    id: input.threadId,
    preview: "",
    updatedAt: "2026-04-10T12:00:00.000Z",
    name: input.threadName,
    archived: false,
    status: { type: "notLoaded" } as const,
  },
});

const createTurnStartedNotification = (input: { threadId: string; turnId: string }) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "turn/started",
  threadId: input.threadId,
  turnId: input.turnId,
  type: "turn" as const,
  event: "started" as const,
  turn: {
    id: input.turnId,
    status: { type: "inProgress" } as const,
  },
});

const createTurnCompletedNotification = (input: {
  threadId: string;
  turnId: string;
  status?:
    | { type: "completed" }
    | { type: "failed"; message: string }
    | { type: "cancelled" }
    | { type: "interrupted" };
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "turn/completed",
  threadId: input.threadId,
  turnId: input.turnId,
  type: "turn" as const,
  event: "completed" as const,
  turn: {
    id: input.turnId,
    status: input.status ?? ({ type: "completed" } as const),
  },
});

const createPlanUpdatedNotification = (input: { threadId: string; turnId: string }) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "turn/plan/updated",
  threadId: input.threadId,
  turnId: input.turnId,
  type: "plan" as const,
  event: "updated" as const,
  explanation: "Ship it",
  steps: [
    {
      step: "Inspect state",
      status: "completed" as const,
    },
    {
      step: "Write patch",
      status: "in_progress" as const,
    },
  ],
});

const createDiffUpdatedNotification = (input: { threadId: string; turnId: string }) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "turn/diff/updated",
  threadId: input.threadId,
  turnId: input.turnId,
  type: "diff" as const,
  event: "updated" as const,
  diff: ["diff --git a/src/example.ts b/src/example.ts", "+added"].join("\n"),
  summary: [
    {
      path: "src/example.ts",
      additions: 1,
      deletions: 0,
    },
  ],
});

const createItemStartedNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  itemType: "agentMessage" | "reasoning" | "commandExecution" | "mcpToolCall";
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/started",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "item" as const,
  event: "started" as const,
  item: buildHarnessItem(input.itemType, input.itemId, "started"),
});

const createItemCompletedNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  item: ReturnType<typeof buildHarnessItem>;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/completed",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "item" as const,
  event: "completed" as const,
  item: input.item,
});

const createMessageDeltaNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  delta: string;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/agentMessage/delta",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "message" as const,
  event: "textDelta" as const,
  delta: input.delta,
});

const createReasoningTextDeltaNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  delta: string;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/reasoning/textDelta",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "reasoning" as const,
  event: "textDelta" as const,
  delta: input.delta,
});

const createReasoningSummaryTextDeltaNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  delta: string;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/reasoning/summaryTextDelta",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "reasoning" as const,
  event: "summaryTextDelta" as const,
  delta: input.delta,
});

const createCommandOutputDeltaNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  delta: string;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/commandExecution/outputDelta",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "command" as const,
  event: "outputDelta" as const,
  delta: input.delta,
});

const createToolProgressNotification = (input: {
  threadId: string;
  turnId: string;
  itemId: string;
  message: string;
}) => ({
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
  rawMethod: "item/mcpToolCall/progress",
  threadId: input.threadId,
  turnId: input.turnId,
  itemId: input.itemId,
  type: "tool" as const,
  event: "progress" as const,
  message: input.message,
});

const buildHarnessItem = (
  itemType: "agentMessage" | "reasoning" | "commandExecution" | "mcpToolCall",
  itemId: string,
  lifecycle: "started" | "completed",
) => {
  switch (itemType) {
    case "agentMessage":
      return {
        id: itemId,
        type: "agentMessage" as const,
        text: lifecycle === "completed" ? "Working on it..." : "",
        phase: null,
      };
    case "reasoning":
      return {
        id: itemId,
        type: "reasoning" as const,
        summary: lifecycle === "completed" ? ["Short summary"] : [],
        content: lifecycle === "completed" ? ["Thinking..."] : [],
      };
    case "commandExecution": {
      const status: "completed" | "inProgress" =
        lifecycle === "completed" ? "completed" : "inProgress";
      return {
        id: itemId,
        type: "commandExecution" as const,
        command: "echo hello",
        cwd: "/tmp/project",
        processId: null,
        status,
        commandActions: [],
        aggregatedOutput: lifecycle === "completed" ? "stdout line\n" : null,
        exitCode: lifecycle === "completed" ? 0 : null,
        durationMs: null,
      };
    }
    case "mcpToolCall": {
      const status: "completed" | "inProgress" =
        lifecycle === "completed" ? "completed" : "inProgress";
      return {
        id: itemId,
        type: "mcpToolCall" as const,
        server: "filesystem",
        tool: "read_file",
        status,
        arguments: {},
        result: null,
        error: null,
        durationMs: null,
      };
    }
  }
};

const toMessageText = (data: unknown): string => {
  if (typeof data === "string") {
    return data;
  }

  if (data instanceof ArrayBuffer) {
    return new TextDecoder().decode(data);
  }

  if (ArrayBuffer.isView(data)) {
    return new TextDecoder().decode(data);
  }

  return String(data);
};

const createSignalRegistrar = (): SignalRegistrar =>
  Object.freeze({
    subscribe: () => () => {},
  });

const createTemporaryDirectory = async (prefix: string): Promise<string> => {
  const directory = await mkdtemp(join(tmpdir(), prefix));
  temporaryDirectories.push(directory);
  return directory;
};
