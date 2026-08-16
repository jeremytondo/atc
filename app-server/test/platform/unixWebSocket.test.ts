import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import * as path from "node:path"
import * as UnixWebSocket from "../../src/platform/unixWebSocket.ts"
import { makeShortSocketDir } from "../blackbox.ts"
import { eventually } from "../testLayers.ts"

// The hand-rolled WebSocket client, two ways: against Bun's own server over
// a unix socket (every frame-length encoding, ping/pong, server-initiated
// close, refusal), and against a raw byte peer that emits exact frames —
// fragmentation with an interleaved ping split across chunk boundaries, a
// masked server frame, and a corrupt length header — the protocol surface
// no cooperative server would exercise.

const serve = (socketPath: string) =>
  Effect.acquireRelease(
    Effect.sync(() =>
      Bun.serve({
        unix: socketPath,
        fetch: (request, server) =>
          server.upgrade(request) ? undefined : new Response("nope", { status: 400 }),
        websocket: {
          message(ws, message) {
            const text = String(message)
            if (text === "PING") ws.ping("hello")
            else if (text === "CLOSE") ws.close(4000, "bye")
            else ws.send(text)
          },
        },
      }),
    ),
    // Not awaited: Bun's stop() promise never settles once the server has
    // closed a websocket itself (observed on 1.3.14).
    (server) => Effect.sync(() => void server.stop(true)),
  )

interface Client {
  readonly connection: UnixWebSocket.Connection
  readonly received: Array<string>
  readonly closed: Array<string>
}

const openClient = (socketPath: string): Effect.Effect<Client> =>
  Effect.callback<Client>((resume) => {
    const connection = UnixWebSocket.open(socketPath)
    const received: Array<string> = []
    const closed: Array<string> = []
    connection.onmessage = (text) => received.push(text)
    connection.onclose = (reason) => closed.push(reason)
    connection.onopen = () => resume(Effect.succeed({ connection, received, closed }))
    return Effect.sync(() => connection.close())
  })

const waitUntil = (check: () => boolean) =>
  eventually(Effect.sync(check), (ok) => ok, { attempts: 200, interval: "10 millis" })

describe("UnixWebSocket", () => {
  it.live("round-trips every frame length class and answers pings", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const socketPath = path.join(makeShortSocketDir(), "ws.sock")
        yield* serve(socketPath)
        const client = yield* openClient(socketPath)
        const messages = ["tiny", "x".repeat(200), "y".repeat(70_000)]
        for (const message of messages) client.connection.send(message)
        yield* waitUntil(() => client.received.length === messages.length)
        assert.deepStrictEqual(client.received, messages)
        // A ping is answered transparently; the connection stays usable.
        client.connection.send("PING")
        client.connection.send("after ping")
        yield* waitUntil(() => client.received.includes("after ping"))
        assert.deepStrictEqual(client.closed, [])
        client.connection.close()
        // The closing handshake completes with the peer's echo.
        yield* waitUntil(() => client.closed.length > 0)
        assert.deepStrictEqual(client.closed, ["closed"])
      }),
    ),
  )

  it.live("a server-initiated close is reported once with its code", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const socketPath = path.join(makeShortSocketDir(), "ws.sock")
        yield* serve(socketPath)
        const client = yield* openClient(socketPath)
        client.connection.send("CLOSE")
        yield* waitUntil(() => client.closed.length > 0)
        assert.deepStrictEqual(client.closed, ["closed by peer (4000)"])
        // Later local closes are no-ops.
        client.connection.close()
        assert.strictEqual(client.closed.length, 1)
      }),
    ),
  )

  it.live("nothing listening is one close before open", () =>
    Effect.gen(function* () {
      const socketPath = path.join(makeShortSocketDir(), "absent.sock")
      const reason = yield* Effect.callback<string>((resume) => {
        const connection = UnixWebSocket.open(socketPath)
        connection.onopen = () => resume(Effect.succeed("opened?!"))
        connection.onclose = (reason) => resume(Effect.succeed(reason))
      })
      assert.include(reason, "connect failed")
    }),
  )
})

