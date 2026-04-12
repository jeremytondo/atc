import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { createCodexAgentSession } from "@/agents/codex-adapter/session";
import {
  type CodexTransport,
  CodexTransportError,
  type CodexTransportEvent,
  type CodexTransportNotification,
  type CodexTransportRequest,
  type CodexTransportResponse,
} from "@/agents/codex-adapter/transport";
import type { AgentNotification } from "@/agents/contracts";
import { BaseEnvironmentResolver } from "@/agents/environment";
import { createSilentLogger } from "@/test-support/logger";

const temporaryDirectories: string[] = [];

afterEach(() => {
  while (temporaryDirectories.length > 0) {
    const directory = temporaryDirectories.pop();
    if (directory !== undefined) {
      rmSync(directory, { recursive: true, force: true });
    }
  }
});

describe("createCodexAgentSession", () => {
  test("connects once, initializes once, and maps model/list results", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        data: [
          {
            id: "gpt-5.4",
            model: "gpt-5.4",
            upgrade: null,
            upgradeInfo: null,
            availabilityNux: null,
            displayName: "GPT-5.4",
            description: "Latest model",
            hidden: false,
            supportedReasoningEfforts: [
              {
                reasoningEffort: "high",
                description: "More reasoning",
              },
            ],
            defaultReasoningEffort: "medium",
            inputModalities: ["text"],
            supportsPersonality: true,
            isDefault: true,
          },
        ],
        nextCursor: null,
      },
      {
        data: [
          {
            id: "gpt-5.4-hidden",
            model: "gpt-5.4-hidden",
            upgrade: null,
            upgradeInfo: null,
            availabilityNux: null,
            displayName: "GPT-5.4 Hidden",
            description: "Hidden model",
            hidden: true,
            supportedReasoningEfforts: [
              {
                reasoningEffort: "high",
                description: "More reasoning",
              },
            ],
            defaultReasoningEffort: "high",
            inputModalities: ["text"],
            supportsPersonality: false,
          },
        ],
        nextCursor: "cursor-2",
      },
    ]);

    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const firstModels = await sessionResult.data.listModels("req-models-1", {
      limit: 20,
      includeHidden: false,
    });
    const secondModels = await sessionResult.data.listModels("req-models-2", {
      includeHidden: true,
    });

    expect(transport.connectCount).toBe(1);
    expect(transport.sent).toEqual([
      {
        id: "atelier-appserver-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode App Server",
            title: null,
            version: "0.1.0",
          },
          capabilities: {
            experimentalApi: true,
          },
        },
      },
      {
        id: "req-models-1",
        method: "model/list",
        params: {
          limit: 20,
          includeHidden: undefined,
        },
      },
      {
        id: "req-models-2",
        method: "model/list",
        params: {
          limit: undefined,
          includeHidden: true,
        },
      },
    ]);
    expect(transport.notified).toEqual([{ method: "initialized" }]);
    expect(firstModels).toEqual({
      ok: true,
      data: {
        models: [
          {
            id: "gpt-5.4",
            model: "gpt-5.4",
            displayName: "GPT-5.4",
            hidden: false,
            defaultReasoningEffort: "medium",
            supportedReasoningEfforts: [
              {
                reasoningEffort: "high",
                description: "More reasoning",
              },
            ],
            inputModalities: ["text"],
            supportsPersonality: true,
            isDefault: true,
          },
        ],
        nextCursor: null,
      },
    });
    expect(secondModels).toEqual({
      ok: true,
      data: {
        models: [
          {
            id: "gpt-5.4-hidden",
            model: "gpt-5.4-hidden",
            displayName: "GPT-5.4 Hidden",
            hidden: true,
            defaultReasoningEffort: "high",
            supportedReasoningEfforts: [
              {
                reasoningEffort: "high",
                description: "More reasoning",
              },
            ],
            inputModalities: ["text"],
            supportsPersonality: false,
            isDefault: false,
          },
        ],
        nextCursor: "cursor-2",
      },
    });
  });

  test("maps thread/list results with nextCursor preservation and status normalization", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        data: [
          {
            id: "thread-1",
            preview: "Ship thread browsing",
            ephemeral: false,
            modelProvider: "openai",
            createdAt: 1_744_280_000,
            updatedAt: 1_744_283_600,
            status: {
              type: "active",
              activeFlags: ["running", "awaiting_approval"],
            },
            path: null,
            cwd: "/tmp/project",
            cliVersion: "0.114.0",
            source: "app-server",
            agentNickname: null,
            agentRole: null,
            gitInfo: null,
            name: "Thread browsing",
            turns: [],
          },
          {
            id: "thread-2",
            preview: "Handle failure",
            ephemeral: false,
            modelProvider: "openai",
            createdAt: 1_744_286_000,
            updatedAt: 1_744_289_600,
            status: {
              type: "systemError",
              error: {
                message: "Provider disconnected",
              },
            },
            path: null,
            cwd: "/tmp/project",
            cliVersion: "0.114.0",
            source: "app-server",
            agentNickname: null,
            agentRole: null,
            gitInfo: null,
            name: null,
            turns: [],
          },
        ],
        nextCursor: "cursor-2",
      },
    ]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const listResult = await sessionResult.data.listThreads("req-threads-1", {
      cursor: "cursor-1",
      limit: 10,
      archived: true,
      workspacePath: "/tmp/project",
    });

    expect(transport.sent).toEqual([
      {
        id: "atelier-appserver-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode App Server",
            title: null,
            version: "0.1.0",
          },
          capabilities: {
            experimentalApi: true,
          },
        },
      },
      {
        id: "req-threads-1",
        method: "thread/list",
        params: {
          cursor: "cursor-1",
          limit: 10,
          archived: true,
          cwd: "/tmp/project",
        },
      },
    ]);
    expect(listResult).toEqual({
      ok: true,
      data: {
        threads: [
          {
            id: "thread-1",
            preview: "Ship thread browsing",
            createdAt: "2025-04-10T10:13:20.000Z",
            updatedAt: "2025-04-10T11:13:20.000Z",
            workspacePath: "/tmp/project",
            name: "Thread browsing",
            archived: true,
            status: {
              type: "active",
              activeFlags: ["running", "awaiting_approval"],
            },
          },
          {
            id: "thread-2",
            preview: "Handle failure",
            createdAt: "2025-04-10T11:53:20.000Z",
            updatedAt: "2025-04-10T12:53:20.000Z",
            workspacePath: "/tmp/project",
            name: null,
            archived: true,
            status: {
              type: "systemError",
              message: "Provider disconnected",
            },
          },
        ],
        nextCursor: "cursor-2",
      },
    });
  });

  test("maps thread/read with includeTurns false and workspace metadata", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        thread: {
          id: "thread-1",
          preview: "Read this thread",
          ephemeral: false,
          modelProvider: "openai",
          createdAt: 1_744_280_000,
          updatedAt: 1_744_283_600,
          status: {
            type: "idle",
          },
          path: null,
          cwd: "/tmp/project",
          cliVersion: "0.114.0",
          source: "app-server",
          agentNickname: null,
          agentRole: null,
          gitInfo: null,
          name: "Readable thread",
          turns: [],
        },
      },
    ]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const readResult = await sessionResult.data.readThread("req-thread-read", {
      threadId: "thread-1",
      includeTurns: false,
      archived: true,
    });

    expect(transport.sent).toEqual([
      {
        id: "atelier-appserver-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode App Server",
            title: null,
            version: "0.1.0",
          },
          capabilities: {
            experimentalApi: true,
          },
        },
      },
      {
        id: "req-thread-read",
        method: "thread/read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      },
    ]);
    expect(readResult).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Read this thread",
          createdAt: "2025-04-10T10:13:20.000Z",
          updatedAt: "2025-04-10T11:13:20.000Z",
          workspacePath: "/tmp/project",
          name: "Readable thread",
          archived: true,
          status: {
            type: "idle",
          },
          turns: [],
        },
      },
    });
  });

  test("maps thread/read with includeTurns true into typed turn and item history", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        thread: {
          id: "thread-1",
          preview: "Read this thread",
          ephemeral: false,
          modelProvider: "openai",
          createdAt: 1_744_280_000,
          updatedAt: 1_744_283_600,
          status: {
            type: "idle",
          },
          path: null,
          cwd: "/tmp/project",
          cliVersion: "0.114.0",
          source: "app-server",
          agentNickname: null,
          agentRole: null,
          gitInfo: null,
          name: "Readable thread",
          turns: [
            {
              id: "turn-1",
              status: "completed",
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
    ]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const readResult = await sessionResult.data.readThread("req-thread-read", {
      threadId: "thread-1",
      includeTurns: true,
      archived: false,
    });

    expect(readResult).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Read this thread",
          createdAt: "2025-04-10T10:13:20.000Z",
          updatedAt: "2025-04-10T11:13:20.000Z",
          workspacePath: "/tmp/project",
          name: "Readable thread",
          archived: false,
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
  });

  test("rejects invalid turn history at the thread/read parse boundary", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        thread: {
          id: "thread-1",
          preview: "Read this thread",
          ephemeral: false,
          modelProvider: "openai",
          createdAt: 1_744_280_000,
          updatedAt: 1_744_283_600,
          status: {
            type: "idle",
          },
          path: null,
          cwd: "/tmp/project",
          cliVersion: "0.114.0",
          source: "app-server",
          agentNickname: null,
          agentRole: null,
          gitInfo: null,
          name: "Readable thread",
          turns: [
            {
              id: "turn-1",
              status: "completed",
              items: [
                {
                  id: "item-1",
                  type: "agentMessage",
                  phase: null,
                },
              ],
              error: null,
            },
          ],
        },
      },
    ]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const readResult = await sessionResult.data.readThread("req-thread-read", {
      threadId: "thread-1",
      includeTurns: true,
      archived: false,
    });

    expect(readResult.ok).toBe(false);
    if (readResult.ok) {
      throw new Error("Expected malformed turn history to be rejected.");
    }

    expect(readResult.error).toMatchObject({
      type: "invalidProviderMessage",
      agentId: "codex",
      provider: "codex",
      message: "Codex operation req-thread-read failed unexpectedly.",
      detail: {
        error: expect.stringContaining("Invalid Codex thread response."),
      },
    });
    expect(readResult.error.type).toBe("invalidProviderMessage");
    if (readResult.error.type !== "invalidProviderMessage") {
      throw new Error("Expected an invalid provider payload error.");
    }
  });

  test("maps thread mutations through the Codex transport contract", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        thread: {
          id: "thread-forked",
          preview: "Forked thread",
          ephemeral: false,
          modelProvider: "openai",
          createdAt: 1_744_290_000,
          updatedAt: 1_744_293_600,
          status: {
            type: "idle",
          },
          path: null,
          cwd: "/tmp/project",
          cliVersion: "0.114.0",
          source: "app-server",
          agentNickname: null,
          agentRole: null,
          gitInfo: null,
          name: "Forked thread",
          turns: [],
        },
        model: "gpt-5.4-mini",
        modelProvider: "openai",
        serviceTier: null,
        cwd: "/tmp/project",
        approvalPolicy: "never",
        sandbox: {
          mode: "workspace-write",
          network_access: false,
          exclude_tmpdir_env_var: false,
          exclude_slash_tmp: false,
        },
        reasoningEffort: "high",
      },
      {},
      {
        thread: {
          id: "thread-1",
          preview: "Archived thread",
          ephemeral: false,
          modelProvider: "openai",
          createdAt: 1_744_280_000,
          updatedAt: 1_744_283_600,
          status: {
            type: "idle",
          },
          path: null,
          cwd: "/tmp/project",
          cliVersion: "0.114.0",
          source: "app-server",
          agentNickname: null,
          agentRole: null,
          gitInfo: null,
          name: "Archived thread",
          turns: [],
        },
      },
      {},
    ]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const forkResult = await sessionResult.data.forkThread("req-thread-fork", {
      threadId: "thread-source",
      workspacePath: "/tmp/project",
      model: "gpt-5.4-mini",
    });
    const archiveResult = await sessionResult.data.archiveThread("req-thread-archive", {
      threadId: "thread-1",
    });
    const unarchiveResult = await sessionResult.data.unarchiveThread("req-thread-unarchive", {
      threadId: "thread-1",
    });
    const setNameResult = await sessionResult.data.setThreadName("req-thread-name", {
      threadId: "thread-1",
      name: "Renamed thread",
    });

    expect(transport.sent).toEqual([
      {
        id: "atelier-appserver-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode App Server",
            title: null,
            version: "0.1.0",
          },
          capabilities: {
            experimentalApi: true,
          },
        },
      },
      {
        id: "req-thread-fork",
        method: "thread/fork",
        params: {
          threadId: "thread-source",
          cwd: "/tmp/project",
          model: "gpt-5.4-mini",
          persistExtendedHistory: true,
        },
      },
      {
        id: "req-thread-archive",
        method: "thread/archive",
        params: {
          threadId: "thread-1",
        },
      },
      {
        id: "req-thread-unarchive",
        method: "thread/unarchive",
        params: {
          threadId: "thread-1",
        },
      },
      {
        id: "req-thread-name",
        method: "thread/name/set",
        params: {
          threadId: "thread-1",
          name: "Renamed thread",
        },
      },
    ]);
    expect(forkResult).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-forked",
          preview: "Forked thread",
          createdAt: "2025-04-10T13:00:00.000Z",
          updatedAt: "2025-04-10T14:00:00.000Z",
          workspacePath: "/tmp/project",
          name: "Forked thread",
          archived: false,
          status: {
            type: "idle",
          },
        },
        model: "gpt-5.4-mini",
        reasoningEffort: "high",
      },
    });
    expect(archiveResult).toEqual({
      ok: true,
      data: {},
    });
    expect(unarchiveResult).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Archived thread",
          createdAt: "2025-04-10T10:13:20.000Z",
          updatedAt: "2025-04-10T11:13:20.000Z",
          workspacePath: "/tmp/project",
          name: "Archived thread",
          archived: false,
          status: {
            type: "idle",
          },
        },
      },
    });
    expect(setNameResult).toEqual({
      ok: true,
      data: {},
    });
  });

  test("maps turn/start through the Codex transport contract", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([
      { userAgent: "Codex/Test" },
      {
        turn: {
          id: "turn-1",
          status: "inProgress",
          error: null,
        },
      },
    ]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const turnResult = await sessionResult.data.startTurn("req-turn-start", {
      threadId: "thread-1",
      prompt: "Ship the turn execution layer",
      cwd: "/tmp/project",
    });

    expect(transport.sent).toEqual([
      {
        id: "atelier-appserver-initialize",
        method: "initialize",
        params: {
          clientInfo: {
            name: "AtelierCode App Server",
            title: null,
            version: "0.1.0",
          },
          capabilities: {
            experimentalApi: true,
          },
        },
      },
      {
        id: "req-turn-start",
        method: "turn/start",
        params: {
          threadId: "thread-1",
          input: [
            {
              type: "text",
              text: "Ship the turn execution layer",
              text_elements: [],
            },
          ],
          cwd: "/tmp/project",
          model: undefined,
          effort: undefined,
        },
      },
    ]);
    expect(turnResult).toEqual({
      ok: true,
      data: {
        turn: {
          id: "turn-1",
          status: {
            type: "inProgress",
          },
        },
      },
    });
  });

  test("translates approval requests and resolves them through provider responses", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([{ userAgent: "Codex/Test" }]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const notifications: unknown[] = [];
    sessionResult.data.subscribe((notification) => {
      notifications.push(notification);
    });

    await sessionResult.data.listModels("req-init", {});

    transport.emit({
      type: "serverRequest",
      request: {
        id: "approval-1",
        method: "item/commandExecution/requestApproval",
        params: {
          threadId: "thread-1",
          turnId: "turn-1",
          itemId: "item-1",
        },
      },
    });

    const resolveResult = await sessionResult.data.resolveApproval({
      requestId: "approval-1",
      resolution: "approved",
    });

    transport.emit({
      type: "notification",
      notification: {
        method: "serverRequest/resolved",
        params: {
          threadId: "thread-1",
          requestId: "approval-1",
        },
      },
    });

    expect(resolveResult).toEqual({
      ok: true,
      data: {
        requestId: "approval-1",
        resolution: "approved",
      },
    });
    expect(transport.responded).toEqual([
      {
        id: "approval-1",
        result: "accept",
      },
    ]);
    expect(notifications).toContainEqual(
      expect.objectContaining({
        type: "approval",
        event: "requested",
        requestId: "approval-1",
      }),
    );
    expect(notifications).toContainEqual(
      expect.objectContaining({
        type: "approval",
        event: "resolved",
        requestId: "approval-1",
        resolution: "approved",
      }),
    );
  });

  test("does not mark the session ready when transport disconnects during initialized notification", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([{ userAgent: "Codex/Test" }], {
      onNotify: (notification, fakeTransport) => {
        if (notification.method !== "initialized") {
          return;
        }

        fakeTransport.emit({
          type: "disconnect",
          disconnect: {
            reason: "process_exited",
            message: "codex app-server exited while the transport was active.",
          },
        });
      },
    });
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    const initializationResult = await sessionResult.data.listModels("req-init", {});

    expect(initializationResult).toMatchObject({
      ok: false,
      error: {
        type: "sessionUnavailable",
        agentId: "codex",
        provider: "codex",
        code: "disconnected",
        message: "codex app-server exited while the transport was active.",
        executable: {
          executableName: "codex",
          resolvedPath: executablePath,
        },
        environment: {
          source: "login_probe",
        },
      },
    });
    expect(sessionResult.data.getState()).toBe("disconnected");
  });

  test("cleans up pending approvals and listeners after disconnect", async () => {
    const executablePath = createFakeExecutable();
    const transport = new FakeTransport([{ userAgent: "Codex/Test" }]);
    const sessionResult = await createCodexAgentSession({
      agentId: "codex",
      config: {
        id: "codex",
        provider: "codex",
      },
      logger: createSilentLogger(),
      transport,
      environmentResolver: new BaseEnvironmentResolver({
        inheritedEnvironment: {
          ATELIERCODE_CODEX_PATH: executablePath,
          PATH: path.dirname(executablePath),
          HOME: "/Users/tester",
          SHELL: "/bin/zsh",
        },
      }),
    });

    expect(sessionResult.ok).toBe(true);
    if (!sessionResult.ok) {
      throw new Error("Expected the Codex session to be created.");
    }

    await sessionResult.data.listModels("req-init", {});

    const notifications: AgentNotification[] = [];
    sessionResult.data.subscribe((notification) => {
      notifications.push(notification);
    });

    transport.emit({
      type: "serverRequest",
      request: {
        id: "approval-1",
        method: "item/commandExecution/requestApproval",
        params: {
          threadId: "thread-1",
          turnId: "turn-1",
          itemId: "item-1",
        },
      },
    });

    transport.emit({
      type: "disconnect",
      disconnect: {
        reason: "process_exited",
        message: "codex app-server exited while the transport was active.",
      },
    });

    transport.emit({
      type: "notification",
      notification: {
        method: "turn/started",
        params: {
          threadId: "thread-1",
          turn: {
            id: "turn-2",
            status: "inProgress",
            error: null,
          },
        },
      },
    });

    const resolveResult = await sessionResult.data.resolveApproval({
      requestId: "approval-1",
      resolution: "approved",
    });

    expect(notifications.filter((notification) => notification.type === "disconnect")).toHaveLength(
      1,
    );
    expect(notifications).toContainEqual(
      expect.objectContaining({
        type: "approval",
        event: "requested",
        requestId: "approval-1",
      }),
    );
    expect(notifications).not.toContainEqual(
      expect.objectContaining({
        type: "turn",
        event: "started",
        turnId: "turn-2",
      }),
    );
    expect(resolveResult).toEqual({
      ok: false,
      error: {
        type: "invalidProviderMessage",
        agentId: "codex",
        provider: "codex",
        message: "No pending approval exists for request approval-1.",
        detail: {
          requestId: "approval-1",
        },
      },
    });
  });
});

