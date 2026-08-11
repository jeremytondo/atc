import { Context, Effect, Layer } from "effect"
import { constants } from "node:fs"
import * as fs from "node:fs/promises"
import { dirname, join } from "node:path"
import { DirectoryCheckTimedOut, DirectoryUnavailable, DirectoryState } from "../api/contract.ts"
import { AppConfig } from "./config.ts"

// Demand-driven directory health (ATC-121, Domain Model rules) and the
// read-only subdirectory listing behind the folder browser (ATC-151): checks
// and listings run only when an operation needs a directory or a client asks
// explicitly — results are never persisted and normal reads never touch the
// filesystem.
// Validation requires an existing directory with read+traversal access
// (deliberately not write: testing writability at registration is worse than
// surfacing a write failure at first use). Identity is the symlink-resolved
// canonical absolute path; nothing else is stored.
// The individual calls stay on node:fs/promises deliberately: the platform
// FileSystem service can express neither the traversal (X_OK) access check
// nor d_type-carrying directory reads (losing d_type would cost a stat per
// entry). The pipeline around them is Effect, so the bounded timeout
// interrupts between steps — queued work never starts after the deadline
// (an already-in-flight OS call still completes harmlessly).

/** Bounded time for any filesystem probe; fail closed beyond it. */
export const CHECK_TIMEOUT_MILLIS = 2000

/** Concurrent `stat` calls per listing; bounds the fan-out a listing runs. */
export const STAT_CONCURRENCY = 16

export type DirectoryCheckResult = {
  readonly path: string
  readonly state: typeof DirectoryState.Type
  readonly checkedAt: string
  /** Present only when the state is not conclusive (e.g. "timeout"). */
  readonly reason?: string
}

export type DirectoryListing = {
  /** Canonical (symlink-resolved) path of the listed directory. */
  readonly path: string
  /** Parent of `path`; absent at the filesystem root. */
  readonly parent?: string
  readonly entries: ReadonlyArray<{ readonly name: string; readonly path: string }>
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
    /**
     * List a directory's subdirectories (omitted `path` = the server's home
     * directory): non-recursive, dotfolders excluded, symlinks included when
     * they resolve to directories, sorted case-insensitively. Fails closed
     * like `canonicalize`, under the same bounded timeout.
     */
    readonly list: (
      path?: string,
    ) => Effect.Effect<DirectoryListing, DirectoryUnavailable | DirectoryCheckTimedOut>
  }
>()("app-server/Directories") {}

const errno = (error: unknown): string | undefined =>
  error instanceof Error && "code" in error ? String(error.code) : undefined

// ENOTDIR: a path component exists but is not a directory.
const stateFromErrno = (error: unknown): DirectoryUnavailable["state"] =>
  errno(error) === "ENOENT"
    ? "missing"
    : errno(error) === "ENOTDIR"
      ? "not_directory"
      : "inaccessible"

const unavailable = (path: string, state: DirectoryUnavailable["state"]) =>
  new DirectoryUnavailable({ path, state })

// One filesystem probe: canonicalize, require a directory, require
// read+traversal. Node errno codes map onto the tagged states.
const probe = (path: string): Effect.Effect<string, DirectoryUnavailable> =>
  Effect.gen(function* () {
    const canonical = yield* Effect.tryPromise({
      try: () => fs.realpath(path),
      catch: (error) => unavailable(path, stateFromErrno(error)),
    })
    const stat = yield* Effect.tryPromise({
      try: () => fs.stat(canonical),
      catch: () => unavailable(path, "inaccessible"),
    })
    if (!stat.isDirectory()) return yield* Effect.fail(unavailable(path, "not_directory"))
    yield* Effect.tryPromise({
      try: () => fs.access(canonical, constants.R_OK | constants.X_OK),
      catch: () => unavailable(path, "inaccessible"),
    })
    return canonical
  })

/** Run a filesystem probe under the bounded timeout; fail closed beyond it. */
const bounded = <A, E>(
  path: string,
  effect: Effect.Effect<A, E>,
): Effect.Effect<A, E | DirectoryCheckTimedOut> =>
  effect.pipe(
    Effect.timeoutOrElse({
      duration: CHECK_TIMEOUT_MILLIS,
      orElse: () => Effect.fail(new DirectoryCheckTimedOut({ path })),
    }),
  )

// Probe the directory, then read its subdirectories. Entry paths are joined,
// not resolved: a symlink entry keeps its name-path, and descending into it
// canonicalizes naturally on the next list.
const listProbe = (path: string): Effect.Effect<DirectoryListing, DirectoryUnavailable> =>
  Effect.gen(function* () {
    const canonical = yield* probe(path)
    const dirents = yield* Effect.tryPromise({
      try: () => fs.readdir(canonical, { withFileTypes: true }),
      // The directory can vanish between probe and readdir.
      catch: (error) => unavailable(path, stateFromErrno(error)),
    })
    const entries: Array<{ name: string; path: string }> = []
    const unclassified: Array<{ name: string; path: string }> = []
    for (const dirent of dirents) {
      if (dirent.name.startsWith(".")) continue
      const entryPath = join(canonical, dirent.name)
      if (dirent.isDirectory()) {
        entries.push({ name: dirent.name, path: entryPath })
        continue
      }
      // Everything else known is not a directory; what remains is a symlink
      // (included only when it resolves to a directory) or UV_DIRENT_UNKNOWN
      // (d_type-less filesystems like NFS report every entry unknown), and
      // both need a stat to classify.
      if (
        dirent.isFile() ||
        dirent.isFIFO() ||
        dirent.isSocket() ||
        dirent.isCharacterDevice() ||
        dirent.isBlockDevice()
      ) {
        continue
      }
      unclassified.push({ name: dirent.name, path: entryPath })
    }
    // Stat the unclassified entries with bounded concurrency: d_type-less
    // filesystems report whole directories this way (see the header for the
    // interruption behavior).
    const classified = yield* Effect.forEach(
      unclassified,
      (entry) =>
        Effect.tryPromise({ try: () => fs.stat(entry.path), catch: () => null }).pipe(
          Effect.map((stat) => (stat.isDirectory() ? entry : null)),
          // Broken or unreadable symlink, or a vanished entry: skip it.
          Effect.orElseSucceed(() => null),
        ),
      { concurrency: STAT_CONCURRENCY },
    )
    entries.push(...classified.filter((entry) => entry !== null))
    // Pinned collation, so the advertised case-insensitive order does not
    // drift with the server's locale.
    entries.sort((a, b) => a.name.localeCompare(b.name, "en"))
    return {
      path: canonical,
      ...(canonical === "/" ? {} : { parent: dirname(canonical) }),
      entries,
    }
  })

export const layer = Layer.effect(Directories)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    return {
      canonicalize: (path) => bounded(path, probe(path)),
      check: (path) =>
        bounded(path, probe(path)).pipe(
          Effect.map((canonical): DirectoryCheckResult => ({
            path: canonical,
            state: "available",
            checkedAt: new Date().toISOString(),
          })),
          Effect.catchTag("DirectoryUnavailable", (error) =>
            Effect.succeed<DirectoryCheckResult>({
              path,
              state: error.state,
              checkedAt: new Date().toISOString(),
            }),
          ),
          Effect.catchTag("DirectoryCheckTimedOut", () =>
            Effect.succeed<DirectoryCheckResult>({
              path,
              state: "unknown",
              checkedAt: new Date().toISOString(),
              reason: "timeout",
            }),
          ),
        ),
      list: (path) => {
        const target = path ?? config.home
        return bounded(target, listProbe(target))
      },
    } satisfies Directories["Service"]
  }),
)
