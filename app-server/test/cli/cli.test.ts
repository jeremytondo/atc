import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Option } from "effect"
import { CliError, Command } from "effect/unstable/cli"
import { port } from "../../src/cli/serve.ts"

// Parse the real --port flag against a probe command so validation is tested
// without starting a server.
const parsePort = (args: ReadonlyArray<string>) =>
  Effect.gen(function* () {
    let parsed: Option.Option<number> | undefined
    const probe = Command.make("probe", { port }, ({ port }) =>
      Effect.sync(() => {
        parsed = port
      }),
    )
    const result = yield* Effect.result(Command.runWith(probe, { version: "0.0.0" })(args))
    return { parsed, result }
  }).pipe(Effect.provide(BunServices.layer))

describe("serve --port validation", () => {
  // The flag is optional: absent means "use the configured port", so the
  // configuration pipeline (flags > env > file > default) stays the single
  // source of the effective port.
  it.effect("is absent by default", () =>
    Effect.gen(function* () {
      const { parsed } = yield* parsePort([])
      assert.deepStrictEqual(parsed, Option.none())
    }),
  )

  it.effect("accepts a valid port", () =>
    Effect.gen(function* () {
      const { parsed } = yield* parsePort(["--port", "8080"])
      assert.deepStrictEqual(parsed, Option.some(8080))
    }),
  )

  // --port=value keeps negative values from being tokenized as flags, so
  // every case exercises the integer parse or the range filter itself.
  it.effect.each([
    ["not a number", "--port=abc"],
    ["zero", "--port=0"],
    ["negative", "--port=-1"],
    ["above 65535", "--port=70000"],
  ] as const)("rejects %s as an invalid value", ([, flag]) =>
    Effect.gen(function* () {
      const { parsed, result } = yield* parsePort([flag])
      assert.strictEqual(parsed, undefined)
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        // Parse failures surface as ShowHelp wrapping the real error; a bare
        // --help would be ShowHelp with no errors, so assert the wrapped tag.
        assert.strictEqual(CliError.isCliError(result.failure), true)
        assert.strictEqual(result.failure._tag, "ShowHelp")
        if (result.failure._tag === "ShowHelp") {
          assert.strictEqual(result.failure.errors[0]?._tag, "InvalidValue")
        }
      }
    }),
  )
})
