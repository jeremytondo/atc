import { describe, expect, it } from "vitest"
import type * as AppServer from "../src/appServer.ts"
import {
  activeThreadSummary,
  connectionLabel,
  normalizeProjectSelection,
  normalizeSelection,
  projectOptions,
  projectIdForSelection,
  threadLabel,
  threadOptions,
} from "../src/view.ts"

const project = (id: string, name: string): AppServer.Project => ({
  id,
  name,
  defaultWorkingDirectory: "/work/" + id,
  createdAt: "2026-08-20T00:00:00.000Z",
  updatedAt: "2026-08-20T00:00:00.000Z",
})

const thread = (
  id: string,
  projectId: string,
  activityState: AppServer.Thread["activityState"] = "idle",
  name: string | undefined = id,
): AppServer.Thread => ({
  id,
  projectId,
  agentId: "codex",
  ...(name === undefined ? {} : { name }),
  workingDirectory: "/work/" + projectId,
  settings: { model: "gpt-5", reasoning: "medium", mode: "chat", access: "auto" },
  activityState,
  unread: activityState === "idle",
  ...(activityState === "working" ? { linkedTerminalId: "terminal-" + id } : {}),
  createdAt: "2026-08-20T00:00:00.000Z",
  updatedAt: "2026-08-20T00:00:00.000Z",
})

const snapshot: AppServer.Snapshot = {
  projects: [project("p1", "Alpha"), project("p2", "Beta")],
  threads: [thread("t1", "p1"), thread("t2", "p2", "working")],
  agents: [
    {
      id: "codex",
      available: true,
      detectedVersion: "1.0.0",
      defaults: { model: "gpt-5", reasoning: "medium", mode: "chat", access: "auto" },
    },
  ],
  fetchedAt: new Date("2026-08-20T00:00:00.000Z"),
}

describe("manager view model", () => {
  it("falls back to the Thread id when an unnamed Thread has no directory basename", () => {
    expect(threadLabel({ ...thread("t1", "p1", "idle", undefined), workingDirectory: "/" })).toBe(
      "t1",
    )
  })

  it("normalizes selection and resolves its Project", () => {
    expect(normalizeSelection(snapshot, "missing")).toBe("t1")
    expect(normalizeSelection(snapshot, "t2")).toBe("t2")
    expect(projectIdForSelection(snapshot, "t2")).toBe("p2")
    expect(projectIdForSelection(snapshot, "missing")).toBe("p1")
  })

  it("makes active and archived Threads separate Project-labelled options", () => {
    const archivedSnapshot = {
      ...snapshot,
      threads: [
        ...snapshot.threads,
        { ...thread("t3", "p1"), archivedAt: "2026-08-21T00:00:00.000Z" },
      ],
    }

    expect(threadOptions(archivedSnapshot)).toEqual([
      {
        threadId: "t2",
        marker: "●",
        tone: "running",
        name: "Beta   │  t2",
        description: "codex  ·  /work/p2",
      },
      {
        threadId: "t1",
        marker: "●",
        tone: "new",
        name: "Alpha  │  t1",
        description: "codex  ·  /work/p1",
      },
    ])
    expect(threadOptions(archivedSnapshot, true)).toEqual([
      {
        threadId: "t3",
        marker: " ",
        tone: "idle",
        name: "Alpha  │  t3",
        description: "ARCHIVED  ·  codex  ·  /work/p1",
      },
    ])
    expect(activeThreadSummary(archivedSnapshot)).toBe("Active Threads (2)  ·  1 running  ·  1 new")
    expect(normalizeSelection(archivedSnapshot, undefined, true)).toBe("t3")
    expect(connectionLabel("disconnected")).toBe("reconnecting…")
  })

  it("uses the status position to distinguish new and idle Threads", () => {
    const idleSnapshot = {
      ...snapshot,
      threads: snapshot.threads.map((item) =>
        item.id === "t1" ? { ...item, unread: false } : item,
      ),
    }

    expect(threadOptions(idleSnapshot)[1]).toEqual({
      threadId: "t1",
      marker: "○",
      tone: "idle",
      name: "Alpha  │  t1",
      description: "codex  ·  /work/p1",
    })
  })

  it("describes Projects with active and archived Thread counts", () => {
    const archivedSnapshot = {
      ...snapshot,
      threads: [
        ...snapshot.threads,
        { ...thread("t3", "p1"), archivedAt: "2026-08-21T00:00:00.000Z" },
      ],
    }

    expect(normalizeProjectSelection(archivedSnapshot, "missing")).toBe("p1")
    expect(projectOptions(archivedSnapshot)).toEqual([
      {
        projectId: "p1",
        name: "Alpha",
        description: "/work/p1  ·  1 active  ·  1 archived",
      },
      {
        projectId: "p2",
        name: "Beta",
        description: "/work/p2  ·  1 active  ·  0 archived",
      },
    ])
  })
})
