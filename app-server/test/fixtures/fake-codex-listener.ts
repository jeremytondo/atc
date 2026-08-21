// Fixture stand-in for `codex app-server --listen unix://`: serves a
// WebSocket on Codex's well-known control socket under CODEX_HOME and
// speaks the JSON-RPC subset the codex adapter uses
// (initialize, thread/start, thread/resume, turn/start, turn/interrupt,
// thread/unsubscribe), broadcasting notifications to every connected
// socket like the real shared app-server. Like the real server it exits
// "already in use" when a live server answers the socket, and rebinds
// over a stale socket file that nothing answers.
//
// Turn behavior is keyed off the input text: containing "HANG" leaves the
// turn active until interrupted; containing "APPROVAL" runs a
// commandExecution item that first round-trips an
// item/commandExecution/requestApproval server request (the decision
// completes or declines the item); "QUESTION" round-trips an
// item/tool/requestUserInput and echoes the answers back as the agent
// message; "STREAM" streams the agent message as item/agentMessage/delta
// frames between item/started and item/completed; "COMMAND" runs a
// completed commandExecution item; every turn broadcasts its userMessage and
// agentMessage items and records them on the turn for thread/resume;
// containing "SPAWN"
// creates a descendant thread (`<threadId>-child-N`) that stays active
// after the parent's turn completes — mirroring the probed real behavior
// (experiments/subagent-activity): a subAgentActivity item on the parent
// plus child thread/status/changed broadcasts, NO thread/started, and no
// thread/list entry. "SPAWN SILENT" skips the spawn broadcasts (the
// reconnect-reconciliation scenario: state exists server-side only);
// "NEEDSINPUT" makes the child wait on approval. The test-only
// "test/child/finish" request completes a child.
//
// Settings (ATC-205): each thread holds model / effort / approvalPolicy /
// sandbox / mode as the real server does. thread/start takes model,
// approvalPolicy and sandbox; thread/start and thread/resume echo them at the
// reply's top level; turn/start applies its overrides (model, effort,
// approvalPolicy, sandboxPolicy, collaborationMode) and records the params
// it received on the turn (`overrides`, visible via thread/resume for
// assertions); thread/settings/update changes settings and broadcasts
// thread/settings/updated like a TUI change would; model/list serves a
// small catalog.
//
// Steering and skills (ATC-216): turn/steer takes input only while the
// named turn is the thread's active one (anything else is an invalid
// request) and echoes it as that turn's next userMessage item, localImage
// blocks included; skills/list answers one entry per asked cwd with one
// enabled skill ("review") and one disabled.
//
// Env switches (baked into the wrapper script by tests):
//   FAKE_CODEX_MODE       "ok" (default) | "never-ready" (alive, never binds)
//   FAKE_CODEX_WRONG_CWD  "start" | "resume" — lie about cwd in that reply
//   FAKE_CODEX_VERSION    printed for `--version` and carried in initialize's
//                         userAgent (default 0.147.0)
//   FAKE_CODEX_PID_FILE   record this pid for reap assertions; the listener
//                         argv lands next to it (listen-record.json) so
//                         tests can pin the launch shape
//   FAKE_CODEX_TTL_MS     self-exit backstop (default 120s) so a test that
//                         fails to stop() a detached fixture cannot leak it
//   FAKE_CODEX_READ_DELAY_MS  delay root thread/read replies, so tests can
//                         prove activity never waits on the ATC-155
//                         prompt (preview) read
//   FAKE_CODEX_MODEL_PAGED  "1" — serve model/list one model per page
//   FAKE_CODEX_PREVIEW_DELAY_MS  make preview readable only this long
//                         after the first turn starts (the real rollout
//                         flush lag), so tests can prove the ATC-155
//                         discovery loop lands mid-turn

import { existsSync, mkdirSync, rmSync } from "node:fs"
import * as path from "node:path"

if (process.argv.includes("--version")) {
  console.log(`codex-cli ${process.env["FAKE_CODEX_VERSION"] ?? "0.147.0"}`)
  process.exit(0)
}

