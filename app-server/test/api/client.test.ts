import { assert, describe, it } from "@effect/vitest"
import { BunHttpClient, BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { HttpClient, HttpClientResponse } from "effect/unstable/http"
import * as Client from "../../src/api/client.ts"
import * as Server from "../../src/server.ts"
import { freePort } from "../blackbox.ts"
import { TestBuildInfoLayer, testBuildInfo } from "../testBuildInfo.ts"
import { TestRepositoryLayers } from "../testLayers.ts"

// The contract-derived client against a real ephemeral App Server: a live
// loopback listener, real HTTP, schema-decoded responses.

const withServer = <A, E, R>(run: (baseUrl: string) => Effect.Effect<A, E, R>) =>
  Effect.gen(function* () {
    const port = yield* Effect.promise(freePort)
    yield* Layer.build(
      Server.layer({ port }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
    )
    return yield* run(`http://127.0.0.1:${port}`)
  }).pipe(Effect.scoped)

describe("contract-derived client", () => {
  it.effect("calls health against a real server", () =>
    withServer((baseUrl) =>
      Effect.gen(function* () {
        const client = yield* Client.make({ baseUrl })
        assert.deepStrictEqual(yield* client.v1.health(), { status: "ok" })
      }),
    ).pipe(Effect.provide([BunHttpClient.layer, BunServices.layer])),
  )

  it.effect("attaches the bearer token to every request", () =>
    Effect.gen(function* () {
      // No listener needed: capture the outgoing request at the HttpClient
      // seam and assert the documented bearerAuth header is attached.
      let authorization: string | undefined
      const capture = HttpClient.make((request) => {
        authorization = request.headers["authorization"]
        return Effect.succeed(HttpClientResponse.fromWeb(request, Response.json({ status: "ok" })))
      })
      const client = yield* Client.make({ baseUrl: "http://127.0.0.1", token: "secret" }).pipe(
        Effect.provideService(HttpClient.HttpClient, capture),
      )
      assert.deepStrictEqual(yield* client.v1.health(), { status: "ok" })
      assert.strictEqual(authorization, "Bearer secret")
    }),
  )

  it.effect("calls version against a real server", () =>
    withServer((baseUrl) =>
      Effect.gen(function* () {
        const client = yield* Client.make({ baseUrl })
        assert.deepStrictEqual(yield* client.v1.version(), {
          version: testBuildInfo.version,
          apiVersion: "v1",
          commit: testBuildInfo.commit,
          builtAt: testBuildInfo.builtAt,
        })
      }),
    ).pipe(Effect.provide([BunHttpClient.layer, BunServices.layer])),
  )
})
