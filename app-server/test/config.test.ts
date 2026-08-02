import { assert, describe, it } from "@effect/vitest"
import { BunFileSystem } from "@effect/platform-bun"
import { Effect } from "effect"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir, homedir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { DEFAULT_PORT } from "../src/api.ts"
import * as Config from "../src/config.ts"

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

const load = (env: Config.Env) => Config.load(env).pipe(Effect.provide(BunFileSystem.layer))

const loadError = (env: Config.Env) =>
  Config.load(env).pipe(Effect.flip, Effect.provide(BunFileSystem.layer))

describe("configuration", () => {
  it.effect("defaults: XDG locations and default port", () =>
    Effect.gen(function* () {
      const config = yield* load({})
      assert.strictEqual(config.port, DEFAULT_PORT)
      assert.strictEqual(config.logLevel, "Info")
      assert.strictEqual(config.configFile, join(homedir(), ".config", "atc", "config.toml"))
      assert.strictEqual(config.dataDir, join(homedir(), ".local", "share", "atc"))
      assert.strictEqual(config.stateDir, join(homedir(), ".local", "state", "atc"))
      assert.strictEqual(config.dbFile, join(homedir(), ".local", "share", "atc", "atc.db"))
      assert.strictEqual(config.logFile, join(homedir(), ".local", "state", "atc", "atc.log"))
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
      const file = writeConfig(`port = 9100\nlogLevel = "debug"\ndataDir = "${scratch}/d"\n`)
      const config = yield* load({ ATC_CONFIG: file })
      assert.strictEqual(config.port, 9100)
      assert.strictEqual(config.logLevel, "Debug")
      assert.strictEqual(config.dataDir, `${scratch}/d`)
      assert.strictEqual(config.dbFile, `${scratch}/d/atc.db`)
    }),
  )

  it.effect("environment beats the config file", () =>
    Effect.gen(function* () {
      const file = writeConfig(`port = 9100\nlogLevel = "debug"\n`)
      const config = yield* load({
        ATC_CONFIG: file,
        ATC_PORT: "9200",
        ATC_LOG_LEVEL: "warn",
        ATC_DATA_DIR: join(scratch, "env-data"),
      })
      assert.strictEqual(config.port, 9200)
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
})
