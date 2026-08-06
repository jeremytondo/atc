import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer, Option, Stream } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { V1Handlers } from "../../src/api/handlers.ts"
import * as Events from "../../src/events/events.ts"
import type { ResourceChangedEvent } from "../../src/events/events.ts"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { makeTestServiceLayers } from "../testLayers.ts"

// Cascade-publish regression tests (ATC-142): the mutations whose events are
// EASY to lose silently, because the changed resource is not the one the
// request named — the silent-sidebar-staleness class the macOS client depends
// on. Direct publishes (create/update/delete of the named resource) are
// covered by the api.test.ts events test.

const kit = makeTestServiceLayers()
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
  kit.layer,
  BunHttpServer.layerHttpServices,
)

const scratch = mkdtempSync(join(tmpdir(), "atc-event-cascade-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

/** Subscribe and collect real events (heartbeats dropped) into a sink. */
const collect = Effect.gen(function* () {
  const events = yield* Events.Events
  const feed = yield* events.subscribe()
  const sink: Array<ResourceChangedEvent> = []
  yield* feed.pipe(
    Stream.runForEach((item) =>
      Effect.sync(() => {
        if (item !== Events.HEARTBEAT) sink.push(item)
      }),
    ),
    Effect.forkScoped,
  )
  return sink
})

/** Poll (real clock) until `predicate` holds on the sink; bounded. */
const eventually = (sink: Array<ResourceChangedEvent>, predicate: () => boolean) =>
  Effect.gen(function* () {
    for (let attempt = 0; !predicate(); attempt++) {
      assert.isBelow(attempt, 300, `condition never held; saw ${JSON.stringify(sink)}`)
      yield* Effect.sleep("10 millis")
    }
  })

const has = (
  sink: Array<ResourceChangedEvent>,
  expected: ResourceChangedEvent,
): (() => boolean) => {
  const matches = (event: ResourceChangedEvent) =>
    event.resource === expected.resource &&
    event.id === expected.id &&
    event.change === expected.change
  return () => sink.some(matches)
}

const setup = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const project = yield* client.v1.createProject({
    payload: { name: "Cascades", defaultWorkingDirectory: realDir },
  })
  const thread = yield* client.v1.createThread({
    payload: { projectId: project.id, agentId: "codex" },
  })
  return { client, project, thread }
})

describe("cascade event publishes", () => {
  it.live("thread delete publishes the orphaned ended terminal's update", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      // The TUI dies and a read tombstones it: the ended terminal survives
      // the thread delete as an unlinked tombstone (FK SET NULL).
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      yield* client.v1.listTerminals({ query: { projectId: thread.projectId } })

      const sink = yield* collect
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* eventually(sink, has(sink, { resource: "thread", id: thread.id, change: "deleted" }))
      // The tombstone's client-visible threadId changed with the delete.
      yield* eventually(
        sink,
        has(sink, { resource: "terminal", id: terminal.id, change: "updated" }),
      )
      yield* client.v1.deleteTerminal({ params: { terminalId: terminal.id } })
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("reconcile tombstoning a dead TUI terminal publishes terminal AND thread", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })

      const sink = yield* collect
      // The session dies out from under ATC; the next reconciled read (the
      // demand-driven pass) writes the tombstone and must tell subscribers
      // about both the terminal and its thread's derived fields.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      yield* client.v1.listTerminals({ query: { projectId: thread.projectId } })
      yield* eventually(
        sink,
        has(sink, { resource: "terminal", id: terminal.id, change: "updated" }),
      )
      yield* eventually(sink, has(sink, { resource: "thread", id: thread.id, change: "updated" }))
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("activity transitions on the observation feed publish thread updates", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId =
        Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""

      const sink = yield* collect
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(sink, has(sink, { resource: "thread", id: thread.id, change: "updated" }))

      // Each transition publishes once; a repeated identical activity does
      // not re-publish (the coalescing clients rely on staying quiet).
      const updates = () =>
        sink.filter((event) => event.resource === "thread" && event.change === "updated").length
      const afterFirst = updates()
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      yield* eventually(sink, () => updates() === afterFirst + 1)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )
})
