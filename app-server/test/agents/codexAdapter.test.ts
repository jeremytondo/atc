import { assert, describe, it } from "@effect/vitest"
import { Effect, Stream } from "effect"
import * as fs from "node:fs"
import * as path from "node:path"
import type { AgentSessionEvent } from "../../src/agents/agentAdapter.ts"
import * as CodexAdapter from "../../src/agents/codexAdapter.ts"
import * as CodexServer from "../../src/agents/codexServer.ts"
import { eventually } from "../testLayers.ts"
import {
  type CodexSandbox,
  codexAdapterLayer,
  collectAgentEvents,
  makeCodexSandbox,
  openExternal,
  waitForAgentEvent,
  TEST_SETTINGS,
} from "./agentTestKit.ts"

// Codex adapter tests against the fake app-server fixture, through the real
// supervision module (ensure → detached fixture on the sandbox's well-known
// unix socket → WebSocket → adapter). All it.live: real processes, sockets,
// and clock. Each test block ends with CodexServer.stop() via `withAdapter`
// so no detached fixture leaks.

/** Run `use` with the adapter, then always stop the detached server. */
const withAdapter = (
  sandbox: CodexSandbox,
  use: (adapter: CodexAdapter.CodexAdapter["Service"]) => Effect.Effect<void, unknown, never>,
) =>
  Effect.gen(function* () {
    const adapter = yield* CodexAdapter.CodexAdapter
    const codexServer = yield* CodexServer.CodexServer
    yield* Effect.ensuring(use(adapter), Effect.orDie(codexServer.stop()))
  }).pipe(Effect.provide(codexAdapterLayer(sandbox)))

