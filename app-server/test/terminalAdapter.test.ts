import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import { afterAll } from "vitest"
import * as Subprocess from "../src/subprocess.ts"
import {
  sessionNameForTerminalId,
  TerminalAdapter,
  terminalIdForSessionName,
} from "../src/terminalAdapter.ts"
import * as Zmx from "../src/zmxAdapter.ts"
import { makeFakeAdapter } from "./fakeTerminalAdapter.ts"
import {
  cleanupTempDirs,
  collectText,
  makeShortSocketDir,
  testAppConfig,
  trackTempDir,
  waitForText,
} from "./testLayers.ts"

// TerminalAdapter coverage: pure helpers, the zmx adapter against the
// fake-zmx fixture (deterministic, no zmx install), and the fake in-memory
// adapter's honesty tests. The real zmx binary is exercised by the opt-in
// zmxSmoke tests.

const fixturePath = fileURLToPath(new URL("fixtures/fake-zmx.ts", import.meta.url))

afterAll(cleanupTempDirs)

/**
 * Per-test sandbox. The wrapper script is the configured "zmx executable":
 * the adapter owns argv and environment, so per-test fixture configuration
 * is baked into the wrapper itself — no process.env mutation anywhere.
 */
const makeSandbox = (vars: Record<string, string> = {}) => {
  const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-zmx-")))
  const stateDir = path.join(base, "state")
  const wrapper = path.join(base, "fake-zmx")
  const assignments = Object.entries({ FAKE_ZMX_STATE: stateDir, ...vars })
    .map(([key, value]) => `${key}='${value}'`)
    .join(" ")
  fs.writeFileSync(
    wrapper,
    `#!/bin/sh\n${assignments} exec "${process.execPath}" "${fixturePath}" "$@"\n`,
  )
  fs.chmodSync(wrapper, 0o755)
  return { base, stateDir, wrapper, socketDir: makeShortSocketDir() }
}

const TIGHT: Zmx.ZmxOptions = { pollInterval: "25 millis", verifyPasses: 8 }

const adapterLayer = (
  sandbox: ReturnType<typeof makeSandbox>,
  options: Zmx.ZmxOptions = TIGHT,
  zmxExecutable: string = sandbox.wrapper,
) =>
  Zmx.layerWith(options).pipe(
    Layer.provide(testAppConfig({ zmxExecutable, terminalSocketDir: sandbox.socketDir })),
    Layer.provide(Subprocess.layer),
    Layer.provideMerge(BunServices.layer),
  )

const readSessionRecord = (sandbox: ReturnType<typeof makeSandbox>, name: string) =>
  JSON.parse(fs.readFileSync(path.join(sandbox.stateDir, name), "utf8")) as {
    startDir: string
    command: Array<string>
    env: Record<string, string | undefined>
  }

describe("session name derivation", () => {
  it("derives and reverses the private session name", () => {
    const id = "01890a5d-ac96-774b-bcce-b302099a8057"
    const name = sessionNameForTerminalId(id)
    assert.strictEqual(name, "atc-01890a5dac96774bbcceb302099a8057")
    assert.strictEqual(terminalIdForSessionName(name), id)
    // Uppercase ids normalize instead of producing an irreversible name.
    assert.strictEqual(sessionNameForTerminalId(id.toUpperCase()), name)
  })

  it("rejects foreign names", () => {
    assert.isUndefined(terminalIdForSessionName("my-shell"))
    assert.isUndefined(terminalIdForSessionName("atc-nothex"))
    assert.isUndefined(terminalIdForSessionName("atc-01890a5dac96774bbcceb302099a80"))
  })
})

describe("parseSessionList", () => {
  it("parses real zmx list lines, tolerating markers and noise", () => {
    const sessions = Zmx.parseSessionList([
      "  name=atc-0123\tpid=85174\tclients=0\tcreated=1785707091\tstart_dir=/w",
      "→ name=other\tpid=12\tclients=1\tcreated=5",
      "name=broken\terr=Timeout\tstatus=unreachable",
      "not a session line",
      "pid=42\tcreated=7",
      "",
    ])
    assert.deepStrictEqual(sessions, [
      { name: "atc-0123", reachable: true, pid: 85174, createdAt: 1785707091 },
      { name: "other", reachable: true, pid: 12, createdAt: 5 },
      { name: "broken", reachable: false },
    ])
  })

  it("parses an empty inventory", () => {
    assert.deepStrictEqual(Zmx.parseSessionList([]), [])
  })
})

describe("zmxChildEnv", () => {
  it("scrubs the nested-client traps and pins ZMX_DIR and TERM", () => {
    const env = Zmx.zmxChildEnv("/sockets", {
      PATH: "/usr/bin",
      HOME: "/home/u",
      ZMX_SESSION: "evil",
      ZMX_SESSION_PREFIX: "pre-",
      EMPTY: undefined,
    })
    assert.deepStrictEqual(env, {
      PATH: "/usr/bin",
      HOME: "/home/u",
      ZMX_DIR: "/sockets",
      TERM: "xterm-256color",
    })
  })
})