// `codex exec` stand-in (ATC-155 title generation): read the prompt from
// stdin, write the "last agent message" to the --output-last-message file.
//   FAKE_CODEX_EXEC_EXIT    non-zero → fail without writing (default 0)
//   FAKE_CODEX_TITLE        fixed reply (default: `fake title: <last line>`,
//                           the last stdin line being the user's message)
//   FAKE_CODEX_EXEC_RECORD  write {argv, markers} JSON here, so tests can
//                           pin the safety-relevant invocation shape
if (process.argv.includes("exec")) {
  const stdin = await new Response(Bun.stdin.stream()).text()
  const pidFileEnv = process.env["FAKE_CODEX_PID_FILE"]
  const recordFile =
    process.env["FAKE_CODEX_EXEC_RECORD"] ??
    (pidFileEnv !== undefined && pidFileEnv !== ""
      ? pidFileEnv.replace(/fixture\.pid$/, "exec-record.json")
      : undefined)
  if (recordFile !== undefined && recordFile !== "") {
    await Bun.write(
      recordFile,
      JSON.stringify({
        argv: process.argv.slice(2),
        markers: ["CLAUDECODE", "CLAUDE_CODE_SESSION_ID"].filter((name) => name in process.env),
      }),
    )
  }
  const execExit = Number.parseInt(process.env["FAKE_CODEX_EXEC_EXIT"] ?? "0", 10)
  if (execExit !== 0) {
    console.error("fake codex exec: scripted failure")
    process.exit(execExit)
  }
  const outIndex = process.argv.indexOf("--output-last-message")
  const outFile = outIndex >= 0 ? process.argv[outIndex + 1] : undefined
  const lastLine =
    stdin
      .split("\n")
      .map((line) => line.trim())
      .findLast((line) => line !== "") ?? ""
  if (outFile !== undefined) {
    await Bun.write(outFile, process.env["FAKE_CODEX_TITLE"] ?? `fake title: ${lastLine}`)
  }
  process.exit(0)
}

const listenIndex = process.argv.indexOf("--listen")
const listenArg = listenIndex >= 0 ? process.argv[listenIndex + 1] : undefined
const mode = process.env["FAKE_CODEX_MODE"] ?? "ok"
const wrongCwd = process.env["FAKE_CODEX_WRONG_CWD"]
const version = process.env["FAKE_CODEX_VERSION"] ?? "0.147.0"
const ttl = Number.parseInt(process.env["FAKE_CODEX_TTL_MS"] ?? "120000", 10)
const pidFile = process.env["FAKE_CODEX_PID_FILE"]
if (pidFile !== undefined && pidFile !== "") {
  await Bun.write(pidFile, String(process.pid))
  await Bun.write(
    path.join(path.dirname(pidFile), "listen-record.json"),
    JSON.stringify({ argv: process.argv.slice(2) }),
  )
}
setTimeout(() => process.exit(0), ttl)

// Bare `unix://` — the one shape ATC starts — binds the well-known socket
// under CODEX_HOME (~/.codex by default), the same derivation as codex.
if (listenArg !== "unix://") {
  console.error("fake-codex-listener: expected --listen unix://")
  process.exit(64)
}
const codexHome =
  process.env["CODEX_HOME"] !== undefined && process.env["CODEX_HOME"] !== ""
    ? process.env["CODEX_HOME"]
    : `${process.env["HOME"]}/.codex`
const socketPath = `${codexHome}/app-server-control/app-server-control.sock`

// A wedged server: alive, never binds.
if (mode === "never-ready") await new Promise(() => {})

// The real server's startup rule: a live server answering the socket means
// "already in use"; a stale socket file that nothing answers is rebound.
mkdirSync(path.dirname(socketPath), { recursive: true })
if (existsSync(socketPath)) {
  const answers = await Bun.connect({
    unix: socketPath,
    socket: {
      data() {},
      open(socket) {
        socket.end()
      },
    },
  }).then(
    () => true,
    () => false,
  )
  if (answers) {
    console.error("app-server control socket is already in use")
    process.exit(1)
  }
  rmSync(socketPath, { force: true })
}

interface Turn {
  readonly id: string
  status: string
  readonly items: Array<Record<string, unknown>>
  readonly startedAt: number
  completedAt: number | null
  error: { message: string } | null
  /** Fixture-only: the turn/start params beyond input/threadId. */
  readonly overrides?: Record<string, unknown>
}

interface ThreadSettings {
  model: string
  effort: string | null
  approvalPolicy: unknown
  sandboxType: string
  mode: string
}

