import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer, Stream } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { V1Handlers } from "../../src/api/handlers.ts"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { TerminalRepository } from "../../src/terminals/terminalRepository.ts"
import { Attachments } from "../../src/threads/attachments.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { TranscriptRepository } from "../../src/threads/transcriptRepository.ts"
import { ThreadRuntime } from "../../src/threads/threadRuntime.ts"
import type { ThreadEvent } from "../../src/threads/threadRuntime.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { eventually, makeTestServiceLayers } from "../testLayers.ts"

// Thread kinds (ATC-224): a thread has one driver for life. A chat thread
// is driven only by the runtime (its terminal operations refuse), is never
// observed or re-read, survives a lost provider session with a notice, and
// refuses a prompt while another client is mid-turn on its session. A tui
// thread is driven only from its terminal (every driving operation
// refuses). All through the fake adapters and the public contract.

const kit = makeTestServiceLayers()
const fake = kit.fakeAgents.codex
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
  kit.layer,
  BunHttpServer.layerHttpServices,
)

const scratch = mkdtempSync(join(tmpdir(), "atc-thread-kinds-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const waitFor = <A>(read: Effect.Effect<A>, predicate: (value: A) => boolean) =>
  eventually(read, predicate, { attempts: 400, interval: "10 millis" })

const setup = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const project = yield* client.v1.createProject({
    payload: { name: `Kinds ${Date.now()}`, defaultWorkingDirectory: realDir },
  })
  const create = (kind: "chat" | "tui") =>
    client.v1.createThread({ payload: { projectId: project.id, agentId: "codex", kind } })
  return { client, project, create }
})

/** Collect a thread's events in the background (scoped). */
const collectEvents = (runtime: ThreadRuntime["Service"], id: string) =>
  Effect.gen(function* () {
    const stream = yield* runtime.subscribe(id)
    const sink: Array<ThreadEvent> = []
    yield* stream.pipe(
      Stream.runForEach((event) => Effect.sync(() => sink.push(event))),
      Effect.forkScoped,
    )
    return sink
  })

/** A chat thread whose first turn completed: confirmed, writer dropped
 * (a shared-server provider holds nothing between turns). */
const settledChatThread = Effect.gen(function* () {
  const { client, create } = yield* setup
  const runtime = yield* ThreadRuntime
  const repository = yield* ThreadRepository
  const thread = yield* create("chat")
  const started = yield* runtime.prompt(thread.id, { prompt: "first" })
  assert.isString(started.turnId)
  const sessionId = (yield* repository.require(thread.id)).providerSessionId ?? ""
  fake.completeTurn(sessionId, "completed")
  yield* waitFor(runtime.hasWriter(thread.id), (held) => !held)
  return { client, thread, sessionId }
})

