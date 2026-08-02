import { afterAll, describe, expect, test } from "vitest"
import * as fs from "node:fs"
import * as path from "node:path"
import { sessionNameForTerminalId } from "../src/terminalAdapter.ts"
import { cleanupTempDirs, postJson, waitUntil } from "./blackbox.ts"
import { startFakeZmxServer } from "./testLayers.ts"

// The WebSocket attach bridge, black-box: a real `atc serve` process whose
// zmx executable is the fake-zmx wrapper, driven over loopback TCP and a
// real WebSocket. (In-process WS upgrades through the Bun adapter do not
// complete under the vitest runtime, and this shape exercises the real
// adapter + PTY + bridge end to end anyway.) Covers pre-upgrade rejection,
// byte bridging both ways, the resize control frame, and the close
// vocabulary. The opt-in zmxRecovery suite runs the same transport against
// real zmx.

afterAll(cleanupTempDirs)

const makeTerminal = async (base: string) => {
  const project = await postJson(base, "/api/v1/projects", {
    name: "P",
    defaultWorkingDirectory: "/tmp",
  })
  return postJson(base, "/api/v1/terminals", { projectId: project["id"] })
}

interface WsSession {
  readonly socket: WebSocket
  readonly received: Array<string>
  readonly closed: Promise<{ code: number; reason: string }>
}

const openSocket = (url: string): Promise<WsSession> =>
  new Promise((resolve, reject) => {
    const socket = new WebSocket(url)
    socket.binaryType = "arraybuffer"
    const received: Array<string> = []
    const decoder = new TextDecoder()
    const closed = new Promise<{ code: number; reason: string }>((resolveClose) => {
      socket.onclose = (event) => resolveClose({ code: event.code, reason: event.reason })
    })
    socket.onmessage = (event) => {
      if (typeof event.data !== "string") received.push(decoder.decode(event.data as ArrayBuffer))
    }
    socket.onopen = () => resolve({ socket, received, closed })
    socket.onerror = () => reject(new Error(`websocket failed to open ${url}`))
  })

describe("terminal attach bridge (black box)", () => {
  test("bridges bytes and control frames, closing 1000 terminal_ended on delete", async () => {
    const server = await startFakeZmxServer()
    try {
      const terminal = await makeTerminal(server.base)
      const ws = await openSocket(
        `${server.base.replace("http", "ws")}/api/v1/terminals/${terminal["id"]}/attach?cols=100&rows=30`,
      )

      // Client -> session -> client: the fake-zmx attach client echoes its
      // PTY input, so a round trip proves both directions through the real
      // adapter and bridge.
      await waitUntil(() => ws.received.join("").includes("attached"), "attach banner")
      ws.socket.send(new TextEncoder().encode("hello\n"))
      await waitUntil(() => ws.received.join("").includes("echo:hello"), "echo round trip")

      // Control frames are accepted (resize is a no-op for the fixture but
      // must not disturb the bridge).
      ws.socket.send(JSON.stringify({ type: "resize", cols: 132, rows: 43 }))
      ws.socket.send(new TextEncoder().encode("again\n"))
      await waitUntil(
        () => ws.received.join("").includes("echo:again"),
        "bridge alive after resize",
      )

      // Deleting the terminal kills the session; the bridge confirms
      // absence against a complete inventory and closes authoritatively.
      const deleted = await fetch(`${server.base}/api/v1/terminals/${terminal["id"]}`, {
        method: "DELETE",
      })
      expect(deleted.status).toBe(204)
      const closed = await ws.closed
      expect(closed.code).toBe(1000)
      expect(closed.reason).toBe("terminal_ended")
    } finally {
      server.proc.kill()
      await server.proc.exited
    }
  }, 30_000)

  test("rejects bad attaches before the upgrade with real HTTP statuses", async () => {
    const server = await startFakeZmxServer()
    try {
      // Unknown terminal: 404 before any upgrade.
      const missing = await fetch(`${server.base}/api/v1/terminals/nope/attach`)
      expect(missing.status).toBe(404)

      // Ended terminal: 410 (reconciliation wrote the tombstone after the
      // session died behind the server's back).
      const terminal = await makeTerminal(server.base)
      fs.rmSync(path.join(server.sandbox.stateDir, sessionNameForTerminalId(terminal["id"] ?? "")))
      const ended = await fetch(`${server.base}/api/v1/terminals/${terminal["id"]}/attach`)
      expect(ended.status).toBe(410)
      // The tombstone is now visible on the resource itself.
      const record = await fetch(`${server.base}/api/v1/terminals/${terminal["id"]}`)
      expect(((await record.json()) as { status: string }).status).toBe("ended")

      // A live terminal without an upgrade handshake: 426, no session touched.
      const second = await makeTerminal(server.base)
      const noUpgrade = await fetch(`${server.base}/api/v1/terminals/${second["id"]}/attach`)
      expect(noUpgrade.status).toBe(426)
    } finally {
      server.proc.kill()
      await server.proc.exited
    }
  }, 30_000)

  test("a session dying mid-attach closes 1000 only after confirmed absence", async () => {
    const server = await startFakeZmxServer()
    try {
      const terminal = await makeTerminal(server.base)
      const ws = await openSocket(
        `${server.base.replace("http", "ws")}/api/v1/terminals/${terminal["id"]}/attach`,
      )
      await waitUntil(() => ws.received.join("").includes("attached"), "attach banner")

      // The session dies out from under the bridge: the attach client sees
      // EOF and a complete inventory omits the session.
      fs.rmSync(path.join(server.sandbox.stateDir, sessionNameForTerminalId(terminal["id"] ?? "")))
      const closed = await ws.closed
      expect(closed.code).toBe(1000)
      expect(closed.reason).toBe("terminal_ended")

      const record = await fetch(`${server.base}/api/v1/terminals/${terminal["id"]}`)
      expect(((await record.json()) as { status: string }).status).toBe("ended")
    } finally {
      server.proc.kill()
      await server.proc.exited
    }
  }, 30_000)
})
