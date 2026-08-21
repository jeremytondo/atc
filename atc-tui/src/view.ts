import * as path from "node:path"
import type * as AppServer from "./appServer.ts"

// Pure presentation models for the OpenTUI manager. Each resource maps to one
// selectable option, so navigation and scrolling stay owned by SelectRenderable.

export type Reachability = "connecting" | "connected" | "disconnected"

export interface ThreadOption {
  readonly threadId: string
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

const threadLabel = (thread: AppServer.Thread): string =>
  thread.name ?? path.posix.basename(thread.workingDirectory) ?? thread.id

const activity = (thread: AppServer.Thread): string => {
  const state =
    thread.activityState === "needs_input"
      ? "input"
      : thread.activityState === "unknown"
        ? "?"
        : thread.activityState
  return [
    state,
    ...(thread.unread ? ["unread"] : []),
    ...(thread.linkedTerminalId === undefined ? [] : ["terminal"]),
  ].join(" · ")
}

export const threadOptions = (
  snapshot: AppServer.Snapshot | undefined,
  archived = false,
): ReadonlyArray<ThreadOption> => {
  if (snapshot === undefined) return []
  const projects = new Map(snapshot.projects.map((project) => [project.id, project.name]))
  return threadsByArchive(snapshot, archived).map((thread) => ({
    threadId: thread.id,
    name: `${projects.get(thread.projectId) ?? "Unknown Project"}  ›  ${threadLabel(thread)}`,
    description: archived
      ? `archived  ·  ${thread.agentId}  ·  ${thread.workingDirectory}`
      : `${activity(thread)}  ·  ${thread.agentId}  ·  ${thread.workingDirectory}`,
  }))
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
