// Fixture stand-in for `codex app-server --listen ws://127.0.0.1:<port>`:
// binds the requested port and answers /readyz so codexServer supervision
// can be exercised without a real Codex install. FAKE_CODEX_MODE selects a
// behavior: "ok" (default) answers /readyz 200, "never-ready" answers 503.
// FAKE_CODEX_PID_FILE records this pid so tests can assert the process was
// reaped even on paths that clear the identity file. FAKE_CODEX_TTL_MS
// (default 120s) self-terminates the process so a test that fails to stop()
// a detached fixture can never leak it for long.

const listenArg = process.argv.find((arg) => arg.startsWith("ws://"))
if (listenArg === undefined) {
  console.error("fake-codex-listener: no ws:// listen argument")
  process.exit(64)
}
const port = Number.parseInt(new URL(listenArg).port, 10)
const mode = process.env["FAKE_CODEX_MODE"] ?? "ok"
const ttl = Number.parseInt(process.env["FAKE_CODEX_TTL_MS"] ?? "120000", 10)
const pidFile = process.env["FAKE_CODEX_PID_FILE"]
if (pidFile !== undefined && pidFile !== "") {
  await Bun.write(pidFile, String(process.pid))
}

Bun.serve({
  hostname: "127.0.0.1",
  port,
  fetch(request) {
    if (new URL(request.url).pathname === "/readyz" && mode === "ok") {
      return new Response("ok", { status: 200 })
    }
    return new Response("not ready", { status: 503 })
  },
})

setTimeout(() => process.exit(0), ttl)
