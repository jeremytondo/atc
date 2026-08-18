import { BunServices } from "@effect/platform-bun"
import { Duration, Effect, Layer, Stream } from "effect"
import * as fs from "node:fs"
import * as path from "node:path"
import { fileURLToPath } from "node:url"
import type { AgentEvent, ThreadSettings } from "../../src/agents/agentAdapter.ts"
import * as ClaudeAdapter from "../../src/agents/claudeAdapter.ts"
import * as ClaudeHooks from "../../src/agents/claudeHooks.ts"
import * as CodexAdapter from "../../src/agents/codexAdapter.ts"
import * as CodexServer from "../../src/agents/codexServer.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import * as UnixWebSocket from "../../src/platform/unixWebSocket.ts"
import { trackTempDir } from "../blackbox.ts"
import { eventually, testAppConfig } from "../testLayers.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"

// Shared scaffolding for agent-provider tests (ATC-123): the fake-codex
// sandbox (wrapper-script seam, like makeFakeZmxSandbox), the layer stacks
// under supervision/adapter tests, and the event-feed helpers every adapter
// test asserts with.

/** The settings every adapter test starts turns with unless it says otherwise. */
export const TEST_SETTINGS: ThreadSettings = {
  model: "test-model",
  reasoning: "medium",
  mode: "chat",
  access: "auto",
}

const fakeCodexFixture = fileURLToPath(
  new URL("../fixtures/fake-codex-listener.ts", import.meta.url),
)

/**
 * A per-test fake-codex sandbox: the wrapper script is the configured codex
 * executable, per-test fixture configuration is baked into it, and the
 * fixture records its pid for reap assertions. Its CODEX_HOME is a private
 * directory (baked into the wrapper AND settled into AppConfig, as one
 * environment would in production), so the well-known socket the fixture
 * binds and ATC dials is the sandbox's own — never the developer's. The
 * base lives under /tmp, short enough for the unix socket-path budget.
 */
export const makeCodexSandbox = (vars: Record<string, string> = {}) => {
  const base = trackTempDir(fs.mkdtempSync("/tmp/atc-codex-"))
  const stateDir = path.join(base, "state")
  const codexHome = path.join(base, "codex")
  const cwd = path.join(base, "work")
  fs.mkdirSync(cwd, { recursive: true })
  const pidFile = path.join(base, "fixture.pid")
  const wrapper = path.join(base, "fake-codex")
  const assignments = Object.entries({
    FAKE_CODEX_PID_FILE: pidFile,
    CODEX_HOME: codexHome,
    ...vars,
  })
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
    codexHome,
    socketPath: CodexServer.controlSocketPath(codexHome),
    cwd,
    wrapper,
    pidFile,
    /** The argv the most recently launched listener saw. */
    listenRecord: path.join(base, "listen-record.json"),
    identityFile: path.join(stateDir, "codex-app-server.json"),
  }
}

export interface CodexSandbox {
  readonly stateDir: string
  readonly codexHome: string
  readonly wrapper: string
}

/** The platform stack under every codex supervision/adapter test. */
const codexPlatform = (sandbox: CodexSandbox) =>
  Layer.mergeAll(
    Subprocess.layer.pipe(Layer.provide(BunServices.layer)),
    testAppConfig({
      codexExecutable: sandbox.wrapper,
      codexHome: sandbox.codexHome,
      stateDir: sandbox.stateDir,
    }),
    TestBuildInfoLayer,
    BunServices.layer,
  )

/** The supervision module over a sandbox wrapper (codexServer tests). */
export const codexServerLayer = (
  sandbox: CodexSandbox,
  options: CodexServer.CodexServerOptions = {},
): Layer.Layer<CodexServer.CodexServer, unknown> =>
  CodexServer.layerWith(options).pipe(Layer.provide(codexPlatform(sandbox)))

/** Adapter + supervision over a sandbox wrapper (codexAdapter tests). */
export const codexAdapterLayer = (
  sandbox: CodexSandbox,
): Layer.Layer<CodexAdapter.CodexAdapter | CodexServer.CodexServer, unknown> => {
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
  configOverrides: Partial<Parameters<typeof testAppConfig>[0]> = {},
  hooksLayer: Layer.Layer<ClaudeHooks.ClaudeHooks> = ClaudeHooks.layer,
): Layer.Layer<ClaudeAdapter.ClaudeAdapter | ClaudeHooks.ClaudeHooks, unknown> =>
  // No transcript unless a test scripts one: history reads must never
  // reach the developer's real ~/.claude.
  ClaudeAdapter.layerWith({ sessionMessagesFn: () => Promise.resolve([]), ...options }).pipe(
    Layer.provideMerge(hooksLayer),
    Layer.provide(testAppConfig({ claudeExecutable: executable, ...configOverrides })),
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
  eventually(
    Effect.sync(() => sink),
    (events) => events.some(predicate),
    { attempts: options?.attempts ?? 200, interval: options?.interval ?? "25 millis" },
  )

/**
 * A second client of the shared fake app-server — the TUI stand-in in
 * adapter tests. Scoped: closing the scope closes the connection.
 */
export interface ExternalClient {
  readonly send: (frame: unknown) => void
  /** One JSON-RPC request; resolves with its `result` (empty when absent). */
  readonly request: (
    id: number,
    method: string,
    params: unknown,
  ) => Effect.Effect<Record<string, unknown>>
  /** `thread/start` for `cwd`, resolving with the new thread id. */
  readonly startThread: (id: number, cwd: string) => Effect.Effect<string>
  readonly close: () => void
}

export const openExternal = (socketPath: string) =>
  Effect.acquireRelease(
    Effect.callback<ExternalClient>((resume) => {
      const socket = UnixWebSocket.open(socketPath)
      const pending = new Map<number, (reply: Effect.Effect<Record<string, unknown>>) => void>()
      socket.onmessage = (text) => {
        const message = JSON.parse(text) as { id?: number; result?: unknown }
        if (message.id === undefined) return
        const waiter = pending.get(message.id)
        if (waiter === undefined) return
        pending.delete(message.id)
        waiter(Effect.succeed((message.result ?? {}) as Record<string, unknown>))
      }
      const request = (id: number, method: string, params: unknown) =>
        Effect.callback<Record<string, unknown>>((resumeRequest) => {
          pending.set(id, resumeRequest)
          socket.send(JSON.stringify({ id, method, params }))
          return Effect.sync(() => pending.delete(id))
        })
      const client: ExternalClient = {
        send: (frame) => socket.send(JSON.stringify(frame)),
        request,
        startThread: (id, cwd) =>
          request(id, "thread/start", { cwd }).pipe(
            Effect.map((result) => (result["thread"] as { id: string }).id),
          ),
        close: () => socket.close(),
      }
      socket.onopen = () => resume(Effect.succeed(client))
      // A closed socket fails the open AND every request still waiting —
      // a test must fail, never hang, when the fake server goes away.
      socket.onclose = (reason) => {
        const failure = Effect.die(new Error(`external client: ${reason}`))
        resume(failure)
        for (const waiter of pending.values()) waiter(failure)
        pending.clear()
      }
    }),
    (client) => Effect.sync(() => client.close()),
  )
