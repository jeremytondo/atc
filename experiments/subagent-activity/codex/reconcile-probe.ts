// ATC-158 live reconciliation probe: while a spawned descendant is running
// and its parent is already idle, can a FRESH connection (reconnect
// stand-in) rebuild the descendant graph via thread/loaded/list +
// thread/read? Output: codex/reconcile-probe.jsonl.
import { appendFileSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import * as path from "node:path"

const OUT = new URL("./reconcile-probe.jsonl", import.meta.url).pathname
writeFileSync(OUT, "")
const started = Date.now()
const record = (kind: string, data: unknown): void => {
  appendFileSync(OUT, `${JSON.stringify({ at: Date.now() - started, kind, data })}\n`)
  console.log(`[${Date.now() - started}ms] ${kind} ${JSON.stringify(data).slice(0, 300)}`)
}

const port = 21000 + Math.floor(Math.random() * 20000)
const url = `ws://127.0.0.1:${port}`
const cwd = mkdtempSync(path.join(tmpdir(), "atc-158-codex-rec-"))
const server = Bun.spawn({
  cmd: ["codex", "app-server", "--listen", url],
  stdout: "ignore",
  stderr: "ignore",
})

interface Client {
  request: (method: string, params: Record<string, unknown>) => Promise<unknown>
  close: () => void
}

const notifications: Array<{ at: number; method: string; params: unknown }> = []

const connect = (name: string, recordNotifications: boolean): Promise<Client> =>
  new Promise((resolve, reject) => {
    const socket = new WebSocket(url)
    const pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()
    let nextId = 1
    socket.onopen = async () => {
      const request = (method: string, params: Record<string, unknown>): Promise<unknown> =>
        new Promise((res, rej) => {
          const id = nextId++
          pending.set(id, { resolve: res, reject: rej })
          socket.send(JSON.stringify({ id, method, params }))
          setTimeout(() => {
            if (pending.delete(id)) rej(new Error(`${method} timed out`))
          }, 30_000)
        })
      await request("initialize", {
        clientInfo: { name: `atc-158-${name}`, title: "probe", version: "0.0.1" },
        capabilities: null,
      })
      socket.send(JSON.stringify({ method: "initialized", params: {} }))
      resolve({ request, close: () => socket.close() })
    }
    socket.onerror = () => reject(new Error("connect failed"))
    socket.onmessage = (event) => {
      const message = JSON.parse(String(event.data)) as {
        id?: number
        method?: string
        params?: Record<string, unknown>
        result?: unknown
        error?: { message?: string } | null
      }
      if (message.id !== undefined && message.method === undefined) {
        const entry = pending.get(message.id)
        if (entry === undefined) return
        pending.delete(message.id)
        if (message.error != null) entry.reject(new Error(message.error.message))
        else entry.resolve(message.result ?? {})
        return
      }
      if (message.id !== undefined && message.method !== undefined) {
        socket.send(
          JSON.stringify({ id: message.id, error: { code: -32601, message: "probe rejects" } }),
        )
        return
      }
      if (recordNotifications && message.method !== undefined) {
        const interesting =
          message.method.startsWith("thread/") ||
          message.method.startsWith("turn/") ||
          (message.method.startsWith("item/") && !message.method.endsWith("/delta"))
        if (interesting) {
          notifications.push({ at: Date.now() - started, method: message.method, params: message.params })
          record(`notify:${message.method}`, message.params)
        }
      }
    }
  })

for (let attempt = 0; ; attempt++) {
  try {
    const test = await connect("ping", false)
    test.close()
    break
  } catch {
    if (attempt > 40) throw new Error("server never came up")
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
}

try {
  const writer = await connect("writer", true)
  const threadReply = (await writer.request("thread/start", {
    cwd,
    approvalPolicy: "never",
    sandbox: "workspace-write",
  })) as { thread?: { id?: string } }
  const rootId = threadReply.thread?.id as string
  record("root-thread", { rootId })
  await writer.request("turn/start", {
    threadId: rootId,
    input: [
      {
        type: "text",
        text:
          "Use your multi-agent/collaboration tools to spawn ONE subagent whose task is exactly: " +
          'run the shell command `sleep 25` and then reply CHILD-DONE. ' +
          "Do NOT wait for the subagent, do not check on it, and do not close it: " +
          "the moment it is spawned, end your turn by replying exactly SPAWNED.",
      },
    ],
  })

  // Wait until the parent's turn completes AND a descendant showed up, then
  // reconcile from a fresh connection while the descendant runs.
  const deadline = Date.now() + 60_000
  let childId: string | undefined
  let parentTurnDone = false
  while (Date.now() < deadline && (childId === undefined || !parentTurnDone)) {
    await new Promise((resolve) => setTimeout(resolve, 250))
    for (const n of notifications) {
      const params = n.params as Record<string, unknown>
      if (n.method === "item/started" || n.method === "item/completed") {
        const item = params["item"] as Record<string, unknown> | undefined
        if (item?.["type"] === "subAgentActivity" && typeof item["agentThreadId"] === "string") {
          childId = item["agentThreadId"] as string
        }
      }
      if (n.method === "turn/completed" && params["threadId"] === rootId) parentTurnDone = true
    }
  }
  record("window", { childId, parentTurnDone })

  const fresh = await connect("fresh", false)
  record("loaded-list", await fresh.request("thread/loaded/list", {}))
  if (childId !== undefined) {
    record("read-child-live", await fresh.request("thread/read", { threadId: childId }))
  }
  record("read-root-live", await fresh.request("thread/read", { threadId: rootId }))
  fresh.close()

  // After the child finishes, read again for the terminal shape.
  await new Promise((resolve) => setTimeout(resolve, 35_000))
  const fresh2 = await connect("fresh2", false)
  record("loaded-list-after", await fresh2.request("thread/loaded/list", {}))
  if (childId !== undefined) {
    record("read-child-after", await fresh2.request("thread/read", { threadId: childId }))
  }
  fresh2.close()
  writer.close()
} finally {
  process.kill(server.pid, "SIGKILL")
}
