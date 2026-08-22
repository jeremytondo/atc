import * as path from "node:path"
import type * as AppServer from "./appServer.ts"

// Pure presentation models for the OpenTUI manager. Each resource maps to one
// selectable option, so navigation and scrolling stay owned by the shared list.

export type Reachability = "connecting" | "connected" | "disconnected"

export interface ThreadOption {
  readonly threadId: string
  readonly marker: string
  readonly tone: "attention" | "running" | "new" | "idle" | "unknown"
  readonly name: string
  readonly description: string
}

export interface ProjectOption {
  readonly projectId: string
  readonly name: string
  readonly description: string
}

const threadsByArchive = (
  snapshot: AppServer.Snapshot | undefined,
  archived: boolean,
): ReadonlyArray<AppServer.Thread> =>
  snapshot?.threads.filter((thread) => (thread.archivedAt !== undefined) === archived) ?? []

export const normalizeSelection = (
  snapshot: AppServer.Snapshot | undefined,
  selectedThreadId: string | undefined,
  archived = false,
): string | undefined => {
  const ids = threadsByArchive(snapshot, archived).map((thread) => thread.id)
  if (selectedThreadId !== undefined && ids.includes(selectedThreadId)) return selectedThreadId
  return ids[0]
}

export const normalizeProjectSelection = (
  snapshot: AppServer.Snapshot | undefined,
  selectedProjectId: string | undefined,
): string | undefined => {
  const ids = snapshot?.projects.map((project) => project.id) ?? []
  if (selectedProjectId !== undefined && ids.includes(selectedProjectId)) return selectedProjectId
  return ids[0]
}

export const projectIdForSelection = (
  snapshot: AppServer.Snapshot,
  selectedThreadId: string | undefined,
): AppServer.Project["id"] | undefined =>
  snapshot.threads.find((thread) => thread.id === selectedThreadId)?.projectId ??
  snapshot.projects[0]?.id

export const threadLabel = (thread: AppServer.Thread): string => {
  if (thread.name !== undefined && thread.name !== "") return thread.name
  const directoryName = path.posix.basename(thread.workingDirectory)
  return directoryName === "" ? thread.id : directoryName
}

const status = (
  thread: AppServer.Thread,
): {
  readonly marker: string
  readonly tone: ThreadOption["tone"]
  readonly label?: string | undefined
  readonly priority: number
} => {
  if (thread.activityState === "needs_input") {
    return { marker: "!", tone: "attention", label: "NEEDS YOU", priority: 0 }
  }
  if (thread.activityState === "working") {
    return { marker: "●", tone: "running", priority: 1 }
  }
  if (thread.activityState === "idle" && thread.unread) {
    return { marker: "●", tone: "new", priority: 2 }
  }
  if (thread.activityState === "unknown") {
    return { marker: "?", tone: "unknown", label: "UNKNOWN", priority: 3 }
  }
  return { marker: "○", tone: "idle", priority: 4 }
}

const truncate = (value: string, width: number): string => {
  const characters = [...value]
  if (characters.length <= width) return value
  return characters.slice(0, Math.max(1, width - 1)).join("") + "…"
}

const projectColumnWidth = (names: ReadonlyArray<string>): number =>
  Math.min(18, Math.max(1, ...names.map((name) => [...name].length)))

const threadDescription = (thread: AppServer.Thread, statusLabel: string | undefined): string =>
  [
    ...(statusLabel === undefined ? [] : [statusLabel]),
    thread.agentId,
    thread.workingDirectory,
  ].join("  ·  ")

export const threadOptions = (
  snapshot: AppServer.Snapshot | undefined,
  archived = false,
): ReadonlyArray<ThreadOption> => {
  if (snapshot === undefined) return []
  const projects = new Map(snapshot.projects.map((project) => [project.id, project.name]))
  const threads = threadsByArchive(snapshot, archived)
  const projectNames = threads.map((thread) => projects.get(thread.projectId) ?? "Unknown Project")
  const columnWidth = projectColumnWidth(projectNames)
  const rows = threads.map((thread, index) => {
    const projectName = projectNames[index] ?? "Unknown Project"
    const threadStatus = status(thread)
    const projectColumn = truncate(projectName, columnWidth).padEnd(columnWidth)
    return {
      thread,
      priority: archived ? 0 : threadStatus.priority,
      option: {
        threadId: thread.id,
        marker: archived ? " " : threadStatus.marker,
        tone: archived ? "idle" : threadStatus.tone,
        name: `${projectColumn}  │  ${threadLabel(thread)}`,
        description: archived
          ? `ARCHIVED  ·  ${thread.agentId}  ·  ${thread.workingDirectory}`
          : threadDescription(thread, threadStatus.label),
      },
    }
  })
  return rows.toSorted((left, right) => left.priority - right.priority).map(({ option }) => option)
}

export const activeThreadSummary = (snapshot: AppServer.Snapshot | undefined): string => {
  const threads = threadsByArchive(snapshot, false)
  const running = threads.filter((thread) => thread.activityState === "working").length
  const needsInput = threads.filter((thread) => thread.activityState === "needs_input").length
  const unread = threads.filter((thread) => thread.activityState === "idle" && thread.unread).length
  return [
    `Active Threads (${threads.length})`,
    ...(needsInput === 0 ? [] : [`${needsInput} need${needsInput === 1 ? "s" : ""} you`]),
    ...(running === 0 ? [] : [`${running} running`]),
    ...(unread === 0 ? [] : [`${unread} new`]),
  ].join("  ·  ")
}

export const projectOptions = (
  snapshot: AppServer.Snapshot | undefined,
): ReadonlyArray<ProjectOption> => {
  if (snapshot === undefined) return []
  return snapshot.projects.map((project) => {
    const owned = snapshot.threads.filter((thread) => thread.projectId === project.id)
    const archived = owned.filter((thread) => thread.archivedAt !== undefined).length
    const active = owned.length - archived
    return {
      projectId: project.id,
      name: project.name,
      description: `${project.defaultWorkingDirectory}  ·  ${active} active  ·  ${archived} archived`,
    }
  })
}

export const connectionLabel = (reachability: Reachability): string =>
  reachability === "connected"
    ? "connected"
    : reachability === "connecting"
      ? "connecting…"
      : "reconnecting…"
