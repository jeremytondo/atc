import { assert } from "@effect/vitest"
import { BunHttpServer, BunServices } from "@effect/platform-bun"
import { Context, Effect, Exit, Layer, Scope, Stream } from "effect"
import type { Duration } from "effect"
import { HttpServer } from "effect/unstable/http"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import * as AgentRegistry from "../src/agents/agentRegistry.ts"
import * as AuthToken from "../src/platform/authToken.ts"
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
  isolatedEnv,
  spawnServe,
  spawnServeHealthy,
  trackTempDir,
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

/** The fixed bearer token the test AuthToken layer accepts. */
export const TEST_AUTH_TOKEN = "atc_test-token"

/** AuthToken over a fixed credential — no filesystem, deterministic. */
export const TestAuthTokenLayer = Layer.succeed(AuthToken.AuthToken)({
  verify: (authorization) => Effect.succeed(authorization === `Bearer ${TEST_AUTH_TOKEN}`),
})

/** A settled AppConfig for tests that only need a few fields overridden. */
export const testAppConfig = (overrides: Partial<AppConfig["Service"]>): Layer.Layer<AppConfig> =>
  Layer.succeed(AppConfig)({
    port: 0,
    bind: "127.0.0.1",
    endpoint: undefined,
    context: {},
    logLevel: "Info",
    configFile: "/dev/null",
    home: os.homedir(),
    dataDir: "/tmp",
    stateDir: "/tmp",
    dbFile: "/tmp/atc.db",
    tokenFile: "/tmp/atc-auth-token",
    logFile: "/tmp/atc.log",
    pidFile: "/tmp/atc.pid",
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
  configOverrides: Partial<AppConfig["Service"]> = {},
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
    | Events.Events
    | AuthToken.AuthToken,
    unknown
  >
} => {
  const fake = makeFakeAdapter()
  const fakeAgents = {
    codex: makeFakeAgentAdapter(),
    claude: makeFakeAgentAdapter({
      // Hooks-shaped: its observation feed dies silently with the TUI, so
      // stale-busy re-derivation stays reachable through this fake.
      observationOutlivesTui: false,
    }),
  }
  const base = Layer.mergeAll(
    ProjectRepository.layer,
    TerminalRepository.layer,
    ThreadRepository.layer,
  ).pipe(Layer.provide(Persistence.layerFile(dbFile)))
  const services = Layer.mergeAll(
    base,
    Directories.layer.pipe(Layer.provide(testAppConfig(configOverrides))),
    fake.layer,
    ClaudeHooks.layer,
  )
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
  const threads = Threads.layerWith(threadsOptions).pipe(
    Layer.provide([services, terminals, registry, eventsLayer]),
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
      TestAuthTokenLayer,
      threads,
      Projects.layer.pipe(Layer.provide([services, terminals, threads, eventsLayer])),
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
  // The env is rebuilt per attempt: it carries the port, and each isolatedEnv
  // call allocates a fresh state home, which must be the one the surviving
  // server actually used.
  let env: ReturnType<typeof isolatedEnv>
  const { proc, port, base } = await spawnServeHealthy((attemptPort) => {
    env = isolatedEnv(sandbox.base, {
      ATC_PORT: String(attemptPort),
      ATC_ZMX_EXECUTABLE: sandbox.wrapper,
      ...extraEnv,
    })
    return spawnServe([process.execPath, "src/main.ts"], attemptPort, appServerRoot, env)
  })
  return { base, port, proc, sandbox, env: env! }
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

/**
 * Build a server layer in its own closable scope — closable mid-test, and
 * the finalizer guarantees the listener dies even when an assertion fails
 * first. Returns the built context and the resolved loopback base URL.
 */
export const startServer = <R, E>(layer: Layer.Layer<R | HttpServer.HttpServer, E>) =>
  Effect.gen(function* () {
    const scope = yield* Scope.make()
    yield* Effect.addFinalizer(() => Scope.close(scope, Exit.void))
    const context = yield* Layer.build(layer).pipe(Effect.provideService(Scope.Scope, scope))
    const address = Context.get(context, HttpServer.HttpServer).address
    if (address._tag !== "TcpAddress") {
      return yield* Effect.die(`expected a TCP address, got ${address._tag}`)
    }
    return {
      scope,
      context,
      hostname: address.hostname,
      base: `http://127.0.0.1:${address.port}`,
    }
  })

/** Poll (real clock) until `predicate` accepts the effect's value; bounded. */
export const eventually = <A, E>(
  effect: Effect.Effect<A, E>,
  predicate: (value: A) => boolean,
  options?: { readonly attempts?: number; readonly interval?: Duration.Input },
) =>
  Effect.gen(function* () {
    const attempts = options?.attempts ?? 300
    for (let attempt = 0; ; attempt++) {
      const value = yield* effect
      if (predicate(value)) return value
      assert.isBelow(attempt, attempts, `condition never held; last: ${JSON.stringify(value)}`)
      yield* Effect.sleep(options?.interval ?? "10 millis")
    }
  })

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
  eventually(
    Effect.sync(() => sink.text),
    (text) => text.includes(pattern),
    { attempts: 100, interval: "50 millis" },
  )
