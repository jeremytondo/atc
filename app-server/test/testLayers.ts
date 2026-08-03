import { assert } from "@effect/vitest"
import { Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import { AppConfig } from "../src/config.ts"
import * as Directories from "../src/directories.ts"
import * as Persistence from "../src/persistence.ts"
import * as ProjectRepository from "../src/projectRepository.ts"

// Handler dependencies for in-process and ephemeral-listener tests: a real
// repository over a fresh in-memory database, and real directory checks.
export const TestRepositoryLayers = Layer.mergeAll(
  ProjectRepository.layer.pipe(Layer.provide(Persistence.layerFile(":memory:"))),
  Directories.layer,
)

/** A settled AppConfig for tests that only need a few fields overridden. */
export const testAppConfig = (overrides: Partial<AppConfig["Service"]>): Layer.Layer<AppConfig> =>
  Layer.succeed(AppConfig)({
    port: 0,
    logLevel: "Info",
    configFile: "/dev/null",
    dataDir: "/tmp",
    stateDir: "/tmp",
    dbFile: "/tmp/atc.db",
    logFile: "/tmp/atc.log",
    zmxExecutable: "zmx",
    terminalSocketDir: "/tmp/atc-sockets",
    ...overrides,
  })

const tempDirs: Array<string> = []

/** Track `dir` for removal by `cleanupTempDirs` (call it from afterAll). */
export const trackTempDir = (dir: string): string => {
  tempDirs.push(dir)
  return dir
}

/**
 * A throwaway socket directory short enough for the unix socket-path budget
 * (~103 bytes) — macOS os.tmpdir() (/var/folders/…) is too deep, /tmp is not.
 */
export const makeShortSocketDir = (): string => trackTempDir(fs.mkdtempSync("/tmp/atc-zs-"))

export const cleanupTempDirs = (): void => {
  for (const dir of tempDirs.splice(0)) fs.rmSync(dir, { recursive: true, force: true })
}

/** Collect a byte stream into a text sink in the background (scoped). */
export const collectText = (output: Stream.Stream<Uint8Array, unknown>) =>
  Effect.gen(function* () {
    const decoder = new TextDecoder()
    const sink = { text: "" }
    yield* output.pipe(
      Stream.runForEach((chunk) =>
        Effect.sync(() => {
          sink.text += decoder.decode(chunk, { stream: true })
        }),
      ),
      Effect.ignore,
      Effect.forkScoped,
    )
    return sink
  })

/** Poll until `pattern` shows up in the sink; assertion-bounded. */
export const waitForText = (sink: { text: string }, pattern: string) =>
  Effect.gen(function* () {
    for (let attempt = 0; !sink.text.includes(pattern); attempt++) {
      assert.isBelow(attempt, 100, `never saw ${JSON.stringify(pattern)} in ${sink.text}`)
      yield* Effect.sleep("50 millis")
    }
  })
