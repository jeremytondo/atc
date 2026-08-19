import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { getSessionMessages } from "@anthropic-ai/claude-agent-sdk"
import { NESTED_SESSION_ENV_VARIABLES } from "../../src/agents/agentAdapter.ts"
import * as ClaudeAdapter from "../../src/agents/claudeAdapter.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import type { PtyChild } from "../../src/platform/subprocess.ts"
import { trackTempDir } from "../blackbox.ts"
import { eventually } from "../testLayers.ts"
import { claudeAdapterLayer, collectAgentEvents, waitForAgentEvent } from "./agentTestKit.ts"

// Live interactive-TUI smoke (opt-in: mise run test:smoke). Two checks:
//
//   - `--session-id` pre-assignment on an INTERACTIVE Claude launch (the
//     ATC-124 decision record's one open item; the -p engine-shared
//     evidence was probed, this proves the TUI path): a real
//     `claude --session-id <minted> --settings <hooks>` is spawned in a PTY
//     and must report the exact minted id through its own hook delivery,
//     before any prompt is typed. A possible first-run trust dialog is
//     answered with Enter.
//   - The one-process hand-off (ATC-203): a native SDK turn, then a TUI
//     turn on the same session (`--resume`) once the SDK process is gone,
//     then — the TUI ended after its last turn is on disk — a native turn
//     again that must answer with the context of BOTH earlier turns. One
//     conversation, one leaf; getSessionMessages agrees.

/** Cheap real-provider settings for the smoke turns (a real, small model). */
const SMOKE_SETTINGS = { model: "haiku", mode: "chat", access: "supervised" } as const

const enabled = process.env["ATC_SMOKE"] === "1"

interface HookDelivery {
  readonly session_id?: string
  readonly hook_event_name?: string
}

/** The hook capture server, a settings file wiring the TUI's hooks to it,
 * and the user's environment minus the nested-session markers this test
 * process may itself carry. */
const tuiHarness = (base: string) =>
  Effect.gen(function* () {
    const captured: Array<HookDelivery> = []
    const server = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      fetch: async (request) => {
        captured.push((await request.json()) as HookDelivery)
        return new Response(null, { status: 204 })
      },
    })
    yield* Effect.addFinalizer(() => Effect.promise(() => server.stop(true)))
    const command = `curl -fsS -m 5 -X POST -H 'Content-Type: application/json' --data-binary @- 'http://127.0.0.1:${server.port}/hook'`
    const settingsFile = path.join(base, "settings.json")
    fs.writeFileSync(
      settingsFile,
      JSON.stringify({
        hooks: Object.fromEntries(
          ["SessionStart", "UserPromptSubmit", "Stop"].map((event) => [
            event,
            [{ hooks: [{ type: "command", command }] }],
          ]),
        ),
      }),
    )
    const env: Record<string, string> = {}
    for (const [key, value] of Object.entries(process.env)) {
      if (value !== undefined) env[key] = value
    }
    for (const name of NESTED_SESSION_ENV_VARIABLES) delete env[name]
    return { captured, settingsFile, env }
  })

/** Spawn the real Claude TUI in a PTY (scoped: the scope's close ends it). */
const spawnTui = (cwd: string, args: ReadonlyArray<string>, env: Record<string, string>) =>
  Effect.gen(function* () {
    const subprocess = yield* Subprocess.Subprocess
    const child = yield* subprocess.spawnPty({
      executable: Subprocess.resolveExecutable("claude") ?? "claude",
      args,
      cwd,
      env,
      captureOutputTail: true,
    })
    yield* child.resize({ cols: 120, rows: 40 })
    return child
  })

/** Wait for a hook delivery matching `predicate`, nudging Enter through
 * occasionally in case the TUI is sitting on a first-run trust dialog. */
const awaitHook = (
  child: PtyChild,
  captured: ReadonlyArray<HookDelivery>,
  predicate: (delivery: HookDelivery) => boolean,
  attempts: number,
) =>
  Effect.gen(function* () {
    for (let attempt = 0; attempt < attempts; attempt++) {
      if (captured.some(predicate)) return true
      if (attempt > 0 && attempt % 20 === 0) yield* child.write("\r")
      yield* Effect.sleep("250 millis")
    }
    return false
  })

const slow = { attempts: 900, interval: "200 millis" } as const

