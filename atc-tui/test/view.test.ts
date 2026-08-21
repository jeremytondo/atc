import { describe, expect, it } from "vitest"
import type * as AppServer from "../src/appServer.ts"
import {
  connectionLabel,
  normalizeSelection,
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

  it("makes every Thread a Project-labelled OpenTUI option", () => {
    expect(threadOptions(snapshot)).toEqual([
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
    expect(connectionLabel("disconnected")).toBe("reconnecting…")
  })
})
