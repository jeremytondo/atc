import { assert, describe, it } from "@effect/vitest"
import { Context, Effect, Exit, Layer, Scope } from "effect"
import { HttpServer } from "effect/unstable/http"
import * as Server from "../src/server.ts"
import { TestBuildInfoLayer } from "./testBuildInfo.ts"
import { TestRepositoryLayers } from "./testLayers.ts"

describe("server layer", () => {
  // it.live: this test does real socket I/O, and the platform's shutdown
  // timeout must run on the real clock, not it.effect's TestClock.
  it.live("binds loopback, serves the API, and releases the port when closed", () =>
    Effect.gen(function* () {
      // A manual scope so the server can be closed mid-test; the finalizer
      // guarantees the listener dies even when an assertion fails first.
      const scope = yield* Scope.make()
      yield* Effect.addFinalizer(() => Scope.close(scope, Exit.void))
      const context = yield* Layer.build(
        Server.layer({ port: 0 }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
      ).pipe(Effect.provideService(Scope.Scope, scope))

      const address = Context.get(context, HttpServer.HttpServer).address
      assert.strictEqual(address._tag, "TcpAddress")
      if (address._tag !== "TcpAddress") return
      assert.strictEqual(address.hostname, "127.0.0.1")

      // Connection: close keeps fetch from pooling an idle socket, so the
      // release check below is forced onto a fresh connection.
      const base = `http://127.0.0.1:${address.port}`
      const health = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/health`, { headers: { connection: "close" } }),
      )
      assert.strictEqual(health.status, 200)
      assert.deepStrictEqual(yield* Effect.promise(() => health.json()), { status: "ok" })

      yield* Scope.close(scope, Exit.void)

      const released = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/health`).then(
          () => false,
          () => true,
        ),
      )
      assert.strictEqual(released, true)
    }),
  )
})
