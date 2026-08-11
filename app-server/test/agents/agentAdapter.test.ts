import { assert, describe, it } from "@effect/vitest"
import { Deferred, Effect, Fiber, Stream } from "effect"
import type { AgentEvent } from "../../src/agents/agentAdapter.ts"
import {
  aggregateActivity,
  makeVersionGate,
  sanitizeTitle,
  titleInstruction,
} from "../../src/agents/agentAdapter.ts"
import type { Subprocess } from "../../src/platform/subprocess.ts"
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

describe("sanitizeTitle", () => {
  it("enforces the output hygiene rules in code, not in the prompt", () => {
    // First non-empty line only, whitespace collapsed.
    assert.strictEqual(sanitizeTitle("\n\n  Fix   login\tflow  \nsecond line"), "Fix login flow")
    // Wrapping quotes (straight and smart, repeated) stripped.
    assert.strictEqual(sanitizeTitle('"Add dark mode"'), "Add dark mode")
    assert.strictEqual(sanitizeTitle("'“Add dark mode”'"), "Add dark mode")
    // Trailing punctuation dropped.
    assert.strictEqual(sanitizeTitle("Add dark mode."), "Add dark mode")
    assert.strictEqual(sanitizeTitle("Add dark mode!?…"), "Add dark mode")
    // ~50-char cap lands on a word boundary.
    const long = sanitizeTitle(
      "Refactor the authentication middleware pipeline for multi-tenant installs",
    )
    assert.isTrue(long !== null && long.length <= 50, `too long: ${long}`)
    assert.strictEqual(long, "Refactor the authentication middleware pipeline")
    // Nothing usable → null, never an empty title.
    assert.isNull(sanitizeTitle(""))
    assert.isNull(sanitizeTitle("  \n\t\n"))
    assert.isNull(sanitizeTitle('"."'))
  })
})

describe("titleInstruction", () => {
  it("requests the closed activity-prefix taxonomy", () => {
    const instruction = titleInstruction("add a login page")
    assert.include(
      instruction,
      "Use sentence case, not title case: capitalize only the first word and proper nouns.",
    )
    assert.include(instruction, "Build: create, implement, change, or fix something.")
    assert.include(instruction, "Review: review or critique existing code or work.")
    assert.include(instruction, "Grill: explicitly grill or stress-test a plan or design.")
    assert.include(instruction, "Explore: brainstorm, research, compare, or understand options.")
    assert.include(
      instruction,
      "Investigate: diagnose a suspected bug or problem without asking for a fix.",
    )
    assert.include(instruction, "If no category clearly matches, omit the prefix.")
  })

  it("carries a capped prompt", () => {
    const instruction = titleInstruction("add a login page")
    assert.include(instruction, "add a login page")
    const capped = titleInstruction("x".repeat(10_000))
    assert.isBelow(capped.length, 5_000)
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

/**
 * A scripted Subprocess whose spawns are counted and optionally held on a
 * gate, so the gate tests control exactly when the version read completes.
 */
const gatedSubprocess = () => {
  let spawns = 0
  let gate: Deferred.Deferred<void> | null = null
  const service: Subprocess["Service"] = {
    spawn: () =>
      Effect.gen(function* () {
        spawns++
        const held = gate
        if (held !== null) yield* Deferred.await(held)
        return {
          pid: 1,
          stdoutLines: Stream.make("codex-cli 9.9.9"),
          stderrTail: Effect.succeed([]),
          writeLine: () => Effect.void,
          endInput: Effect.void,
          exitCode: Effect.succeed(0),
        }
      }),
    spawnPty: () => Effect.die("unused"),
    spawnDetached: () => Effect.die("unused"),
  }
  return {
    service,
    spawns: () => spawns,
    hold: () => {
      gate = Deferred.makeUnsafe<void>()
    },
    release: () => {
      if (gate !== null) Deferred.doneUnsafe(gate, Effect.void)
      gate = null
    },
  }
}

const untilSpawns = (fake: { spawns: () => number }, count: number) =>
  Effect.gen(function* () {
    for (let attempt = 0; fake.spawns() < count; attempt++) {
      assert.isBelow(attempt, 100, `never reached ${count} spawns (at ${fake.spawns()})`)
      yield* Effect.yieldNow
    }
  })

describe("makeVersionGate", () => {
  it.effect("single-flight: concurrent callers share one version read, memoized after", () =>
    Effect.gen(function* () {
      const fake = gatedSubprocess()
      const gate = yield* makeVersionGate(fake.service, "codex", "/bin/echo", "1.0.0")
      fake.hold()
      const first = yield* Effect.forkChild(gate)
      const second = yield* Effect.forkChild(gate)
      yield* untilSpawns(fake, 1)
      fake.release()
      yield* Fiber.join(first)
      yield* Fiber.join(second)
      assert.strictEqual(fake.spawns(), 1)
      // A later caller replays the memo without re-reading.
      yield* gate
      assert.strictEqual(fake.spawns(), 1)
    }),
  )

  it.effect("an interrupted first caller never poisons the gate", () =>
    Effect.gen(function* () {
      const fake = gatedSubprocess()
      const gate = yield* makeVersionGate(fake.service, "codex", "/bin/echo", "1.0.0")
      fake.hold()
      const first = yield* Effect.forkChild(gate)
      yield* untilSpawns(fake, 1)
      // The driver's interruption must be invalidated on the way out, not
      // memoized — a poisoned memo would replay the interrupt to every
      // later caller for the life of the process.
      yield* Fiber.interrupt(first)
      fake.release()
      yield* gate
      assert.strictEqual(fake.spawns(), 2)
    }),
  )
})
