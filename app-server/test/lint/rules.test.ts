// End-to-end tests for the ATC oxlint plugin rules (lint/). Each case runs
// the real oxlint binary over a fixture; see harness.ts.
import { describe } from "vitest"
import { ruleTests } from "./harness.ts"

describe("atc/no-process-env", () => {
  const rule = ruleTests("atc/no-process-env")

  rule.valid("reading AppConfig", `export const x = (config: { port: number }) => config.port`)
  rule.valid("a local object named env", `const app = { env: "prod" }\nexport const e = app.env`)
  rule.invalid("process.env reads", `export const home = process.env["HOME"]`, "AppConfig")
  rule.invalid("globalThis.process.env", `export const e = globalThis.process.env`)
  rule.invalid("optional-chained process?.env", `export const e = process?.env`)
})

describe("atc/no-adhoc-spawn", () => {
  const rule = ruleTests("atc/no-adhoc-spawn")

  rule.valid("other Bun APIs", `export const f = Bun.file("x.txt")`)
  rule.valid(
    "spawn on non-Bun objects",
    `const pool = { spawn: () => 1 }\nexport const x = pool.spawn()`,
  )
  rule.invalid("Bun.spawn", `export const p = Bun.spawn(["ls"])`, "Subprocess service")
  rule.invalid("Bun.spawnSync", `export const p = Bun.spawnSync(["ls"])`)
  rule.invalid("globalThis.Bun.spawn", `export const p = globalThis.Bun.spawn(["ls"])`)
  rule.invalid(
    "importing node:child_process",
    `import { spawn } from "node:child_process"\nexport const p = spawn("ls")`,
  )
})

describe("atc/no-manual-effect-runtime", () => {
  const rule = ruleTests("atc/no-manual-effect-runtime")

  rule.valid(
    "lazy Effect composition",
    `import { Effect } from "effect"\nexport const program = Effect.gen(function* () { yield* Effect.void })`,
  )
  rule.valid(
    "run-like members on unrelated bindings",
    `const scheduler = { runPromise: () => 1 }\nexport const x = scheduler.runPromise()`,
  )
  rule.invalid(
    "Effect.runPromise",
    `import { Effect } from "effect"\nexport const x = Effect.runPromise(Effect.void)`,
    "src/main.ts",
  )
  rule.invalid(
    "Effect.runSync via namespace import",
    `import * as Effect from "effect/Effect"\nexport const x = Effect.runSync(Effect.void)`,
  )
  rule.invalid(
    "ManagedRuntime.make",
    `import { Layer, ManagedRuntime } from "effect"\nexport const rt = ManagedRuntime.make(Layer.empty)`,
  )
  rule.invalid(
    "BunRuntime.runMain",
    `import { BunRuntime } from "@effect/platform-bun"\nimport { Effect } from "effect"\nBunRuntime.runMain(Effect.void)`,
  )
  rule.invalid(
    "a directly imported runPromise",
    `import { runPromise } from "effect/Effect"\nimport { Effect } from "effect"\nexport const x = runPromise(Effect.void)`,
  )
  rule.invalid(
    "a directly imported ManagedRuntime make",
    `import { make } from "effect/ManagedRuntime"\nimport { Layer } from "effect"\nexport const rt = make(Layer.empty)`,
  )
  rule.invalid(
    "a directly imported runMain",
    `import { runMain } from "@effect/platform-bun/BunRuntime"\nimport { Effect } from "effect"\nrunMain(Effect.void)`,
  )
  rule.valid(
    "a local function that shares an executor name",
    `const runPromise = () => 1\nexport const x = runPromise()`,
  )
})

describe("atc/canonical-namespace-imports", () => {
  const rule = ruleTests("atc/canonical-namespace-imports")

  rule.valid(
    "canonical namespace import",
    `import * as Terminals from "./terminals.ts"\nexport const t = Terminals`,
  )
  rule.valid("the Zmx exception", `import * as Zmx from "./zmxAdapter.ts"\nexport const z = Zmx`)
  rule.valid(
    "PascalCase for hyphenated basenames",
    `import * as FakeZmx from "./fake-zmx.ts"\nexport const f = FakeZmx`,
  )
  rule.valid(
    "unaliased named imports",
    `import { layer } from "./events.ts"\nexport const l = layer`,
  )
  rule.valid(
    "aliased imports from packages",
    `import { Option as O } from "effect"\nexport const o = O`,
  )
  rule.invalid(
    "a non-canonical namespace name",
    `import * as Term from "./terminals.ts"\nexport const t = Term`,
    "`Terminals`",
  )
  rule.invalid(
    "an aliased named import",
    `import { layer as eventsLayer } from "./events.ts"\nexport const l = eventsLayer`,
    "Never alias",
  )
})
