import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
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

// One live process per thread (ATC-203), through the fake adapters and the
// uniform seam only: the runtime hands a one-process provider's session
// between the TUI (a fake terminal) and its own turns — native wins, the
// TUI ends after its last turn is on disk and comes back when the queue
// drains, closing hands back, and a shared-server provider is untouched.

const scratch = mkdtempSync(join(tmpdir(), "atc-tui-ownership-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const kit = makeTestServiceLayers()
const claude = kit.fakeAgents.claude
const codex = kit.fakeAgents.codex

let sessionCounter = 0

/** A confirmed thread on `agentId` whose provider session the fake knows,
 * with its TUI open: the state every hand-off starts from. */
const openedThread = (agentId: "claude-code" | "codex") =>
  Effect.gen(function* () {
    const projects = yield* Projects
    const threads = yield* Threads
    const repository = yield* ThreadRepository
    const project = yield* projects.create({ name: "Ownership", defaultWorkingDirectory: realDir })
    const thread = yield* threads.create({ projectId: project.id, agentId })
    const sessionId = `${agentId}-session-${++sessionCounter}`
    const fake = agentId === "claude-code" ? claude : codex
    fake.seed(sessionId, realDir)
    yield* repository.setProviderSession(thread.id, sessionId, null)
    yield* repository.confirm(thread.id)
    const terminal = yield* threads.openTerminal(thread.id)
    return { threadId: thread.id, sessionId, terminal, fake }
  })

const terminalAlive = (terminalId: string): boolean =>
  kit.fake.sessions.has(sessionNameForTerminalId(terminalId))

const waitFor = <A>(read: Effect.Effect<A>, predicate: (value: A) => boolean) =>
  eventually(read, predicate, { attempts: 400, interval: "10 millis" })

describe("Thread TUI ownership", () => {
  it.live("a native prompt takes an idle one-process TUI over, then relaunches it", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId, terminal, fake } = yield* openedThread("claude-code")
      assert.isTrue(terminalAlive(terminal.id))
      const readsBefore = fake.historyReads.length

      const started = yield* runtime.prompt(threadId, "native wins")
      // The turn started at once: the TUI ended first — after a settled
      // history read (its last turn on disk) — and the session resumed
      // natively with the prompt.
      assert.isString(started.turnId)
      assert.isFalse(terminalAlive(terminal.id))
      assert.strictEqual(fake.historyReads.slice(readsBefore)[0], sessionId)
      assert.deepStrictEqual(fake.sessions.get(sessionId)?.inputs, ["native wins"])
      const mid = yield* threads.get(threadId).pipe(Effect.orDie)
      assert.isUndefined(mid.linkedTerminalId)

      // The run ends with nothing queued: the TUI (still shown) comes back
      // in a fresh terminal on the same session.
      fake.completeTurn(sessionId, "completed")
      const after = yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.linkedTerminalId !== undefined,
      )
      assert.notStrictEqual(after.linkedTerminalId, terminal.id)
      assert.isTrue(terminalAlive(after.linkedTerminalId ?? ""))
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a TUI whose last turn cannot be read stays alive: the take-over fails closed", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const { threadId, sessionId, terminal, fake } = yield* openedThread("claude-code")
      fake.setUnavailable("transcript reads are down")
      const refused = yield* runtime.prompt(threadId, "not yet").pipe(Effect.flip)
      assert.strictEqual(refused._tag, "ProviderUnavailable")
      assert.isTrue(terminalAlive(terminal.id))
      // Un-admitted, not queued: the caller's error means "not accepted".
      assert.deepStrictEqual(yield* runtime.listQueue(threadId), [])
      fake.setUnavailable(null)
      const started = yield* runtime.prompt(threadId, "now")
      assert.isString(started.turnId)
      assert.isFalse(terminalAlive(terminal.id))
      fake.completeTurn(sessionId, "completed")
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a prompt while the TUI is busy queues, and drains at its idle by taking over", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId, terminal, fake } = yield* openedThread("claude-code")

      fake.emitActivity(sessionId, "working")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "working",
      )
      const queued = yield* runtime.prompt(threadId, "after the tui")
      assert.isUndefined(queued.turnId)
      assert.isTrue(terminalAlive(terminal.id))
      assert.deepStrictEqual(fake.sessions.get(sessionId)?.inputs, [])

      // The TUI's turn ends: re-read, then the queued prompt takes over.
      fake.emitActivity(sessionId, "idle")
      yield* waitFor(
        Effect.sync(() => fake.sessions.get(sessionId)?.inputs ?? []),
        (inputs) => inputs.includes("after the tui"),
      )
      assert.isFalse(terminalAlive(terminal.id))
      fake.completeTurn(sessionId, "completed")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.linkedTerminalId !== undefined,
      )
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("closing the TUI hands back at once when idle, at its idle when busy", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId, terminal, fake } = yield* openedThread("claude-code")

      const closed = yield* threads.closeTerminal(threadId)
      assert.isUndefined(closed.linkedTerminalId)
      assert.isFalse(terminalAlive(terminal.id))

      // Reopen, then close while busy: the TUI stays until its idle.
      const reopened = yield* threads.openTerminal(threadId)
      fake.emitActivity(sessionId, "working")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "working",
      )
      const stillOpen = yield* threads.closeTerminal(threadId)
      assert.strictEqual(stillOpen.linkedTerminalId, reopened.id)
      // Showing the TUI again before the idle calls the hand-off off: the
      // idle passes and the TUI stays.
      const shownAgain = yield* threads.openTerminal(threadId)
      assert.strictEqual(shownAgain.id, reopened.id)
      fake.emitActivity(sessionId, "idle")
      yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.activityState === "idle",
      )
      assert.isTrue(terminalAlive(reopened.id))
      // Closed again while idle: gone now.
      yield* threads.closeTerminal(threadId)
      assert.isFalse(terminalAlive(reopened.id))

      // Closed means not shown: a native turn ends with no relaunch.
      const started = yield* runtime.prompt(threadId, "chat only")
      assert.isString(started.turnId)
      fake.completeTurn(sessionId, "completed")
      yield* waitFor(
        runtime.transcript(threadId).pipe(Effect.orDie),
        (transcript) => transcript.turns.at(-1)?.status === "completed",
      )
      const after = yield* threads.get(threadId).pipe(Effect.orDie)
      assert.isUndefined(after.linkedTerminalId)
      // The native side keeps its connection: the TUI is not wanted, so
      // nothing takes the session over (ATC-207).
      assert.isTrue(yield* runtime.hasWriter(threadId))
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a TUI open during a native turn is refused and happens at the run's end", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId, fake } = yield* openedThread("claude-code")
      yield* threads.closeTerminal(threadId)

      const started = yield* runtime.prompt(threadId, "busy natively")
      assert.isString(started.turnId)
      const refused = yield* threads.openTerminal(threadId).pipe(Effect.flip)
      assert.strictEqual(refused._tag, "ThreadBusy")

      fake.completeTurn(sessionId, "completed")
      const after = yield* waitFor(
        threads.get(threadId).pipe(Effect.orDie),
        (current) => current.linkedTerminalId !== undefined,
      )
      assert.isTrue(terminalAlive(after.linkedTerminalId ?? ""))
    }).pipe(Effect.provide(kit.layer)),
  )

  it.live("a shared-server provider's TUI and native turns coexist untouched", () =>
    Effect.gen(function* () {
      const runtime = yield* ThreadRuntime
      const threads = yield* Threads
      const { threadId, sessionId, terminal, fake } = yield* openedThread("codex")

      const started = yield* runtime.prompt(threadId, "alongside the tui")
      assert.isString(started.turnId)
      assert.isTrue(terminalAlive(terminal.id))
      fake.completeTurn(sessionId, "completed")
      yield* waitFor(runtime.hasWriter(threadId), (driving) => !driving)
      assert.isTrue(terminalAlive(terminal.id))
      const closed = yield* threads.closeTerminal(threadId)
      assert.strictEqual(closed.linkedTerminalId, terminal.id)
    }).pipe(Effect.provide(kit.layer)),
  )
})
