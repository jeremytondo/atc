import { assert, describe, it } from "@effect/vitest"
import { createTestRenderer, type TestRendererSetup } from "@opentui/core/testing"
import { Deferred, Effect, Fiber, Queue, Ref } from "effect"
import { TestClock } from "effect/testing"
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
      kind: "tui",
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
      kind: "tui",
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
      kind: "tui",
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
  // The Project-wide aggregate includes a Chat Thread that is intentionally
  // absent from this TUI-only snapshot's Thread objects.
  threadCountsByProject: new Map([
    ["p1", { active: 2, archived: 1 }],
    ["p2", { active: 1, archived: 0 }],
  ]),
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
  it.effect("moves App Server status into Settings", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, {}, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Active Threads (2)")
          assert.notInclude(setup.captureCharFrame(), "http://127.0.0.1:4242")

          setup.mockInput.pressKey("s", { ctrl: true })
          yield* waitForFrame(setup, "App Server    http://127.0.0.1:4242")
          const settings = setup.captureCharFrame()
          assert.include(settings, "Connection    connected")
          assert.include(settings, "Last synced")
          setup.mockInput.pressKey("q")
        }),
      )

      assert.deepStrictEqual(result, { type: "quit" })
    }),
  )

  it.effect("creates an unnamed TUI Thread input without exposing a kind picker", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t2" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Beta   │  Second")
          setup.mockInput.pressKey("n")
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
          yield* waitForFrame(setup, "Alpha  │  First")
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
            yield* waitForFrame(setup, "Alpha  │  First")
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
            yield* waitForFrame(setup, "Alpha  │  First")
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

  it.effect("always shows and runs the current view's contextual commands", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "[Enter] Open")
          const frame = setup.captureCharFrame()
          assert.include(frame, "Alpha  │  First")
          assert.include(frame, "[Enter] Open")
          assert.include(frame, "[a] Archive")
          assert.include(frame, "[Ctrl-Space] Global")
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
      const result = yield* runManager(
        snapshot,
        { section: "archived", selectedThreadId: "t1", selectedArchivedThreadId: "t3" },
        ({ setup }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "Archived Threads (1)")
            yield* waitForFrame(setup, "Alpha  │  Archived First")
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

  it.effect("temporarily replaces contextual commands with global navigation", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "[n] New thread")
          setup.mockInput.pressKey(" ", { ctrl: true })
          yield* waitForFrame(setup, "[a] Archived")
          yield* waitForFrame(setup, "[Esc] Back")
          setup.mockInput.pressKey("a")
          yield* waitForFrame(setup, "Archived Threads (1)")
          yield* waitForFrame(setup, "[Enter/u] Restore")
          setup.mockInput.pressKey(" ", { ctrl: true })
          setup.mockInput.pressKey("p")
          yield* waitForFrame(setup, "╭─ Projects (2)")
          yield* waitForFrame(setup, "[n] New project")
          setup.mockInput.pressKey("q")
        }),
      )

      assert.deepStrictEqual(result, { type: "quit" })
    }),
  )

  it.effect("cancels global navigation and restores contextual commands", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t2" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Active Threads (2)")
          setup.mockInput.pressKey(" ", { ctrl: true })
          yield* waitForFrame(setup, "[t] Threads")
          setup.mockInput.pressEscape()
          yield* waitForFrame(setup, "[n] New thread")
          setup.mockInput.pressKey("a")
        }),
      )

      assert.deepStrictEqual(result, {
        type: "archiveThread",
        threadId: "t2",
        state: {
          selectedThreadId: "t2",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("leaves global navigation keys unbound outside global mode", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Active Threads (2)")
          setup.mockInput.pressKey("p")
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

  it.effect("renames a Project from the Projects view", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Active Threads (2)")
          setup.mockInput.pressKey(" ", { ctrl: true })
          setup.mockInput.pressKey("p")
          yield* waitForFrame(setup, "╭─ Projects (2)")
          const frameId = setup.renderer.frameId
          setup.mockInput.pressArrow("down")
          yield* Effect.promise(() => setup.waitFor(() => setup.renderer.frameId > frameId))
          yield* waitForFrame(setup, "[Enter/r] Rename")
          setup.mockInput.pressKey("r")
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

  it.effect("renames an active Thread", () =>
    Effect.gen(function* () {
      const result = yield* runManager(snapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Alpha  │  First")
          yield* waitForFrame(setup, "[r] Rename")
          setup.mockInput.pressKey("r")
          yield* waitForFrame(setup, "Rename Thread · First")
          yield* Effect.promise(() => setup.mockInput.typeText("First Prime"))
          setup.mockInput.pressEnter()
        }),
      )

      assert.deepStrictEqual(result, {
        type: "updateThread",
        threadId: "t1",
        input: { name: "First Prime" },
        state: {
          selectedThreadId: "t1",
          section: "threads",
          status: undefined,
        },
      })
    }),
  )

  it.effect("guards permanent Project deletion with the all-kind Thread count", () =>
    Effect.gen(function* () {
      const result = yield* runManager(
        snapshot,
        { section: "projects", selectedProjectId: "p1" },
        ({ setup }) =>
          Effect.gen(function* () {
            yield* waitForFrame(setup, "╭─ Projects (2)")
            setup.mockInput.pressKey("d")
            yield* waitForFrame(setup, "Delete Project · Alpha")
            setup.mockInput.pressEnter()
            yield* waitForFrame(setup, "Project deletion cancelled.")
            setup.mockInput.pressKey("d")
            yield* waitForFrame(setup, "Delete 3 Threads and their Terminals")
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
            yield* waitForFrame(setup, "╭─ Projects (2)")
            setup.mockInput.pressKey("d")
            yield* waitForFrame(setup, "Delete Project · Alpha")
            setup.mockInput.pressArrow("down")
            setup.mockInput.pressEnter()
            yield* waitForFrame(setup, "Deleting Project…")
            const action = yield* Deferred.await(actionStarted)
            assert.strictEqual(action.projectId, "p1")
            assert.include(setup.captureCharFrame(), "Working")
            yield* Deferred.succeed(actionFinished, {
              type: "continue",
              state: {
                ...action.state,
                selectedProjectId: undefined,
                status: "Project deleted.",
              },
            })
            yield* waitForFrame(setup, "Project deleted.")
            yield* waitForFrame(setup, "╭─ Projects (2)")
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
            yield* waitForFrame(setup, "Alpha  │  First")
            yield* waitForFrame(setup, "Beta   │  Second")
            setup.mockInput.pressArrow("down")
            yield* Queue.offer(uiUpdates, void 0)
            yield* Effect.promise(() =>
              setup.waitForFrame((frame) => frame.includes("○  Beta   │  Second")),
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

  it.effect("keeps selection stable while a running Thread animates", () =>
    Effect.gen(function* () {
      const activeSnapshot: AppServer.Snapshot = {
        ...snapshot,
        threads: snapshot.threads.map((thread) =>
          thread.id === "t2"
            ? { ...thread, activityState: "working", linkedTerminalId: "terminal-t2" }
            : thread,
        ),
      }
      const result = yield* runManager(activeSnapshot, { selectedThreadId: "t1" }, ({ setup }) =>
        Effect.gen(function* () {
          const runningMarkerColor = () =>
            setup
              .captureSpans()
              .lines.flatMap((line) => line.spans)
              .find((span) => span.text === "●")
              ?.fg.toInts()

          yield* waitForFrame(setup, "●  Beta   │  Second")
          assert.deepStrictEqual(runningMarkerColor(), [180, 83, 9, 255])
          yield* TestClock.adjust("200 millis")
          yield* Effect.promise(() => setup.waitFor(() => runningMarkerColor()?.[0] === 194))
          assert.deepStrictEqual(runningMarkerColor(), [194, 103, 8, 255])
          const frame = setup.captureCharFrame()
          assert.include(frame, "○  Alpha  │  First")
          const lines = frame.split("\n")
          const threadLine = lines.findIndex((line) => line.includes("Alpha  │  First"))
          assert.strictEqual(
            lines[threadLine + 1]?.indexOf("codex"),
            lines[threadLine]?.indexOf("Alpha"),
          )
          setup.mockInput.pressEnter()
        }),
      )

      assert.deepStrictEqual(result, {
        type: "attach",
        threadId: "t1",
        state: {
          selectedThreadId: "t1",
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
            yield* waitForFrame(setup, "Alpha  │  First")
            setup.mockInput.pressKey("r", { ctrl: true })
            yield* waitForFrame(setup, "Refreshing…")
            yield* Queue.take(refreshRequests)
            yield* Queue.offer(uiUpdates, void 0)
            yield* Effect.promise(() =>
              setup.waitForFrame((frame) =>
                Promise.resolve(
                  frame.includes("Alpha  │  First") && !frame.includes("Refreshing…"),
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
