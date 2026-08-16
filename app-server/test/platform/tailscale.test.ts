import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { mkdirSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import * as Subprocess from "../../src/platform/subprocess.ts"
import * as Tailscale from "../../src/platform/tailscale.ts"
import { eventually, makeFakeTailscaleSandbox, testAppConfig } from "../testLayers.ts"

const layerFor = (executable: string, port = 8443) =>
  Tailscale.layer.pipe(
    Layer.provide(testAppConfig({ tailscale: true, tailscaleExecutable: executable, port })),
    Layer.provide(Subprocess.layer),
    Layer.provideMerge(BunServices.layer),
  )

describe("Tailscale supervisor", () => {
  it("falls back to the documented macOS app CLI only for the default executable", () => {
    assert.deepStrictEqual(Tailscale.executableCandidates("tailscale", "darwin"), [
      "tailscale",
      "/Applications/Tailscale.app/Contents/MacOS/Tailscale",
    ])
    assert.deepStrictEqual(Tailscale.executableCandidates("tailscale", "linux"), ["tailscale"])
    assert.deepStrictEqual(Tailscale.executableCandidates("custom-tailscale", "darwin"), [
      "custom-tailscale",
    ])
  })

  it.live("verifies a healthy foreground Serve child and reports its URL", () => {
    const sandbox = makeFakeTailscaleSandbox()
    return Effect.gen(function* () {
      const tailscale = yield* Tailscale.Tailscale
      const status = yield* eventually(tailscale.status, (value) => value.state === "running", {
        attempts: 500,
      })
      assert.deepStrictEqual(status, {
        state: "running",
        url: "https://node.tailnet.ts.net:8443",
      })
    }).pipe(Effect.provide(layerFor(sandbox.wrapper)))
  })

  it.live("reports NeedsLogin and heals after the backend starts running", () => {
    const sandbox = makeFakeTailscaleSandbox({ FAKE_TAILSCALE_BACKEND: "NeedsLogin" })
    return Effect.gen(function* () {
      const tailscale = yield* Tailscale.Tailscale
      const error = yield* eventually(tailscale.status, (value) => value.state === "error", {
        attempts: 500,
      })
      assert.strictEqual(error.state, "error")
      if (error.state === "error") assert.include(error.reason, "logged out")

      mkdirSync(sandbox.stateDir, { recursive: true })
      writeFileSync(join(sandbox.stateDir, "backend"), "Running")
      const healed = yield* eventually(tailscale.status, (value) => value.state === "running", {
        attempts: 1_000,
      })
      assert.strictEqual(healed.state, "running")
    }).pipe(Effect.provide(layerFor(sandbox.wrapper)))
  })

  it.live("leaves running when the foreground Serve child exits", () => {
    const sandbox = makeFakeTailscaleSandbox()
    return Effect.gen(function* () {
      const tailscale = yield* Tailscale.Tailscale
      yield* eventually(tailscale.status, (value) => value.state === "running", {
        attempts: 500,
      })
      writeFileSync(join(sandbox.stateDir, "serve-mode"), "exit")
      const failed = yield* eventually(tailscale.status, (value) => value.state === "error", {
        attempts: 500,
      })
      assert.strictEqual(failed.state, "error")
    }).pipe(Effect.provide(layerFor(sandbox.wrapper)))
  })

  it.live("fails layer construction when the configured executable is missing", () =>
    Effect.gen(function* () {
      const error = yield* Layer.build(layerFor("/definitely/missing/tailscale")).pipe(
        Effect.scoped,
        Effect.flip,
      )
      assert.include(
        error.message,
        'tailscale executable "/definitely/missing/tailscale" not found; install Tailscale',
      )
      assert.include(error.message, "ATC_TAILSCALE_EXECUTABLE")
    }),
  )
})
