import { assert, describe, it } from "@effect/vitest"
import { Context, Effect, Exit, Layer, Scope } from "effect"
import { HttpServer } from "effect/unstable/http"
import * as Events from "../src/events/events.ts"
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

  // The ATC-128 shutdown requirement: an open SSE subscriber must not hold
  // the listener's bounded graceful stop (2s) and die on the forced-interrupt
  // path — the drain layer in server.ts ends subscriber streams first.
  it.live("graceful shutdown with an open event subscriber is quick and clean", () =>
    Effect.gen(function* () {
      const scope = yield* Scope.make()
      yield* Effect.addFinalizer(() => Scope.close(scope, Exit.void))
      // TestRepositoryLayers is merged alongside the server (one memoized
      // instance) so the test can watch the same Events service the
      // handlers publish through.
      const context = yield* Layer.build(
        Layer.mergeAll(Server.layer({ port: 0 }), TestRepositoryLayers).pipe(
          Layer.provide([TestBuildInfoLayer, TestRepositoryLayers]),
        ),
      ).pipe(Effect.provideService(Scope.Scope, scope))
      const events = Context.get(context, Events.Events)
      const address = Context.get(context, HttpServer.HttpServer).address
      assert.strictEqual(address._tag, "TcpAddress")
      if (address._tag !== "TcpAddress") return
      const base = `http://127.0.0.1:${address.port}`

      const response = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/events`, { headers: { accept: "text/event-stream" } }),
      )
      assert.strictEqual(response.status, 200)
      // The subscription registers before the response exists, so by the
      // time fetch resolved the feed is already live.
      assert.strictEqual(yield* events.subscriberCount(), 1)
      const reader = response.body!.getReader()
      // The opening SSE comment is load-bearing: it is the first body byte
      // that lets fetch-based clients observe "stream open" at all.
      const opening = yield* Effect.promise(() => reader.read())
      assert.strictEqual(new TextDecoder().decode(opening.value), ": connected\n\n")

      // A mutation event traverses the real wire before shutdown begins.
      const created = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/projects`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ name: "Shutdown", defaultWorkingDirectory: "/tmp" }),
        }).then((r) => r.json() as Promise<{ id: string }>),
      )
      const decoder = new TextDecoder()
      let payload = ""
      for (let attempt = 0; !payload.includes(`"id":"${created.id}"`); attempt++) {
        assert.isBelow(attempt, 10, `event never arrived; saw: ${JSON.stringify(payload)}`)
        const chunk = yield* Effect.promise(() => reader.read())
        assert.isFalse(chunk.done, "the stream ended before delivering the event")
        payload += decoder.decode(chunk.value, { stream: true })
      }
      assert.include(payload, '"change":"created"')

      // Shutdown must complete well inside the 2s graceful-stop bound, and
      // the subscriber's stream must end rather than error.
      const started = Date.now()
      yield* Scope.close(scope, Exit.void)
      assert.isBelow(Date.now() - started, 1500, "shutdown rode the graceful-stop timeout")
      const end = yield* Effect.promise(() =>
        reader.read().then(
          (result) => ({ done: result.done, errored: false }),
          () => ({ done: false, errored: true }),
        ),
      )
      assert.isFalse(end.errored, "the subscriber stream errored instead of ending")
      assert.isTrue(end.done)
    }),
  )

  // Subscriber cleanup on client disconnect rides Bun's ReadableStream
  // cancel → fiber interrupt → Stream.ensuring — third-party behavior an
  // Effect or Bun upgrade could silently change, hence a real-wire test.
  it.live("a dropped subscriber is unregistered", () =>
    Effect.gen(function* () {
      const scope = yield* Scope.make()
      yield* Effect.addFinalizer(() => Scope.close(scope, Exit.void))
      const context = yield* Layer.build(
        Layer.mergeAll(Server.layer({ port: 0 }), TestRepositoryLayers).pipe(
          Layer.provide([TestBuildInfoLayer, TestRepositoryLayers]),
        ),
      ).pipe(Effect.provideService(Scope.Scope, scope))
      const events = Context.get(context, Events.Events)
      const address = Context.get(context, HttpServer.HttpServer).address
      if (address._tag !== "TcpAddress") return assert.fail("expected a TCP address")

      const abort = new AbortController()
      const response = yield* Effect.promise(() =>
        fetch(`http://127.0.0.1:${address.port}/api/v1/events`, { signal: abort.signal }),
      )
      assert.strictEqual(response.status, 200)
      assert.strictEqual(yield* events.subscriberCount(), 1)

      abort.abort()
      // Unregistration has no awaitable signal — it rides Bun's cancel →
      // fiber interrupt → ensuring — so poll the condition bounded only by
      // the test timeout, never a fixed wall-clock budget of our own.
      while ((yield* events.subscriberCount()) > 0) {
        yield* Effect.sleep("10 millis")
      }
    }),
  )
})
