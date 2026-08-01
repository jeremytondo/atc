import { fileURLToPath } from "node:url"

// Shared helpers for black-box tests that spawn a real `atc serve` process —
// from source (serve.blackbox.test.ts) or from the compiled artifact
// (compiled.blackbox.test.ts) — and talk to it over loopback TCP.

export const appServerRoot = fileURLToPath(new URL("..", import.meta.url))

/** Deterministic host-target artifact path produced by `mise run build`. */
export const compiledBinaryPath =
  process.env["ATC_COMPILED_BIN"] ?? `${appServerRoot}dist/atc-${process.platform}-${process.arch}`

/** Spawn `<command> serve --port <port>`; `command` is the program plus any leading args. */
export const spawnServe = (
  command: ReadonlyArray<string>,
  port: number,
  cwd: string = appServerRoot,
) =>
  Bun.spawn([...command, "serve", "--port", String(port)], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
  })

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
