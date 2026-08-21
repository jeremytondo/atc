import type { SessionMessage } from "@anthropic-ai/claude-agent-sdk"
import { assert, describe, it } from "@effect/vitest"
import * as ClaudeItems from "../../src/agents/claudeItems.ts"

// The Claude SDK shape → vocabulary mapping table (ATC-193). Pure functions:
// no fixture query, no SDK.

const turnId = "claude-turn-1"
const openedAt = "2026-08-17T00:00:00.000Z"

describe("ClaudeItems.toolItem / completeToolItem", () => {
  it("Bash is a command; its result is the output", () => {
    const item = ClaudeItems.toolItem(
      { id: "tu1", name: "Bash", input: { command: "bun test\n# more", cwd: "/w" } },
      turnId,
      null,
      "running",
    )
    assert.deepStrictEqual(item, {
      type: "command",
      id: "tu1",
      turnId,
      title: "bun test",
      status: "running",
      command: "bun test\n# more",
      cwd: "/w",
    })
    if (item.type !== "command") return assert.fail("expected a command")
    assert.deepStrictEqual(ClaudeItems.completeToolItem(item, { content: "ok\n" }, undefined), {
      ...item,
      status: "completed" as const,
      output: "ok\n",
    })
    assert.deepStrictEqual(
      ClaudeItems.completeToolItem(item, { content: "boom", is_error: true }, undefined),
      { ...item, status: "error" as const, error: "boom", output: "boom" },
    )
  })

  it("edit tools are fileChanges; the structured result supplies kind and diff", () => {
    const item = ClaudeItems.toolItem(
      { id: "tu2", name: "Write", input: { file_path: "/w/new.ts", content: "x" } },
      turnId,
      "parent-tool",
      "pending",
    )
    assert.deepStrictEqual(item, {
      type: "fileChange",
      id: "tu2",
      turnId,
      parentItemId: "parent-tool",
      title: "Edit new.ts",
      status: "pending",
      changes: [{ path: "/w/new.ts", kind: "update" }],
    })
    if (item.type !== "fileChange") return assert.fail("expected a fileChange")
    const completed = ClaudeItems.completeToolItem(
      item,
      { content: "File created successfully" },
      {
        type: "create",
        filePath: "/w/new.ts",
        content: "x",
        structuredPatch: [{ oldStart: 0, oldLines: 0, newStart: 1, newLines: 1, lines: ["+x"] }],
      },
    )
    assert.deepStrictEqual(completed, {
      ...item,
      status: "completed" as const,
      title: "Create new.ts",
      changes: [{ path: "/w/new.ts", kind: "add", diff: "@@ -0,0 +1,1 @@\n+x" }],
    })
    // Without a structured result (history) the change keeps its safe kind.
    const plain = ClaudeItems.completeToolItem(item, { content: "ok" }, undefined)
    assert.deepStrictEqual(plain.type === "fileChange" ? plain.changes : [], [
      { path: "/w/new.ts", kind: "update" },
    ])
  })

  it("mcp__server__tool is an mcpCall; anything else is a titled toolCall", () => {
    const mcp = ClaudeItems.toolItem(
      { id: "tu3", name: "mcp__linear-server__get_issue", input: { id: "ATC-1" } },
      turnId,
      null,
      "running",
    )
    assert.deepStrictEqual(mcp, {
      type: "mcpCall",
      id: "tu3",
      turnId,
      title: "linear-server · get_issue",
      status: "running",
      server: "linear-server",
      tool: "get_issue",
      arguments: { id: "ATC-1" },
    })
    if (mcp.type !== "mcpCall") return assert.fail("expected an mcpCall")
    assert.deepStrictEqual(
      ClaudeItems.completeToolItem(mcp, { content: [{ type: "text", text: "{}" }] }, undefined),
      { ...mcp, status: "completed" as const, result: [{ type: "text", text: "{}" }] },
    )
    const read = ClaudeItems.toolItem(
      { id: "tu4", name: "Read", input: { file_path: "/w/a.ts" } },
      turnId,
      null,
      "running",
    )
    assert.strictEqual(read.type === "toolCall" ? read.title : "", "Read a.ts")
    const task = ClaudeItems.toolItem(
      { id: "tu5", name: "Task", input: { description: "survey tests", prompt: "…" } },
      turnId,
      null,
      "running",
    )
    assert.strictEqual(task.type === "toolCall" ? task.title : "", "survey tests")
    // The structured output wins over the text content when present.
    const done = ClaudeItems.completeToolItem(read, { content: "text" }, { file: { content: "x" } })
    assert.deepStrictEqual(done.type === "toolCall" ? done.output : undefined, {
      file: { content: "x" },
    })
  })
})

