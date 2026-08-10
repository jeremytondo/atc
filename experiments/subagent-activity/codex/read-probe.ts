// ATC-158 follow-up probe (token-free): can thread/read and
// thread/loaded/list see a descendant thread that thread/list omits?
// Uses the persisted threads from record.ts's run (ids passed as argv).
import { appendFileSync, writeFileSync } from "node:fs"

const OUT = new URL("./read-probe.jsonl", import.meta.url).pathname
writeFileSync(OUT, "")
const record = (kind: string, data: unknown): void => {
  appendFileSync(OUT, `${JSON.stringify({ kind, data })}\n`)
  console.log(kind, JSON.stringify(data).slice(0, 400))
}

const [rootId, childId] = process.argv.slice(2)
const port = 21000 + Math.floor(Math.random() * 20000)
const url = `ws://127.0.0.1:${port}`
const server = Bun.spawn({
  cmd: ["codex", "app-server", "--listen", url],
  stdout: "ignore",
  stderr: "ignore",
})

const connect = (): Promise<{
  request: (method: string, params: Record<string, unknown>) => Promise<unknown>
  close: () => void
}> =>
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
          }, 20_000)
        })
      await request("initialize", {
        clientInfo: { name: "atc-158-read-probe", title: "probe", version: "0.0.1" },
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
        result?: unknown
        error?: { message?: string } | null
      }
      if (message.id !== undefined && message.method === undefined) {
        const entry = pending.get(message.id)
        if (entry === undefined) return
        pending.delete(message.id)
        if (message.error != null) entry.reject(new Error(message.error.message))
        else entry.resolve(message.result ?? {})
      }
    }
  })

for (let attempt = 0; ; attempt++) {
  try {
    const test = await connect()
    test.close()
    break
  } catch {
    if (attempt > 40) throw new Error("server never came up")
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
}

try {
  const client = await connect()
  for (const [label, method, params] of [
    ["loaded-list", "thread/loaded/list", {}],
    ["read-root", "thread/read", { threadId: rootId }],
    ["read-child", "thread/read", { threadId: childId }],
  ] as const) {
    try {
      record(label, await client.request(method, params as Record<string, unknown>))
    } catch (error) {
      record(`${label}-error`, String(error))
    }
  }
  client.close()
} finally {
  process.kill(server.pid, "SIGKILL")
}
