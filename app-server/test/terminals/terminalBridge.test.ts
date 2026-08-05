import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import * as Socket from "effect/unstable/socket/Socket"
import * as ProjectRepository from "../../src/projects/projectRepository.ts"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { runBridge } from "../../src/terminals/terminalAttach.ts"
import * as Terminals from "../../src/terminals/terminals.ts"
import { makeTestServiceLayers } from "../testLayers.ts"

// The attach bridge in-process, against the fake adapter and an in-memory
// socket: deterministic coverage of bridge behavior the black-box suite
// cannot isolate (terminalAttach.test.ts drives the real transport).

/**
 * An in-memory Socket plus levers: push client frames in, and observe the
 * close events the bridge writes (fromTransformStream consumes CloseEvents
 * internally, so they are recorded by wrapping the writer).
 */
const makeTestSocket = Effect.gen(function* () {
  let push!: (frame: Uint8Array | string) => void
  const readable = new ReadableStream<Uint8Array | string>({
    start(controller) {
      push = (frame) => controller.enqueue(frame)
    },
  })
  const writable = new WritableStream<Uint8Array>()
  const closes: Array<Socket.CloseEvent> = []
  const inner = yield* Socket.fromTransformStream(Effect.succeed({ readable, writable }))
  const socket: Socket.Socket = {
    ...inner,
    writer: Effect.map(inner.writer, (write) => (chunk) => {
      if (chunk instanceof Socket.CloseEvent) closes.push(chunk)
      return write(chunk)
    }),
  }
  return { socket, push, closes }
})

describe("terminal attach bridge (in-process)", () => {
  it.effect("a failing PTY write ends the bridge with 1011 attach_failed", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const projects = yield* ProjectRepository.ProjectRepository
      const project = yield* projects.create({ name: "P", defaultWorkingDirectory: "/tmp" })
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      // The attach client's PTY dies under the bridge: writes start failing
      // while the session itself stays live and reachable.
      const session = fake.sessions.get(sessionNameForTerminalId(created.id))!
      session.writeFails = true

      const { closes, push, socket } = yield* makeTestSocket
      push(new TextEncoder().encode("x"))
      yield* runBridge(terminals, created.id, { cols: 80, rows: 24 }, socket).pipe(Effect.scoped)

      // Dropped input must end the bridge retryably — never silently, and
      // never as 1000 terminal_ended (the session was not proven absent).
      assert.deepStrictEqual(
        closes.map((event) => [event.code, event.reason]),
        [[1011, "attach_failed"]],
      )
      assert.strictEqual((yield* terminals.get(created.id)).status, "live")
    }).pipe(Effect.provide(layer))
  })
})
