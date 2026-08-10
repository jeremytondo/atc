// ATC-158 Codex probe: record descendant-thread evidence on the app-server
// socket. Evidence sought:
//   (a) thread/started broadcasts for descendant (subagent) threads carry
//       parentThreadId;
//   (b) descendant thread/status/changed fans out without subscription
//       (observer socket sees it too);
//   (c) a fresh connection's thread/list walk reconstructs parentThreadId +
//       status for live descendants (reconnect reconciliation);
//   (d) whether the parent goes idle while a spawned descendant still runs.
// Output: codex/recording.jsonl — one {at, socket, kind, data} line per event.
import { appendFileSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import * as path from "node:path"

const OUT = new URL("./recording.jsonl", import.meta.url).pathname
writeFileSync(OUT, "")
const started = Date.now()
const record = (socket: string, kind: string, data: unknown): void => {
  appendFileSync(OUT, `${JSON.stringify({ at: Date.now() - started, socket, kind, data })}\n`)
  console.log(`[${Date.now() - started}ms] ${socket} ${kind}`)
}

const port = 21000 + Math.floor(Math.random() * 20000)
const url = `ws://127.0.0.1:${port}`
const cwd = mkdtempSync(path.join(tmpdir(), "atc-158-codex-"))

const server = Bun.spawn({
  cmd: ["codex", "app-server", "--listen", url],
  stdout: "ignore",
  stderr: "ignore",
})
const serverPid = server.pid
record("probe", "server-spawned", { pid: serverPid, url, cwd })

interface Client {
  readonly name: string
  readonly socket: WebSocket
  readonly request: (method: string, params: Record<string, unknown>) => Promise<unknown>
}

const connect = (name: string): Promise<Client> =>
  new Promise((resolve, reject) => {
    const socket = new WebSocket(url)
    const pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()
    let nextId = 1
    const request = (method: string, params: Record<string, unknown>): Promise<unknown> =>
      new Promise((res, rej) => {
        const id = nextId++
        pending.set(id, { resolve: res, reject: rej })
        socket.send(JSON.stringify({ id, method, params }))
        setTimeout(() => {
          if (pending.delete(id)) rej(new Error(`${method} timed out`))
        }, 30_000)
      })
    socket.onopen = async () => {
      try {
        const client: Client = { name, socket, request }
        await request("initialize", {
          clientInfo: { name: `atc-158-probe-${name}`, title: "ATC-158 probe", version: "0.0.1" },
          capabilities: null,
        })
        socket.send(JSON.stringify({ method: "initialized", params: {} }))
        resolve(client)
      } catch (error) {
        reject(error as Error)
      }
    }
    socket.onerror = () => reject(new Error(`could not connect ${name}`))
    socket.onmessage = (event) => {
      const raw = String(event.data)
      let message: {
        id?: number | string
        method?: string
        params?: Record<string, unknown>
        result?: unknown
        error?: { code?: number; message?: string } | null
      }
      try {
        message = JSON.parse(raw)
      } catch {
        record(name, "invalid-json", raw)
        return
      }
      if (message.id !== undefined && message.method === undefined) {
        record(name, "reply", message)
        const entry = pending.get(Number(message.id))
        if (entry !== undefined) {
          pending.delete(Number(message.id))
          if (message.error != null) entry.reject(new Error(message.error.message ?? "rpc error"))
          else entry.resolve(message.result ?? {})
        }
        return
      }
      if (message.id !== undefined && message.method !== undefined) {
        record(name, `server-request:${message.method}`, message)
        socket.send(
          JSON.stringify({
            id: message.id,
            error: { code: -32601, message: "atc-158 probe rejects provider requests" },
          }),
        )
        return
      }
      record(name, `notify:${message.method}`, message)
    }
  })

const waitForServer = async (): Promise<void> => {
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      await new Promise<void>((resolve, reject) => {
        const test = new WebSocket(url)
        test.onopen = () => {
          test.close()
          resolve()
        }
        test.onerror = () => reject(new Error("not up"))
      })
      return
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 500))
    }
  }
  throw new Error("codex app-server never came up")
}

const listThreads = async (client: Client): Promise<void> => {
  let cursor: string | undefined
  for (let page = 0; page < 20; page++) {
    const reply = (await client.request(
      "thread/list",
      cursor === undefined ? { archived: false } : { archived: false, cursor },
    )) as { data?: Array<Record<string, unknown>>; nextCursor?: string | null }
    record(client.name, "thread/list-page", reply)
    const next = reply.nextCursor
    if (typeof next !== "string" || next === cursor) return
    cursor = next
  }
}

try {
  await waitForServer()
  const writer = await connect("writer")
  const observer = await connect("observer")
  record("probe", "connected", {})

  const threadReply = (await writer.request("thread/start", {
    cwd,
    approvalPolicy: "never",
    sandbox: "workspace-write",
  })) as { thread?: { id?: string } }
  const rootId = threadReply.thread?.id
  record("probe", "root-thread", { rootId })

  await writer.request("turn/start", {
    threadId: rootId,
    input: [
      {
        type: "text",
        text:
          "Use your multi-agent/collaboration tools to spawn ONE subagent whose task is exactly: " +
          'run the shell command `sleep 15` and then reply CHILD-DONE. ' +
          "Do NOT wait for the subagent, do not check on it, and do not close it: " +
          "the moment it is spawned, end your turn by replying exactly SPAWNED.",
      },
    ],
  })
  record("probe", "turn-started", {})

  // Give the descendant time to appear and run, then reconcile from a fresh
  // connection while it is (hopefully) still active.
  await new Promise((resolve) => setTimeout(resolve, 25_000))
  const fresh = await connect("fresh")
  await listThreads(fresh)
  fresh.socket.close()

  // Let the descendant finish and record the trailing status transitions.
  await new Promise((resolve) => setTimeout(resolve, 30_000))
  const fresh2 = await connect("fresh2")
  await listThreads(fresh2)
  fresh2.socket.close()

  writer.socket.close()
  observer.socket.close()
  record("probe", "done", {})
} finally {
  process.kill(serverPid, "SIGKILL")
}
console.log(`recorded to ${OUT}`)
