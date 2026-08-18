import type { SessionMessage } from "@anthropic-ai/claude-agent-sdk"
import { assert, describe, it } from "@effect/vitest"
import { Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import type { AgentSessionEvent } from "../../src/agents/agentAdapter.ts"
import * as ClaudeAdapter from "../../src/agents/claudeAdapter.ts"
import * as ClaudeHooks from "../../src/agents/claudeHooks.ts"
import { claudeAdapterLayer, collectAgentEvents, waitForAgentEvent } from "./agentTestKit.ts"
import { eventually } from "../testLayers.ts"
import { trackTempDir } from "../blackbox.ts"
import { makeFakeClaudeQuery } from "./fakeClaudeQuery.ts"
import type { FakeClaudeQueryOptions } from "./fakeClaudeQuery.ts"

// Claude adapter tests over the scripted query() seam (fakeClaudeQuery.ts):
// the SDK's probed behaviors — init only after the first message, invalid
// resume as a single error result, iterator throw after error results —
// drive the adapter's deferred verification and truthful turn outcomes.
// claudeExecutable points at /bin/echo so resolution succeeds without a
// Claude install (the fake never spawns anything).

const workDir = () => trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-test-")))

const adapterStack = (options: FakeClaudeQueryOptions = {}) => {
  const fake = makeFakeClaudeQuery(options)
  const layer = claudeAdapterLayer({ queryFn: fake.queryFn, initTimeout: "5 seconds" })
  return { fake, layer }
}

describe("ClaudeAdapter", () => {
  it.live("create: identity verified from init, turn completes on the feed", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack({ sessionId: "session-a" })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "hello" })
            assert.strictEqual(connection.providerSessionId, "session-a")
            assert.strictEqual(connection.cwd, cwd)
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "completed",
            )
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("create with a lying cwd fails closed", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack({ wrongCwd: true })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.scoped(
          Effect.flip(adapter.createSession({ cwd: workDir(), input: "hello" })),
        )
        assert.strictEqual(failure._tag, "AgentIdentityMismatch")
        if (failure._tag === "AgentIdentityMismatch") assert.strictEqual(failure.field, "cwd")
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("resume: deferred verification completes at the first turn", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack({ knownSessions: ["session-b"] })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: "session-b",
              cwd,
            })
            // No identity evidence yet — the SDK sends none until a turn.
            assert.strictEqual(yield* connection.activity, "unknown")
            const sink = yield* collectAgentEvents(connection.events)
            const turn = yield* connection.startTurn("continue please")
            assert.strictEqual(connection.providerSessionId, "session-b")
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "completed",
            )
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("resume of an unknown session fails closed at the first turn", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack({ knownSessions: [] })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: "missing-session",
              cwd: workDir(),
            })
            return yield* Effect.flip(connection.startTurn("hello?"))
          }),
        )
        assert.strictEqual(failure._tag, "AgentResumeFailed")
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a disagreeing session id in the evidence is a mismatch, never adopted", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack({
        knownSessions: ["session-c"],
        reportSessionId: "some-other-session",
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: "session-c",
              cwd: workDir(),
            })
            return yield* Effect.flip(connection.startTurn("hello?"))
          }),
        )
        assert.strictEqual(failure._tag, "AgentIdentityMismatch")
        if (failure._tag === "AgentIdentityMismatch") {
          assert.strictEqual(failure.field, "sessionId")
        }
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("one writer per session id", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack({ knownSessions: ["session-d"] })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            yield* adapter.resumeSession({ providerSessionId: "session-d", cwd })
            const failure = yield* Effect.flip(
              adapter.resumeSession({ providerSessionId: "session-d", cwd }),
            )
            assert.strictEqual(failure._tag, "AgentConflict")
          }),
        )
        // The writer role is released with the scope.
        yield* Effect.scoped(adapter.resumeSession({ providerSessionId: "session-d", cwd }))
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("interrupt: truthful interrupted outcome, then the connection ends", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "HANG until told otherwise",
            })
            const sink = yield* collectAgentEvents(connection.events)
            const stale = yield* Effect.flip(connection.interrupt({ turnId: "wrong-turn" }))
            assert.strictEqual(stale._tag, "AgentConflict")

            yield* connection.interrupt(turn)
            assert.deepStrictEqual(fake.interrupts.length, 1)
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "interrupted",
            )
            // An interrupted turn ends the held query; the session is over
            // (AgentUnavailable — the seam's "resume it" signal), not a
            // caller-side handle conflict.
            const closed = yield* Effect.flip(connection.startTurn("another"))
            assert.strictEqual(closed._tag, "AgentUnavailable")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("an error result is a failed turn, not a transport defect", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "FAILTURN please",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "failed" &&
                (event.detail ?? "").includes("error_max_turns"),
            )
            // Error-path idle derived from the result message itself.
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("permission callbacks park as approval requests until answered", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "needs PERMISSION for a tool",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(sink, (event) => event.type === "requestOpened")
            const opened = sink.find((event) => event.type === "requestOpened")
            if (opened?.type !== "requestOpened" || opened.request.kind !== "approval") {
              return assert.fail("expected an approval request")
            }
            // The SDK's pre-rendered title and reason ride the request; the
            // Bash input becomes a command subject naming the tool item.
            assert.strictEqual(opened.request.title, "Claude wants to run pwd")
            assert.strictEqual(opened.request.reason, "outside the allow list")
            assert.deepStrictEqual(opened.request.subject, { type: "command", command: "pwd" })
            assert.strictEqual(opened.request.itemId, "fake-tool-use-1")
            // The tool item is pending while its approval parks (whether the
            // ask or the tool_use block landed first) — nothing has been
            // denied yet.
            yield* waitForAgentEvent(
              sink,
              (event) =>
                (event.type === "itemStarted" || event.type === "itemUpdated") &&
                event.item.id === "fake-tool-use-1" &&
                event.item.type === "command" &&
                event.item.status === "pending",
            )
            assert.deepStrictEqual(fake.decisions, [])
            assert.isTrue(
              sink.some((event) => event.type === "activity" && event.activity === "needs_input"),
            )
            yield* connection.respond(opened.request.id, { kind: "approval", decision: "accept" })
            yield* waitForAgentEvent(sink, (event) => event.type === "requestClosed")
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.deepStrictEqual(fake.decisions, [
              { toolName: "Bash", result: { behavior: "allow" } },
            ])
            // Accepted: the command ran and its result completed the item.
            const completed = sink.find(
              (event) => event.type === "itemCompleted" && event.item.id === "fake-tool-use-1",
            )
            assert.isTrue(
              completed?.type === "itemCompleted" &&
                completed.item.type === "command" &&
                completed.item.status === "completed" &&
                completed.item.output === "/work",
            )
            // Answered requests are gone: a second answer is a conflict.
            const again = yield* Effect.flip(
              connection.respond(opened.request.id, { kind: "approval", decision: "accept" }),
            )
            assert.strictEqual(again._tag, "AgentConflict")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a declined approval fails the tool item; a cancel interrupts the turn", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "needs PERMISSION for a tool",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(sink, (event) => event.type === "requestOpened")
            const opened = sink.find((event) => event.type === "requestOpened")
            if (opened?.type !== "requestOpened") return assert.fail("expected a request")
            // Kind mismatch is a conflict, and leaves the request parked.
            const mismatch = yield* Effect.flip(
              connection.respond(opened.request.id, { kind: "question", answers: {} }),
            )
            assert.strictEqual(mismatch._tag, "AgentConflict")
            yield* connection.respond(opened.request.id, { kind: "approval", decision: "decline" })
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.deepStrictEqual(fake.decisions, [
              { toolName: "Bash", result: { behavior: "deny", message: "declined" } },
            ])
            const completed = sink.find(
              (event) => event.type === "itemCompleted" && event.item.id === "fake-tool-use-1",
            )
            assert.isTrue(
              completed?.type === "itemCompleted" &&
                completed.item.type === "command" &&
                completed.item.status === "error",
            )
          }),
        )
        // cancel = deny + interrupt: the turn ends interrupted (and, Claude
        // being Claude, the connection with it).
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "needs PERMISSION again",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(sink, (event) => event.type === "requestOpened")
            const opened = sink.find((event) => event.type === "requestOpened")
            if (opened?.type !== "requestOpened") return assert.fail("expected a request")
            yield* connection.respond(opened.request.id, { kind: "approval", decision: "cancel" })
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "interrupted",
            )
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("assistant text streams as content blocks; the per-block echo never duplicates it", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "STREAM please",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            const items = sink.flatMap((event) =>
              event.type === "itemStarted" ||
              event.type === "itemCompleted" ||
              event.type === "textDelta"
                ? [event]
                : [],
            )
            // The prompt is the turn's first item, keyed by the turn.
            assert.deepStrictEqual(items[0], {
              type: "itemCompleted",
              item: {
                type: "userMessage",
                id: `${turn.turnId}:prompt`,
                turnId: turn.turnId,
                text: "STREAM please",
              },
            })
            // Then the streamed block: keyed by API message id + block index.
            assert.deepStrictEqual(items.slice(1), [
              {
                type: "itemStarted",
                item: {
                  type: "assistantText",
                  id: "msg_fake_1:0",
                  turnId: turn.turnId,
                  text: "",
                  complete: false,
                },
              },
              { type: "textDelta", itemId: "msg_fake_1:0", delta: "fake: " },
              { type: "textDelta", itemId: "msg_fake_1:0", delta: "STREAM please" },
              {
                type: "itemCompleted",
                item: {
                  type: "assistantText",
                  id: "msg_fake_1:0",
                  turnId: turn.turnId,
                  text: "fake: STREAM please",
                  complete: true,
                },
              },
            ])
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("tool_use blocks open tool items and their results complete them", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "use a TOOL",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            const tool = sink.flatMap((event) =>
              (event.type === "itemStarted" || event.type === "itemCompleted") &&
              event.item.id === "toolu_fake_read"
                ? [event]
                : [],
            )
            assert.deepStrictEqual(tool, [
              {
                type: "itemStarted",
                item: {
                  type: "toolCall",
                  id: "toolu_fake_read",
                  turnId: turn.turnId,
                  title: "Read notes.md",
                  status: "running",
                  name: "Read",
                  input: { file_path: "/work/notes.md" },
                },
              },
              {
                type: "itemCompleted",
                item: {
                  type: "toolCall",
                  id: "toolu_fake_read",
                  turnId: turn.turnId,
                  title: "Read notes.md",
                  status: "completed",
                  name: "Read",
                  input: { file_path: "/work/notes.md" },
                  output: { file: { filePath: "/work/notes.md", content: "hello" } },
                },
              },
            ])
            // The un-streamed final text still lands, keyed by its message uuid.
            const text = sink.find(
              (event) => event.type === "itemCompleted" && event.item.type === "assistantText",
            )
            assert.isTrue(
              text?.type === "itemCompleted" &&
                text.item.type === "assistantText" &&
                text.item.text === "fake: use a TOOL" &&
                text.item.complete,
            )
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("in-process hook callbacks alone still drive the activity feed", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      // No session_state_changed events at all: activity must come from
      // the SDK hook callbacks (UserPromptSubmit → working, Stop → idle).
      const { layer } = adapterStack({ stateEvents: false })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "hello" })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.isTrue(
              sink.some((event) => event.type === "activity" && event.activity === "working"),
            )
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  // ATC-158: the feed carries the session TREE's aggregate. The scripted
  // payloads replay the recorded background-subagent evidence
  // (fixtures/claude-background-hook-payloads.json shapes).
  it.live("background work keeps the aggregate busy after the root stops", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack({ sessionId: "session-bg" })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "spawn" })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            // The recorded shape: the root's Stop carries a live background
            // subagent in its level snapshot — the aggregate stays working.
            yield* Effect.promise(() =>
              fake.fireHook("Stop", {
                background_tasks: [
                  { id: "bg1", type: "subagent", status: "running", description: "worker" },
                ],
                session_crons: [],
              }),
            )
            assert.strictEqual(yield* connection.activity, "working")
            // A descendant waiting on permission surfaces needs_input...
            yield* Effect.promise(() => fake.fireHook("PermissionRequest", { agent_id: "bg1" }))
            assert.strictEqual(yield* connection.activity, "needs_input")
            // ...and clears it when it proceeds.
            yield* Effect.promise(() =>
              fake.fireHook("PostToolUse", { agent_id: "bg1", tool_name: "Bash" }),
            )
            assert.strictEqual(yield* connection.activity, "working")
            // SubagentStop's snapshot still contains the stopping agent
            // (probed): subtracting it lands the last-child idle.
            yield* Effect.promise(() =>
              fake.fireHook("SubagentStop", {
                agent_id: "bg1",
                background_tasks: [{ id: "bg1", type: "subagent", status: "running" }],
                session_crons: [],
              }),
            )
            assert.strictEqual(yield* connection.activity, "idle")
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "activity" && event.activity === "idle",
            )
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a success result never clobbers live background evidence", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack({ sessionId: "session-bg2" })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "hello" })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            yield* Effect.promise(() => fake.fireHook("SubagentStart", { agent_id: "bg2" }))
            assert.strictEqual(yield* connection.activity, "working")
            // A whole further turn completes (Stop without a snapshot,
            // result success, state idle): the background evidence holds.
            const second = yield* connection.startTurn("again")
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === second.turnId,
            )
            assert.strictEqual(yield* connection.activity, "working")
            yield* Effect.promise(() => fake.fireHook("SubagentStop", { agent_id: "bg2" }))
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a pending session cron holds the aggregate busy until it is gone", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack({ sessionId: "session-cron" })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "hello" })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            yield* Effect.promise(() =>
              fake.fireHook("Stop", {
                background_tasks: [],
                session_crons: [{ id: "c1", schedule: "* * * * *", recurring: true, prompt: "x" }],
              }),
            )
            assert.strictEqual(yield* connection.activity, "working")
            yield* Effect.promise(() =>
              fake.fireHook("Stop", { background_tasks: [], session_crons: [] }),
            )
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("AskUserQuestion surfaces as a question; answers go back keyed by question text", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "ASK me something",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "requestOpened" && event.request.kind === "question",
            )
            const opened = sink.find((event) => event.type === "requestOpened")
            if (opened?.type !== "requestOpened" || opened.request.kind !== "question") {
              return assert.fail("expected a question request")
            }
            assert.deepStrictEqual(
              opened.request.questions.map((question) => [
                question.id,
                question.header,
                question.options.map((option) => option.label),
                question.freeform,
              ]),
              [
                ["q0", "Color", ["red", "blue"], true],
                ["q1", "Notes", [], true],
              ],
            )
            yield* connection.respond(opened.request.id, {
              kind: "question",
              answers: { q0: ["blue"], q1: ["ship it"] },
            })
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.deepStrictEqual(fake.decisions, [
              {
                toolName: "AskUserQuestion",
                result: {
                  behavior: "allow",
                  updatedInput: {
                    questions: [
                      {
                        question: "Which color?",
                        header: "Color",
                        options: [
                          { label: "red", description: "warm" },
                          { label: "blue", description: "cool" },
                        ],
                        multiSelect: false,
                      },
                      { question: "Any notes?", header: "Notes", options: [], multiSelect: false },
                    ],
                    answers: { "Which color?": "blue", "Any notes?": "ship it" },
                  },
                },
              },
            ])
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("tuiLaunch enables Remote Control and builds the hook plumbing", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const launch = yield* adapter.tuiLaunch({
          providerSessionId: "session-t",
          cwd: "/w",
          providerMetadata: undefined,
        })
        const spec = launch.launchSpec
        // A launch that minted the secret hands the metadata back so the
        // caller can persist it.
        assert.isString(launch.providerMetadata)
        // The settings live in a 0600 file, never in argv.
        const settingsFile = spec.command[4] ?? ""
        assert.deepStrictEqual(spec.command, [
          "/bin/echo",
          "--resume",
          "session-t",
          "--settings",
          settingsFile,
          "--remote-control",
        ])
        assert.strictEqual((fs.statSync(settingsFile).mode & 0o777).toString(8), "600")
        const settings = JSON.parse(fs.readFileSync(settingsFile, "utf8")) as {
          hooks: Record<string, Array<{ hooks: Array<{ command: string }> }>>
        }
        assert.includeMembers(Object.keys(settings.hooks), ["Stop", "PermissionRequest"])
        const command = settings.hooks["Stop"]?.[0]?.hooks[0]?.command ?? ""
        assert.include(command, "/internal/claude/hooks")
        // The secret lives only in the 0600 header file, never in the
        // curl command line itself.
        const headerFile = /-H @'([^']+)'/.exec(command)?.[1] ?? ""
        assert.strictEqual((fs.statSync(headerFile).mode & 0o777).toString(8), "600")
        const header = fs.readFileSync(headerFile, "utf8")
        const secret =
          new RegExp(`${ClaudeHooks.SECRET_HEADER}: ([a-f0-9]+)`).exec(header)?.[1] ?? ""
        assert.isAbove(secret.length, 30)
        assert.notInclude(command, secret)
        // The returned metadata carries exactly the registered secret.
        assert.strictEqual(
          (JSON.parse(launch.providerMetadata ?? "{}") as { hookSecret?: string }).hookSecret,
          secret,
        )
        // The secret is live: a delivery for the registered session lands.
        const status = yield* hooks.deliver(secret, {
          session_id: "session-t",
          hook_event_name: "Stop",
        })
        assert.strictEqual(status, 204)
        // A relaunch that hands the metadata back keeps the same secret —
        // the running TUI's hooks stay valid.
        const relaunch = yield* adapter.tuiLaunch({
          providerSessionId: "session-t",
          cwd: "/w",
          providerMetadata: launch.providerMetadata,
        })
        assert.strictEqual(relaunch.providerMetadata, launch.providerMetadata)
      }).pipe(Effect.provide(layer))
    }),
  )
})

