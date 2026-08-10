// ATC-158 Claude probe: record the hook payload sequence around background
// subagents, backgrounded shell tasks, and session crons. Evidence sought:
//   (a) root Stop fires with non-empty background_tasks while a background
//       descendant runs;
//   (b) SubagentStop for the last descendant carries the level snapshot
//       excluding it (the last-child transition evidence);
//   (c) session_state_changed timing relative to the result message;
//   (d) session_crons payload shape, if a cron tool is reachable.
// Output: claude/recording.jsonl — one {at, kind, data} line per event.
import { query, type SDKUserMessage } from "@anthropic-ai/claude-agent-sdk"
import { appendFileSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import * as path from "node:path"

const OUT = new URL("./recording.jsonl", import.meta.url).pathname
writeFileSync(OUT, "")
const started = Date.now()
const record = (kind: string, data: unknown): void => {
  appendFileSync(OUT, `${JSON.stringify({ at: Date.now() - started, kind, data })}\n`)
  console.log(`[${Date.now() - started}ms] ${kind}`)
}

const HOOK_EVENTS = [
  "UserPromptSubmit",
  "PreToolUse",
  "PostToolUse",
  "Stop",
  "StopFailure",
  "SubagentStart",
  "SubagentStop",
  "TaskCreated",
  "TaskCompleted",
  "Notification",
  "PermissionRequest",
] as const

const cwd = mkdtempSync(path.join(tmpdir(), "atc-158-claude-"))

const pending: Array<SDKUserMessage> = []
let wake: (() => void) | null = null
let closed = false
const push = (text: string): void => {
  pending.push({ type: "user", message: { role: "user", content: text }, parent_tool_use_id: null })
  wake?.()
}
async function* input(): AsyncGenerator<SDKUserMessage> {
  while (true) {
    const next = pending.shift()
    if (next !== undefined) {
      yield next
      continue
    }
    if (closed) return
    await new Promise<void>((resolve) => {
      wake = () => {
        wake = null
        resolve()
      }
    })
  }
}

const env: Record<string, string> = {}
for (const [key, value] of Object.entries(process.env)) if (value !== undefined) env[key] = value
env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
for (const name of [
  "CLAUDE_CODE_CHILD_SESSION",
  "CLAUDECODE",
  "CLAUDE_CODE_ENTRYPOINT",
  "CLAUDE_CODE_SSE_PORT",
  "CLAUDE_CODE_SESSION_ID",
  "CLAUDE_CODE_BRIDGE_SESSION_ID",
  "CLAUDE_PID",
])
  delete env[name]

const hooks = Object.fromEntries(
  HOOK_EVENTS.map((event) => [
    event,
    [
      {
        hooks: [
          (payload: unknown) => {
            record("hook", payload)
            return Promise.resolve({ continue: true as const })
          },
        ],
      },
    ],
  ]),
)

// Turn scripts, advanced when each `result` message arrives.
const turns: Array<string> = [
  // (a)+(b): a background subagent that outlives the root turn.
  "Use the Agent tool (subagent) with run_in_background set to true to launch one general-purpose agent whose prompt is exactly: \"Run the bash command `sleep 8` and then reply DONE-CHILD\". Do NOT wait for the agent or check on it; the moment it is launched, reply with exactly SPAWNED and end your turn.",
  // shell-type background task evidence.
  "Use the Bash tool with run_in_background set to true to start the command `sleep 6`. Do not wait for it; immediately reply with exactly BG-SHELL and end your turn.",
  // (d): cron evidence, if any cron/wakeup tool exists in this context.
  "If you have a tool for scheduling a wake-up or cron for this session (for example ScheduleWakeup or CronCreate), call it to schedule a one-shot wake-up 300 seconds from now, then reply CRON-DONE. If you have no such tool, reply NO-CRON-TOOL. Do nothing else.",
]

let turnIndex = 0
const handle = query({
  prompt: input(),
  options: {
    cwd,
    env,
    settingSources: [],
    permissionMode: "bypassPermissions",
    persistSession: true,
    hooks: hooks as never,
  },
})

const deadline = setTimeout(() => {
  record("timeout", "probe hit the global deadline")
  closed = true
  wake?.()
  process.exit(1)
}, 240_000)

push(turns[turnIndex]!)
for await (const message of handle) {
  const m = message as { type: string; subtype?: string }
  record(`sdk:${m.type}${m.subtype !== undefined ? `:${m.subtype}` : ""}`, message)
  if (m.type === "result") {
    turnIndex += 1
    if (turnIndex === 1) {
      // Keep the session open so the background subagent's SubagentStop can
      // fire while the root is idle; then move on.
      record("note", "waiting 20s after turn 1 for background subagent completion evidence")
      await new Promise((resolve) => setTimeout(resolve, 20_000))
    }
    if (turnIndex === 2) {
      record("note", "waiting 12s after turn 2 for background shell completion evidence")
      await new Promise((resolve) => setTimeout(resolve, 12_000))
    }
    const next = turns[turnIndex]
    if (next === undefined) {
      closed = true
      wake?.()
    } else {
      push(next)
    }
  }
}
clearTimeout(deadline)
record("done", { cwd })
console.log(`recorded to ${OUT}`)
