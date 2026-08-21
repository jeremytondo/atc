import { describe, expect, it } from "vitest"
import type * as AppServer from "../src/appServer.ts"
import {
  connectionLabel,
  normalizeProjectSelection,
  normalizeSelection,
  projectOptions,
  projectIdForSelection,
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
): AppServer.Thread => ({
  id,
  projectId,
  agentId: "codex",
  name: id,
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
        threadId: "t1",
        name: "Alpha  ›  t1",
        description: "idle · unread  ·  codex  ·  /work/p1",
      },
      {
        threadId: "t2",
        name: "Beta  ›  t2",
        description: "working · terminal  ·  codex  ·  /work/p2",
      },
    ])
    expect(threadOptions(archivedSnapshot, true)).toEqual([
      {
        threadId: "t3",
        name: "Alpha  ›  t3",
        description: "archived  ·  codex  ·  /work/p1",
      },
    ])
    expect(normalizeSelection(archivedSnapshot, undefined, true)).toBe("t3")
    expect(connectionLabel("disconnected")).toBe("reconnecting…")
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
