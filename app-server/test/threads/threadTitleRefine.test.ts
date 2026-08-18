import { assert, describe, it } from "@effect/vitest"
import { Effect, Option } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { apiTestLayer, eventually, makeTestServiceLayers } from "../testLayers.ts"

// Title refinement (ATC-190) through the uniform seam contract: an
// auto-named thread's title is refined at most once from collected
// conversation context — at the refine delay or the turn's end — with
// manual names always winning and every failure silent. All through the
// fake adapters — no provider knowledge, no real 30-second waits (the
// delay is a ThreadsOptions seam; assertions wait on conditions).

// Two kits: one whose refine delay fires promptly inside a test (the "30s
// elapsed" path) and one whose delay never fires (the turn-end path).
const fastKit = makeTestServiceLayers(":memory:", { titleRefineDelay: "20 millis" })
const FastLayer = apiTestLayer(fastKit)
const slowKit = makeTestServiceLayers(":memory:", { titleRefineDelay: "1 hour" })
const SlowLayer = apiTestLayer(slowKit)

const scratch = mkdtempSync(join(tmpdir(), "atc-thread-refine-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

/** One opened thread with its observation live. */
const openedThread = (name?: string) =>
  Effect.gen(function* () {
    const client = yield* HttpApiTest.groups(Api, ["v1"])
    const repository = yield* ThreadRepository
    const project = yield* client.v1.createProject({
      payload: {
        name: `Refine ${name ?? "unnamed"} ${Date.now()}`,
        defaultWorkingDirectory: realDir,
      },
    })
    const thread = yield* client.v1.createThread({
      payload: { projectId: project.id, agentId: "codex", ...(name !== undefined ? { name } : {}) },
    })
    yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
    const record = Option.getOrThrow(yield* repository.get(thread.id))
    return { client, threadId: thread.id, sessionId: record.providerSessionId ?? "" }
  })

describe("thread title refinement", () => {
  it.live("the elapsed delay refines mid-turn, once, and never re-arms", () =>
    Effect.gen(function* () {
      const fake = fastKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()
      const requestsBefore = fake.titleRequests.length
      const contextsBefore = fake.contextRequests.length

      // First pass: the instant name from the opaque prompt.
      fake.setTitle("Build - ATC-128")
      fake.emitUserPrompt(sessionId, "/implement ATC-128")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Build - ATC-128",
      )

      // The turn starts; the delay elapses long before any idle edge.
      fake.setTitleContext(sessionId, "user: /implement ATC-128\nassistant: Restoring terminals")
      fake.setTitle("Build - ATC-128 Restore terminal sessions")
      fake.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Build - ATC-128 Restore terminal sessions",
      )
      // The refine call carried the first prompt, the collected context,
      // and the seed as the current title.
      assert.deepStrictEqual(fake.titleRequests[fake.titleRequests.length - 1], {
        cwd: realDir,
        prompt: "/implement ATC-128",
        refine: {
          context: "user: /implement ATC-128\nassistant: Restoring terminals",
          currentTitle: "Build - ATC-128",
        },
      })
      assert.strictEqual(fake.contextRequests.length, contextsBefore + 1)

      // A later turn never refines again: the thread is spent.
      fake.emitActivity(sessionId, "idle")
      fake.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "working",
      )
      assert.strictEqual(fake.contextRequests.length, contextsBefore + 1)
      assert.strictEqual(fake.titleRequests.length, requestsBefore + 2)

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(FastLayer)),
  )

  it.live("a turn ending before the delay fires the refinement at the idle edge", () =>
    Effect.gen(function* () {
      const fake = slowKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()

      fake.setTitle("Build - ATC-128")
      fake.emitUserPrompt(sessionId, "/implement ATC-128")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Build - ATC-128",
      )

      fake.setTitleContext(sessionId, "assistant: Short turn, real work")
      fake.setTitle("Build - ATC-128 Short but real")
      fake.emitActivity(sessionId, "working")
      fake.emitActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Build - ATC-128 Short but real",
      )

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(SlowLayer)),
  )

  it.live("a superseded session's observation ending never spends a native turn's refinement", () =>
    Effect.gen(function* () {
      const fake = slowKit.fakeAgents.codex
      const repository = yield* ThreadRepository
      // A TUI is open on an idle, unconfirmed session A (ATC-202's likely
      // shape: opened in TUI, first prompt sent from Chat).
      const { client, threadId, sessionId: sessionA } = yield* openedThread()
      const contextsBefore = fake.contextRequests.length
      // A's observation is live: its idle is what the thread reads.
      fake.emitActivity(sessionA, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "idle",
      )

      fake.setTitle("Native title")
      const started = yield* client.v1.promptThread({
        params: { threadId },
        payload: { prompt: "first prompt, natively" },
      })
      assert.isString(started.turnId)
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Native title",
      )
      // The prompt re-materialized the thread as a fresh session B; the read
      // above swapped the observation, ending A's feed. That end belongs to
      // A — B's refinement, armed on B, must keep waiting for B's turn end.
      const sessionB = Option.getOrThrow(yield* repository.get(threadId)).providerSessionId ?? ""
      assert.notStrictEqual(sessionB, sessionA)
      yield* eventually(
        Effect.sync(() => fake.observerCount(sessionA)),
        (count) => count === 0,
      )

      fake.setTitleContext(sessionB, "assistant: did the native work")
      fake.setTitle("Native title refined")
      fake.completeTurn(sessionB, "completed")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Native title refined",
      )
      assert.strictEqual(fake.contextRequests.length, contextsBefore + 1)

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(SlowLayer)),
  )

  it.live("an empty early collect gets exactly one catch-up at turn end", () =>
    Effect.gen(function* () {
      const fake = fastKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()
      const contextsBefore = fake.contextRequests.length

      fake.setTitle("First pass")
      fake.emitUserPrompt(sessionId, "opaque prompt")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "First pass",
      )

      // No context yet: the early attempt collects nothing and waits.
      fake.emitActivity(sessionId, "working")
      yield* eventually(
        Effect.sync(() => fake.contextRequests.length),
        (count) => count === contextsBefore + 1,
      )
      assert.strictEqual((yield* client.v1.getThread({ params: { threadId } })).name, "First pass")

      // Context exists by turn end: the catch-up refines.
      fake.setTitleContext(sessionId, "assistant: The work took shape late")
      fake.setTitle("Refined at turn end")
      fake.emitActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Refined at turn end",
      )
      assert.strictEqual(fake.contextRequests.length, contextsBefore + 2)

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(FastLayer)),
  )

  it.live("a manual rename racing the refinement wins", () =>
    Effect.gen(function* () {
      const fake = fastKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()
      const requestsBefore = fake.titleRequests.length

      fake.setTitle("First pass")
      fake.emitUserPrompt(sessionId, "race prompt")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "First pass",
      )

      fake.setTitleContext(sessionId, "assistant: racing")
      fake.setTitle("Refined too late")
      fake.setTitleHangs(true)
      fake.emitActivity(sessionId, "working")
      // The refine one-shot is in flight (hanging) when the rename lands.
      yield* eventually(
        Effect.sync(() => fake.titleRequests.length),
        (count) => count === requestsBefore + 2,
      )
      const outcomesBefore = fake.titleOutcomes.length
      yield* client.v1.updateThread({ params: { threadId }, payload: { name: "Manual" } })
      fake.setTitleHangs(false)
      yield* eventually(
        Effect.sync(() => fake.titleOutcomes.length),
        (count) => count === outcomesBefore + 1,
      )
      // The refinement completed with its own title, yet the manual name
      // holds — the seed guard no-ops atomically.
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Manual",
      )

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(FastLayer)),
  )

  it.live("context unavailable at turn end keeps the first-pass name silently", () =>
    Effect.gen(function* () {
      const fake = fastKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()
      const requestsBefore = fake.titleRequests.length

      fake.setTitle("First pass")
      fake.emitUserPrompt(sessionId, "opaque prompt")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "First pass",
      )

      // The turn ends with no context ever collected (the provider emitted
      // nothing): the refinement gives up without a title call.
      fake.emitActivity(sessionId, "working")
      fake.emitActivity(sessionId, "idle")
      // Barrier: a later event observed proves the edges were consumed.
      fake.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "working",
      )
      assert.strictEqual(fake.titleRequests.length, requestsBefore + 1)
      assert.strictEqual((yield* client.v1.getThread({ params: { threadId } })).name, "First pass")

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(FastLayer)),
  )

  it.live("a prompt discovered only after a short turn's end still refines", () =>
    Effect.gen(function* () {
      const fake = slowKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()
      const requestsBefore = fake.titleRequests.length

      // The whole turn passes before any prompt evidence (the Codex
      // preview poll can lag even the idle edge).
      fake.setTitleContext(sessionId, "assistant: Restored the terminal sessions")
      fake.setTitle("Build - ATC-128 Restore terminals")
      fake.emitActivity(sessionId, "working")
      fake.emitActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "idle",
      )

      // The late prompt fires the instant pass AND wakes the refinement.
      fake.emitUserPrompt(sessionId, "/implement ATC-128")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Build - ATC-128 Restore terminals",
      )
      yield* eventually(
        Effect.sync(() => fake.titleRequests.slice(requestsBefore)),
        (requests) => requests.some((request) => request.refine !== undefined),
      )

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(SlowLayer)),
  )

  it.live("a failing refine one-shot leaves the first-pass name, silently", () =>
    Effect.gen(function* () {
      const fake = fastKit.fakeAgents.codex
      const { client, threadId, sessionId } = yield* openedThread()

      fake.setTitle("First pass")
      fake.emitUserPrompt(sessionId, "doomed refine")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "First pass",
      )

      fake.setTitleContext(sessionId, "assistant: about to fail")
      fake.setTitleFails("provider exploded")
      const outcomesBefore = fake.titleOutcomes.length
      fake.emitActivity(sessionId, "working")
      yield* eventually(
        Effect.sync(() => fake.titleOutcomes.length),
        (count) => count === outcomesBefore + 1,
      )
      assert.strictEqual((yield* client.v1.getThread({ params: { threadId } })).name, "First pass")

      fake.setTitleFails(null)
      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(FastLayer)),
  )
})
