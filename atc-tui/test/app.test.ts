import { assert, describe, it } from "@effect/vitest"
import { Effect, Ref } from "effect"
import type * as AppServer from "../src/appServer.ts"
import { refreshSnapshot } from "../src/app.ts"

const thread = (id: string, unread: boolean): AppServer.TuiThread => ({
  id,
  projectId: "project-1",
  agentId: "codex",
  kind: "tui",
  name: id,
  workingDirectory: "/work",
  settings: { model: "gpt-5", reasoning: "medium", mode: "chat", access: "auto" },
  activityState: "idle",
  unread,
  createdAt: "2026-08-20T00:00:00.000Z",
  updatedAt: "2026-08-20T00:00:00.000Z",
})

const snapshot: AppServer.Snapshot = {
  projects: [],
  threads: [thread("attached", true), thread("elsewhere", true)],
  threadCountsByProject: new Map(),
  agents: [],
  fetchedAt: new Date("2026-08-20T00:00:00.000Z"),
}

describe("application coordinator", () => {
  it.effect("marks an unread Thread viewed when it finishes under the active attachment", () =>
    Effect.gen(function* () {
      const marked: Array<string> = []
      const snapshotRef = yield* Ref.make<AppServer.Snapshot | undefined>(undefined)
      const attachedThreadIdRef = yield* Ref.make<string | undefined>("attached")
      const server = {
        snapshot: Effect.succeed(snapshot),
        markThreadViewed: (threadId: string) => {
          marked.push(threadId)
          return Effect.succeed(thread(threadId, false))
        },
      }

      yield* refreshSnapshot(server, snapshotRef, attachedThreadIdRef)

      assert.deepStrictEqual(marked, ["attached"])
      assert.deepStrictEqual(
        (yield* Ref.get(snapshotRef))?.threads.map(({ id, unread }) => ({ id, unread })),
        [
          { id: "attached", unread: false },
          { id: "elsewhere", unread: true },
        ],
      )
    }),
  )

  it.effect("leaves unread Threads alone when no terminal is attached", () =>
    Effect.gen(function* () {
      const marked: Array<string> = []
      const snapshotRef = yield* Ref.make<AppServer.Snapshot | undefined>(undefined)
      const attachedThreadIdRef = yield* Ref.make<string | undefined>(undefined)
      const server = {
        snapshot: Effect.succeed(snapshot),
        markThreadViewed: (threadId: string) => {
          marked.push(threadId)
          return Effect.succeed(thread(threadId, false))
        },
      }

      yield* refreshSnapshot(server, snapshotRef, attachedThreadIdRef)

      assert.deepStrictEqual(marked, [])
      assert.isTrue((yield* Ref.get(snapshotRef))?.threads[0]?.unread)
    }),
  )
})
