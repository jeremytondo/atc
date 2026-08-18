import { assert, describe, it } from "@effect/vitest"
import { Effect, Option, Stream } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import * as Events from "../../src/events/events.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { apiTestLayer, eventually, makeTestServiceLayers } from "../testLayers.ts"

// Auto-naming (ATC-155) through the uniform seam contract: the first user
// prompt observed on an unnamed, unconfirmed thread titles it via the
// adapter's generateTitle one-shot, and a creation-time or manual name
// always wins. A first turn driven natively (ATC-202) is named exactly the
// same way. All through the fake adapters — no provider knowledge.

const kit = makeTestServiceLayers()
const TestLayer = apiTestLayer(kit)

const scratch = mkdtempSync(join(tmpdir(), "atc-thread-title-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

/** One opened unnamed thread with its observation live. */
const openedThread = (name?: string) =>
  Effect.gen(function* () {
    const client = yield* HttpApiTest.groups(Api, ["v1"])
    const repository = yield* ThreadRepository
    const project = yield* client.v1.createProject({
      payload: {
        name: `Title ${name ?? "unnamed"} ${Date.now()}`,
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

/** One unnamed thread with no terminal: its first turn will be native. */
const nativeThread = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const repository = yield* ThreadRepository
  const project = yield* client.v1.createProject({
    payload: { name: `Title native ${Date.now()}`, defaultWorkingDirectory: realDir },
  })
  const thread = yield* client.v1.createThread({
    payload: { projectId: project.id, agentId: "codex" },
  })
  /** The provider session the runtime adopted for the thread. */
  const readSessionId = repository
    .get(thread.id)
    .pipe(Effect.map((record) => Option.getOrThrow(record).providerSessionId ?? ""))
  return { client, threadId: thread.id, readSessionId }
})

describe("thread auto-naming", () => {
  it.live("titles an unnamed thread from its first prompt, once, and publishes", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const { client, threadId, sessionId } = yield* openedThread()
        const events = yield* Events.Events
        const seen: Array<string> = []
        yield* (yield* events.subscribe()).pipe(
          Stream.runForEach((event) =>
            Effect.sync(() => {
              if (
                typeof event === "object" &&
                event.resource === "thread" &&
                event.id === threadId
              ) {
                seen.push(event.change)
              }
            }),
          ),
          Effect.ignore,
          Effect.forkScoped,
        )
        const requestsBefore = kit.fakeAgents.codex.titleRequests.length
        kit.fakeAgents.codex.setTitle('  "Add dark mode to settings."  ')

        kit.fakeAgents.codex.emitUserPrompt(sessionId, "please add dark mode to the settings page")
        yield* eventually(
          client.v1.getThread({ params: { threadId } }),
          (read) => read.name === "Add dark mode to settings",
        )
        // The generation saw the thread's cwd and the verbatim prompt, and
        // the adoption published a thread update.
        const request = kit.fakeAgents.codex.titleRequests[requestsBefore]
        assert.deepStrictEqual(request, {
          cwd: realDir,
          prompt: "please add dark mode to the settings page",
        })
        yield* eventually(
          Effect.sync(() => seen),
          (list) => list.includes("updated"),
        )

        // A later prompt on the same subscription never re-titles.
        kit.fakeAgents.codex.emitUserPrompt(sessionId, "now remove it again")
        kit.fakeAgents.codex.emitActivity(sessionId, "working")
        yield* eventually(
          client.v1.getThread({ params: { threadId } }),
          (read) => read.activityState === "working",
        )
        assert.strictEqual(kit.fakeAgents.codex.titleRequests.length, requestsBefore + 1)

        yield* client.v1.deleteThread({ params: { threadId } })
      }),
    ).pipe(Effect.provide(TestLayer)),
  )

  it.live("titles a thread whose first turn is native from its prompt, once", () =>
    Effect.gen(function* () {
      const fake = kit.fakeAgents.codex
      const { client, threadId, readSessionId } = yield* nativeThread
      const requestsBefore = fake.titleRequests.length
      fake.setTitle('  "Add dark mode to settings."  ')

      const started = yield* client.v1.promptThread({
        params: { threadId },
        payload: { prompt: "please add dark mode to the settings page" },
      })
      assert.isString(started.turnId)
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Add dark mode to settings",
      )
      // The generation saw the thread's cwd and the verbatim prompt — the
      // surface that started the thread is invisible in the request.
      assert.deepStrictEqual(fake.titleRequests[requestsBefore], {
        cwd: realDir,
        prompt: "please add dark mode to the settings page",
      })

      // A later native prompt on the (now confirmed) thread never re-titles.
      // Idle lands before the run's connection is released, so the prompt
      // may start at once or queue and start moments later — either way
      // it runs, and it must not name again.
      fake.completeTurn(yield* readSessionId, "completed")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "idle",
      )
      yield* client.v1.promptThread({
        params: { threadId },
        payload: { prompt: "now remove it again" },
      })
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "working",
      )
      assert.strictEqual(fake.titleRequests.length, requestsBefore + 1)

      fake.completeTurn(yield* readSessionId, "completed")
      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a native first turn followed by a TUI turn names the thread once, not twice", () =>
    Effect.gen(function* () {
      const fake = kit.fakeAgents.codex
      const { client, threadId, readSessionId } = yield* nativeThread
      const requestsBefore = fake.titleRequests.length
      const contextsBefore = fake.contextRequests.length
      fake.setTitle("Native first")

      yield* client.v1.promptThread({
        params: { threadId },
        payload: { prompt: "first, natively" },
      })
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Native first",
      )
      // The native turn ends: its one refinement collects (nothing usable
      // here) and is spent.
      const providerSessionId = yield* readSessionId
      fake.completeTurn(providerSessionId, "completed")
      yield* eventually(
        Effect.sync(() => fake.contextRequests.length),
        (count) => count === contextsBefore + 1,
      )

      // The second turn runs in the TUI over the same, now confirmed,
      // session: neither its prompt nor its busy/idle edges may name or
      // refine again.
      yield* client.v1.openThreadTerminal({ params: { threadId } })
      fake.emitUserPrompt(providerSessionId, "second, in the TUI")
      fake.emitActivity(providerSessionId, "working")
      fake.emitActivity(providerSessionId, "idle")
      // Barrier: a later event observed proves the edges were consumed.
      fake.emitActivity(providerSessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "working",
      )
      assert.strictEqual(fake.titleRequests.length, requestsBefore + 1)
      assert.strictEqual(fake.contextRequests.length, contextsBefore + 1)
      assert.strictEqual(
        (yield* client.v1.getThread({ params: { threadId } })).name,
        "Native first",
      )

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a creation-time name suppresses generation and refinement entirely", () =>
    Effect.gen(function* () {
      const { client, threadId, sessionId } = yield* openedThread("Named at birth")
      const requestsBefore = kit.fakeAgents.codex.titleRequests.length
      const contextsBefore = kit.fakeAgents.codex.contextRequests.length

      kit.fakeAgents.codex.emitUserPrompt(sessionId, "first prompt")
      // A whole first turn passes: neither its busy onset nor its idle
      // edge may trigger any title work on a creation-named thread.
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      // Barrier: a later event observed proves the edges were consumed.
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.activityState === "working",
      )
      assert.strictEqual(kit.fakeAgents.codex.titleRequests.length, requestsBefore)
      assert.strictEqual(kit.fakeAgents.codex.contextRequests.length, contextsBefore)
      assert.strictEqual(
        (yield* client.v1.getThread({ params: { threadId } })).name,
        "Named at birth",
      )

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a manual rename during generation wins", () =>
    Effect.gen(function* () {
      const { client, threadId, sessionId } = yield* openedThread()
      const requestsBefore = kit.fakeAgents.codex.titleRequests.length
      const outcomesBefore = kit.fakeAgents.codex.titleOutcomes.length
      kit.fakeAgents.codex.setTitle("Generated too late")
      kit.fakeAgents.codex.setTitleHangs(true)

      kit.fakeAgents.codex.emitUserPrompt(sessionId, "rename race")
      yield* eventually(
        Effect.sync(() => kit.fakeAgents.codex.titleRequests.length),
        (count) => count === requestsBefore + 1,
      )
      // The rename lands while the one-shot is still in flight.
      yield* client.v1.updateThread({ params: { threadId }, payload: { name: "Manual" } })
      kit.fakeAgents.codex.setTitleHangs(false)
      yield* eventually(
        Effect.sync(() => kit.fakeAgents.codex.titleOutcomes.length),
        (count) => count === outcomesBefore + 1,
      )
      // Generation completed with its own title, yet the manual name holds
      // (the guarded write's atomicity is proven at the repository level).
      assert.deepStrictEqual(kit.fakeAgents.codex.titleOutcomes[outcomesBefore], {
        title: "Generated too late",
      })
      yield* eventually(
        client.v1.getThread({ params: { threadId } }),
        (read) => read.name === "Manual",
      )

      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("generation failure leaves the thread unnamed, silently", () =>
    Effect.gen(function* () {
      const { client, threadId, sessionId } = yield* openedThread()
      const outcomesBefore = kit.fakeAgents.codex.titleOutcomes.length
      kit.fakeAgents.codex.setTitleFails("provider exploded")

      kit.fakeAgents.codex.emitUserPrompt(sessionId, "doomed prompt")
      yield* eventually(
        Effect.sync(() => kit.fakeAgents.codex.titleOutcomes.length),
        (count) => count === outcomesBefore + 1,
      )
      const read = yield* client.v1.getThread({ params: { threadId } })
      assert.isUndefined(read.name)

      kit.fakeAgents.codex.setTitleFails(null)
      yield* client.v1.deleteThread({ params: { threadId } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("the guarded rename is atomic: only the expected name is ever replaced", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const repository = yield* ThreadRepository
      const project = yield* client.v1.createProject({
        payload: { name: `Guard ${Date.now()}`, defaultWorkingDirectory: realDir },
      })
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      // Unnamed: the first-pass write (expected null) adopts.
      const adopted = yield* repository.renameIfUnchanged(thread.id, "Generated", null)
      assert.strictEqual(Option.getOrThrow(adopted).name, "Generated")
      // Named: expected-null refuses — even against another generated name.
      const refused = yield* repository.renameIfUnchanged(thread.id, "Generated again", null)
      assert.isTrue(Option.isNone(refused))
      // The refinement's seed guard: the exact current name replaces, any
      // other expected value no-ops.
      const stale = yield* repository.renameIfUnchanged(thread.id, "Refined", "Some other seed")
      assert.isTrue(Option.isNone(stale))
      const refined = yield* repository.renameIfUnchanged(thread.id, "Refined", "Generated")
      assert.strictEqual(Option.getOrThrow(refined).name, "Refined")
      assert.strictEqual(
        (yield* client.v1.getThread({ params: { threadId: thread.id } })).name,
        "Refined",
      )
      // A vanished row is a silent no-op, not a crash.
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      const gone = yield* repository.renameIfUnchanged(thread.id, "Ghost", null)
      assert.isTrue(Option.isNone(gone))
    }).pipe(Effect.provide(TestLayer)),
  )
})