describe.skipIf(!enabled)("live claude TUI smoke (opt-in)", () => {
  it.live(
    "an interactive launch adopts the minted --session-id and confirms via hooks",
    () =>
      Effect.gen(function* () {
        const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-tui-")))
        const cwd = path.join(base, "work")
        fs.mkdirSync(cwd)
        const { captured, settingsFile, env } = yield* tuiHarness(base)
        const sessionId = crypto.randomUUID()
        const child = yield* spawnTui(
          cwd,
          ["--session-id", sessionId, "--settings", settingsFile],
          env,
        )
        const confirmed = yield* awaitHook(child, captured, (p) => p.session_id === sessionId, 240)
        const tail = yield* child.outputTail
        assert.isTrue(
          confirmed,
          `no hook delivery carried the minted id; captured=${JSON.stringify(captured)} tail=${tail.slice(-500)}`,
        )
        assert.notInclude(tail, "already in use")
      }).pipe(
        Effect.scoped,
        Effect.provide(Subprocess.layer.pipe(Layer.provideMerge(BunServices.layer))),
      ),
    120_000,
  )

  it.live(
    "native → TUI → native on one session keeps one conversation with full context",
    () =>
      Effect.gen(function* () {
        const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-handoff-")))
        const cwd = path.join(base, "work")
        fs.mkdirSync(cwd)
        const { captured, settingsFile, env } = yield* tuiHarness(base)
        const adapter = yield* ClaudeAdapter.ClaudeAdapter

        const completedTurn = (sink: Parameters<typeof waitForAgentEvent>[0], turnId: string) =>
          waitForAgentEvent(
            sink,
            (event) =>
              event.type === "turnCompleted" &&
              event.turnId === turnId &&
              event.outcome === "completed",
            slow,
          )

        // 1. Native turn; the connection scope closes, so the SDK process
        //    is gone before the TUI starts (one live process per thread).
        const sessionId = yield* Effect.scoped(
          Effect.gen(function* () {
            const { connection, turn } = yield* adapter.createSession({
              cwd,
              input: "The first code word is ALPHA. Reply with exactly: OK",
              settings: SMOKE_SETTINGS,
            })
            const sink = yield* collectAgentEvents(connection.events)
            yield* completedTurn(sink, turn.turnId)
            return connection.providerSessionId
          }),
        )

        // 2. TUI turn on the same session; ended only after its last turn is
        //    on disk (the adapter's readHistory settle), as the runtime does.
        yield* Effect.scoped(
          Effect.gen(function* () {
            const child = yield* spawnTui(
              cwd,
              ["--resume", sessionId, "--settings", settingsFile],
              env,
            )
            const started = yield* awaitHook(
              child,
              captured,
              (p) => p.session_id === sessionId,
              240,
            )
            assert.isTrue(
              started,
              `the TUI never started: ${(yield* child.outputTail).slice(-500)}`,
            )
            // Type the prompt once the TUI echoes it (its input is live),
            // then submit.
            const prompt = "The second code word is BRAVO. Reply with exactly: OK"
            yield* eventually(
              child.write(prompt).pipe(Effect.andThen(child.outputTail)),
              (tail) => tail.includes(prompt),
              { attempts: 20, interval: "500 millis" },
            )
            yield* child.write("\r")
            const stopped = yield* awaitHook(
              child,
              captured,
              (p) => p.session_id === sessionId && p.hook_event_name === "Stop",
              600,
            )
            assert.isTrue(
              stopped,
              `the TUI turn never stopped: ${(yield* child.outputTail).slice(-800)}`,
            )
            const history = yield* adapter.readHistory({ providerSessionId: sessionId, cwd })
            assert.strictEqual(history.length, 2)
            assert.isAtLeast(history[1]?.items.length ?? 0, 2)
          }),
        )

        // 3. Native again: the model must know both code words.
        yield* Effect.scoped(
          Effect.gen(function* () {
            const connection = yield* adapter.resumeSession({
              providerSessionId: sessionId,
              cwd,
              settings: SMOKE_SETTINGS,
            })
            const sink = yield* collectAgentEvents(connection.events)
            const turn = yield* connection.startTurn(
              "Reply with the two code words I gave you, in order, separated by a space, and nothing else.",
              SMOKE_SETTINGS,
            )
            yield* completedTurn(sink, turn.turnId)
            const text = sink
              .flatMap((event) =>
                event.type === "itemCompleted" && event.item.type === "assistantText"
                  ? [event.item.text]
                  : [],
              )
              .join(" ")
            assert.include(text, "ALPHA")
            assert.include(text, "BRAVO")
          }),
        )
        const messages = yield* Effect.promise(() => getSessionMessages(sessionId, { dir: cwd }))
        const prompts = messages.filter((m) => m.type === "user" && m.parent_tool_use_id === null)
        assert.strictEqual(prompts.length, 3)
      }).pipe(
        Effect.scoped,
        Effect.provide(claudeAdapterLayer({ sessionMessagesFn: getSessionMessages }, "claude")),
        Effect.provide(Subprocess.layer.pipe(Layer.provideMerge(BunServices.layer))),
      ),
    300_000,
  )
})
