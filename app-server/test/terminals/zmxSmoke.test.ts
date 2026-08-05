import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import { afterAll } from "vitest"
import { TerminalAdapter } from "../../src/terminals/terminalAdapter.ts"
import { cleanupTempDirs, makeShortSocketDir } from "../blackbox.ts"
import { collectText, waitForText, zmxAdapterLayer } from "../testLayers.ts"

// Opt-in smoke tests against the real, user-installed zmx binary
// (mise run test:zmx). They prove the pinned behavioral assumptions — attach
// auto-creates, kill-then-poll, private ZMX_DIR isolation — on the real
// multiplexer, inside a throwaway socket directory.

const enabled = process.env["ATC_ZMX_SMOKE"] === "1"

afterAll(cleanupTempDirs)

const makeLayer = () =>
  zmxAdapterLayer({
    zmxExecutable: process.env["ATC_ZMX_EXECUTABLE"] ?? "zmx",
    terminalSocketDir: makeShortSocketDir(),
  })

describe.skipIf(!enabled)("real zmx smoke (opt-in: mise run test:zmx)", () => {
  it.live(
    "runs the full interactive lifecycle: create, list, attach, kill",
    () =>
      Effect.gen(function* () {
        const adapter = yield* TerminalAdapter
        const name = `atc-${"5".repeat(32)}`
        yield* Effect.gen(function* () {
          yield* adapter.createSession({ name, cwd: "/tmp" })
          const sessions = yield* adapter.listSessions()
          assert.deepStrictEqual(
            sessions.map((s) => s.name),
            [name],
          )

          yield* Effect.gen(function* () {
            const connection = yield* adapter.attachSession(name, { cols: 100, rows: 30 })
            const sink = yield* collectText(connection.output)
            yield* connection.write("printf 'MARKER-%s\\n' smoke\n")
            yield* waitForText(sink, "MARKER-smoke")
          }).pipe(Effect.scoped)

          // Detaching must not have ended the session.
          assert.deepStrictEqual(
            (yield* adapter.listSessions()).map((s) => s.name),
            [name],
          )
        }).pipe(Effect.ensuring(Effect.ignore(adapter.killSession(name))))
        assert.deepStrictEqual(yield* adapter.listSessions(), [])
      }).pipe(Effect.provide(makeLayer())),
    30_000,
  )

  it.live(
    "creates an exec-style command session and kills it",
    () =>
      Effect.gen(function* () {
        const adapter = yield* TerminalAdapter
        const name = `atc-${"6".repeat(32)}`
        yield* Effect.gen(function* () {
          yield* adapter.createSession({ name, cwd: "/tmp", command: ["sleep", "120"] })
          const sessions = yield* adapter.listSessions()
          assert.deepStrictEqual(
            sessions.map((s) => s.name),
            [name],
          )
        }).pipe(Effect.ensuring(Effect.ignore(adapter.killSession(name))))
        assert.deepStrictEqual(yield* adapter.listSessions(), [])
      }).pipe(Effect.provide(makeLayer())),
    30_000,
  )
})