type FakeTransportOptions = Readonly<{
  onNotify?: (notification: CodexTransportNotification, transport: FakeTransport) => void;
}>;

class FakeTransport implements CodexTransport {
  readonly sent: CodexTransportRequest[] = [];
  readonly notified: CodexTransportNotification[] = [];
  readonly responded: CodexTransportResponse[] = [];
  connectCount = 0;

  private readonly listeners = new Set<(event: CodexTransportEvent) => void>();
  private disconnected = false;

  constructor(
    private readonly responses: unknown[] = [],
    private readonly options: FakeTransportOptions = {},
  ) {}

  async connect(): Promise<void> {
    this.connectCount += 1;
    this.disconnected = false;
  }

  async disconnect(): Promise<void> {
    this.disconnected = true;
  }

  async send<TResult = unknown>(request: CodexTransportRequest): Promise<TResult> {
    if (this.disconnected) {
      throw new CodexTransportError("process_exited", "Codex transport is not connected.");
    }

    this.sent.push(request);
    const response = this.responses.shift();
    return response as TResult;
  }

  async notify(notification: CodexTransportNotification): Promise<void> {
    if (this.disconnected) {
      throw new CodexTransportError("process_exited", "Codex transport is not connected.");
    }

    this.notified.push(notification);
    this.options.onNotify?.(notification, this);
  }

  async respond(response: CodexTransportResponse): Promise<void> {
    if (this.disconnected) {
      throw new CodexTransportError("process_exited", "Codex transport is not connected.");
    }

    this.responded.push(response);
  }

  subscribe(listener: (event: CodexTransportEvent) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  emit(event: CodexTransportEvent): void {
    if (event.type === "disconnect") {
      this.disconnected = true;
    }

    for (const listener of this.listeners) {
      listener(event);
    }
  }
}

const createFakeExecutable = (): string => {
  const directory = mkdtempSync(path.join(os.tmpdir(), "atelier-appserver-codex-session-"));
  temporaryDirectories.push(directory);
  const executablePath = path.join(directory, "codex");
  writeFileSync(executablePath, "#!/bin/sh\nexit 0\n");
  chmodSync(executablePath, 0o755);
  return executablePath;
};
