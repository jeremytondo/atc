import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { HttpApiTest } from "effect/unstable/httpapi"
import { mkdtempSync, realpathSync, rmSync, symlinkSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { Api } from "../../src/api/contract.ts"
import { V1Handlers } from "../../src/api/handlers.ts"
import { sessionNameForTerminalId } from "../../src/terminals/terminalAdapter.ts"
import { TerminalRepository } from "../../src/terminals/terminalRepository.ts"
import { ThreadRepository } from "../../src/threads/threadRepository.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { makeTestServiceLayers } from "../testLayers.ts"

// Threads foundation coverage (ATC-124 / ATC-139): the durable local-only
// record, its lifecycle rules, and the derived Thread↔Terminal link — all
// through the public contract. Provider materialization is ATC-141 work; the
// proof here is that none of these paths need an agent adapter at all.

const kit = makeTestServiceLayers()
// The kit layer is shared by reference between the handlers and the test
// bodies, so repository seeding below hits the same database the API serves.
const TestLayer = Layer.mergeAll(
  V1Handlers.pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
  kit.layer,
  BunHttpServer.layerHttpServices,
)

const scratch = mkdtempSync(join(tmpdir(), "atc-threads-test-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))
const realDir = realpathSync(scratch)
const linkedDir = join(scratch, "linked")
symlinkSync(realDir, linkedDir)

const testClient = Effect.gen(function* () {
  const client = yield* HttpApiTest.groups(Api, ["v1"])
  const makeProject = client.v1.createProject({
    payload: { name: "Threads", defaultWorkingDirectory: realDir },
  })
  return { client, makeProject }
})

describe("/api/v1/threads", () => {
  it.effect("creates, lists, gets, updates, archives, and deletes a thread", () =>
    Effect.gen(function* () {
      const { client, makeProject } = yield* testClient
      const project = yield* makeProject

      // Create is local-only, defaults to the project directory, and starts
      // idle: no provider session exists, so nothing can be running.
      const created = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      assert.match(
        created.id,
        /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$/,
        "UUIDv7 id",
      )
      assert.strictEqual(created.agentId, "codex")
      assert.strictEqual(created.workingDirectory, realDir)
      assert.strictEqual(created.activityState, "idle")
      assert.isUndefined(created.name)
      assert.isUndefined(created.linkedTerminalId)
      assert.isUndefined(created.pinnedAt)
      assert.isUndefined(created.archivedAt)
      assert.strictEqual(created.createdAt, created.updatedAt)

      // An explicit symlinked directory is stored canonicalized.
      const canonical = yield* client.v1.createThread({
        payload: {
          projectId: project.id,
          agentId: "claude-code",
          name: "Canonical",
          workingDirectory: linkedDir,
        },
      })
      assert.strictEqual(canonical.workingDirectory, realDir)
      assert.strictEqual(canonical.name, "Canonical")

      // Newest first, both active. Listings are asserted project-scoped so
      // tests stay order-independent on the shared module database.
      assert.deepStrictEqual(yield* client.v1.listThreads({ query: { projectId: project.id } }), [
        canonical,
        created,
      ])
      assert.deepStrictEqual(
        yield* client.v1.getThread({ params: { threadId: created.id } }),
        created,
      )

      // Rename; an empty patch changes nothing, updatedAt included.
      const renamed = yield* client.v1.updateThread({
        params: { threadId: created.id },
        payload: { name: "Renamed" },
      })
      assert.strictEqual(renamed.name, "Renamed")
      const unchanged = yield* client.v1.updateThread({
        params: { threadId: created.id },
        payload: {},
      })
      assert.deepStrictEqual(unchanged, renamed)

      // Pin/unpin are idempotent and use pinnedAt as the durable order key.
      const pinned = yield* client.v1.pinThread({ params: { threadId: created.id } })
      assert.isString(pinned.pinnedAt)
      const pinnedAgain = yield* client.v1.pinThread({ params: { threadId: created.id } })
      assert.deepStrictEqual(pinnedAgain, pinned)
      const unpinned = yield* client.v1.unpinThread({ params: { threadId: created.id } })
      assert.isUndefined(unpinned.pinnedAt)
      const unpinnedAgain = yield* client.v1.unpinThread({ params: { threadId: created.id } })
      assert.deepStrictEqual(unpinnedAgain, unpinned)

      // Archive is idempotent, clears a pin, and excludes the thread from
      // the default list. Restoring never silently re-pins it.
      yield* client.v1.pinThread({ params: { threadId: created.id } })
      const archived = yield* client.v1.archiveThread({ params: { threadId: created.id } })
      assert.isString(archived.archivedAt)
      assert.isUndefined(archived.pinnedAt)
      const again = yield* client.v1.archiveThread({ params: { threadId: created.id } })
      assert.deepStrictEqual(again, archived)
      const pinArchived = yield* Effect.flip(
        client.v1.pinThread({ params: { threadId: created.id } }),
      )
      assert.strictEqual(pinArchived._tag, "ThreadArchived")
      assert.deepStrictEqual(
        yield* client.v1.unpinThread({ params: { threadId: created.id } }),
        archived,
      )
      assert.deepStrictEqual(yield* client.v1.listThreads({ query: { projectId: project.id } }), [
        canonical,
      ])
      assert.deepStrictEqual(
        yield* client.v1.listThreads({ query: { projectId: project.id, archived: "true" } }),
        [archived],
      )
      const restored = yield* client.v1.unarchiveThread({ params: { threadId: created.id } })
      assert.isUndefined(restored.archivedAt)
      assert.isUndefined(restored.pinnedAt)

      // Project-scoped listing.
      assert.deepStrictEqual(
        yield* client.v1.listThreads({ query: { projectId: "someone-else" } }),
        [],
      )

      yield* client.v1.deleteThread({ params: { threadId: created.id } })
      yield* client.v1.deleteThread({ params: { threadId: canonical.id } })
      assert.deepStrictEqual(yield* client.v1.listThreads({ query: { projectId: project.id } }), [])
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("unknown ids are ThreadNotFound on every id-taking operation", () =>
    Effect.gen(function* () {
      const client = yield* HttpApiTest.groups(Api, ["v1"])
      const attempts: ReadonlyArray<Effect.Effect<unknown, unknown>> = [
        client.v1.getThread({ params: { threadId: "missing" } }),
        client.v1.updateThread({ params: { threadId: "missing" }, payload: { name: "x" } }),
        client.v1.archiveThread({ params: { threadId: "missing" } }),
        client.v1.unarchiveThread({ params: { threadId: "missing" } }),
        client.v1.pinThread({ params: { threadId: "missing" } }),
        client.v1.unpinThread({ params: { threadId: "missing" } }),
        client.v1.deleteThread({ params: { threadId: "missing" } }),
      ]
      for (const attempt of attempts) {
        const error = (yield* Effect.flip(attempt)) as { _tag: string; threadId?: string }
        assert.strictEqual(error._tag, "ThreadNotFound")
        assert.strictEqual(error.threadId, "missing")
      }
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("create validates the project and the directory, storing nothing on failure", () =>
    Effect.gen(function* () {
      const { client, makeProject } = yield* testClient
      const unknownProject = yield* Effect.flip(
        client.v1.createThread({ payload: { projectId: "missing", agentId: "codex" } }),
      )
      assert.strictEqual(unknownProject._tag, "ProjectNotFound")

      const project = yield* makeProject
      const missingDir = yield* Effect.flip(
        client.v1.createThread({
          payload: {
            projectId: project.id,
            agentId: "codex",
            workingDirectory: join(realDir, "nope"),
          },
        }),
      )
      assert.strictEqual(missingDir._tag, "DirectoryUnavailable")
      assert.deepStrictEqual(yield* client.v1.listThreads({ query: { projectId: project.id } }), [])
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("project deletion is restricted while threads exist, archived included", () =>
    Effect.gen(function* () {
      const { client, makeProject } = yield* testClient
      const project = yield* makeProject
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      yield* client.v1.archiveThread({ params: { threadId: thread.id } })
      const restricted = yield* Effect.flip(
        client.v1.deleteProject({ params: { projectId: project.id } }),
      )
      assert.strictEqual(restricted._tag, "ProjectHasThreads")
      if (restricted._tag === "ProjectHasThreads") {
        assert.strictEqual(restricted.threadCount, 1)
      }
      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("an archived row rejects a stale pin write", () =>
    Effect.gen(function* () {
      const { client, makeProject } = yield* testClient
      const repository = yield* ThreadRepository
      const project = yield* makeProject
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })
      yield* client.v1.archiveThread({ params: { threadId: thread.id } })

      // Models pin() racing after its active-record read but losing the SQL
      // write race to archive(): storage keeps the archived row unpinned.
      const guarded = yield* repository.setPinned(thread.id, true)
      assert.isUndefined(guarded.pinnedAt)
      assert.isUndefined((yield* client.v1.getThread({ params: { threadId: thread.id } })).pinnedAt)

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("delete kills the live linked terminal and unlinks ended tombstones", () =>
    Effect.gen(function* () {
      const { client, makeProject } = yield* testClient
      const repository = yield* TerminalRepository
      const project = yield* makeProject
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "codex" },
      })

      // Seed a live linked TUI terminal the way ATC-141's openTerminal will:
      // a terminals row carrying thread_id plus its adapter session.
      const record = yield* repository.create({
        projectId: project.id,
        threadId: thread.id,
        initialWorkingDirectory: realDir,
      })
      kit.fake.seed(sessionNameForTerminalId(record.id))
      yield* repository.markLive(record.id)

      // The link is derived onto the thread, and onto the terminal itself.
      const linked = yield* client.v1.getThread({ params: { threadId: thread.id } })
      assert.strictEqual(linked.linkedTerminalId, record.id)
      const terminal = yield* client.v1.getTerminal({ params: { terminalId: record.id } })
      assert.strictEqual(terminal.threadId, thread.id)

      // An ended linked terminal (a previous TUI run) must survive the
      // thread's deletion as an unlinked tombstone.
      const ended = yield* repository.create({
        projectId: project.id,
        threadId: thread.id,
        initialWorkingDirectory: realDir,
      })
      yield* repository.markLive(ended.id)
      yield* repository.markEnded([ended.id])

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      // The live terminal's record and session are gone…
      assert.isFalse(kit.fake.sessions.has(sessionNameForTerminalId(record.id)))
      const gone = yield* Effect.flip(client.v1.getTerminal({ params: { terminalId: record.id } }))
      assert.strictEqual(gone._tag, "TerminalNotFound")
      // …while the tombstone remains, now unlinked (FK SET NULL).
      const tombstone = yield* client.v1.getTerminal({ params: { terminalId: ended.id } })
      assert.strictEqual(tombstone.status, "ended")
      assert.isUndefined(tombstone.threadId)

      yield* client.v1.deleteTerminal({ params: { terminalId: ended.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("a linked terminal that died shows as unlinked once reconciled", () =>
    Effect.gen(function* () {
      const { client, makeProject } = yield* testClient
      const repository = yield* TerminalRepository
      const project = yield* makeProject
      const thread = yield* client.v1.createThread({
        payload: { projectId: project.id, agentId: "claude-code" },
      })
      const record = yield* repository.create({
        projectId: project.id,
        threadId: thread.id,
        initialWorkingDirectory: realDir,
      })
      kit.fake.seed(sessionNameForTerminalId(record.id))
      yield* repository.markLive(record.id)
      assert.strictEqual(
        (yield* client.v1.getThread({ params: { threadId: thread.id } })).linkedTerminalId,
        record.id,
      )

      // The session dies out from under ATC: the reconciled terminals
      // listing tombstones it, so the thread's derived link disappears.
      kit.fake.sessions.delete(sessionNameForTerminalId(record.id))
      assert.isUndefined(
        (yield* client.v1.getThread({ params: { threadId: thread.id } })).linkedTerminalId,
      )

      yield* client.v1.deleteThread({ params: { threadId: thread.id } })
      yield* client.v1.deleteTerminal({ params: { terminalId: record.id } })
      yield* client.v1.deleteProject({ params: { projectId: project.id } })
    }).pipe(Effect.provide(TestLayer)),
  )
})
