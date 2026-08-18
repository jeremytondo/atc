import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Deferred, Effect, Fiber, Layer } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { SqlClient } from "effect/unstable/sql"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { V1Handlers } from "../../src/api/handlers.ts"
import { ThreadRuntime } from "../../src/threads/threadRuntime.ts"
import { applySettingsPatch } from "../../src/threads/threadSettings.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { eventually, makeTestServiceLayers } from "../testLayers.ts"

// Chat mode settings (ATC-205) through the public contract and the runtime,
// over the fake adapters: the settings patch and its per-model reasoning
// rule, the write-through agent defaults a new thread inherits, the settings
// a turn starts with, and provider-side changes adopted from the feed.

const kit = makeTestServiceLayers()
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
  kit.layer,
  BunHttpServer.layerHttpServices,
)

const scratch = mkdtempSync(join(tmpdir(), "atc-thread-settings-test-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)

const testClient = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const project = yield* client.v1.createProject({
    payload: { name: "Settings", defaultWorkingDirectory: realDir },
  })
  return { client, project }
})

describe("applySettingsPatch", () => {
  const catalog = [
    {
      value: "big",
      displayName: "Big",
      description: "",
      isDefault: true,
      supportedEffortLevels: ["low", "medium", "high", "xhigh"] as const,
      defaultEffortLevel: "medium" as const,
    },
    {
      value: "mid",
      displayName: "Mid",
      description: "",
      isDefault: false,
      supportedEffortLevels: ["low", "medium"] as const,
      defaultEffortLevel: "low" as const,
    },
    {
      value: "tiny",
      displayName: "Tiny",
      description: "",
      isDefault: false,
      supportedEffortLevels: [] as const,
    },
  ]
  const current = { model: "big", reasoning: "xhigh", mode: "chat", access: "auto" } as const

  it("a model change keeps a supported level, else resets to the new model's default", () => {
    assert.deepStrictEqual(applySettingsPatch(current, { model: "mid" }, catalog), {
      settings: { model: "mid", reasoning: "low", mode: "chat", access: "auto" },
    })
    assert.deepStrictEqual(
      applySettingsPatch({ ...current, reasoning: "low" }, { model: "mid" }, catalog),
      { settings: { model: "mid", reasoning: "low", mode: "chat", access: "auto" } },
    )
    // No effort support: reasoning disappears.
    assert.deepStrictEqual(applySettingsPatch(current, { model: "tiny" }, catalog), {
      settings: { model: "tiny", mode: "chat", access: "auto" },
    })
  })

  it("rejects an unknown model and an unsupported level; mode/access need no catalog", () => {
    assert.deepStrictEqual(applySettingsPatch(current, { model: "nope" }, catalog), {
      rejected: 'unknown model "nope"',
    })
    assert.deepStrictEqual(applySettingsPatch(current, { reasoning: "ultra" }, catalog), {
      rejected: 'model "big" does not support reasoning "ultra"',
    })
    assert.deepStrictEqual(
      applySettingsPatch(current, { mode: "plan", access: "supervised" }, null),
      { settings: { model: "big", reasoning: "xhigh", mode: "plan", access: "supervised" } },
    )
    // A thread already on an unlisted model keeps taking patches; only a
    // CHANGE to an unknown model is held to the catalog.
    assert.deepStrictEqual(
      applySettingsPatch({ ...current, model: "retired" }, { reasoning: "low" }, catalog),
      { settings: { model: "retired", reasoning: "low", mode: "chat", access: "auto" } },
    )
  })
})

