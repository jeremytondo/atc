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

/** Bounded time for any filesystem probe; fail closed beyond it. */
export const CHECK_TIMEOUT_MILLIS = 2000

/** Concurrent `stat` calls per listing; bounds the work a timeout abandons. */
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
      return { ok: false as const, state: stateFromErrno(error) }
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

// ENOTDIR: a path component exists but is not a directory.
const stateFromErrno = (error: unknown): DirectoryUnavailable["state"] =>
  errno(error) === "ENOENT"
    ? "missing"
    : errno(error) === "ENOTDIR"
      ? "not_directory"
      : "inaccessible"

/** Run a filesystem probe under the bounded timeout; fail closed beyond it. */
const bounded = <T>(path: string, run: () => Promise<T>) =>
  Effect.promise(run).pipe(
    Effect.timeoutOrElse({
      duration: CHECK_TIMEOUT_MILLIS,
      orElse: () => Effect.fail(new DirectoryCheckTimedOut({ path })),
    }),
  )

type ListProbe =
  | { readonly ok: true; readonly listing: DirectoryListing }
  | { readonly ok: false; readonly state: DirectoryUnavailable["state"] }

// Probe the directory, then read its subdirectories. Entry paths are joined,
// not resolved: a symlink entry keeps its name-path, and descending into it
// canonicalizes naturally on the next list.
const listProbe = (path: string): Promise<ListProbe> =>
  (async () => {
    const probed = await probe(path)
    if (!probed.ok) return probed
    const canonical = probed.canonical
    let dirents
    try {
      dirents = await fs.readdir(canonical, { withFileTypes: true })
    } catch (error) {
      // The directory can vanish between probe and readdir.
      return { ok: false as const, state: stateFromErrno(error) }
    }
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
    // Stat the unclassified entries through a small worker pool: d_type-less
    // filesystems report whole directories this way, and the bounded timeout
    // abandons rather than cancels these promises — so the pool, not the
    // timeout, is what keeps a huge directory from piling up thousands of
    // in-flight stats per request.
    const classified: Array<{ name: string; path: string } | null> = new Array(unclassified.length)
    let nextIndex = 0
    const statWorker = async () => {
      while (true) {
        const index = nextIndex++
        const entry = unclassified[index]
        if (entry === undefined) return
        try {
          classified[index] = (await fs.stat(entry.path)).isDirectory() ? entry : null
        } catch {
          // Broken or unreadable symlink, or a vanished entry: skip it.
          classified[index] = null
        }
      }
    }
    await Promise.all(
      Array.from({ length: Math.min(STAT_CONCURRENCY, unclassified.length) }, statWorker),
    )
    entries.push(...classified.filter((entry) => entry !== null))
    // Pinned collation, so the advertised case-insensitive order does not
    // drift with the server's locale.
    entries.sort((a, b) => a.name.localeCompare(b.name, "en"))
    return {
      ok: true as const,
      listing: {
        path: canonical,
        ...(canonical === "/" ? {} : { parent: dirname(canonical) }),
        entries,
      },
    }
  })()

export const layer = Layer.effect(Directories)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    return {
      canonicalize: (path) =>
        bounded(path, () => probe(path)).pipe(
          Effect.flatMap((result) =>
            result.ok
              ? Effect.succeed(result.canonical)
              : Effect.fail(new DirectoryUnavailable({ path, state: result.state })),
          ),
        ),
      check: (path) =>
        bounded(path, () => probe(path)).pipe(
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
      list: (path) => {
        const target = path ?? config.home
        return bounded(target, () => listProbe(target)).pipe(
          Effect.flatMap((result) =>
            result.ok
              ? Effect.succeed(result.listing)
              : Effect.fail(new DirectoryUnavailable({ path: target, state: result.state })),
          ),
        )
      },
    } satisfies Directories["Service"]
  }),
)
