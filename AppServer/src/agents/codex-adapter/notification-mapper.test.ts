import { describe, expect, test } from "bun:test";
import {
  mapCodexResolvedApproval,
  mapCodexServerRequest,
  mapCodexTransportNotification,
} from "@/agents/codex-adapter/notification-mapper";
import type {
  CodexAgentMessageDeltaNotification,
  CodexCommandExecutionOutputDeltaNotification,
  CodexCommandExecutionRequestApprovalParams,
  CodexMcpToolCallProgressNotification,
  CodexReasoningSummaryTextDeltaNotification,
  CodexReasoningTextDeltaNotification,
  CodexTurnDiffUpdatedNotification,
  CodexTurnPlanUpdatedNotification,
} from "@/agents/codex-adapter/protocol";

const context = {
  agentId: "codex",
  provider: "codex" as const,
  receivedAt: "2026-04-10T12:00:00.000Z",
};

describe("mapCodexTransportNotification", () => {
  test("maps thread mutation notifications into provider-neutral thread events", () => {
    expect(
      mapCodexTransportNotification(
        {
          method: "thread/archived",
          params: {
            threadId: "thread-1",
          },
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "thread/archived",
        rawPayload: {
          threadId: "thread-1",
        },
        type: "thread",
        event: "archived",
        threadId: "thread-1",
        thread: {
          id: "thread-1",
          preview: "",
          updatedAt: "2026-04-10T12:00:00.000Z",
          name: null,
          archived: true,
          status: {
            type: "notLoaded",
          },
        },
      },
    ]);

    expect(
      mapCodexTransportNotification(
        {
          method: "thread/unarchived",
          params: {
            threadId: "thread-1",
          },
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "thread/unarchived",
        rawPayload: {
          threadId: "thread-1",
        },
        type: "thread",
        event: "unarchived",
        threadId: "thread-1",
        thread: {
          id: "thread-1",
          preview: "",
          updatedAt: "2026-04-10T12:00:00.000Z",
          name: null,
          archived: false,
          status: {
            type: "notLoaded",
          },
        },
      },
    ]);

    expect(
      mapCodexTransportNotification(
        {
          method: "thread/name/updated",
          params: {
            threadId: "thread-1",
            threadName: "Renamed thread",
          },
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "thread/name/updated",
        rawPayload: {
          threadId: "thread-1",
          threadName: "Renamed thread",
        },
        type: "thread",
        event: "nameUpdated",
        threadId: "thread-1",
        threadName: "Renamed thread",
        thread: {
          id: "thread-1",
          preview: "",
          updatedAt: "2026-04-10T12:00:00.000Z",
          name: "Renamed thread",
          archived: false,
          status: {
            type: "notLoaded",
          },
        },
      },
    ]);
  });

  test("maps pinned plan and diff fixtures into provider-neutral notifications", () => {
    const planFixture: CodexTurnPlanUpdatedNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      explanation: "Ship the session layer",
      plan: [
        {
          step: "Implement transport",
          status: "completed",
        },
        {
          step: "Add tests",
          status: "inProgress",
        },
      ],
    };
    const diffFixture: CodexTurnDiffUpdatedNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      diff: [
        "diff --git a/src/example.ts b/src/example.ts",
        "--- a/src/example.ts",
        "+++ b/src/example.ts",
        "+added",
        "-removed",
      ].join("\n"),
    };

    const mappedPlan = mapCodexTransportNotification(
      {
        method: "turn/plan/updated",
        params: planFixture,
      },
      context,
    );
    const mappedDiff = mapCodexTransportNotification(
      {
        method: "turn/diff/updated",
        params: diffFixture,
      },
      context,
    );

    expect(mappedPlan).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "turn/plan/updated",
        rawPayload: planFixture,
        type: "plan",
        event: "updated",
        threadId: "thread-1",
        turnId: "turn-1",
        explanation: "Ship the session layer",
        steps: [
          {
            step: "Implement transport",
            status: "completed",
          },
          {
            step: "Add tests",
            status: "in_progress",
          },
        ],
      },
    ]);
    expect(mappedDiff).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "turn/diff/updated",
        rawPayload: diffFixture,
        type: "diff",
        event: "updated",
        threadId: "thread-1",
        turnId: "turn-1",
        diff: diffFixture.diff,
        summary: [
          {
            path: "src/example.ts",
            additions: 1,
            deletions: 1,
          },
        ],
      },
    ]);
  });

  test("maps pinned reasoning fixtures into provider-neutral notifications", () => {
    const reasoningFixture: CodexReasoningTextDeltaNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-1",
      delta: "Thinking...",
      contentIndex: 0,
    };

    expect(
      mapCodexTransportNotification(
        {
          method: "item/reasoning/textDelta",
          params: reasoningFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/reasoning/textDelta",
        rawPayload: reasoningFixture,
        type: "reasoning",
        event: "textDelta",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-1",
        delta: "Thinking...",
      },
    ]);
  });

  test("maps supported item delta fixtures into provider-neutral notifications", () => {
    const messageFixture: CodexAgentMessageDeltaNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-message",
      delta: "Working on it...",
    };
    const reasoningSummaryFixture: CodexReasoningSummaryTextDeltaNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-reasoning",
      delta: "Short summary",
      summaryIndex: 0,
    };
    const commandFixture: CodexCommandExecutionOutputDeltaNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-command",
      delta: "stdout line\n",
    };
    const toolFixture: CodexMcpToolCallProgressNotification = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-tool",
      message: "Fetching results",
    };

    expect(
      mapCodexTransportNotification(
        {
          method: "item/agentMessage/delta",
          params: messageFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/agentMessage/delta",
        rawPayload: messageFixture,
        type: "message",
        event: "textDelta",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-message",
        delta: "Working on it...",
      },
    ]);
    expect(
      mapCodexTransportNotification(
        {
          method: "item/reasoning/summaryTextDelta",
          params: reasoningSummaryFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/reasoning/summaryTextDelta",
        rawPayload: reasoningSummaryFixture,
        type: "reasoning",
        event: "summaryTextDelta",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-reasoning",
        delta: "Short summary",
      },
    ]);
    expect(
      mapCodexTransportNotification(
        {
          method: "item/commandExecution/outputDelta",
          params: commandFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/commandExecution/outputDelta",
        rawPayload: commandFixture,
        type: "command",
        event: "outputDelta",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-command",
        delta: "stdout line\n",
      },
    ]);
    expect(
      mapCodexTransportNotification(
        {
          method: "item/mcpToolCall/progress",
          params: toolFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/mcpToolCall/progress",
        rawPayload: toolFixture,
        type: "tool",
        event: "progress",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-tool",
        message: "Fetching results",
      },
    ]);
  });

  test("maps typed item lifecycle notifications", () => {
    expect(
      mapCodexTransportNotification(
        {
          method: "item/completed",
          params: {
            threadId: "thread-1",
            turnId: "turn-1",
            item: {
              id: "item-1",
              type: "commandExecution",
              command: "echo hello",
              cwd: "/tmp/project",
              processId: null,
              status: "completed",
              commandActions: [],
              aggregatedOutput: "hello\n",
              exitCode: 0,
              durationMs: null,
            },
          },
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/completed",
        rawPayload: {
          threadId: "thread-1",
          turnId: "turn-1",
          item: {
            id: "item-1",
            type: "commandExecution",
            command: "echo hello",
            cwd: "/tmp/project",
            processId: null,
            status: "completed",
            commandActions: [],
            aggregatedOutput: "hello\n",
            exitCode: 0,
            durationMs: null,
          },
        },
        type: "item",
        event: "completed",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-1",
        item: {
          id: "item-1",
          type: "commandExecution",
          command: "echo hello",
          cwd: "/tmp/project",
          processId: null,
          status: "completed",
          commandActions: [],
          aggregatedOutput: "hello\n",
          exitCode: 0,
          durationMs: null,
        },
      },
    ]);
  });
});

