import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { HttpBody, HttpClientRequest, HttpRouter, HttpServerRequest } from "effect/unstable/http"
import * as fs from "node:fs"
import { fileURLToPath } from "node:url"
import type { AgentActivity } from "../src/agentAdapter.ts"
import * as ClaudeHooks from "../src/claudeHooks.ts"
import * as Server from "../src/server.ts"
import { TestBuildInfoLayer } from "./testBuildInfo.ts"
import { TestRepositoryLayers } from "./testLayers.ts"

// The Claude hook receiver, exercised with payloads RECORDED from a real
// Claude Code 2.1.221 run (fixtures/claude-hook-payloads.json): secret
// gating, session_id agreement, and activity normalization. One black-box
// case proves the internal route is actually mounted on a real serve.

const recorded = JSON.parse(
  fs.readFileSync(
    fileURLToPath(new URL("fixtures/claude-hook-payloads.json", import.meta.url)),
    "utf8",
  ),
) as Array<Record<string, unknown>>

const RECORDED_SESSION = "8825b7a5-b6a4-4dc1-9daf-e45904303e62"

describe("claude hook activity normalization", () => {
  it("maps the recorded vocabulary and the notification subtypes", () => {
    assert.strictEqual(ClaudeHooks.hookActivity("UserPromptSubmit", {}), "working")
    assert.strictEqual(ClaudeHooks.hookActivity("PreToolUse", {}), "working")
    assert.strictEqual(ClaudeHooks.hookActivity("PostToolUse", {}), "working")
    assert.strictEqual(ClaudeHooks.hookActivity("Stop", {}), "idle")
    assert.strictEqual(ClaudeHooks.hookActivity("StopFailure", {}), "idle")
    assert.strictEqual(ClaudeHooks.hookActivity("PermissionRequest", {}), "needs_input")
    assert.strictEqual(
      ClaudeHooks.hookActivity("Notification", { notification_type: "permission_prompt" }),
      "needs_input",
    )
    assert.strictEqual(
      ClaudeHooks.hookActivity("Notification", { notification_type: "agent_needs_input" }),
      "needs_input",
    )
    assert.strictEqual(
      ClaudeHooks.hookActivity("Notification", { notification_type: "idle_prompt" }),
      "idle",
    )
    // Lifecycle and unknown events are never guessed at.
    assert.isNull(ClaudeHooks.hookActivity("SessionEnd", {}))
    assert.isNull(ClaudeHooks.hookActivity("SomethingNew", {}))
    assert.isNull(ClaudeHooks.hookActivity("Notification", { notification_type: "mystery" }))
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
              ),
            ),
            Effect.provideService(ClaudeHooks.ClaudeHooks, hooks),
            Effect.orDie,
          )
        assert.strictEqual((yield* post(secret)).status, 204)
        assert.deepStrictEqual(seen, ["idle"])
        assert.strictEqual((yield* post("bogus")).status, 404)
      }),
    ).pipe(Effect.provide([ClaudeHooks.layer, BunHttpServer.layerHttpServices])),
  )
})
