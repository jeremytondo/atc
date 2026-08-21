import { assert, describe, it } from "@effect/vitest"
import { Effect, Stream } from "effect"
import { existsSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { afterAll } from "vitest"
import type { ThreadEvent } from "../../src/threads/threadRuntime.ts"
import { Attachments } from "../../src/threads/attachments.ts"
import { ThreadRuntime } from "../../src/threads/threadRuntime.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { TranscriptRepository } from "../../src/threads/transcriptRepository.ts"
import { Threads } from "../../src/threads/threads.ts"
import { Projects } from "../../src/projects/projects.ts"
import { eventually, makeTestServiceLayers } from "../testLayers.ts"

// The Thread runtime (ATC-193) against the fake adapter and the uniform
// seam contract only: prompt → items → completion, queueing, interrupt,
// parked requests, the transcript and its replayable stream, re-reads, and
// restart recovery. Nothing here knows a provider.

const scratch = mkdtempSync(join(tmpdir(), "atc-runtime-test-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

/** Collect a thread's events in the background (scoped). */
const collectEvents = (runtime: ThreadRuntime["Service"], id: string, after?: number) =>
  Effect.gen(function* () {
    const stream = yield* runtime.subscribe(id, after)
    const sink: Array<ThreadEvent> = []
    yield* stream.pipe(
      Stream.runForEach((event) => Effect.sync(() => sink.push(event))),
      Effect.forkScoped,
    )
    return sink
  })

const waitFor = <A>(read: Effect.Effect<A>, predicate: (value: A) => boolean) =>
  eventually(read, predicate, { attempts: 400, interval: "10 millis" })

const seqs = (events: ReadonlyArray<ThreadEvent>): Array<number> =>
  events.flatMap((event) => ("seq" in event ? [event.seq] : []))

describe("ThreadRuntime", () => {
  const kit = makeTestServiceLayers()
  const fake = kit.fakeAgents.codex

  const newThread = Effect.gen(function* () {
    const projects = yield* Projects
    const threads = yield* Threads
    const project = yield* projects.create({ name: "Runtime", defaultWorkingDirectory: scratch })
    return yield* threads.create({ projectId: project.id, agentId: "codex" })
  })

  it.live("a first prompt creates the session, streams items, and completes the turn", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const thread = yield* newThread
      const events = yield* collectEvents(runtime, thread.id)

      const started = yield* runtime.prompt(thread.id, { prompt: "hello" })
      assert.isString(started.turnId)
      const turnId = started.turnId ?? ""
      // A fresh session was created with the prompt, and identity persisted
      // (and confirmed: the prompt is durable provider history).
      const session = [...fake.sessions.values()].find((entry) => entry.inputs.includes("hello"))
      assert.isDefined(session)
      const record = yield* Effect.flatMap(ThreadRepository, (repo) => repo.require(thread.id))
      assert.strictEqual(record.providerSessionId, session?.providerSessionId)
      assert.isString(record.confirmedAt)
      // The thread reads as working while the runtime drives it.
      yield* waitFor(
        threads.get(thread.id).pipe(Effect.orDie),
        (current) => current.activityState === "working",
      )

      // The adapter streams a reply.
      const providerSessionId = session?.providerSessionId ?? ""
      fake.emitItem(providerSessionId, "itemStarted", {
        type: "assistantText",
        id: "a1",
        turnId,
        text: "",
        complete: false,
      })
      fake.emitTextDelta(providerSessionId, "a1", "hi ")
      fake.emitTextDelta(providerSessionId, "a1", "there")
      fake.emitItem(providerSessionId, "itemCompleted", {
        type: "assistantText",
        id: "a1",
        turnId,
        text: "hi there",
        complete: true,
      })
      fake.completeTurn(providerSessionId, "completed")

      yield* waitFor(
        Effect.sync(() => events),
        (list) => list.some((event) => event.type === "turn.completed"),
      )
      // The queue publishes once per prompt, after the start decision: a
      // prompt that started at once never appears in a published queue.
      assert.deepStrictEqual(
        events.map((event) => event.type),
        [
          "turn.started",
          "queue.updated",
          "item.completed",
          "item.started",
          "text.delta",
          "text.delta",
          "item.completed",
          "turn.completed",
        ],
      )
      // Durable events carry strictly increasing seqs; live-only ones none.
      const durable = seqs(events)
      assert.deepStrictEqual(
        durable,
        durable.toSorted((left, right) => left - right),
      )
      assert.strictEqual(new Set(durable).size, durable.length)

      const transcript = yield* runtime.transcript(thread.id)
      assert.deepStrictEqual(
        transcript.items.map((item) => [item.type, item.id]),
        [
          ["userMessage", `${turnId}:prompt`],
          ["assistantText", "a1"],
        ],
      )
      assert.strictEqual(transcript.turns.length, 1)
      assert.strictEqual(transcript.turns[0]?.status, "completed")
      assert.strictEqual(transcript.turns[0]?.promptId, started.promptId)
      assert.isString(transcript.turns[0]?.endedAt)
      assert.strictEqual(transcript.seq, durable[durable.length - 1])
      assert.isFalse(transcript.hasMore)
      // The turn's end is idle, and — no client viewed it — unread.
      const after = yield* threads.get(thread.id)
      assert.strictEqual(after.activityState, "idle")
      assert.isTrue(after.unread)
      assert.isFalse(yield* runtime.hasWriter(thread.id))
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("a prompt's images reach the adapter, its item, and the queue; delete purges them", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const attachments = yield* Attachments
      const threads = yield* Threads
      const thread = yield* newThread
      const events = yield* collectEvents(runtime, thread.id)
      const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3])
      const shot = yield* attachments.create(thread.id, {
        bytes: png,
        mediaType: "image/png",
        name: "shot.png",
      })
      assert.isTrue(existsSync(shot.path))

      // The turn starts with the image resolved; the fake, like both real
      // adapters, puts it on the prompt's item.
      const started = yield* runtime.prompt(thread.id, { prompt: "look", attachments: [shot.id] })
      assert.isString(started.turnId)
      const session = [...fake.sessions.values()].find((entry) => entry.inputs.includes("look"))
      assert.deepStrictEqual(session?.attachments, [[shot]])
      const page = yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (current) =>
        current.items.some((item) => item.type === "userMessage"),
      )
      const message = page.items.find((item) => item.type === "userMessage")
      assert.deepStrictEqual(message?.type === "userMessage" ? message.attachments : undefined, [
        shot,
      ])

      // A prompt queued behind the running turn lists its images, on the
      // queue and on its event; a foreign id is refused before admission.
      const second = yield* attachments.create(thread.id, { bytes: png, mediaType: "image/png" })
      assert.strictEqual(second.name, "image.png")
      const queued = yield* runtime.prompt(thread.id, {
        prompt: "and this",
        attachments: [second.id],
      })
      assert.isUndefined(queued.turnId)
      assert.deepStrictEqual(
        (yield* runtime.listQueue(thread.id)).map((entry) => entry.attachments),
        [[second]],
      )
      const published = yield* waitFor(
        Effect.sync(() => events.findLast((event) => event.type === "queue.updated")),
        (event) => event?.type === "queue.updated" && event.prompts.length === 1,
      )
      assert.deepStrictEqual(
        published?.type === "queue.updated" ? published.prompts[0]?.attachments : undefined,
        [second],
      )
      const other = yield* newThread
      const elsewhere = yield* attachments.create(other.id, { bytes: png, mediaType: "image/png" })
      const refused = yield* Effect.flip(
        runtime.prompt(thread.id, { prompt: "x", attachments: [elsewhere.id] }),
      )
      assert.strictEqual(refused._tag, "AttachmentNotFound")
      assert.lengthOf(yield* runtime.listQueue(thread.id), 1)

      // The queued prompt drains at the turn's end with its image resolved.
      fake.completeTurn(session?.providerSessionId ?? "", "completed")
      yield* waitFor(
        Effect.sync(() => session?.inputs ?? []),
        (inputs) => inputs.includes("and this"),
      )
      assert.deepStrictEqual(session?.attachments.at(-1), [second])

      // The thread's bytes go with the thread.
      yield* threads.delete(thread.id)
      assert.isFalse(existsSync(dirname(shot.path)))
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("a provider re-read keeps a prompt's images where its turn id is stable", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const attachments = yield* Attachments
      const thread = yield* newThread
      const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3])
      const shot = yield* attachments.create(thread.id, { bytes: png, mediaType: "image/png" })
      const started = yield* runtime.prompt(thread.id, { prompt: "keep", attachments: [shot.id] })
      const turnId = started.turnId ?? ""
      const session = [...fake.sessions.values()].find((entry) => entry.inputs.includes("keep"))
      const providerSessionId = session?.providerSessionId ?? ""
      yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (current) =>
        current.items.some((item) => item.type === "userMessage"),
      )
      fake.completeTurn(providerSessionId, "completed")
      yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (current) =>
        current.turns.every((turn) => turn.status === "completed"),
      )
      // The provider's history keeps the turn id but re-numbers items and
      // knows nothing of ATC's attachments (Codex echoes localImage paths).
      fake.setHistory(providerSessionId, [
        {
          turn: { id: turnId, status: "completed" },
          items: [{ type: "userMessage", id: "item-1", turnId, text: "keep" }],
        },
      ])
      yield* runtime.reread(thread.id)
      const page = yield* runtime.transcript(thread.id)
      assert.strictEqual(page.snapshotVersion, 1)
      const message = page.items.find((item) => item.type === "userMessage")
      assert.deepStrictEqual(message?.type === "userMessage" ? message.attachments : undefined, [
        shot,
      ])
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("streamed text persists as durable partials while the answer streams", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const thread = yield* newThread
      const events = yield* collectEvents(runtime, thread.id)
      const started = yield* runtime.prompt(thread.id, { prompt: "stream" })
      const turnId = started.turnId ?? ""
      const providerSessionId =
        [...fake.sessions.values()].find((entry) => entry.inputs.includes("stream"))
          ?.providerSessionId ?? ""
      fake.emitItem(providerSessionId, "itemStarted", {
        type: "assistantText",
        id: "a1",
        turnId,
        text: "",
        complete: false,
      })
      fake.emitTextDelta(providerSessionId, "a1", "partial ")
      fake.emitTextDelta(providerSessionId, "a1", "answer")
      // The throttle persists the text so far: a fresh read (a reconnect, a
      // second window, GET /transcript) carries the mid-stream answer.
      yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (page) =>
        page.items.some(
          (item) =>
            item.type === "assistantText" && item.text === "partial answer" && !item.complete,
        ),
      )
      // The flush published the partial as a durable item.updated…
      yield* waitFor(
        Effect.sync(() => events),
        (list) =>
          list.some(
            (event) =>
              event.type === "item.updated" &&
              event.item.type === "assistantText" &&
              event.item.text === "partial answer",
          ),
      )
      // …which a subscriber replaying the gap receives too.
      const replay = yield* collectEvents(runtime, thread.id, 0)
      yield* waitFor(
        Effect.sync(() => replay),
        (list) =>
          list.some(
            (event) =>
              event.type === "item.updated" &&
              event.item.type === "assistantText" &&
              event.item.text === "partial answer",
          ),
      )
      // The adapter's own completion stays the final word.
      fake.emitItem(providerSessionId, "itemCompleted", {
        type: "assistantText",
        id: "a1",
        turnId,
        text: "partial answer!",
        complete: true,
      })
      fake.completeTurn(providerSessionId, "completed")
      yield* waitFor(
        Effect.sync(() => events),
        (list) => list.some((event) => event.type === "turn.completed"),
      )
      const transcript = yield* runtime.transcript(thread.id)
      const item = transcript.items.find((entry) => entry.id === "a1")
      assert.isTrue(item?.type === "assistantText" && item.text === "partial answer!")
      assert.isTrue(item?.type === "assistantText" && item.complete)
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("items are stamped: createdAt on every item, completedAt on finished tools", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const thread = yield* newThread
      const started = yield* runtime.prompt(thread.id, { prompt: "work" })
      const turnId = started.turnId ?? ""
      const providerSessionId =
        [...fake.sessions.values()].find((entry) => entry.inputs.includes("work"))
          ?.providerSessionId ?? ""
      fake.emitItem(providerSessionId, "itemStarted", {
        type: "command",
        id: "c1",
        turnId,
        title: "bun test",
        status: "running",
        command: "bun test",
      })
      yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (page) =>
        page.items.some((item) => item.id === "c1"),
      )
      const runningPage = yield* runtime.transcript(thread.id)
      const running = runningPage.items.find((item) => item.id === "c1")
      assert.isString(running?.createdAt)
      assert.isUndefined(running?.type === "command" ? running.completedAt : undefined)
      fake.emitItem(providerSessionId, "itemCompleted", {
        type: "command",
        id: "c1",
        turnId,
        title: "bun test",
        status: "completed",
        command: "bun test",
        exitCode: 0,
      })
      yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (page) =>
        page.items.some(
          (item) => item.id === "c1" && item.type === "command" && item.status === "completed",
        ),
      )
      const page = yield* runtime.transcript(thread.id)
      const done = page.items.find((item) => item.id === "c1")
      // The update kept the first write's createdAt; the terminal status
      // stamped completedAt.
      assert.strictEqual(done?.createdAt, running?.createdAt)
      assert.isString(done?.type === "command" ? done.completedAt : undefined)
      // The user prompt item is dated too.
      assert.isString(page.items.find((item) => item.type === "userMessage")?.createdAt)
      fake.completeTurn(providerSessionId, "completed")
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live(
    "a prompt during a turn queues and runs at the next idle; interrupt keeps the queue",
    () =>
      Effect.gen(function* () {
        const runtime = yield* ThreadRuntime
        const thread = yield* newThread
        const events = yield* collectEvents(runtime, thread.id)
        const first = yield* runtime.prompt(thread.id, { prompt: "one" })
        const providerSessionId =
          [...fake.sessions.values()].find((entry) => entry.inputs.includes("one"))
            ?.providerSessionId ?? ""

        const second = yield* runtime.prompt(thread.id, { prompt: "two" })
        assert.isUndefined(second.turnId)
        const queued = yield* runtime.listQueue(thread.id)
        assert.deepStrictEqual(
          queued.map((entry) => [entry.id, entry.prompt]),
          [[second.promptId, "two"]],
        )
        // A started prompt is a turn now — it cannot be deleted from the queue.
        const gone = yield* Effect.flip(runtime.deleteQueued(thread.id, first.promptId))
        assert.strictEqual(gone._tag, "QueuedPromptNotFound")

        // Interrupt ends the current turn (interrupted) and the queue proceeds.
        yield* runtime.interrupt(thread.id)
        yield* waitFor(
          Effect.sync(() => events),
          (list) =>
            list.some(
              (event) => event.type === "turn.completed" && event.turn.status === "interrupted",
            ),
        )
        yield* waitFor(
          Effect.sync(() => fake.sessions.get(providerSessionId)?.inputs ?? []),
          (inputs) => inputs.length === 2,
        )
        assert.deepStrictEqual(fake.sessions.get(providerSessionId)?.inputs, ["one", "two"])
        assert.deepStrictEqual(yield* runtime.listQueue(thread.id), [])
        // The queue events show the started first prompt (already empty),
        // the second's admission, and the drain — never a queue holding a
        // prompt that is already a turn.
        const queueSizes = () =>
          events.flatMap((event) => (event.type === "queue.updated" ? [event.prompts.length] : []))
        yield* waitFor(Effect.sync(queueSizes), (sizes) => sizes.length === 3)
        assert.deepStrictEqual(queueSizes(), [0, 1, 0])
        // The second turn resumed the exact session and runs the queued prompt.
        const transcript = yield* runtime.transcript(thread.id)
        assert.deepStrictEqual(
          transcript.turns.map((turn) => [turn.status, turn.promptId]),
          [
            ["interrupted", first.promptId],
            ["running", second.promptId],
          ],
        )
        // An idle interrupt after the second turn ends is a no-op.
        fake.completeTurn(providerSessionId, "completed")
        yield* waitFor(
          Effect.sync(() => events),
          (list) => list.filter((event) => event.type === "turn.completed").length === 2,
        )
        yield* runtime.interrupt(thread.id)
        // A waiting prompt can be withdrawn.
        const third = yield* runtime.prompt(thread.id, { prompt: "three" })
        assert.isString(third.turnId)
        const fourth = yield* runtime.prompt(thread.id, { prompt: "four" })
        yield* runtime.deleteQueued(thread.id, fourth.promptId)
        assert.deepStrictEqual(yield* runtime.listQueue(thread.id), [])
        fake.completeTurn(providerSessionId, "completed")
      }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("provider requests park until answered; answers are validated", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const thread = yield* newThread
      const events = yield* collectEvents(runtime, thread.id)
      yield* runtime.prompt(thread.id, { prompt: "ask" })
      const providerSessionId =
        [...fake.sessions.values()].find((entry) => entry.inputs.includes("ask"))
          ?.providerSessionId ?? ""
      const requestId = fake.openRequest(providerSessionId, "question")
      yield* waitFor(
        Effect.sync(() => events),
        (list) => list.some((event) => event.type === "request.opened"),
      )
      const pending = yield* runtime.listRequests(thread.id)
      assert.deepStrictEqual(
        pending.map((request) => [request.id, request.kind]),
        [[requestId, "question"]],
      )
      yield* waitFor(
        threads.get(thread.id).pipe(Effect.orDie),
        (current) => current.activityState === "needs_input",
      )

      const mismatch = yield* Effect.flip(
        runtime.answerRequest(thread.id, requestId, { kind: "approval", decision: "accept" }),
      )
      assert.strictEqual(mismatch._tag, "InvalidRequestAnswer")
      const unknownQuestion = yield* Effect.flip(
        runtime.answerRequest(thread.id, requestId, { kind: "question", answers: { q9: ["A"] } }),
      )
      assert.strictEqual(unknownQuestion._tag, "InvalidRequestAnswer")
      const notAnOption = yield* Effect.flip(
        runtime.answerRequest(thread.id, requestId, {
          kind: "question",
          answers: { q0: ["Z"] },
        }),
      )
      assert.strictEqual(notAnOption._tag, "InvalidRequestAnswer")
      const missing = yield* Effect.flip(
        runtime.answerRequest(thread.id, "nope", { kind: "question", answers: { q0: ["A"] } }),
      )
      assert.strictEqual(missing._tag, "RequestNotFound")
      const unanswered = yield* Effect.flip(
        runtime.answerRequest(thread.id, requestId, { kind: "question", answers: {} }),
      )
      assert.strictEqual(unanswered._tag, "InvalidRequestAnswer")

      yield* runtime.answerRequest(thread.id, requestId, {
        kind: "question",
        answers: { q0: ["B"] },
      })
      assert.deepStrictEqual(fake.answers.at(-1), {
        requestId,
        answer: { kind: "question", answers: { q0: ["B"] } },
      })
      yield* waitFor(
        Effect.sync(() => events),
        (list) => list.some((event) => event.type === "request.closed"),
      )
      assert.deepStrictEqual(yield* runtime.listRequests(thread.id), [])
      const again = yield* Effect.flip(
        runtime.answerRequest(thread.id, requestId, { kind: "question", answers: { q0: ["A"] } }),
      )
      assert.strictEqual(again._tag, "RequestNotFound")
      // Requests die with their turn.
      const second = fake.openRequest(providerSessionId, "approval")
      fake.completeTurn(providerSessionId, "completed")
      yield* waitFor(
        Effect.sync(() => events),
        (list) =>
          list.some((event) => event.type === "request.closed" && event.requestId === second),
      )
      assert.deepStrictEqual(yield* runtime.listRequests(thread.id), [])
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("subscribing after a seq replays every later change, then goes live", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const thread = yield* newThread
      const live = yield* collectEvents(runtime, thread.id)
      const started = yield* runtime.prompt(thread.id, { prompt: "replay me" })
      const turnId = started.turnId ?? ""
      const providerSessionId =
        [...fake.sessions.values()].find((entry) => entry.inputs.includes("replay me"))
          ?.providerSessionId ?? ""
      fake.emitItem(providerSessionId, "itemCompleted", {
        type: "assistantText",
        id: "r1",
        turnId,
        text: "one",
        complete: true,
      })
      yield* waitFor(
        Effect.sync(() => live),
        (list) => list.some((event) => event.type === "item.completed" && event.item.id === "r1"),
      )
      // Rejoin after the prompt item's seq: the reply and its turn replay
      // with their current state, then a live change follows.
      const promptSeq = live.find(
        (event) => event.type === "item.completed" && event.item.type === "userMessage",
      )
      const after = promptSeq !== undefined && "seq" in promptSeq ? promptSeq.seq : -1
      const rejoined = yield* collectEvents(runtime, thread.id, after)
      yield* waitFor(
        Effect.sync(() => rejoined),
        (list) => list.length >= 1,
      )
      assert.deepStrictEqual(
        rejoined.map((event) => [event.type, "seq" in event ? event.seq > after : null]),
        [["item.updated", true]],
      )
      fake.completeTurn(providerSessionId, "completed")
      yield* waitFor(
        Effect.sync(() => rejoined),
        (list) => list.some((event) => event.type === "turn.completed"),
      )
      // Everything after `after` — replayed and live — arrives once, in order.
      const heads = seqs(rejoined)
      assert.deepStrictEqual(
        heads,
        heads.toSorted((left, right) => left - right),
      )
      assert.strictEqual(new Set(heads).size, heads.length)
      assert.isTrue(heads.every((seq) => seq > after))

      // Paging: newest-first pages walk back with `before`.
      const page = yield* runtime.transcript(thread.id, { limit: 1 })
      assert.deepStrictEqual(
        page.items.map((item) => item.id),
        ["r1"],
      )
      assert.isTrue(page.hasMore)
      const older = yield* runtime.transcript(thread.id, { limit: 1, before: "r1" })
      assert.deepStrictEqual(
        older.items.map((item) => item.type),
        ["userMessage"],
      )
      assert.isFalse(older.hasMore)
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live(
    "replay follows seq order, and a rejoin from before a replacement is told to refetch",
    () =>
      Effect.gen(function* () {
        const runtime = yield* ThreadRuntime
        const transcripts = yield* TranscriptRepository
        const thread = yield* newThread
        const started = yield* runtime.prompt(thread.id, { prompt: "order" })
        const turnId = started.turnId ?? ""
        const providerSessionId =
          [...fake.sessions.values()].find((entry) => entry.inputs.includes("order"))
            ?.providerSessionId ?? ""
        // Two items, then the OLDER one is updated: its seq is now the newest.
        fake.emitItem(providerSessionId, "itemStarted", {
          type: "assistantText",
          id: "o1",
          turnId,
          text: "",
          complete: false,
        })
        fake.emitItem(providerSessionId, "itemCompleted", {
          type: "assistantText",
          id: "o2",
          turnId,
          text: "second",
          complete: true,
        })
        fake.emitItem(providerSessionId, "itemCompleted", {
          type: "assistantText",
          id: "o1",
          turnId,
          text: "first, finished late",
          complete: true,
        })
        yield* waitFor(transcripts.counters(thread.id), (counters) => counters.seq >= 5)
        const rejoined = yield* collectEvents(runtime, thread.id, 0)
        yield* waitFor(
          Effect.sync(() => rejoined),
          (list) => list.length >= 4,
        )
        const replayed = seqs(rejoined)
        assert.deepStrictEqual(
          replayed,
          replayed.toSorted((left, right) => left - right),
        )
        // The late update of o1 replays after o2, at its own (higher) seq.
        const ids = rejoined.flatMap((event) =>
          event.type === "item.updated" ? [event.item.id] : [],
        )
        assert.deepStrictEqual(ids.slice(-2), ["o2", "o1"])

        // A replacement in between: a client that left before it must refetch.
        const before = yield* transcripts.counters(thread.id)
        fake.completeTurn(providerSessionId, "completed")
        fake.setHistory(providerSessionId, [
          {
            turn: { id: "h-turn", status: "completed" },
            items: [{ type: "userMessage", id: "h1", turnId: "h-turn", text: "order" }],
          },
        ])
        yield* runtime.reread(thread.id)
        const late = yield* collectEvents(runtime, thread.id, before.seq)
        yield* waitFor(
          Effect.sync(() => late),
          (list) => list.length >= 1,
        )
        const first = late[0]
        assert.isTrue(
          first?.type === "snapshot.invalidated" &&
            first.snapshotVersion === 1 &&
            first.seq > before.seq,
        )
        // A cursor from the replaced copy reads as an empty page (the client
        // sees the new snapshotVersion and restarts).
        const stale = yield* runtime.transcript(thread.id, { before: "o2", limit: 10 })
        assert.deepStrictEqual(stale.items, [])
        assert.isFalse(stale.hasMore)
        assert.strictEqual(stale.snapshotVersion, 1)
      }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("a prompt during a TUI-driven turn queues and drains at the observed idle", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const thread = yield* newThread
      // A confirmed thread whose session the provider knows.
      const seeded = fake.seed(`tui-${crypto.randomUUID()}`, thread.workingDirectory)
      const repo = yield* ThreadRepository
      const record = yield* repo.setProviderSession(thread.id, seeded.providerSessionId, null)
      yield* repo.confirm(record.id)
      const confirmed = yield* repo.require(record.id)
      // The TUI is mid-turn: the observation drain reports busy.
      yield* runtime.noteActivity(confirmed, "working")
      const queued = yield* runtime.prompt(thread.id, { prompt: "after the tui" })
      assert.isUndefined(queued.turnId)
      assert.deepStrictEqual(seeded.inputs, [])
      assert.deepStrictEqual(
        (yield* runtime.listQueue(thread.id)).map((entry) => entry.prompt),
        ["after the tui"],
      )
      // The TUI turn ends: re-read, then the queued prompt runs.
      fake.setHistory(seeded.providerSessionId, [
        {
          turn: { id: "tui-turn", status: "completed" },
          items: [{ type: "userMessage", id: "t1", turnId: "tui-turn", text: "typed in the tui" }],
        },
      ])
      yield* runtime.noteActivity(confirmed, "idle")
      yield* runtime.observedIdle(confirmed)
      yield* waitFor(
        Effect.sync(() => seeded.inputs),
        (inputs) => inputs.length === 1,
      )
      assert.deepStrictEqual(seeded.inputs, ["after the tui"])
      const transcript = yield* runtime.transcript(thread.id)
      // The re-read landed first (the TUI's turn), then the native turn.
      assert.deepStrictEqual(
        transcript.turns.map((turn) => [turn.id === "tui-turn" ? "tui" : "native", turn.status]),
        [
          ["tui", "completed"],
          ["native", "running"],
        ],
      )
      assert.strictEqual(transcript.snapshotVersion, 1)
      fake.completeTurn(seeded.providerSessionId, "completed")
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("a re-read replaces the copy, bumps the snapshot, and never wipes on empty history", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const thread = yield* newThread
      const events = yield* collectEvents(runtime, thread.id)
      const started = yield* runtime.prompt(thread.id, { prompt: "history" })
      const providerSessionId =
        [...fake.sessions.values()].find((entry) => entry.inputs.includes("history"))
          ?.providerSessionId ?? ""
      fake.completeTurn(providerSessionId, "completed")
      yield* waitFor(
        Effect.sync(() => events),
        (list) => list.some((event) => event.type === "turn.completed"),
      )
      const before = yield* runtime.transcript(thread.id)
      assert.strictEqual(before.snapshotVersion, 0)

      // Empty history: the copy stays.
      yield* runtime.reread(thread.id)
      const kept = yield* runtime.transcript(thread.id)
      assert.strictEqual(kept.snapshotVersion, 0)
      assert.strictEqual(kept.items.length, 1)

      fake.setHistory(providerSessionId, [
        {
          turn: { id: "h-turn", status: "completed" },
          items: [
            { type: "userMessage", id: "h1", turnId: "h-turn", text: "history" },
            { type: "assistantText", id: "h2", turnId: "h-turn", text: "done", complete: true },
          ],
        },
      ])
      yield* runtime.reread(thread.id)
      const replaced = yield* runtime.transcript(thread.id)
      assert.strictEqual(replaced.snapshotVersion, 1)
      assert.deepStrictEqual(
        replaced.items.map((item) => item.id),
        ["h1", "h2"],
      )
      assert.deepStrictEqual(
        replaced.turns.map((turn) => turn.id),
        ["h-turn"],
      )
      assert.isTrue(replaced.seq > before.seq)
      yield* waitFor(
        Effect.sync(() => events),
        (list) => list.some((event) => event.type === "snapshot.invalidated"),
      )
      const invalidated = events.find((event) => event.type === "snapshot.invalidated")
      assert.isTrue(
        invalidated?.type === "snapshot.invalidated" &&
          invalidated.snapshotVersion === 1 &&
          invalidated.seq === replaced.seq,
      )
      assert.isString(started.turnId)
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("a provider that refuses the immediate start un-admits the prompt", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const thread = yield* newThread
      fake.setUnavailable("provider down")
      const failure = yield* Effect.flip(runtime.prompt(thread.id, { prompt: "nothing" })).pipe(
        Effect.ensuring(Effect.sync(() => fake.setUnavailable(null))),
      )
      assert.strictEqual(failure._tag, "ProviderUnavailable")
      assert.deepStrictEqual(yield* runtime.listQueue(thread.id), [])
      assert.isFalse(yield* runtime.hasWriter(thread.id))
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )

  it.live("archive refuses mid-turn; delete releases the run", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const thread = yield* newThread
      yield* runtime.prompt(thread.id, { prompt: "busy" })
      const providerSessionId =
        [...fake.sessions.values()].find((entry) => entry.inputs.includes("busy"))
          ?.providerSessionId ?? ""
      const busy = yield* Effect.flip(threads.archive(thread.id))
      assert.strictEqual(busy._tag, "ThreadBusy")
      yield* threads.delete(thread.id)
      assert.isFalse(yield* runtime.hasWriter(thread.id))
      // The fake connection was closed with the run: no writer remains.
      const resumed = yield* Effect.scoped(
        fake.adapter.resumeSession({
          providerSessionId,
          cwd: thread.workingDirectory,
          settings: thread.settings,
        }),
      )
      assert.strictEqual(resumed.providerSessionId, providerSessionId)
    }).pipe(Effect.scoped, Effect.provide(kit.layer)),
  )
})

describe("ThreadRuntime restart", () => {
  it.live("a turn still running on a shared provider server is reattached and followed", () =>
    Effect.gen(function* () {
      const dbFile = join(scratch, `reattach-${crypto.randomUUID()}.db`)
      const first = makeTestServiceLayers(dbFile)
      const { threadId, providerSessionId, turnId, workingDirectory } = yield* Effect.gen(
        function* () {
          const runtime = yield* ThreadRuntime
          const projects = yield* Projects
          const threads = yield* Threads
          const project = yield* projects.create({
            name: "Reattach",
            defaultWorkingDirectory: scratch,
          })
          const thread = yield* threads.create({ projectId: project.id, agentId: "codex" })
          const started = yield* runtime.prompt(thread.id, { prompt: "long running" })
          yield* runtime.prompt(thread.id, { prompt: "afterwards" })
          yield* waitFor(
            runtime.transcript(thread.id).pipe(Effect.orDie),
            (transcript) => transcript.items.length === 1,
          )
          const session = [...first.fakeAgents.codex.sessions.values()].find((entry) =>
            entry.inputs.includes("long running"),
          )
          return {
            threadId: thread.id,
            providerSessionId: session?.providerSessionId ?? "",
            turnId: started.turnId ?? "",
            workingDirectory: thread.workingDirectory,
          }
        },
      ).pipe(Effect.scoped, Effect.provide(first.layer))

      // The provider still runs the turn when ATC comes back.
      const second = makeTestServiceLayers(dbFile)
      const fake = second.fakeAgents.codex
      fake.seed(providerSessionId, workingDirectory)
      fake.seedRunningTurn(providerSessionId, turnId)
      yield* Effect.gen(function* () {
        const runtime = yield* ThreadRuntime
        const threads = yield* Threads
        // Reattached: the run is registered, the thread reads working, the
        // queued prompt waits.
        yield* waitFor(runtime.hasWriter(threadId), (driving) => driving)
        yield* waitFor(
          threads.get(threadId).pipe(Effect.orDie),
          (current) => current.activityState === "working",
        )
        assert.deepStrictEqual(
          (yield* runtime.listQueue(threadId)).map((entry) => entry.prompt),
          ["afterwards"],
        )
        // The turn ends on the provider: followed to completion, then the
        // queue drains onto the same session.
        fake.completeTurn(providerSessionId, "completed")
        yield* waitFor(
          Effect.sync(() => fake.sessions.get(providerSessionId)?.inputs ?? []),
          (inputs) => inputs.length === 1,
        )
        assert.deepStrictEqual(fake.sessions.get(providerSessionId)?.inputs, ["afterwards"])
        const transcript = yield* runtime.transcript(threadId)
        assert.deepStrictEqual(
          transcript.turns.map((turn) => [turn.id === turnId ? "reattached" : "next", turn.status]),
          [
            ["reattached", "completed"],
            ["next", "running"],
          ],
        )
      }).pipe(Effect.scoped, Effect.provide(second.layer))
    }),
  )

  it.live("a running turn is marked interrupted and the queue drains after a restart", () =>
    Effect.gen(function* () {
      const dbFile = join(scratch, `restart-${crypto.randomUUID()}.db`)
      const first = makeTestServiceLayers(dbFile)
      // Session 1: a turn is running and a second prompt waits.
      const { threadId, providerSessionId, workingDirectory } = yield* Effect.gen(function* () {
        const runtime = yield* ThreadRuntime
        const projects = yield* Projects
        const threads = yield* Threads
        const project = yield* projects.create({
          name: "Restart",
          defaultWorkingDirectory: scratch,
        })
        const thread = yield* threads.create({ projectId: project.id, agentId: "codex" })
        yield* runtime.prompt(thread.id, { prompt: "before restart" })
        yield* runtime.prompt(thread.id, { prompt: "after restart" })
        // The prompt item has landed before the "crash".
        yield* waitFor(
          runtime.transcript(thread.id).pipe(Effect.orDie),
          (transcript) => transcript.items.length === 1,
        )
        const session = [...first.fakeAgents.codex.sessions.values()].find((entry) =>
          entry.inputs.includes("before restart"),
        )
        return {
          threadId: thread.id,
          providerSessionId: session?.providerSessionId ?? "",
          workingDirectory: thread.workingDirectory,
        }
      }).pipe(Effect.scoped, Effect.provide(first.layer))

      // Session 2 over the same database: the provider knows the session
      // (seeded) but no turn is running there any more.
      const second = makeTestServiceLayers(dbFile)
      second.fakeAgents.codex.seed(providerSessionId, workingDirectory)
      yield* Effect.gen(function* () {
        const runtime = yield* ThreadRuntime
        yield* waitFor(
          runtime.transcript(threadId).pipe(Effect.orDie),
          (transcript) =>
            transcript.turns.some((turn) => turn.status === "interrupted") &&
            transcript.turns.some((turn) => turn.status === "running"),
        )
        const transcript = yield* runtime.transcript(threadId)
        assert.deepStrictEqual(
          transcript.turns.map((turn) => turn.status),
          ["interrupted", "running"],
        )
        // The queued prompt drained into the new turn on the exact session.
        assert.deepStrictEqual(second.fakeAgents.codex.sessions.get(providerSessionId)?.inputs, [
          "after restart",
        ])
        assert.deepStrictEqual(yield* runtime.listQueue(threadId), [])
        assert.isTrue(yield* runtime.hasWriter(threadId))
      }).pipe(Effect.scoped, Effect.provide(second.layer))
    }),
  )
})
