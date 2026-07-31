import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect } from "effect"
import { CliError, Command } from "effect/unstable/cli"
import { DEFAULT_PORT, port } from "../src/cli.ts"

// Parse the real --port flag against a probe command so validation is tested
// without starting a server.
const parsePort = (args: ReadonlyArray<string>) =>
  Effect.gen(function* () {
    let parsed: number | undefined
    const probe = Command.make("probe", { port }, ({ port }) =>
      Effect.sync(() => {
        parsed = port
      }),
    )
    const result = yield* Effect.result(Command.runWith(probe, { version: "0.0.0" })(args))
    return { parsed, result }
  }).pipe(Effect.provide(BunServices.layer))

describe("serve --port validation", () => {
  it.effect("defaults to the dev port", () =>
    Effect.gen(function* () {
      const { parsed } = yield* parsePort([])
      assert.strictEqual(parsed, DEFAULT_PORT)
    }),
  )

  it.effect("accepts a valid port", () =>
    Effect.gen(function* () {
      const { parsed } = yield* parsePort(["--port", "8080"])
      assert.strictEqual(parsed, 8080)
    }),
  )

  it.effect.each([
    ["not a number", "abc"],
    ["zero", "0"],
    ["negative", "-1"],
    ["above 65535", "70000"],
  ] as const)("rejects %s with a CLI error", ([, value]) =>
    Effect.gen(function* () {
      const { parsed, result } = yield* parsePort(["--port", value])
      assert.strictEqual(parsed, undefined)
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(CliError.isCliError(result.failure), true)
      }
    }),
  )
})
