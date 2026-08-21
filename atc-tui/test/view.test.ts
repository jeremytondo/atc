import { describe, expect, it } from "vitest"
import type * as AppServer from "../src/appServer.ts"
import { moveSelection, normalizeSelection, render } from "../src/view.ts"

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
  settings: {
    model: "gpt-5",
    reasoning: "medium",
    mode: "chat",
    access: "auto",
  },
  activityState,
  unread: activityState === "idle",
  ...(activityState === "working" ? { linkedTerminalId: "terminal-" + id } : {}),
  createdAt: "2026-08-20T00:00:00.000Z",
  updatedAt: "2026-08-20T00:00:00.000Z",
})

const snapshot: AppServer.Snapshot = {
  projects: [project("p1", "Alpha"), project("p2", "Beta")],
  threads: [thread("t1", "p1"), thread("t2", "p1", "working")],
  fetchedAt: new Date("2026-08-20T00:00:00.000Z"),
}

describe("navigation", () => {
  it("normalizes stale selection and clamps movement", () => {
    expect(normalizeSelection(snapshot, "missing")).toBe("t1")
    expect(moveSelection(snapshot, "t1", 1)).toBe("t2")
    expect(moveSelection(snapshot, "t2", 1)).toBe("t2")
    expect(moveSelection(snapshot, "t1", -1)).toBe("t1")
  })
})

describe("render", () => {
  it("groups Threads under Projects and shows live status", () => {
    const output = render({
      endpoint: new URL("https://atc.example"),
      reachability: "connected",
      snapshot,
      state: { selectedThreadId: "t2" },
      columns: 100,
      rows: 30,
    })

    expect(output).toContain("Alpha  (2)")
    expect(output).toContain("Beta  (0)")
    expect(output).toContain("t1  [idle · unread]  codex")
    expect(output).toContain("t2  [working · terminal]  codex")
    expect(output).toContain("\u001b[7m")
  })
})