describe("CodexAdapter", () => {
  it.live(
    "create: verified identity, truthful feed, first turn completes",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "hello", attachments: [] },
                settings: TEST_SETTINGS,
              })
              assert.isString(connection.providerSessionId)
              assert.strictEqual(connection.cwd, sandbox.cwd)
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId === turn.turnId &&
                  event.outcome === "completed",
              )
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "activity" && event.activity === "idle",
              )
              assert.strictEqual(yield* connection.activity, "idle")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "a turn's images go out as localImage paths and map back onto the prompt's item",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        const shot = {
          id: "att-1",
          name: "shot.png",
          mediaType: "image/png" as const,
          byteSize: 11,
          path: path.join(sandbox.cwd, "shot.png"),
          createdAt: "2026-08-20T00:00:00.000Z",
        }
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "", attachments: [shot] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              // The fixture echoes the input back as the real server does —
              // a localImage block — and the adapter names our attachment.
              const message = sink.find(
                (event) => event.type === "itemCompleted" && event.item.type === "userMessage",
              )
              assert.isTrue(
                message?.type === "itemCompleted" && message.item.type === "userMessage",
              )
              if (message?.type === "itemCompleted" && message.item.type === "userMessage") {
                assert.strictEqual(message.item.text, "")
                assert.deepStrictEqual(message.item.attachments, [shot])
              }
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "resume: exact identity round trip; unknown id fails closed",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const threadId = yield* Effect.scoped(
              Effect.gen(function* () {
                const { connection, turn } = yield* adapter.createSession({
                  cwd: sandbox.cwd,
                  input: { text: "seed", attachments: [] },
                  settings: TEST_SETTINGS,
                })
                const sink = yield* collectAgentEvents(connection.events)
                yield* waitForAgentEvent(
                  sink,
                  (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
                )
                return connection.providerSessionId
              }),
            )

            yield* Effect.scoped(
              Effect.gen(function* () {
                const resumed = yield* adapter.resumeSession({
                  providerSessionId: threadId,
                  cwd: sandbox.cwd,
                  settings: TEST_SETTINGS,
                })
                assert.strictEqual(resumed.providerSessionId, threadId)
              }),
            )

            const failure = yield* Effect.scoped(
              Effect.flip(
                adapter.resumeSession({
                  providerSessionId: "00000000-0000-7000-8000-000000000000",
                  cwd: sandbox.cwd,
                  settings: TEST_SETTINGS,
                }),
              ),
            )
            assert.strictEqual(failure._tag, "AgentResumeFailed")
          }),
        )
      }),
    30_000,
  )

  it.live(
    "create with a lying cwd is an identity mismatch, never adopted",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_WRONG_CWD: "start" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const failure = yield* Effect.scoped(
              Effect.flip(
                adapter.createSession({
                  cwd: sandbox.cwd,
                  input: { text: "hello", attachments: [] },
                  settings: TEST_SETTINGS,
                }),
              ),
            )
            assert.strictEqual(failure._tag, "AgentIdentityMismatch")
            if (failure._tag === "AgentIdentityMismatch") {
              assert.strictEqual(failure.field, "cwd")
            }
          }),
        )
      }),
    30_000,
  )

  it.live(
    "resume with a lying cwd is an identity mismatch, never adopted",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_WRONG_CWD: "resume" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const threadId = yield* Effect.scoped(
              Effect.gen(function* () {
                const { connection, turn } = yield* adapter.createSession({
                  cwd: sandbox.cwd,
                  input: { text: "seed", attachments: [] },
                  settings: TEST_SETTINGS,
                })
                const sink = yield* collectAgentEvents(connection.events)
                yield* waitForAgentEvent(
                  sink,
                  (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
                )
                return connection.providerSessionId
              }),
            )
            const failure = yield* Effect.scoped(
              Effect.flip(
                adapter.resumeSession({
                  providerSessionId: threadId,
                  cwd: sandbox.cwd,
                  settings: TEST_SETTINGS,
                }),
              ),
            )
            assert.strictEqual(failure._tag, "AgentIdentityMismatch")
            if (failure._tag === "AgentIdentityMismatch") {
              assert.strictEqual(failure.field, "cwd")
            }
          }),
        )
      }),
    30_000,
  )

  it.live(
    "single writer per thread: a concurrent second connection conflicts",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "HANG on", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const failure = yield* Effect.flip(
                adapter.resumeSession({
                  providerSessionId: connection.providerSessionId,
                  cwd: sandbox.cwd,
                  settings: TEST_SETTINGS,
                }),
              )
              assert.strictEqual(failure._tag, "AgentConflict")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "interrupt: exact turn only, truthful interrupted outcome",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "HANG until interrupted", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "activity" && event.activity === "working",
              )
              // A stale/foreign target is refused before anything is sent.
              const stale = yield* Effect.flip(connection.interrupt({ turnId: "not-a-turn" }))
              assert.strictEqual(stale._tag, "AgentConflict")

              yield* connection.interrupt(turn)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId === turn.turnId &&
                  event.outcome === "interrupted",
              )
              assert.strictEqual(yield* connection.activity, "idle")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "provider approval requests park on the feed until answered on the JSON-RPC request",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "needs APPROVAL for this", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(sink, (event) => event.type === "requestOpened")
              const opened = sink.find((event) => event.type === "requestOpened")
              if (opened?.type !== "requestOpened" || opened.request.kind !== "approval") {
                return assert.fail("expected an approval request")
              }
              // The command, cwd, and reason ride the request; it names the
              // pending commandExecution item, which flips to pending.
              assert.deepStrictEqual(opened.request.subject, {
                type: "command",
                command: "/bin/sh -c pwd",
                cwd: sandbox.cwd,
              })
              assert.strictEqual(opened.request.reason, "needs network")
              assert.strictEqual(opened.request.turnId, turn.turnId)
              const itemId = opened.request.itemId
              assert.isString(itemId)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "itemUpdated" &&
                  event.item.id === itemId &&
                  event.item.type === "command" &&
                  event.item.status === "pending",
              )
              // needs_input was truthfully reported while the request parks.
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "activity" && event.activity === "needs_input",
              )
              assert.isFalse(sink.some((event) => event.type === "turnCompleted"))
              yield* connection.respond(opened.request.id, { kind: "approval", decision: "accept" })
              yield* waitForAgentEvent(sink, (event) => event.type === "requestClosed")
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              const completed = sink.find(
                (event) => event.type === "itemCompleted" && event.item.id === itemId,
              )
              assert.isTrue(
                completed?.type === "itemCompleted" &&
                  completed.item.type === "command" &&
                  completed.item.status === "completed" &&
                  completed.item.exitCode === 0,
              )
              const again = yield* Effect.flip(
                connection.respond(opened.request.id, { kind: "approval", decision: "accept" }),
              )
              assert.strictEqual(again._tag, "AgentConflict")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "a declined approval ends the item declined; questions answer by id",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "needs APPROVAL for this", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(sink, (event) => event.type === "requestOpened")
              const opened = sink.find((event) => event.type === "requestOpened")
              if (opened?.type !== "requestOpened") return assert.fail("expected a request")
              yield* connection.respond(opened.request.id, {
                kind: "approval",
                decision: "decline",
              })
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              const declined = sink.find(
                (event) =>
                  event.type === "itemCompleted" &&
                  event.item.id === opened.request.itemId &&
                  event.item.type === "command" &&
                  event.item.status === "error" &&
                  event.item.error === "declined",
              )
              assert.isDefined(declined)

              // A question round trip: answers keyed by the question id reach
              // the provider (the fixture echoes them back as its reply).
              const asked = yield* connection.startTurn(
                { text: "QUESTION time", attachments: [] },
                TEST_SETTINGS,
              )
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "requestOpened" && event.request.kind === "question",
              )
              const question = sink.findLast((event) => event.type === "requestOpened")
              if (question?.type !== "requestOpened" || question.request.kind !== "question") {
                return assert.fail("expected a question request")
              }
              assert.deepStrictEqual(
                question.request.questions.map((entry) => [entry.id, entry.freeform]),
                [["color", false]],
              )
              yield* connection.respond(question.request.id, {
                kind: "question",
                answers: { color: ["blue"] },
              })
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === asked.turnId,
              )
              const echo = sink.find(
                (event) =>
                  event.type === "itemCompleted" &&
                  event.item.type === "assistantText" &&
                  event.item.text.startsWith("answers:"),
              )
              assert.isTrue(
                echo?.type === "itemCompleted" &&
                  echo.item.type === "assistantText" &&
                  echo.item.text === 'answers: {"color":{"answers":["blue"]}}',
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "TUI-driven turns on the shared server appear on our feed",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "seed", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )

              // A second client of the same shared server (the TUI stand-in)
              // drives a turn on the same thread.
              const external = yield* openExternal(sandbox.socketPath)
              external.send({
                id: 1,
                method: "turn/start",
                params: {
                  threadId: connection.providerSessionId,
                  input: [{ type: "text", text: "external turn" }],
                },
              })
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId !== turn.turnId &&
                  event.outcome === "completed",
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "tuiLaunch probes session existence, then hands back the exact remote-resume argv",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            // A session the provider actually has: the probe passes.
            const sessionId = yield* Effect.scoped(
              Effect.map(
                adapter.createSession({
                  cwd: sandbox.cwd,
                  input: { text: "seed", attachments: [] },
                  settings: TEST_SETTINGS,
                }),
                ({ connection }) => connection.providerSessionId,
              ),
            )
            const { launchSpec } = yield* adapter.tuiLaunch({
              providerSessionId: sessionId,
              cwd: sandbox.cwd,
              providerMetadata: undefined,
            })
            assert.strictEqual(launchSpec.command[0], sandbox.wrapper)
            assert.deepStrictEqual(launchSpec.command.slice(1), [
              "resume",
              "--remote",
              `unix://${sandbox.socketPath}`,
              sessionId,
            ])

            // A session codex no longer has (pruned/deleted history) fails
            // the probe closed instead of launching a TUI that dies in the
            // pty with no typed error.
            const missing = yield* Effect.flip(
              adapter.tuiLaunch({
                providerSessionId: "some-pruned-thread",
                cwd: sandbox.cwd,
                providerMetadata: undefined,
              }),
            )
            assert.strictEqual(missing._tag, "AgentResumeFailed")
          }),
        )
      }),
    30_000,
  )

  it.live(
    "agent text streams: itemStarted, deltas, itemCompleted — after the user's message item",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "STREAM me", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              const items = sink.flatMap((event) =>
                event.type === "itemStarted" ||
                event.type === "itemCompleted" ||
                event.type === "textDelta"
                  ? [event]
                  : [],
              )
              const first = items[0]
              assert.isTrue(
                first?.type === "itemCompleted" &&
                  first.item.type === "userMessage" &&
                  first.item.text === "STREAM me" &&
                  first.item.turnId === turn.turnId,
              )
              const started = items[1]
              if (started?.type !== "itemStarted" || started.item.type !== "assistantText") {
                return assert.fail("expected the streamed agent message to start")
              }
              assert.strictEqual(started.item.complete, false)
              assert.deepStrictEqual(
                items.slice(2, 4).map((event) => (event.type === "textDelta" ? event : null)),
                [
                  { type: "textDelta", itemId: started.item.id, delta: "fake: " },
                  { type: "textDelta", itemId: started.item.id, delta: "STREAM me" },
                ],
              )
              const completed = items[4]
              assert.isTrue(
                completed?.type === "itemCompleted" &&
                  completed.item.type === "assistantText" &&
                  completed.item.id === started.item.id &&
                  completed.item.complete &&
                  completed.item.text === "fake: STREAM me",
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "readHistory returns thread/resume's turns and items; an unknown id fails closed",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const threadId = yield* Effect.scoped(
              Effect.gen(function* () {
                const { connection, turn } = yield* adapter.createSession({
                  cwd: sandbox.cwd,
                  input: { text: "run a COMMAND", attachments: [] },
                  settings: TEST_SETTINGS,
                })
                const sink = yield* collectAgentEvents(connection.events)
                yield* waitForAgentEvent(
                  sink,
                  (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
                )
                return connection.providerSessionId
              }),
            )
            const history = yield* adapter.readHistory({
              providerSessionId: threadId,
              cwd: sandbox.cwd,
            })
            assert.strictEqual(history.length, 1)
            const [entry] = history
            assert.strictEqual(entry?.turn.status, "completed")
            assert.isString(entry?.turn.startedAt)
            assert.isString(entry?.turn.endedAt)
            assert.deepStrictEqual(
              entry?.items.map((item) => [item.type, item.turnId === entry.turn.id]),
              [
                ["userMessage", true],
                ["command", true],
                ["assistantText", true],
              ],
            )
            const command = entry?.items[1]
            assert.isTrue(
              command?.type === "command" &&
                command.status === "completed" &&
                command.exitCode === 0,
            )
            const failure = yield* Effect.flip(
              adapter.readHistory({
                providerSessionId: "00000000-0000-7000-8000-000000000000",
                cwd: sandbox.cwd,
              }),
            )
            assert.strictEqual(failure._tag, "AgentResumeFailed")
          }),
        )
      }),
    30_000,
  )

  it.live(
    "an older installed codex warns but still works",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_VERSION: "0.1.0" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "hello", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
            }),
          ),
        )
      }),
    30_000,
  )
})

