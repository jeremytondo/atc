import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import * as CodexItems from "../../src/agents/codexItems.ts"

// The Codex wire-shape → vocabulary mapping table (ATC-193), pinned against
// the codex 0.147 generated protocol shapes. Pure functions: no fixture.

const turnId = "turn-1"

describe("CodexItems.mapItem", () => {
  it("userMessage joins its text inputs and skips non-text inputs", () => {
    const item = CodexItems.mapItem(
      {
        type: "userMessage",
        id: "u1",
        clientId: null,
        content: [
          { type: "text", text: "hello", text_elements: [] },
          { type: "image", url: "data:…" },
          { type: "text", text: "world", text_elements: [] },
        ],
      },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(item, { type: "userMessage", id: "u1", turnId, text: "hello\nworld" })
  })

  it("agentMessage streams (started = incomplete) and carries its phase as provider metadata", () => {
    const started = CodexItems.mapItem(
      { type: "agentMessage", id: "a1", text: "", phase: null, memoryCitation: null },
      turnId,
      "started",
    )
    assert.deepStrictEqual(started, {
      type: "assistantText",
      id: "a1",
      turnId,
      text: "",
      complete: false,
    })
    const completed = CodexItems.mapItem(
      { type: "agentMessage", id: "a1", text: "done", phase: "final_answer", memoryCitation: null },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(completed, {
      type: "assistantText",
      id: "a1",
      turnId,
      text: "done",
      complete: true,
      providerMetadata: { codex: { phase: "final_answer" } },
    })
  })

  it("plan is assistant text marked as a plan", () => {
    assert.deepStrictEqual(
      CodexItems.mapItem({ type: "plan", id: "p1", text: "1. do" }, turnId, "completed"),
      {
        type: "assistantText",
        id: "p1",
        turnId,
        text: "1. do",
        complete: true,
        providerMetadata: { codex: { plan: true } },
      },
    )
  })

  it("reasoning shows the summary and keeps raw content as metadata; raw-only falls back", () => {
    assert.deepStrictEqual(
      CodexItems.mapItem(
        { type: "reasoning", id: "r1", summary: ["first", "second"], content: ["raw"] },
        turnId,
        "completed",
      ),
      {
        type: "reasoning",
        id: "r1",
        turnId,
        text: "first\n\nsecond",
        complete: true,
        providerMetadata: { codex: { content: ["raw"] } },
      },
    )
    assert.deepStrictEqual(
      CodexItems.mapItem(
        { type: "reasoning", id: "r2", summary: [], content: ["raw"] },
        turnId,
        "started",
      ),
      { type: "reasoning", id: "r2", turnId, text: "raw", complete: false },
    )
  })

  it("commandExecution maps status, output, exit code, and duration", () => {
    const base = {
      type: "commandExecution",
      id: "c1",
      pluginId: null,
      scriptPath: null,
      command: "bun test\n--filter x",
      cwd: "/work",
      processId: null,
      source: "agent",
      commandActions: [],
      aggregatedOutput: null,
      exitCode: null,
      durationMs: null,
    }
    assert.deepStrictEqual(
      CodexItems.mapItem({ ...base, status: "inProgress" }, turnId, "started"),
      {
        type: "command",
        id: "c1",
        turnId,
        title: "bun test",
        status: "running",
        command: "bun test\n--filter x",
        cwd: "/work",
      },
    )
    assert.deepStrictEqual(
      CodexItems.mapItem(
        { ...base, status: "completed", aggregatedOutput: "ok\n", exitCode: 0, durationMs: 40 },
        turnId,
        "completed",
      ),
      {
        type: "command",
        id: "c1",
        turnId,
        title: "bun test",
        status: "completed",
        command: "bun test\n--filter x",
        cwd: "/work",
        output: "ok\n",
        exitCode: 0,
        providerMetadata: { codex: { durationMs: 40 } },
      },
    )
    const declined = CodexItems.mapItem({ ...base, status: "declined" }, turnId, "completed")
    assert.strictEqual(declined?.type === "command" ? declined.status : "", "error")
    assert.strictEqual(declined?.type === "command" ? declined.error : "", "declined")
    const failed = CodexItems.mapItem(
      { ...base, status: "failed", exitCode: 1 },
      turnId,
      "completed",
    )
    assert.strictEqual(failed?.type === "command" ? failed.error : "", "failed")
  })

  it("fileChange maps kinds 1:1, keeps diffs, and titles by verb and count", () => {
    const item = CodexItems.mapItem(
      {
        type: "fileChange",
        id: "f1",
        status: "completed",
        changes: [
          { path: "/w/a.ts", kind: { type: "update", move_path: null }, diff: "@@ -1 +1 @@" },
          { path: "/w/b.ts", kind: { type: "add" }, diff: "+new" },
        ],
      },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(item, {
      type: "fileChange",
      id: "f1",
      turnId,
      title: "Edit 2 files",
      status: "completed",
      changes: [
        { path: "/w/a.ts", kind: "update", diff: "@@ -1 +1 @@" },
        { path: "/w/b.ts", kind: "add", diff: "+new" },
      ],
    })
    const single = CodexItems.mapItem(
      {
        type: "fileChange",
        id: "f2",
        status: "inProgress",
        changes: [{ path: "/w/new.ts", kind: { type: "add" }, diff: "+x" }],
      },
      turnId,
      "started",
    )
    assert.strictEqual(single?.type === "fileChange" ? single.title : "", "Create new.ts")
  })

  it("mcpToolCall carries server, tool, arguments, result, and error", () => {
    const item = CodexItems.mapItem(
      {
        type: "mcpToolCall",
        id: "m1",
        server: "linear",
        tool: "get_issue",
        status: "failed",
        arguments: { id: "ATC-1" },
        appContext: null,
        pluginId: null,
        readOnlyHint: null,
        result: null,
        error: { message: "boom" },
        durationMs: null,
      },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(item, {
      type: "mcpCall",
      id: "m1",
      turnId,
      title: "linear · get_issue",
      status: "error",
      error: "boom",
      server: "linear",
      tool: "get_issue",
      arguments: { id: "ATC-1" },
    })
    const ok = CodexItems.mapItem(
      {
        type: "mcpToolCall",
        id: "m2",
        server: "linear",
        tool: "get_issue",
        status: "completed",
        arguments: {},
        result: { content: [{ type: "text", text: "…" }], structuredContent: null, _meta: null },
        error: null,
      },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(ok?.type === "mcpCall" ? ok.result : undefined, {
      content: [{ type: "text", text: "…" }],
      structuredContent: null,
      _meta: null,
    })
  })

  it("every other tool-like item is a toolCall named after the codex type", () => {
    const search = CodexItems.mapItem(
      { type: "webSearch", id: "w1", query: "effect v4", action: null, results: null },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(search, {
      type: "toolCall",
      id: "w1",
      turnId,
      title: "Search: effect v4",
      status: "completed",
      name: "webSearch",
      input: { query: "effect v4", action: null, results: null },
    })
    const dynamic = CodexItems.mapItem(
      {
        type: "dynamicToolCall",
        id: "d1",
        namespace: null,
        tool: "lint",
        arguments: { fix: true },
        status: "inProgress",
        contentItems: null,
        success: null,
        durationMs: null,
      },
      turnId,
      "started",
    )
    assert.strictEqual(dynamic?.type === "toolCall" ? dynamic.status : "", "running")
    assert.strictEqual(dynamic?.type === "toolCall" ? dynamic.title : "", "lint")
    const sub = CodexItems.mapItem(
      {
        type: "subAgentActivity",
        id: "s1",
        kind: "started",
        agentThreadId: "child",
        agentPath: "/r/c",
      },
      turnId,
      "started",
    )
    assert.strictEqual(sub?.type === "toolCall" ? sub.title : "", "Subagent started")
    // A type this build has never heard of still surfaces.
    const novel = CodexItems.mapItem(
      { type: "holographicCall", id: "h1", foo: 1 },
      turnId,
      "completed",
    )
    assert.deepStrictEqual(novel, {
      type: "toolCall",
      id: "h1",
      turnId,
      title: "holographicCall",
      status: "completed",
      name: "holographicCall",
      input: { foo: 1 },
    })
  })

  it("compaction is a marker; hookPrompt and review markers are not emitted; junk is skipped", () => {
    assert.deepStrictEqual(
      CodexItems.mapItem({ type: "contextCompaction", id: "k1" }, turnId, "completed"),
      { type: "compaction", id: "k1", turnId },
    )
    assert.isNull(
      CodexItems.mapItem({ type: "hookPrompt", id: "h", fragments: [] }, turnId, "completed"),
    )
    assert.isNull(
      CodexItems.mapItem({ type: "enteredReviewMode", id: "e", review: "" }, turnId, "completed"),
    )
    assert.isNull(CodexItems.mapItem({ type: "agentMessage" }, turnId, "completed"))
    assert.isNull(CodexItems.mapItem("nonsense", turnId, "completed"))
  })
})

describe("CodexItems.mapRequest / answerResult", () => {
  const openedAt = "2026-08-17T00:00:00.000Z"

  it("command approvals carry the command, cwd, and reason", () => {
    const request = CodexItems.mapRequest(
      "item/commandExecution/requestApproval",
      "7",
      {
        threadId: "t",
        turnId,
        itemId: "c1",
        startedAtMs: 1,
        approvalId: null,
        environmentId: null,
        reason: "network",
        command: "curl x",
        cwd: "/w",
      },
      undefined,
      openedAt,
    )
    assert.deepStrictEqual(request, {
      kind: "approval",
      id: "7",
      turnId,
      itemId: "c1",
      openedAt,
      title: "curl x",
      reason: "network",
      subject: { type: "command", command: "curl x", cwd: "/w" },
    })
    assert.deepStrictEqual(
      CodexItems.answerResult({ kind: "approval", decision: "acceptForSession" }),
      { decision: "acceptForSession" },
    )
  })

  it("fileChange approvals take their changes from the pending item", () => {
    const pending = CodexItems.mapItem(
      {
        type: "fileChange",
        id: "f1",
        status: "inProgress",
        changes: [{ path: "/w/a.ts", kind: { type: "update", move_path: null }, diff: "d" }],
      },
      turnId,
      "started",
    )
    const request = CodexItems.mapRequest(
      "item/fileChange/requestApproval",
      "8",
      { threadId: "t", turnId, itemId: "f1", startedAtMs: 1, reason: null },
      pending ?? undefined,
      openedAt,
    )
    assert.deepStrictEqual(request, {
      kind: "approval",
      id: "8",
      turnId,
      itemId: "f1",
      openedAt,
      title: "Edit a.ts",
      subject: { type: "fileChange", changes: [{ path: "/w/a.ts", kind: "update", diff: "d" }] },
    })
  })

  it("requestUserInput becomes a question with freeform where the provider allows it", () => {
    const request = CodexItems.mapRequest(
      "item/tool/requestUserInput",
      "9",
      {
        threadId: "t",
        turnId,
        itemId: "q1",
        isBlocking: true,
        autoResolutionMs: null,
        questions: [
          {
            id: "color",
            header: "Color",
            question: "Which?",
            isOther: true,
            isSecret: false,
            options: [{ label: "red", description: "warm" }],
          },
          {
            id: "note",
            header: "Note",
            question: "Notes?",
            isOther: false,
            isSecret: true,
            options: null,
          },
        ],
      },
      undefined,
      openedAt,
    )
    assert.deepStrictEqual(request, {
      kind: "question",
      id: "9",
      turnId,
      itemId: "q1",
      openedAt,
      questions: [
        {
          id: "color",
          header: "Color",
          question: "Which?",
          options: [{ label: "red", description: "warm" }],
          multiSelect: false,
          freeform: true,
          secret: false,
        },
        {
          id: "note",
          header: "Note",
          question: "Notes?",
          options: [],
          multiSelect: false,
          freeform: true,
          secret: true,
        },
      ],
    })
    assert.deepStrictEqual(
      CodexItems.answerResult({ kind: "question", answers: { color: ["red"], note: ["fine"] } }),
      { answers: { color: { answers: ["red"] }, note: { answers: ["fine"] } } },
    )
  })

  it("unsurfaced request kinds and malformed params are null", () => {
    assert.isNull(
      CodexItems.mapRequest(
        "item/permissions/requestApproval",
        "1",
        { threadId: "t", turnId },
        undefined,
        openedAt,
      ),
    )
    assert.isNull(CodexItems.mapRequest("item/tool/requestUserInput", "1", {}, undefined, openedAt))
  })
})

describe("CodexItems.mapHistory", () => {
  it.effect("thread/resume turns become HistoryTurns with ISO stamps and mapped items", () =>
    Effect.gen(function* () {
      const turns = yield* CodexItems.decodeHistoryTurns([
        {
          id: "t1",
          status: "completed",
          error: null,
          startedAt: 1_700_000_000,
          completedAt: 1_700_000_010,
          items: [
            {
              type: "userMessage",
              id: "u",
              clientId: null,
              content: [{ type: "text", text: "hi", text_elements: [] }],
            },
            { type: "hookPrompt", id: "h", fragments: [] },
            { type: "agentMessage", id: "a", text: "hello", phase: null, memoryCitation: null },
          ],
        },
        {
          id: "t2",
          status: "failed",
          error: { message: "quota" },
          startedAt: null,
          completedAt: null,
          items: [],
        },
        {
          id: "t3",
          status: "inProgress",
          error: null,
          startedAt: null,
          completedAt: null,
          items: [],
        },
      ])
      assert.deepStrictEqual(CodexItems.mapHistory(turns), [
        {
          turn: {
            id: "t1",
            status: "completed",
            startedAt: "2023-11-14T22:13:20.000Z",
            endedAt: "2023-11-14T22:13:30.000Z",
          },
          items: [
            { type: "userMessage", id: "u", turnId: "t1", text: "hi" },
            { type: "assistantText", id: "a", turnId: "t1", text: "hello", complete: true },
          ],
        },
        { turn: { id: "t2", status: "failed", error: "quota" }, items: [] },
        { turn: { id: "t3", status: "running" }, items: [] },
      ])
    }),
  )
})
