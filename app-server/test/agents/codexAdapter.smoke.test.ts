import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import * as CodexAdapter from "../../src/agents/codexAdapter.ts"
import * as CodexServer from "../../src/agents/codexServer.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { collectAgentEvents, waitForAgentEvent } from "./agentTestKit.ts"
import { testAppConfig } from "../testLayers.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"

// Live codex adapter smoke (opt-in: mise run test:smoke): the full accepted
// topology against the real Codex CLI — detached app-server spawn, adopt,
// create with verified identity, exact resume, interrupt, explicit stop.
// Needs an installed, authenticated codex; runs real (cheap) model turns.
// The stateDir is STABLE across runs on purpose: if a run is killed hard
// and orphans the detached server, the next run adopts and stops it
// (adopt-or-replace is the cleanup story, not temp-dir tracking).

const enabled = process.env["ATC_SMOKE"] === "1"

const stateDir = path.join(os.tmpdir(), "atc-codex-smoke-state")

describe.skipIf(!enabled)("live codex adapter smoke (opt-in)", () => {
  it.live(
    "create → resume → turn → interrupt → stop against real codex",
    () =>
      Effect.gen(function* () {
        const cwd = fs.mkdtempSync(path.join(os.tmpdir(), "atc-codex-smoke-work-"))
        const platform = Layer.mergeAll(
          Subprocess.layer.pipe(Layer.provide(BunServices.layer)),
          testAppConfig({ stateDir }),
          TestBuildInfoLayer,
          BunServices.layer,
        )
        const server = CodexServer.layerWith({}).pipe(Layer.provide(platform))
        const layer = Layer.mergeAll(
          CodexAdapter.layer.pipe(Layer.provide(server), Layer.provide(platform)),
          server,
        )

        yield* Effect.gen(function* () {
          const adapter = yield* CodexAdapter.CodexAdapter
          const codexServer = yield* CodexServer.CodexServer

          const run = Effect.gen(function* () {
            const slow = { attempts: 600, interval: "200 millis" } as const

            // Create + first turn.
            const threadId = yield* Effect.scoped(
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

            // Exact resume + interrupt cycle.
            yield* Effect.scoped(
              Effect.gen(function* () {
                const connection = yield* adapter.resumeSession({
                  providerSessionId: threadId,
                  cwd,
                })
                assert.strictEqual(connection.providerSessionId, threadId)
                const sink = yield* collectAgentEvents(connection.events)
                const turn = yield* connection.startTurn(
                  "Count from 1 to 200, one number per line. Do not stop early.",
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

            // The identity survives for a third connection (exact resume).
            yield* Effect.scoped(
              Effect.gen(function* () {
                const connection = yield* adapter.resumeSession({
                  providerSessionId: threadId,
                  cwd,
                })
                assert.strictEqual(connection.providerSessionId, threadId)
              }),
            )
          })

          // Whatever happens, the detached real codex server must be
          // stopped — never left running on the developer's machine.
          yield* Effect.ensuring(run, Effect.orDie(codexServer.stop()))
        }).pipe(Effect.provide(layer))
      }),
    300_000,
  )
})
