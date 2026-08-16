import { expect } from "vitest"
import { mkdtempSync, rmSync } from "node:fs"
import { fileURLToPath } from "node:url"

// Shared helpers for black-box tests that spawn a real `atc serve` process —
// from source (serve.blackbox.test.ts) or from the compiled artifact
// (compiled.blackbox.test.ts) — and talk to it over loopback TCP.

export const appServerRoot = fileURLToPath(new URL("..", import.meta.url))

/** Deterministic host-target artifact path produced by `mise run build`. */
export const compiledBinaryPath =
  process.env["ATC_COMPILED_BIN"] ?? `${appServerRoot}dist/atc-${process.platform}-${process.arch}`

const tempDirs: Array<string> = []

/** Track `dir` for removal by `cleanupTempDirs`. */
export const trackTempDir = (dir: string): string => {
  tempDirs.push(dir)
  return dir
}

export const cleanupTempDirs = (): void => {
  for (const dir of tempDirs.splice(0)) rmSync(dir, { recursive: true, force: true })
}

// Unconditional: suites that allocate tracked dirs (isolatedEnv does it
// implicitly) must not leak them just because they forgot an afterAll.
process.on("exit", cleanupTempDirs)

/**
 * A throwaway directory short enough for the unix socket-path budget
 * (~103 bytes) — macOS os.tmpdir() (/var/folders/…) is too deep, /tmp is not.
 */
export const makeShortSocketDir = (): string => trackTempDir(mkdtempSync("/tmp/atc-zs-"))

/**
 * Environment pointing every settled location (config, data, state, Codex
 * home) away from the real user locations on the machine running the tests. The state
 * home gets its own short /tmp path rather than living under `dir`: the
 * terminal socket directory lives under it and must fit the unix
 * socket-path budget. Read the location back from the returned env when a
 * test needs to inspect state files.
 *
 * Every inherited ATC_* variable is stripped: environment beats config in
 * AppConfig precedence, and test runs launched from inside an ATC terminal
 * session inherit ATC_ENDPOINT (and friends) pointing at the developer's
 * real server — without scrubbing, blackbox CLI calls would create projects
 * and threads there instead of on the isolated test server. Tests state
 * their ATC_* configuration explicitly via `extra`.
 */
export const isolatedEnv = (dir: string, extra: Record<string, string> = {}) => ({
  ...Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("ATC_"))),
  XDG_CONFIG_HOME: `${dir}/config`,
  XDG_DATA_HOME: `${dir}/data`,
  XDG_STATE_HOME: trackTempDir(mkdtempSync("/tmp/atc-st-")),
  // A private Codex home: the shared app-server socket under the real one
  // belongs to the developer's Codex clients.
  CODEX_HOME: `${dir}/codex`,
  ...extra,
})

/** Spawn `<command> serve --port <port>`; `command` is the program plus any leading args. */
export const spawnServe = (
  command: ReadonlyArray<string>,
  port: number,
  cwd: string = appServerRoot,
  env: Record<string, string | undefined> = process.env,
) =>
  Bun.spawn([...command, "serve", "--port", String(port)], {
    cwd,
    env,
    stdout: "pipe",
    stderr: "pipe",
  })

/**
 * Run `<command> ...args` to completion, capturing stdout, stderr, and exit
 * code. `cwd` is required: compiled-artifact tests must run outside the
 * repository, so no caller should silently inherit the repo cwd. `stdin`
 * (when given) is fed to the child and closed.
 */
export const runCli = async (
  command: ReadonlyArray<string>,
  args: ReadonlyArray<string>,
  cwd: string,
  env: Record<string, string | undefined> = process.env,
  stdin?: string,
) => {
  const proc = Bun.spawn([...command, ...args], {
    cwd,
    env,
    stdout: "pipe",
    stderr: "pipe",
    ...(stdin === undefined ? {} : { stdin: Buffer.from(stdin) }),
  })
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  return { stdout, stderr, exitCode }
}

// Bind-then-release to pick a port for the child. Racy in principle (another
// process could grab it in between — Linux hands the just-released port to
// the next bind(0) LIFO, so concurrent spawns make the race real, not
// theoretical), but --port 0 is deliberately rejected by validation, so this
// is the practical option. Spawns that must survive the race go through
// `spawnServeHealthy`, which retries on a fresh pick when the child loses.
export const freePort = async (): Promise<number> => {
  const probe = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response("") })
  const port = probe.port!
  await probe.stop(true)
  return port
}

