import { assert, describe, it } from "@effect/vitest"
import { Effect, Fiber, Option } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { TerminalRepository } from "../../src/terminals/terminalRepository.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { apiTestLayer, eventually, makeTestServiceLayers } from "../testLayers.ts"

// Archive suspends a thread's runtime (ATC-157): the linked zmx-backed TUI
// terminal is killed and adapter-owned session resources released before
// archivedAt is written, while the durable thread and its provider
// conversation identity survive for an exact resume. These tests drive the
// lifecycle through the public contract with the fake adapters; the
// attach-side `1000 terminal_ended` close rides the same Terminals.delete
// the attach bridge suite already proves black-box.

const kit = makeTestServiceLayers()
const TestLayer = apiTestLayer(kit)

// A dedicated kit whose identity never resolves until released — for the
// queues-behind-open paths, where the open must still hold the thread's
// lifecycle lock when archive/delete arrives. The tight TUI-liveness watch
// is the discriminator: an implementation that killed the terminal mid-open
// instead of queueing would trip watchForEarlyDeath and fail the open
// fiber, so a successful Fiber.join on the open proves the wait happened.
const holdKit = makeTestServiceLayers(":memory:", { launchWatchInterval: "25 millis" })
const HoldLayer = apiTestLayer(holdKit)

const scratch = mkdtempSync(join(tmpdir(), "atc-thread-archive-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const setup = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const project = yield* client.v1.createProject({
    payload: { name: "Archive", defaultWorkingDirectory: realDir },
  })
  const thread = yield* client.v1.createThread({
    payload: { projectId: project.id, agentId: "codex" },
  })
  return { client, project, thread }
})

