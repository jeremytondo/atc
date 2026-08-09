import { assert, describe, it } from "@effect/vitest"
import { BunFileSystem } from "@effect/platform-bun"
import { Effect } from "effect"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { DEFAULT_PORT } from "../../src/api/contract.ts"
import * as Config from "../../src/platform/config.ts"

// The settled configuration pipeline: XDG paths, ATC_* environment, TOML file,
// precedence, and fail-fast diagnostics naming the offending source.

const scratch = mkdtempSync(join(tmpdir(), "atc-config-test-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

let caseId = 0
const writeConfig = (contents: string) => {
  const file = join(scratch, `config-${caseId++}.toml`)
  Bun.write(file, contents)
  return file
}

// Every test injects HOME so results never depend on the developer's real
// ~/.config/atc/config.toml.
const home = join(scratch, "home")

const load = (env: Config.Env) =>
  Config.load({ HOME: home, ...env }).pipe(Effect.provide(BunFileSystem.layer))

const loadError = (env: Config.Env) =>
  Config.load({ HOME: home, ...env }).pipe(Effect.flip, Effect.provide(BunFileSystem.layer))

describe("configuration", () => {
  it.effect("defaults: XDG locations under HOME and the default port", () =>
    Effect.gen(function* () {
      const config = yield* load({})
      assert.strictEqual(config.port, DEFAULT_PORT)
      assert.strictEqual(config.bind, "127.0.0.1")
      assert.strictEqual(config.logLevel, "Info")
      assert.strictEqual(config.configFile, join(home, ".config", "atc", "config.toml"))
      assert.strictEqual(config.dataDir, join(home, ".local", "share", "atc"))
      assert.strictEqual(config.stateDir, join(home, ".local", "state", "atc"))
      assert.strictEqual(config.dbFile, join(home, ".local", "share", "atc", "atc.db"))
      assert.strictEqual(config.tokenFile, join(home, ".local", "share", "atc", "auth-token"))
      assert.strictEqual(config.logFile, join(home, ".local", "state", "atc", "atc.log"))
      assert.strictEqual(config.zmxExecutable, "zmx")
      assert.strictEqual(config.codexExecutable, "codex")
      assert.strictEqual(config.claudeExecutable, "claude")
      assert.strictEqual(
        config.terminalSocketDir,
        join(home, ".local", "state", "atc", "terminals"),
      )
    }),
  )

  it.effect("provider executables follow the precedence rule and reject relative paths", () =>
    Effect.gen(function* () {
      const file = writeConfig(
        `codexExecutable = "/opt/from-file/codex"\nclaudeExecutable = "/opt/from-file/claude"\n`,
      )
      const fromFile = yield* load({ ATC_CONFIG: file })
      assert.strictEqual(fromFile.codexExecutable, "/opt/from-file/codex")
      assert.strictEqual(fromFile.claudeExecutable, "/opt/from-file/claude")
      const fromEnv = yield* load({
        ATC_CONFIG: file,
        ATC_CODEX_EXECUTABLE: "/opt/from-env/codex",
        ATC_CLAUDE_EXECUTABLE: "/opt/from-env/claude",
      })
      assert.strictEqual(fromEnv.codexExecutable, "/opt/from-env/codex")
      assert.strictEqual(fromEnv.claudeExecutable, "/opt/from-env/claude")

      const codexError = yield* loadError({ ATC_CODEX_EXECUTABLE: "bin/codex" })
      assert.include(codexError.message, "bare name or an absolute path")
      assert.include(codexError.source, "ATC_CODEX_EXECUTABLE")
      const claudeError = yield* loadError({ ATC_CLAUDE_EXECUTABLE: "bin/claude" })
      assert.include(claudeError.source, "ATC_CLAUDE_EXECUTABLE")
    }),
  )

  it.effect("zmxExecutable follows the precedence rule: env beats file", () =>
    Effect.gen(function* () {
      const file = writeConfig(`zmxExecutable = "/opt/from-file/zmx"\n`)
      assert.strictEqual((yield* load({ ATC_CONFIG: file })).zmxExecutable, "/opt/from-file/zmx")
      assert.strictEqual(
        (yield* load({ ATC_CONFIG: file, ATC_ZMX_EXECUTABLE: "/opt/from-env/zmx" })).zmxExecutable,
        "/opt/from-env/zmx",
      )
    }),
  )

  it.effect("a relative zmxExecutable path is rejected (never cwd-dependent)", () =>
    Effect.gen(function* () {
      const error = yield* loadError({ ATC_ZMX_EXECUTABLE: "bin/zmx" })
      assert.include(error.message, "bare name or an absolute path")
      assert.include(error.source, "ATC_ZMX_EXECUTABLE")
      // Bare names stay valid — they resolve on PATH.
      assert.strictEqual((yield* load({ ATC_ZMX_EXECUTABLE: "my-zmx" })).zmxExecutable, "my-zmx")
    }),
  )

  it.effect("XDG_* overrides move every location", () =>
    Effect.gen(function* () {
      const config = yield* load({
        XDG_CONFIG_HOME: join(scratch, "cfg"),
        XDG_DATA_HOME: join(scratch, "data"),
        XDG_STATE_HOME: join(scratch, "state"),
      })
      assert.strictEqual(config.configFile, join(scratch, "cfg", "atc", "config.toml"))
      assert.strictEqual(config.dataDir, join(scratch, "data", "atc"))
      assert.strictEqual(config.stateDir, join(scratch, "state", "atc"))
    }),
  )

  it.effect("config file values apply when the environment is silent", () =>
    Effect.gen(function* () {
      const file = writeConfig(
        `port = 9100\nbind = "0.0.0.0"\nlogLevel = "debug"\ndataDir = "${scratch}/d"\n`,
      )
      const config = yield* load({ ATC_CONFIG: file })
      assert.strictEqual(config.port, 9100)
      assert.strictEqual(config.bind, "0.0.0.0")
      assert.strictEqual(config.logLevel, "Debug")
      assert.strictEqual(config.dataDir, `${scratch}/d`)
      assert.strictEqual(config.dbFile, `${scratch}/d/atc.db`)
      assert.strictEqual(config.tokenFile, `${scratch}/d/auth-token`)
    }),
  )

  it.effect("environment beats the config file", () =>
    Effect.gen(function* () {
      const file = writeConfig(`port = 9100\nbind = "0.0.0.0"\nlogLevel = "debug"\n`)
      const config = yield* load({
        ATC_CONFIG: file,
        ATC_PORT: "9200",
        ATC_BIND: "100.64.0.7",
        ATC_LOG_LEVEL: "warn",
        ATC_DATA_DIR: join(scratch, "env-data"),
      })
      assert.strictEqual(config.port, 9200)
      assert.strictEqual(config.bind, "100.64.0.7")
      assert.strictEqual(config.logLevel, "Warn")
      assert.strictEqual(config.dataDir, join(scratch, "env-data"))
    }),
  )

  it.effect("a missing config file is not an error", () =>
    Effect.gen(function* () {
      const config = yield* load({ ATC_CONFIG: join(scratch, "does-not-exist.toml") })
      assert.strictEqual(config.port, DEFAULT_PORT)
    }),
  )

  it.effect("malformed TOML fails naming the file", () =>
    Effect.gen(function* () {
      const file = writeConfig("port = [not toml")
      const error = yield* loadError({ ATC_CONFIG: file })
      assert.strictEqual(error._tag, "ConfigLoadError")
      assert.strictEqual(error.source, file)
    }),
  )

  it.effect("an unknown key fails naming the key", () =>
    Effect.gen(function* () {
      const file = writeConfig(`prot = 9100\n`)
      const error = yield* loadError({ ATC_CONFIG: file })
      assert.strictEqual(error.source, file)
      assert.include(error.message, '"prot"')
    }),
  )

  it.effect("an invalid port in the environment fails with a one-line diagnostic", () =>
    Effect.gen(function* () {
      const error = yield* loadError({ ATC_PORT: "70000" })
      assert.strictEqual(error._tag, "ConfigLoadError")
      assert.notInclude(error.message, "\n")
    }),
  )

  it.effect("an invalid log level names the accepted values", () =>
    Effect.gen(function* () {
      const error = yield* loadError({ ATC_LOG_LEVEL: "verbose" })
      assert.include(error.message, "verbose")
      assert.include(error.message, "debug")
    }),
  )

  it.effect("log level is case-insensitive", () =>
    Effect.gen(function* () {
      const config = yield* load({ ATC_LOG_LEVEL: "ERROR" })
      assert.strictEqual(config.logLevel, "Error")
    }),
  )

  it.effect("empty environment values mean unset, including ATC_CONFIG", () =>
    Effect.gen(function* () {
      const config = yield* load({ ATC_CONFIG: "", ATC_PORT: "" })
      assert.strictEqual(config.configFile, join(home, ".config", "atc", "config.toml"))
      assert.strictEqual(config.port, DEFAULT_PORT)
    }),
  )

  it.effect("ATC_ENDPOINT and the context variables are captured verbatim", () =>
    Effect.gen(function* () {
      const config = yield* load({
        ATC_ENDPOINT: "http://127.0.0.1:9400",
        ATC_PROJECT_ID: "p-1",
        // Empty means unset, like every other environment value.
        ATC_WORKSPACE_ID: "",
      })
      assert.strictEqual(config.endpoint, "http://127.0.0.1:9400")
      assert.deepStrictEqual(config.context, {
        ATC_ENDPOINT: "http://127.0.0.1:9400",
        ATC_PROJECT_ID: "p-1",
      })
    }),
  )

  it.effect("absent context means no endpoint and an empty context", () =>
    Effect.gen(function* () {
      const config = yield* load({})
      assert.strictEqual(config.endpoint, undefined)
      assert.deepStrictEqual(config.context, {})
    }),
  )

  it.effect("a malformed ATC_ENDPOINT fails naming the variable", () =>
    Effect.gen(function* () {
      const malformed = yield* loadError({ ATC_ENDPOINT: "not a url" })
      assert.strictEqual(malformed.source, "ATC_ENDPOINT")
      assert.include(malformed.message, "http(s)")
      // Non-HTTP schemes are equally unusable as a base URL.
      const scheme = yield* loadError({ ATC_ENDPOINT: "file:///tmp/api" })
      assert.strictEqual(scheme.source, "ATC_ENDPOINT")
    }),
  )

  it.effect("a relative data directory is rejected (never cwd-dependent)", () =>
    Effect.gen(function* () {
      const error = yield* loadError({ ATC_DATA_DIR: "relative/data" })
      assert.include(error.message, "absolute")
      assert.include(error.source, "ATC_DATA_DIR")
    }),
  )

  it.effect("a relative config file path is rejected", () =>
    Effect.gen(function* () {
      const error = yield* loadError({ ATC_CONFIG: "relative.toml" })
      assert.include(error.message, "absolute")
    }),
  )

  it.effect("a relative state directory is rejected", () =>
    Effect.gen(function* () {
      const error = yield* loadError({ XDG_STATE_HOME: "relative-state" })
      assert.include(error.message, "absolute")
      assert.include(error.source, "XDG_STATE_HOME")
    }),
  )
})
