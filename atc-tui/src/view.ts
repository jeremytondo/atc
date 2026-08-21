import * as path from "node:path"
import type * as AppServer from "./appServer.ts"

// Pure navigation and rendering model. The UI is intentionally a small
// grouped Project/Thread list; the host terminal owns all actual terminal
// rendering once a Thread is entered.

export type Reachability = "connecting" | "connected" | "disconnected"

export interface ViewState {
  readonly selectedThreadId?: string | undefined
  readonly status?: string | undefined
}

interface DisplayLine {
  readonly text: string
  readonly threadId?: string | undefined
}

export const threadIds = (snapshot: AppServer.Snapshot | undefined): ReadonlyArray<string> =>
  snapshot?.threads.map((thread) => thread.id) ?? []

export const normalizeSelection = (
  snapshot: AppServer.Snapshot | undefined,
  selectedThreadId: string | undefined,
): string | undefined => {
  const ids = threadIds(snapshot)
  if (selectedThreadId !== undefined && ids.includes(selectedThreadId)) return selectedThreadId
  return ids[0]
}

export const moveSelection = (
  snapshot: AppServer.Snapshot | undefined,
  selectedThreadId: string | undefined,
  delta: number,
): string | undefined => {
  const ids = threadIds(snapshot)
  if (ids.length === 0) return undefined
  const current = selectedThreadId === undefined ? 0 : Math.max(0, ids.indexOf(selectedThreadId))
  const next = Math.max(0, Math.min(ids.length - 1, current + delta))
  return ids[next]
}

const threadLabel = (thread: AppServer.Thread): string =>
  thread.name ?? path.posix.basename(thread.workingDirectory) ?? thread.id

const activity = (thread: AppServer.Thread): string => {
  const state =
    thread.activityState === "needs_input"
      ? "input"
      : thread.activityState === "unknown"
        ? "?"
        : thread.activityState
  const overlays = [
    state,
    ...(thread.unread ? ["unread"] : []),
    ...(thread.linkedTerminalId === undefined ? [] : ["terminal"]),
  ]
  return overlays.join(" · ")
}

const displayLines = (snapshot: AppServer.Snapshot | undefined): ReadonlyArray<DisplayLine> => {
  if (snapshot === undefined) return [{ text: "  Waiting for the App Server…" }]
  if (snapshot.projects.length === 0) return [{ text: "  No Projects." }]

  const lines: Array<DisplayLine> = []
  for (const project of snapshot.projects) {
    const threads = snapshot.threads.filter((thread) => thread.projectId === project.id)
    lines.push({ text: `${project.name}  (${threads.length})` })
    if (threads.length === 0) {
      lines.push({ text: "    No active Threads." })
      continue
    }
    for (const thread of threads) {
      lines.push({
        text: `  ${threadLabel(thread)}  [${activity(thread)}]  ${thread.agentId}`,
        threadId: thread.id,
      })
    }
  }

  const knownProjects = new Set(snapshot.projects.map((project) => project.id))
  const orphaned = snapshot.threads.filter((thread) => !knownProjects.has(thread.projectId))
  if (orphaned.length > 0) {
    lines.push({ text: `Unknown Project  (${orphaned.length})` })
    for (const thread of orphaned) {
      lines.push({
        text: `  ${threadLabel(thread)}  [${activity(thread)}]  ${thread.agentId}`,
        threadId: thread.id,
      })
    }
  }
  return lines
}

const truncate = (value: string, width: number): string =>
  value.length <= width
    ? value
    : width <= 1
      ? value.slice(0, width)
      : value.slice(0, width - 1) + "…"

const visibleLines = (
  lines: ReadonlyArray<DisplayLine>,
  selectedThreadId: string | undefined,
  available: number,
): ReadonlyArray<DisplayLine> => {
  if (lines.length <= available) return lines
  const selected = Math.max(
    0,
    lines.findIndex((line) => line.threadId === selectedThreadId),
  )
  const start = Math.max(
    0,
    Math.min(lines.length - available, selected - Math.floor(available / 2)),
  )
  return lines.slice(start, start + available)
}

export const render = (options: {
  readonly endpoint: URL
  readonly reachability: Reachability
  readonly snapshot: AppServer.Snapshot | undefined
  readonly state: ViewState
  readonly columns: number
  readonly rows: number
}): string => {
  const width = Math.max(20, options.columns || 80)
  const height = Math.max(8, options.rows || 24)
  const selection = normalizeSelection(options.snapshot, options.state.selectedThreadId)
  const lines = visibleLines(displayLines(options.snapshot), selection, Math.max(1, height - 8))
  const connection =
    options.reachability === "connected"
      ? "connected"
      : options.reachability === "connecting"
        ? "connecting…"
        : "reconnecting…"
  const body = lines
    .map((line) => {
      const selected = line.threadId !== undefined && line.threadId === selection
      const prefix = selected ? "› " : "  "
      const content = truncate(prefix + line.text, width)
      return selected ? `\x1b[7m${content}\x1b[0m` : content
    })
    .join("\r\n")
  const fetched =
    options.snapshot === undefined
      ? ""
      : ` · synced ${options.snapshot.fetchedAt.toLocaleTimeString()}`
  const status =
    options.state.status === undefined ? "" : `\r\n${truncate(options.state.status, width)}`

  return (
    "\x1b[2J\x1b[H\x1b[?25l" +
    `ATC · ${options.endpoint.origin}\r\n` +
    `${connection}${fetched}\r\n\r\n` +
    body +
    status +
    "\r\n\r\n↑/↓ or j/k select · Enter attach · r refetch · q quit\r\n" +
    "Ctrl-\\ returns from the zmx session to this list\r\n"
  )
}
