import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer, Option } from "effect"
import { HttpBody, HttpClientRequest, HttpRouter, HttpServerRequest } from "effect/unstable/http"
import * as fs from "node:fs"
import { fileURLToPath } from "node:url"
import type { AgentActivity } from "../../src/agents/agentAdapter.ts"
import * as ClaudeHooks from "../../src/agents/claudeHooks.ts"
import * as Server from "../../src/server.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { TestAuthTokenLayer, TestRepositoryLayers } from "../testLayers.ts"

// The Claude hook receiver, exercised with payloads RECORDED from real
// Claude Code runs: secret gating, session_id agreement, and aggregate
// activity tracking (ATC-158) over the background-subagent lifecycle
// (fixtures/claude-background-hook-payloads.json, recorded 2026-08-10 on
// 2.1.226). One black-box case proves the internal route is mounted.

const recorded = JSON.parse(
  fs.readFileSync(
    fileURLToPath(new URL("../fixtures/claude-hook-payloads.json", import.meta.url)),
    "utf8",
  ),
) as Array<Record<string, unknown>>

const RECORDED_SESSION = "8825b7a5-b6a4-4dc1-9daf-e45904303e62"

// The background-subagent lifecycle: prompt → Agent tool → SubagentStart →
// root Stop with a live background_tasks snapshot → descendant tool use →
// SubagentStop (whose snapshot still contains the stopping agent) → wake
// turn → Stop with an empty snapshot; plus one Stop with a backgrounded
// shell still running.
const background = JSON.parse(
  fs.readFileSync(
    fileURLToPath(new URL("../fixtures/claude-background-hook-payloads.json", import.meta.url)),
    "utf8",
  ),
) as Array<Record<string, unknown>>

const feed = (
  tracker: ClaudeHooks.ActivityTracker,
  payload: Record<string, unknown>,
): AgentActivity | null => tracker.update(payload["hook_event_name"] as string, payload)

describe("claude aggregate activity tracker", () => {
  it("maps the root vocabulary and the notification subtypes", () => {
    const of = (event: string, payload: Record<string, unknown> = {}) =>
      ClaudeHooks.makeActivityTracker().update(event, payload)
    assert.strictEqual(of("UserPromptSubmit"), "working")
    assert.strictEqual(of("PreToolUse"), "working")
    assert.strictEqual(of("PostToolUse"), "working")
    assert.strictEqual(of("Stop"), "idle")
    assert.strictEqual(of("StopFailure"), "idle")
    assert.strictEqual(of("PermissionRequest"), "needs_input")
    assert.strictEqual(
      of("Notification", { notification_type: "permission_prompt" }),
      "needs_input",
    )
    assert.strictEqual(
      of("Notification", { notification_type: "agent_needs_input" }),
      "needs_input",
    )
    assert.strictEqual(of("Notification", { notification_type: "idle_prompt" }), "idle")
    // Lifecycle and unknown events are never guessed at.
    assert.isNull(of("SessionEnd"))
    assert.isNull(of("SomethingNew"))
    assert.isNull(of("Notification", { notification_type: "mystery" }))
  })

  it("keeps the recorded background-subagent lifecycle busy until the last child stops", () => {
    const tracker = ClaudeHooks.makeActivityTracker()
    const timeline = background.map((payload) => feed(tracker, payload))
    assert.deepStrictEqual(timeline, [
      "working", // UserPromptSubmit
      "working", // PreToolUse Agent
      "working", // SubagentStart
      "working", // PostToolUse
      "working", // root Stop — background subagent still running
      "working", // descendant PreToolUse (agent_id)
      "working", // descendant PostToolUse (agent_id)
      "idle", //    SubagentStop: snapshot minus the stopping agent — last child
      "working", // wake-turn UserPromptSubmit
      "idle", //    wake-turn Stop with empty snapshot
      "working", // Stop with a backgrounded shell still running
    ])
  })

  it("a pending session cron keeps the aggregate busy", () => {
    const tracker = ClaudeHooks.makeActivityTracker()
    assert.strictEqual(
      tracker.update("Stop", {
        background_tasks: [],
        session_crons: [{ id: "c1", schedule: "0 9 * * *", recurring: true, prompt: "loop" }],
      }),
      "working",
    )
    assert.strictEqual(tracker.update("Stop", { background_tasks: [], session_crons: [] }), "idle")
  })

  it("a descendant waiting on permission surfaces needs_input and survives snapshots", () => {
    const tracker = ClaudeHooks.makeActivityTracker()
    tracker.update("Stop", {
      background_tasks: [{ id: "bg1", type: "subagent", status: "running" }],
      session_crons: [],
    })
    assert.strictEqual(tracker.update("PermissionRequest", { agent_id: "bg1" }), "needs_input")
    // A later coarse snapshot cannot see the prompt; the needs_input sticks.
    assert.strictEqual(
      tracker.update("Stop", {
        background_tasks: [{ id: "bg1", type: "subagent", status: "running" }],
        session_crons: [],
      }),
      "needs_input",
    )
    // The descendant proceeding clears it.
    assert.strictEqual(tracker.update("PostToolUse", { agent_id: "bg1" }), "working")
  })

  it("a level snapshot rebuilds the set after a restart (no wedged counter)", () => {
    // A fresh tracker (post-restart) hearing only the mid-flight Stop
    // reconstructs the background evidence from the snapshot alone.
    const tracker = ClaudeHooks.makeActivityTracker()
    assert.strictEqual(feed(tracker, background[4]!), "working")
    // And the recorded SubagentStop alone lands the idle transition.
    assert.strictEqual(feed(tracker, background[7]!), "idle")
  })

  it("task-list edges are pending evidence a snapshot replaces", () => {
    const tracker = ClaudeHooks.makeActivityTracker()
    tracker.update("UserPromptSubmit", {})
    assert.strictEqual(tracker.update("TaskCreated", { task_id: "t1" }), "working")
    // The turn's Stop snapshot does not count the task-list entry: replaced.
    assert.strictEqual(tracker.update("Stop", { background_tasks: [], session_crons: [] }), "idle")
  })
})

