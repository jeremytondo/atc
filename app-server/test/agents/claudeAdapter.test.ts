import { assert, describe, it } from "@effect/vitest"
import { Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import * as ClaudeAdapter from "../../src/agents/claudeAdapter.ts"
import * as ClaudeHooks from "../../src/agents/claudeHooks.ts"
import { claudeAdapterLayer, collectAgentEvents, waitForAgentEvent } from "./agentTestKit.ts"
import { trackTempDir } from "../blackbox.ts"
import { makeFakeClaudeQuery } from "./fakeClaudeQuery.ts"
import type { FakeClaudeQueryOptions } from "./fakeClaudeQuery.ts"

// Claude adapter tests over the scripted query() seam (fakeClaudeQuery.ts):
// the SDK's probed behaviors — init only after the first message, invalid
// resume as a single error result, iterator throw after error results —
// drive the adapter's deferred verification and truthful turn outcomes.
// claudeExecutable points at /bin/echo so resolution succeeds without a
// Claude install (the fake never spawns anything).

const workDir = () => trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-test-")))

const adapterStack = (options: FakeClaudeQueryOptions = {}) => {
  const fake = makeFakeClaudeQuery(options)
  const layer = claudeAdapterLayer({ queryFn: fake.queryFn, initTimeout: "5 seconds" })
  return { fake, layer }
}

describe("ClaudeAdapter", () => {
  it.live("create: identity verified from init, turn completes on the feed", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack({ sessionId: "session-a" })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "hello" })
            assert.strictEqual(connection.providerSessionId, "session-a")
            assert.strictEqual(connection.cwd, cwd)
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "completed",
            )
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("create with a lying cwd fails closed", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack({ wrongCwd: true })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.scoped(
          Effect.flip(adapter.createSession({ cwd: workDir(), input: "hello" })),
        )
        assert.strictEqual(failure._tag, "AgentIdentityMismatch")
        if (failure._tag === "AgentIdentityMismatch") assert.strictEqual(failure.field, "cwd")
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("resume: deferred verification completes at the first turn", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack({ knownSessions: ["session-b"] })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: "session-b",
              cwd,
            })
            // No identity evidence yet — the SDK sends none until a turn.
            assert.strictEqual(yield* connection.activity, "unknown")
            const sink = yield* collectAgentEvents(connection.events)
            const turn = yield* connection.startTurn("continue please")
            assert.strictEqual(connection.providerSessionId, "session-b")
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "completed",
            )
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("resume of an unknown session fails closed at the first turn", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack({ knownSessions: [] })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: "missing-session",
              cwd: workDir(),
            })
            return yield* Effect.flip(connection.startTurn("hello?"))
          }),
        )
        assert.strictEqual(failure._tag, "AgentResumeFailed")
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("a disagreeing session id in the evidence is a mismatch, never adopted", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack({
        knownSessions: ["session-c"],
        reportSessionId: "some-other-session",
      })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const failure = yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: "session-c",
              cwd: workDir(),
            })
            return yield* Effect.flip(connection.startTurn("hello?"))
          }),
        )
        assert.strictEqual(failure._tag, "AgentIdentityMismatch")
        if (failure._tag === "AgentIdentityMismatch") {
          assert.strictEqual(failure.field, "sessionId")
        }
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("one writer per session id", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack({ knownSessions: ["session-d"] })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            yield* adapter.resumeSession({ providerSessionId: "session-d", cwd })
            const failure = yield* Effect.flip(
              adapter.resumeSession({ providerSessionId: "session-d", cwd }),
            )
            assert.strictEqual(failure._tag, "AgentConflict")
          }),
        )
        // The writer role is released with the scope.
        yield* Effect.scoped(adapter.resumeSession({ providerSessionId: "session-d", cwd }))
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("interrupt: truthful interrupted outcome, then the connection ends", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "HANG until told otherwise",
            })
            const sink = yield* collectAgentEvents(connection.events)
            const stale = yield* Effect.flip(connection.interrupt({ turnId: "wrong-turn" }))
            assert.strictEqual(stale._tag, "AgentConflict")

            yield* connection.interrupt(turn)
            assert.deepStrictEqual(fake.interrupts.length, 1)
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "interrupted",
            )
            // An interrupted turn ends the held query; the session is over
            // (AgentUnavailable — the seam's "resume it" signal), not a
            // caller-side handle conflict.
            const closed = yield* Effect.flip(connection.startTurn("another"))
            assert.strictEqual(closed._tag, "AgentUnavailable")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("an error result is a failed turn, not a transport defect", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "FAILTURN please",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) =>
                event.type === "turnCompleted" &&
                event.turnId === turn.turnId &&
                event.outcome === "failed" &&
                (event.detail ?? "").includes("error_max_turns"),
            )
            // Error-path idle derived from the result message itself.
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("permission callbacks surface as request events and are denied", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "needs PERMISSION for a tool",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "requestOpened" && event.kind === "approval",
            )
            yield* waitForAgentEvent(sink, (event) => event.type === "requestClosed")
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.deepStrictEqual(fake.denials, [{ toolName: "Bash", behavior: "deny" }])
            assert.isTrue(
              sink.some((event) => event.type === "activity" && event.activity === "needs_input"),
            )
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("in-process hook callbacks alone still drive the activity feed", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      // No session_state_changed events at all: activity must come from
      // the SDK hook callbacks (UserPromptSubmit → working, Stop → idle).
      const { layer } = adapterStack({ stateEvents: false })
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({ cwd, input: "hello" })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.isTrue(
              sink.some((event) => event.type === "activity" && event.activity === "working"),
            )
            assert.strictEqual(yield* connection.activity, "idle")
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("AskUserQuestion surfaces as a question, not an approval", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const { fake, layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "ASK me something",
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "requestOpened" && event.kind === "question",
            )
            yield* waitForAgentEvent(
              sink,
              (event) => event.type === "turnCompleted" && event.turnId === turn.turnId,
            )
            assert.deepStrictEqual(fake.denials, [
              { toolName: "AskUserQuestion", behavior: "deny" },
            ])
          }),
        )
      }).pipe(Effect.provide(layer))
    }),
  )

  it.live("tuiLaunch registers a webhook secret and builds the hooks settings", () =>
    Effect.gen(function* () {
      const { layer } = adapterStack()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const launch = yield* adapter.tuiLaunch({
          providerSessionId: "session-t",
          cwd: "/w",
          providerMetadata: undefined,
        })
        const spec = launch.launchSpec
        // A launch that minted the secret hands the metadata back so the
        // caller can persist it.
        assert.isString(launch.providerMetadata)
        assert.strictEqual(spec.command[0], "/bin/echo")
        assert.deepStrictEqual(spec.command.slice(1, 3), ["--resume", "session-t"])
        assert.strictEqual(spec.command[3], "--settings")
        // The settings live in a 0600 file, never in argv.
        const settingsFile = spec.command[4] ?? ""
        assert.strictEqual((fs.statSync(settingsFile).mode & 0o777).toString(8), "600")
        const settings = JSON.parse(fs.readFileSync(settingsFile, "utf8")) as {
          hooks: Record<string, Array<{ hooks: Array<{ command: string }> }>>
        }
        assert.includeMembers(Object.keys(settings.hooks), ["Stop", "PermissionRequest"])
        const command = settings.hooks["Stop"]?.[0]?.hooks[0]?.command ?? ""
        assert.include(command, "/internal/claude/hooks")
        // The secret lives only in the 0600 header file, never in the
        // curl command line itself.
        const headerFile = /-H @'([^']+)'/.exec(command)?.[1] ?? ""
        assert.strictEqual((fs.statSync(headerFile).mode & 0o777).toString(8), "600")
        const header = fs.readFileSync(headerFile, "utf8")
        const secret =
          new RegExp(`${ClaudeHooks.SECRET_HEADER}: ([a-f0-9]+)`).exec(header)?.[1] ?? ""
        assert.isAbove(secret.length, 30)
        assert.notInclude(command, secret)
        // The returned metadata carries exactly the registered secret.
        assert.strictEqual(
          (JSON.parse(launch.providerMetadata ?? "{}") as { hookSecret?: string }).hookSecret,
          secret,
        )
        // The secret is live: a delivery for the registered session lands.
        const status = yield* hooks.deliver(secret, {
          session_id: "session-t",
          hook_event_name: "Stop",
        })
        assert.strictEqual(status, 204)
        // A relaunch that hands the metadata back keeps the same secret —
        // the running TUI's hooks stay valid.
        const relaunch = yield* adapter.tuiLaunch({
          providerSessionId: "session-t",
          cwd: "/w",
          providerMetadata: launch.providerMetadata,
        })
        assert.strictEqual(relaunch.providerMetadata, launch.providerMetadata)
      }).pipe(Effect.provide(layer))
    }),
  )
})