describe("zmx adapter against fake-zmx", () => {
  it.live("creates a session, settles, and detaches the creation client", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({ name: "atc-aaaa", cwd: sandbox.base })
      const sessions = yield* adapter.listSessions()
      assert.deepStrictEqual(
        sessions.map((s) => s.name),
        ["atc-aaaa"],
      )
      // The creation client ran with the private dir pinned and the session
      // TERM set (the scrub itself is pinned by the zmxChildEnv unit test).
      const record = readSessionRecord(sandbox, "atc-aaaa")
      assert.strictEqual(record.env["ZMX_DIR"], sandbox.socketDir)
      assert.strictEqual(record.env["TERM"], "xterm-256color")
      assert.strictEqual(record.startDir, fs.realpathSync(sandbox.base))
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("re-tightens a pre-existing permissive socket directory at boot", () => {
    const sandbox = makeSandbox()
    // A prior or manual zmx invocation could have left the directory more
    // open than ATC's guarantee; boot must restore 0700, not just claim it.
    fs.chmodSync(sandbox.socketDir, 0o755)
    return Effect.gen(function* () {
      yield* TerminalAdapter
      assert.strictEqual(fs.statSync(sandbox.socketDir).mode & 0o777, 0o700)
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("passes an exec-style argv through and accepts a finished command", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_ATTACH_MODE: "exit" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({
        name: "atc-bbbb",
        cwd: sandbox.base,
        command: ["echo", "hi there"],
      })
      assert.deepStrictEqual(readSessionRecord(sandbox, "atc-bbbb").command, ["echo", "hi there"])
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("refuses to create over an existing session", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({ name: "atc-dupe", cwd: sandbox.base })
      const result = yield* Effect.result(
        adapter.createSession({ name: "atc-dupe", cwd: sandbox.base, command: ["other"] }),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /already exists/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("never treats an unreachable inventory entry as a settled create", () => {
    // Regression: a wedged socket produces an err= entry; creating that name
    // must fail (the path is held), not silently report success.
    const sandbox = makeSandbox({ FAKE_ZMX_UNREACHABLE: "atc-wedged" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(
        adapter.createSession({ name: "atc-wedged", cwd: sandbox.base }),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /already exists/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("fails a launch whose session never appears, with diagnostics", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_ATTACH_MODE: "fail-fast" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(
        adapter.createSession({ name: "atc-cccc", cwd: sandbox.base }),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /cannot create session/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("fails a launch that never settles within the verification passes", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_SETTLE_MS: "60000" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(
        adapter.createSession({ name: "atc-eeee", cwd: sandbox.base }),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /never settled/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("rejects a working directory that does not exist as a conclusive failure", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(
        adapter.createSession({ name: "atc-ffff", cwd: path.join(sandbox.base, "nope") }),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /working directory/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("reports an unavailable inventory instead of guessing", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_LIST_MODE: "fail" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(adapter.listSessions())
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "ZmxUnavailable")
        assert.match(result.failure.message, /inventory unavailable/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("kills a session and verifies its delayed disappearance", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_ATTACH_MODE: "exit", FAKE_ZMX_KILL_MS: "200" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({ name: "atc-gggg", cwd: sandbox.base })
      yield* adapter.killSession("atc-gggg")
      assert.deepStrictEqual(yield* adapter.listSessions(), [])
    }).pipe(Effect.provide(adapterLayer(sandbox, { pollInterval: "50 millis", verifyPasses: 10 })))
  })

  it.live("treats killing an absent session as success", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.killSession("atc-not-there")
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("fails kill when the session refuses to die", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_ATTACH_MODE: "exit", FAKE_ZMX_KILL_MODE: "ignore" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({ name: "atc-hhhh", cwd: sandbox.base })
      const result = yield* Effect.result(adapter.killSession("atc-hhhh"))
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /still present/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("refuses to attach to a session missing from the inventory", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(
        adapter.attachSession("atc-nope", { cols: 80, rows: 24 }).pipe(Effect.scoped),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionNotFound")
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("refuses to attach to an unreachable session (it would resurrect it)", () => {
    const sandbox = makeSandbox({ FAKE_ZMX_UNREACHABLE: "atc-wedged" })
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(
        adapter.attachSession("atc-wedged", { cols: 80, rows: 24 }).pipe(Effect.scoped),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SessionOperationFailed")
        assert.match(result.failure.message, /unreachable/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("attaches and round-trips terminal bytes", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({ name: "atc-iiii", cwd: sandbox.base })
      yield* Effect.gen(function* () {
        const connection = yield* adapter.attachSession("atc-iiii", { cols: 80, rows: 24 })
        const sink = yield* collectText(connection.output)
        yield* connection.write("hello\n")
        yield* waitForText(sink, "echo:hello")
      }).pipe(Effect.scoped)
    }).pipe(Effect.provide(adapterLayer(sandbox)))
  })

  it.live("fails with one actionable line when the executable is missing", () => {
    const sandbox = makeSandbox()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const result = yield* Effect.result(adapter.listSessions())
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "ZmxUnavailable")
        assert.match(result.failure.message, /install zmx on PATH|ATC_ZMX_EXECUTABLE/)
      }
    }).pipe(Effect.provide(adapterLayer(sandbox, TIGHT, "definitely-not-a-real-zmx-executable")))
  })

  it.live("refuses to boot when socket paths would exceed the unix limit", () => {
    const sandbox = makeSandbox()
    const deepDir = path.join(sandbox.base, "x".repeat(120))
    return Effect.gen(function* () {
      const result = yield* Effect.result(
        Layer.build(
          Zmx.layerWith(TIGHT).pipe(
            Layer.provide(
              testAppConfig({ zmxExecutable: sandbox.wrapper, terminalSocketDir: deepDir }),
            ),
            Layer.provide(Subprocess.layer),
            Layer.provideMerge(BunServices.layer),
          ),
        ),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.match(String(result.failure), /too deep|exceed/)
      }
    }).pipe(Effect.scoped)
  })
})

describe("fake adapter honesty", () => {
  it.effect("mirrors the adapter contract: create, list, attach, kill", () => {
    const fake = makeFakeAdapter()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      yield* adapter.createSession({ name: "atc-1111", cwd: "/w", command: ["vim"] })
      assert.deepStrictEqual(
        (yield* adapter.listSessions()).map((s) => s.name),
        ["atc-1111"],
      )
      // Creating is never silently attaching, like the real adapter.
      const duplicate = yield* Effect.result(
        adapter.createSession({ name: "atc-1111", cwd: "/other" }),
      )
      assert.strictEqual(duplicate._tag, "Failure")
      yield* Effect.gen(function* () {
        const connection = yield* adapter.attachSession("atc-1111", { cols: 100, rows: 30 })
        yield* connection.write("keys")
        yield* connection.resize({ cols: 120, rows: 40 })
        fake.emitOutput("atc-1111", new TextEncoder().encode("out"))
        const first = yield* Stream.runHead(connection.output)
        assert.strictEqual(first._tag, "Some")
        const session = fake.sessions.get("atc-1111")
        assert.deepStrictEqual(session?.written, ["keys"])
        assert.deepStrictEqual(session?.lastResize, { cols: 120, rows: 40 })
      }).pipe(Effect.scoped)
      yield* adapter.killSession("atc-1111")
      assert.deepStrictEqual(yield* adapter.listSessions(), [])
      // Idempotent: killing the absent session still succeeds.
      yield* adapter.killSession("atc-1111")
    }).pipe(Effect.provide(fake.layer))
  })

  it.effect("fails attach like the real adapter: missing and unreachable", () => {
    const fake = makeFakeAdapter()
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      const missing = yield* Effect.result(
        adapter.attachSession("atc-none", { cols: 80, rows: 24 }).pipe(Effect.scoped),
      )
      assert.strictEqual(missing._tag, "Failure")
      if (missing._tag === "Failure") {
        assert.strictEqual(missing.failure._tag, "SessionNotFound")
      }
      yield* adapter.createSession({ name: "atc-2222", cwd: "/w" })
      fake.sessions.get("atc-2222")!.reachable = false
      assert.deepStrictEqual(yield* adapter.listSessions(), [
        { name: "atc-2222", reachable: false },
      ])
      const unreachable = yield* Effect.result(
        adapter.attachSession("atc-2222", { cols: 80, rows: 24 }).pipe(Effect.scoped),
      )
      assert.strictEqual(unreachable._tag, "Failure")
      if (unreachable._tag === "Failure") {
        assert.strictEqual(unreachable.failure._tag, "SessionOperationFailed")
      }
    }).pipe(Effect.provide(fake.layer))
  })

  it.effect("switches every inventory consumer to ZmxUnavailable", () => {
    const fake = makeFakeAdapter()
    fake.setUnavailable(true)
    return Effect.gen(function* () {
      const adapter = yield* TerminalAdapter
      for (const operation of [
        Effect.asVoid(adapter.listSessions()),
        adapter.createSession({ name: "atc-3333", cwd: "/w" }),
        adapter.killSession("atc-3333"),
        Effect.asVoid(Effect.scoped(adapter.attachSession("atc-3333", { cols: 1, rows: 1 }))),
      ]) {
        const result = yield* Effect.result(operation)
        assert.strictEqual(result._tag, "Failure")
        if (result._tag === "Failure") {
          assert.strictEqual((result.failure as { _tag: string })._tag, "ZmxUnavailable")
        }
      }
    }).pipe(Effect.provide(fake.layer))
  })
})
