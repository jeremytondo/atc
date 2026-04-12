import { describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, realpath, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type CreateFakeAgentSessionOptions,
  createFakeAgentRegistry,
  createFakeAgentSession,
  createTestAgentItem,
  createTestAgentThread,
  createTestAgentThreadDetail,
  createTestAgentTurnDetail,
} from "@/test-support/agents";
import { createCapturingLogger, createSilentLogger } from "@/test-support/logger";
import { createThreadsService } from "@/threads/service";
import { createInMemoryThreadsStore, type ThreadsStore } from "@/threads/store";

const workspace = Object.freeze({
  id: "workspace-1",
  workspacePath: "/tmp/project",
  createdAt: "2026-04-10T09:00:00.000Z",
  lastOpenedAt: "2026-04-10T09:00:00.000Z",
});

describe("createThreadsService", () => {
  test("scopes thread/list to the opened workspace and surfaces stored defaults", async () => {
    const session = createFakeAgentSession({
      listThreads: async () => ({
        ok: true,
        data: {
          threads: [
            createTestAgentThread({
              id: "thread-1",
              preview: "Ship thread browsing",
              createdAt: "2026-04-10T10:00:00.000Z",
              updatedAt: "2026-04-10T11:00:00.000Z",
              workspacePath: "/tmp/project",
              name: "Thread browsing",
              archived: false,
              status: { type: "active", activeFlags: ["running"] },
            }),
          ],
          nextCursor: "cursor-2",
        },
      }),
    });
    const store = createInMemoryThreadsStore([
      {
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
        threadWorkspacePath: "/tmp/project",
        archived: false,
        model: "gpt-5.4",
        reasoningEffort: "medium",
        firstSeenAt: "2026-04-10T09:30:00.000Z",
        lastSeenAt: "2026-04-10T09:30:00.000Z",
      },
    ]);
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.listThreads("req-1", workspace, {
      archived: false,
      limit: 20,
    });

    expect(result).toEqual({
      ok: true,
      data: {
        threads: [
          {
            id: "thread-1",
            preview: "Ship thread browsing",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: "Thread browsing",
            archived: false,
            model: "gpt-5.4",
            reasoningEffort: "medium",
            status: { type: "active", activeFlags: ["running"] },
          },
        ],
        nextCursor: "cursor-2",
      },
    });
    expect(session.listThreadsCalls).toEqual([
      {
        requestId: "req-1",
        params: {
          archived: false,
          limit: 20,
          workspacePath: "/tmp/project",
        },
      },
    ]);
  });

  test("thread/list still succeeds when linkage persistence fails", async () => {
    const { logger, records } = createCapturingLogger();
    const session = createFakeAgentSession({
      listThreads: async () => ({
        ok: true,
        data: {
          threads: [
            createTestAgentThread({
              id: "thread-1",
              workspacePath: "/tmp/project",
            }),
          ],
          nextCursor: null,
        },
      }),
    });
    const service = createThreadsService({
      logger,
      registry: createFakeAgentRegistry(session),
      store: createFailingUpsertStore(createInMemoryThreadsStore()),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    await expect(service.listThreads("req-list", workspace, {})).resolves.toMatchObject({
      ok: true,
      data: {
        threads: [
          {
            id: "thread-1",
          },
        ],
      },
    });
    expect(records).toContainEqual(
      expect.objectContaining({
        level: "warn",
        message: "Failed to persist thread linkage metadata",
        operation: "thread/list",
      }),
    );
  });

  test("starts a thread, persists resolved defaults, and returns the public thread shape", async () => {
    const session = createFakeAgentSession({
      startThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-2",
            workspacePath: "/tmp/project",
            status: { type: "idle" },
          }),
          model: "gpt-5.4",
          reasoningEffort: "high",
        },
      }),
    });
    const store = createInMemoryThreadsStore();
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.startThread("req-start", workspace, {
      reasoningEffort: "high",
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-2",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: null,
          archived: false,
          model: "gpt-5.4",
          reasoningEffort: "high",
          status: { type: "idle" },
        },
      },
    });
    await expect(
      store.getWorkspaceThreadLink({
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-2",
      }),
    ).resolves.toMatchObject({
      model: "gpt-5.4",
      reasoningEffort: "high",
    });
  });

  test("thread/start still succeeds when linkage persistence fails", async () => {
    const { logger, records } = createCapturingLogger();
    const session = createFakeAgentSession({
      startThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-start",
            workspacePath: "/tmp/project",
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const service = createThreadsService({
      logger,
      registry: createFakeAgentRegistry(session),
      store: createFailingUpsertStore(createInMemoryThreadsStore()),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    await expect(service.startThread("req-start", workspace, {})).resolves.toMatchObject({
      ok: true,
      data: {
        thread: {
          id: "thread-start",
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      },
    });
    expect(records).toContainEqual(
      expect.objectContaining({
        level: "warn",
        message: "Failed to persist thread linkage metadata",
        operation: "thread/start",
      }),
    );
  });

  test("resumes a thread using stored defaults when the request omits overrides", async () => {
    const store = createInMemoryThreadsStore([
      {
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
        threadWorkspacePath: "/tmp/project",
        archived: true,
        model: "gpt-5.4-mini",
        reasoningEffort: "low",
        firstSeenAt: "2026-04-10T09:30:00.000Z",
        lastSeenAt: "2026-04-10T09:30:00.000Z",
      },
    ]);
    const session = createFakeAgentSession({
      resumeThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-1",
            workspacePath: "/tmp/project",
            archived: true,
            status: { type: "idle" },
          }),
          model: "gpt-5.4-mini",
          reasoningEffort: "low",
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.resumeThread("req-resume", workspace, {
      threadId: "thread-1",
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: null,
          archived: true,
          model: "gpt-5.4-mini",
          reasoningEffort: "low",
          status: { type: "idle" },
        },
      },
    });
    expect(session.resumeThreadCalls).toEqual([
      {
        requestId: "req-resume",
        params: {
          threadId: "thread-1",
          workspacePath: "/tmp/project",
          model: "gpt-5.4-mini",
          reasoningEffort: "low",
        },
      },
    ]);
  });

  test("lets explicit resume overrides win over stored defaults while preserving stored fallbacks", async () => {
    const store = createInMemoryThreadsStore([
      {
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
        threadWorkspacePath: "/tmp/project",
        archived: false,
        model: "gpt-5.4-mini",
        reasoningEffort: "low",
        firstSeenAt: "2026-04-10T09:30:00.000Z",
        lastSeenAt: "2026-04-10T09:30:00.000Z",
      },
    ]);
    const session = createFakeAgentSession({
      resumeThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-1",
            workspacePath: "/tmp/project",
          }),
          model: "provider-model",
          reasoningEffort: "medium",
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.resumeThread("req-resume", workspace, {
      threadId: "thread-1",
      model: "request-model",
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: null,
          archived: false,
          model: "request-model",
          reasoningEffort: "low",
          status: { type: "idle" },
        },
      },
    });
  });

  test("thread/resume still succeeds when linkage persistence fails", async () => {
    const { logger, records } = createCapturingLogger();
    const session = createFakeAgentSession({
      resumeThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: "/tmp/project",
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const service = createThreadsService({
      logger,
      registry: createFakeAgentRegistry(session),
      store: createFailingUpsertStore(
        createInMemoryThreadsStore([
          {
            workspaceId: "workspace-1",
            provider: "codex",
            threadId: "thread-1",
            threadWorkspacePath: "/tmp/project",
            archived: false,
            model: "gpt-5.4",
            reasoningEffort: "medium",
            firstSeenAt: "2026-04-10T09:30:00.000Z",
            lastSeenAt: "2026-04-10T09:30:00.000Z",
          },
        ]),
      ),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    await expect(
      service.resumeThread("req-resume", workspace, {
        threadId: "thread-1",
      }),
    ).resolves.toMatchObject({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
        },
      },
    });
    expect(records).toContainEqual(
      expect.objectContaining({
        level: "warn",
        message: "Failed to persist thread linkage metadata",
        operation: "thread/resume",
      }),
    );
  });

  test("filters cross-workspace provider list results and does not persist them", async () => {
    const { logger, records } = createCapturingLogger();
    const store = createInMemoryThreadsStore();
    const session = createFakeAgentSession({
      listThreads: async () => ({
        ok: true,
        data: {
          threads: [
            createTestAgentThread({
              id: "thread-ok",
              workspacePath: "/tmp/project",
            }),
            createTestAgentThread({
              id: "thread-other",
              workspacePath: "/tmp/other-project",
            }),
          ],
          nextCursor: "cursor-2",
        },
      }),
    });
    const service = createThreadsService({
      logger,
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.listThreads("req-list", workspace, {});

    expect(result).toEqual({
      ok: true,
      data: {
        threads: [
          {
            id: "thread-ok",
            preview: "Thread preview",
            createdAt: "2026-04-10T10:00:00.000Z",
            updatedAt: "2026-04-10T11:00:00.000Z",
            name: null,
            archived: false,
            model: null,
            reasoningEffort: null,
            status: { type: "idle" },
          },
        ],
        nextCursor: "cursor-2",
      },
    });
    await expect(
      store.listWorkspaceThreadLinks({
        workspaceId: "workspace-1",
        provider: "codex",
      }),
    ).resolves.toEqual([
      expect.objectContaining({
        threadId: "thread-ok",
      }),
    ]);
    expect(records).toContainEqual(
      expect.objectContaining({
        level: "warn",
        message: "Filtered cross-workspace thread from provider list",
        threadId: "thread-other",
      }),
    );
  });

  test("normalizes equivalent workspace paths when filtering thread/list", async () => {
    const rootDirectory = await mkdtemp(join(tmpdir(), "atelier-threads-service-"));
    const workspaceDirectory = join(rootDirectory, "workspace");
    const aliasPath = join(rootDirectory, "workspace-alias");

    await mkdir(workspaceDirectory, { recursive: true });
    await symlink(workspaceDirectory, aliasPath);

    const canonicalWorkspacePath = await realpath(workspaceDirectory);
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(
        createFakeAgentSession({
          listThreads: async () => ({
            ok: true,
            data: {
              threads: [
                createTestAgentThread({
                  id: "thread-symlink",
                  workspacePath: aliasPath,
                }),
              ],
              nextCursor: null,
            },
          }),
        }),
      ),
      store: createInMemoryThreadsStore(),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    try {
      await expect(
        service.listThreads(
          "req-list",
          {
            ...workspace,
            workspacePath: canonicalWorkspacePath,
          },
          {},
        ),
      ).resolves.toMatchObject({
        ok: true,
        data: {
          threads: [
            {
              id: "thread-symlink",
            },
          ],
        },
      });
    } finally {
      await rm(rootDirectory, { force: true, recursive: true });
    }
  });

  test("reads provider-authoritative turn history when thread/read requests turns", async () => {
    const session = createFakeAgentSession({
      readThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: "thread-1",
            turns: [
              createTestAgentTurnDetail({
                id: "turn-1",
                items: [createTestAgentItem({ id: "item-1", text: "Hello from history" })],
              }),
            ],
          }),
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store: createInMemoryThreadsStore(),
    });

    const result = await service.readThread("req-1", workspace, {
      threadId: "thread-1",
      includeTurns: true,
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: null,
          archived: false,
          model: null,
          reasoningEffort: null,
          status: { type: "idle" },
          turns: [
            {
              id: "turn-1",
              status: { type: "completed" },
              items: [
                {
                  id: "item-1",
                  type: "agentMessage",
                  text: "Hello from history",
                  phase: null,
                },
              ],
              error: null,
            },
          ],
        },
      },
    });
    expect(session.readThreadCalls).toEqual([
      {
        requestId: "req-1",
        params: {
          threadId: "thread-1",
          includeTurns: true,
        },
      },
    ]);
  });

  test("thread/read returns provider-authoritative archived and does not pass cached archived back upstream", async () => {
    const store = createInMemoryThreadsStore([
      {
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
        threadWorkspacePath: "/tmp/project",
        archived: true,
        model: "gpt-5.4",
        reasoningEffort: null,
        firstSeenAt: "2026-04-10T09:30:00.000Z",
        lastSeenAt: "2026-04-10T09:30:00.000Z",
      },
    ]);
    const session = createFakeAgentSession({
      readThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: "thread-1",
            workspacePath: "/tmp/project",
            archived: false,
          }),
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.readThread("req-read", workspace, {
      threadId: "thread-1",
      includeTurns: false,
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: null,
          archived: false,
          model: "gpt-5.4",
          reasoningEffort: null,
          status: { type: "idle" },
          turns: [],
        },
      },
    });
    expect(session.readThreadCalls).toEqual([
      {
        requestId: "req-read",
        params: {
          threadId: "thread-1",
          includeTurns: false,
        },
      },
    ]);
  });

  test("forks a thread after missing-link workspace validation and persists fork defaults", async () => {
    const session = createFakeAgentSession({
      readThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: params.threadId,
            workspacePath: "/tmp/project",
            archived: true,
          }),
        },
      }),
      forkThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-forked",
            workspacePath: "/tmp/project",
            name: "Forked thread",
          }),
          model: "gpt-5.4-mini",
          reasoningEffort: "high",
        },
      }),
    });
    const store = createInMemoryThreadsStore();
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.forkThread("req-fork", workspace, {
      threadId: "thread-source",
      model: "gpt-5.4-mini",
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-forked",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: "Forked thread",
          archived: false,
          model: "gpt-5.4-mini",
          reasoningEffort: "high",
          status: { type: "idle" },
        },
      },
    });
    expect(session.readThreadCalls).toEqual([
      {
        requestId: "req-fork",
        params: {
          threadId: "thread-source",
          includeTurns: false,
        },
      },
    ]);
    expect(session.forkThreadCalls).toEqual([
      {
        requestId: "req-fork",
        params: {
          threadId: "thread-source",
          workspacePath: "/tmp/project",
          model: "gpt-5.4-mini",
        },
      },
    ]);
    await expect(
      store.getWorkspaceThreadLink({
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-forked",
      }),
    ).resolves.toMatchObject({
      threadWorkspacePath: "/tmp/project",
      archived: false,
      model: "gpt-5.4-mini",
      reasoningEffort: "high",
    });
  });

  test("rejects archive when missing-link validation finds a different workspace", async () => {
    const session = createFakeAgentSession({
      readThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: params.threadId,
            workspacePath: "/tmp/other-project",
          }),
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store: createInMemoryThreadsStore(),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.archiveThread("req-archive", workspace, {
      threadId: "thread-elsewhere",
    });

    expect(result).toMatchObject({
      ok: false,
      error: {
        code: -33008,
        data: {
          code: "THREAD_WORKSPACE_MISMATCH",
          threadId: "thread-elsewhere",
          openedWorkspacePath: "/tmp/project",
          threadWorkspacePath: "/tmp/other-project",
        },
      },
    });
    expect(session.archiveThreadCalls).toEqual([]);
  });

  test("archive and unarchive refresh persisted archive hints while preserving defaults", async () => {
    const store = createInMemoryThreadsStore([
      {
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
        threadWorkspacePath: "/tmp/project",
        archived: false,
        model: "gpt-5.4",
        reasoningEffort: "medium",
        firstSeenAt: "2026-04-10T09:30:00.000Z",
        lastSeenAt: "2026-04-10T09:30:00.000Z",
      },
    ]);
    const session = createFakeAgentSession({
      archiveThread: async () => ({
        ok: true,
        data: {},
      }),
      unarchiveThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: params.threadId,
            workspacePath: "/tmp/project",
            archived: false,
          }),
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    await expect(
      service.archiveThread("req-archive", workspace, {
        threadId: "thread-1",
      }),
    ).resolves.toEqual({
      ok: true,
      data: {},
    });
    await expect(
      store.getWorkspaceThreadLink({
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
      }),
    ).resolves.toMatchObject({
      archived: true,
      model: "gpt-5.4",
      reasoningEffort: "medium",
    });

    const unarchiveResult = await service.unarchiveThread("req-unarchive", workspace, {
      threadId: "thread-1",
    });

    expect(unarchiveResult).toEqual({
      ok: true,
      data: {
        thread: {
          id: "thread-1",
          preview: "Thread preview",
          createdAt: "2026-04-10T10:00:00.000Z",
          updatedAt: "2026-04-10T11:00:00.000Z",
          name: null,
          archived: false,
          model: "gpt-5.4",
          reasoningEffort: "medium",
          status: { type: "idle" },
        },
      },
    });
    await expect(
      store.getWorkspaceThreadLink({
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
      }),
    ).resolves.toMatchObject({
      archived: false,
      model: "gpt-5.4",
      reasoningEffort: "medium",
    });
  });

  test("thread/name/set establishes linkage through read fallback without local name persistence", async () => {
    const session = createFakeAgentSession({
      readThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: params.threadId,
            workspacePath: "/tmp/project",
            name: "Old name",
          }),
        },
      }),
      setThreadName: async () => ({
        ok: true,
        data: {},
      }),
    });
    const store = createInMemoryThreadsStore();
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store,
      now: () => "2026-04-10T12:00:00.000Z",
    });

    await expect(
      service.setThreadName("req-name", workspace, {
        threadId: "thread-1",
        name: "Renamed thread",
      }),
    ).resolves.toEqual({
      ok: true,
      data: {},
    });
    await expect(
      store.getWorkspaceThreadLink({
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
      }),
    ).resolves.toEqual({
      workspaceId: "workspace-1",
      provider: "codex",
      threadId: "thread-1",
      threadWorkspacePath: "/tmp/project",
      archived: false,
      model: null,
      reasoningEffort: null,
      firstSeenAt: "2026-04-10T12:00:00.000Z",
      lastSeenAt: "2026-04-10T12:00:00.000Z",
    });
  });

  test("surfaces provider session-unavailable errors for thread mutations", async () => {
    const error = Object.freeze({
      type: "sessionUnavailable" as const,
      agentId: "codex",
      provider: "codex" as const,
      code: "disconnected" as const,
      message: "Agent session disconnected.",
    });
    const service = createMutationTestService({
      forkThread: async () => ({ ok: false, error }),
      archiveThread: async () => ({ ok: false, error }),
      unarchiveThread: async () => ({ ok: false, error }),
      setThreadName: async () => ({ ok: false, error }),
    });

    for (const invocation of mutationMethodInvocations) {
      await expect(invocation.call(service)).resolves.toEqual({
        ok: false,
        error,
      });
    }
  });

  test("surfaces provider remote errors for thread mutations", async () => {
    const error = Object.freeze({
      type: "remoteError" as const,
      agentId: "codex",
      provider: "codex" as const,
      requestId: "req-remote",
      code: 500,
      message: "Provider mutation failed.",
      data: { reason: "upstream exploded" },
    });
    const service = createMutationTestService({
      forkThread: async () => ({ ok: false, error }),
      archiveThread: async () => ({ ok: false, error }),
      unarchiveThread: async () => ({ ok: false, error }),
      setThreadName: async () => ({ ok: false, error }),
    });

    for (const invocation of mutationMethodInvocations) {
      await expect(invocation.call(service)).resolves.toEqual({
        ok: false,
        error,
      });
    }
  });

  test("maps invalid provider payloads for thread mutations to stable service errors", async () => {
    const error = Object.freeze({
      type: "invalidProviderMessage" as const,
      agentId: "codex",
      provider: "codex" as const,
      message: "Malformed provider payload.",
      detail: { field: "thread" },
    });
    const service = createMutationTestService({
      forkThread: async () => ({ ok: false, error }),
      archiveThread: async () => ({ ok: false, error }),
      unarchiveThread: async () => ({ ok: false, error }),
      setThreadName: async () => ({ ok: false, error }),
    });

    for (const invocation of mutationMethodInvocations) {
      await expect(invocation.call(service)).resolves.toEqual({
        ok: false,
        error: {
          type: "invalidProviderPayload",
          agentId: "codex",
          provider: "codex",
          operation: invocation.operation,
          message: "Malformed provider payload.",
          detail: { field: "thread" },
        },
      });
    }
  });

  test("thread/fork still succeeds when linkage writes fail", async () => {
    const { logger, records } = createCapturingLogger();
    const session = createFakeAgentSession({
      readThread: async (_requestId, params) => ({
        ok: true,
        data: {
          thread: createTestAgentThreadDetail({
            id: params.threadId,
            workspacePath: "/tmp/project",
          }),
        },
      }),
      forkThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-forked",
            workspacePath: "/tmp/project",
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const service = createThreadsService({
      logger,
      registry: createFakeAgentRegistry(session),
      store: createFailingUpsertStore(createInMemoryThreadsStore()),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    await expect(
      service.forkThread("req-fork", workspace, {
        threadId: "thread-source",
      }),
    ).resolves.toMatchObject({
      ok: true,
      data: {
        thread: {
          id: "thread-forked",
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      },
    });
    expect(records).toContainEqual(
      expect.objectContaining({
        level: "warn",
        message: "Failed to persist thread linkage metadata",
        operation: "thread/fork",
      }),
    );
  });

  test("warns when provider defaults differ from Atelier-resolved defaults without changing behavior", async () => {
    const { logger, records } = createCapturingLogger();
    const session = createFakeAgentSession({
      startThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-started",
            workspacePath: "/tmp/project",
          }),
          model: "provider-model",
          reasoningEffort: "medium",
        },
      }),
    });
    const service = createThreadsService({
      logger,
      registry: createFakeAgentRegistry(session),
      store: createInMemoryThreadsStore(),
      now: () => "2026-04-10T12:00:00.000Z",
    });

    const result = await service.startThread("req-start", workspace, {
      model: "request-model",
      reasoningEffort: "high",
    });

    expect(result).toEqual({
      ok: true,
      data: {
        thread: expect.objectContaining({
          id: "thread-started",
          model: "request-model",
          reasoningEffort: "high",
        }),
      },
    });
    expect(records).toContainEqual(
      expect.objectContaining({
        level: "warn",
        message: "Resolved thread defaults differ from provider response",
        operation: "thread/start",
        threadId: "thread-started",
        resolvedModel: "request-model",
        providerModel: "provider-model",
      }),
    );
  });

  test("returns a workspace mismatch error when provider metadata does not match the opened workspace", async () => {
    const session = createFakeAgentSession({
      resumeThread: async () => ({
        ok: true,
        data: {
          thread: createTestAgentThread({
            id: "thread-9",
            workspacePath: "/tmp/other-project",
          }),
          model: "gpt-5.4",
          reasoningEffort: "medium",
        },
      }),
    });
    const service = createThreadsService({
      logger: createSilentLogger("error"),
      registry: createFakeAgentRegistry(session),
      store: createInMemoryThreadsStore(),
    });

    const result = await service.resumeThread("req-1", workspace, {
      threadId: "thread-9",
    });

    expect(result).toMatchObject({
      ok: false,
      error: {
        code: -33008,
        message: "Thread does not belong to the opened workspace",
        data: {
          code: "THREAD_WORKSPACE_MISMATCH",
          threadId: "thread-9",
          openedWorkspacePath: "/tmp/project",
          threadWorkspacePath: "/tmp/other-project",
        },
      },
    });
  });
});

