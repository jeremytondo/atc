import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { ThreadRuntime } from "../../src/threads/threadRuntime.ts"
import { Threads } from "../../src/threads/threads.ts"
import { Projects } from "../../src/projects/projects.ts"
import type { FakeAgentAdapter } from "../agents/fakeAgentAdapter.ts"
import { eventually, makeTestServiceLayers } from "../testLayers.ts"

// Resident writer connections (ATC-207), through the fake adapters and the
// uniform seam only: on a one-process provider the connection a native
// turn opened outlives the turn — the next prompt starts on it, its feed
// keeps driving the ledger between turns — and closes only at a lifecycle
// boundary (the provider ending it, release, the idle timeout). A
// shared-server provider still holds nothing between turns.

const scratch = mkdtempSync(join(tmpdir(), "atc-residency-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const kit = makeTestServiceLayers()
const claude = kit.fakeAgents.claude
const codex = kit.fakeAgents.codex

const waitFor = <A>(read: Effect.Effect<A>, predicate: (value: A) => boolean) =>
  eventually(read, predicate, { attempts: 400, interval: "10 millis" })

/** Writer connections opened on `sessionId` so far. */
const opened = (fake: FakeAgentAdapter, sessionId: string): number =>
  fake.connectionsOpened.filter((id) => id === sessionId).length

/** A thread on `agentId` whose first native turn has completed: its
 * session is confirmed, and (one-process provider) its writer resident. */
const settledThread = (layerKit: typeof kit, agentId: "claude-code" | "codex", prompt = "first") =>
  Effect.gen(function* () {
    const projects = yield* Projects
    const threads = yield* Threads
    const runtime = yield* ThreadRuntime
    const repository = yield* ThreadRepository
    const fake = agentId === "claude-code" ? layerKit.fakeAgents.claude : layerKit.fakeAgents.codex
    const project = yield* projects.create({ name: "Residency", defaultWorkingDirectory: realDir })
    const thread = yield* threads.create({ projectId: project.id, agentId, kind: "chat" })
    const started = yield* runtime.prompt(thread.id, { prompt: prompt })
    assert.isString(started.turnId)
    const record = yield* repository.require(thread.id)
    const sessionId = record.providerSessionId ?? ""
    fake.completeTurn(sessionId, "completed")
    yield* waitFor(
      threads.get(thread.id).pipe(Effect.orDie),
      (current) => current.activityState === "idle",
    )
    return { threadId: thread.id, sessionId, fake }
  })

describe("Resident writer connections", () => {
  it.live("consecutive native turns on a one-process provider share one connection", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId } = yield* settledThread(kit, "claude-code")
      // The turn ended, the connection did not.
      assert.isTrue(claude.isConnected(sessionId))
      assert.isTrue(yield* runtime.hasWriter(threadId))
      assert.strictEqual(opened(claude, sessionId), 1)

      const second = yield* runtime.prompt(threadId, { prompt: "second" })
      assert.isString(second.turnId)
      // startTurn twice on the same connection: no resume, no new process.
      assert.deepStrictEqual(claude.sessions.get(sessionId)?.inputs, ["first", "second"])
      assert.strictEqual(opened(claude, sessionId), 1)
      claude.completeTurn(sessionId, "completed")
      yield* waitFor(
        runtime.transcript(threadId).pipe(Effect.orDie),
        (transcript) => transcript.turns.filter((turn) => turn.status === "completed").length === 2,
      )
      const after = yield* threads.get(threadId)
      assert.strictEqual(after.activityState, "idle")
      assert.isTrue(after.unread)
      assert.isTrue(claude.isConnected(sessionId))
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a prompt queued behind a turn starts on the retained connection", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const { threadId, sessionId } = yield* settledThread(kit, "claude-code")
      const running = yield* runtime.prompt(threadId, { prompt: "running" })
      assert.isString(running.turnId)
      const queued = yield* runtime.prompt(threadId, { prompt: "queued" })
      assert.isUndefined(queued.turnId)
      claude.completeTurn(sessionId, "completed")
      yield* waitFor(
        Effect.sync(() => claude.sessions.get(sessionId)?.inputs ?? []),
        (inputs) => inputs.includes("queued"),
      )
      assert.strictEqual(opened(claude, sessionId), 1)
      assert.deepStrictEqual(yield* runtime.listQueue(threadId), [])
      claude.completeTurn(sessionId, "completed")
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live(
    "session activity between turns keeps updating the thread, and never blocks a prompt",
    () =>
      Effect.gen(function* () {
        const runtime = yield* ThreadRuntime
        const threads = yield* Threads
        const { threadId, sessionId } = yield* settledThread(kit, "claude-code")
        // Background work on the resident connection (a subagent winding
        // down after the turn's result) busies the thread — its feed is the
        // ledger's evidence — and its idle lands too.
        claude.emitConnectionActivity(sessionId, "working")
        yield* waitFor(
          threads.get(threadId).pipe(Effect.orDie),
          (current) => current.activityState === "working",
        )
        // Our own connection's busy is not "another surface": a prompt
        // starts on it at once rather than queueing behind it.
        const started = yield* runtime.prompt(threadId, { prompt: "while background runs" })
        assert.isString(started.turnId)
        claude.completeTurn(sessionId, "completed")
        yield* waitFor(
          threads.get(threadId).pipe(Effect.orDie),
          (current) => current.activityState === "idle",
        )
        claude.emitConnectionActivity(sessionId, "working")
        yield* waitFor(
          threads.get(threadId).pipe(Effect.orDie),
          (current) => current.activityState === "working",
        )
        claude.emitConnectionActivity(sessionId, "idle")
        yield* waitFor(
          threads.get(threadId).pipe(Effect.orDie),
          (current) => current.activityState === "idle",
        )
      }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a connection the provider ends drops once, and a queued prompt resumes afresh", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId } = yield* settledThread(kit, "claude-code")
      // Claude's shape after an interrupt: turnCompleted(interrupted), then
      // the connection closes and the feed ends — synchronously, before
      // the runtime has seen either — with a prompt already queued behind
      // the turn.
      const started = yield* runtime.prompt(threadId, { prompt: "stop me" })
      assert.isString(started.turnId)
      const queued = yield* runtime.prompt(threadId, { prompt: "and again" })
      assert.isUndefined(queued.turnId)
      claude.completeTurn(sessionId, "interrupted")
      claude.endConnection(sessionId)
      // The queued prompt starts exactly once, on one fresh resume of the
      // same session — never on the dead connection.
      yield* waitFor(
        Effect.sync(() => claude.sessions.get(sessionId)?.inputs ?? []),
        (inputs) => inputs.includes("and again"),
      )
      assert.strictEqual(opened(claude, sessionId), 2)
      assert.deepStrictEqual(claude.sessions.get(sessionId)?.inputs, [
        "first",
        "stop me",
        "and again",
      ])
      assert.deepStrictEqual(yield* runtime.listQueue(threadId), [])
      const transcript = yield* runtime.transcript(threadId)
      assert.deepStrictEqual(
        transcript.turns.map((turn) => turn.status),
        ["completed", "interrupted", "running"],
      )
      claude.completeTurn(sessionId, "completed")

      // The provider ending an idle resident connection: the writer drops,
      // the thread reads idle, and the next prompt resumes.
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "idle",
      )
      claude.endConnection(sessionId)
      yield* waitFor(runtime.hasWriter(threadId), (held) => !held)
      assert.strictEqual((yield* threads.get(threadId)).activityState, "idle")
      const again = yield* runtime.prompt(threadId, { prompt: "once more" })
      assert.isString(again.turnId)
      assert.strictEqual(opened(claude, sessionId), 3)
      claude.completeTurn(sessionId, "completed")
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("archiving a thread releases its resident connection", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId } = yield* settledThread(kit, "claude-code")
      assert.isTrue(claude.isConnected(sessionId))
      yield* threads.archive(threadId)
      assert.isFalse(claude.isConnected(sessionId))
      assert.isFalse(yield* runtime.hasWriter(threadId))
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a shared-server provider holds nothing between turns", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const { threadId, sessionId } = yield* settledThread(kit, "codex")
      yield* waitFor(runtime.hasWriter(threadId), (driving) => !driving)
      assert.isFalse(codex.isConnected(sessionId))
      const second = yield* runtime.prompt(threadId, { prompt: "second" })
      assert.isString(second.turnId)
      assert.strictEqual(opened(codex, sessionId), 2)
      codex.completeTurn(sessionId, "completed")
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live(
    "an idle resident connection closes after the idle timeout; the next prompt resumes",
    () =>
      Effect.gen(function* () {
        const fastKit = makeTestServiceLayers(":memory:", { residentIdleTimeout: "20 millis" })
        yield* Effect.gen(function* () {
          const runtime = yield* ThreadRuntime
          const { threadId, sessionId, fake } = yield* settledThread(fastKit, "claude-code")
          yield* waitFor(
            Effect.sync(() => fake.isConnected(sessionId)),
            (connected) => !connected,
          )
          assert.isFalse(yield* runtime.hasWriter(threadId))
          const started = yield* runtime.prompt(threadId, { prompt: "after the timeout" })
          assert.isString(started.turnId)
          assert.strictEqual(opened(fake, sessionId), 2)
          assert.deepStrictEqual(fake.sessions.get(sessionId)?.inputs, [
            "first",
            "after the timeout",
          ])
          fake.completeTurn(sessionId, "completed")
        }).pipe(Effect.provide(fastKit.layer))
      }),
  )

  it.live("a turn the provider starts on a resident connection is ATC's run", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId } = yield* settledThread(kit, "claude-code")
      const transcript = runtime.transcript(threadId).pipe(Effect.orDie)

      // The provider wakes itself: the turn lands as ours, with its prompt.
      const wakeId = claude.startProviderTurn(
        sessionId,
        "<task-notification>done</task-notification>",
      )
      yield* waitFor(transcript, (current) =>
        current.turns.some((turn) => turn.id === wakeId && turn.status === "running"),
      )
      const midway = yield* transcript
      assert.deepStrictEqual(
        midway.items.filter((item) => item.turnId === wakeId).map((item) => item.type),
        ["userMessage"],
      )
      assert.strictEqual((yield* threads.get(threadId)).activityState, "working")

      // Its requests park with the run and are answerable.
      const requestId = claude.openRequest(sessionId, "question")
      yield* waitFor(runtime.listRequests(threadId).pipe(Effect.orDie), (list) =>
        list.some((request) => request.id === requestId),
      )
      // A prompt admitted meanwhile waits: the connection is mid-turn.
      const later = yield* runtime.prompt(threadId, { prompt: "after" })
      assert.isUndefined(later.turnId)
      assert.deepStrictEqual(claude.sessions.get(sessionId)?.inputs, ["first"])

      yield* runtime.answerRequest(threadId, requestId, {
        kind: "question",
        answers: { q0: ["A"] },
      })
      assert.deepStrictEqual(claude.answers.at(-1)?.requestId, requestId)
      claude.completeTurn(sessionId, "completed")
      yield* waitFor(transcript, (current) =>
        current.turns.some((turn) => turn.id === wakeId && turn.status === "completed"),
      )
      // Its end drains the queue onto the same connection.
      yield* waitFor(
        Effect.sync(() => claude.sessions.get(sessionId)?.inputs ?? []),
        (inputs) => inputs.includes("after"),
      )
      assert.strictEqual(opened(claude, sessionId), 1)
      claude.completeTurn(sessionId, "completed")
    }).pipe(Effect.provide(kit.layer)),
  )
})