describe("ClaudeItems.mapRequest / permissionResult", () => {
  const context = { requestId: "req-1", toolUseID: "tu-1" }

  it("Bash asks are command approvals; edit tools fileChange; others tool", () => {
    assert.deepStrictEqual(
      ClaudeItems.mapRequest(
        "Bash",
        { command: "rm x" },
        { ...context, title: "Claude wants to run rm x", decisionReason: "not allowed" },
        turnId,
        openedAt,
      ),
      {
        kind: "approval",
        id: "req-1",
        turnId,
        itemId: "tu-1",
        openedAt,
        title: "Claude wants to run rm x",
        reason: "not allowed",
        subject: { type: "command", command: "rm x" },
      },
    )
    const edit = ClaudeItems.mapRequest("Edit", { file_path: "/w/a.ts" }, context, turnId, openedAt)
    assert.deepStrictEqual(edit.kind === "approval" ? edit.subject : undefined, {
      type: "fileChange",
      changes: [{ path: "/w/a.ts", kind: "update" }],
    })
    assert.strictEqual(edit.kind === "approval" ? edit.title : "", "Edit a.ts")
    const fetch = ClaudeItems.mapRequest(
      "WebFetch",
      { url: "https://x" },
      context,
      turnId,
      openedAt,
    )
    assert.deepStrictEqual(fetch.kind === "approval" ? fetch.subject : undefined, {
      type: "tool",
      name: "WebFetch",
      input: { url: "https://x" },
    })
  })

  it("decisions map onto PermissionResults; session grants reuse the SDK's suggestions", () => {
    const request = ClaudeItems.mapRequest(
      "WebFetch",
      { url: "https://x" },
      context,
      turnId,
      openedAt,
    )
    const decide = (
      decision: "accept" | "acceptForSession" | "decline" | "cancel",
      suggestions = [],
    ) =>
      ClaudeItems.permissionResult(
        request,
        { kind: "approval", decision },
        { url: "https://x" },
        suggestions,
      )
    assert.deepStrictEqual(decide("accept"), { behavior: "allow" })
    assert.deepStrictEqual(decide("decline"), { behavior: "deny", message: "declined" })
    assert.deepStrictEqual(decide("cancel"), {
      behavior: "deny",
      message: "cancelled",
      interrupt: true,
    })
    assert.deepStrictEqual(decide("acceptForSession"), {
      behavior: "allow",
      updatedPermissions: [
        {
          type: "addRules",
          rules: [{ toolName: "WebFetch" }],
          behavior: "allow",
          destination: "session",
        },
      ],
    })
    const suggestion = {
      type: "addRules" as const,
      rules: [{ toolName: "WebFetch", ruleContent: "domain:x" }],
      behavior: "allow" as const,
      destination: "session" as const,
    }
    assert.deepStrictEqual(
      ClaudeItems.permissionResult(
        request,
        { kind: "approval", decision: "acceptForSession" },
        {},
        [suggestion],
      ),
      { behavior: "allow", updatedPermissions: [suggestion] },
    )
  })

  it("AskUserQuestion is a question; answers return keyed by question text", () => {
    const input = {
      questions: [
        {
          question: "Which?",
          header: "Pick",
          options: [{ label: "a", description: "A" }, { label: "b" }],
          multiSelect: true,
        },
      ],
    }
    const request = ClaudeItems.mapRequest("AskUserQuestion", input, context, turnId, openedAt)
    assert.deepStrictEqual(request, {
      kind: "question",
      id: "req-1",
      turnId,
      itemId: "tu-1",
      openedAt,
      questions: [
        {
          id: "q0",
          header: "Pick",
          question: "Which?",
          options: [
            { label: "a", description: "A" },
            { label: "b", description: "" },
          ],
          multiSelect: true,
          freeform: true,
          secret: false,
        },
      ],
    })
    assert.deepStrictEqual(
      ClaudeItems.permissionResult(
        request,
        { kind: "question", answers: { q0: ["a", "b"] } },
        input,
        [],
      ),
      { behavior: "allow", updatedInput: { ...input, answers: { "Which?": "a, b" } } },
    )
  })
})

