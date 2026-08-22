import { assert, it as effectIt } from "@effect/vitest"
import { Deferred, Effect } from "effect"
import { TestClock } from "effect/testing"
import { describe, expect, it } from "vitest"
import {
  SseParser,
  backoffMillis,
  headers,
  parseConnectionSignal,
  type ResourceSignal,
  subscribe,
} from "../src/sse.ts"

const config = {
  endpoint: new URL("http://127.0.0.1:7331"),
  zmxExecutable: "zmx",
  zmxDir: "/tmp/atc/terminals",
  environment: {},
}

const asFetch = (
  implementation: (
    input: Parameters<typeof fetch>[0],
    init?: Parameters<typeof fetch>[1],
  ) => Promise<Response>,
): typeof fetch => Object.assign(implementation, { preconnect: globalThis.fetch.preconnect })

describe("SseParser", () => {
  it("preserves framing across chunks and surfaces comments immediately", () => {
    const parser = new SseParser()
    const first = parser.consume(new TextEncoder().encode(": con"))
    const second = parser.consume(
      new TextEncoder().encode('nected\r\n\r\ndata: {"resource":"thread",'),
    )
    const third = parser.consume(
      new TextEncoder().encode('"id":"t1","change":"updated"}\n\n: heartbeat\n\n'),
    )

    expect(first).toEqual([])
    expect(second).toEqual([{ type: "comment", value: "connected" }])
    expect(third).toEqual([
      {
        type: "data",
        value: '{"resource":"thread","id":"t1","change":"updated"}',
      },
      { type: "comment", value: "heartbeat" },
    ])
  })

  it("joins multiple data lines", () => {
    const parser = new SseParser()
    expect(parser.consume(new TextEncoder().encode("data: one\ndata: two\n\n"))).toEqual([
      { type: "data", value: "one\ntwo" },
    ])
  })
})

describe("backoffMillis", () => {
  it("doubles and caps at eight seconds", () => {
    expect([0, 1, 2, 3, 4, 10].map(backoffMillis)).toEqual([500, 1_000, 2_000, 4_000, 8_000, 8_000])
  })
})

describe("connection signals", () => {
  it("distinguishes opening comments, heartbeats, and resource changes", () => {
    expect(parseConnectionSignal({ type: "comment", value: "connected" })).toEqual({
      type: "connected",
    })
    expect(parseConnectionSignal({ type: "comment", value: "heartbeat" })).toEqual({
      type: "heartbeat",
    })
    expect(
      parseConnectionSignal({
        type: "data",
        value: '{"resource":"thread","id":"t1","change":"updated"}',
      }),
    ).toEqual({
      type: "change",
      change: { resource: "thread", id: "t1", change: "updated" },
    })
    expect(parseConnectionSignal({ type: "data", value: "not json" })).toBeUndefined()
  })

  it("adds authorization only when configured", () => {
    expect(headers(config)).toEqual({ accept: "text/event-stream" })
    expect(headers({ ...config, token: "secret" })).toEqual({
      accept: "text/event-stream",
      authorization: "Bearer secret",
    })
  })
})

describe("subscription", () => {
  effectIt.effect("keeps an established heartbeat stream alive past both deadlines", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const originalFetch = globalThis.fetch
        const connected = yield* Deferred.make<void>()
        const signals: Array<ResourceSignal> = []
        let bodyController: ReadableStreamDefaultController<Uint8Array> | undefined
        const body = new ReadableStream<Uint8Array>({
          start: (controller) => {
            bodyController = controller
            controller.enqueue(new TextEncoder().encode(": connected\n\n"))
          },
        })
        globalThis.fetch = asFetch(() =>
          Promise.resolve(new Response(body, { headers: { "content-type": "text/event-stream" } })),
        )
        yield* Effect.addFinalizer(() => Effect.sync(() => (globalThis.fetch = originalFetch)))

        yield* Effect.forkScoped(
          subscribe(config, (signal) =>
            Effect.sync(() => signals.push(signal)).pipe(
              Effect.andThen(
                signal.type === "connected" ? Deferred.succeed(connected, void 0) : Effect.void,
              ),
            ),
          ),
        )
        yield* Deferred.await(connected)
        for (let heartbeat = 0; heartbeat < 3; heartbeat += 1) {
          yield* TestClock.adjust("25 seconds")
          bodyController?.enqueue(new TextEncoder().encode(": heartbeat\n\n"))
          yield* Effect.yieldNow
        }

        assert.deepStrictEqual(signals, [{ type: "connected" }])
      }),
    ),
  )

  effectIt.effect("times out only while waiting for the connection response", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const originalFetch = globalThis.fetch
        const signals: Array<ResourceSignal> = []
        const stalledFetch = asFetch(
          (_input, init) =>
            new Promise<Response>((_resolve, reject) => {
              const signal = init?.signal
              if (signal === undefined || signal === null) {
                reject(new Error("missing connection abort signal"))
                return
              }
              signal.addEventListener("abort", () => reject(signal.reason), { once: true })
            }),
        )
        globalThis.fetch = stalledFetch
        yield* Effect.addFinalizer(() => Effect.sync(() => (globalThis.fetch = originalFetch)))

        yield* Effect.forkScoped(
          subscribe(config, (signal) => Effect.sync(() => signals.push(signal))),
        )
        yield* Effect.yieldNow
        yield* TestClock.adjust("10 seconds")
        yield* Effect.yieldNow

        assert.deepStrictEqual(signals[0], {
          type: "disconnected",
          reason: "event stream connection timed out",
        })
      }),
    ),
  )
})
