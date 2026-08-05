import { assert } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Duration, Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import type { AgentEvent } from "../../src/agents/agentAdapter.ts"
import * as ClaudeAdapter from "../../src/agents/claudeAdapter.ts"
import * as ClaudeHooks from "../../src/agents/claudeHooks.ts"
import * as CodexAdapter from "../../src/agents/codexAdapter.ts"
import * as CodexServer from "../../src/agents/codexServer.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { trackTempDir } from "../blackbox.ts"
import { testAppConfig } from "../testLayers.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"

// Shared scaffolding for agent-provider tests (ATC-123): the fake-codex
// sandbox (wrapper-script seam, like makeFakeZmxSandbox), the layer stacks
// under supervision/adapter tests, and the event-feed helpers every adapter
// test asserts with.

const fakeCodexFixture = fileURLToPath(
  new URL("../fixtures/fake-codex-listener.ts", import.meta.url),
)

/**
 * A per-test fake-codex sandbox: the wrapper script is the configured codex
 * executable, per-test fixture configuration is baked into it, and the
 * fixture records its pid for reap assertions.
 */
export const makeCodexSandbox = (vars: Record<string, string> = {}) => {
  const base = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-codex-")))
  const stateDir = path.join(base, "state")
  const cwd = path.join(base, "work")
  fs.mkdirSync(cwd, { recursive: true })
  const pidFile = path.join(base, "fixture.pid")
  const wrapper = path.join(base, "fake-codex")
  const assignments = Object.entries({ FAKE_CODEX_PID_FILE: pidFile, ...vars })
    .map(([key, value]) => `${key}='${value}'`)
    .join(" ")
  fs.writeFileSync(
    wrapper,
    `#!/bin/sh\n${assignments} exec "${process.execPath}" "${fakeCodexFixture}" "$@"\n`,
  )
  fs.chmodSync(wrapper, 0o755)
  return {
    base,
    stateDir,
    cwd,
    wrapper,
    pidFile,
    identityFile: path.join(stateDir, "codex-app-server.json"),
  }
}

/** The platform stack under every codex supervision/adapter test. */
const codexPlatform = (sandbox: { readonly stateDir: string; readonly wrapper: string }) =>
  Layer.mergeAll(
    Subprocess.layer.pipe(Layer.provide(BunServices.layer)),
    testAppConfig({ codexExecutable: sandbox.wrapper, stateDir: sandbox.stateDir }),
    TestBuildInfoLayer,
    BunServices.layer,
  )

/** The supervision module over a sandbox wrapper (codexServer tests). */
export const codexServerLayer = (
  sandbox: { readonly stateDir: string; readonly wrapper: string },
  options: CodexServer.CodexServerOptions = {},
): Layer.Layer<CodexServer.CodexServer, unknown> =>
  CodexServer.layerWith(options).pipe(Layer.provide(codexPlatform(sandbox)))

/** Adapter + supervision over a sandbox wrapper (codexAdapter tests). */
export const codexAdapterLayer = (sandbox: {
  readonly stateDir: string
  readonly wrapper: string
}): Layer.Layer<CodexAdapter.CodexAdapter | CodexServer.CodexServer, unknown> => {
  const platform = codexPlatform(sandbox)
  // `server` appears twice in the graph but is memoized to one instance.
  const server = CodexServer.layerWith({}).pipe(Layer.provide(platform))
  const adapter = CodexAdapter.layer.pipe(Layer.provide(server), Layer.provide(platform))
  return Layer.mergeAll(adapter, server)
}

/**
 * The Claude adapter stack (with ClaudeHooks exposed for webhook tests).
 * claudeExecutable defaults to /bin/echo so resolution succeeds without a
 * Claude install — fixture tests inject a scripted queryFn and never spawn.
 */
export const claudeAdapterLayer = (
  options: ClaudeAdapter.ClaudeAdapterOptions = {},
  executable = "/bin/echo",
): Layer.Layer<ClaudeAdapter.ClaudeAdapter | ClaudeHooks.ClaudeHooks, unknown> =>
  ClaudeAdapter.layerWith(options).pipe(
    Layer.provideMerge(ClaudeHooks.layer),
    Layer.provide(testAppConfig({ claudeExecutable: executable })),
    Layer.provide(Subprocess.layer),
    Layer.provideMerge(BunServices.layer),
  )

/** Collect an agent event feed into a sink in the background (scoped). */
export const collectAgentEvents = (events: Stream.Stream<AgentEvent, unknown>) =>
  Effect.gen(function* () {
    const sink: Array<AgentEvent> = []
    yield* events.pipe(
      Stream.runForEach((event) => Effect.sync(() => sink.push(event))),
      Effect.ignore,
      Effect.forkScoped,
    )
    return sink
  })

/** Poll (real clock) until the sink holds a matching event; bounded. */
export const waitForAgentEvent = (
  sink: Array<AgentEvent>,
  predicate: (event: AgentEvent) => boolean,
  options?: { readonly attempts?: number; readonly interval?: Duration.Input },
) =>
  Effect.gen(function* () {
    const attempts = options?.attempts ?? 200
    for (let attempt = 0; !sink.some(predicate); attempt++) {
      assert.isBelow(attempt, attempts, `never saw expected event in ${JSON.stringify(sink)}`)
      yield* Effect.sleep(options?.interval ?? "25 millis")
    }
  })
