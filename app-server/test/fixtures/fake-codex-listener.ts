// Fixture stand-in for `codex app-server --listen ws://127.0.0.1:<port>`:
// binds the requested port, answers /readyz, and speaks the JSON-RPC subset
// the codex adapter uses (initialize, thread/start, thread/resume,
// turn/start, turn/interrupt, thread/unsubscribe), broadcasting
// notifications to every connected socket like the real shared app-server.
//
// Turn behavior is keyed off the input text: containing "HANG" leaves the
// turn active until interrupted; containing "APPROVAL" first round-trips an
// item/commandExecution/requestApproval server request; containing "SPAWN"
// creates a descendant thread (`<threadId>-child-N`) that stays active
// after the parent's turn completes — mirroring the probed real behavior
// (experiments/subagent-activity): a subAgentActivity item on the parent
// plus child thread/status/changed broadcasts, NO thread/started, and no
// thread/list entry. "SPAWN SILENT" skips the spawn broadcasts (the
// reconnect-reconciliation scenario: state exists server-side only);
// "NEEDSINPUT" makes the child wait on approval. The test-only
// "test/child/finish" request completes a child.
//
// Env switches (baked into the wrapper script by tests):
//   FAKE_CODEX_MODE       "ok" (default) | "never-ready" (503 /readyz)
//   FAKE_CODEX_WRONG_CWD  "start" | "resume" — lie about cwd in that reply
//   FAKE_CODEX_VERSION    printed for `--version` (default 0.146.0)
//   FAKE_CODEX_PID_FILE   record this pid for reap assertions
//   FAKE_CODEX_TTL_MS     self-exit backstop (default 120s) so a test that
//                         fails to stop() a detached fixture cannot leak it
//   FAKE_CODEX_READ_DELAY_MS  delay root thread/read replies, so tests can
//                         prove activity never waits on the ATC-155
//                         prompt (preview) read
//   FAKE_CODEX_PREVIEW_DELAY_MS  make preview readable only this long
//                         after the first turn starts (the real rollout
//                         flush lag), so tests can prove the ATC-155
//                         discovery loop lands mid-turn

if (process.argv.includes("--version")) {
  console.log(`codex-cli ${process.env["FAKE_CODEX_VERSION"] ?? "0.146.0"}`)
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

const listenArg = process.argv.find((arg) => arg.startsWith("ws://"))
if (listenArg === undefined) {
  console.error("fake-codex-listener: no ws:// listen argument")
  process.exit(64)
}
const port = Number.parseInt(new URL(listenArg).port, 10)
const mode = process.env["FAKE_CODEX_MODE"] ?? "ok"
const wrongCwd = process.env["FAKE_CODEX_WRONG_CWD"]
const ttl = Number.parseInt(process.env["FAKE_CODEX_TTL_MS"] ?? "120000", 10)
const pidFile = process.env["FAKE_CODEX_PID_FILE"]
if (pidFile !== undefined && pidFile !== "") {
  await Bun.write(pidFile, String(process.pid))
}

interface Thread {
  readonly id: string
  readonly cwd: string
  readonly turns: Array<{ id: string; status: string }>
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
const pendingServerRequests = new Map<number, () => void>()
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

const childStatus = (child: Child) =>
  child.status === "idle"
    ? { type: "idle" }
    : {
        type: "active",
        activeFlags: child.status === "needsInput" ? ["waitingOnApproval"] : [],
      }

const settleTurn = (thread: Thread, turnId: string, status: string) => {
  const turn = thread.turns.find((entry) => entry.id === turnId)
  if (turn !== undefined) turn.status = status
}

const finishTurn = (thread: Thread, turnId: string, text: string) => {
  broadcast("item/completed", {
    threadId: thread.id,
    turnId,
    item: { id: crypto.randomUUID(), type: "agentMessage", text: `fake: ${text}` },
  })
  broadcast("thread/status/changed", { threadId: thread.id, status: { type: "idle" } })
  settleTurn(thread, turnId, "completed")
  broadcast("turn/completed", { threadId: thread.id, turn: { id: turnId, status: "completed" } })
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

  // Responses to our server requests (approval round trip): any answer,
  // result or error, unblocks the pending turn.
  if (message.method === undefined && message.id !== undefined) {
    const pending = pendingServerRequests.get(message.id)
    if (pending !== undefined) {
      pendingServerRequests.delete(message.id)
      pending()
    }
    return
  }

  const params = message.params ?? {}
  switch (message.method) {
    case "initialize":
      return respond({ userAgent: "fake-codex-app-server" })
    case "initialized":
      return
    case "thread/start": {
      const cwd = String(params["cwd"] ?? "/")
      const thread: Thread = {
        id: crypto.randomUUID(),
        cwd,
        turns: [],
        preview: "",
        previewReadableAt: 0,
        archived: false,
      }
      threads.set(thread.id, thread)
      const reported = wrongCwd === "start" ? `${cwd}-wrong` : cwd
      respond({ thread: threadShape(thread, reported) })
      return broadcast("thread/started", { thread: { id: thread.id, cwd: reported } })
    }
    case "thread/resume": {
      const threadId = String(params["threadId"] ?? "")
      const thread = threads.get(threadId)
      if (thread === undefined) {
        return respondError(-32600, `no rollout found for thread id ${threadId}`)
      }
      const reported = wrongCwd === "resume" ? `${thread.cwd}-wrong` : thread.cwd
      return respond({ thread: threadShape(thread, reported) })
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
      const input = params["input"] as Array<{ text?: string }> | undefined
      const text = input?.[0]?.text ?? ""
      const turnId = crypto.randomUUID()
      thread.turns.push({ id: turnId, status: "inProgress" })
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
      broadcast("item/completed", {
        threadId,
        turnId,
        item: { id: crypto.randomUUID(), type: "userMessage", content: [{ type: "text", text }] },
      })
      if (text.includes("HANG")) {
        hangingTurns.add(threadId)
        return
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
        const requestId = nextServerRequestId++
        pendingServerRequests.set(requestId, () => {
          broadcast("thread/status/changed", {
            threadId,
            status: { type: "active", activeFlags: [] },
          })
          finishTurn(thread, turnId, text)
        })
        socket.send(
          JSON.stringify({
            id: requestId,
            method: "item/commandExecution/requestApproval",
            params: { threadId, turnId, command: "/bin/sh -c pwd", cwd: thread.cwd },
          }),
        )
        return broadcast("thread/status/changed", {
          threadId,
          status: { type: "active", activeFlags: ["waitingOnApproval"] },
        })
      }
      return finishTurn(thread, turnId, text)
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
  hostname: "127.0.0.1",
  port,
  fetch(request, server) {
    const url = new URL(request.url)
    if (url.pathname === "/readyz") {
      return mode === "ok"
        ? new Response("ok", { status: 200 })
        : new Response("not ready", { status: 503 })
    }
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

setTimeout(() => process.exit(0), ttl)
