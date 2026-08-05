import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
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
    "tuiLaunch hands back the exact remote-resume argv",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* withAdapter(sandbox, (adapter) =>
          Effect.gen(function* () {
            const spec = yield* adapter.tuiLaunch({
              providerSessionId: "some-thread",
              cwd: sandbox.cwd,
            })
            assert.strictEqual(spec.command[0], sandbox.wrapper)
            assert.deepStrictEqual(spec.command.slice(1, 3), ["resume", "--remote"])
            assert.match(spec.command[3] ?? "", /^ws:\/\/127\.0\.0\.1:\d+$/)
            assert.strictEqual(spec.command[4], "some-thread")
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