describe("claude hook webhook delivery", () => {
  it.effect("recorded payloads flow to subscribers under the right secret", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const seen: Array<{ sessionId: string; activity: AgentActivity }> = []
        yield* hooks.subscribe((sessionId, activity) => seen.push({ sessionId, activity }))
        const secret = yield* hooks.registerSecret(RECORDED_SESSION)

        for (const payload of recorded) {
          assert.strictEqual(yield* hooks.deliver(secret, payload), 204)
        }
        // UserPromptSubmit/PreToolUse/PostToolUse → working, Stop → idle.
        assert.deepStrictEqual(
          seen.map((entry) => entry.activity),
          ["working", "working", "working", "idle"],
        )
        assert.isTrue(seen.every((entry) => entry.sessionId === RECORDED_SESSION))
      }),
    ).pipe(Effect.provide(ClaudeHooks.layer)),
  )

  it.effect("the background lifecycle aggregates across webhook deliveries", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const sessionId = background[0]!["session_id"] as string
        const seen: Array<AgentActivity> = []
        yield* hooks.subscribe((_sessionId, activity) => seen.push(activity))
        const secret = yield* hooks.registerSecret(sessionId)
        for (const payload of background) {
          assert.strictEqual(yield* hooks.deliver(secret, payload), 204)
        }
        // Same timeline as the tracker unit test: the root Stop mid-flight
        // stays working, the SubagentStop lands idle, the shell Stop stays
        // working.
        assert.strictEqual(seen[4], "working")
        assert.strictEqual(seen[7], "idle")
        assert.strictEqual(seen[10], "working")
      }),
    ).pipe(Effect.provide(ClaudeHooks.layer)),
  )

  it.effect("unknown secrets, spoofed sessions, and junk are rejected", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const seen: Array<string> = []
        yield* hooks.subscribe((sessionId) => seen.push(sessionId))
        const secret = yield* hooks.registerSecret(RECORDED_SESSION)

        assert.strictEqual(yield* hooks.deliver("not-a-secret", recorded[0]), 404)
        // A valid secret with a payload for a DIFFERENT session is spoofing.
        assert.strictEqual(
          yield* hooks.deliver(secret, { session_id: "intruder", hook_event_name: "Stop" }),
          400,
        )
        assert.strictEqual(yield* hooks.deliver(secret, { nonsense: true }), 400)
        assert.strictEqual(yield* hooks.deliver(secret, null), 400)
        assert.deepStrictEqual(seen, [])
      }),
    ).pipe(Effect.provide(ClaudeHooks.layer)),
  )

  // In-process over the real routes layer, sharing ONE ClaudeHooks instance
  // between registration and the route: proves the mount actually delivers
  // (a 404 alone could come from an unmounted path just as well).
  it.effect("the internal route is mounted and delivers to the registry", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const seen: Array<AgentActivity> = []
        yield* hooks.subscribe((_sessionId, activity) => seen.push(activity))
        const secret = yield* hooks.registerSecret(RECORDED_SESSION)
        const handler = yield* HttpRouter.toHttpEffect(
          Server.routes.pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
        )
        const post = (headerSecret: string) =>
          handler.pipe(
            Effect.provideService(
              HttpServerRequest.HttpServerRequest,
              HttpServerRequest.fromClientRequest(
                HttpClientRequest.post("http://127.0.0.1/internal/claude/hooks").pipe(
                  HttpClientRequest.setHeader("host", "127.0.0.1"),
                  HttpClientRequest.setHeader(ClaudeHooks.SECRET_HEADER, headerSecret),
                  HttpClientRequest.setBody(HttpBody.jsonUnsafe(recorded[3])),
                ),
              ).modify({ remoteAddress: Option.some("127.0.0.1") }),
            ),
            Effect.provideService(ClaudeHooks.ClaudeHooks, hooks),
            Effect.orDie,
          )
        assert.strictEqual((yield* post(secret)).status, 204)
        assert.deepStrictEqual(seen, ["idle"])
        assert.strictEqual((yield* post("bogus")).status, 404)
      }),
    ).pipe(
      Effect.provide([ClaudeHooks.layer, TestAuthTokenLayer, BunHttpServer.layerHttpServices]),
    ),
  )
})
