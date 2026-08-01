import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { fileURLToPath } from "node:url"
import { codexSmoke, SmokeError } from "../src/smoke.ts"
import * as Subprocess from "../src/subprocess.ts"
import { TestBuildInfoLayer } from "./testBuildInfo.ts"

// The codex round-trip logic against a fixture app-server: no Codex install,
// no credentials, deterministic. The live provider is exercised by the opt-in
// provider.smoke tests.

const appServerRoot = fileURLToPath(new URL("..", import.meta.url))

const TestLayer = Layer.mergeAll(
  Subprocess.layer.pipe(Layer.provideMerge(BunServices.layer)),
  TestBuildInfoLayer,
)

const fixtureSpec = (...args: ReadonlyArray<string>): Subprocess.SpawnSpec => ({
  executable: process.execPath,
  args: ["test/fixtures/fake-codex-app-server.ts", ...args],
  cwd: appServerRoot,
  env: {},
})

describe("codexSmoke against a fixture app-server", () => {
  it.live("completes the initialize round trip and terminates the child", () =>
    codexSmoke(fixtureSpec()).pipe(Effect.provide(TestLayer)),
  )

  it.live("fails with diagnostics when initialize is rejected", () =>
    Effect.gen(function* () {
      const result = yield* Effect.result(codexSmoke(fixtureSpec("error")))
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.instanceOf(result.failure, SmokeError)
        assert.match(result.failure.message, /initialize failed/)
        assert.match(result.failure.message, /fixture rejects initialize/)
      }
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("times out with stderr diagnostics when the server never responds", () =>
    Effect.gen(function* () {
      // sleep-forever stays silent, so waiting for the response must time out.
      const result = yield* Effect.result(
        codexSmoke(
          {
            executable: process.execPath,
            args: ["test/fixtures/sleep-forever.ts"],
            cwd: appServerRoot,
            env: {},
            forceKillAfter: "200 millis",
          },
          { initializeTimeout: "300 millis" },
        ),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.instanceOf(result.failure, SmokeError)
        assert.match(result.failure.message, /timed out/)
      }
    }).pipe(Effect.provide(TestLayer)),
  )
})
