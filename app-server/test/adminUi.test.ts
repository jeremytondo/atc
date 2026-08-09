import { assert, describe, it } from "@effect/vitest"
import { Context, Effect, Exit, Layer, Scope } from "effect"
import { HttpServer } from "effect/unstable/http"
import * as AdminUi from "../src/adminUi/adminUi.ts"
import * as Server from "../src/server.ts"
import { TestBuildInfoLayer } from "./testBuildInfo.ts"
import { TestRepositoryLayers } from "./testLayers.ts"

// The admin UI route (ATC-126) over a real listener: a source run serves the
// committed web/build directly, so these tests cover the path mapping, cache
// policy, and router precedence that the compiled black-box test re-verifies
// against the embedded copy.

const startServer = Effect.gen(function* () {
  const scope = yield* Scope.make()
  yield* Effect.addFinalizer(() => Scope.close(scope, Exit.void))
  const context = yield* Layer.build(
    Server.layer({ port: 0 }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
  ).pipe(Effect.provideService(Scope.Scope, scope))
  const address = Context.get(context, HttpServer.HttpServer).address
  if (address._tag !== "TcpAddress") {
    return yield* Effect.die(`expected a TCP address, got ${address._tag}`)
  }
  return `http://127.0.0.1:${address.port}`
})

describe("admin UI", () => {
  it("maps build files to routes", () => {
    assert.strictEqual(AdminUi.routeForFile("index.html"), "/")
    assert.strictEqual(AdminUi.routeForFile("docs.html"), "/docs")
    assert.strictEqual(AdminUi.routeForFile("nested/index.html"), "/nested")
    assert.strictEqual(AdminUi.routeForFile("favicon.svg"), "/favicon.svg")
    assert.strictEqual(AdminUi.routeForFile("_app/version.json"), "/_app/version.json")
  })

  it.live("serves the prerendered pages with the API routes intact", () =>
    Effect.gen(function* () {
      const base = yield* startServer

      const home = yield* Effect.promise(() => fetch(`${base}/`))
      assert.strictEqual(home.status, 200)
      assert.include(home.headers.get("content-type"), "text/html")
      assert.strictEqual(home.headers.get("cache-control"), "no-cache")
      const html = yield* Effect.promise(() => home.text())
      assert.include(html, "_app/immutable")

      // The page lives at its extensionless route; the trailing-slash
      // variant redirects there rather than aliasing it, because the
      // prerendered HTML's relative asset URLs only resolve from the
      // canonical path.
      const docs = yield* Effect.promise(() => fetch(`${base}/docs`))
      assert.strictEqual(docs.status, 200)
      assert.include(docs.headers.get("content-type"), "text/html")
      const slashed = yield* Effect.promise(() => fetch(`${base}/docs/`, { redirect: "manual" }))
      assert.strictEqual(slashed.status, 308)
      assert.strictEqual(slashed.headers.get("location"), "/docs")
      const followed = yield* Effect.promise(() => fetch(`${base}/docs/`))
      assert.strictEqual(followed.status, 200)

      // Hashed assets referenced by the page resolve and are cacheable
      // forever; unknown paths are plain 404s.
      const assetPath = html.match(/_app\/immutable\/entry\/[\w.-]+\.js/)?.[0]
      assert.isDefined(assetPath)
      const asset = yield* Effect.promise(() => fetch(`${base}/${assetPath}`))
      assert.strictEqual(asset.status, 200)
      assert.include(asset.headers.get("content-type"), "text/javascript")
      assert.include(asset.headers.get("cache-control"), "immutable")
      const missing = yield* Effect.promise(() => fetch(`${base}/no-such-page`))
      assert.strictEqual(missing.status, 404)

      // Percent-encoded request paths are decoded before the lookup, and a
      // traversal attempt still just misses the fixed key set.
      const encoded = yield* Effect.promise(() => fetch(`${base}/favicon%2Esvg`))
      assert.strictEqual(encoded.status, 200)
      const traversal = yield* Effect.promise(() => fetch(`${base}/%2e%2e/package.json`))
      assert.strictEqual(traversal.status, 404)

      // The wildcard never shadows the static routes.
      const health = yield* Effect.promise(() => fetch(`${base}/api/v1/health`))
      assert.deepStrictEqual(yield* Effect.promise(() => health.json()), { status: "ok" })
      const openapi = yield* Effect.promise(() => fetch(`${base}/openapi.json`))
      assert.strictEqual(openapi.status, 200)
      assert.include(openapi.headers.get("content-type"), "application/json")
    }),
  )
})
