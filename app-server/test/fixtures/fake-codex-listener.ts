// Fixture stand-in for `codex app-server --listen ws://127.0.0.1:<port>`:
// binds the requested port, answers /readyz, and speaks the JSON-RPC subset
// the codex adapter uses (initialize, thread/start, thread/resume,
// turn/start, turn/interrupt, thread/unsubscribe), broadcasting
// notifications to every connected socket like the real shared app-server.
//
// Turn behavior is keyed off the input text: containing "HANG" leaves the
// turn active until interrupted; containing "APPROVAL" first round-trips an
// item/commandExecution/requestApproval server request.
//
// Env switches (baked into the wrapper script by tests):
//   FAKE_CODEX_MODE       "ok" (default) | "never-ready" (503 /readyz)
//   FAKE_CODEX_WRONG_CWD  "start" | "resume" — lie about cwd in that reply
//   FAKE_CODEX_VERSION    printed for `--version` (default 0.146.0)
//   FAKE_CODEX_PID_FILE   record this pid for reap assertions
//   FAKE_CODEX_TTL_MS     self-exit backstop (default 120s) so a test that
//                         fails to stop() a detached fixture cannot leak it

if (process.argv.includes("--version")) {
  console.log(`codex-cli ${process.env["FAKE_CODEX_VERSION"] ?? "0.146.0"}`)
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
}

const threads = new Map<string, Thread>()
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

const finishTurn = (thread: Thread, turnId: string, text: string) => {
  broadcast("item/completed", {
    threadId: thread.id,
    turnId,
    item: { id: crypto.randomUUID(), type: "agentMessage", text: `fake: ${text}` },
  })
  broadcast("thread/status/changed", { threadId: thread.id, status: { type: "idle" } })
  thread.turns.push({ id: turnId, status: "completed" })
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
      const thread: Thread = { id: crypto.randomUUID(), cwd, turns: [] }
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
    case "turn/start": {
      const threadId = String(params["threadId"] ?? "")
      const thread = threads.get(threadId)
      if (thread === undefined) return respondError(-32600, `unknown thread ${threadId}`)
      const input = params["input"] as Array<{ text?: string }> | undefined
      const text = input?.[0]?.text ?? ""
      const turnId = crypto.randomUUID()
      respond({ turn: { id: turnId } })
      broadcast("thread/status/changed", {
        threadId,
        status: { type: "active", activeFlags: [] },
      })
      broadcast("turn/started", { threadId, turn: { id: turnId } })
      if (text.includes("HANG")) {
        hangingTurns.add(threadId)
        return
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
      thread.turns.push({ id: turnId, status: "interrupted" })
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