describe("ClaudeItems.mapHistory", () => {
  const message = (
    type: "user" | "assistant" | "system",
    content: unknown,
    uuid: string,
    extra: Partial<SessionMessage> & { timestamp?: string } = {},
  ): SessionMessage =>
    ({
      type,
      uuid,
      session_id: "s",
      message: { role: type, content },
      parent_tool_use_id: null,
      parent_agent_id: null,
      ...extra,
    }) as SessionMessage

  it("an image-only prompt opens a turn with empty text (ATC-216)", () => {
    const history = ClaudeItems.mapHistory([
      message("user", "look", "u1"),
      message("assistant", [{ type: "text", text: "ok" }], "a1"),
      message(
        "user",
        [{ type: "image", source: { type: "base64", media_type: "image/png", data: "AAAA" } }],
        "u2",
      ),
      message("assistant", [{ type: "text", text: "a picture" }], "a2"),
    ])
    assert.deepStrictEqual(
      history.map((turn) => ({ id: turn.turn.id, items: turn.items.map((item) => item.id) })),
      [
        { id: "u1", items: ["u1:0", "a1:0"] },
        { id: "u2", items: ["u2:0", "a2:0"] },
      ],
    )
    assert.deepStrictEqual(history[1]?.items[0], {
      type: "userMessage",
      id: "u2:0",
      turnId: "u2",
      text: "",
    })
  })

  it("groups at each user text message, links tool results, and reads incomplete turns as interrupted", () => {
    const history = ClaudeItems.mapHistory([
      message("user", "first prompt", "u1", { timestamp: "2026-08-17T10:00:00.000Z" }),
      message("assistant", [{ type: "thinking", thinking: "hmm" }], "a1", {
        timestamp: "2026-08-17T10:00:01.000Z",
      }),
      message(
        "assistant",
        [{ type: "tool_use", id: "t1", name: "Read", input: { file_path: "/w/x" } }],
        "a2",
      ),
      message("user", [{ type: "tool_result", tool_use_id: "t1", content: "contents" }], "u2"),
      message("assistant", [{ type: "text", text: "done" }], "a3", {
        timestamp: "2026-08-17T10:00:05.000Z",
      }),
      // A subagent's message never reaches the top-level transcript.
      message("assistant", [{ type: "text", text: "nested" }], "a4", { parent_tool_use_id: "t1" }),
      message("system", undefined, "sys1"),
      message("user", [{ type: "text", text: "second prompt" }], "u3"),
      message(
        "assistant",
        [{ type: "tool_use", id: "t2", name: "Bash", input: { command: "ls" } }],
        "a5",
      ),
    ])
    assert.deepStrictEqual(history, [
      {
        turn: {
          id: "u1",
          status: "completed",
          startedAt: "2026-08-17T10:00:00.000Z",
          endedAt: "2026-08-17T10:00:05.000Z",
        },
        items: [
          { type: "userMessage", id: "u1:0", turnId: "u1", text: "first prompt" },
          { type: "reasoning", id: "a1:0", turnId: "u1", text: "hmm", complete: true },
          {
            type: "toolCall",
            id: "t1",
            turnId: "u1",
            title: "Read x",
            status: "completed",
            name: "Read",
            input: { file_path: "/w/x" },
            output: "contents",
          },
          { type: "assistantText", id: "a3:0", turnId: "u1", text: "done", complete: true },
        ],
      },
      {
        turn: { id: "u3", status: "interrupted" },
        items: [
          { type: "userMessage", id: "u3:0", turnId: "u3", text: "second prompt" },
          {
            type: "command",
            id: "t2",
            turnId: "u3",
            title: "ls",
            status: "error",
            error: "no result recorded",
            command: "ls",
          },
        ],
      },
    ])
  })

  it("an empty or result-only transcript yields no turns", () => {
    assert.deepStrictEqual(ClaudeItems.mapHistory([]), [])
    assert.deepStrictEqual(
      ClaudeItems.mapHistory([
        message("user", [{ type: "tool_result", tool_use_id: "t9", content: "x" }], "u9"),
      ]),
      [],
    )
  })
})
