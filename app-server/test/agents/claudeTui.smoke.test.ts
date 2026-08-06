import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { NESTED_SESSION_ENV_VARIABLES } from "../../src/agents/agentAdapter.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { trackTempDir } from "../blackbox.ts"

// Live interactive-TUI smoke (opt-in: mise run test:smoke): the one
// implementation-time check the ATC-124 decision record left open —
// `--session-id` pre-assignment on an INTERACTIVE Claude launch (the -p
// engine-shared evidence was probed; this proves the TUI path). A real
// `claude --session-id <minted> --settings <hooks>` is spawned in a PTY and
// must report the exact minted id through its own hook delivery, before any
// prompt is typed. A possible first-run trust dialog is answered with Enter.

const enabled = process.env["ATC_SMOKE"] === "1"

describe.skipIf(!enabled)("live claude TUI pre-assignment smoke (opt-in)", () => {
  it.live(
    "an interactive launch adopts the minted --session-id and confirms via hooks",
    () =>
      Effect.gen(function* () {
        const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-claude-tui-")))
        const cwd = path.join(base, "work")
        fs.mkdirSync(cwd)

        // Capture server: every hook delivery lands here as JSON.
        const captured: Array<{ session_id?: string; hook_event_name?: string }> = []
        const server = Bun.serve({
          hostname: "127.0.0.1",
          port: 0,
          fetch: async (request) => {
            captured.push((await request.json()) as (typeof captured)[number])
            return new Response(null, { status: 204 })
          },
        })
        yield* Effect.addFinalizer(() => Effect.promise(() => server.stop(true)))

        const command = `curl -fsS -m 5 -X POST -H 'Content-Type: application/json' --data-binary @- 'http://127.0.0.1:${server.port}/hook'`
        const hookConfig = Object.fromEntries(
          ["SessionStart", "UserPromptSubmit", "Stop"].map((event) => [
            event,
            [{ hooks: [{ type: "command", command }] }],
          ]),
        )
        const settingsFile = path.join(base, "settings.json")
        fs.writeFileSync(settingsFile, JSON.stringify({ hooks: hookConfig }))

        const sessionId = crypto.randomUUID()
        // The user's env (credentials) minus the nested-session markers this
        // test process may itself carry.
        const env: Record<string, string> = {}
        for (const [key, value] of Object.entries(process.env)) {
          if (value !== undefined) env[key] = value
        }
        for (const name of NESTED_SESSION_ENV_VARIABLES) delete env[name]

        const subprocess = yield* Subprocess.Subprocess
        const child = yield* subprocess.spawnPty({
          executable: Subprocess.resolveExecutable("claude") ?? "claude",
          args: ["--session-id", sessionId, "--settings", settingsFile],
          cwd,
          env,
          captureOutputTail: true,
        })
        yield* child.resize({ cols: 120, rows: 40 })

        // Wait for the hook confirmation; nudge Enter through occasionally
        // in case the TUI is sitting on a first-run trust dialog.
        const confirmed = yield* Effect.gen(function* () {
          for (let attempt = 0; attempt < 240; attempt++) {
            const hit = captured.find((payload) => payload.session_id === sessionId)
            if (hit !== undefined) return true
            if (attempt > 0 && attempt % 20 === 0) yield* child.write("\r")
            yield* Effect.sleep("250 millis")
          }
          return false
        })
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
})
