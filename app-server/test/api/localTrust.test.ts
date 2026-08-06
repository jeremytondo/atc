import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { isLoopbackHost, isLoopbackOrigin } from "../../src/api/localTrust.ts"
import { openApiJson } from "../../src/api/openapi.ts"
import * as Server from "../../src/server.ts"
import { freePort } from "../blackbox.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { TestRepositoryLayers } from "../testLayers.ts"

// Listener hardening: spoofed-Host / cross-Origin requests are rejected with
// 403 while ordinary local requests pass. Runs against a real loopback
// listener because the guard sits on the HTTP layer, not in the handlers.

const withServer = <A, E, R>(run: (port: number) => Effect.Effect<A, E, R>) =>
  Effect.gen(function* () {
    const port = yield* Effect.promise(freePort)
    yield* Layer.build(
      Server.layer({ port }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
    )
    return yield* run(port)
  }).pipe(Effect.scoped, Effect.provide(BunServices.layer))

/** Raw HTTP exchange so tests can send arbitrary Host headers (fetch can't). */
const rawStatus = (
  port: number,
  headers: ReadonlyArray<string>,
  path = "/api/v1/health",
): Promise<number> =>
  new Promise((resolve, reject) => {
    let data = ""
    Bun.connect({
      hostname: "127.0.0.1",
      port,
      socket: {
        open(socket) {
          socket.write(
            [`GET ${path} HTTP/1.1`, ...headers, "Connection: close", "", ""].join("\r\n"),
          )
        },
        data(socket, chunk) {
          data += chunk.toString()
          const match = data.match(/^HTTP\/1\.1 (\d{3})/)
          if (match) {
            socket.end()
            resolve(Number(match[1]))
          }
        },
        error(_socket, error) {
          reject(error)
        },
      },
    }).catch(reject)
  })

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
