import { assert, describe, it } from "@effect/vitest"
import { Effect, Layer, Option } from "effect"
import { mkdtempSync, realpathSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import * as Directories from "../src/directories.ts"
import * as Persistence from "../src/persistence.ts"
import * as ProjectRepository from "../src/projectRepository.ts"
import { sessionNameForTerminalId } from "../src/terminalAdapter.ts"
import * as TerminalRepository from "../src/terminalRepository.ts"
import * as Terminals from "../src/terminals.ts"
import { makeFakeAdapter } from "./fakeTerminalAdapter.ts"
import { makeTestServiceLayers } from "./testLayers.ts"

// The Terminals domain service under the ATC-122 reconciliation invariants,
// driven entirely through the fake adapter: authoritative absence, stored
// state untouched on unavailability, two-phase create, tombstones, and
// startup recovery.

// Canonical (symlink-resolved) so repo-seeded and canonicalized paths agree.
const scratch = realpathSync(mkdtempSync(join(tmpdir(), "atc-terminals-test-")))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

/** One project to own the terminals, created fresh per test layer. */
const makeProject = Effect.gen(function* () {
  const projects = yield* ProjectRepository.ProjectRepository
  return yield* projects.create({ name: "P", defaultWorkingDirectory: scratch })
})

describe("Terminals service", () => {
  it.effect("creates a live terminal whose session runs in the project directory", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({
        projectId: project.id,
        name: "build",
        command: ["bun", "run", "dev"],
      })
      assert.strictEqual(created.status, "live")
      assert.strictEqual(created.name, "build")
      assert.deepStrictEqual(created.command, ["bun", "run", "dev"])
      assert.strictEqual(created.initialWorkingDirectory, project.defaultWorkingDirectory)
      const session = fake.sessions.get(sessionNameForTerminalId(created.id))
      assert.isDefined(session)
      assert.strictEqual(session?.cwd, project.defaultWorkingDirectory)
      assert.deepStrictEqual(session?.command, ["bun", "run", "dev"])

      assert.deepStrictEqual(yield* terminals.list(), [created])
      assert.deepStrictEqual(yield* terminals.get(created.id), created)
    }).pipe(Effect.provide(layer))
  })

  it.effect("rejects a create for an unknown project", () => {
    const { layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const terminals = yield* Terminals.Terminals
      const error = yield* Effect.flip(terminals.create({ projectId: "nope" }))
      assert.strictEqual(error._tag, "ProjectNotFound")
    }).pipe(Effect.provide(layer))
  })

  it.effect("rejects a create in an unusable working directory", () => {
    const { layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const error = yield* Effect.flip(
        terminals.create({ projectId: project.id, workingDirectory: join(scratch, "nope") }),
      )
      assert.strictEqual(error._tag, "DirectoryUnavailable")
    }).pipe(Effect.provide(layer))
  })

  it.effect("a failed launch leaves no record", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      fake.setCreateFails(true)
      const error = yield* Effect.flip(terminals.create({ projectId: project.id }))
      assert.strictEqual(error._tag, "TerminalLaunchFailed")
      fake.setCreateFails(false)
      assert.deepStrictEqual(yield* terminals.list(), [])
    }).pipe(Effect.provide(layer))
  })

  it.effect("an unavailable multiplexer fails create retryably, leaving no record", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      fake.setUnavailable(true)
      const error = yield* Effect.flip(terminals.create({ projectId: project.id }))
      assert.strictEqual(error._tag, "ZmxUnavailable")
      fake.setUnavailable(false)
      assert.deepStrictEqual(yield* terminals.list(), [])
    }).pipe(Effect.provide(layer))
  })

  it.effect("reconciliation tombstones a terminal whose session died, and keeps it", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      // The session dies out from under us (complete inventory omits it).
      fake.sessions.delete(sessionNameForTerminalId(created.id))
      const listed = yield* terminals.list()
      assert.strictEqual(listed[0]?.status, "ended")
      assert.isDefined(listed[0]?.endedAt)
      // The tombstone persists until an explicit delete, with a stable
      // endedAt across further reconciles.
      const again = yield* terminals.get(created.id)
      assert.strictEqual(again.status, "ended")
      assert.strictEqual(again.endedAt, listed[0]?.endedAt)
    }).pipe(Effect.provide(layer))
  })

  it.effect("an unavailable inventory leaves stored state untouched", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      fake.sessions.delete(sessionNameForTerminalId(created.id))
      fake.setUnavailable(true)
      // The session is gone, but no complete inventory can prove it.
      assert.strictEqual((yield* terminals.list())[0]?.status, "live")
      assert.strictEqual((yield* terminals.get(created.id)).status, "live")
    }).pipe(Effect.provide(layer))
  })

  it.effect("an unreachable session is never treated as absent", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      fake.sessions.get(sessionNameForTerminalId(created.id))!.reachable = false
      assert.strictEqual((yield* terminals.get(created.id)).status, "live")
      assert.strictEqual(yield* terminals.confirmEnded(created.id), false)
    }).pipe(Effect.provide(layer))
  })

  it.effect("rename updates the label and nothing else", () => {
    const { layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      const renamed = yield* terminals.rename(created.id, "logs")
      assert.strictEqual(renamed.name, "logs")
      assert.strictEqual(renamed.status, "live")
      const missing = yield* Effect.flip(terminals.rename("nope", "x"))
      assert.strictEqual(missing._tag, "TerminalNotFound")
    }).pipe(Effect.provide(layer))
  })

  it.effect("delete kills the session and removes the record, tombstones included", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      yield* terminals.delete(created.id)
      assert.strictEqual(fake.sessions.size, 0)
      assert.deepStrictEqual(yield* terminals.list(), [])

      // A tombstone deletes without a session to kill.
      const second = yield* terminals.create({ projectId: project.id })
      fake.sessions.delete(sessionNameForTerminalId(second.id))
      assert.strictEqual((yield* terminals.get(second.id)).status, "ended")
      yield* terminals.delete(second.id)
      assert.deepStrictEqual(yield* terminals.list(), [])

      const missing = yield* Effect.flip(terminals.delete("nope"))
      assert.strictEqual(missing._tag, "TerminalNotFound")
    }).pipe(Effect.provide(layer))
  })

  it.effect("delete requires certainty: an unavailable inventory keeps the record", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      fake.setUnavailable(true)
      const error = yield* Effect.flip(terminals.delete(created.id))
      assert.strictEqual(error._tag, "ZmxUnavailable")
      fake.setUnavailable(false)
      assert.strictEqual((yield* terminals.get(created.id)).status, "live")
    }).pipe(Effect.provide(layer))
  })

  it.effect("confirmEnded writes the tombstone only on authoritative absence", () => {
    const { fake, layer } = makeTestServiceLayers()
    return Effect.gen(function* () {
      const project = yield* makeProject
      const terminals = yield* Terminals.Terminals
      const created = yield* terminals.create({ projectId: project.id })
      assert.strictEqual(yield* terminals.confirmEnded(created.id), false)
      fake.sessions.delete(sessionNameForTerminalId(created.id))
      assert.strictEqual(yield* terminals.confirmEnded(created.id), true)
      assert.strictEqual((yield* terminals.get(created.id)).status, "ended")
    }).pipe(Effect.provide(layer))
  })
})

