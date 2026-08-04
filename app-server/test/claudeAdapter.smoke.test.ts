import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import * as ClaudeAdapter from "../src/claudeAdapter.ts"
import { claudeAdapterLayer, collectAgentEvents, waitForAgentEvent } from "./agentTestKit.ts"
import { trackTempDir } from "./blackbox.ts"

// Live Claude adapter smoke (opt-in: mise run test:smoke): the real Agent
// SDK end to end — create with verified identity, exact resume with
// deferred verification at the first turn, interrupt with a truthful
// interrupted outcome. Needs an installed, authenticated Claude Code; runs
// real (cheap) turns from a directory outside any repository.

const enabled = process.env["ATC_SMOKE"] === "1"

describe.skipIf(!enabled)("live claude adapter smoke (opt-in)", () => {
  it.live(
    "create → resume → turn → interrupt against real Claude Code",
    () =>
      Effect.gen(function* () {
        const cwd = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-smoke-")))
        const layer = claudeAdapterLayer({}, "claude")
        const slow = { attempts: 900, interval: "200 millis" } as const

        yield* Effect.gen(function* () {
          const adapter = yield* ClaudeAdapter.ClaudeAdapter

          // Create + first turn.
          const sessionId = yield* Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd,
                input: "Reply with exactly: SMOKE-OK",
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId === turn.turnId &&
                  event.outcome === "completed",
                slow,
              )
              return connection.providerSessionId
            }),
          )

          // Exact resume; verification completes at the first turn.
          yield* Effect.scoped(
            Effect.gen(function* () {
              const connection = yield* adapter.resumeSession({
                providerSessionId: sessionId,
                cwd,
              })
              const sink = yield* collectAgentEvents(connection.events)
              const turn = yield* connection.startTurn(
                "What exact text did I ask you to reply with earlier? Answer with just that text.",
              )
              assert.strictEqual(connection.providerSessionId, sessionId)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId === turn.turnId &&
                  event.outcome === "completed",
                slow,
              )
            }),
          )

          // Interrupt a long-running turn; the outcome must be truthful.
          yield* Effect.scoped(
            Effect.gen(function* () {
              const connection = yield* adapter.resumeSession({
                providerSessionId: sessionId,
                cwd,
              })
              const sink = yield* collectAgentEvents(connection.events)
              const turn = yield* connection.startTurn(
                "Count from 1 to 500, one number per line. Do not stop early.",
              )
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "activity" && event.activity === "working",
                slow,
              )
              yield* connection.interrupt(turn)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId === turn.turnId &&
                  event.outcome === "interrupted",
                slow,
              )
            }),
          )

          // An unknown session still fails closed against the real SDK.
          const failure = yield* Effect.scoped(
            Effect.gen(function* () {
              const connection = yield* adapter.resumeSession({
                providerSessionId: "00000000-0000-4000-8000-000000000000",
                cwd,
              })
              return yield* Effect.flip(connection.startTurn("hello?"))
            }),
          )
          assert.strictEqual(failure._tag, "AgentResumeFailed")
        }).pipe(Effect.provide(layer))
      }),
    300_000,
  )
})
