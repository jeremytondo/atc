import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import * as fs from "node:fs"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import * as CodexServer from "../../src/agents/codexServer.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { eventually } from "../testLayers.ts"
import { codexServerLayer, makeCodexSandbox } from "./agentTestKit.ts"

// Supervision tests against the fake-codex-listener fixture (a Bun script
// that serves the well-known unix socket under its CODEX_HOME). All tests
// are it.live: they spawn real detached processes and need the real clock
// for readiness retries. Every path ends in stop() so no detached fixture
// outlives its test; the fixture's TTL self-exit is the backstop.

const fixturePath = fileURLToPath(new URL("../fixtures/fake-codex-listener.ts", import.meta.url))

/** The pid the most recently launched fixture recorded. */
const fixturePid = (sandbox: { readonly pidFile: string }): number =>
  Number.parseInt(fs.readFileSync(sandbox.pidFile, "utf8"), 10)

const readIdentity = (identityFile: string): { pid: number } =>
  JSON.parse(fs.readFileSync(identityFile, "utf8")) as { pid: number }

/**
 * A server ATC did not start: the fixture launched by hand on the sandbox's
 * well-known socket (no pid file, so a later ATC spawn would be visible).
 */
const spawnForeign = (
  sandbox: { readonly codexHome: string },
  env: Record<string, string> = {},
  args: ReadonlyArray<string> = ["app-server", "--listen", "unix://"],
) =>
  Bun.spawn([process.execPath, fixturePath, ...args], {
    env: { ...process.env, CODEX_HOME: sandbox.codexHome, ...env },
  })

/** Poll a real condition; never a bare sleep. */
const waitFor = (check: () => boolean) =>
  eventually(Effect.sync(check), (ok) => ok, { attempts: 200, interval: "50 millis" })