describe("Terminals startup reconciliation", () => {
  it.effect("resolves live, starting, and orphan sessions before serving", () => {
    const fake = makeFakeAdapter()
    // A file-backed database so the seeding phase and the Terminals build
    // (whose layer runs the startup pass) see the same rows across two
    // separate provides.
    const services = Layer.mergeAll(
      Layer.mergeAll(ProjectRepository.layer, TerminalRepository.layer).pipe(
        Layer.provide(Persistence.layerFile(join(scratch, "startup.db"))),
      ),
      Directories.layer,
      fake.layer,
    )
    const fakeSession = (name: string) =>
      fake.sessions.set(name, {
        name,
        cwd: scratch,
        command: undefined,
        written: [],
        lastResize: undefined,
        reachable: true,
        writeFails: false,
      })

    // Phase 1 — seed the world a crash would leave behind.
    const seeded = Effect.gen(function* () {
      const repo = yield* TerminalRepository.TerminalRepository
      const projects = yield* ProjectRepository.ProjectRepository
      const project = yield* projects.create({ name: "P", defaultWorkingDirectory: scratch })
      const seed = (name: string) =>
        repo.create({ projectId: project.id, name, initialWorkingDirectory: scratch })
      const survivor = yield* seed("survivor") // live row, session survives
      yield* repo.markLive(survivor.id)
      const dead = yield* seed("dead") // live row, session gone
      yield* repo.markLive(dead.id)
      const adopted = yield* seed("adopted") // starting row, session made it
      const failed = yield* seed("failed") // starting row, session absent
      const wedged = yield* seed("wedged") // starting row, session unreachable
      fakeSession(sessionNameForTerminalId(survivor.id))
      fakeSession(sessionNameForTerminalId(adopted.id))
      fakeSession(sessionNameForTerminalId(wedged.id))
      fake.sessions.get(sessionNameForTerminalId(wedged.id))!.reachable = false
      // An orphan session no record claims (provably ours: private dir).
      fakeSession("atc-orphan")
      return { survivor, failed, wedged }
    }).pipe(Effect.provide(services))

    // Phase 2 — building Terminals.layer runs the startup pass. Sequenced
    // after phase 1 so the seeding is on disk before the layer builds.
    return seeded.pipe(
      Effect.flatMap(({ failed, survivor, wedged }) =>
        Effect.gen(function* () {
          const terminals = yield* Terminals.Terminals
          const repo = yield* TerminalRepository.TerminalRepository

          const byName = new Map((yield* terminals.list()).map((t) => [t.name, t]))
          assert.strictEqual(byName.get("survivor")?.status, "live")
          assert.strictEqual(byName.get("dead")?.status, "ended")
          assert.strictEqual(byName.get("adopted")?.status, "live")
          // The failed launch left no record, and the orphan was cleaned.
          assert.isTrue(Option.isNone(yield* repo.get(failed.id)))
          assert.isFalse(fake.sessions.has("atc-orphan"))
          assert.isTrue(fake.sessions.has(sessionNameForTerminalId(survivor.id)))
          // An unreachable session proves nothing: the row stays `starting`
          // (hidden from the API) and the session is spared orphan cleanup.
          assert.isFalse(byName.has("wedged"))
          const wedgedRow = yield* repo.get(wedged.id)
          assert.strictEqual(Option.getOrUndefined(wedgedRow)?.status, "starting")
          assert.isTrue(fake.sessions.has(sessionNameForTerminalId(wedged.id)))
        }).pipe(
          Effect.provide(Layer.mergeAll(services, Terminals.layer.pipe(Layer.provide(services)))),
        ),
      ),
    )
  })
})
