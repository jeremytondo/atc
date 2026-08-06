import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import * as fs from "node:fs"
import { fileURLToPath } from "node:url"
import * as CodexServer from "../../src/agents/codexServer.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { freePort } from "../blackbox.ts"
import { codexServerLayer, makeCodexSandbox } from "./agentTestKit.ts"

// Supervision tests against the fake-codex-listener fixture (a Bun script
// that binds the requested --listen port and answers /readyz). All tests are
// it.live: they spawn real detached processes and need the real clock for
// readiness retries. Every path ends in stop() so no detached fixture
// outlives its test; the fixture's TTL self-exit is the backstop.

const fixturePath = fileURLToPath(new URL("../fixtures/fake-codex-listener.ts", import.meta.url))

/** The pid the most recently launched fixture recorded. */
const fixturePid = (sandbox: { readonly pidFile: string }): number =>
  Number.parseInt(fs.readFileSync(sandbox.pidFile, "utf8"), 10)

const readIdentity = (identityFile: string): { pid: number; port: number } =>
  JSON.parse(fs.readFileSync(identityFile, "utf8")) as { pid: number; port: number }

/** Poll a real condition; assertion-bounded, never a bare sleep. */
const waitFor = (what: string, check: () => boolean, attempts = 200) =>
  Effect.gen(function* () {
    for (let attempt = 0; !check(); attempt++) {
      assert.isBelow(attempt, attempts, `never observed: ${what}`)
      yield* Effect.sleep("50 millis")
    }
  })

describe("CodexServer", () => {
  it.live(
    "spawns detached, persists identity, adopts from a fresh service, and stops",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()

        // First service instance spawns the server.
        const first = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* codex.ensure()
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
        assert.isTrue(Subprocess.isProcessAlive(first.pid))
        assert.strictEqual(first.url, `ws://127.0.0.1:${first.port}`)
        const persisted = readIdentity(sandbox.identityFile)
        assert.strictEqual(persisted.pid, first.pid)
        assert.strictEqual(persisted.port, first.port)

        // The first service's layer scope is closed now — the detached
        // server must have survived it (the whole point of detaching).
        assert.isTrue(Subprocess.isProcessAlive(first.pid))

        // A fresh service instance adopts the same server instead of
        // spawning a second one, then stop() reaps it and clears identity.
        yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          const adopted = yield* codex.ensure()
          assert.strictEqual(adopted.pid, first.pid)
          assert.strictEqual(adopted.port, first.port)
          yield* codex.stop()
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
        assert.isTrue(yield* Subprocess.waitForProcessExit(first.pid))
        assert.isFalse(fs.existsSync(sandbox.identityFile))
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
          JSON.stringify({ pid: gone.pid ?? 999999, port: 1, startedAt: "then" }),
        )
        yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          const info = yield* codex.ensure()
          assert.isTrue(Subprocess.isProcessAlive(info.pid))
          assert.notStrictEqual(info.port, 1)
          yield* codex.stop()
        }).pipe(Effect.provide(codexServerLayer(sandbox)))
      }),
    30_000,
  )

  it.live(
    "replaces an alive-but-unready server (adopt-or-replace, never accumulate)",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        // A live process that IS an app-server by command line but never
        // becomes ready: the never-ready fixture launched by hand.
        const stalePort = yield* Effect.promise(() => freePort())
        const stale = Bun.spawn(
          [process.execPath, fixturePath, "app-server", "--listen", `ws://127.0.0.1:${stalePort}`],
          { env: { ...process.env, FAKE_CODEX_MODE: "never-ready" } },
        )
        try {
          fs.mkdirSync(sandbox.stateDir, { recursive: true })
          fs.writeFileSync(
            sandbox.identityFile,
            JSON.stringify({ pid: stale.pid, port: stalePort, startedAt: "then" }),
          )
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            const info = yield* codex.ensure()
            // The stale claimant was killed; the new server is ready.
            assert.isTrue(yield* Subprocess.waitForProcessExit(stale.pid))
            assert.isTrue(Subprocess.isProcessAlive(info.pid))
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
            JSON.stringify({ pid: bystander.pid, port: 1, startedAt: "then" }),
          )
          yield* Effect.gen(function* () {
            const codex = yield* CodexServer.CodexServer
            const info = yield* codex.ensure()
            // A fresh server was spawned and the bystander was left alone.
            assert.isTrue(Subprocess.isProcessAlive(info.pid))
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
    "a server that never becomes ready is reaped and reported actionably",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox({ FAKE_CODEX_MODE: "never-ready" })
        const failure = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* Effect.flip(codex.ensure())
        }).pipe(Effect.provide(codexServerLayer(sandbox, { readyTimeout: "700 millis" })))
        assert.include(failure.message, "not ready")
        // The failed spawn was cleaned up: no identity, no stray process.
        assert.isFalse(fs.existsSync(sandbox.identityFile))
        assert.isTrue(yield* Subprocess.waitForProcessExit(fixturePid(sandbox)))
      }),
    30_000,
  )

  it.live(
    "a missing executable is one actionable diagnostic",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        const failure = yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          return yield* Effect.flip(codex.ensure())
        }).pipe(
          Effect.provide(
            codexServerLayer({
              stateDir: sandbox.stateDir,
              wrapper: "atc-test-definitely-not-codex",
            }),
          ),
        )
        assert.include(failure.message, "install the Codex CLI")
        assert.include(failure.message, "ATC_CODEX_EXECUTABLE")
      }),
    30_000,
  )

  it.live(
    "the exit watcher restarts a server that died underneath us",
    () =>
      Effect.gen(function* () {
        const sandbox = makeCodexSandbox()
        yield* Effect.gen(function* () {
          const codex = yield* CodexServer.CodexServer
          const info = yield* codex.ensure()
          process.kill(info.pid, "SIGKILL")
          yield* waitFor("watcher restarted the server with a new live pid", () => {
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
