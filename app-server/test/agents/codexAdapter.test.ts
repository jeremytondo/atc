import { assert, describe, it } from "@effect/vitest"
import { Effect, Stream } from "effect"
import * as fs from "node:fs"
import * as path from "node:path"
import * as CodexAdapter from "../../src/agents/codexAdapter.ts"
import * as CodexServer from "../../src/agents/codexServer.ts"
import {
  codexAdapterLayer,
  collectAgentEvents,
  makeCodexSandbox,
  waitForAgentEvent,
} from "./agentTestKit.ts"

// Codex adapter tests against the fake app-server fixture, through the real
// supervision module (ensure → detached fixture → WebSocket → adapter). All
// it.live: real processes, sockets, and clock. Each test block ends with
// CodexServer.stop() via `withAdapter` so no detached fixture leaks.

/** Run `use` with the adapter, then always stop the detached server. */
const withAdapter = (
  sandbox: { readonly stateDir: string; readonly wrapper: string },
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
                input: "hello",
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
                  input: "seed",
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
                })
                assert.strictEqual(resumed.providerSessionId, threadId)
              }),
            )

            const failure = yield* Effect.scoped(
              Effect.flip(
                adapter.resumeSession({
                  providerSessionId: "00000000-0000-7000-8000-000000000000",
                  cwd: sandbox.cwd,
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
              Effect.flip(adapter.createSession({ cwd: sandbox.cwd, input: "hello" })),
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
                  input: "seed",
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
              Effect.flip(adapter.resumeSession({ providerSessionId: threadId, cwd: sandbox.cwd })),
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
                input: "HANG on",
              })
              const failure = yield* Effect.flip(
                adapter.resumeSession({
                  providerSessionId: connection.providerSessionId,
                  cwd: sandbox.cwd,
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
                input: "HANG until interrupted",
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
    "provider approval requests surface on the feed and are auto-rejected",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: "needs APPROVAL for this",
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(sink, (event) => event.type === "requestOpened")
              const opened = sink.find((event) => event.type === "requestOpened")
              assert.strictEqual(opened?.type === "requestOpened" ? opened.kind : "", "approval")
              yield* waitForAgentEvent(sink, (event) => event.type === "requestClosed")
              // The reject unblocks the fixture, so the turn still completes.
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )
              // needs_input was truthfully reported while the request hung.
              assert.isTrue(
                sink.some((event) => event.type === "activity" && event.activity === "needs_input"),
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
                input: "seed",
              })
              const sink = yield* collectAgentEvents(connection.events)
              yield* waitForAgentEvent(
                sink,
                (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
              )

              // A second client of the same shared server (the TUI stand-in)
              // drives a turn on the same thread.
              const identity = JSON.parse(
                fs.readFileSync(path.join(sandbox.stateDir, "codex-app-server.json"), "utf8"),
              ) as { port: number }
              const external = new WebSocket(`ws://127.0.0.1:${identity.port}`)
              yield* Effect.callback<void>((resume) => {
                external.onopen = () => resume(Effect.void)
              })
              external.send(
                JSON.stringify({
                  id: 1,
                  method: "turn/start",
                  params: {
                    threadId: connection.providerSessionId,
                    input: [{ type: "text", text: "external turn" }],
                  },
                }),
              )
              yield* waitForAgentEvent(
                sink,
                (event) =>
                  event.type === "turnCompleted" &&
                  event.turnId !== turn.turnId &&
                  event.outcome === "completed",
              )
              external.close()
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
                adapter.createSession({ cwd: sandbox.cwd, input: "seed" }),
                ({ connection }) => connection.providerSessionId,
              ),
            )
            const { launchSpec } = yield* adapter.tuiLaunch({
              providerSessionId: sessionId,
              cwd: sandbox.cwd,
              providerMetadata: undefined,
            })
            assert.strictEqual(launchSpec.command[0], sandbox.wrapper)
            assert.deepStrictEqual(launchSpec.command.slice(1, 3), ["resume", "--remote"])
            assert.match(launchSpec.command[3] ?? "", /^ws:\/\/127\.0\.0\.1:\d+$/)
            assert.strictEqual(launchSpec.command[4], sessionId)

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
    "an older installed codex warns but still works",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_VERSION: "0.1.0" })
        yield* withAdapter(sandbox, (adapter) =>
          Effect.scoped(
            Effect.gen(function* () {
              const { connection, turn } = yield* adapter.createSession({
                cwd: sandbox.cwd,
                input: "hello",
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
  const openExternal = (url: string) =>
    Effect.acquireRelease(
      Effect.callback<WebSocket>((resume) => {
        const socket = new WebSocket(url)
        socket.onopen = () => resume(Effect.succeed(socket))
      }),
      (socket) => Effect.sync(() => socket.close()),
    )

  const externalRequest = (socket: WebSocket, id: number, method: string, params: unknown) =>
    Effect.callback<Record<string, unknown>>((resume) => {
      const listener = (event: MessageEvent) => {
        const message = JSON.parse(String(event.data)) as { id?: number; result?: unknown }
        if (message.id === id) {
          socket.removeEventListener("message", listener)
          resume(Effect.succeed((message.result ?? {}) as Record<string, unknown>))
        }
      }
      socket.addEventListener("message", listener)
      socket.send(JSON.stringify({ id, method, params }))
    })

  const startExternalThread = (socket: WebSocket, id: number, cwd: string) =>
    externalRequest(socket, id, "thread/start", { cwd }).pipe(
      Effect.map((result) => (result["thread"] as { id: string }).id),
    )

  const waitForActivity = (sink: Array<string>, wanted: string) =>
    Effect.gen(function* () {
      for (let attempt = 0; !sink.includes(wanted); attempt++) {
        assert.isBelow(attempt, 200, `never saw ${wanted} in ${JSON.stringify(sink)}`)
        yield* Effect.sleep("25 millis")
      }
    })

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
              assert.strictEqual(prepared.launchSpec.command[3], "--remote")
              const url = prepared.launchSpec.command[4] ?? ""
              assert.match(url, /^ws:\/\/127\.0\.0\.1:\d+$/)

              // The launched TUI stand-in bootstraps a thread in the cwd.
              const external = yield* openExternal(url)
              const threadId = yield* startExternalThread(external, 1, sandbox.cwd)
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
              const url = prepared.launchSpec.command[4] ?? ""
              const external = yield* openExternal(url)
              // Another client's thread in a different cwd is not ours.
              const foreignId = yield* startExternalThread(external, 1, otherCwd)
              const matchingId = yield* startExternalThread(external, 2, sandbox.cwd)
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
              const identity = JSON.parse(
                fs.readFileSync(path.join(sandbox.stateDir, "codex-app-server.json"), "utf8"),
              ) as { port: number }
              const external = yield* openExternal(`ws://127.0.0.1:${identity.port}`)
              const threadId = yield* startExternalThread(external, 1, sandbox.cwd)

              const stream = yield* adapter.observeSession({
                providerSessionId: threadId,
                providerMetadata: undefined,
              })
              const sink: Array<string> = []
              yield* stream.pipe(
                Stream.runForEach((value) => Effect.sync(() => sink.push(value))),
                Effect.ignore,
                Effect.forkScoped,
              )
              yield* externalRequest(external, 2, "turn/start", {
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
            const identity = JSON.parse(
              fs.readFileSync(path.join(sandbox.stateDir, "codex-app-server.json"), "utf8"),
            ) as { port: number }
            // The fake serves one thread per page, so the second thread only
            // appears after following nextCursor.
            const laterPage = yield* Effect.scoped(
              Effect.gen(function* () {
                const socket = yield* openExternal(`ws://127.0.0.1:${identity.port}`)
                yield* startExternalThread(socket, 1, sandbox.cwd)
                return yield* startExternalThread(socket, 2, sandbox.cwd)
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
                const socket = yield* openExternal(`ws://127.0.0.1:${identity.port}`)
                yield* externalRequest(socket, 3, "thread/archive", { threadId: laterPage })
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
  const openExternal = (url: string) =>
    Effect.acquireRelease(
      Effect.callback<WebSocket>((resume) => {
        const socket = new WebSocket(url)
        socket.onopen = () => resume(Effect.succeed(socket))
      }),
      (socket) => Effect.sync(() => socket.close()),
    )

  const externalRequest = (socket: WebSocket, id: number, method: string, params: unknown) =>
    Effect.callback<Record<string, unknown>>((resume) => {
      const listener = (event: MessageEvent) => {
        const message = JSON.parse(String(event.data)) as { id?: number; result?: unknown }
        if (message.id === id) {
          socket.removeEventListener("message", listener)
          resume(Effect.succeed((message.result ?? {}) as Record<string, unknown>))
        }
      }
      socket.addEventListener("message", listener)
      socket.send(JSON.stringify({ id, method, params }))
    })

  const fixtureUrl = (sandbox: { readonly stateDir: string }) => {
    const identity = JSON.parse(
      fs.readFileSync(path.join(sandbox.stateDir, "codex-app-server.json"), "utf8"),
    ) as { port: number }
    return `ws://127.0.0.1:${identity.port}`
  }

  const finishChild = (sandbox: { readonly stateDir: string }, childId: string) =>
    Effect.scoped(
      Effect.gen(function* () {
        const socket = yield* openExternal(fixtureUrl(sandbox))
        yield* externalRequest(socket, 900, "test/child/finish", { threadId: childId })
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
                input: "SPAWN one worker",
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
                input: "SPAWN NEEDSINPUT worker",
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
              const url = fixtureUrl(sandbox)
              const rootId = yield* Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(url)
                  const started = yield* externalRequest(socket, 1, "thread/start", {
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
                Stream.runForEach((activity) => Effect.sync(() => sink.push(activity))),
                Effect.forkScoped,
              )
              // An external writer (TUI stand-in) runs the spawning turn.
              yield* Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(url)
                  yield* externalRequest(socket, 2, "turn/start", {
                    threadId: rootId,
                    input: [{ type: "text", text: "SPAWN from tui" }],
                  })
                }),
              )
              const waitFor = (wanted: string) =>
                Effect.gen(function* () {
                  for (let attempt = 0; sink[sink.length - 1] !== wanted; attempt++) {
                    assert.isBelow(attempt, 200, `never settled on ${wanted}: ${sink.join(",")}`)
                    yield* Effect.sleep("25 millis")
                  }
                })
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
            const url = fixtureUrl(sandbox)
            const spawn = (input: string) =>
              Effect.scoped(
                Effect.gen(function* () {
                  const socket = yield* openExternal(url)
                  const started = yield* externalRequest(socket, 1, "thread/start", {
                    cwd: sandbox.cwd,
                  })
                  const rootId = (started["thread"] as { id: string }).id
                  yield* externalRequest(socket, 2, "turn/start", {
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
})