// ATC-140: fresh-launch identity capture, passive observation, and the
// thread/list reconciliation check — the TUI stand-in is a second WebSocket
// client of the same shared fake app-server.
describe("CodexAdapter TUI session plumbing", () => {
  const waitForActivity = (sink: Array<string>, wanted: string) =>
    eventually(
      Effect.sync(() => sink),
      (entries) => entries.includes(wanted),
      { attempts: 200, interval: "25 millis" },
    )

  it.live(
    "prepareTuiSession captures the fresh TUI's thread/started identity",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const prepared = yield* adapter.prepareTuiSession({ cwd: sandbox.cwd })
              assert.strictEqual(prepared.launchSpec.command[0], sandbox.wrapper)
              // --cd pins the thread's cwd server-side: the remote TUI does
              // not forward its own working directory (codex 0.146.0).
              assert.deepStrictEqual(prepared.launchSpec.command.slice(1, 3), ["--cd", sandbox.cwd])
              // The TUI is pointed at the shared server's well-known socket
              // explicitly — never left to its own auto-join.
              assert.deepStrictEqual(prepared.launchSpec.command.slice(3), [
                "--remote",
                `unix://${sandbox.socketPath}`,
              ])

              // The launched TUI stand-in bootstraps a thread in the cwd.
              const external = yield* openExternal(sandbox.socketPath)
              const threadId = yield* external.startThread(1, sandbox.cwd)
              const identity = yield* prepared.identity
              assert.strictEqual(identity.providerSessionId, threadId)
              assert.strictEqual(identity.cwd, sandbox.cwd)
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "observers of a TUI-driven thread receive its conversation items",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              // Any passive call brings the shared server up for the stand-in.
              yield* adapter.checkSession({ providerSessionId: "warm-up" })
              const external = yield* openExternal(sandbox.socketPath)
              const threadId = yield* external.startThread(1, sandbox.cwd)
              const stream = yield* adapter.observeSession({
                providerSessionId: threadId,
                providerMetadata: undefined,
              })
              const sink: Array<AgentSessionEvent> = []
              yield* stream.pipe(
                Stream.runForEach((event) => Effect.sync(() => sink.push(event))),
                Effect.forkScoped,
              )
              // The TUI stand-in drives a turn; the fan-out carries its items.
              yield* external.request(2, "turn/start", {
                threadId,
                input: [{ type: "text", text: "from the tui" }],
              })
              yield* eventually(
                Effect.sync(() => sink),
                (entries) =>
                  entries.some(
                    (event) =>
                      event.type === "itemCompleted" && event.item.type === "assistantText",
                  ),
                { attempts: 200, interval: "25 millis" },
              )
              const items = sink.flatMap((event) =>
                event.type === "itemCompleted" ? [event.item] : [],
              )
              assert.deepStrictEqual(
                items.map((item) => [item.type, item.type === "userMessage" ? item.text : ""]),
                [
                  ["userMessage", "from the tui"],
                  ["assistantText", ""],
                ],
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "capture ignores foreign-cwd threads and adopts the matching one",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        const otherCwd = path.join(sandbox.base, "other")
        fs.mkdirSync(otherCwd, { recursive: true })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const prepared = yield* adapter.prepareTuiSession({ cwd: sandbox.cwd })
              const external = yield* openExternal(sandbox.socketPath)
              // Another client's thread in a different cwd is not ours.
              const foreignId = yield* external.startThread(1, otherCwd)
              const matchingId = yield* external.startThread(2, sandbox.cwd)
              const identity = yield* prepared.identity
              assert.notStrictEqual(identity.providerSessionId, foreignId)
              assert.strictEqual(identity.providerSessionId, matchingId)
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "observeSession streams coarse status for a thread it never joined",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              // Arm the shared connection, then drive a thread externally.
              const activity = yield* adapter.checkSession({ providerSessionId: "none" })
              assert.strictEqual(activity, "unknown")
              const external = yield* openExternal(sandbox.socketPath)
              const threadId = yield* external.startThread(1, sandbox.cwd)

              const stream = yield* adapter.observeSession({
                providerSessionId: threadId,
                providerMetadata: undefined,
              })
              const sink: Array<string> = []
              yield* stream.pipe(
                Stream.runForEach((event) =>
                  Effect.sync(() =>
                    sink.push(
                      event.type === "activity"
                        ? event.activity
                        : event.type === "userPrompt"
                          ? `prompt:${event.text}`
                          : event.type === "settings"
                            ? "settings"
                            : `item:${event.item.type}`,
                    ),
                  ),
                ),
                Effect.ignore,
                Effect.forkScoped,
              )
              yield* external.request(2, "turn/start", {
                threadId,
                input: [{ type: "text", text: "external turn" }],
              })
              yield* waitForActivity(sink, "working")
              yield* waitForActivity(sink, "idle")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "observeSession emits the session's first user prompt exactly once",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              // Arm the shared connection, then drive the thread externally
              // (the TUI stand-in): item/* notifications never reach this
              // passive socket, so the prompt must come from the
              // demand-driven thread/read on the busy transition.
              assert.strictEqual(
                yield* adapter.checkSession({ providerSessionId: "none" }),
                "unknown",
              )
              const external = yield* openExternal(sandbox.socketPath)
              const threadId = yield* external.startThread(1, sandbox.cwd)
              const stream = yield* adapter.observeSession({
                providerSessionId: threadId,
                providerMetadata: undefined,
              })
              const sink: Array<string> = []
              yield* stream.pipe(
                Stream.runForEach((event) =>
                  Effect.sync(() =>
                    sink.push(
                      event.type === "activity"
                        ? event.activity
                        : event.type === "userPrompt"
                          ? `prompt:${event.text}`
                          : event.type === "settings"
                            ? "settings"
                            : `item:${event.item.type}`,
                    ),
                  ),
                ),
                Effect.ignore,
                Effect.forkScoped,
              )
              yield* external.request(2, "turn/start", {
                threadId,
                input: [{ type: "text", text: "please add dark mode" }],
              })
              yield* waitForActivity(sink, "prompt:please add dark mode")
              yield* waitForActivity(sink, "idle")
              // A second turn's busy transition re-reads nothing: the first
              // prompt was already emitted, exactly once.
              yield* external.request(3, "turn/start", {
                threadId,
                input: [{ type: "text", text: "second message" }],
              })
              yield* eventually(
                Effect.sync(() => sink),
                (entries) => entries.filter((entry) => entry === "idle").length >= 2,
                { attempts: 200, interval: "25 millis" },
              )
              assert.deepStrictEqual(
                sink.filter((entry) => entry.startsWith("prompt:")),
                ["prompt:please add dark mode"],
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "generateTitle runs an ephemeral codex exec and returns its last message",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const title = yield* adapter.generateTitle({
              cwd: sandbox.cwd,
              prompt: "please add dark mode",
            })
            // The exec fixture replies with the last stdin line — the
            // verbatim user prompt at the shared template's tail.
            assert.strictEqual(title, "fake title: please add dark mode")
            // The safety-relevant invocation shape is pinned: ephemeral,
            // read-only sandbox, low reasoning, prompt via stdin (never
            // argv), nested-session markers scrubbed by the seam.
            const recorded = JSON.parse(
              fs.readFileSync(path.join(sandbox.base, "exec-record.json"), "utf8"),
            ) as { argv: Array<string>; markers: Array<string> }
            assert.include(recorded.argv, "--ephemeral")
            assert.include(recorded.argv, "--skip-git-repo-check")
            assert.include(recorded.argv.join(" "), "-s read-only")
            assert.include(recorded.argv.join(" "), "--model gpt-5.6-luna")
            assert.include(recorded.argv.join(" "), 'model_reasoning_effort="low"')
            assert.include(recorded.argv, "-")
            assert.isFalse(recorded.argv.some((arg) => arg.includes("dark mode")))
            assert.deepStrictEqual(recorded.markers, [])
            // The temporary output file is cleaned up with the scope.
            const leftovers = fs
              .readdirSync(sandbox.stateDir)
              .filter((entry) => entry.startsWith("codex-title-"))
            assert.deepStrictEqual(leftovers, [])
          }),
        )
      }),
    30_000,
  )

  it.live(
    "the activity feed never waits on the prompt read",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_READ_DELAY_MS: "3000" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              assert.strictEqual(
                yield* adapter.checkSession({ providerSessionId: "none" }),
                "unknown",
              )
              const external = yield* openExternal(sandbox.socketPath)
              const threadId = yield* external.startThread(1, sandbox.cwd)
              const stream = yield* adapter.observeSession({
                providerSessionId: threadId,
                providerMetadata: undefined,
              })
              const sink: Array<string> = []
              yield* stream.pipe(
                Stream.runForEach((event) =>
                  Effect.sync(() =>
                    sink.push(
                      event.type === "activity"
                        ? event.activity
                        : event.type === "userPrompt"
                          ? `prompt:${event.text}`
                          : event.type === "settings"
                            ? "settings"
                            : `item:${event.item.type}`,
                    ),
                  ),
                ),
                Effect.ignore,
                Effect.forkScoped,
              )
              yield* external.request(2, "turn/start", {
                threadId,
                input: [{ type: "text", text: "slow read" }],
              })
              // The whole turn's activity lands while the prompt read is
              // still parked behind the fixture's delay — the feed (and the
              // confirmed-marker write it drives) never waits on the RPC.
              yield* waitForActivity(sink, "working")
              yield* waitForActivity(sink, "idle")
              assert.isTrue(
                sink.every((entry) => !entry.startsWith("prompt:")),
                `prompt arrived before the delayed read resolved: ${sink.join(",")}`,
              )
              // The prompt still arrives once the read completes.
              yield* waitForActivity(sink, "prompt:slow read")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "an unflushed preview is retried in-fiber, landing mid-turn",
    () =>
      Effect.gen(function* () {
        // The real rollout flush lags turn start (probed ~1.4s); the whole
        // turn here finishes before the preview is readable, so only the
        // discovery loop's own backoff — never a later activity
        // transition — can deliver the prompt.
        const sandbox = makeCodexSandbox({ FAKE_CODEX_PREVIEW_DELAY_MS: "1500" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              assert.strictEqual(
                yield* adapter.checkSession({ providerSessionId: "none" }),
                "unknown",
              )
              const external = yield* openExternal(sandbox.socketPath)
              const threadId = yield* external.startThread(1, sandbox.cwd)
              const stream = yield* adapter.observeSession({
                providerSessionId: threadId,
                providerMetadata: undefined,
              })
              const sink: Array<string> = []
              yield* stream.pipe(
                Stream.runForEach((event) =>
                  Effect.sync(() =>
                    sink.push(
                      event.type === "activity"
                        ? event.activity
                        : event.type === "userPrompt"
                          ? `prompt:${event.text}`
                          : event.type === "settings"
                            ? "settings"
                            : `item:${event.item.type}`,
                    ),
                  ),
                ),
                Effect.ignore,
                Effect.forkScoped,
              )
              yield* external.request(2, "turn/start", {
                threadId,
                input: [{ type: "text", text: "late preview" }],
              })
              yield* waitForActivity(sink, "idle")
              yield* waitForActivity(sink, "prompt:late preview")
              // No transition after idle exists to re-arm discovery — the
              // in-fiber retry alone delivered the prompt.
              assert.deepStrictEqual(
                sink.filter((entry) => !entry.startsWith("prompt:") && !entry.startsWith("item:")),
                ["working", "idle"],
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "a failing codex exec is a typed protocol error, never a crash",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_EXEC_EXIT: "3" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const failure = yield* Effect.flip(
              adapter.generateTitle({ cwd: sandbox.cwd, prompt: "doomed" }),
            )
            assert.strictEqual(failure._tag, "AgentProtocolError")
          }),
        )
      }),
    30_000,
  )

  it.live(
    "checkSession walks the provider's paginated thread list",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            // Force the connection + server up before the external client.
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: "missing" }),
              "unknown",
            )
            // The fake serves one thread per page, so the second thread only
            // appears after following nextCursor.
            const laterPage = yield* Effect.scoped(
              Effect.gen(function* () {
                const socket = yield* openExternal(sandbox.socketPath)
                yield* socket.startThread(1, sandbox.cwd)
                return yield* socket.startThread(2, sandbox.cwd)
              }),
            )
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: laterPage }),
              "idle",
            )
            // A miss walks every page before answering `unknown`.
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: "missing" }),
              "unknown",
            )
            // A session archived through another Codex surface still exists:
            // the archived population is walked too, so it is found — and
            // tuiLaunch must not fail it closed as deleted.
            yield* Effect.scoped(
              Effect.gen(function* () {
                const socket = yield* openExternal(sandbox.socketPath)
                yield* socket.request(3, "thread/archive", { threadId: laterPage })
              }),
            )
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: laterPage }),
              "idle",
            )
            const { launchSpec } = yield* adapter.tuiLaunch({
              providerSessionId: laterPage,
              cwd: sandbox.cwd,
              providerMetadata: undefined,
            })
            assert.strictEqual(launchSpec.command[4], laterPage)
          }),
        )
      }),
    30_000,
  )
})