describe("CodexServer", () => {
  it.live(
    "starts detached on the well-known socket, persists identity, re-adopts, and stops",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()

        // First service instance starts the server.
        const first = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* codex.ensure()
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
        assert.strictEqual(first.socketPath, sandbox.socketPath)
        assert.isNotNull(first.pid)
        assert.isTrue(Subprocess.isProcessAlive(first.pid!))
        assert.strictEqual(readIdentity(sandbox.identityFile).pid, first.pid)
        // Exactly the well-known socket, never any other address.
        const record = JSON.parse(fs.readFileSync(sandbox.listenRecord, "utf8")) as {
          argv: ReadonlyArray<string>
        }
        assert.deepStrictEqual(record.argv, ["app-server", "--listen", "unix://"])

        // The first service's layer scope is closed now — the detached
        // server must have survived it (the whole point of detaching).
        assert.isTrue(Subprocess.isProcessAlive(first.pid!))

        // A fresh service instance adopts the same server as its own
        // instead of starting a second one, then stop() reaps it.
        yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          const adopted = yield* codex.ensure()
          assert.strictEqual(adopted.pid, first.pid)
          yield* codex.stop()
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
        assert.isTrue(yield* Subprocess.waitForProcessExit(first.pid!))
        assert.isFalse(fs.existsSync(sandbox.identityFile))
      }),
    30_000,
  )

  it.live(
    "adopts a live foreign server without starting or signaling anything",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        const foreign = spawnForeign(sandbox)
        try {
          yield* waitFor(() => fs.existsSync(sandbox.socketPath))
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            const info = yield* codex.ensure()
            assert.isNull(info.pid)
            assert.strictEqual(info.socketPath, sandbox.socketPath)
            // Nothing of ours: no spawn, no identity — and a second ensure
            // still starts nothing.
            assert.isFalse(fs.existsSync(sandbox.pidFile))
            assert.isFalse(fs.existsSync(sandbox.identityFile))
            assert.isNull((yield* codex.ensure()).pid)
            assert.isFalse(fs.existsSync(sandbox.pidFile))
            // stop() only ever targets a server we started.
            yield* codex.stop()
            assert.isTrue(Subprocess.isProcessAlive(foreign.pid))
          }).pipe(Effect.provide(codexServerLayer(sandbox)))
          assert.isTrue(Subprocess.isProcessAlive(foreign.pid))
        } finally {
          foreign.kill()
        }
      }),
    30_000,
  )

  it.live(
    "a foreign server that goes away is replaced by our own on next use",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        const foreign = spawnForeign(sandbox)
        try {
          yield* waitFor(() => fs.existsSync(sandbox.socketPath))
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            assert.isNull((yield* codex.ensure()).pid)
            foreign.kill()
            assert.isTrue(yield* Subprocess.waitForProcessExit(foreign.pid))
            // The socket is dead: nothing to adopt, so ATC starts its own —
            // rebinding over the stale socket file the foreign one left.
            const own = yield* codex.ensure()
            assert.isNotNull(own.pid)
            assert.isTrue(Subprocess.isProcessAlive(own.pid!))
            // The watcher was idle while a foreign server was adopted; it
            // is armed again now that a server of ours exists.
            process.kill(own.pid!, "SIGKILL")
            yield* waitFor(() => {
              if (!fs.existsSync(sandbox.identityFile)) return false
              const identity = readIdentity(sandbox.identityFile)
              return identity.pid !== own.pid && Subprocess.isProcessAlive(identity.pid)
            })
            yield* codex.stop()
          }).pipe(Effect.provide(codexServerLayer(sandbox, { watchInterval: "50 millis" })))
        } finally {
          foreign.kill()
        }
      }),
    30_000,
  )

  it.live(
    "losing the start race adopts the winner instead of failing",
    () =>
      Effect.gen(function* () {
        // Between ATC's probe (nothing answers) and its child's bind, a
        // foreign server takes the socket: codex then exits "already in
        // use". Modeled deterministically by a wrapper that starts the
        // foreign server, waits for it, and only then runs "our" child.
        const sandbox = makeCodexSandbox()
        const foreignPidFile = path.join(sandbox.base, "foreign-fixture.pid")
        fs.writeFileSync(
          sandbox.wrapper,
          [
            "#!/bin/sh",
            `CODEX_HOME='${sandbox.codexHome}' FAKE_CODEX_PID_FILE='${foreignPidFile}' ` +
              `"${process.execPath}" "${fixturePath}" app-server --listen unix:// &`,
            `while [ ! -S '${sandbox.socketPath}' ]; do sleep 0.05; done`,
            `CODEX_HOME='${sandbox.codexHome}' FAKE_CODEX_PID_FILE='${sandbox.pidFile}' ` +
              `exec "${process.execPath}" "${fixturePath}" "$@"`,
            "",
          ].join("\n"),
        )
        try {
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            yield* codex.ensure()
            // Our child lost and exited; the winner is untouched. If the
            // socket answered before our child had exited (or even started),
            // the first answer may briefly have named our pid — the watcher
            // reconciles.
            yield* waitFor(() => fs.existsSync(sandbox.pidFile))
            assert.isTrue(yield* Subprocess.waitForProcessExit(fixturePid(sandbox)))
            yield* waitFor(() => !fs.existsSync(sandbox.identityFile))
            assert.isNull((yield* codex.ensure()).pid)
            yield* codex.stop()
            assert.isTrue(Subprocess.isProcessAlive(fixturePid({ pidFile: foreignPidFile })))
          }).pipe(Effect.provide(codexServerLayer(sandbox, { watchInterval: "50 millis" })))
        } finally {
          if (fs.existsSync(foreignPidFile)) {
            process.kill(fixturePid({ pidFile: foreignPidFile }), "SIGKILL")
          }
        }
      }),
    30_000,
  )

  it.live(
    "replaces a dead persisted pid instead of failing",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        // A pid that is certainly dead: spawn a no-op and wait for it.
        const gone = Bun.spawnSync(["true"])
        fs.mkdirSync(sandbox.stateDir, { recursive: true })
        fs.writeFileSync(
          sandbox.identityFile,
          JSON.stringify({ pid: gone.pid ?? 999999, startedAt: "then" }),
        )
        yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          const info = yield* codex.ensure()
          assert.isNotNull(info.pid)
          assert.isTrue(Subprocess.isProcessAlive(info.pid!))
          yield* codex.stop()
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
      }),
    30_000,
  )

  it.live(
    "replaces our own alive-but-silent server (never a foreign one)",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        // A live process that IS an app-server by command line and IS
        // recorded as ours, but never binds: the never-ready fixture.
        const stale = spawnForeign(sandbox, { FAKE_CODEX_MODE: "never-ready" })
        try {
          fs.mkdirSync(sandbox.stateDir, { recursive: true })
          fs.writeFileSync(
            sandbox.identityFile,
            JSON.stringify({ pid: stale.pid, startedAt: "then" }),
          )
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            const info = yield* codex.ensure()
            // The silent claimant was ours, so it was replaced.
            assert.isTrue(yield* Subprocess.waitForProcessExit(stale.pid))
            assert.isNotNull(info.pid)
            assert.isTrue(Subprocess.isProcessAlive(info.pid!))
            yield* codex.stop()
          }).pipe(Effect.provide(codexServerLayer(sandbox, { adoptTimeout: "500 millis" })))
        } finally {
          stale.kill()
        }
      }),
    30_000,
  )

  it.live(
    "a recycled pid that is not an app-server is abandoned, never signaled",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        // A live process whose command line is NOT an app-server — models a
        // pid recycled to an innocent process after a reboot.
        const bystander = Bun.spawn([
          process.execPath,
          fileURLToPath(new URL("../fixtures/sleep-forever.ts", import.meta.url)),
        ])
        try {
          fs.mkdirSync(sandbox.stateDir, { recursive: true })
          fs.writeFileSync(
            sandbox.identityFile,
            JSON.stringify({ pid: bystander.pid, startedAt: "then" }),
          )
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            const info = yield* codex.ensure()
            // A fresh server was started and the bystander was left alone.
            assert.isTrue(Subprocess.isProcessAlive(info.pid!))
            assert.isTrue(Subprocess.isProcessAlive(bystander.pid))
            yield* codex.stop()
          }).pipe(Effect.provide(codexServerLayer(sandbox)))
          assert.isTrue(Subprocess.isProcessAlive(bystander.pid))
        } finally {
          bystander.kill()
        }
      }),
    30_000,
  )

  it.live(
    "a server that never answers is reaped and reported actionably",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_MODE: "never-ready" })
        const failure = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* Effect.flip(codex.ensure())
        }).pipe(Effect.provide(codexServerLayer(sandbox, { readyTimeout: "700 millis" })))
        assert.include(failure.message, "not answering")
        assert.include(failure.message, sandbox.socketPath)
        // The failed start was cleaned up: no identity, no stray process.
        assert.isFalse(fs.existsSync(sandbox.identityFile))
        assert.isTrue(yield* Subprocess.waitForProcessExit(fixturePid(sandbox)))
      }),
    30_000,
  )

  it.live(
    "a start that exits early reports the child's output",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        fs.writeFileSync(
          sandbox.wrapper,
          `#!/bin/sh\necho "fake codex: cannot start" >&2\nexit 3\n`,
        )
        const failure = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* Effect.flip(codex.ensure())
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
        assert.include(failure.message, "exited before answering")
        assert.include(failure.message, "fake codex: cannot start")
        assert.isFalse(fs.existsSync(sandbox.identityFile))
      }),
    30_000,
  )

  it.live(
    "a missing executable is one actionable diagnostic, and building the layer touches nothing",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        const layer = codexServerLayer({
          stateDir: sandbox.stateDir,
          codexHome: sandbox.codexHome,
          wrapper: "atc-test-definitely-not-codex",
        })
        // Codex is only consulted on first use: an ATC without codex boots.
        yield* Effect.gen(function* () {
          yield* CodexServer.CodexServer
          assert.isFalse(fs.existsSync(sandbox.socketPath))
        }).pipe(Effect.provide(layer))
        const failure = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* Effect.flip(codex.ensure())
        }).pipe(Effect.provide(layer))
        assert.include(failure.message, "install the Codex CLI")
        assert.include(failure.message, "ATC_CODEX_EXECUTABLE")
      }),
    30_000,
  )

  it.live(
    "the exit watcher restarts our own server that died underneath us",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          const info = yield* codex.ensure()
          process.kill(info.pid!, "SIGKILL")
          yield* waitFor(() => {
            if (!fs.existsSync(sandbox.identityFile)) return false
            const identity = readIdentity(sandbox.identityFile)
            return identity.pid !== info.pid && Subprocess.isProcessAlive(identity.pid)
          })
          const replacement = yield* codex.ensure()
          assert.notStrictEqual(replacement.pid, info.pid)
          yield* codex.stop()
        }).pipe(Effect.provide(codexServerLayer(sandbox, { watchInterval: "50 millis" })))
      }),
    30_000,
  )
})

describe("CodexServer.controlSocketPath", () => {
  it("is codex's hardcoded location under the codex home", () => {
    assert.strictEqual(
      CodexServer.controlSocketPath("/home/u/.codex"),
      path.join("/home/u/.codex", "app-server-control", "app-server-control.sock"),
    )
  })
})