describe("thread kinds", () => {
  it.effect("the kind is required on the wire, listed, filterable, and immutable", () =>
    Effect.gen(function* () {
      const { client, project, create } = yield* setup
      const chat = yield* create("chat")
      const tui = yield* create("tui")
      assert.strictEqual(chat.kind, "chat")
      assert.strictEqual(tui.kind, "tui")
      const all = yield* client.v1.listThreads({ query: { projectId: project.id } })
      assert.deepStrictEqual(
        all.map((thread) => thread.id).toSorted(),
        [chat.id, tui.id].toSorted(),
      )
      const onlyTui = yield* client.v1.listThreads({
        query: { projectId: project.id, kind: "tui" },
      })
      assert.deepStrictEqual(
        onlyTui.map((thread) => thread.id),
        [tui.id],
      )
      const onlyChat = yield* client.v1.listThreads({
        query: { projectId: project.id, kind: "chat" },
      })
      assert.deepStrictEqual(
        onlyChat.map((thread) => thread.id),
        [chat.id],
      )
      // A rename is kind-agnostic and leaves the kind alone.
      const renamed = yield* client.v1.updateThread({
        params: { threadId: tui.id },
        payload: { name: "still a tui" },
      })
      assert.strictEqual(renamed.kind, "tui")
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("a chat thread has no terminal; a tui thread is never driven from ATC", () =>
    Effect.gen(function* () {
      const { client, create } = yield* setup
      const attachments = yield* Attachments
      const chat = yield* create("chat")
      const tui = yield* create("tui")

      const refusedOpen = yield* client.v1
        .openThreadTerminal({ params: { threadId: chat.id } })
        .pipe(Effect.flip)
      assert.strictEqual(refusedOpen._tag, "ThreadKindMismatch")
      assert.isTrue(refusedOpen._tag === "ThreadKindMismatch" && refusedOpen.kind === "chat")
      const refusedClose = yield* client.v1
        .closeThreadTerminal({ params: { threadId: chat.id } })
        .pipe(Effect.flip)
      assert.strictEqual(refusedClose._tag, "ThreadKindMismatch")

      const refusedPrompt = yield* client.v1
        .promptThread({ params: { threadId: tui.id }, payload: { prompt: "no" } })
        .pipe(Effect.flip)
      assert.strictEqual(refusedPrompt._tag, "ThreadKindMismatch")
      assert.isTrue(refusedPrompt._tag === "ThreadKindMismatch" && refusedPrompt.kind === "tui")
      // Refused before anything was admitted: the queue stays empty.
      assert.deepStrictEqual(yield* client.v1.listThreadQueue({ params: { threadId: tui.id } }), [])
      const refusedInterrupt = yield* client.v1
        .interruptThread({ params: { threadId: tui.id } })
        .pipe(Effect.flip)
      assert.strictEqual(refusedInterrupt._tag, "ThreadKindMismatch")
      // The kind gate comes before the request lookup.
      const refusedAnswer = yield* client.v1
        .answerThreadRequest({
          params: { threadId: tui.id, requestId: "none" },
          payload: { kind: "approval", decision: "accept" },
        })
        .pipe(Effect.flip)
      assert.strictEqual(refusedAnswer._tag, "ThreadKindMismatch")
      const refusedWithdraw = yield* client.v1
        .deleteQueuedPrompt({ params: { threadId: tui.id, promptId: "none" } })
        .pipe(Effect.flip)
      assert.strictEqual(refusedWithdraw._tag, "ThreadKindMismatch")
      const refusedSettings = yield* client.v1
        .updateThread({ params: { threadId: tui.id }, payload: { settings: { access: "auto" } } })
        .pipe(Effect.flip)
      assert.strictEqual(refusedSettings._tag, "ThreadKindMismatch")
      const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1])
      const refusedUpload = yield* attachments
        .create(tui.id, { bytes: png, mediaType: "image/png" })
        .pipe(Effect.flip)
      assert.strictEqual(refusedUpload._tag, "ThreadKindMismatch")

      // The reads stay open on both kinds.
      assert.deepStrictEqual(
        (yield* client.v1.getThreadTranscript({ params: { threadId: tui.id }, query: {} })).items,
        [],
      )
      assert.deepStrictEqual(
        yield* client.v1.listThreadRequests({ params: { threadId: tui.id } }),
        [],
      )
      // And the chat thread's own driving operations are untouched.
      const started = yield* client.v1.promptThread({
        params: { threadId: chat.id },
        payload: { prompt: "hello" },
      })
      assert.isString(started.turnId)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a turn run elsewhere on a chat thread changes nothing in ATC", () =>
    Effect.gen(function* () {
      const { client, thread, sessionId } = yield* settledChatThread
      const runtime = yield* ThreadRuntime
      const events = yield* collectEvents(runtime, thread.id)
      const before = yield* runtime.transcript(thread.id)
      const named = (yield* client.v1.getThread({ params: { threadId: thread.id } })).name

      // Another client drives the session: prompt, busy, idle, new history.
      // A chat thread is never observed, so none of it reaches ATC — no
      // activity, no rows, no re-read, no unread.
      fake.emitUserPrompt(sessionId, "typed in the codex app")
      fake.emitActivity(sessionId, "working")
      fake.emitActivity(sessionId, "idle")
      fake.setHistory(sessionId, [
        {
          turn: { id: "outside-turn", status: "completed" },
          items: [{ type: "userMessage", id: "o1", turnId: "outside-turn", text: "outside" }],
        },
      ])
      yield* runtime.reread(thread.id)
      assert.strictEqual(fake.observerCount(sessionId), 0)
      const after = yield* runtime.transcript(thread.id)
      assert.strictEqual(after.snapshotVersion, 0)
      assert.deepStrictEqual(
        after.items.map((item) => item.id),
        before.items.map((item) => item.id),
      )
      assert.isFalse(events.some((event) => event.type === "snapshot.invalidated"))
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.notStrictEqual(read.activityState, "working")
      assert.strictEqual(read.name, named)
      assert.isUndefined(read.linkedTerminalId)
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live(
    "a lost provider session starts afresh: transcript kept, one notice, a working turn",
    () =>
      Effect.gen(function* () {
        const { thread, sessionId } = yield* settledChatThread
        const runtime = yield* ThreadRuntime
        const repository = yield* ThreadRepository
        const events = yield* collectEvents(runtime, thread.id)
        // The provider swept the session (Claude's cleanup, a deleted Codex
        // thread): the resume fails closed.
        fake.sessions.delete(sessionId)

        const started = yield* runtime.prompt(thread.id, { prompt: "second" })
        assert.isString(started.turnId)
        const record = yield* repository.require(thread.id)
        assert.isString(record.providerSessionId)
        assert.notStrictEqual(record.providerSessionId, sessionId)
        assert.isString(record.confirmedAt)
        const fresh = fake.sessions.get(record.providerSessionId ?? "")
        assert.deepStrictEqual(fresh?.inputs, ["second"])

        yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (page) =>
          page.items.some((item) => item.type === "userMessage" && item.text === "second"),
        )
        const page = yield* runtime.transcript(thread.id)
        // The first turn's rows survive; the notice opens the fresh turn,
        // ahead of its prompt.
        assert.deepStrictEqual(
          page.items.map((item) => [item.type, item.turnId === started.turnId ? "new" : "old"]),
          [
            ["userMessage", "old"],
            ["notice", "new"],
            ["userMessage", "new"],
          ],
        )
        const notice = page.items.find((item) => item.type === "notice")
        assert.strictEqual(
          notice?.type === "notice" ? notice.text : undefined,
          "Previous session was lost; the agent will not remember earlier turns.",
        )
        assert.isTrue(
          events.some((event) => event.type === "item.completed" && event.item.type === "notice"),
        )
        assert.isFalse(events.some((event) => event.type === "snapshot.invalidated"))
        fake.completeTurn(record.providerSessionId ?? "", "completed")
        yield* waitFor(runtime.transcript(thread.id).pipe(Effect.orDie), (current) =>
          current.turns.every((turn) => turn.status === "completed"),
        )
      }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live(
    "a chat thread's replacement counter from before its kind never asks a rejoin to refetch",
    () =>
      Effect.gen(function* () {
        const { thread } = yield* settledChatThread
        const runtime = yield* ThreadRuntime
        const transcripts = yield* TranscriptRepository
        // The one-time migration can classify a thread with earlier re-reads
        // as chat; its snapshot_seq then outruns any cursor a client held.
        const counters = yield* transcripts.replace(thread.id, [
          {
            turn: { id: "old-turn", status: "completed" },
            items: [{ type: "userMessage", id: "o1", turnId: "old-turn", text: "before" }],
          },
        ])
        assert.isTrue(counters.snapshotSeq > 0)
        const rejoined = yield* collectEvents(runtime, thread.id)
        const replay = yield* runtime.subscribe(thread.id, 0)
        const first = yield* replay.pipe(Stream.take(1), Stream.runCollect)
        assert.notStrictEqual([...first][0]?.type, "snapshot.invalidated")
        assert.isFalse(rejoined.some((event) => event.type === "snapshot.invalidated"))
      }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("a terminal linked to a chat thread is never shown or observed", () =>
    Effect.gen(function* () {
      const { client, thread, sessionId } = yield* settledChatThread
      const terminals = yield* TerminalRepository
      // The one-time migration's residue: a TUI still linked to a row that
      // came out chat.
      const record = yield* terminals.create({
        projectId: thread.projectId,
        threadId: thread.id,
        initialWorkingDirectory: thread.workingDirectory,
      })
      kit.fake.seed(sessionNameForTerminalId(record.id))
      yield* terminals.markLive(record.id)
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.isUndefined(read.linkedTerminalId)
      assert.strictEqual(fake.observerCount(sessionId), 0)
      const listed = yield* client.v1.listThreads({ query: { projectId: thread.projectId } })
      assert.isUndefined(listed.find((entry) => entry.id === thread.id)?.linkedTerminalId)
      // The terminal itself is still an ordinary terminal.
      const terminal = yield* client.v1.getTerminal({ params: { terminalId: record.id } })
      assert.strictEqual(terminal.threadId, thread.id)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a chat connection ending mid-turn keeps the streamed text and fails the turn", () =>
    Effect.gen(function* () {
      const { thread, sessionId } = yield* settledChatThread
      const runtime = yield* ThreadRuntime
      const started = yield* runtime.prompt(thread.id, { prompt: "stream" })
      const turnId = started.turnId ?? ""
      fake.emitItem(sessionId, "itemStarted", {
        type: "assistantText",
        id: "s1",
        turnId,
        text: "",
        complete: false,
      })
      // Under the flush threshold and ahead of the throttle: only the
      // writer's own ledger holds this text when the connection dies.
      fake.emitTextDelta(sessionId, "s1", "half an answer")
      fake.endConnection(sessionId)
      const page = yield* waitFor(
        runtime.transcript(thread.id).pipe(Effect.orDie),
        (current) => current.turns.find((turn) => turn.id === turnId)?.status === "failed",
      )
      const item = page.items.find((entry) => entry.id === "s1")
      assert.strictEqual(item?.type === "assistantText" ? item.text : undefined, "half an answer")
      assert.strictEqual(page.snapshotVersion, 0)
      // The writer deregisters once its scope has closed, after the row.
      yield* waitFor(runtime.hasWriter(thread.id), (held) => !held)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a chat thread mid-turn in another client refuses the prompt, un-admitted", () =>
    Effect.gen(function* () {
      const { thread, sessionId } = yield* settledChatThread
      const runtime = yield* ThreadRuntime
      // The shared server reports a turn another client started.
      fake.seedRunningTurn(sessionId, "outside-turn")
      const refused = yield* runtime.prompt(thread.id, { prompt: "wait" }).pipe(Effect.flip)
      assert.strictEqual(refused._tag, "ProviderSessionConflict")
      assert.include(refused.message, "another client")
      // Not queued behind a turn ATC cannot see, and nothing held open.
      assert.deepStrictEqual(yield* runtime.listQueue(thread.id), [])
      assert.isFalse(yield* runtime.hasWriter(thread.id))
      assert.isFalse(fake.isConnected(sessionId))
      // Once it ends, the next prompt runs as usual.
      const started = yield* runtime.prompt(thread.id, { prompt: "now" })
      assert.isString(started.turnId)
      fake.completeTurn(sessionId, "completed")
    }).pipe(Effect.provide(TestLayer)),
  )
})
