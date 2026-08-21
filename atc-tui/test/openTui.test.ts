import { assert, describe, it } from "@effect/vitest"
import { createTestRenderer, type TestRendererSetup } from "@opentui/core/testing"
import { Deferred, Effect, Fiber, Queue, Ref } from "effect"
import type * as AppServer from "../src/appServer.ts"
import * as OpenTui from "../src/openTui.ts"
import type * as View from "../src/view.ts"

const defaults: AppServer.Thread["settings"] = {
  model: "gpt-5",
  reasoning: "medium",
  mode: "chat",
  access: "auto",
}

const snapshot: AppServer.Snapshot = {
  projects: [
    {
      id: "p1",
      name: "Alpha",
      defaultWorkingDirectory: "/work/alpha",
      createdAt: "2026-08-20T00:00:00.000Z",
      updatedAt: "2026-08-20T00:00:00.000Z",
    },
    {
      id: "p2",
      name: "Beta",
      defaultWorkingDirectory: "/work/beta",
      createdAt: "2026-08-20T00:00:00.000Z",
      updatedAt: "2026-08-20T00:00:00.000Z",
    },
  ],
  threads: [
    {
      id: "t1",
      projectId: "p1",
      agentId: "codex",
      name: "First",
      workingDirectory: "/work/alpha",
      settings: defaults,
      activityState: "idle",
      unread: false,
      createdAt: "2026-08-20T00:00:00.000Z",
      updatedAt: "2026-08-20T00:00:00.000Z",
    },
    {
      id: "t2",
      projectId: "p2",
      agentId: "codex",
      name: "Second",
      workingDirectory: "/work/beta",
      settings: defaults,
      activityState: "idle",
      unread: false,
      createdAt: "2026-08-20T00:00:00.000Z",
      updatedAt: "2026-08-20T00:00:00.000Z",
    },
    {
      id: "t3",
      projectId: "p1",
      agentId: "codex",
      name: "Archived First",
      workingDirectory: "/work/alpha",
      settings: defaults,
      activityState: "idle",
      unread: false,
      archivedAt: "2026-08-21T00:00:00.000Z",
      createdAt: "2026-08-19T00:00:00.000Z",
      updatedAt: "2026-08-21T00:00:00.000Z",
    },
  ],
  agents: [
    { id: "claude-code", available: true, detectedVersion: "1.0.0", defaults },
    { id: "codex", available: true, detectedVersion: "1.0.0", defaults },
  ],
  fetchedAt: new Date("2026-08-20T00:00:00.000Z"),
}

interface ManagerHarness {
  readonly setup: TestRendererSetup
  readonly uiUpdates: Queue.Queue<void>
  readonly refreshRequests: Queue.Queue<void>
  readonly snapshotRef: Ref.Ref<AppServer.Snapshot | undefined>
}

const defaultListDirectory = (
  requestedPath?: string,
): Effect.Effect<AppServer.DirectoryListing> => {
  const path = requestedPath ?? "/work"
  return Effect.succeed({
    path,
    ...(path === "/" ? {} : { parent: "/" }),
    entries: path === "/work" ? [{ name: "gamma", path: "/work/gamma" }] : [],
  })
}

const runWithManagerRenderer = <Result>(
  initialSnapshot: AppServer.Snapshot | undefined,
  initial: OpenTui.ManagerState,
  drive: (harness: ManagerHarness) => Effect.Effect<void, unknown>,
  start: (
    setup: TestRendererSetup,
    options: OpenTui.ManagerOptions,
  ) => Effect.Effect<Result, unknown>,
  listDirectory = defaultListDirectory,
): Effect.Effect<Result, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const setup = yield* Effect.acquireRelease(
        Effect.promise(() =>
          createTestRenderer({
            width: 80,
            height: 24,
            exitOnCtrlC: false,
            exitSignals: [],
            useMouse: false,
            autoFocus: false,
          }),
        ),
        ({ renderer }) => Effect.sync(() => renderer.destroy()),
      )
      yield* Effect.sync(() => setup.renderer.start())
      const snapshotRef = yield* Ref.make<AppServer.Snapshot | undefined>(initialSnapshot)
      const reachabilityRef = yield* Ref.make<View.Reachability>("connected")
      const backgroundStatusRef = yield* Ref.make<string | undefined>(undefined)
      const uiUpdates = yield* Queue.unbounded<void>()
      const refreshRequests = yield* Queue.unbounded<void>()
      const resultFiber = yield* Effect.forkScoped(
        start(setup, {
          endpoint: new URL("http://127.0.0.1:4242"),
          listDirectory,
          snapshotRef,
          reachabilityRef,
          backgroundStatusRef,
          uiUpdates,
          refreshRequests,
          initial,
        }),
      )

      yield* drive({ setup, uiUpdates, refreshRequests, snapshotRef })
      return yield* Fiber.join(resultFiber)
    }),
  )