describe("threads.archive", () => {
  it.live("kills the live linked terminal, releases the runtime, then archives", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const record = Option.getOrThrow(yield* repository.get(thread.id))
      const sessionId = record.providerSessionId ?? ""

      const archived = yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      assert.isString(archived.archivedAt)
      assert.isUndefined(archived.linkedTerminalId)

      // The zmx session and the terminal record are gone — an archived
      // thread consumes no live session.
      assert.isFalse(kit.fake.sessions.has(sessionNameForTerminalId(terminal.id)))
      const gone = yield* Effect.flip(
        client.v1.getTerminal({ params: { terminalId: terminal.id } }),
      )
      assert.strictEqual(gone._tag, "TerminalNotFound")

      // Adapter runtime released with the persisted metadata; observation
      // stopped.
      const release = kit.fakeAgents.codex.released.at(-1)
      assert.strictEqual(release?.providerSessionId, sessionId)
      assert.strictEqual(release?.providerMetadata, record.providerMetadata)
      assert.strictEqual(kit.fakeAgents.codex.observerCount(sessionId), 0)

      // The durable row keeps everything needed for an exact later resume.
      const after = Option.getOrThrow(yield* repository.get(thread.id))
      assert.strictEqual(after.providerSessionId, record.providerSessionId)
      assert.strictEqual(after.providerMetadata, record.providerMetadata)
      assert.strictEqual(after.workingDirectory, record.workingDirectory)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("refuses to archive mid-turn without touching the terminal", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""

      // Busy covers needs_input too: a turn parked on an approval is still
      // mid-turn.
      for (const activity of ["working", "needs_input"] as const) {
        kit.fakeAgents.codex.emitActivity(sessionId, activity)
        yield* eventually(
          client.v1.getThread({ params: { threadId: thread.id } }),
          (t) => t.activityState === activity,
        )
        const busy = yield* Effect.flip(
          client.v1.archiveThread({ params: { threadId: thread.id } }),
        )
        assert.strictEqual(busy._tag, "ThreadBusy")
        assert.isTrue(kit.fake.sessions.has(sessionNameForTerminalId(terminal.id)))
        const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
        assert.isUndefined(read.archivedAt)
        assert.strictEqual(read.linkedTerminalId, terminal.id)
      }

      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "idle",
      )
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a kill that cannot verify death fails ZmxUnavailable and archives nothing", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""

      kit.fake.setUnavailable(true)
      const failure = yield* Effect.flip(
        client.v1.archiveThread({ params: { threadId: thread.id } }),
      )
      assert.strictEqual(failure._tag, "ZmxUnavailable")
      kit.fake.setUnavailable(false)

      // The thread stays active with its terminal record intact, and the
      // runtime was NOT released — suspension happens only after confirmed
      // terminal death.
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.isUndefined(read.archivedAt)
      assert.strictEqual(read.linkedTerminalId, terminal.id)
      assert.isTrue(kit.fake.sessions.has(sessionNameForTerminalId(terminal.id)))
      assert.isFalse(kit.fakeAgents.codex.released.some((r) => r.providerSessionId === sessionId))

      // The retry converges once the inventory is back.
      const archived = yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      assert.isString(archived.archivedAt)
      assert.isFalse(kit.fake.sessions.has(sessionNameForTerminalId(terminal.id)))

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("re-archiving converges: a lingering live terminal on an archived thread is killed", () =>
    Effect.gen(function* () {
      const { client, project, thread } = yield* setup
      const terminalRepository = yield* TerminalRepository

      // Archive with no terminal: the plain metadata flip.
      const archived = yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      assert.isString(archived.archivedAt)

      // A legacy row: a live linked terminal owned by an already-archived
      // thread (from before archive killed terminals).
      const record = yield* terminalRepository.create({
        projectId: project.id,
        threadId: thread.id,
        initialWorkingDirectory: realDir,
      })
      kit.fake.seed(sessionNameForTerminalId(record.id))
      yield* terminalRepository.markLive(record.id)

      // Repeat archive succeeds and reaps it — the one-click manual cleanup.
      const again = yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      assert.strictEqual(again.archivedAt, archived.archivedAt)
      assert.isUndefined(again.linkedTerminalId)
      assert.isFalse(kit.fake.sessions.has(sessionNameForTerminalId(record.id)))
      const gone = yield* Effect.flip(client.v1.getTerminal({ params: { terminalId: record.id } }))
      assert.strictEqual(gone._tag, "TerminalNotFound")

      // With nothing live left, repeat archive is a pure no-op.
      const third = yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      assert.deepStrictEqual(third, again)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("archive queues behind an in-flight open, then kills the fresh terminal", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      holdKit.fakeAgents.codex.setIdentityHangs(true)
      const openFiber = yield* client.v1
        .openThreadTerminal({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      yield* eventually(
        Effect.sync(() => holdKit.fake.sessions.size),
        (size) => size === 1,
      )

      // Archive arrives while the open still holds the lifecycle lock: it
      // must queue (not conflict, not act) until the open completes.
      const archiveFiber = yield* client.v1
        .archiveThread({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      const pending = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.isUndefined(pending.archivedAt)

      // The open wins the queue, then archive acts on the resulting
      // terminal — last write wins, and the archived-implies-no-live-
      // terminal invariant holds. The join succeeding is itself an
      // assertion (see the kit comment): a kill that jumped the queue
      // would have failed the open via the liveness watch.
      holdKit.fakeAgents.codex.setIdentityHangs(false)
      const opened = yield* Fiber.join(openFiber)
      const archived = yield* Fiber.join(archiveFiber)
      assert.isString(archived.archivedAt)
      assert.isUndefined(archived.linkedTerminalId)
      assert.isFalse(holdKit.fake.sessions.has(sessionNameForTerminalId(opened.id)))
      const gone = yield* Effect.flip(client.v1.getTerminal({ params: { terminalId: opened.id } }))
      assert.strictEqual(gone._tag, "TerminalNotFound")
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.isString(read.archivedAt)
      assert.isUndefined(read.linkedTerminalId)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(HoldLayer)),
  )

  it.live("delete queues behind an in-flight open, then reaps the fresh terminal", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      holdKit.fakeAgents.codex.setIdentityHangs(true)
      const openFiber = yield* client.v1
        .openThreadTerminal({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      yield* eventually(
        Effect.sync(() => holdKit.fake.sessions.size),
        (size) => size === 1,
      )

      // Delete arrives while the open still holds the lifecycle lock: it
      // queues (the row is still readable), and once the open completes it
      // reaps the fresh terminal, releases the adopted session, and removes
      // the row — the same last-write-wins rule as archive.
      const deleteFiber = yield* client.v1
        .deleteThread({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      const pending = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.strictEqual(pending.id, thread.id)

      holdKit.fakeAgents.codex.setIdentityHangs(false)
      const opened = yield* Fiber.join(openFiber)
      yield* Fiber.join(deleteFiber)
      assert.isFalse(holdKit.fake.sessions.has(sessionNameForTerminalId(opened.id)))
      const goneTerminal = yield* Effect.flip(
        client.v1.getTerminal({ params: { terminalId: opened.id } }),
      )
      assert.strictEqual(goneTerminal._tag, "TerminalNotFound")
      const goneThread = yield* Effect.flip(
        client.v1.getThread({ params: { threadId: thread.id } }),
      )
      assert.strictEqual(goneThread._tag, "ThreadNotFound")
      const fresh = holdKit.fakeAgents.codex.prepared.at(-1)?.providerSessionId ?? ""
      assert.isTrue(
        holdKit.fakeAgents.codex.released.some((r) => r.providerSessionId === fresh),
        "the adopted session was released by the queued delete",
      )
    }).pipe(Effect.provide(HoldLayer)),
  )

  it.live("unarchive launches nothing; the next open resumes the exact confirmed session", () =>
    Effect.gen(function* () {
      const { client, project, thread } = yield* setup
      const repository = yield* ThreadRepository
      yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""

      // Confirm the session (a completed first turn), then go idle.
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        repository.get(thread.id),
        (r) => Option.isSome(r) && r.value.confirmedAt !== undefined,
      )
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "idle",
      )
      const preparedBefore = kit.fakeAgents.codex.prepared.length

      yield* client.v1.archiveThread({ params: { threadId: thread.id } })

      // Unarchive is local-only: no terminal appears.
      const restored = yield* client.v1.unarchiveThread({ params: { threadId: thread.id } })
      assert.isUndefined(restored.archivedAt)
      assert.isUndefined(restored.linkedTerminalId)
      assert.deepStrictEqual(
        yield* client.v1.listTerminals({ query: { projectId: project.id } }),
        [],
      )

      // The next open resumes the exact archived-and-released session in a
      // fresh terminal — no re-materialization.
      const reopened = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const relaunch = kit.fake.sessions.get(sessionNameForTerminalId(reopened.id))
      assert.deepStrictEqual(relaunch?.command, ["fake-agent", "resume", sessionId])
      assert.strictEqual(kit.fakeAgents.codex.prepared.length, preparedBefore)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )
})