/** Raw HTTP exchange so tests can send arbitrary Host headers (fetch can't). */
export const rawStatus = (
  port: number,
  headers: ReadonlyArray<string>,
  path = "/api/v1/health",
): Promise<number> =>
  new Promise((resolve, reject) => {
    let data = ""
    Bun.connect({
      hostname: "127.0.0.1",
      port,
      socket: {
        open(socket) {
          socket.write(
            [`GET ${path} HTTP/1.1`, ...headers, "Connection: close", "", ""].join("\r\n"),
          )
        },
        data(socket, chunk) {
          data += chunk.toString()
          const match = data.match(/^HTTP\/1\.1 (\d{3})/)
          if (match) {
            socket.end()
            resolve(Number(match[1]))
          }
        },
        error(_socket, error) {
          reject(error)
        },
      },
    }).catch(reject)
  })

/** The spawned server exited before answering healthy — the shape a lost
 * freePort claim race leaves behind (`spawnServeHealthy` retries on it). */
export class ServerExitedError extends Error {}

export const waitForHealth = async (base: string, proc: Bun.Subprocess): Promise<Response> => {
  for (let attempt = 0; attempt < 100; attempt++) {
    // A child that died can never become healthy — and another process may
    // already answer on its port (the freePort claim race), so waiting
    // longer would end with a stranger's response. Fail fast with the
    // child's own diagnostic.
    if (proc.exitCode !== null || proc.signalCode !== null) {
      const stderr = await new Response(proc.stderr as ReadableStream).text()
      throw new ServerExitedError(
        `server at ${base} exited before becoming healthy; stderr:\n${stderr}`,
      )
    }
    try {
      // Only OUR healthy server returns 2xx here; an interloper on a stolen
      // port answers 404 and must read as "not up yet", never as the result.
      const response = await fetch(`${base}/api/v1/health`)
      if (response.ok) return response
    } catch {
      // Not accepting connections yet.
    }
    await Bun.sleep(50)
  }
  proc.kill()
  const stderr = await new Response(proc.stderr as ReadableStream).text()
  throw new Error(`server at ${base} never became healthy; stderr:\n${stderr}`)
}

/**
 * Pick a free port, spawn a server on it via `spawn`, and wait until that
 * child answers healthy. A child that exits instead (it lost the freePort
 * claim race to a concurrent spawn) is respawned on a fresh pick; a child
 * that hangs without dying is a real bug and fails immediately.
 */
export const spawnServeHealthy = async (
  spawn: (port: number) => Bun.Subprocess,
  attempts = 3,
): Promise<{ proc: Bun.Subprocess; port: number; base: string; health: Response }> => {
  for (let attempt = 0; ; attempt++) {
    const port = await freePort()
    const proc = spawn(port)
    const base = `http://127.0.0.1:${port}`
    try {
      const health = await waitForHealth(base, proc)
      return { proc, port, base, health }
    } catch (error) {
      proc.kill()
      await proc.exited
      if (!(error instanceof ServerExitedError) || attempt >= attempts - 1) throw error
    }
  }
}

/** POST a JSON body and return the decoded JSON response (asserting 2xx). */
export const postJson = async (base: string, pathName: string, body: unknown) => {
  const response = await fetch(`${base}${pathName}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  })
  expect(response.ok, `${pathName}: ${response.status} ${await response.clone().text()}`).toBe(true)
  return (await response.json()) as Record<string, string>
}

/** Poll until `condition` holds; assertion-bounded (attempts × 25ms). */
export const waitUntil = async (
  condition: () => boolean | Promise<boolean>,
  what: string,
  attempts = 200,
) => {
  for (let attempt = 0; !(await condition()); attempt++) {
    expect(attempt, `timed out waiting for ${what}`).toBeLessThan(attempts)
    await Bun.sleep(25)
  }
}

/** One project owning one terminal — the seed for attach/recovery tests. */
export const makeTerminal = async (base: string) => {
  const project = await postJson(base, "/api/v1/projects", {
    name: "P",
    defaultWorkingDirectory: "/tmp",
  })
  return postJson(base, "/api/v1/terminals", { projectId: project["id"] })
}

export interface WsSession {
  readonly socket: WebSocket
  /** Decoded binary frames, in arrival order. Text frames are ignored. */
  readonly received: Array<string>
  readonly closed: Promise<{ code: number; reason: string }>
}

/** Open a WebSocket collecting decoded binary frames; resolves once open. */
export const openSocket = (url: string): Promise<WsSession> =>
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