const runManager = (
  initialSnapshot: AppServer.Snapshot | undefined,
  initial: OpenTui.ManagerState,
  drive: (harness: ManagerHarness) => Effect.Effect<void, unknown>,
  listDirectory = defaultListDirectory,
): Effect.Effect<OpenTui.ManagerResult, unknown> =>
  runWithManagerRenderer(
    initialSnapshot,
    initial,
    drive,
    (setup, options) => OpenTui.runWithRenderer(setup.renderer, options),
    listDirectory,
  )

const waitForFrame = (setup: TestRendererSetup, text: string) =>
  Effect.promise(() => setup.waitForFrame((frame) => frame.includes(text)))

describe("OpenTUI manager", () => {
  it.effect("creates an unnamed Thread in the selected Project with the preferred agent", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t2" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Beta  ›  Second")
          setup.mockInput.pressKey("n", { ctrl: true })
          yield* waitForFrame(setup, "New Thread · Project")
          setup.mockInput.pressEnter()
          yield* waitForFrame(setup, "New Thread · Agent")
          setup.mockInput.pressEnter()
        }),
      )

      assert.deepStrictEqual(result, {
        type: "createThread",
        input: { projectId: "p2", agentId: "codex" },
        state: {
          selectedThreadId: "t2",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("validates and completes a Project directory from the server home", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Alpha  ›  First")
          setup.mockInput.pressKey("p", { ctrl: true })
          yield* waitForFrame(setup, "New Project · Name")
          yield* Effect.promise(() => setup.mockInput.typeText("Gamma"))
          setup.mockInput.pressEnter()
          yield* waitForFrame(setup, "New Project · Directory")
          yield* Effect.promise(() => setup.mockInput.typeText("relative"))
          setup.mockInput.pressEnter()
          yield* waitForFrame(setup, "Enter an absolute path beginning with / or ~/.")
          "relative".split("").forEach(() => setup.mockInput.pressBackspace())
          yield* Effect.promise(() => setup.mockInput.typeText("~/ga"))
          yield* waitForFrame(setup, "gamma/")
          setup.mockInput.pressTab()
          yield* waitForFrame(setup, "~/gamma/")
          setup.mockInput.pressEnter()
        }),
      )

      assert.deepStrictEqual(result, {
        type: "createProject",
        input: { name: "Gamma", defaultWorkingDirectory: "/work/gamma" },
        state: {
          selectedThreadId: "t1",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("navigates directory suggestions without moving focus from the input", () =>
    Effect.gen(function* () {
      const listDirectory = (requestedPath?: string) =>
        Effect.succeed<AppServer.DirectoryListing>({
          path: requestedPath ?? "/srv/home",
          parent: "/srv",
          entries:
            requestedPath === undefined
              ? [
                  { name: "Alpha", path: "/srv/home/Alpha" },
                  { name: "Beta", path: "/srv/home/Beta" },
                ]
              : [],
        })
      const result = yield* runManager(
        snapshot,
        { selectedThreadId: "t1" },
        ({ setup }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Alpha  ›  First")
            setup.mockInput.pressKey("p", { ctrl: true })
            yield* waitForFrame(setup, "New Project · Name")
            yield* Effect.promise(() => setup.mockInput.typeText("Beta"))
            setup.mockInput.pressEnter()
            yield* waitForFrame(setup, "Alpha/")
            yield* waitForFrame(setup, "Beta/")
            setup.mockInput.pressArrow("down")
            setup.mockInput.pressTab()
            yield* waitForFrame(setup, "~/Beta/")
            setup.mockInput.pressEnter()
          }),
        listDirectory,
      )

      assert.deepStrictEqual(result, {
        type: "createProject",
        input: { name: "Beta", defaultWorkingDirectory: "/srv/home/Beta" },
        state: {
          selectedThreadId: "t1",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("keeps stale directory responses from replacing current suggestions", () =>
    Effect.gen(function* () {
      const pendingAlpha = yield* Deferred.make<AppServer.DirectoryListing>()
      const listDirectory = (requestedPath?: string): Effect.Effect<AppServer.DirectoryListing> => {
        if (requestedPath === "/srv/home/alpha") return Deferred.await(pendingAlpha)
        if (requestedPath === undefined) {
          return Effect.succeed({
            path: "/srv/home",
            parent: "/srv",
            entries: [
              { name: "alpha", path: "/srv/home/alpha" },
              { name: "other", path: "/srv/home/other" },
            ],
          })
        }
        if (requestedPath === "/srv/home/other") {
          return Effect.succeed({
            path: requestedPath,
            parent: "/srv/home",
            entries: [{ name: "fresh", path: requestedPath + "/fresh" }],
          })
        }
        return Effect.succeed({ path: requestedPath, parent: "/srv/home", entries: [] })
      }

      const result = yield* runManager(
        snapshot,
        { selectedThreadId: "t1" },
        ({ setup }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Alpha  ›  First")
            setup.mockInput.pressKey("p", { ctrl: true })
            yield* waitForFrame(setup, "New Project · Name")
            yield* Effect.promise(() => setup.mockInput.typeText("Out of order"))
            setup.mockInput.pressEnter()
            yield* waitForFrame(setup, "alpha/")
            yield* Effect.promise(() => setup.mockInput.typeText("~/alpha/"))
            yield* waitForFrame(setup, "Loading /srv/home/alpha…")
            "~/alpha/".split("").forEach(() => setup.mockInput.pressBackspace())
            yield* Effect.promise(() => setup.mockInput.typeText("~/other/"))
            yield* waitForFrame(setup, "fresh/")
            yield* Effect.promise(() => setup.waitForVisualIdle())
            const frameId = setup.renderer.frameId
            yield* Deferred.succeed(pendingAlpha, {
              path: "/srv/home/alpha",
              parent: "/srv/home",
              entries: [{ name: "stale", path: "/srv/home/alpha/stale" }],
            })
            yield* Effect.promise(() => setup.waitFor(() => setup.renderer.frameId > frameId))
            const frame = setup.captureCharFrame()
            assert.include(frame, "fresh/")
            assert.notInclude(frame, "stale/")
            setup.mockInput.pressTab()
            yield* waitForFrame(setup, "~/other/fresh/")
            setup.mockInput.pressEnter()
          }),
        listDirectory,
      )

      assert.deepStrictEqual(result, {
        type: "createProject",
        input: {
          name: "Out of order",
          defaultWorkingDirectory: "/srv/home/other/fresh",
        },
        state: {
          selectedThreadId: "t1",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("archives the selected Thread immediately", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Alpha  ›  First")
          setup.mockInput.pressKey("a")
        }),
      )

      assert.deepStrictEqual(result, {
        type: "archiveThread",
        threadId: "t1",
        state: {
          selectedThreadId: "t1",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("lists and restores archived Threads", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Active Threads (2)")
          setup.mockInput.pressTab()
          yield* waitForFrame(setup, "Archived Threads (1)")
          yield* waitForFrame(setup, "Alpha  ›  Archived First")
          setup.mockInput.pressKey("u")
        }),
      )

      assert.deepStrictEqual(result, {
        type: "unarchiveThread",
        threadId: "t3",
        state: {
          selectedThreadId: "t1",
          section: "archived",
          selectedArchivedThreadId: "t3",
          status: undefined,
        },
      })
    }),
  )

  it.effect("renames a Project from the Projects view", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Active Threads (2)")
          setup.mockInput.pressTab()
          yield* waitForFrame(setup, "Archived Threads (1)")
          setup.mockInput.pressTab()
          yield* waitForFrame(setup, "Projects (2)")
          setup.mockInput.pressArrow("down")
          yield* waitForFrame(setup, "/work/beta  ·  1 active  ·  0 archived")
          setup.mockInput.pressKey("e")
          yield* waitForFrame(setup, "Rename Project · Beta")
          yield* Effect.promise(() => setup.mockInput.typeText("Beta Prime"))
          setup.mockInput.pressEnter()
        }),
      )

      assert.deepStrictEqual(result, {
        type: "updateProject",
        projectId: "p2",
        input: { name: "Beta Prime" },
        state: {
          selectedThreadId: "t1",
          section: "projects",
          status: undefined,
          selectedProjectId: "p2",
        },
      })
    }),
  )

  it.effect("guards permanent Project deletion", () =>
    Effect.gen(function* () {
      const result = yield* runManager(
        snapshot,
        { section: "projects", selectedProjectId: "p1" },
        ({ setup }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Projects (2)")
            setup.mockInput.pressKey("d")
            yield* waitForFrame(setup, "Delete Project · Alpha")
            setup.mockInput.pressEnter()
            yield* waitForFrame(setup, "Project deletion cancelled.")
            setup.mockInput.pressKey("d")
            yield* waitForFrame(setup, "Delete 2 Threads and their Terminals")
            setup.mockInput.pressArrow("down")
            setup.mockInput.pressEnter()
          }),
      )

      assert.deepStrictEqual(result, {
        type: "deleteProject",
        projectId: "p1",
        state: {
          section: "projects",
          selectedProjectId: "p1",
          status: undefined,
        },
      })
    }),
  )

  it.effect("keeps the renderer mounted while Project deletion runs", () =>
    Effect.gen(function* () {
      const actionStarted =
        yield* Deferred.make<Extract<OpenTui.ManagerAction, { readonly type: "deleteProject" }>>()
      const actionFinished = yield* Deferred.make<OpenTui.ManagerTransition>()

      const result = yield* runWithManagerRenderer(
        snapshot,
        { section: "projects", selectedProjectId: "p1" },
        ({ setup }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Projects (2)")
            setup.mockInput.pressKey("d")
            yield* waitForFrame(setup, "Delete Project · Alpha")
            setup.mockInput.pressArrow("down")
            setup.mockInput.pressEnter()
            yield* waitForFrame(setup, "Deleting Project…")
            const action = yield* Deferred.await(actionStarted)
            assert.strictEqual(action.projectId, "p1")
            assert.include(setup.captureCharFrame(), "ATC")
            yield* Deferred.succeed(actionFinished, {
              type: "continue",
              state: {
                ...action.state,
                selectedProjectId: undefined,
                status: "Project deleted.",
              },
            })
            yield* waitForFrame(setup, "Project deleted.")
            yield* waitForFrame(setup, "Projects (2)")
            setup.mockInput.pressKey("q")
          }),
        (setup, options) =>
          OpenTui.runSessionWithRenderer(setup.renderer, options, (action) => {
            if (action.type !== "deleteProject") {
              return Effect.die(new Error(`unexpected action: ${action.type}`))
            }
            return Deferred.succeed(actionStarted, action).pipe(
              Effect.andThen(Deferred.await(actionFinished)),
            )
          }),
      )

      assert.deepStrictEqual(result, { type: "quit" })
    }),
  )

  it.effect("navigates and attaches across Project boundaries", () =>
    Effect.gen(function* () {
      const result = yield* runManager(
        undefined,
        { selectedThreadId: "t1" },
        ({ setup, uiUpdates, snapshotRef }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Waiting for the App Server…")
            yield* Ref.set(snapshotRef, snapshot)
            yield* Queue.offer(uiUpdates, void 0)
            yield* waitForFrame(setup, "Alpha  ›  First")
            yield* waitForFrame(setup, "Beta  ›  Second")
            setup.mockInput.pressArrow("down")
            yield* Queue.offer(uiUpdates, void 0)
            yield* Effect.promise(() =>
              setup.waitForFrame((frame) => frame.includes("▶ Beta  ›  Second")),
            )
            setup.mockInput.pressEnter()
          }),
      )

      assert.deepStrictEqual(result, {
        type: "attach",
        threadId: "t2",
        state: {
          selectedThreadId: "t2",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("clears the refresh status after the live snapshot updates", () =>
    Effect.gen(function* () {
      const result = yield* runManager(
        snapshot,
        { selectedThreadId: "t1" },
        ({ setup, uiUpdates, refreshRequests }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Alpha  ›  First")
            setup.mockInput.pressKey("r")
            yield* waitForFrame(setup, "Refreshing…")
            yield* Queue.take(refreshRequests)
            yield* Queue.offer(uiUpdates, void 0)
            yield* Effect.promise(() =>
              setup.waitForFrame((frame) =>
                Promise.resolve(
                  frame.includes("Alpha  ›  First") && !frame.includes("Refreshing…"),
                ),
              ),
            )
            setup.mockInput.pressKey("q")
          }),
      )

      assert.deepStrictEqual(result, { type: "quit" })
    }),
  )
})
