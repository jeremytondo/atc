import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { Api } from "../src/api.ts"
import { V1Handlers } from "../src/handlers.ts"
import { TestBuildInfoLayer, testBuildInfo } from "./testBuildInfo.ts"

// In-process contract tests: the same encode/route/decode pipeline as the real
// server, but no listener.
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide(TestBuildInfoLayer)),
  BunHttpServer.layerHttpServices,
)

describe("/api/v1", () => {
  it.effect("health returns ok", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const health = yield* client.v1.health()
      assert.deepStrictEqual(health, { status: "ok" })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("version reports injected build metadata", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const version = yield* client.v1.version()
      assert.deepStrictEqual(version, {
        version: testBuildInfo.version,
        apiVersion: "v1",
        commit: testBuildInfo.commit,
        builtAt: testBuildInfo.builtAt,
      })
    }).pipe(Effect.provide(TestLayer)),
  )
})
