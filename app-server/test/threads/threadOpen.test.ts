import { assert, describe, it } from "@effect/vitest"
import { Effect, Fiber, Option } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { apiTestLayer, eventually, makeTestServiceLayers } from "../testLayers.ts"

// The terminal-first workflow (ATC-124 / ATC-141) against the uniform seam
// contract only — these tests are the proof that threads/ needs no provider
// knowledge: identity materialization, idempotent reopen, confirmation from
// the normalized feed, provisional re-materialization, fail-closed cleanup,
// release-on-delete, and demand-driven staleness re-derivation, all through
// the fake adapters.

const kit = makeTestServiceLayers()
const TestLayer = apiTestLayer(kit)

// A dedicated kit whose identity await is tight, for the timeout path.
const hangKit = makeTestServiceLayers(":memory:", { identityTimeout: "250 millis" })
const HangLayer = apiTestLayer(hangKit)

// A kit with the default (generous) bound whose identity simply never
// resolves — for externally-interrupted and concurrent-open paths, where
// the open must still be in flight when the test acts.
const holdKit = makeTestServiceLayers()
const HoldLayer = apiTestLayer(holdKit)

// A kit whose TUI-liveness watch is tight, for the fail-fast death path —
// the identity bound stays generous so the failure provably comes from the
// watch, not the timeout.
const watchKit = makeTestServiceLayers(":memory:", { launchWatchInterval: "25 millis" })
const WatchLayer = apiTestLayer(watchKit)

const scratch = mkdtempSync(join(tmpdir(), "atc-thread-open-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const setup = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const project = yield* client.v1.createProject({
    payload: { name: "Open", defaultWorkingDirectory: realDir },
  })
  const thread = yield* client.v1.createThread({
    payload: { projectId: project.id, agentId: "codex" },
  })
  return { client, project, thread }
})

/**
 * The "before the restart" seed shared by the persistence tests: one opened
 * thread with its identity persisted, returning what the post-restart side
 * needs. Runs inside the caller's provided layer stack, so callers can keep
 * acting (emitting activity, awaiting confirmation) in the same build.
 */
const openedThreadState = (name: string) =>
  Effect.gen(function* () {
    const client = yield* HttpApiTest.groups(Api, ["v1"])
    const repository = yield* ThreadRepository
    const project = yield* client.v1.createProject({
      payload: { name, defaultWorkingDirectory: realDir },
    })
    const thread = yield* client.v1.createThread({
      payload: { projectId: project.id, agentId: "codex" },
    })
    const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
    const record = Option.getOrThrow(yield* repository.get(thread.id))
    return {
      threadId: thread.id,
      terminalId: terminal.id,
      sessionId: record.providerSessionId ?? "",
    }
  })

