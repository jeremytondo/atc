import { assert, describe, it } from "@effect/vitest"
import { Effect, Stream } from "effect"
import type { AgentEvent } from "../../src/agents/agentAdapter.ts"
import { aggregateActivity } from "../../src/agents/agentAdapter.ts"
import { makeFakeAgentAdapter } from "./fakeAgentAdapter.ts"

// Seam-semantics tests over the fake adapter: the observable rules every
// real adapter must hold (fail-closed resume, single writer, single active
// turn, exact interrupt targets, truthful status feed). PR2/PR3 hold the
// real adapters to the same shape against provider fixtures.

/** Collect events in the background; assertions poll the sink. */
const collectEvents = (events: Stream.Stream<AgentEvent, unknown>) =>
  Effect.gen(function* () {
    const sink: Array<AgentEvent> = []
    yield* events.pipe(
      Stream.runForEach((event) => Effect.sync(() => sink.push(event))),
      Effect.ignore,
      Effect.forkScoped,
    )
    return sink
  })

const waitForEvent = (sink: Array<AgentEvent>, predicate: (event: AgentEvent) => boolean) =>
  Effect.gen(function* () {
    for (let attempt = 0; !sink.some(predicate); attempt++) {
      assert.isBelow(attempt, 100, `never saw expected event in ${JSON.stringify(sink)}`)
      yield* Effect.yieldNow
    }
  })

describe("aggregateActivity", () => {
  it("applies the needs_input > working > unknown > idle precedence", () => {
    assert.strictEqual(aggregateActivity("idle", []), "idle")
    assert.strictEqual(aggregateActivity("idle", ["idle", "idle"]), "idle")
    assert.strictEqual(aggregateActivity("idle", ["working"]), "working")
    assert.strictEqual(aggregateActivity("working", ["idle"]), "working")
    assert.strictEqual(aggregateActivity("idle", ["working", "needs_input"]), "needs_input")
    assert.strictEqual(aggregateActivity("needs_input", ["working"]), "needs_input")
    assert.strictEqual(aggregateActivity("working", ["unknown"]), "working")
  })

  it("one unestablished member keeps an otherwise idle tree unknown", () => {
    assert.strictEqual(aggregateActivity("idle", ["unknown"]), "unknown")
    assert.strictEqual(aggregateActivity("unknown", []), "unknown")
    assert.strictEqual(aggregateActivity("unknown", ["idle"]), "unknown")
  })
})

describe("AgentAdapter seam semantics (fake adapter)", () => {
  it.effect("create starts the first turn and the feed tells the truth", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      const { connection, turn } = yield* fake.adapter.createSession({
        cwd: "/work",
        input: "do the thing",
      })
      const sink = yield* collectEvents(connection.events)
      assert.strictEqual(yield* connection.activity, "working")
      assert.deepStrictEqual(fake.sessions.get(connection.providerSessionId)?.inputs, [
        "do the thing",
      ])

      fake.completeTurn(connection.providerSessionId, "completed")
      yield* waitForEvent(
        sink,
        (event) =>
          event.type === "turnCompleted" &&
          event.turnId === turn.turnId &&
          event.outcome === "completed",
      )
      assert.strictEqual(yield* connection.activity, "idle")
    }).pipe(Effect.scoped),
  )

  it.effect("a second turn while one is active is a conflict", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      const { connection } = yield* fake.adapter.createSession({ cwd: "/work", input: "one" })
      const second = yield* Effect.flip(connection.startTurn("two"))
      assert.strictEqual(second._tag, "AgentConflict")
    }).pipe(Effect.scoped),
  )

  it.effect("resume of an unknown session fails closed", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      const failure = yield* Effect.flip(
        fake.adapter.resumeSession({ providerSessionId: "nope", cwd: "/work" }),
      )
      assert.strictEqual(failure._tag, "AgentResumeFailed")
    }).pipe(Effect.scoped),
  )

  it.effect("resume with a mismatched cwd is an identity mismatch, never adopted", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      fake.seed("session-1", "/original")
      const failure = yield* Effect.flip(
        fake.adapter.resumeSession({ providerSessionId: "session-1", cwd: "/elsewhere" }),
      )
      assert.strictEqual(failure._tag, "AgentIdentityMismatch")
    }).pipe(Effect.scoped),
  )

  it.effect("one writer per session: a concurrent second connection conflicts", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      fake.seed("session-1", "/work")
      yield* fake.adapter.resumeSession({ providerSessionId: "session-1", cwd: "/work" })
      const second = yield* Effect.flip(
        fake.adapter.resumeSession({ providerSessionId: "session-1", cwd: "/work" }),
      )
      assert.strictEqual(second._tag, "AgentConflict")
    }).pipe(Effect.scoped),
  )

  it.effect("closing the connection releases the writer role", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      fake.seed("session-1", "/work")
      yield* Effect.scoped(
        fake.adapter.resumeSession({ providerSessionId: "session-1", cwd: "/work" }),
      )
      // The first writer's scope closed, so a new writer may connect.
      const connection = yield* fake.adapter.resumeSession({
        providerSessionId: "session-1",
        cwd: "/work",
      })
      assert.strictEqual(connection.providerSessionId, "session-1")
    }).pipe(Effect.scoped),
  )

  it.effect("interrupt targets the exact turn; stale targets conflict", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      const { connection, turn } = yield* fake.adapter.createSession({
        cwd: "/work",
        input: "long task",
      })
      const sink = yield* collectEvents(connection.events)
      yield* connection.interrupt(turn)
      yield* waitForEvent(
        sink,
        (event) =>
          event.type === "turnCompleted" &&
          event.turnId === turn.turnId &&
          event.outcome === "interrupted",
      )
      // The turn is over; interrupting it again is stale, not idempotent.
      const stale = yield* Effect.flip(connection.interrupt(turn))
      assert.strictEqual(stale._tag, "AgentConflict")
    }).pipe(Effect.scoped),
  )

  it.effect("a pending provider request wins: needs_input beats working", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      const { connection } = yield* fake.adapter.createSession({ cwd: "/work", input: "ask me" })
      const sink = yield* collectEvents(connection.events)
      const requestId = fake.openRequest(connection.providerSessionId, "approval")
      yield* waitForEvent(
        sink,
        (event) => event.type === "requestOpened" && event.requestId === requestId,
      )
      assert.strictEqual(yield* connection.activity, "needs_input")
      fake.closeRequest(connection.providerSessionId, requestId)
      assert.strictEqual(yield* connection.activity, "working")
    }).pipe(Effect.scoped),
  )

  it.effect("unavailability is injected everywhere", () =>
    Effect.gen(function* () {
      const fake = makeFakeAgentAdapter()
      fake.setUnavailable("fake outage")
      const failure = yield* Effect.flip(fake.adapter.createSession({ cwd: "/work", input: "hi" }))
      assert.strictEqual(failure._tag, "AgentUnavailable")
    }).pipe(Effect.scoped),
  )
})