// ATC-158: descendant (subagent) threads fold into their root's aggregate.
// The fixture mirrors the probed real behavior: a subAgentActivity item on
// the parent, child thread/status/changed broadcasts, no thread/started,
// no thread/list entry, and loaded/list + thread/read for reconciliation.
describe("CodexAdapter descendant aggregation", () => {
  const finishChild = (sandbox: { readonly socketPath: string }, childId: string) =>
    Effect.scoped(
      Effect.gen(function* () {
        const socket = yield* openExternal(sandbox.socketPath)
        yield* socket.request(900, "test/child/finish", { threadId: childId })
      }),
    )

  it.live(
    "an idle parent with a working descendant stays working; the last child lands idle",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "SPAWN one worker", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId === turn.turnId &&
                  event.outcome === "completed",
              )
              // The parent's own status went idle with the turn, but the
              // descendant is still active: the aggregate stays working.
              assert.strictEqual(yield* connection.activity, "working")
              yield* finishChild(sandbox, `${connection.providerSessionId}-child-1`)
              // The child finishing flips the aggregate to idle even though
              // the parent was already idle — the last-child transition.
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "activity" && event.activity === "idle",
              )
              assert.strictEqual(yield* connection.activity, "idle")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "a descendant waiting on approval surfaces the aggregate needs_input",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "SPAWN NEEDSINPUT worker", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              assert.strictEqual(yield* connection.activity, "needs_input")
              yield* finishChild(sandbox, `${connection.providerSessionId}-child-1`)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "activity" && event.activity === "idle",
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "observed (TUI-driven) roots aggregate descendant activity too",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              // Force the shared connection (and fixture) up.
              yield* adapter.checkSession({ providerSessionId: "missing" })
              const url = sandbox.socketPath
              const rootId = yield* Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(url)
                  const started = yield* socket.request(1, "thread/start", {
                    cwd: sandbox.cwd,
                  })
                  return (started["thread"] as { id: string }).id
                }),
              )
              const stream = yield* adapter.observeSession({
                providerSessionId: rootId,
                providerMetadata: undefined,
              })
              const sink: Array<string> = []
              yield* stream.pipe(
                Stream.runForEach((event) =>
                  Effect.sync(() =>
                    sink.push(
                      event.type === "activity"
                        ? event.activity
                        : event.type === "userPrompt"
                          ? `prompt:${event.text}`
                          : event.type === "settings"
                            ? "settings"
                            : `item:${event.item.type}`,
                    ),
                  ),
                ),
                Effect.forkScoped,
              )
              // An external writer (TUI stand-in) runs the spawning turn.
              yield* Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(url)
                  yield* socket.request(2, "turn/start", {
                    threadId: rootId,
                    input: [{ type: "text", text: "SPAWN from tui" }],
                  })
                }),
              )
              const waitFor = (wanted: string) =>
                eventually(
                  Effect.sync(() => sink),
                  (entries) => entries.includes(wanted),
                  { attempts: 200, interval: "25 millis" },
                )
              // The parent's turn completes but the child holds it working.
              yield* waitFor("working")
              yield* finishChild(sandbox, `${rootId}-child-1`)
              yield* waitFor("idle")
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "checkSession reconciles descendants it never saw broadcast (reconnect)",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            yield* adapter.checkSession({ providerSessionId: "missing" })
            const url = sandbox.socketPath
            const spawn = (input: string) =>
              Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(url)
                  const started = yield* socket.request(1, "thread/start", {
                    cwd: sandbox.cwd,
                  })
                  const rootId = (started["thread"] as { id: string }).id
                  yield* socket.request(2, "turn/start", {
                    threadId: rootId,
                    input: [{ type: "text", text: input }],
                  })
                  return rootId
                }),
              )
            // SILENT: the fixture spawns the descendant without broadcasts,
            // so only the demand-driven walk can discover it.
            const workingRoot = yield* spawn("SPAWN SILENT worker")
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: workingRoot }),
              "working",
            )
            const waitingRoot = yield* spawn("SPAWN SILENT NEEDSINPUT worker")
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: waitingRoot }),
              "needs_input",
            )
            yield* finishChild(sandbox, `${workingRoot}-child-1`)
            assert.strictEqual(
              yield* adapter.checkSession({ providerSessionId: workingRoot }),
              "idle",
            )
          }),
        )
      }),
    30_000,
  )

  it.live(
    "reconciliation drops a tracked child whose idle broadcast was missed",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              // Live broadcasts teach the adapter about the child...
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "SPAWN worker", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              assert.strictEqual(yield* connection.activity, "working")
              // ...then the child finishes and unloads without a broadcast.
              yield* Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(sandbox.socketPath)
                  yield* socket.request(901, "test/child/vanish", {
                    threadId: `${connection.providerSessionId}-child-1`,
                  })
                }),
              )
              // Without reconciliation the stale working entry would pin
              // the aggregate busy forever; the walk replaces the set.
              assert.strictEqual(
                yield* adapter.checkSession({
                  providerSessionId: connection.providerSessionId,
                }),
                "idle",
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "a connection teardown tells observers the evidence is gone (unknown)",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* Effect.gen(function* () {
          const adapter = yield* CodexAdapter.CodexAdapter
          const codexServer = yield* CodexServer.CodexServer
          yield* Effect.ensuring(
            Effect.scoped(
              Effect.gen(function* () {
                yield* adapter.checkSession({ providerSessionId: "missing" })
                const url = sandbox.socketPath
                const rootId = yield* Effect.scoped(
                  Effect.gen(function* () {
                    const socket = yield* openExternal(url)
                    const started = yield* socket.request(1, "thread/start", {
                      cwd: sandbox.cwd,
                    })
                    return (started["thread"] as { id: string }).id
                  }),
                )
                const stream = yield* adapter.observeSession({
                  providerSessionId: rootId,
                  providerMetadata: undefined,
                })
                const sink: Array<string> = []
                yield* stream.pipe(
                  Stream.runForEach((event) =>
                    Effect.sync(() =>
                      sink.push(
                        event.type === "activity"
                          ? event.activity
                          : event.type === "userPrompt"
                            ? `prompt:${event.text}`
                            : event.type === "settings"
                              ? "settings"
                              : `item:${event.item.type}`,
                      ),
                    ),
                  ),
                  Effect.forkScoped,
                )
                yield* Effect.scoped(
                  Effect.gen(function* () {
                    const socket = yield* openExternal(url)
                    yield* socket.request(2, "turn/start", {
                      threadId: rootId,
                      input: [{ type: "text", text: "SPAWN then die" }],
                    })
                  }),
                )
                yield* eventually(
                  Effect.sync(() => sink),
                  (entries) => entries.includes("working"),
                  { attempts: 200, interval: "25 millis" },
                )
                // The evidence source dies with the socket: the observer
                // must hear `unknown`, never sit on the stale busy state.
                yield* Effect.orDie(codexServer.stop())
                yield* eventually(
                  Effect.sync(() => sink),
                  (entries) => entries.includes("unknown"),
                  { attempts: 200, interval: "25 millis" },
                )
              }),
            ),
            Effect.orDie(codexServer.stop()),
          )
        }).pipe(Effect.provide(codexAdapterLayer(sandbox)))
      }),
    30_000,
  )

  it.live("a failed connection attempt is memoized: one probe serves the whole window", () =>
    Effect.gen(function* () {
      const sandbox = makeCodexSandbox()
      // Break the wrapper: every spawn logs itself then dies immediately,
      // so each real probe fails fast and leaves a visible mark.
      const spawnLog = path.join(sandbox.base, "spawns.log")
      fs.writeFileSync(sandbox.wrapper, `#!/bin/sh\necho probe >> "${spawnLog}"\nexit 1\n`)
      const probes = () =>
        fs.existsSync(spawnLog) ? fs.readFileSync(spawnLog, "utf8").trim().split("\n").length : 0
      yield* withAdapter(sandbox, (adapter) =>
        Effect.gen(function* () {
          const first = yield* Effect.flip(adapter.checkSession({ providerSessionId: "t" }))
          assert.strictEqual(first._tag, "AgentUnavailable")
          const paid = probes()
          assert.isAtLeast(paid, 1, "the first call must really probe")
          // Within the memo window the failure is replayed, not re-probed —
          // a dead Codex costs one probe per window, not one per caller.
          const second = yield* Effect.flip(adapter.checkSession({ providerSessionId: "t" }))
          assert.strictEqual(second._tag, "AgentUnavailable")
          assert.strictEqual(probes(), paid)
        }),
      )
    }),
  )
})