describe("threads.openTerminal", () => {
  it.live("materializes a fresh session: launch, identity, persistence, idempotency", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const preparedBefore = kit.fakeAgents.codex.prepared.length

      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      assert.strictEqual(terminal.threadId, thread.id)
      assert.strictEqual(terminal.status, "live")

      // The terminal runs the adapter's launch spec with the launch env
      // applied and the nested-session markers scrubbed.
      const session = kit.fake.sessions.get(sessionNameForTerminalId(terminal.id))
      assert.deepStrictEqual(session?.command?.slice(0, 2), ["fake-agent", "--fresh"])
      assert.strictEqual(session?.env?.["FAKE_AGENT"], "1")
      assert.isTrue(session?.env !== undefined && "CLAUDECODE" in session.env)
      assert.isUndefined(session?.env?.["CLAUDECODE"])

      // Identity and opaque metadata are persisted, unconfirmed.
      const record = Option.getOrThrow(yield* repository.get(thread.id))
      assert.strictEqual(record.providerSessionId, session?.command?.[2])
      assert.include(record.providerMetadata ?? "", "fake")
      assert.isUndefined(record.confirmedAt)

      // Honest status: a session exists but no evidence has arrived.
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.strictEqual(read.linkedTerminalId, terminal.id)
      assert.strictEqual(read.activityState, "unknown")

      // Idempotent: a second open returns the live linked terminal as-is.
      const again = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      assert.strictEqual(again.id, terminal.id)
      assert.strictEqual(kit.fakeAgents.codex.prepared.length, preparedBefore + 1)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("confirms at the feed's first busy signal and reopens the exact session", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const record = Option.getOrThrow(yield* repository.get(thread.id))
      const sessionId = record.providerSessionId ?? ""
      const preparedAfterOpen = kit.fakeAgents.codex.prepared.length

      // The one normalized feed: busy = a submitted first prompt — the
      // confirmed marker persists at onset, activity tracks truthfully.
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "working",
      )
      yield* eventually(
        repository.get(thread.id),
        (r) => Option.isSome(r) && r.value.confirmedAt !== undefined,
      )
      // Archive is refused mid-turn.
      const busy = yield* Effect.flip(client.v1.archiveThread({ params: { threadId: thread.id } }))
      assert.strictEqual(busy._tag, "ThreadBusy")
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")

      // The TUI ends; the confirmed session reopens with its exact id.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      const reopened = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      assert.notStrictEqual(reopened.id, terminal.id)
      const relaunch = kit.fake.sessions.get(sessionNameForTerminalId(reopened.id))
      assert.deepStrictEqual(relaunch?.command, ["fake-agent", "resume", sessionId])
      // Reopen is exact resume — no fresh session was prepared.
      assert.strictEqual(kit.fakeAgents.codex.prepared.length, preparedAfterOpen)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("reopening a confirmed session the provider pruned fails typed, launching nothing", () =>
    Effect.gen(function* () {
      const { client, thread, project } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        repository.get(thread.id),
        (r) => Option.isSome(r) && r.value.confirmedAt !== undefined,
      )
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")

      // The TUI ends; meanwhile the provider's history for the session is
      // pruned. The reopen probe fails closed with a presentable error —
      // no blind `resume` launch that dies in the pty — and repeats
      // consistently on every retry.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      kit.fakeAgents.codex.setSessionPruned(sessionId, true)
      for (let attempt = 0; attempt < 2; attempt++) {
        const failure = yield* Effect.flip(
          client.v1.openThreadTerminal({ params: { threadId: thread.id } }),
        )
        assert.strictEqual(failure._tag, "ProviderSessionConflict")
      }
      // Only the first TUI's tombstone remains — no new terminal launched.
      const terminals = yield* client.v1.listTerminals({ query: { projectId: project.id } })
      assert.deepStrictEqual(
        terminals.map((t) => ({ id: t.id, status: t.status })),
        [{ id: terminal.id, status: "ended" }],
      )
      kit.fakeAgents.codex.setSessionPruned(sessionId, false)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* client.v1.deleteTerminal({ params: { terminalId: terminal.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("the busy→idle drop marks the thread unread until viewed (ATC-160)", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""

      // Mid-turn is never unread: unread marks a finish, not activity.
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "working",
      )
      assert.isFalse((yield* client.v1.getThread({ params: { threadId: thread.id } })).unread)

      // The drop stamps: the thread reads idle AND unread until viewed.
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "idle" && t.unread,
      )

      // Viewing clears it everywhere; repeating is a byte-identical no-op.
      const viewed = yield* client.v1.markThreadViewed({ params: { threadId: thread.id } })
      assert.isFalse(viewed.unread)
      assert.deepStrictEqual(
        yield* client.v1.markThreadViewed({ params: { threadId: thread.id } }),
        viewed,
      )

      // The next turn re-arms the overlay.
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      yield* eventually(client.v1.getThread({ params: { threadId: thread.id } }), (t) => t.unread)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("an archived thread is never unread; unarchive resurfaces the pending finish", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      kit.fakeAgents.codex.emitActivity(sessionId, "idle")
      yield* eventually(client.v1.getThread({ params: { threadId: thread.id } }), (t) => t.unread)

      // Archive is a deliberate put-away: the archived row reads not-unread
      // even though the finish was never viewed…
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      const archived = yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      assert.isFalse(archived.unread)

      // …and unarchive resurfaces it: the result is still unseen.
      const restored = yield* client.v1.unarchiveThread({ params: { threadId: thread.id } })
      assert.isTrue(restored.unread)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("an unconfirmed session re-materializes with fresh identity on the next open", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const first = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""

      // Zero completed turns: the TUI ends before any prompt. The next
      // open must not resume — there is no history to protect, and the
      // superseded session's adapter resources are released.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const second = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
      assert.isAbove(second.length, 0)
      assert.notStrictEqual(second, first)
      assert.isTrue(
        kit.fakeAgents.codex.released.some((r) => r.providerSessionId === first),
        "the superseded session was released",
      )

      // The NEW session's feed drives the thread — the old subscription is
      // gone, and the old feed can no longer confirm anything.
      kit.fakeAgents.codex.emitActivity(first, "working")
      const record = Option.getOrThrow(yield* repository.get(thread.id))
      assert.isUndefined(record.confirmedAt)

      kit.fakeAgents.codex.emitActivity(second, "working")
      yield* eventually(
        repository.get(thread.id),
        (r) => Option.isSome(r) && r.value.confirmedAt !== undefined,
      )

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("an interrupted open leaves no live terminal behind", () =>
    Effect.gen(function* () {
      const { client, thread, project } = yield* setup
      holdKit.fakeAgents.codex.setIdentityHangs(true)
      const fiber = yield* client.v1
        .openThreadTerminal({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      // The terminal is live and identity is still pending…
      yield* eventually(
        Effect.sync(() => holdKit.fake.sessions.size),
        (size) => size === 1,
      )

      // …and a concurrent second open never double-launches: the in-flight
      // terminal is already live and linked, so the idempotent path returns
      // it as-is.
      const second = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      assert.strictEqual(second.threadId, thread.id)
      assert.strictEqual(holdKit.fake.sessions.size, 1)

      // The caller goes away (Ctrl-C / dropped connection): the launched
      // terminal must not survive as an orphan the thread adopted nothing
      // from.
      yield* Fiber.interrupt(fiber)
      yield* eventually(
        Effect.sync(() => holdKit.fake.sessions.size),
        (size) => size === 0,
      )
      assert.deepStrictEqual(
        yield* client.v1.listTerminals({ query: { projectId: project.id } }),
        [],
      )
      holdKit.fakeAgents.codex.setIdentityHangs(false)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(HoldLayer)),
  )

  it.live("identity resolving after a mid-open delete releases the fresh session", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const threadRepository = yield* ThreadRepository
      holdKit.fakeAgents.codex.setIdentityHangs(true)
      const fiber = yield* client.v1
        .openThreadTerminal({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      yield* eventually(
        Effect.sync(() => holdKit.fake.sessions.size),
        (size) => size === 1,
      )
      const fresh = holdKit.fakeAgents.codex.prepared.at(-1)?.providerSessionId ?? ""

      // The row vanishes while identity is still pending. Threads.delete
      // itself now queues behind the in-flight open (ATC-157), so this
      // models the residual ATC-139 window (a repository-level cascade or
      // defect) directly at the repository: when the identity then
      // resolves, adoption finds no row — the ownership bracket must
      // release the fresh session's adapter resources, because no thread
      // row will ever own (or release) them.
      yield* threadRepository.delete(thread.id)
      holdKit.fakeAgents.codex.setIdentityHangs(false)
      const exit = yield* Fiber.await(fiber)
      assert.strictEqual(exit._tag, "Failure")
      yield* eventually(
        Effect.sync(() => holdKit.fakeAgents.codex.released),
        (released) => released.some((r) => r.providerSessionId === fresh),
      )
      assert.strictEqual(holdKit.fake.sessions.size, 0)
    }).pipe(Effect.provide(HoldLayer)),
  )

  it.live("a TUI that dies during the identity wait fails fast with a real diagnostic", () =>
    Effect.gen(function* () {
      const { client, thread, project } = yield* setup
      watchKit.fakeAgents.codex.setIdentityHangs(true)
      const fiber = yield* client.v1
        .openThreadTerminal({ params: { threadId: thread.id } })
        .pipe(Effect.forkChild)
      yield* eventually(
        Effect.sync(() => watchKit.fake.sessions.size),
        (size) => size === 1,
      )

      // The TUI exits immediately (the first-run auth failure): the open
      // fails with the specific diagnostic well before the 30s bound, and
      // nothing survives.
      watchKit.fake.sessions.clear()
      const failure = yield* Effect.flip(Fiber.join(fiber))
      assert.strictEqual(failure._tag, "ProviderUnavailable")
      if (failure._tag === "ProviderUnavailable") {
        assert.include(failure.reason, "TUI exited")
      }
      assert.deepStrictEqual(
        yield* client.v1.listTerminals({ query: { projectId: project.id } }),
        [],
      )
      watchKit.fakeAgents.codex.setIdentityHangs(false)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(WatchLayer)),
  )

  it.live("identity establishment failing in time cleans up the launched terminal", () =>
    Effect.gen(function* () {
      const { client, thread, project } = yield* setup
      hangKit.fakeAgents.codex.setIdentityHangs(true)
      const failure = yield* Effect.flip(
        client.v1.openThreadTerminal({ params: { threadId: thread.id } }),
      )
      assert.strictEqual(failure._tag, "ProviderUnavailable")
      // Fail-closed: no terminal survives, nothing was adopted.
      assert.deepStrictEqual(
        yield* client.v1.listTerminals({ query: { projectId: project.id } }),
        [],
      )
      assert.strictEqual(hangKit.fake.sessions.size, 0)
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.strictEqual(read.activityState, "idle")
      hangKit.fakeAgents.codex.setIdentityHangs(false)
    }).pipe(Effect.provide(HangLayer)),
  )

  it.live("archived threads are blocked from opening", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      const failure = yield* Effect.flip(
        client.v1.openThreadTerminal({ params: { threadId: thread.id } }),
      )
      assert.strictEqual(failure._tag, "ThreadArchived")
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("delete releases the adapter's session resources with the persisted metadata", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const record = Option.getOrThrow(yield* repository.get(thread.id))
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      const release = kit.fakeAgents.codex.released.at(-1)
      assert.strictEqual(release?.providerSessionId, record.providerSessionId)
      assert.strictEqual(release?.providerMetadata, record.providerMetadata)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("restart recovery: a read under a live TUI re-establishes observation", () =>
    Effect.gen(function* () {
      const dbFile = join(scratch, "restart.db")
      // "Before the restart": open a thread's terminal and persist identity.
      const state = yield* openedThreadState("Restart").pipe(
        Effect.provide(apiTestLayer(makeTestServiceLayers(dbFile))),
      )

      // "After the restart": a fresh process (fresh in-memory adapters) over
      // the same database, with the zmx session still alive (seeded — real
      // zmx sessions survive an ATC restart). No poller runs: the first
      // READ re-links, re-observes, and status flows again.
      const after = makeTestServiceLayers(dbFile)
      after.fake.seed(sessionNameForTerminalId(state.terminalId))
      yield* Effect.gen(function* () {
        const client = yield* HttpApiTest.groups(Api, ["v1"])
        const read = yield* client.v1.getThread({ params: { threadId: state.threadId } })
        assert.strictEqual(read.linkedTerminalId, state.terminalId)
        assert.strictEqual(read.activityState, "unknown")
        after.fakeAgents.codex.emitActivity(state.sessionId, "working")
        yield* eventually(
          client.v1.getThread({ params: { threadId: state.threadId } }),
          (t) => t.activityState === "working",
        )
        yield* client.v1.deleteThread({ params: { threadId: state.threadId } })
      }).pipe(Effect.provide(apiTestLayer(after)))
    }),
  )

  it.live("restart mid-first-turn: an observed busy confirms durably, the next open resumes", () =>
    Effect.gen(function* () {
      const dbFile = join(scratch, "restart-mid-turn.db")
      // "Before the restart": the first turn starts; ATC goes down before
      // its idle ever arrives on the feed.
      const before = makeTestServiceLayers(dbFile)
      const state = yield* Effect.gen(function* () {
        const repository = yield* ThreadRepository
        const opened = yield* openedThreadState("RestartMidTurn")
        before.fakeAgents.codex.emitActivity(opened.sessionId, "working")
        yield* eventually(
          repository.get(opened.threadId),
          (r) => Option.isSome(r) && r.value.confirmedAt !== undefined,
        )
        return opened
      }).pipe(Effect.provide(apiTestLayer(before)))

      // "After the restart": the TUI ended during the downtime, and the
      // completed turn's idle was never observed by any process. The session
      // has history (a submitted prompt), so the next open must resume the
      // exact id, never re-materialize.
      const after = makeTestServiceLayers(dbFile)
      yield* Effect.gen(function* () {
        const client = yield* HttpApiTest.groups(Api, ["v1"])
        const reopened = yield* client.v1.openThreadTerminal({
          params: { threadId: state.threadId },
        })
        const relaunch = after.fake.sessions.get(sessionNameForTerminalId(reopened.id))
        assert.deepStrictEqual(relaunch?.command, ["fake-agent", "resume", state.sessionId])
        assert.strictEqual(after.fakeAgents.codex.prepared.length, 0)
        yield* client.v1.deleteThread({ params: { threadId: state.threadId } })
      }).pipe(Effect.provide(apiTestLayer(after)))
    }),
  )

  it.live("concurrent first reads establish exactly one observation (no leaked subscription)", () =>
    Effect.gen(function* () {
      const dbFile = join(scratch, "concurrent-reads.db")
      const state = yield* openedThreadState("Concurrent").pipe(
        Effect.provide(apiTestLayer(makeTestServiceLayers(dbFile))),
      )

      // Relaunch: nothing is observed yet, and the sidebar list + detail
      // get race their first reads — exactly one adapter subscription may
      // survive; a second would leak until shutdown and double-drive the
      // thread's activity.
      const after = makeTestServiceLayers(dbFile)
      after.fake.seed(sessionNameForTerminalId(state.terminalId))
      yield* Effect.gen(function* () {
        const client = yield* HttpApiTest.groups(Api, ["v1"])
        yield* Effect.all(
          [
            client.v1.listThreads({ query: {} }),
            client.v1.getThread({ params: { threadId: state.threadId } }),
            client.v1.getThread({ params: { threadId: state.threadId } }),
            client.v1.listThreads({ query: {} }),
          ],
          { concurrency: "unbounded" },
        )
        assert.strictEqual(after.fakeAgents.codex.observerCount(state.sessionId), 1)
        yield* client.v1.deleteThread({ params: { threadId: state.threadId } })
      }).pipe(Effect.provide(apiTestLayer(after)))
    }),
  )

  it.live("a live shared-server observation stays authoritative after its terminal dies", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "working",
      )

      // The TUI terminal dies but the shared-server feed is still live: the
      // busy state is current evidence, so reads must NOT burn a provider
      // reconciliation walk (the scripted "idle" would betray one).
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      kit.fakeAgents.codex.setCheckActivity(sessionId, "idle")
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.isUndefined(read.linkedTerminalId)
      assert.strictEqual(read.activityState, "working")
      assert.notInclude(kit.fakeAgents.codex.checkCalls, sessionId)

      kit.fakeAgents.codex.setCheckActivity(sessionId, null)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a busy state with no live observation re-derives from the adapter on read", () =>
    Effect.gen(function* () {
      const { client, thread } = yield* setup
      const repository = yield* ThreadRepository
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
      kit.fakeAgents.codex.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "working",
      )

      // The driver AND its evidence source die out from under ATC: stale
      // `working` must re-derive from the adapter's reconciliation check,
      // not persist as a lie.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      kit.fakeAgents.codex.endObservations(sessionId)
      kit.fakeAgents.codex.setCheckActivity(sessionId, "idle")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.linkedTerminalId === undefined && t.activityState === "idle",
      )
      // A finish discovered on read stamps the unread overlay too.
      yield* eventually(client.v1.getThread({ params: { threadId: thread.id } }), (t) => t.unread)

      kit.fakeAgents.codex.setCheckActivity(sessionId, null)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a hooks-shaped observation never shields a busy state from re-derivation", () =>
    Effect.gen(function* () {
      const { client, project } = yield* setup
      const repository = yield* ThreadRepository
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "claude-code" },
      })
      const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
      const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
      kit.fakeAgents.claude.emitActivity(sessionId, "working")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (t) => t.activityState === "working",
      )

      // The hooks feed dies silently with the TUI — its subscription is
      // still registered, but it is no evidence. A busy snapshot must
      // re-derive through checkSession, never ride the dead feed.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      kit.fakeAgents.claude.setCheckActivity(sessionId, "idle")
      const read = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.strictEqual(read.activityState, "idle")
      assert.include(kit.fakeAgents.claude.checkCalls, sessionId)

      kit.fakeAgents.claude.setCheckActivity(sessionId, null)
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a page of stale-busy threads fans out bounded, never serialized (dead provider)", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const repository = yield* ThreadRepository
      const fake = kit.fakeAgents.codex
      const project = yield* client.v1.createProject({
        payload: { name: "FanOut", defaultWorkingDirectory: realDir },
      })

      // Ten threads, each left stale-busy with no live observation: the
      // shape a dead provider leaves behind, where every read must consult
      // checkSession (kept scripted busy so each listing re-derives).
      const sessionIds: Array<string> = []
      for (let i = 0; i < 10; i++) {
        const thread = yield* client.v1.createThread({
          payload: { projectId: project.id, agentId: "codex" },
        })
        const terminal = yield* client.v1.openThreadTerminal({ params: { threadId: thread.id } })
        const sessionId =
          Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
        sessionIds.push(sessionId)
        fake.emitActivity(sessionId, "working")
        yield* eventually(
          client.v1.getThread({ params: { threadId: thread.id } }),
          (t) => t.activityState === "working",
        )
        kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
        fake.endObservations(sessionId)
        fake.setCheckActivity(sessionId, "working")
        // Settle the observation claim release: once the read consults
        // checkSession, re-derivation (not the dead feed) owns this thread.
        yield* eventually(
          client.v1
            .getThread({ params: { threadId: thread.id } })
            .pipe(Effect.map(() => fake.checkCalls.includes(sessionId))),
          (consulted) => consulted,
        )
      }

      // One listing over the page: every record re-derives concurrently,
      // capped at 8 in flight — neither serialized (minutes against a dead
      // provider) nor a stampede.
      fake.resetCheckStats()
      const before = fake.checkCalls.length
      fake.setCheckHangs(true)
      const listing = yield* Effect.forkChild(
        client.v1.listThreads({ query: { projectId: project.id } }),
      )
      yield* eventually(
        Effect.sync(() => fake.maxConcurrentChecks()),
        (max) => max === 8,
      )
      fake.setCheckHangs(false)
      const listed = yield* Fiber.join(listing)
      assert.strictEqual(listed.length, 10)
      assert.isTrue(listed.every((t) => t.activityState === "working"))
      assert.strictEqual(fake.checkCalls.length - before, 10)
      assert.strictEqual(fake.maxConcurrentChecks(), 8)

      for (const sessionId of sessionIds) fake.setCheckActivity(sessionId, null)
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )
})
