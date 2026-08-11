import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer, BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import * as AgentRegistry from "../../src/agents/agentRegistry.ts"
import * as ClaudeAdapter from "../../src/agents/claudeAdapter.ts"
import * as CodexAdapter from "../../src/agents/codexAdapter.ts"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { Api } from "../../src/api/contract.ts"
import { V1Handlers } from "../../src/api/handlers.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { makeTestServiceLayers, testAppConfig } from "../testLayers.ts"
import { makeFakeAgentAdapter } from "./fakeAgentAdapter.ts"

// The read-only agents registry through the public contract (ATC-140):
// demand-driven availability detection over the fake adapters, unknown
// slugs 404. Nothing is persisted anywhere.

const kit = makeTestServiceLayers()
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
  kit.layer,
  BunHttpServer.layerHttpServices,
)

describe("/api/v1/agents", () => {
  it.effect("lists both built-in agents with availability", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const agents = yield* client.v1.listAgents()
      assert.deepStrictEqual(
        agents.map(({ id }) => id),
        ["codex", "claude-code"],
      )
      // /bin/echo resolves, so both are available.
      for (const agent of agents) {
        assert.isTrue(agent.available)
        assert.isUndefined(agent.reason)
      }
      assert.deepStrictEqual(
        yield* client.v1.getAgent({ params: { agentId: "claude-code" } }),
        agents[1],
      )
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("adapterFor hands back the adapter behind the slug", () =>
    Effect.gen(function* () {
      const registry = yield* AgentRegistry.AgentRegistry
      assert.strictEqual(registry.adapterFor("codex"), kit.fakeAgents.codex.adapter)
      assert.strictEqual(registry.adapterFor("claude-code"), kit.fakeAgents.claude.adapter)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("an unknown slug is AgentNotFound", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const error = yield* Effect.flip(client.v1.getAgent({ params: { agentId: "gpt" } }))
      assert.strictEqual(error._tag, "AgentNotFound")
      if (error._tag === "AgentNotFound") assert.strictEqual(error.agentId, "gpt")
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("a missing executable reports unavailable with the actionable hint", () =>
    Effect.gen(function* () {
      const registry = yield* AgentRegistry.AgentRegistry
      const agent = yield* registry.get("codex")
      assert.isFalse(agent.available)
      assert.include(agent.reason ?? "", "codexExecutable")
      assert.isUndefined(agent.detectedVersion)
    }).pipe(
      Effect.provide(
        AgentRegistry.layer.pipe(
          Layer.provide([
            Layer.succeed(CodexAdapter.CodexAdapter)(makeFakeAgentAdapter().adapter),
            Layer.succeed(ClaudeAdapter.ClaudeAdapter)(makeFakeAgentAdapter().adapter),
            testAppConfig({
              codexExecutable: "/definitely/not/codex",
              claudeExecutable: "/definitely/not/claude",
            }),
            Subprocess.layer.pipe(Layer.provide(BunServices.layer)),
          ]),
        ),
      ),
    ),
  )
})