// ATC-140: the TUI-session plumbing — pre-assigned identity, hook-secret
// metadata, webhook-fed observation, and release cleanup. No query() seam
// involved: nothing here spawns or scripts the SDK.
describe("ClaudeAdapter TUI session plumbing", () => {
  const stateDir = () => trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-state-")))

  /** The real hooks service, with minted secrets recorded for assertions. */
  const spyHooksLayer = (minted: Array<string>) =>
    Layer.effect(ClaudeHooks.ClaudeHooks)(
      Effect.gen(function* () {
        const real = yield* ClaudeHooks.ClaudeHooks
        return {
          ...real,
          registerSecret: (providerSessionId: string) =>
            real
              .registerSecret(providerSessionId)
              .pipe(Effect.tap((secret) => Effect.sync(() => minted.push(secret)))),
        }
      }),
    ).pipe(Layer.provide(ClaudeHooks.layer))

  /** Collect an observation stream into a string sink (scoped): activity
   * values verbatim, prompts as `prompt:<text>`. */
  const collectActivity = (stream: Stream.Stream<AgentSessionEvent>) =>
    Effect.gen(function* () {
      const sink: Array<string> = []
      yield* stream.pipe(
        Stream.runForEach((event) =>
          Effect.sync(() =>
            sink.push(
              event.type === "activity"
                ? event.activity
                : event.type === "userPrompt"
                  ? `prompt:${event.text}`
                  : `item:${event.item.type}`,
            ),
          ),
        ),
        Effect.ignore,
        Effect.forkScoped,
      )
      return sink
    })

  const waitForActivity = (sink: Array<string>, wanted: string) =>
    eventually(
      Effect.sync(() => sink),
      (entries) => entries.includes(wanted),
      { attempts: 200 },
    )

  it.live("prepareTuiSession pre-assigns identity and enables Remote Control", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            const prepared = yield* adapter.prepareTuiSession({ cwd })
            // Identity resolves immediately: pre-assignment, not capture.
            const identity = yield* prepared.identity
            assert.match(
              identity.providerSessionId,
              /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
            )
            assert.strictEqual(identity.cwd, cwd)
            const settingsFile = prepared.launchSpec.command[4]!
            assert.deepStrictEqual(prepared.launchSpec.command, [
              "/bin/echo",
              "--session-id",
              identity.providerSessionId,
              "--settings",
              settingsFile,
              "--remote-control",
            ])
            assert.isTrue(fs.existsSync(settingsFile))
            // The metadata carries the secret; the registration accepts it.
            const metadata = JSON.parse(identity.providerMetadata ?? "{}") as {
              hookSecret?: string
            }
            assert.isString(metadata.hookSecret)
            const status = yield* hooks.deliver(metadata.hookSecret ?? "", {
              session_id: identity.providerSessionId,
              hook_event_name: "Stop",
            })
            assert.strictEqual(status, 204)
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("observeSession restores the persisted secret and streams webhook activity", () =>
    Effect.gen(function* () {
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            // The secret arrives only via persisted metadata — the register
            // path never ran in this "restarted" process.
            const secret = "a".repeat(64)
            const metadata = JSON.stringify({ hookSecret: secret })
            const stream = yield* adapter.observeSession({
              providerSessionId: "restored-session",
              providerMetadata: metadata,
            })
            const sink = yield* collectActivity(stream)
            const status = yield* hooks.deliver(secret, {
              session_id: "restored-session",
              hook_event_name: "UserPromptSubmit",
            })
            assert.strictEqual(status, 204)
            yield* waitForActivity(sink, "working")
            // Another session's activity never leaks into this stream.
            const otherSecret = "b".repeat(64)
            yield* hooks.adoptSecret("other-session", otherSecret)
            yield* hooks.deliver(otherSecret, {
              session_id: "other-session",
              hook_event_name: "Stop",
            })
            yield* hooks.deliver(secret, {
              session_id: "restored-session",
              hook_event_name: "Stop",
            })
            yield* waitForActivity(sink, "idle")
            assert.deepStrictEqual(sink, ["working", "idle"])
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("releaseSession removes the hook files and revokes the secret", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            const prepared = yield* adapter.prepareTuiSession({ cwd })
            const identity = yield* prepared.identity
            const settingsFile = prepared.launchSpec.command[4]!
            const metadata = JSON.parse(identity.providerMetadata ?? "{}") as {
              hookSecret?: string
            }
            yield* adapter.releaseSession({
              providerSessionId: identity.providerSessionId,
              providerMetadata: identity.providerMetadata,
            })
            assert.isFalse(fs.existsSync(settingsFile))
            assert.isFalse(fs.existsSync(settingsFile.replace(/\.json$/, ".header")))
            const status = yield* hooks.deliver(metadata.hookSecret ?? "", {
              session_id: identity.providerSessionId,
              hook_event_name: "Stop",
            })
            assert.strictEqual(status, 404)
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("a relaunch after releaseSession recreates the hook plumbing", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            const prepared = yield* adapter.prepareTuiSession({ cwd })
            const identity = yield* prepared.identity
            // Archive-time release removes the files and revokes the secret;
            // the resume relaunch must rebuild both from the persisted
            // metadata alone (the archive → unarchive → open round trip).
            yield* adapter.releaseSession({
              providerSessionId: identity.providerSessionId,
              providerMetadata: identity.providerMetadata,
            })
            const relaunch = yield* adapter.tuiLaunch({
              providerSessionId: identity.providerSessionId,
              cwd,
              providerMetadata: identity.providerMetadata,
            })
            const settingsFile = relaunch.launchSpec.command[4] ?? ""
            assert.strictEqual((fs.statSync(settingsFile).mode & 0o777).toString(8), "600")
            // The metadata-carried secret is re-adopted, so the relaunched
            // TUI's hooks deliver again.
            const metadata = JSON.parse(identity.providerMetadata ?? "{}") as {
              hookSecret?: string
            }
            const status = yield* hooks.deliver(metadata.hookSecret ?? "", {
              session_id: identity.providerSessionId,
              hook_event_name: "Stop",
            })
            assert.strictEqual(status, 204)
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("a failed prepare revokes the freshly minted secret", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      // stateDir is occupied by a regular file, so the hook plumbing fails
      // after the secret was minted and registered.
      const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-state-")))
      const blocked = path.join(base, "not-a-dir")
      fs.writeFileSync(blocked, "")
      const minted: Array<string> = []
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const failure = yield* Effect.scoped(Effect.flip(adapter.prepareTuiSession({ cwd })))
        assert.strictEqual(failure._tag, "AgentUnavailable")
        assert.lengthOf(minted, 1)
        const status = yield* hooks.deliver(minted[0] ?? "", {
          session_id: "any",
          hook_event_name: "Stop",
        })
        assert.strictEqual(status, 404)
      }).pipe(
        Effect.provide(
          claudeAdapterLayer({}, "/bin/echo", { stateDir: blocked }, spyHooksLayer(minted)),
        ),
      )
    }),
  )

  it.live("a failed settings write cleans the partial header file and secret", () =>
    Effect.gen(function* () {
      const dir = stateDir()
      // The settings path is occupied by a directory, so the SECOND write
      // fails after the header file was written and the secret registered.
      fs.mkdirSync(path.join(dir, "claude-hooks-session-w.json"))
      const minted: Array<string> = []
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const failure = yield* Effect.flip(
          adapter.tuiLaunch({
            providerSessionId: "session-w",
            cwd: "/w",
            providerMetadata: undefined,
          }),
        )
        assert.strictEqual(failure._tag, "AgentUnavailable")
        assert.isFalse(fs.existsSync(path.join(dir, "claude-hooks-session-w.header")))
        assert.lengthOf(minted, 1)
        const status = yield* hooks.deliver(minted[0] ?? "", {
          session_id: "session-w",
          hook_event_name: "Stop",
        })
        assert.strictEqual(status, 404)
      }).pipe(
        Effect.provide(
          claudeAdapterLayer({}, "/bin/echo", { stateDir: dir }, spyHooksLayer(minted)),
        ),
      )
    }),
  )

  it.live("observeSession streams root user prompts and ignores subagent ones", () =>
    Effect.gen(function* () {
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            const secret = "c".repeat(64)
            const stream = yield* adapter.observeSession({
              providerSessionId: "prompt-session",
              providerMetadata: JSON.stringify({ hookSecret: secret }),
            })
            const sink = yield* collectActivity(stream)
            yield* hooks.deliver(secret, {
              session_id: "prompt-session",
              hook_event_name: "UserPromptSubmit",
              prompt: "add a login page",
            })
            // A subagent's UserPromptSubmit is dispatch, not the user (it
            // still registers agent-1 as live background work, so the
            // SubagentStop below lets the root Stop land idle).
            yield* hooks.deliver(secret, {
              session_id: "prompt-session",
              hook_event_name: "UserPromptSubmit",
              prompt: "synthetic subagent dispatch",
              agent_id: "agent-1",
            })
            yield* hooks.deliver(secret, {
              session_id: "prompt-session",
              hook_event_name: "SubagentStop",
              agent_id: "agent-1",
            })
            yield* hooks.deliver(secret, {
              session_id: "prompt-session",
              hook_event_name: "Stop",
            })
            yield* waitForActivity(sink, "idle")
            assert.deepStrictEqual(
              sink.filter((entry) => entry.startsWith("prompt:")),
              ["prompt:add a login page"],
            )
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )
})

describe("ClaudeAdapter generateTitle", () => {
  it.live("runs one bounded, tool-less, unpersisted haiku one-shot", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const fake = makeFakeClaudeQuery()
      let captured: Record<string, unknown> | undefined
      const layer = claudeAdapterLayer({
        queryFn: (args) => {
          captured = args.options as unknown as Record<string, unknown>
          return fake.queryFn(args)
        },
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const raw = yield* adapter.generateTitle({ cwd, prompt: "add dark mode" })
        // The fake echoes its input, which is the shared template carrying
        // the verbatim prompt — the raw reply comes back unsanitized.
        assert.isTrue(raw.startsWith("fake: You write concise titles"), raw)
        assert.include(raw, "add dark mode")
        assert.strictEqual(captured?.["maxTurns"], 1)
        assert.strictEqual(captured?.["persistSession"], false)
        assert.strictEqual(captured?.["model"], "haiku")
        assert.deepStrictEqual(captured?.["tools"], [])
        assert.strictEqual(captured?.["cwd"], cwd)
        // The resolver machinery is gone (ATC-190): no MCP servers, no
        // pre-approvals — and strict MCP still keeps the project's own
        // .mcp.json out of the unattended run.
        assert.isUndefined(captured?.["mcpServers"])
        assert.isUndefined(captured?.["allowedTools"])
        assert.strictEqual(captured?.["strictMcpConfig"], true)
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a failed turn is a typed protocol error, never a crash", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.flip(
          adapter.generateTitle({ cwd: workDir(), prompt: "FAILTURN" }),
        )
        assert.strictEqual(failure._tag, "AgentProtocolError")
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a synchronously throwing query is a typed protocol error too", () =>
    Effect.gen(function* () {
      const layer = claudeAdapterLayer({
        queryFn: () => {
          throw new Error("sync setup boom")
        },
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.flip(adapter.generateTitle({ cwd: workDir(), prompt: "x" }))
        assert.strictEqual(failure._tag, "AgentProtocolError")
        if (failure._tag === "AgentProtocolError") {
          assert.include(failure.reason, "sync setup boom")
        }
      }).pipe(Effect.provide(layer))
    }),
  )
})

describe("ClaudeAdapter collectTitleContext", () => {
  const transcriptMessage = (
    type: "user" | "assistant" | "system",
    content: unknown,
    parentToolUseId: string | null = null,
  ): SessionMessage =>
    ({
      type,
      uuid: crypto.randomUUID(),
      session_id: "context-session",
      message: { role: type, content },
      parent_tool_use_id: parentToolUseId,
      parent_agent_id: null,
    }) as SessionMessage

  it.live("reads the transcript on demand into labeled conversation text", () =>
    Effect.gen(function* () {
      const calls: Array<{ sessionId: string; dir: string | undefined }> = []
      const layer = claudeAdapterLayer({
        sessionMessagesFn: (sessionId, options) => {
          calls.push({ sessionId, dir: options?.dir })
          return Promise.resolve([
            transcriptMessage("user", "/implement ATC-190"),
            transcriptMessage("assistant", [
              { type: "text", text: "Reading the issue: replace resolver-driven naming." },
              { type: "tool_use", id: "t1", name: "Read", input: {} },
            ]),
            // Tool results, tool-nested messages, and system entries carry
            // no title signal and stay out of the context.
            transcriptMessage("user", [{ type: "tool_result", tool_use_id: "t1", content: "…" }]),
            transcriptMessage("assistant", "tool-nested narration", "tool-1"),
            transcriptMessage("system", "compact boundary"),
          ])
        },
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const context = yield* adapter.collectTitleContext({
          providerSessionId: "context-session",
          cwd: "/work",
        })
        assert.strictEqual(
          context,
          "user: /implement ATC-190\n" +
            "assistant: Reading the issue: replace resolver-driven naming.",
        )
        assert.deepStrictEqual(calls, [{ sessionId: "context-session", dir: "/work" }])
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live(
    "readHistory normalizes the transcript into turns; a failed read is a protocol error",
    () =>
      Effect.gen(function* () {
        const calls: Array<{ sessionId: string; dir: string | undefined }> = []
        const layer = claudeAdapterLayer({
          sessionMessagesFn: (sessionId, options) => {
            calls.push({ sessionId, dir: options?.dir })
            if (sessionId === "gone-session") return Promise.reject(new Error("no transcript"))
            return Promise.resolve([
              transcriptMessage("user", "hello"),
              transcriptMessage("assistant", [
                { type: "tool_use", id: "t1", name: "Bash", input: { command: "ls" } },
              ]),
              transcriptMessage("user", [
                { type: "tool_result", tool_use_id: "t1", content: "a b" },
              ]),
              transcriptMessage("assistant", "two files"),
            ])
          },
        })
        yield* Effect.gen(function* () {
          const adapter = yield* ClaudeAdapter.ClaudeAdapter
          const history = yield* adapter.readHistory({
            providerSessionId: "context-session",
            cwd: "/work",
          })
          assert.deepStrictEqual(calls, [{ sessionId: "context-session", dir: "/work" }])
          assert.strictEqual(history.length, 1)
          assert.deepStrictEqual(
            history[0]?.items.map((item) => item.type),
            ["userMessage", "command", "assistantText"],
          )
          const command = history[0]?.items[1]
          assert.isTrue(command?.type === "command" && command.output === "a b")
          assert.strictEqual(history[0]?.turn.status, "completed")
          const failure = yield* Effect.flip(
            adapter.readHistory({ providerSessionId: "gone-session", cwd: "/work" }),
          )
          assert.strictEqual(failure._tag, "AgentProtocolError")
        }).pipe(Effect.provide(layer))
      }),
  )

  it.live(
    "readHistory reads the session file with every branch in file order; the SDK is the fallback",
    () =>
      Effect.gen(function* () {
        // A session driven from two surfaces: the TUI's second prompt forks
        // from before the native turn (its in-memory conversation never saw
        // it), so the last-written leaf's branch (what getSessionMessages
        // returns) would drop the native turn entirely.
        const line = (entry: Record<string, unknown>) => JSON.stringify(entry)
        const file = [
          line({
            type: "user",
            uuid: "u1",
            parentUuid: null,
            sessionId: "s",
            message: { role: "user", content: "hi" },
          }),
          line({
            type: "assistant",
            uuid: "a1",
            parentUuid: "u1",
            sessionId: "s",
            message: { role: "assistant", content: [{ type: "text", text: "hello" }] },
          }),
          line({
            type: "user",
            uuid: "n1",
            parentUuid: "a1",
            sessionId: "s",
            origin: "sdk",
            message: { role: "user", content: "native" },
          }),
          line({
            type: "assistant",
            uuid: "na1",
            parentUuid: "n1",
            sessionId: "s",
            message: { role: "assistant", content: [{ type: "text", text: "native reply" }] },
          }),
          line({
            type: "user",
            uuid: "u2",
            parentUuid: "a1",
            sessionId: "s",
            message: { role: "user", content: "tui again" },
          }),
          line({ type: "attachment", uuid: "att", parentUuid: "u2", sessionId: "s" }),
          line({
            type: "user",
            uuid: "meta",
            parentUuid: "u2",
            sessionId: "s",
            isMeta: true,
            message: { role: "user", content: "reminder" },
          }),
          line({
            type: "assistant",
            uuid: "a2",
            parentUuid: "u2",
            sessionId: "s",
            message: { role: "assistant", content: [{ type: "text", text: "tui reply" }] },
          }),
          "{not json",
        ].join("\n")
        const sdkCalls: Array<string> = []
        const layer = claudeAdapterLayer({
          sessionFileFn: (sessionId) => Promise.resolve(sessionId === "s" ? file : null),
          sessionMessagesFn: (sessionId) => {
            sdkCalls.push(sessionId)
            return Promise.resolve([
              transcriptMessage("user", "from the sdk"),
              transcriptMessage("assistant", "reply"),
            ])
          },
        })
        yield* Effect.gen(function* () {
          const adapter = yield* ClaudeAdapter.ClaudeAdapter
          const history = yield* adapter.readHistory({ providerSessionId: "s", cwd: "/work" })
          assert.deepStrictEqual(
            history.map((turn) =>
              turn.items.map((item) => ("text" in item ? item.text : item.type)),
            ),
            [
              ["hi", "hello"],
              ["native", "native reply"],
              ["tui again", "tui reply"],
            ],
          )
          assert.deepStrictEqual(sdkCalls, [])
          const fallback = yield* adapter.readHistory({
            providerSessionId: "elsewhere",
            cwd: "/work",
          })
          assert.deepStrictEqual(sdkCalls, ["elsewhere"])
          assert.strictEqual(fallback.length, 1)
        }).pipe(Effect.provide(layer))
      }),
  )

  it.live("readHistory settles while the newest turn is still output-less", () =>
    Effect.gen(function* () {
      // Claude appends the assistant line after the Stop hook that triggers
      // the observed-idle read: the first read sees the prompt alone.
      const prompt = JSON.stringify({
        type: "user",
        uuid: "u1",
        parentUuid: null,
        sessionId: "s",
        message: { role: "user", content: "hi" },
      })
      const reply = JSON.stringify({
        type: "assistant",
        uuid: "a1",
        parentUuid: "u1",
        sessionId: "s",
        message: { role: "assistant", content: [{ type: "text", text: "hello" }] },
      })
      let reads = 0
      const layer = claudeAdapterLayer({
        historySettleDelay: "1 millis",
        sessionFileFn: () => {
          reads += 1
          return Promise.resolve(reads < 3 ? prompt : `${prompt}\n${reply}`)
        },
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const history = yield* adapter.readHistory({ providerSessionId: "s", cwd: "/work" })
        assert.strictEqual(reads, 3)
        assert.deepStrictEqual(
          history[0]?.items.map((item) => item.type),
          ["userMessage", "assistantText"],
        )
        // A turn that truly produced nothing settles out after the bound.
        reads = 0
        const quiet = claudeAdapterLayer({
          historySettleDelay: "1 millis",
          sessionFileFn: () => {
            reads += 1
            return Promise.resolve(prompt)
          },
        })
        yield* Effect.gen(function* () {
          const bounded = yield* ClaudeAdapter.ClaudeAdapter
          const history = yield* bounded.readHistory({ providerSessionId: "s", cwd: "/work" })
          assert.strictEqual(history[0]?.items.length, 1)
          assert.strictEqual(reads, 9)
        }).pipe(Effect.provide(quiet))
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a failed or empty transcript read is silently no context", () =>
    Effect.gen(function* () {
      const layer = claudeAdapterLayer({
        sessionMessagesFn: (sessionId) =>
          sessionId === "empty-session"
            ? Promise.resolve([])
            : Promise.reject(new Error("no transcript")),
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        assert.isNull(
          yield* adapter.collectTitleContext({ providerSessionId: "empty-session", cwd: "/work" }),
        )
        assert.isNull(
          yield* adapter.collectTitleContext({ providerSessionId: "gone-session", cwd: "/work" }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )
})
