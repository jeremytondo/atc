import * as path from "node:path"
import type * as AppServer from "./appServer.ts"

// Pure presentation model for the OpenTUI manager. Every active Thread maps
// to exactly one selectable option, regardless of Project, so navigation and
// scrolling are owned by SelectRenderable rather than terminal line math.

export type Reachability = "connecting" | "connected" | "disconnected"

export interface ThreadOption {
  readonly threadId: string
  readonly name: string
  readonly description: string
}

const threadIds = (snapshot: AppServer.Snapshot | undefined): ReadonlyArray<string> =>
  snapshot?.threads.map((thread) => thread.id) ?? []

export const normalizeSelection = (
  snapshot: AppServer.Snapshot | undefined,
  selectedThreadId: string | undefined,
): string | undefined => {
  const ids = threadIds(snapshot)
  if (selectedThreadId !== undefined && ids.includes(selectedThreadId)) return selectedThreadId
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
): ReadonlyArray<ThreadOption> => {
  if (snapshot === undefined) return []
  const projects = new Map(snapshot.projects.map((project) => [project.id, project.name]))
  return snapshot.threads.map((thread) => ({
    threadId: thread.id,
    name: `${projects.get(thread.projectId) ?? "Unknown Project"}  ›  ${threadLabel(thread)}`,
    description: `${activity(thread)}  ·  ${thread.agentId}  ·  ${thread.workingDirectory}`,
  }))
}

export const connectionLabel = (reachability: Reachability): string =>
  reachability === "connected"
    ? "connected"
    : reachability === "connecting"
      ? "connecting…"
      : "reconnecting…"
