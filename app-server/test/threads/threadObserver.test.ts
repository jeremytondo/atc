import { assert, describe, it } from "@effect/vitest"
import { Effect, Option } from "effect"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { ThreadRuntime } from "../../src/threads/threadRuntime.ts"
import { Threads } from "../../src/threads/threads.ts"
import { Projects } from "../../src/projects/projects.ts"
import { eventually, makeTestServiceLayers } from "../testLayers.ts"

// The Thread observer (ATC-226) through the fake adapters and the uniform
// seam only: a tui thread's terminal feed drives the ledger and the
// transcript copy (re-read at its idle), closing the terminal ends it now
// when idle and at its next idle when busy — after that turn's re-read —
// and showing it again calls the close off. Nothing here is ever driven by
// the runtime, and nothing relaunches a terminal by itself.

const scratch = mkdtempSync(join(tmpdir(), "atc-observer-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const kit = makeTestServiceLayers()
const fake = kit.fakeAgents.codex

const terminalAlive = (terminalId: string): boolean =>
  kit.fake.sessions.has(sessionNameForTerminalId(terminalId))

const waitFor = <A>(read: Effect.Effect<A>, predicate: (value: A) => boolean) =>
  eventually(read, predicate, { attempts: 400, interval: "10 millis" })

/** A tui thread with its terminal open and its session observed. */
const openedThread = Effect.gen(function* () {
  const projects = yield* Projects
  const threads = yield* Threads
  const repository = yield* ThreadRepository
  const project = yield* projects.create({ name: "Observer", defaultWorkingDirectory: realDir })
  const thread = yield* threads.create({ projectId: project.id, agentId: "codex", kind: "tui" })
  const terminal = yield* threads.openTerminal(thread.id)
  const sessionId = Option.getOrThrow(yield* repository.get(thread.id)).providerSessionId ?? ""
  return { threadId: thread.id, terminal, sessionId }
})

describe("ThreadObserver", () => {
  it.live("the observed idle re-reads the copy; nothing relaunches an ended terminal", () =>
    Effect.gen(function* () {
      const threads = yield* Threads
      const runtime = yield* ThreadRuntime
      const { threadId, terminal, sessionId } = yield* openedThread
      assert.strictEqual(fake.observerCount(sessionId), 1)

      fake.setHistory(sessionId, [
        {
          turn: { id: "tui-turn", status: "completed" },
          items: [{ type: "userMessage", id: "t1", turnId: "tui-turn", text: "typed" }],
        },
      ])
      fake.emitActivity(sessionId, "working")
      fake.emitActivity(sessionId, "idle")
      const page = yield* waitFor(
        runtime.transcript(threadId).pipe(Effect.orDie),
        (current) => current.snapshotVersion === 1,
      )
      assert.deepStrictEqual(
        page.items.map((item) => item.id),
        ["t1"],
      )
      const read = yield* threads.get(threadId)
      assert.strictEqual(read.activityState, "idle")
      assert.isTrue(read.unread)

      // The TUI's own items land in the copy as they happen.
      fake.emitObservedItem(sessionId, "itemCompleted", {
        type: "assistantText",
        id: "o1",
        turnId: "tui-turn",
        text: "seen live",
        complete: true,
      })
      yield* waitFor(runtime.transcript(threadId).pipe(Effect.orDie), (current) =>
        current.items.some((item) => item.id === "o1"),
      )

      // The TUI exits on its own: the thread unlinks, and stays unlinked.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.linkedTerminalId === undefined,
      )
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("closing ends an idle terminal now, a busy one after its idle's re-read", () =>
    Effect.gen(function* () {
      const threads = yield* Threads
      const runtime = yield* ThreadRuntime
      const { threadId, terminal } = yield* openedThread

      const closed = yield* threads.closeTerminal(threadId)
      assert.isUndefined(closed.linkedTerminalId)
      assert.isFalse(terminalAlive(terminal.id))

      // Reopened (an unconfirmed session re-materializes: a fresh identity),
      // then closed while busy: it stays until its idle.
      const reopened = yield* threads.openTerminal(threadId)
      const repository = yield* ThreadRepository
      const session = Option.getOrThrow(yield* repository.get(threadId)).providerSessionId ?? ""
      fake.emitActivity(session, "working")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "working",
      )
      const stillOpen = yield* threads.closeTerminal(threadId)
      assert.strictEqual(stillOpen.linkedTerminalId, reopened.id)
      assert.isTrue(terminalAlive(reopened.id))
      fake.setHistory(session, [
        {
          turn: { id: "last-turn", status: "completed" },
          items: [{ type: "userMessage", id: "l1", turnId: "last-turn", text: "last" }],
        },
      ])
      fake.emitActivity(session, "idle")
      yield* waitFor(
        Effect.sync(() => terminalAlive(reopened.id)),
        (alive) => !alive,
      )
      // Its last turn landed in the copy before the kill.
      const page = yield* runtime.transcript(threadId)
      assert.deepStrictEqual(
        page.items.map((item) => item.id),
        ["l1"],
      )
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a close left pending when the terminal died never ends the next one", () =>
    Effect.gen(function* () {
      const threads = yield* Threads
      const repository = yield* ThreadRepository
      const { threadId, terminal, sessionId } = yield* openedThread
      fake.emitActivity(sessionId, "working")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "working",
      )
      yield* threads.closeTerminal(threadId)
      // The busy TUI dies before its idle; the user opens a new one.
      kit.fake.sessions.delete(sessionNameForTerminalId(terminal.id))
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.linkedTerminalId === undefined,
      )
      const next = yield* threads.openTerminal(threadId)
      const session = Option.getOrThrow(yield* repository.get(threadId)).providerSessionId ?? ""
      fake.emitActivity(session, "working")
      fake.emitActivity(session, "idle")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "idle",
      )
      assert.isTrue(terminalAlive(next.id))
      yield* threads.closeTerminal(threadId)
      assert.isFalse(terminalAlive(next.id))
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("showing the terminal again before the idle calls a pending close off", () =>
    Effect.gen(function* () {
      const threads = yield* Threads
      const { threadId, terminal, sessionId } = yield* openedThread
      fake.emitActivity(sessionId, "working")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "working",
      )
      yield* threads.closeTerminal(threadId)
      const shownAgain = yield* threads.openTerminal(threadId)
      assert.strictEqual(shownAgain.id, terminal.id)
      fake.emitActivity(sessionId, "idle")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "idle",
      )
      assert.isTrue(terminalAlive(terminal.id))
      yield* threads.closeTerminal(threadId)
      assert.isFalse(terminalAlive(terminal.id))
    }).pipe(Effect.provide(kit.layer)),
  )
})
