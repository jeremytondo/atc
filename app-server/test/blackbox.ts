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
 * Environment pointing every settled location (config, data, state) away
 * from the real user locations on the machine running the tests. The state
 * home gets its own short /tmp path rather than living under `dir`: the
 * terminal socket directory lives under it and must fit the unix
 * socket-path budget. Read the location back from the returned env when a
 * test needs to inspect state files.
 */
export const isolatedEnv = (dir: string, extra: Record<string, string> = {}) => ({
  ...process.env,
  XDG_CONFIG_HOME: `${dir}/config`,
  XDG_DATA_HOME: `${dir}/data`,
  XDG_STATE_HOME: trackTempDir(mkdtempSync("/tmp/atc-st-")),
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
 * repository, so no caller should silently inherit the repo cwd.
 */
export const runCli = async (
  command: ReadonlyArray<string>,
  args: ReadonlyArray<string>,
  cwd: string,
  env: Record<string, string | undefined> = process.env,
) => {
  const proc = Bun.spawn([...command, ...args], { cwd, env, stdout: "pipe", stderr: "pipe" })
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  return { stdout, stderr, exitCode }
}

// Bind-then-release to pick a port for the child. Racy in principle (another
// process could grab it in between), but --port 0 is deliberately rejected by
// validation, so this is the practical option; stderr is surfaced on failure.
export const freePort = async (): Promise<number> => {
  const probe = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response("") })
  const port = probe.port!
  await probe.stop(true)
  return port
}

export const waitForHealth = async (base: string, proc: Bun.Subprocess): Promise<Response> => {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      return await fetch(`${base}/api/v1/health`)
    } catch {
      await Bun.sleep(50)
    }
  }
  proc.kill()
  const stderr = await new Response(proc.stderr as ReadableStream).text()
  throw new Error(`server at ${base} never became healthy; stderr:\n${stderr}`)
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
