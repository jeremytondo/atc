import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Fiber, Layer, Queue, Stream } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { realpathSync } from "node:fs"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { V1Handlers } from "../../src/api/handlers.ts"
import * as Events from "../../src/events/events.ts"
import { TestBuildInfoLayer, testBuildInfo } from "../testBuildInfo.ts"
import { TestRepositoryLayers } from "../testLayers.ts"

// In-process contract tests: the same encode/route/decode pipeline as the real
// server, but no listener. TestRepositoryLayers is merged in as well (one
// memoized instance) so tests can reach the same domain services the handlers
// use — the events test drives the Events service directly.
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
  TestRepositoryLayers,
  BunHttpServer.layerHttpServices,
)

describe("/api/v1", () => {
  it.effect("health returns ok", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const health = yield* client.v1.health()
      assert.deepStrictEqual(health, { status: "ok" })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("version reports injected build metadata", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const version = yield* client.v1.version()
      assert.deepStrictEqual(version, {
        version: testBuildInfo.version,
        apiVersion: "v1",
        commit: testBuildInfo.commit,
        builtAt: testBuildInfo.builtAt,
      })
    }).pipe(Effect.provide(TestLayer)),
  )
})

// Real directories for validation coverage. macOS tmpdir is itself behind a
// symlink, so expectations always go through realpathSync.
const scratch = mkdtempSync(join(tmpdir(), "atc-api-test-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

const realDir = realpathSync(scratch)
const linkedDir = join(scratch, "linked")
symlinkSync(realDir, linkedDir)
const filePath = join(scratch, "a-file")
writeFileSync(filePath, "not a directory")
mkdirSync(join(scratch, "second"))

describe("/api/v1/projects", () => {
  it.effect("creates, lists, gets, updates, and deletes a project", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])

      // Create canonicalizes the symlinked directory to its real path.
      const created = yield* client.v1.createProject({
        payload: { name: "Atelier", defaultWorkingDirectory: linkedDir },
      })
      assert.strictEqual(created.name, "Atelier")
      assert.strictEqual(created.defaultWorkingDirectory, realDir)
      assert.match(
        created.id,
        /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$/,
        "UUIDv7 id",
      )
      assert.strictEqual(created.createdAt, created.updatedAt)

      assert.deepStrictEqual(yield* client.v1.listProjects(), [created])
      assert.deepStrictEqual(
        yield* client.v1.getProject({ params: { projectId: created.id } }),
        created,
      )

      const renamed = yield* client.v1.updateProject({
        params: { projectId: created.id },
        payload: { name: "Renamed" },
      })
      assert.strictEqual(renamed.name, "Renamed")
      assert.strictEqual(renamed.defaultWorkingDirectory, realDir)
      assert.isAtLeast(Date.parse(renamed.updatedAt), Date.parse(created.updatedAt))

      const moved = yield* client.v1.updateProject({
        params: { projectId: created.id },
        payload: { defaultWorkingDirectory: join(realDir, "second") },
      })
      assert.strictEqual(moved.name, "Renamed")
      assert.strictEqual(moved.defaultWorkingDirectory, join(realDir, "second"))

      yield* client.v1.deleteProject({ params: { projectId: created.id } })
      assert.deepStrictEqual(yield* client.v1.listProjects(), [])
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("unknown ids are ProjectNotFound on get, update, and delete", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const attempts: ReadonlyArray<Effect.Effect<unknown, unknown>> = [
        client.v1.getProject({ params: { projectId: "missing" } }),
        client.v1.updateProject({ params: { projectId: "missing" }, payload: { name: "x" } }),
        client.v1.deleteProject({ params: { projectId: "missing" } }),
      ]
      for (const attempt of attempts) {
        const error = (yield* Effect.flip(attempt)) as { _tag: string; projectId?: string }
        assert.strictEqual(error._tag, "ProjectNotFound")
        assert.strictEqual(error.projectId, "missing")
      }
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("rejects unusable directories with the tagged state, touching nothing", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])

      const missing = yield* Effect.flip(
        client.v1.createProject({
          payload: { name: "P", defaultWorkingDirectory: join(realDir, "nope") },
        }),
      )
      assert.strictEqual(missing._tag, "DirectoryUnavailable")
      if (missing._tag === "DirectoryUnavailable") assert.strictEqual(missing.state, "missing")

      const notDirectory = yield* Effect.flip(
        client.v1.createProject({ payload: { name: "P", defaultWorkingDirectory: filePath } }),
      )
      assert.strictEqual(notDirectory._tag, "DirectoryUnavailable")
      if (notDirectory._tag === "DirectoryUnavailable") {
        assert.strictEqual(notDirectory.state, "not_directory")
      }

      // A failed create leaves nothing behind, and an update rejection leaves
      // the stored directory unchanged.
      assert.deepStrictEqual(yield* client.v1.listProjects(), [])
      const created = yield* client.v1.createProject({
        payload: { name: "P", defaultWorkingDirectory: realDir },
      })
      const rejected = yield* Effect.flip(
        client.v1.updateProject({
          params: { projectId: created.id },
          payload: { defaultWorkingDirectory: join(realDir, "nope") },
        }),
      )
      assert.strictEqual(rejected._tag, "DirectoryUnavailable")
      assert.deepStrictEqual(
        yield* client.v1.getProject({ params: { projectId: created.id } }),
        created,
      )
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("restricts project deletion while terminals exist, tombstones included", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const project = yield* client.v1.createProject({
        payload: { name: "Guarded", defaultWorkingDirectory: realDir },
      })
      const terminal = yield* client.v1.createTerminal({
        payload: { projectId: project.id },
      })
      const restricted = yield* Effect.flip(
        client.v1.deleteProject({ params: { projectId: project.id } }),
      )
      assert.strictEqual(restricted._tag, "ProjectHasTerminals")
      if (restricted._tag === "ProjectHasTerminals") {
        assert.strictEqual(restricted.terminalCount, 1)
      }
      // Deleting the terminal (record and session) releases the project.
      yield* client.v1.deleteTerminal({ params: { terminalId: terminal.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("rejects relative paths at the schema boundary", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const error = yield* Effect.flip(
        client.v1.createProject({
          payload: { name: "P", defaultWorkingDirectory: "relative/path" },
        }),
      )
      // Encoding fails client-side before any request: the contract's
      // AbsolutePath check applies to the typed client too.
      assert.strictEqual(error._tag, "SchemaError")
    }).pipe(Effect.provide(TestLayer)),
  )
})

describe("/api/v1/events", () => {
  // it.live: SSE delivery crosses the in-process HTTP pipeline's promise
  // boundaries, so the queue take below needs the real clock, not
  // it.effect's TestClock.
  it.live("streams typed mutation events and completes when the service closes", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const events = yield* Events.Events
      // The handler registers the subscriber before producing the response,
      // so once subscribeEvents resolves the feed is live — no mutation
      // published after this point can be missed.
      const feed = yield* client.v1.subscribeEvents()
      assert.strictEqual(yield* events.subscriberCount(), 1)
      const received = yield* Queue.make<Events.ResourceChangedEvent>()
      const subscriber = yield* Effect.forkChild(
        Stream.runForEach(feed, (event) => Queue.offer(received, event)),
      )

      const project = yield* client.v1.createProject({
        payload: { name: "Evented", defaultWorkingDirectory: realDir },
      })
      // Condition-driven: take blocks until delivery, bounded only by the
      // test timeout.
      const event = yield* Queue.take(received)
      assert.deepStrictEqual(event, { resource: "project", id: project.id, change: "created" })

      // Service shutdown ends the stream cleanly: the subscriber returns
      // instead of hanging or failing — the guarantee graceful server
      // shutdown relies on.
      yield* events.close()
      yield* Fiber.join(subscriber)
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )
})

describe("/api/v1/fs/check", () => {
  it.effect("reports the tagged directory states", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])

      const available = yield* client.v1.checkDirectory({ query: { path: linkedDir } })
      assert.strictEqual(available.state, "available")
      assert.strictEqual(available.path, realDir)
      // reason is an absent key (never null) when the state is conclusive.
      assert.isUndefined(available.reason)
      assert.isFalse(Number.isNaN(Date.parse(available.checkedAt)))

      const missing = yield* client.v1.checkDirectory({
        query: { path: join(realDir, "nope") },
      })
      assert.strictEqual(missing.state, "missing")
      assert.strictEqual(missing.path, join(realDir, "nope"))

      const notDirectory = yield* client.v1.checkDirectory({ query: { path: filePath } })
      assert.strictEqual(notDirectory.state, "not_directory")

      // A path component that exists but is a file (ENOTDIR), not ENOENT.
      const throughFile = yield* client.v1.checkDirectory({
        query: { path: join(filePath, "sub") },
      })
      assert.strictEqual(throughFile.state, "not_directory")
    }).pipe(Effect.provide(TestLayer)),
  )
})