// ATC-140: the TUI-session plumbing — pre-assigned identity, hook-secret
// metadata, webhook-fed observation, and release cleanup. No query() seam
// involved: nothing here spawns or scripts the SDK.
describe("ClaudeAdapter TUI session plumbing", () => {
  const stateDir = () => trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-state-")))

  /** The real hooks service, with minted secrets recorded for assertions. */
  const spyHooksLayer = (minted: Array<string>) =>
    Layer.effect(ClaudeHooks.ClaudeHooks)(
      Effect.gen(function* () {
        const real = yield* ClaudeHooks.ClaudeHooks
        return {
          ...real,
          registerSecret: (providerSessionId: string) =>
            real
              .registerSecret(providerSessionId)
              .pipe(Effect.tap((secret) => Effect.sync(() => minted.push(secret)))),
        }
      }),
    ).pipe(Layer.provide(ClaudeHooks.layer))

  /** Collect an activity stream into a sink in the background (scoped). */
  const collectActivity = (stream: Stream.Stream<string>) =>
    Effect.gen(function* () {
      const sink: Array<string> = []
      yield* stream.pipe(
        Stream.runForEach((activity) => Effect.sync(() => sink.push(activity))),
        Effect.ignore,
        Effect.forkScoped,
      )
      return sink
    })

  const waitForActivity = (sink: Array<string>, wanted: string) =>
    Effect.gen(function* () {
      for (let attempt = 0; !sink.includes(wanted); attempt++) {
        assert.isBelow(attempt, 200, `never saw ${wanted} in ${JSON.stringify(sink)}`)
        yield* Effect.sleep("10 millis")
      }
    })

  it.live("prepareTuiSession pre-assigns identity and writes the hook plumbing", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            const prepared = yield* adapter.prepareTuiSession({ cwd })
            // Identity resolves immediately: pre-assignment, not capture.
            const identity = yield* prepared.identity
            assert.match(
              identity.providerSessionId,
              /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
            )
            assert.strictEqual(identity.cwd, cwd)
            assert.deepStrictEqual(prepared.launchSpec.command.slice(1, 3), [
              "--session-id",
              identity.providerSessionId,
            ])
            const settingsFile = prepared.launchSpec.command[4]!
            assert.isTrue(fs.existsSync(settingsFile))
            // The metadata carries the secret; the registration accepts it.
            const metadata = JSON.parse(identity.providerMetadata ?? "{}") as {
              hookSecret?: string
            }
            assert.isString(metadata.hookSecret)
            const status = yield* hooks.deliver(metadata.hookSecret ?? "", {
              session_id: identity.providerSessionId,
              hook_event_name: "Stop",
            })
            assert.strictEqual(status, 204)
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("observeSession restores the persisted secret and streams webhook activity", () =>
    Effect.gen(function* () {
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            // The secret arrives only via persisted metadata — the register
            // path never ran in this "restarted" process.
            const secret = "a".repeat(64)
            const metadata = JSON.stringify({ hookSecret: secret })
            const stream = yield* adapter.observeSession({
              providerSessionId: "restored-session",
              providerMetadata: metadata,
            })
            const sink = yield* collectActivity(stream)
            const status = yield* hooks.deliver(secret, {
              session_id: "restored-session",
              hook_event_name: "UserPromptSubmit",
            })
            assert.strictEqual(status, 204)
            yield* waitForActivity(sink, "working")
            // Another session's activity never leaks into this stream.
            const otherSecret = "b".repeat(64)
            yield* hooks.adoptSecret("other-session", otherSecret)
            yield* hooks.deliver(otherSecret, {
              session_id: "other-session",
              hook_event_name: "Stop",
            })
            yield* hooks.deliver(secret, {
              session_id: "restored-session",
              hook_event_name: "Stop",
            })
            yield* waitForActivity(sink, "idle")
            assert.deepStrictEqual(sink, ["working", "idle"])
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("releaseSession removes the hook files and revokes the secret", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      const dir = stateDir()
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        yield* Effect.scoped(
          Effect.gen(function* () {
            const prepared = yield* adapter.prepareTuiSession({ cwd })
            const identity = yield* prepared.identity
            const settingsFile = prepared.launchSpec.command[4]!
            const metadata = JSON.parse(identity.providerMetadata ?? "{}") as {
              hookSecret?: string
            }
            yield* adapter.releaseSession({
              providerSessionId: identity.providerSessionId,
              providerMetadata: identity.providerMetadata,
            })
            assert.isFalse(fs.existsSync(settingsFile))
            assert.isFalse(fs.existsSync(settingsFile.replace(/\.json$/, ".header")))
            const status = yield* hooks.deliver(metadata.hookSecret ?? "", {
              session_id: identity.providerSessionId,
              hook_event_name: "Stop",
            })
            assert.strictEqual(status, 404)
          }),
        )
      }).pipe(Effect.provide(claudeAdapterLayer({}, "/bin/echo", { stateDir: dir })))
    }),
  )

  it.live("a failed prepare revokes the freshly minted secret", () =>
    Effect.gen(function* () {
      const cwd = workDir()
      // stateDir is occupied by a regular file, so the hook plumbing fails
      // after the secret was minted and registered.
      const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-state-")))
      const blocked = path.join(base, "not-a-dir")
      fs.writeFileSync(blocked, "")
      const minted: Array<string> = []
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const failure = yield* Effect.scoped(Effect.flip(adapter.prepareTuiSession({ cwd })))
        assert.strictEqual(failure._tag, "AgentUnavailable")
        assert.lengthOf(minted, 1)
        const status = yield* hooks.deliver(minted[0] ?? "", {
          session_id: "any",
          hook_event_name: "Stop",
        })
        assert.strictEqual(status, 404)
      }).pipe(
        Effect.provide(
          claudeAdapterLayer({}, "/bin/echo", { stateDir: blocked }, spyHooksLayer(minted)),
        ),
      )
    }),
  )

  it.live("a failed settings write cleans the partial header file and secret", () =>
    Effect.gen(function* () {
      const dir = stateDir()
      // The settings path is occupied by a directory, so the SECOND write
      // fails after the header file was written and the secret registered.
      fs.mkdirSync(path.join(dir, "claude-hooks-session-w.json"))
      const minted: Array<string> = []
      yield* Effect.gen(function* () {
        const adapter = yield* ClaudeAdapter.ClaudeAdapter
        const hooks = yield* ClaudeHooks.ClaudeHooks
        const failure = yield* Effect.flip(
          adapter.tuiLaunch({
            providerSessionId: "session-w",
            cwd: "/w",
            providerMetadata: undefined,
          }),
        )
        assert.strictEqual(failure._tag, "AgentUnavailable")
        assert.isFalse(fs.existsSync(path.join(dir, "claude-hooks-session-w.header")))
        assert.lengthOf(minted, 1)
        const status = yield* hooks.deliver(minted[0] ?? "", {
          session_id: "session-w",
          hook_event_name: "Stop",
        })
        assert.strictEqual(status, 404)
      }).pipe(
        Effect.provide(
          claudeAdapterLayer({}, "/bin/echo", { stateDir: dir }, spyHooksLayer(minted)),
        ),
      )
    }),
  )
})
