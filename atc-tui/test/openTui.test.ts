import { assert, describe, it } from "@effect/vitest"
import { createTestRenderer, type TestRendererSetup } from "@opentui/core/testing"
import { Effect, Fiber, Queue, Ref } from "effect"
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
  ],
  agents: [
    { id: "claude-code", available: true, detectedVersion: "1.0.0", defaults },
    { id: "codex", available: true, detectedVersion: "1.0.0", defaults },
  ],
  fetchedAt: new Date("2026-08-20T00:00:00.000Z"),
}

const runManager = (
  initial: OpenTui.ManagerState,
  drive: (setup: TestRendererSetup) => Effect.Effect<void, unknown>,
): Effect.Effect<OpenTui.ManagerResult, unknown> =>
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
      const snapshotRef = yield* Ref.make<AppServer.Snapshot | undefined>(snapshot)
      const reachabilityRef = yield* Ref.make<View.Reachability>("connected")
      const backgroundStatusRef = yield* Ref.make<string | undefined>(undefined)
      const uiUpdates = yield* Queue.unbounded<void>()
      const refreshRequests = yield* Queue.unbounded<void>()
      const resultFiber = yield* Effect.forkScoped(
        OpenTui.runWithRenderer(setup.renderer, {
          endpoint: new URL("http://127.0.0.1:4242"),
          snapshotRef,
          reachabilityRef,
          backgroundStatusRef,
          uiUpdates,
          refreshRequests,
          initial,
        }),
      )

      yield* drive(setup)
      return yield* Fiber.join(resultFiber)
    }),
  )

const waitForFrame = (setup: TestRendererSetup, text: string) =>
  Effect.promise(() => setup.waitForFrame((frame) => frame.includes(text)))

describe("OpenTUI manager", () => {
  it.effect("creates an unnamed Thread in the selected Project with the preferred agent", () =>
    Effect.gen(function* () {
      const result = yield* runManager({ selectedThreadId: "t2" }, (setup) =>
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
        type: "create",
        input: { projectId: "p2", agentId: "codex" },
        selectedThreadId: "t2",
      })
    }),
  )

  it.effect("navigates and attaches across Project boundaries", () =>
    Effect.gen(function* () {
      const result = yield* runManager({ selectedThreadId: "t1" }, (setup) =>
        Effect.gen(function* () {
          yield* waitForFrame(setup, "Alpha  ›  First")
          yield* waitForFrame(setup, "Beta  ›  Second")
          setup.mockInput.pressArrow("down")
          yield* Effect.yieldNow
          setup.mockInput.pressEnter()
        }),
      )

      assert.deepStrictEqual(result, { type: "attach", threadId: "t2" })
    }),
  )
})