describe("thread settings", () => {
  it.effect("patches settings per thread; a model change is validated against the catalog", () =>
    Effect.gen(function* () {
      const { client, project } = yield* testClient
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      // A fresh install: the registry seed, until a thread changes something.
      assert.deepStrictEqual(thread.settings, {
        model: "gpt-5.6-sol",
        reasoning: "high",
        mode: "chat",
        access: "auto",
      })

      // An empty settings object is an empty patch: nothing changes,
      // updatedAt included.
      const empty = yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: {} },
      })
      assert.strictEqual(empty.updatedAt, thread.updatedAt)

      // Access and mode never touch the catalog.
      const reads = kit.fakeAgents.codex.modelReads()
      const patched = yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { access: "supervised", mode: "plan" } },
      })
      assert.deepStrictEqual(patched.settings, {
        model: "gpt-5.6-sol",
        reasoning: "high",
        mode: "plan",
        access: "supervised",
      })
      assert.strictEqual(kit.fakeAgents.codex.modelReads(), reads)

      // A model change consults the catalog (once — cached after) and is
      // validated BEFORE anything is written: the rename in the same patch
      // never lands when the settings are rejected.
      const rejected = yield* Effect.flip(
        client.v1.updateThread({
          params: { threadId: thread.id },
          payload: { name: "Renamed", settings: { model: "nope" } },
        }),
      )
      assert.strictEqual(rejected._tag, "InvalidThreadSettings")
      const untouched = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.isUndefined(untouched.name)
      const switched = yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { name: "Renamed", settings: { model: "fake-large" } },
      })
      assert.strictEqual(switched.name, "Renamed")
      assert.strictEqual(switched.settings.model, "fake-large")
      assert.strictEqual(kit.fakeAgents.codex.modelReads(), reads + 1)

      // Persisted: a fresh read agrees.
      const fetched = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.deepStrictEqual(fetched.settings, switched.settings)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("the last changed thread's settings become the agent's defaults", () =>
    Effect.gen(function* () {
      const { client, project } = yield* testClient
      const first = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "claude-code" },
      })
      yield* client.v1.updateThread({
        params: { threadId: first.id },
        payload: { settings: { model: "fake-large", reasoning: "low", access: "fullAccess" } },
      })
      const agent = yield* client.v1.getAgent({ params: { agentId: "claude-code" } })
      assert.deepStrictEqual(agent.defaults, {
        model: "fake-large",
        reasoning: "low",
        mode: "chat",
        access: "fullAccess",
      })
      const second = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "claude-code" },
      })
      assert.deepStrictEqual(second.settings, agent.defaults)
      // Per agent: codex is untouched by a claude change.
      const codex = yield* client.v1.getAgent({ params: { agentId: "codex" } })
      assert.notStrictEqual(codex.defaults.access, "fullAccess")
    }).pipe(Effect.provide(TestLayer)),
  )

  // it.live: the test polls (`eventually`) on the real clock.
  it.live("concurrent patches merge rather than overwrite; a no-op patch writes nothing", () =>
    Effect.gen(function* () {
      const { client, project } = yield* testClient
      const fake = kit.fakeAgents.claude
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "claude-code" },
      })
      // Park the catalog read the model patch needs, so a mode patch lands
      // between that patch's read of the row and its write.
      const gate = yield* Deferred.make<void>()
      fake.gateModels(Deferred.await(gate))
      const modelPatch = yield* Effect.forkChild(
        client.v1.updateThread({
          params: { threadId: thread.id },
          payload: { settings: { model: "fake-large" } },
        }),
      )
      yield* eventually(
        Effect.sync(() => fake.modelReads()),
        (reads) => reads >= 1,
      )
      const modePatch = yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { mode: "plan" } },
      })
      assert.strictEqual(modePatch.settings.mode, "plan")
      yield* Deferred.succeed(gate, undefined)
      fake.gateModels(Effect.void)
      // The model patch lost the write race and re-applied itself on top:
      // both changes stand.
      const merged = yield* Fiber.join(modelPatch)
      assert.strictEqual(merged.settings.model, "fake-large")
      assert.strictEqual(merged.settings.mode, "plan")

      // Another thread of the agent takes the defaults on; re-picking what
      // the first already holds must not write it back (updatedAt) nor
      // reset the defaults to it.
      const other = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "claude-code" },
      })
      yield* client.v1.updateThread({
        params: { threadId: other.id },
        payload: { settings: { access: "supervised" } },
      })
      const repicked = yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { mode: "plan", model: "fake-large" } },
      })
      assert.strictEqual(repicked.updatedAt, merged.updatedAt)
      const agent = yield* client.v1.getAgent({ params: { agentId: "claude-code" } })
      assert.strictEqual(agent.defaults.access, "supervised")
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("a row holding a literal this build does not know still takes patches", () =>
    Effect.gen(function* () {
      const { client, project } = yield* testClient
      const sql = yield* SqlClient.SqlClient
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      // A newer build wrote a mode this one cannot decode: the read coerces
      // it (settingsFromColumns), and the compare-and-swap must still match
      // the row — the guard names the literal as stored, not as read.
      yield* sql`UPDATE threads SET mode = 'from-the-future' WHERE id = ${thread.id}`
      const coerced = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.strictEqual(coerced.settings.mode, "chat")
      const patched = yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { access: "supervised" } },
      })
      assert.strictEqual(patched.settings.access, "supervised")
      // The write normalized the row: the unknown literal is gone.
      const rows = yield* sql<{ mode: string }>`SELECT mode FROM threads WHERE id = ${thread.id}`
      assert.deepStrictEqual(rows, [{ mode: "chat" }])
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("a turn starts with the thread's stored settings, and provider reports are adopted", () =>
    Effect.gen(function* () {
      const { client, project } = yield* testClient
      const runtime = yield* ThreadRuntime
      const fake = kit.fakeAgents.codex
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { access: "autoAcceptEdits" } },
      })
      const started = yield* runtime.prompt(thread.id, "hello")
      assert.isString(started.turnId)
      const session = [...fake.sessions.values()].find((entry) => entry.inputs.includes("hello"))
      assert.isDefined(session)
      assert.deepStrictEqual(session!.turnSettings, [
        { model: "gpt-5.6-sol", reasoning: "high", mode: "chat", access: "autoAcceptEdits" },
      ])

      // A change made while the turn runs is stored, not pushed: the turn
      // in flight is untouched, and the next turn carries it.
      yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { mode: "plan" } },
      })
      assert.strictEqual(session!.turnSettings.length, 1)
      fake.completeTurn(session!.providerSessionId, "completed")
      yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (current) => current.activityState === "idle",
      )
      yield* runtime.prompt(thread.id, "again")
      yield* eventually(
        Effect.sync(() => session!.turnSettings.length),
        (count) => count === 2,
      )
      assert.deepStrictEqual(session!.turnSettings[1], {
        model: "gpt-5.6-sol",
        reasoning: "high",
        mode: "plan",
        access: "autoAcceptEdits",
      })

      // A change made while this turn runs is stored; the provider's late
      // confirmation of what the turn was started with must not roll it
      // back — but a genuine provider-side change (a TUI switching the
      // model) still wins as it arrives.
      yield* client.v1.updateThread({
        params: { threadId: thread.id },
        payload: { settings: { access: "supervised" } },
      })
      fake.emitSettings(session!.providerSessionId, {
        model: "gpt-5.6-sol",
        reasoning: "high",
        mode: "plan",
        access: "autoAcceptEdits",
      })
      fake.emitSettings(session!.providerSessionId, { model: "fake-large", reasoning: null })
      const adopted = yield* eventually(
        client.v1.getThread({ params: { threadId: thread.id } }),
        (current) => current.settings.model === "fake-large",
      )
      assert.deepStrictEqual(adopted.settings, {
        model: "fake-large",
        mode: "plan",
        access: "supervised",
      })
      fake.completeTurn(session!.providerSessionId, "completed")
    }).pipe(Effect.provide(TestLayer)),
  )
})
