import { existsSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll, describe, expect, test } from "vitest"
import { sessionNameForTerminalId } from "../src/terminalAdapter.ts"
import {
  compiledBinaryPath,
  freePort,
  isolatedEnv,
  makeTerminal,
  openSocket,
  spawnServe,
  waitForHealth,
  waitUntil,
} from "./blackbox.ts"

// Opt-in restart-recovery tests (mise run test:zmx): the compiled atc
// executable against the real installed zmx. Terminals must survive a server
// restart (the zmx sessions outlive the process), reconciliation must adopt
// the surviving sessions and clean orphans, and the WebSocket transport must
// work against a recovered session.

const enabled = process.env["ATC_ZMX_SMOKE"] === "1"

const scratch = mkdtempSync(join(tmpdir(), "atc-recovery-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

const zmxList = (socketDir: string): string =>
  Bun.spawnSync(["zmx", "list"], {
    env: { ...process.env, ZMX_DIR: socketDir, ZMX_SESSION: undefined },
  }).stdout.toString()

describe.skipIf(!enabled)("terminal restart recovery (compiled, real zmx)", () => {
  test("terminals survive a restart; orphans are cleaned; attach works after recovery", async () => {
    expect(
      existsSync(compiledBinaryPath),
      `missing compiled artifact at ${compiledBinaryPath}; run \`mise run build\``,
    ).toBe(true)
    expect(Bun.which("zmx"), "zmx not found on PATH").not.toBeNull()

    const port = await freePort()
    const env = isolatedEnv(scratch, { ATC_PORT: String(port) })
    const socketDir = join(env.XDG_STATE_HOME, "atc", "terminals")
    const base = `http://127.0.0.1:${port}`

    let server = spawnServe([compiledBinaryPath], port, scratch, env)
    try {
      await waitForHealth(base, server)
      const terminal = await makeTerminal(base)
      const sessionName = sessionNameForTerminalId(terminal["id"] ?? "")
      expect(zmxList(socketDir)).toContain(sessionName)

      // An orphan session in the private dir: provably ours, no record.
      Bun.spawnSync(["zmx", "run", "atc-orphan-test", "-d", "sleep", "300"], {
        env: { ...process.env, ZMX_DIR: socketDir },
      })
      await waitUntil(() => zmxList(socketDir).includes("atc-orphan-test"), "orphan session")

      // Restart: the session daemons must survive the server's death.
      server.kill("SIGTERM")
      expect(await server.exited).toBe(130)
      expect(zmxList(socketDir)).toContain(sessionName)

      server = spawnServe([compiledBinaryPath], port, scratch, env)
      await waitForHealth(base, server)

      // Startup reconciliation adopted the survivor and killed the orphan.
      const listed = (await (await fetch(`${base}/api/v1/terminals`)).json()) as Array<{
        id: string
        status: string
      }>
      expect(listed.map((t) => [t.id, t.status])).toEqual([[terminal["id"], "live"]])
      await waitUntil(() => !zmxList(socketDir).includes("atc-orphan-test"), "orphan cleanup")
      expect(zmxList(socketDir)).toContain(sessionName)

      // The transport works against the recovered session.
      const ws = await openSocket(
        `ws://127.0.0.1:${port}/api/v1/terminals/${terminal["id"]}/attach`,
      )
      ws.socket.send(new TextEncoder().encode("printf 'MARKER-%s\\n' recovered\n"))
      // 600 × 25ms — a real login shell can be slow to come up.
      await waitUntil(() => ws.received.join("").includes("MARKER-recovered"), "marker", 600)
      ws.socket.close()

      // Delete kills the real session and verifies absence.
      const deleted = await fetch(`${base}/api/v1/terminals/${terminal["id"]}`, {
        method: "DELETE",
      })
      expect(deleted.status).toBe(204)
      expect(zmxList(socketDir)).not.toContain(sessionName)
    } finally {
      server.kill()
      await server.exited
      // Best-effort: no zmx daemons may outlive the test.
      for (const line of zmxList(socketDir).split("\n")) {
        const name = /name=(\S+)/.exec(line)?.[1]
        if (name !== undefined) {
          Bun.spawnSync(["zmx", "kill", name], { env: { ...process.env, ZMX_DIR: socketDir } })
        }
      }
    }
  }, 120_000)
})
