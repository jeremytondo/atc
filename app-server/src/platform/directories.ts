import { Context, Effect, Layer } from "effect"
import { constants } from "node:fs"
import * as fs from "node:fs/promises"
import { DirectoryCheckTimedOut, DirectoryUnavailable, DirectoryState } from "../api/contract.ts"

// Demand-driven directory health (ATC-121, Domain Model rules): checks run
// only when an operation needs a directory or a client asks explicitly —
// results are never persisted and normal reads never touch the filesystem.
// Validation requires an existing directory with read+traversal access
// (deliberately not write: testing writability at registration is worse than
// surfacing a write failure at first use). Identity is the symlink-resolved
// canonical absolute path; nothing else is stored.

/** Bounded time for any filesystem probe; fail closed beyond it. */
export const CHECK_TIMEOUT_MILLIS = 2000

export type DirectoryCheckResult = {
  readonly path: string
  readonly state: typeof DirectoryState.Type
  readonly checkedAt: string
  /** Present only when the state is not conclusive (e.g. "timeout"). */
  readonly reason?: string
}

export class Directories extends Context.Service<
  Directories,
  {
    /**
     * Validate `path` as a usable directory and return its canonical form.
     * Fails closed: `DirectoryUnavailable` with the tagged state, or
     * `DirectoryCheckTimedOut` when the probe exceeds the bounded timeout.
     */
    readonly canonicalize: (
      path: string,
    ) => Effect.Effect<string, DirectoryUnavailable | DirectoryCheckTimedOut>
    /**
     * Explicit health check: never fails — a timeout is reported as
     * `unknown` with a reason, because it is not proof the path is missing.
     */
    readonly check: (path: string) => Effect.Effect<DirectoryCheckResult>
  }
>()("app-server/Directories") {}

type Probe =
  | { readonly ok: true; readonly canonical: string }
  | { readonly ok: false; readonly state: DirectoryUnavailable["state"] }

// One filesystem probe: canonicalize, require a directory, require
// read+traversal. Node errno codes map onto the tagged states.
const probe = (path: string): Promise<Probe> =>
  (async () => {
    let canonical: string
    try {
      canonical = await fs.realpath(path)
    } catch (error) {
      // ENOTDIR: a path component exists but is not a directory.
      return {
        ok: false as const,
        state:
          errno(error) === "ENOENT"
            ? "missing"
            : errno(error) === "ENOTDIR"
              ? "not_directory"
              : "inaccessible",
      }
    }
    let isDirectory: boolean
    try {
      isDirectory = (await fs.stat(canonical)).isDirectory()
    } catch {
      return { ok: false as const, state: "inaccessible" }
    }
    if (!isDirectory) return { ok: false as const, state: "not_directory" }
    try {
      await fs.access(canonical, constants.R_OK | constants.X_OK)
    } catch {
      return { ok: false as const, state: "inaccessible" }
    }
    return { ok: true as const, canonical }
  })()

const errno = (error: unknown): string | undefined =>
  error instanceof Error && "code" in error ? String(error.code) : undefined

const timedProbe = (path: string) =>
  Effect.promise(() => probe(path)).pipe(
    Effect.timeoutOrElse({
      duration: CHECK_TIMEOUT_MILLIS,
      orElse: () => Effect.fail(new DirectoryCheckTimedOut({ path })),
    }),
  )

export const layer = Layer.succeed(Directories)({
  canonicalize: (path) =>
    timedProbe(path).pipe(
      Effect.flatMap((result) =>
        result.ok
          ? Effect.succeed(result.canonical)
          : Effect.fail(new DirectoryUnavailable({ path, state: result.state })),
      ),
    ),
  check: (path) =>
    timedProbe(path).pipe(
      Effect.map((result): DirectoryCheckResult => ({
        path: result.ok ? result.canonical : path,
        state: result.ok ? "available" : result.state,
        checkedAt: new Date().toISOString(),
      })),
      Effect.catch(() =>
        Effect.succeed<DirectoryCheckResult>({
          path,
          state: "unknown",
          checkedAt: new Date().toISOString(),
          reason: "timeout",
        }),
      ),
    ),
})