describe("CodexAdapter collectTitleContext", () => {
  it.live(
    "retains observed conversation items as labeled context, pruned with the root",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const threadId = yield* Effect.scoped(
              Effect.gen(function* () {
                const { connection, turn } = yield* adapter.createSession({
                  cwd: sandbox.cwd,
                  input: { text: "seed turn", attachments: [] },
                  settings: TEST_SETTINGS,
                })
                const id = connection.providerSessionId
                const sink = yield* collectAgentEvents(connection.events)
                yield* waitForAgentEvent(
                  sink,
                  (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
                )
                // Nothing observed the first turn, so nothing was retained.
                assert.isNull(
                  yield* adapter.collectTitleContext({
                    providerSessionId: id,
                    cwd: sandbox.cwd,
                  }),
                )
                yield* adapter.observeSession({
                  providerSessionId: id,
                  providerMetadata: undefined,
                })
                const second = yield* connection.startTurn(
                  { text: "hello context", attachments: [] },
                  TEST_SETTINGS,
                )
                yield* waitForAgentEvent(
                  sink,
                  (event) => event.type === "turnCompleted" && event.turnId === second.turnId,
                )
                // Both the echoed user message and the reply, in order.
                yield* eventually(
                  adapter.collectTitleContext({ providerSessionId: id, cwd: sandbox.cwd }),
                  (context) => context === "user: hello context\nassistant: fake: hello context",
                )
                return id
              }),
            )
            // Writer and observer are gone: the retention went with the
            // root's bookkeeping.
            assert.isNull(
              yield* adapter.collectTitleContext({
                providerSessionId: threadId,
                cwd: sandbox.cwd,
              }),
            )
          }),
        )
      }),
    30_000,
  )

  // --- Chat mode settings (ATC-205) -------------------------------------------

  it.live(
    "settings: thread/start carries the ladder, turn/start pushes only the difference plus the two constants",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const settings = {
                model: "fake-sol",
                reasoning: "high",
                mode: "chat",
                access: "supervised",
              } as const
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "hello", attachments: [] },
                settings,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              // The second turn changes access and reasoning: exactly those
              // ride as overrides; model is unchanged and stays home.
              const second = yield* connection.startTurn(
                { text: "again", attachments: [] },
                {
                  ...settings,
                  reasoning: "low",
                  access: "fullAccess",
                  mode: "plan",
                },
              )
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === second.turnId,
              )
              const external = yield* openExternal(sandbox.socketPath)
              const resumed = yield* external.request(1, "thread/resume", {
                threadId: connection.providerSessionId,
              })
              // thread/start received the Supervised knobs; the last turn's
              // overrides moved the thread to Full access.
              assert.strictEqual(resumed["model"], "fake-sol")
              assert.strictEqual(resumed["reasoningEffort"], "low")
              assert.strictEqual(resumed["approvalPolicy"], "never")
              assert.deepStrictEqual(resumed["sandbox"], { type: "dangerFullAccess" })
              const turns = (resumed["thread"] as { turns: Array<{ overrides: unknown }> }).turns
              assert.deepStrictEqual(turns[0]!.overrides, {
                // thread/start carried model + Supervised; the reply echoed the
                // model's default effort (medium), so only effort differed.
                effort: "high",
                approvalsReviewer: "user",
                collaborationMode: {
                  mode: "default",
                  settings: { model: "fake-sol", reasoning_effort: "high" },
                },
              })
              assert.deepStrictEqual(turns[1]!.overrides, {
                effort: "low",
                approvalPolicy: "never",
                sandboxPolicy: { type: "dangerFullAccess" },
                approvalsReviewer: "user",
                collaborationMode: {
                  mode: "plan",
                  settings: { model: "fake-sol", reasoning_effort: "low" },
                },
              })
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "settings: a change made by another client reaches the writer feed and observers as the seam's report",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: { text: "hello", attachments: [] },
                settings: TEST_SETTINGS,
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              const observed: Array<AgentSessionEvent> = []
              const stream = yield* adapter.observeSession({
                providerSessionId: connection.providerSessionId,
                providerMetadata: undefined,
              })
              yield* stream.pipe(
                Stream.runForEach((event) => Effect.sync(() => observed.push(event))),
                Effect.forkScoped,
              )
              // The "TUI" switches model, effort, sandbox, and mode.
              const external = yield* openExternal(sandbox.socketPath)
              yield* external.request(1, "thread/settings/update", {
                threadId: connection.providerSessionId,
                model: "fake-luna",
                effort: "low",
                approvalPolicy: "on-request",
                sandboxType: "readOnly",
                mode: "plan",
              })
              yield* waitForAgentEvent(sink, (event) => event.type === "settings")
              assert.deepStrictEqual(
                sink.find((event) => event.type === "settings"),
                {
                  type: "settings",
                  settings: {
                    model: "fake-luna",
                    reasoning: "low",
                    access: "supervised",
                    mode: "plan",
                  },
                },
              )
              yield* eventually(
                Effect.sync(() => observed),
                (entries) => entries.some((event) => event.type === "settings"),
              )
              // Codex reports the ladder as two knobs; the reverse read projects
              // read-only onto Supervised whatever the approval policy says,
              // and an unknown effort onto "no level".
              yield* external.request(2, "thread/settings/update", {
                threadId: connection.providerSessionId,
                effort: "galactic",
                approvalPolicy: "never",
                sandboxType: "workspaceWrite",
                mode: "default",
              })
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "settings" && event.settings.access === "auto",
              )
              assert.deepStrictEqual(
                sink.findLast((event) => event.type === "settings"),
                {
                  type: "settings",
                  settings: { model: "fake-luna", reasoning: null, access: "auto", mode: "chat" },
                },
              )
            }),
          ),
        )
      }),
    30_000,
  )

  it.live(
    "listModels: every page of the catalog, hidden entries dropped, efforts and defaults decoded",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_MODEL_PAGED: "1" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            assert.deepStrictEqual(yield* adapter.listModels(), [
              {
                value: "fake-sol",
                displayName: "Fake Sol",
                description: "the default",
                isDefault: true,
                supportedEffortLevels: ["low", "medium", "high", "xhigh"],
                defaultEffortLevel: "medium",
              },
              {
                value: "fake-luna",
                displayName: "Fake Luna",
                description: "smaller",
                isDefault: false,
                supportedEffortLevels: ["low", "medium"],
                defaultEffortLevel: "low",
              },
              {
                value: "fake-terra",
                displayName: "Fake Terra",
                description: "misreports its default",
                isDefault: false,
                supportedEffortLevels: ["low", "medium"],
                defaultEffortLevel: "low",
              },
            ])
          }),
        )
      }),
    30_000,
  )
})