const createFailingUpsertStore = (store: ThreadsStore): ThreadsStore =>
  Object.freeze({
    getWorkspaceThreadLink: store.getWorkspaceThreadLink,
    listWorkspaceThreadLinks: store.listWorkspaceThreadLinks,
    upsertWorkspaceThreadLinks: async () => {
      throw new Error("bookkeeping write failed");
    },
  });

type TestThreadsService = ReturnType<typeof createThreadsService>;

const mutationMethodInvocations: readonly Readonly<{
  operation: "thread/fork" | "thread/archive" | "thread/unarchive" | "thread/name/set";
  call: (service: TestThreadsService) => Promise<unknown>;
}>[] = Object.freeze([
  Object.freeze({
    operation: "thread/fork" as const,
    call: (service: TestThreadsService) =>
      service.forkThread("req-fork", workspace, {
        threadId: "thread-1",
      }),
  }),
  Object.freeze({
    operation: "thread/archive" as const,
    call: (service: TestThreadsService) =>
      service.archiveThread("req-archive", workspace, {
        threadId: "thread-1",
      }),
  }),
  Object.freeze({
    operation: "thread/unarchive" as const,
    call: (service: TestThreadsService) =>
      service.unarchiveThread("req-unarchive", workspace, {
        threadId: "thread-1",
      }),
  }),
  Object.freeze({
    operation: "thread/name/set" as const,
    call: (service: TestThreadsService) =>
      service.setThreadName("req-name", workspace, {
        threadId: "thread-1",
        name: "Renamed thread",
      }),
  }),
]);

const createMutationTestService = (
  sessionOptions: Pick<
    CreateFakeAgentSessionOptions,
    "forkThread" | "archiveThread" | "unarchiveThread" | "setThreadName"
  >,
): TestThreadsService =>
  createThreadsService({
    logger: createSilentLogger("error"),
    registry: createFakeAgentRegistry(createFakeAgentSession(sessionOptions)),
    store: createInMemoryThreadsStore([
      {
        workspaceId: "workspace-1",
        provider: "codex",
        threadId: "thread-1",
        threadWorkspacePath: "/tmp/project",
        archived: false,
        model: "gpt-5.4",
        reasoningEffort: "medium",
        firstSeenAt: "2026-04-10T09:30:00.000Z",
        lastSeenAt: "2026-04-10T09:30:00.000Z",
      },
    ]),
    now: () => "2026-04-10T12:00:00.000Z",
  });
