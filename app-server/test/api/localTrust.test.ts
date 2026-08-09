import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { isLoopbackAddress, isLoopbackHost, isLoopbackOrigin } from "../../src/api/localTrust.ts"
import { openApiJson } from "../../src/api/openapi.ts"
import * as Server from "../../src/server.ts"
import { freePort, rawStatus } from "../blackbox.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { TEST_AUTH_TOKEN, TestRepositoryLayers } from "../testLayers.ts"

// Listener trust: spoofed-Host / cross-Origin requests are rejected with 403
// while ordinary local requests pass, and non-loopback requests pass exactly
// when they present the bearer token (ATC-148). Runs against a real loopback
// listener because the guard sits on the HTTP layer, not in the handlers.

const withServer = <A, E, R>(run: (port: number) => Effect.Effect<A, E, R>) =>
  Effect.gen(function* () {
    const port = yield* Effect.promise(freePort)
    yield* Layer.build(
      Server.layer({ port }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
    )
    return yield* run(port)
  }).pipe(Effect.scoped, Effect.provide(BunServices.layer))

describe("host validation", () => {
  it("accepts loopback hosts and rejects everything else", () => {
    assert.isTrue(isLoopbackHost("127.0.0.1:7332"))
    assert.isTrue(isLoopbackHost("127.0.0.1"))
    assert.isTrue(isLoopbackHost("localhost:7332"))
    assert.isTrue(isLoopbackHost("LOCALHOST:7332"))
    assert.isTrue(isLoopbackHost("[::1]:7332"))
    assert.isTrue(isLoopbackHost("[::1]"))
    assert.isFalse(isLoopbackHost(undefined))
    assert.isFalse(isLoopbackHost("evil.example:7332"))
    assert.isFalse(isLoopbackHost("127.0.0.1.evil.example"))
    assert.isFalse(isLoopbackHost("localhost.evil.example"))
    assert.isFalse(isLoopbackHost("192.168.1.10:7332"))
  })

  it("recognizes loopback peer addresses and rejects routable ones", () => {
    assert.isTrue(isLoopbackAddress("127.0.0.1"))
    assert.isTrue(isLoopbackAddress("127.1.2.3"))
    assert.isTrue(isLoopbackAddress("::1"))
    assert.isTrue(isLoopbackAddress("::ffff:127.0.0.1"))
    // The gate that stops a remote client spoofing `Host: localhost`.
    assert.isFalse(isLoopbackAddress("192.168.1.10"))
    assert.isFalse(isLoopbackAddress("10.0.0.5"))
    assert.isFalse(isLoopbackAddress("100.64.0.7"))
    assert.isFalse(isLoopbackAddress("::ffff:192.168.1.10"))
  })

  it("accepts loopback origins and rejects everything else", () => {
    assert.isTrue(isLoopbackOrigin("http://localhost:5173"))
    assert.isTrue(isLoopbackOrigin("http://127.0.0.1:7332"))
    assert.isTrue(isLoopbackOrigin("http://[::1]:7332"))
    assert.isTrue(isLoopbackOrigin("https://localhost"))
    assert.isFalse(isLoopbackOrigin("http://evil.example"))
    assert.isFalse(isLoopbackOrigin("https://localhost.evil.example"))
    assert.isFalse(isLoopbackOrigin("null"))
    assert.isFalse(isLoopbackOrigin("file:///etc/passwd"))
  })
})

describe("hardened listener", () => {
  it.effect("accepts plain local requests and browser requests from loopback origins", () =>
    withServer((port) =>
      Effect.promise(async () => {
        const plain = await fetch(`http://127.0.0.1:${port}/api/v1/health`)
        assert.strictEqual(plain.status, 200)

        const sameOrigin = await fetch(`http://127.0.0.1:${port}/api/v1/health`, {
          headers: { Origin: `http://localhost:5173` },
        })
        assert.strictEqual(sameOrigin.status, 200)
      }),
    ),
  )

  it.effect("rejects a spoofed Host (DNS rebinding) with 403", () =>
    withServer((port) =>
      Effect.promise(async () => {
        assert.strictEqual(await rawStatus(port, ["Host: evil.example"]), 403)
        assert.strictEqual(await rawStatus(port, ["Host: 127.0.0.1.evil.example"]), 403)
        assert.strictEqual(await rawStatus(port, [`Host: 127.0.0.1:${port}`]), 200)
      }),
    ),
  )

  it.effect("serves the canonical OpenAPI document at /openapi.json under the same guard", () =>
    withServer((port) =>
      Effect.promise(async () => {
        const response = await fetch(`http://127.0.0.1:${port}/openapi.json`)
        assert.strictEqual(response.status, 200)
        assert.include(response.headers.get("content-type") ?? "", "application/json")
        // Byte-identical to the canonical serialization — the same text the
        // checked-in openapi.json artifact holds (openapi.test.ts pins that).
        assert.strictEqual(await response.text(), openApiJson)

        assert.strictEqual(await rawStatus(port, ["Host: evil.example"], "/openapi.json"), 403)
      }),
    ),
  )

  it.effect("rejects cross-origin browser requests (CSRF) with 403", () =>
    withServer((port) =>
      Effect.promise(async () => {
        const crossOrigin = await fetch(`http://127.0.0.1:${port}/api/v1/health`, {
          headers: { Origin: "http://evil.example" },
        })
        assert.strictEqual(crossOrigin.status, 403)
        assert.strictEqual(await crossOrigin.text(), "")

        const nullOrigin = await fetch(`http://127.0.0.1:${port}/api/v1/health`, {
          headers: { Origin: "null" },
        })
        assert.strictEqual(nullOrigin.status, 403)
      }),
    ),
  )
})

describe("bearer token trust (remote access)", () => {
  const remoteHost = "Host: workstation.tailnet:7332"
  const bearer = `Authorization: Bearer ${TEST_AUTH_TOKEN}`

  it.effect("a non-loopback request passes exactly when it presents the token", () =>
    withServer((port) =>
      Effect.promise(async () => {
        assert.strictEqual(await rawStatus(port, [remoteHost, bearer]), 200)
        assert.strictEqual(await rawStatus(port, [remoteHost]), 403)
        assert.strictEqual(
          await rawStatus(port, [remoteHost, "Authorization: Bearer atc_wrong"]),
          403,
        )
        assert.strictEqual(
          await rawStatus(port, [remoteHost, `Authorization: Basic ${TEST_AUTH_TOKEN}`]),
          403,
        )
      }),
    ),
  )

  it.effect("the attach WebSocket path sits under the same guard", () =>
    withServer((port) =>
      Effect.promise(async () => {
        const attach = "/api/v1/terminals/nonexistent/attach"
        assert.strictEqual(await rawStatus(port, [remoteHost], attach), 403)
        // With the token the guard passes and the handler answers (404: no
        // such terminal) — proof the token opens the pre-upgrade path too.
        assert.strictEqual(await rawStatus(port, [remoteHost, bearer], attach), 404)
      }),
    ),
  )

  it.effect("loopback requests never need the token", () =>
    withServer((port) =>
      Effect.promise(async () => {
        assert.strictEqual(await rawStatus(port, [`Host: 127.0.0.1:${port}`]), 200)
      }),
    ),
  )
})
