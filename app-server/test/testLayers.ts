import { assert } from "@effect/vitest"
import { Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import { AppConfig } from "../src/config.ts"
import * as Directories from "../src/directories.ts"
import * as Persistence from "../src/persistence.ts"
import * as ProjectRepository from "../src/projectRepository.ts"
import type { TerminalAdapter } from "../src/terminalAdapter.ts"
import * as TerminalRepository from "../src/terminalRepository.ts"
import * as Terminals from "../src/terminals.ts"
import {
  appServerRoot,
  freePort,
  isolatedEnv,
  makeShortSocketDir,
  spawnServe,
  trackTempDir,
  waitForHealth,
} from "./blackbox.ts"
import { makeFakeAdapter } from "./fakeTerminalAdapter.ts"
import type { FakeTerminalAdapter } from "./fakeTerminalAdapter.ts"

/**
 * Handler dependencies for in-process and ephemeral-listener tests: real
 * repositories over one fresh in-memory database, real directory checks,
 * and the fake in-memory terminal adapter (returned for failure injection
 * and assertions).
 */
export const makeTestServiceLayers = (): {
  readonly fake: FakeTerminalAdapter
  readonly layer: Layer.Layer<
    | ProjectRepository.ProjectRepository
    | TerminalRepository.TerminalRepository
    | Terminals.Terminals
    | Directories.Directories
    | TerminalAdapter,
    unknown
  >
} => {
  const fake = makeFakeAdapter()
  const base = Layer.mergeAll(ProjectRepository.layer, TerminalRepository.layer).pipe(
    Layer.provide(Persistence.layerFile(":memory:")),
  )
  const services = Layer.mergeAll(base, Directories.layer, fake.layer)
  return {
    fake,
    layer: Layer.mergeAll(services, Terminals.layer.pipe(Layer.provide(services))),
  }
}

export const TestRepositoryLayers = makeTestServiceLayers().layer

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

const fakeZmxFixture = fileURLToPath(new URL("fixtures/fake-zmx.ts", import.meta.url))

/**
 * A real `atc serve` (from source) whose zmx executable is the fake-zmx
 * wrapper: the deterministic full-stack seam for black-box terminal tests.
 * Kill `proc` (and await `proc.exited`) when done.
 */
export const startFakeZmxServer = async (extraEnv: Record<string, string> = {}) => {
  const sandbox = makeFakeZmxSandbox()
  const port = await freePort()
  const env = isolatedEnv(sandbox.base, {
    ATC_PORT: String(port),
    ATC_ZMX_EXECUTABLE: sandbox.wrapper,
    ...extraEnv,
  })
  const proc = spawnServe([process.execPath, "src/main.ts"], port, appServerRoot, env)
  await waitForHealth(`http://127.0.0.1:${port}`, proc)
  return { base: `http://127.0.0.1:${port}`, port, proc, sandbox, env }
}

/**
 * A per-test fake-zmx sandbox. The wrapper script is the configured "zmx
 * executable": the adapter owns argv and environment, so per-test fixture
 * configuration is baked into the wrapper itself — no process.env mutation
 * anywhere.
 */
export const makeFakeZmxSandbox = (vars: Record<string, string> = {}) => {
  const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-zmx-")))
  const stateDir = path.join(base, "state")
  const wrapper = path.join(base, "fake-zmx")
  const assignments = Object.entries({ FAKE_ZMX_STATE: stateDir, ...vars })
    .map(([key, value]) => `${key}='${value}'`)
    .join(" ")
  fs.writeFileSync(
    wrapper,
    `#!/bin/sh\n${assignments} exec "${process.execPath}" "${fakeZmxFixture}" "$@"\n`,
  )
  fs.chmodSync(wrapper, 0o755)
  return { base, stateDir, wrapper, socketDir: makeShortSocketDir() }
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
