import { assert, describe, it } from "@effect/vitest"
import { Context, Effect, Exit, Layer, Scope } from "effect"
import { HttpServer } from "effect/unstable/http"
import * as Server from "../src/server.ts"
import { TestBuildInfoLayer } from "./testBuildInfo.ts"

describe("server layer", () => {
  it.effect("binds loopback, serves the API, and releases the port when closed", () =>
    Effect.gen(function* () {
      const scope = yield* Scope.make()
      const context = yield* Layer.build(
        Server.layer({ port: 0 }).pipe(Layer.provide(TestBuildInfoLayer)),
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