interface Thread {
  readonly id: string
  readonly cwd: string
  readonly turns: Array<Turn>
  readonly settings: ThreadSettings
  // "Usually the first user message in the thread, if available" — set at
  // the first turn/start, like the real rollout history (ATC-155).
  preview: string
  // Epoch millis before which reads report an empty preview (the real
  // rollout flush lag; FAKE_CODEX_PREVIEW_DELAY_MS).
  previewReadableAt: number
  archived: boolean
}

interface Child {
  readonly parentId: string
  status: "active" | "needsInput" | "idle"
}

const threads = new Map<string, Thread>()
const children = new Map<string, Child>()
const sockets = new Set<import("bun").ServerWebSocket<unknown>>()
const hangingTurns = new Set<string>()
const pendingServerRequests = new Map<number, (reply: unknown) => void>()
let nextServerRequestId = 1000

const broadcast = (method: string, params: unknown) => {
  const frame = JSON.stringify({ method, params })
  for (const socket of sockets) socket.send(frame)
}

const threadShape = (thread: Thread, cwd: string) => ({
  id: thread.id,
  cwd,
  status: { type: "idle" },
  turns: thread.turns,
})

const SANDBOX_TYPES: Record<string, string> = {
  "read-only": "readOnly",
  "workspace-write": "workspaceWrite",
  "danger-full-access": "dangerFullAccess",
}

/** The reply's top-level settings echo, as the real thread/start and thread/resume carry it. */
const settingsEcho = (thread: Thread) => ({
  model: thread.settings.model,
  reasoningEffort: thread.settings.effort,
  approvalPolicy: thread.settings.approvalPolicy,
  sandbox: { type: thread.settings.sandboxType },
})

const settingsUpdatedParams = (thread: Thread) => ({
  threadId: thread.id,
  threadSettings: {
    model: thread.settings.model,
    effort: thread.settings.effort,
    approvalPolicy: thread.settings.approvalPolicy,
    sandboxPolicy: { type: thread.settings.sandboxType },
    collaborationMode: { mode: thread.settings.mode },
  },
})

const FAKE_MODELS = [
  {
    id: "fake-sol",
    model: "fake-sol",
    displayName: "Fake Sol",
    description: "the default",
    hidden: false,
    isDefault: true,
    supportedReasoningEfforts: ["low", "medium", "high", "xhigh"].map((level) => ({
      reasoningEffort: level,
      description: level,
    })),
    defaultReasoningEffort: "medium",
  },
  {
    id: "fake-luna",
    model: "fake-luna",
    displayName: "Fake Luna",
    description: "smaller",
    hidden: false,
    isDefault: false,
    supportedReasoningEfforts: ["low", "medium"].map((level) => ({
      reasoningEffort: level,
      description: level,
    })),
    defaultReasoningEffort: "low",
  },
  {
    // A default the model does not itself list (seen on real catalogs):
    // the seam clamps it to the first listed level.
    id: "fake-terra",
    model: "fake-terra",
    displayName: "Fake Terra",
    description: "misreports its default",
    hidden: false,
    isDefault: false,
    supportedReasoningEfforts: ["low", "medium"].map((level) => ({
      reasoningEffort: level,
      description: level,
    })),
    defaultReasoningEffort: "high",
  },
  {
    id: "fake-hidden",
    model: "fake-hidden",
    displayName: "Hidden",
    description: "not listed",
    hidden: true,
    isDefault: false,
    supportedReasoningEfforts: [],
    defaultReasoningEffort: "low",
  },
]

const childStatus = (child: Child) =>
  child.status === "idle"
    ? { type: "idle" }
    : {
        type: "active",
        activeFlags: child.status === "needsInput" ? ["waitingOnApproval"] : [],
      }

const settleTurn = (thread: Thread, turnId: string, status: string) => {
  const turn = thread.turns.find((entry) => entry.id === turnId)
  if (turn === undefined) return
  turn.status = status
  turn.completedAt = Math.floor(Date.now() / 1000)
}

/** Broadcast one item lifecycle notification; completed items land on the
 * turn's history (what thread/resume replays). */
const emitItem = (
  thread: Thread,
  turnId: string,
  phase: "started" | "completed",
  item: Record<string, unknown>,
) => {
  broadcast(`item/${phase}`, { threadId: thread.id, turnId, item })
  if (phase === "completed") thread.turns.find((entry) => entry.id === turnId)?.items.push(item)
}

const agentMessage = (text: string) => ({
  id: crypto.randomUUID(),
  type: "agentMessage",
  text,
  phase: null,
})