// A raw peer: completes the upgrade by hand, then writes exact frame bytes
// in the chunks the test dictates and records what the client sends back.
describe("UnixWebSocket against a raw peer", () => {
  const ACCEPT_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
  const decoder = new TextDecoder()
  const encoder = new TextEncoder()

  /** Serve `socketPath`; `script` gets the accepted socket after the upgrade. */
  const rawPeer = (
    socketPath: string,
    script: (peer: Bun.Socket<undefined>) => void,
    inbound: Array<Uint8Array>,
  ) =>
    Effect.acquireRelease(
      Effect.sync(() =>
        Bun.listen<undefined>({
          unix: socketPath,
          socket: {
            data(peer, chunk) {
              const bytes = new Uint8Array(chunk)
              const head = decoder.decode(bytes)
              if (!head.startsWith("GET ")) {
                inbound.push(bytes)
                return
              }
              const key = /Sec-WebSocket-Key: (.+)\r\n/.exec(head)?.[1] ?? ""
              const accept = new Bun.CryptoHasher("sha1").update(key + ACCEPT_GUID).digest("base64")
              peer.write(
                `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n` +
                  `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
              )
              script(peer)
            },
          },
        }),
      ),
      (listener) => Effect.sync(() => listener.stop(true)),
    )

  /** An unmasked server frame (the normal server→client shape). */
  const frame = (fin: boolean, opcode: number, payload: string): Uint8Array =>
    new Uint8Array([(fin ? 0x80 : 0) | opcode, payload.length, ...encoder.encode(payload)])

  it.live("reassembles fragments split across chunks around an interleaved ping", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const socketPath = path.join(makeShortSocketDir(), "raw.sock")
        const inbound: Array<Uint8Array> = []
        yield* rawPeer(
          socketPath,
          (peer) => {
            // Frame boundaries and chunk boundaries deliberately disagree.
            const bytes = new Uint8Array([
              ...frame(false, 0x1, "hel"),
              ...frame(true, 0x9, "p"),
              ...frame(true, 0x0, "lo"),
            ])
            peer.write(bytes.subarray(0, 4))
            setTimeout(() => peer.write(bytes.subarray(4, 9)), 20)
            setTimeout(() => peer.write(bytes.subarray(9)), 40)
          },
          inbound,
        )
        const client = yield* openClient(socketPath)
        yield* waitUntil(() => client.received.length > 0)
        assert.deepStrictEqual(client.received, ["hello"])
        // The pong (opcode 0xa, masked, one byte) went back for the ping.
        const pong = inbound.find((bytes) => (bytes[0]! & 0x0f) === 0xa)
        assert.isDefined(pong)
        assert.strictEqual(pong![1]!, 0x80 | 1)
        assert.deepStrictEqual(client.closed, [])
      }),
    ),
  )

  it.live("a masked frame is unmasked; a corrupt length is a protocol failure, never a hang", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const socketPath = path.join(makeShortSocketDir(), "raw.sock")
        yield* rawPeer(
          socketPath,
          (peer) => {
            const masked = new Uint8Array([
              0x81,
              0x80 | 2,
              1,
              2,
              3,
              4,
              "o".charCodeAt(0) ^ 1,
              "k".charCodeAt(0) ^ 2,
            ])
            peer.write(masked)
            // A 64-bit length with the reserved high bit set.
            peer.write(new Uint8Array([0x81, 0x7f, 0x80, 0, 0, 0, 0, 0, 0, 0]))
          },
          [],
        )
        const client = yield* openClient(socketPath)
        yield* waitUntil(() => client.closed.length > 0)
        assert.deepStrictEqual(client.received, ["ok"])
        assert.match(client.closed[0]!, /^protocol error/)
      }),
    ),
  )
})
