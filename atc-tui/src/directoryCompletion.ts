import * as path from "node:path"
import type * as AppServer from "./appServer.ts"

// Directory completion is expressed in server-host paths. The TUI learns the
// server's home from the root browser request and expands only `~` and `~/`;
// client-local environment and filesystem state never participate.

const normalize = (value: string): string => {
  const normalized = path.posix.normalize(value)
  return normalized === "/" ? normalized : normalized.replace(/\/+$/, "")
}

export const resolveInput = (value: string, home: string | undefined): string | undefined => {
  const trimmed = value.trim()
  if (trimmed === "") return undefined
  if (trimmed === "~") return home
  if (trimmed.startsWith("~/")) {
    if (home === undefined) return undefined
    return normalize(path.posix.join(home, trimmed.slice(2)))
  }
  if (!trimmed.startsWith("/")) return undefined
  return normalize(trimmed)
}

export const listingPath = (value: string, home: string | undefined): string | undefined => {
  const trimmed = value.trim()
  if (trimmed === "" || trimmed === "~") return home
  const resolved = resolveInput(value, home)
  if (resolved === undefined) return undefined
  return value.trim().endsWith("/") ? resolved : path.posix.dirname(resolved)
}

export const displayPath = (absolutePath: string, home: string): string => {
  if (absolutePath === home) return "~"
  if (home !== "/" && absolutePath.startsWith(home + "/")) {
    return "~" + absolutePath.slice(home.length)
  }
  return absolutePath
}

export const completedInput = (absolutePath: string, home: string): string => {
  const displayed = displayPath(absolutePath, home)
  return displayed === "/" ? displayed : displayed.replace(/\/+$/, "") + "/"
}

export const suggestions = (
  value: string,
  home: string,
  listing: AppServer.DirectoryListing,
): ReadonlyArray<AppServer.DirectoryEntry> => {
  const resolved = resolveInput(value, home)
  const prefix =
    value.trim() === "" ||
    value.trim() === "~" ||
    value.trim().endsWith("/") ||
    resolved === undefined
      ? ""
      : path.posix.basename(resolved).toLocaleLowerCase("en")
  return listing.entries.filter((entry) => entry.name.toLocaleLowerCase("en").startsWith(prefix))
}