const endTurn = (thread: Thread, turnId: string, status: "completed" | "interrupted") => {
  broadcast("thread/status/changed", { threadId: thread.id, status: { type: "idle" } })
  settleTurn(thread, turnId, status)
  broadcast("turn/completed", { threadId: thread.id, turn: { id: turnId, status } })
}

const finishTurn = (thread: Thread, turnId: string, text: string) => {
  emitItem(thread, turnId, "completed", agentMessage(`fake: ${text}`))
  endTurn(thread, turnId, "completed")
}

const handle = (
  socket: import("bun").ServerWebSocket<unknown>,
  message: {
    id?: number
    method?: string
    params?: Record<string, unknown>
    result?: unknown
    error?: unknown
  },
) => {
  const respond = (result: unknown) => socket.send(JSON.stringify({ id: message.id, result }))
  const respondError = (code: number, text: string) =>
    socket.send(JSON.stringify({ id: message.id, error: { code, message: text } }))

  // Responses to our server requests (approval/question round trips): any
  // answer, result or error, unblocks the pending turn with its payload.
  if (message.method === undefined && message.id !== undefined) {
    const pending = pendingServerRequests.get(message.id)
    if (pending !== undefined) {
      pendingServerRequests.delete(message.id)
      pending(message.result)
    }
    return
  }

  const params = message.params ?? {}
  switch (message.method) {
    case "initialize":
      return respond({ userAgent: `fake-codex-app-server/${version}` })
    case "initialized":
      return
    case "thread/start": {
      const cwd = String(params["cwd"] ?? "/")
      const thread: Thread = {
        id: crypto.randomUUID(),
        cwd,
        turns: [],
        settings: {
          model: String(params["model"] ?? "fake-sol"),
          // Like the real reply: a fresh thread echoes its model's default effort.
          effort:
            FAKE_MODELS.find((model) => model.model === String(params["model"] ?? "fake-sol"))
              ?.defaultReasoningEffort ?? null,
          approvalPolicy: params["approvalPolicy"] ?? "on-request",
          sandboxType:
            SANDBOX_TYPES[String(params["sandbox"] ?? "workspace-write")] ?? "workspaceWrite",
          mode: "default",
        },
        preview: "",
        previewReadableAt: 0,
        archived: false,
      }
      threads.set(thread.id, thread)
      const reported = wrongCwd === "start" ? `${cwd}-wrong` : cwd
      respond({ thread: threadShape(thread, reported), ...settingsEcho(thread) })
      return broadcast("thread/started", { thread: { id: thread.id, cwd: reported } })
    }
    case "thread/resume": {
      const threadId = String(params["threadId"] ?? "")
      const thread = threads.get(threadId)
      if (thread === undefined) {
        return respondError(-32600, `no rollout found for thread id ${threadId}`)
      }
      const reported = wrongCwd === "resume" ? `${thread.cwd}-wrong` : thread.cwd
      // Like the real reply, thread/resume carries the full turn history.
      return respond({
        thread: {
          ...threadShape(thread, reported),
          turns: thread.turns.map((turn) => ({
            id: turn.id,
            items: turn.items,
            status: turn.status,
            error: turn.error,
            startedAt: turn.startedAt,
            completedAt: turn.completedAt,
            // Fixture-only: what turn/start received (settings assertions).
            overrides: turn.overrides,
          })),
        },
        ...settingsEcho(thread),
      })
    }
    case "thread/settings/update": {
      const thread = threads.get(String(params["threadId"] ?? ""))
      if (thread === undefined) return respondError(-32600, "unknown thread")
      if (typeof params["model"] === "string") thread.settings.model = params["model"]
      if ("effort" in params) thread.settings.effort = (params["effort"] as string | null) ?? null
      if ("approvalPolicy" in params) thread.settings.approvalPolicy = params["approvalPolicy"]
      if (typeof params["sandboxType"] === "string")
        thread.settings.sandboxType = params["sandboxType"]
      if (typeof params["mode"] === "string") thread.settings.mode = params["mode"]
      respond({})
      return broadcast("thread/settings/updated", settingsUpdatedParams(thread))
    }
    case "model/list": {
      // Paginated like the real reply: one model per page under
      // FAKE_CODEX_MODEL_PAGED, else everything at once.
      if (process.env["FAKE_CODEX_MODEL_PAGED"] !== "1") {
        return respond({ data: FAKE_MODELS, nextCursor: null })
      }
      const start = Number.parseInt(String(params["cursor"] ?? "0"), 10)
      const next = start + 1
      return respond({
        data: FAKE_MODELS.slice(start, next),
        nextCursor: next < FAKE_MODELS.length ? String(next) : null,
      })
    }
    case "thread/unsubscribe":
      return respond({ status: "unsubscribed" })
    case "thread/archive": {
      const thread = threads.get(String(params["threadId"] ?? ""))
      if (thread === undefined) return respondError(-32600, "unknown thread")
      thread.archived = true
      return respond({})
    }
    case "thread/loaded/list":
      // Loaded = everything this fixture has in memory; descendants
      // included (the real server keeps running subagents loaded).
      return respond({ data: [...threads.keys(), ...children.keys()], nextCursor: null })
    case "thread/read": {
      const id = String(params["threadId"] ?? "")
      const thread = threads.get(id)
      if (thread !== undefined) {
        // The real server rejects includeTurns for paginated-history
        // threads — the TUI-created kind ATC observes (probed 2026-08-10).
        if (params["includeTurns"] === true) {
          return respondError(
            -32600,
            "paginated threads do not support thread/read(includeTurns=true)",
          )
        }
        const reply = {
          thread: {
            id,
            cwd: thread.cwd,
            parentThreadId: null,
            preview: Date.now() >= thread.previewReadableAt ? thread.preview : "",
            status: hangingTurns.has(id) ? { type: "active", activeFlags: [] } : { type: "idle" },
          },
        }
        const delay = Number.parseInt(process.env["FAKE_CODEX_READ_DELAY_MS"] ?? "0", 10)
        if (delay > 0) {
          setTimeout(() => respond(reply), delay)
          return
        }
        return respond(reply)
      }
      const child = children.get(id)
      if (child === undefined) return respondError(-32600, `unknown thread ${id}`)
      return respond({
        thread: { id, parentThreadId: child.parentId, status: childStatus(child) },
      })
    }
    case "test/child/vanish": {
      // The missed-idle-broadcast scenario: the child finishes and unloads
      // while nobody hears about it (only reconciliation can notice).
      if (!children.delete(String(params["threadId"] ?? ""))) {
        return respondError(-32600, "unknown child")
      }
      return respond({})
    }
    case "test/child/finish": {
      const child = children.get(String(params["threadId"] ?? ""))
      if (child === undefined) return respondError(-32600, "unknown child")
      child.status = "idle"
      respond({})
      return broadcast("thread/status/changed", {
        threadId: String(params["threadId"]),
        status: { type: "idle" },
      })
    }
    case "thread/list": {
      // Real reply shape is paginated: { data, nextCursor } with nextCursor
      // null on the last page, and the populations are disjoint: archived
      // threads appear only when `archived: true`. One thread per page
      // forces clients to walk.
      const all = [...threads.values()].filter(
        (thread) => thread.archived === (params["archived"] === true),
      )
      const offset = Number.parseInt(String(params["cursor"] ?? "0"), 10) || 0
      return respond({
        data: all.slice(offset, offset + 1).map((thread) => ({
          id: thread.id,
          cwd: thread.cwd,
          status: hangingTurns.has(thread.id)
            ? { type: "active", activeFlags: [] }
            : { type: "idle" },
          turns: thread.turns,
        })),
        nextCursor: offset + 1 < all.length ? String(offset + 1) : null,
      })
    }
    case "turn/start": {
      const threadId = String(params["threadId"] ?? "")
      const thread = threads.get(threadId)
      if (thread === undefined) return respondError(-32600, `unknown thread ${threadId}`)
      const input = (params["input"] ?? []) as Array<{
        type: string
        text?: string
        path?: string
      }>
      const text = input
        .flatMap((block) => (block.type === "text" && block.text !== undefined ? [block.text] : []))
        .join("\n")
      const turnId = crypto.randomUUID()
      // Overrides are sticky for this and subsequent turns, like the real server.
      if (typeof params["model"] === "string") thread.settings.model = params["model"]
      if (typeof params["effort"] === "string") thread.settings.effort = params["effort"]
      if ("approvalPolicy" in params) thread.settings.approvalPolicy = params["approvalPolicy"]
      const sandboxPolicy = params["sandboxPolicy"] as { type?: string } | undefined
      if (typeof sandboxPolicy?.type === "string") thread.settings.sandboxType = sandboxPolicy.type
      const collaboration = params["collaborationMode"] as { mode?: string } | undefined
      if (typeof collaboration?.mode === "string") thread.settings.mode = collaboration.mode
      const { input: _input, threadId: _threadId, ...overrides } = params
      thread.turns.push({
        id: turnId,
        status: "inProgress",
        items: [],
        startedAt: Math.floor(Date.now() / 1000),
        completedAt: null,
        error: null,
        overrides,
      })
      if (thread.preview === "") {
        thread.preview = text
        const flushLag = Number.parseInt(process.env["FAKE_CODEX_PREVIEW_DELAY_MS"] ?? "0", 10)
        thread.previewReadableAt = Date.now() + flushLag
      }
      respond({ turn: { id: turnId } })
      broadcast("thread/status/changed", {
        threadId,
        status: { type: "active", activeFlags: [] },
      })
      broadcast("turn/started", { threadId, turn: { id: turnId } })
      // The real server broadcasts the turn's input back as a completed
      // userMessage item — content text blocks, unlike agentMessage's
      // top-level text — the retention evidence for title refinement.
      // Image inputs come back as localImage blocks, like the real server.
      emitItem(thread, turnId, "completed", {
        id: crypto.randomUUID(),
        type: "userMessage",
        content: input.map((block) =>
          block.type === "localImage"
            ? { type: "localImage", path: block.path }
            : { type: "text", text: block.text ?? "", text_elements: [] },
        ),
      })
      if (text.includes("HANG")) {
        hangingTurns.add(threadId)
        return
      }
      if (text.includes("STREAM")) {
        const item = agentMessage("")
        emitItem(thread, turnId, "started", item)
        for (const delta of ["fake: ", text]) {
          broadcast("item/agentMessage/delta", { threadId, turnId, itemId: item.id, delta })
        }
        emitItem(thread, turnId, "completed", { ...item, text: `fake: ${text}` })
        return endTurn(thread, turnId, "completed")
      }
      if (text.includes("COMMAND")) {
        const item = {
          id: crypto.randomUUID(),
          type: "commandExecution",
          command: "pwd",
          cwd: thread.cwd,
          status: "inProgress",
          aggregatedOutput: null,
          exitCode: null,
          durationMs: null,
        }
        emitItem(thread, turnId, "started", item)
        emitItem(thread, turnId, "completed", {
          ...item,
          status: "completed",
          aggregatedOutput: `${thread.cwd}\n`,
          exitCode: 0,
          durationMs: 12,
        })
        return finishTurn(thread, turnId, text)
      }
      if (text.includes("QUESTION")) {
        const requestId = nextServerRequestId++
        pendingServerRequests.set(requestId, (reply) => {
          const answers = (reply as { answers?: unknown } | undefined)?.answers ?? "rejected"
          emitItem(thread, turnId, "completed", agentMessage(`answers: ${JSON.stringify(answers)}`))
          endTurn(thread, turnId, "completed")
        })
        socket.send(
          JSON.stringify({
            id: requestId,
            method: "item/tool/requestUserInput",
            params: {
              threadId,
              turnId,
              itemId: crypto.randomUUID(),
              questions: [
                {
                  id: "color",
                  header: "Color",
                  question: "Which color?",
                  isOther: false,
                  isSecret: false,
                  options: [
                    { label: "red", description: "warm" },
                    { label: "blue", description: "cool" },
                  ],
                },
              ],
              isBlocking: true,
              autoResolutionMs: null,
            },
          }),
        )
        return broadcast("thread/status/changed", {
          threadId,
          status: { type: "active", activeFlags: ["waitingOnUserInput"] },
        })
      }
      if (text.includes("SPAWN")) {
        const childId = `${threadId}-child-${children.size + 1}`
        const child: Child = {
          parentId: threadId,
          status: text.includes("NEEDSINPUT") ? "needsInput" : "active",
        }
        children.set(childId, child)
        if (!text.includes("SILENT")) {
          broadcast("item/started", {
            threadId,
            turnId,
            item: {
              type: "subAgentActivity",
              kind: "started",
              id: crypto.randomUUID(),
              agentThreadId: childId,
              agentPath: "/root/child",
            },
          })
          broadcast("thread/status/changed", { threadId: childId, status: childStatus(child) })
        }
        return finishTurn(thread, turnId, text)
      }
      if (text.includes("APPROVAL")) {
        const item = {
          id: crypto.randomUUID(),
          type: "commandExecution",
          command: "/bin/sh -c pwd",
          cwd: thread.cwd,
          status: "inProgress",
          aggregatedOutput: null,
          exitCode: null,
          durationMs: null,
        }
        emitItem(thread, turnId, "started", item)
        const requestId = nextServerRequestId++
        pendingServerRequests.set(requestId, (reply) => {
          const decision = (reply as { decision?: unknown } | undefined)?.decision
          broadcast("thread/status/changed", {
            threadId,
            status: { type: "active", activeFlags: [] },
          })
          if (decision === "accept" || decision === "acceptForSession") {
            emitItem(thread, turnId, "completed", {
              ...item,
              status: "completed",
              aggregatedOutput: `${thread.cwd}\n`,
              exitCode: 0,
              durationMs: 8,
            })
            return finishTurn(thread, turnId, text)
          }
          emitItem(thread, turnId, "completed", { ...item, status: "declined" })
          return decision === "cancel"
            ? endTurn(thread, turnId, "interrupted")
            : finishTurn(thread, turnId, text)
        })
        socket.send(
          JSON.stringify({
            id: requestId,
            method: "item/commandExecution/requestApproval",
            params: {
              threadId,
              turnId,
              itemId: item.id,
              command: item.command,
              cwd: thread.cwd,
              reason: "needs network",
            },
          }),
        )
        return broadcast("thread/status/changed", {
          threadId,
          status: { type: "active", activeFlags: ["waitingOnApproval"] },
        })
      }
      return finishTurn(thread, turnId, text)
    }
    case "skills/list": {
      // One entry per asked cwd: an enabled skill and a disabled one.
      const cwds = (params["cwds"] as Array<string> | undefined) ?? []
      return respond({
        data: cwds.map((cwd) => ({
          cwd,
          errors: [],
          skills: [
            {
              name: "review",
              description: "Review the current diff carefully",
              shortDescription: "Review the diff",
              enabled: true,
              path: "/skills/review/SKILL.md",
              scope: "user",
            },
            {
              name: "hidden",
              description: "A disabled skill",
              enabled: false,
              path: "/skills/hidden/SKILL.md",
              scope: "user",
            },
          ],
        })),
      })
    }
    case "turn/steer": {
      const threadId = String(params["threadId"] ?? "")
      const expectedTurnId = String(params["expectedTurnId"] ?? "")
      const thread = threads.get(threadId)
      if (thread === undefined) return respondError(-32600, `unknown thread ${threadId}`)
      const active = thread.turns.find((turn) => turn.status === "inProgress")
      if (active === undefined || active.id !== expectedTurnId) {
        return respondError(-32600, `turn ${expectedTurnId} is not the active turn`)
      }
      const input = (params["input"] ?? []) as Array<{
        type: string
        text?: string
        path?: string
      }>
      respond({ turnId: active.id })
      // The real server echoes the steer as the turn's next userMessage item.
      return emitItem(thread, active.id, "completed", {
        id: crypto.randomUUID(),
        type: "userMessage",
        content: input.map((block) =>
          block.type === "localImage"
            ? { type: "localImage", path: block.path }
            : { type: "text", text: block.text ?? "", text_elements: [] },
        ),
      })
    }
    case "turn/interrupt": {
      const threadId = String(params["threadId"] ?? "")
      const turnId = String(params["turnId"] ?? "")
      const thread = threads.get(threadId)
      if (thread === undefined) return respondError(-32600, `unknown thread ${threadId}`)
      respond({})
      hangingTurns.delete(threadId)
      broadcast("thread/status/changed", { threadId, status: { type: "idle" } })
      settleTurn(thread, turnId, "interrupted")
      return broadcast("turn/completed", { threadId, turn: { id: turnId, status: "interrupted" } })
    }
    default:
      if (message.id !== undefined) return respondError(-32601, `unknown method ${message.method}`)
  }
}

Bun.serve({
  unix: socketPath,
  fetch(request, server) {
    if (server.upgrade(request)) return undefined
    return new Response("not found", { status: 404 })
  },
  websocket: {
    open(socket) {
      sockets.add(socket)
    },
    close(socket) {
      sockets.delete(socket)
    },
    message(socket, raw) {
      handle(socket, JSON.parse(String(raw)) as Parameters<typeof handle>[1])
    },
  },
})