describe("mapCodexServerRequest", () => {
  test("maps command approval requests into provider-neutral approval notifications", () => {
    const approvalFixture: CodexCommandExecutionRequestApprovalParams = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-1",
      command: "git status",
      cwd: "/tmp/project",
      commandActions: [
        {
          type: "unknown",
          command: "git status",
        },
      ],
    };

    expect(
      mapCodexServerRequest(
        {
          id: "approval-1",
          method: "item/commandExecution/requestApproval",
          params: approvalFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/commandExecution/requestApproval",
        rawPayload: approvalFixture,
        type: "approval",
        event: "requested",
        requestId: "approval-1",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-1",
        approval: {
          requestId: "approval-1",
          kind: "commandExecution",
          threadId: "thread-1",
          turnId: "turn-1",
          itemId: "item-1",
          supportedResolutions: ["approved", "approvedForSession", "declined", "cancelled"],
          approvalId: null,
          reason: null,
          command: "git status",
          cwd: "/tmp/project",
          commandActions: [
            {
              type: "unknown",
              command: "git status",
            },
          ],
          rawRequest: {
            id: "approval-1",
            method: "item/commandExecution/requestApproval",
            params: approvalFixture,
          },
        },
      },
    ]);
  });

  test("maps file change approval requests into provider-neutral approval notifications", () => {
    const approvalFixture = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-2",
      reason: "Needs write access",
      grantRoot: "/tmp/project",
    };

    expect(
      mapCodexServerRequest(
        {
          id: "approval-2",
          method: "item/fileChange/requestApproval",
          params: approvalFixture,
        },
        context,
      ),
    ).toEqual([
      {
        agentId: "codex",
        provider: "codex",
        receivedAt: "2026-04-10T12:00:00.000Z",
        rawMethod: "item/fileChange/requestApproval",
        rawPayload: approvalFixture,
        type: "approval",
        event: "requested",
        requestId: "approval-2",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-2",
        approval: {
          requestId: "approval-2",
          kind: "fileChange",
          threadId: "thread-1",
          turnId: "turn-1",
          itemId: "item-2",
          supportedResolutions: ["approved", "approvedForSession", "declined", "cancelled"],
          reason: "Needs write access",
          grantRoot: "/tmp/project",
          rawRequest: {
            id: "approval-2",
            method: "item/fileChange/requestApproval",
            params: approvalFixture,
          },
        },
      },
    ]);
  });

  test("maps MCP elicitation approval requests with form and url variants", () => {
    const formFixture = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-3",
      serverName: "docs",
      mode: "form",
      message: "Need extra fields",
      requestedSchema: {
        type: "object",
        properties: {
          query: {
            type: "string",
          },
        },
      },
      _meta: {
        source: "mcp",
      },
    };
    const urlFixture = {
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-4",
      serverName: "browser",
      mode: "url",
      message: "Open this link",
      url: "https://example.com",
      elicitationId: "elicit-1",
    };

    expect(
      mapCodexServerRequest(
        {
          id: "approval-3",
          method: "mcpServer/elicitation/request",
          params: formFixture,
        },
        context,
      ),
    ).toEqual([
      expect.objectContaining({
        requestId: "approval-3",
        approval: {
          requestId: "approval-3",
          kind: "mcpElicitation",
          threadId: "thread-1",
          turnId: "turn-1",
          itemId: "item-3",
          supportedResolutions: ["approved", "declined", "cancelled"],
          serverName: "docs",
          mode: "form",
          message: "Need extra fields",
          requestedSchema: formFixture.requestedSchema,
          url: null,
          elicitationId: null,
          metadata: {
            source: "mcp",
          },
          rawRequest: {
            id: "approval-3",
            method: "mcpServer/elicitation/request",
            params: formFixture,
          },
        },
      }),
    ]);
    expect(
      mapCodexServerRequest(
        {
          id: "approval-4",
          method: "mcpServer/elicitation/request",
          params: urlFixture,
        },
        context,
      ),
    ).toEqual([
      expect.objectContaining({
        requestId: "approval-4",
        approval: {
          requestId: "approval-4",
          kind: "mcpElicitation",
          threadId: "thread-1",
          turnId: "turn-1",
          itemId: "item-4",
          supportedResolutions: ["approved", "declined", "cancelled"],
          serverName: "browser",
          mode: "url",
          message: "Open this link",
          requestedSchema: null,
          url: "https://example.com",
          elicitationId: "elicit-1",
          metadata: null,
          rawRequest: {
            id: "approval-4",
            method: "mcpServer/elicitation/request",
            params: urlFixture,
          },
        },
      }),
    ]);
  });
});

describe("mapCodexResolvedApproval", () => {
  test("maps resolved approvals with the recorded client resolution", () => {
    expect(
      mapCodexResolvedApproval(
        "approval-1",
        {
          approval: {
            requestId: "approval-1",
            kind: "fileChange",
            threadId: "thread-1",
            turnId: "turn-1",
            itemId: "item-1",
            supportedResolutions: ["approved", "declined", "cancelled"],
            reason: "Write files",
            grantRoot: "/tmp/project",
            rawRequest: {},
          },
          resolution: "declined",
        },
        context,
      ),
    ).toEqual({
      agentId: "codex",
      provider: "codex",
      receivedAt: "2026-04-10T12:00:00.000Z",
      rawMethod: "serverRequest/resolved",
      threadId: "thread-1",
      turnId: "turn-1",
      itemId: "item-1",
      type: "approval",
      event: "resolved",
      requestId: "approval-1",
      approval: {
        requestId: "approval-1",
        kind: "fileChange",
        threadId: "thread-1",
        turnId: "turn-1",
        itemId: "item-1",
        supportedResolutions: ["approved", "declined", "cancelled"],
        reason: "Write files",
        grantRoot: "/tmp/project",
        rawRequest: {},
      },
      resolution: "declined",
    });
  });
});
