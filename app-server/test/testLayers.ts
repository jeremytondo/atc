import { assert } from "@effect/vitest"
import { BunHttpServer, BunServices } from "@effect/platform-bun"
import { Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import * as AgentRegistry from "../src/agents/agentRegistry.ts"
import * as ClaudeAdapter from "../src/agents/claudeAdapter.ts"
import * as ClaudeHooks from "../src/agents/claudeHooks.ts"
import * as CodexAdapter from "../src/agents/codexAdapter.ts"
import { AppConfig } from "../src/platform/config.ts"
import * as Directories from "../src/platform/directories.ts"
import * as Events from "../src/events/events.ts"
import * as Persistence from "../src/platform/persistence.ts"
import * as ProjectRepository from "../src/projects/projectRepository.ts"
import * as Projects from "../src/projects/projects.ts"
import * as Subprocess from "../src/platform/subprocess.ts"
import type { TerminalAdapter } from "../src/terminals/terminalAdapter.ts"
import * as TerminalRepository from "../src/terminals/terminalRepository.ts"
import * as Terminals from "../src/terminals/terminals.ts"
import * as ThreadRepository from "../src/threads/threadRepository.ts"
import * as Threads from "../src/threads/threads.ts"
import * as Zmx from "../src/terminals/zmxAdapter.ts"
import { V1Handlers } from "../src/api/handlers.ts"
import {
  appServerRoot,
  freePort,
  isolatedEnv,
  spawnServe,
  trackTempDir,
  waitForHealth,
} from "./blackbox.ts"
import { TestBuildInfoLayer } from "./testBuildInfo.ts"
import { makeFakeAdapter } from "./terminals/fakeTerminalAdapter.ts"
import type { FakeTerminalAdapter } from "./terminals/fakeTerminalAdapter.ts"
import { makeFakeAgentAdapter } from "./agents/fakeAgentAdapter.ts"
import type { FakeAgentAdapter } from "./agents/fakeAgentAdapter.ts"

type ServiceLayer = Layer.Layer<
  | ProjectRepository.ProjectRepository
  | TerminalRepository.TerminalRepository
  | ThreadRepository.ThreadRepository
  | Directories.Directories
  | TerminalAdapter
  | ClaudeHooks.ClaudeHooks,
  unknown
>

/** A settled AppConfig for tests that only need a few fields overridden. */
export const testAppConfig = (overrides: Partial<AppConfig["Service"]>): Layer.Layer<AppConfig> =>
  Layer.succeed(AppConfig)({
    port: 0,
    endpoint: undefined,
    context: {},
    logLevel: "Info",
    configFile: "/dev/null",
    dataDir: "/tmp",
    stateDir: "/tmp",
    dbFile: "/tmp/atc.db",
    logFile: "/tmp/atc.log",
    zmxExecutable: "zmx",
    codexExecutable: "codex",
    claudeExecutable: "claude",
    terminalSocketDir: "/tmp/atc-sockets",
    ...overrides,
  })

/**
 * Handler dependencies for in-process and ephemeral-listener tests: real
 * repositories over one fresh database (in-memory unless a file is given),
 * real directory checks, and the fake in-memory adapters (returned for
 * failure injection and assertions). Both agent slugs resolve to fake
 * adapters; the registry detects availability against /bin/echo so no
 * provider install is ever consulted. `services` omits Terminals so
 * startup-reconciliation tests can seed rows before its layer builds.
 */
export const makeTestServiceLayers = (
  dbFile = ":memory:",
  threadsOptions: Threads.ThreadsOptions = {},
  eventsOptions: Events.EventsOptions = {},
): {
  readonly fake: FakeTerminalAdapter
  readonly fakeAgents: { readonly codex: FakeAgentAdapter; readonly claude: FakeAgentAdapter }
  readonly services: ServiceLayer
  readonly layer: Layer.Layer<
    | Layer.Success<ServiceLayer>
    | Terminals.Terminals
    | Threads.Threads
    | Projects.Projects
    | AgentRegistry.AgentRegistry
    | Events.Events,
    unknown
  >
} => {
  const fake = makeFakeAdapter()
  // Distinct capability reports so a swapped slug→adapter mapping cannot
  // pass the registry tests.
  const fakeAgents = {
    codex: makeFakeAgentAdapter(),
    claude: makeFakeAgentAdapter({
      capabilities: { testedVersion: "0.0.0-fake-claude", tuiObservation: "hooks" },
    }),
  }
  const base = Layer.mergeAll(
    ProjectRepository.layer,
    TerminalRepository.layer,
    ThreadRepository.layer,
  ).pipe(Layer.provide(Persistence.layerFile(dbFile)))
  const services = Layer.mergeAll(base, Directories.layer, fake.layer, ClaudeHooks.layer)
  const eventsLayer = Events.layerWith(eventsOptions)
  const terminals = Terminals.layer.pipe(Layer.provide([services, eventsLayer]))
  const registry = AgentRegistry.layer.pipe(
    Layer.provide([
      Layer.succeed(CodexAdapter.CodexAdapter)(fakeAgents.codex.adapter),
      Layer.succeed(ClaudeAdapter.ClaudeAdapter)(fakeAgents.claude.adapter),
      testAppConfig({ codexExecutable: "/bin/echo", claudeExecutable: "/bin/echo" }),
      Subprocess.layer.pipe(Layer.provide(BunServices.layer)),
    ]),
  )
  return {
    fake,
    fakeAgents,
    services,
    layer: Layer.mergeAll(
      services,
      terminals,
      registry,
      eventsLayer,
      Threads.layerWith(threadsOptions).pipe(
        Layer.provide([services, terminals, registry, eventsLayer]),
      ),
      Projects.layer.pipe(Layer.provide([services, eventsLayer])),
    ),
  }
}

export const TestRepositoryLayers = makeTestServiceLayers().layer

type TestKitLayer = ReturnType<typeof makeTestServiceLayers>["layer"]

/**
 * The one HttpApiTest layer stack for in-process API tests over a kit:
 * handlers wired to the kit's services, the services themselves (so tests
 * reach the same instances the handlers use), and the HTTP test services.
 */
export const apiTestLayer = (kit: { readonly layer: TestKitLayer }) =>
  Layer.mergeAll(
    V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
    kit.layer,
    BunHttpServer.layerHttpServices,
  )

/** The full zmx-adapter layer stack shared by every in-process adapter test. */
export const zmxAdapterLayer = (options: {
  readonly zmxExecutable: string
  readonly terminalSocketDir: string
  readonly zmx?: Zmx.ZmxOptions
}): Layer.Layer<TerminalAdapter, unknown> =>
  Zmx.layerWith(options.zmx ?? {}).pipe(
    Layer.provide(
      testAppConfig({
        zmxExecutable: options.zmxExecutable,
        terminalSocketDir: options.terminalSocketDir,
      }),
    ),
    Layer.provide(Subprocess.layer),
    Layer.provideMerge(BunServices.layer),
  )

const fakeZmxFixture = fileURLToPath(new URL("fixtures/fake-zmx.ts", import.meta.url))

/**
 * A real `atc serve` (from source) whose zmx executable is the fake-zmx
 * wrapper: the deterministic full-stack seam for black-box terminal tests.
 * Kill `proc` (and await `proc.exited`) when done — or use
 * `withFakeZmxServer`, which does it for you.
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

/** Run `use` against a fake-zmx `atc serve`, always reaping the process. */
export const withFakeZmxServer = async (
  use: (server: Awaited<ReturnType<typeof startFakeZmxServer>>) => Promise<void>,
  extraEnv: Record<string, string> = {},
): Promise<void> => {
  const server = await startFakeZmxServer(extraEnv)
  try {
    await use(server)
  } finally {
    server.proc.kill()
    await server.proc.exited
  }
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
  return { base, stateDir, wrapper }
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
