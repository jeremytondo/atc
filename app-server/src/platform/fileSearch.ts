import { Cache, Context, Duration, Effect, Exit, Layer } from "effect"
import ignore from "ignore"
import * as fs from "node:fs/promises"
import { join } from "node:path"
import { DirectoryUnavailable } from "../api/contract.ts"
import type { DirectoryCheckTimedOut } from "../api/contract.ts"
import type * as Contract from "../api/contract.ts"
import { Directories } from "./directories.ts"

// Filename search over one directory tree (ATC-216): what the composer's
// `@` mention ranks from. Directory-driven, not project-scoped, so it
// follows a thread's working directory (worktrees included) and a
// not-yet-created thread's directory chip alike. Pure TypeScript on
// purpose: no native module in the compiled binary, and at the scale of a
// capped filename index a readdir walk plus subsequence scoring answers
// within a frame; the contract hides the engine so it can be swapped.
//
//   - The index of a directory is built by one bounded walk (WALK_CAP
//     entries seen, then `truncated`; READ_CONCURRENCY directories read at
//     a time), skipping `.git` and everything the tree's `.gitignore` files
//     exclude — each file's patterns apply to its own subtree and a deeper
//     rule overrides a shallower one, as git applies them — and is cached
//     for INDEX_TTL, so a keystroke re-ranks in memory and only the first
//     query pays the walk. A part of the tree that cannot be read fails the
//     walk (DirectoryUnavailable), never a partial index; a failed walk is
//     never cached, so the next query retries.
//   - Ranking (T3Code's searchRanking): on the basename first — exact,
//     prefix, word boundary, substring, subsequence — then the same tiers on
//     the relative path; lower scores win, ties break by path. An empty
//     query lists the index in path order.
//   - Nothing is persisted, nothing is watched: freshness is the TTL.

/** Entries (files and directories) one walk looks at; past it the index reports `truncated`. */
export const WALK_CAP = 50_000

/** How long a built index answers queries before the tree is walked again. */
export const INDEX_TTL: Duration.Input = "10 seconds"

export type FileMatch = typeof Contract.FsFileEntry.Type
export type FileSearchResult = typeof Contract.FsFilesResponse.Type

/** Matches returned when the caller names no limit. */
export const DEFAULT_LIMIT = 25

interface FileIndex {
  readonly files: ReadonlyArray<FileMatch>
  readonly truncated: boolean
}

export class FileSearch extends Context.Service<
  FileSearch,
  {
    /**
     * The best `limit` (default DEFAULT_LIMIT) matches for `query` (default
     * none: path order) under `dir`, canonicalized and validated like every
     * directory the server touches.
     */
    readonly search: (options: {
      readonly dir: string
      readonly query?: string | undefined
      readonly limit?: number | undefined
    }) => Effect.Effect<FileSearchResult, DirectoryUnavailable | DirectoryCheckTimedOut>
  }
>()("app-server/FileSearch") {}

/** A gitignore matcher with the subtree (relative to the index root) it governs. */
interface IgnoreScope {
  readonly base: string
  readonly matcher: ReturnType<typeof ignore>
}

interface WalkDirectory {
  readonly relative: string
  readonly scopes: ReadonlyArray<IgnoreScope>
}

/** Directories read concurrently at once; bounds the walk's fan-out. */
const READ_CONCURRENCY = 16

/** Errno codes that mean "not there (any more)" — never a failure of the walk. */
const ABSENT = new Set(["ENOENT", "ENOTDIR", "EISDIR"])

const isAbsent = (error: unknown): boolean =>
  error instanceof Error && "code" in error && ABSENT.has(String(error.code))

const readIgnoreScope = async (directory: string, base: string): Promise<IgnoreScope | null> => {
  try {
    const text = await fs.readFile(join(directory, ".gitignore"), "utf8")
    return { base, matcher: ignore().add(text) }
  } catch (error) {
    if (isAbsent(error)) return null
    throw error
  }
}

/**
 * Whether the governing `.gitignore` files exclude `relativePath`, as git
 * decides it: the scopes run root-first and the deepest rule that speaks
 * wins, so a nested `!keep.log` re-includes what the root ignored. A
 * directory is tested with a trailing slash (directory-only patterns).
 */
const isIgnored = (
  scopes: ReadonlyArray<IgnoreScope>,
  relativePath: string,
  isDirectory: boolean,
): boolean => {
  let ignored = false
  for (const scope of scopes) {
    const within = scope.base === "" ? relativePath : relativePath.slice(scope.base.length + 1)
    const verdict = scope.matcher.test(isDirectory ? `${within}/` : within)
    if (verdict.ignored) ignored = true
    else if (verdict.unignored) ignored = false
  }
  return ignored
}

/** `fn` over `items`, READ_CONCURRENCY at a time (a recursion, not a loop). */
const mapBounded = async <A, B>(
  items: ReadonlyArray<A>,
  fn: (item: A) => Promise<B>,
  done: Array<B> = [],
): Promise<Array<B>> => {
  if (items.length === 0) return done
  const batch = await Promise.all(items.slice(0, READ_CONCURRENCY).map(fn))
  return mapBounded(items.slice(READ_CONCURRENCY), fn, [...done, ...batch])
}

/**
 * Walk `root` breadth-first into a file index (see the header): one level
 * of directories at a time, each level a recursion. Every entry seen —
 * file or directory — counts against WALK_CAP, so a wide tree of empty
 * directories ends too; `signal` ends an abandoned walk between levels.
 */
const buildIndex = async (root: string, signal: AbortSignal): Promise<FileIndex> => {
  const files: Array<FileMatch> = []
  let seen = 0
  const walkLevel = async (level: ReadonlyArray<WalkDirectory>): Promise<boolean> => {
    if (level.length === 0) return false
    if (signal.aborted) throw new Error("the walk was abandoned")
    const listed = await mapBounded(level, async (directory) => ({
      directory,
      dirents: await fs
        .readdir(directory.relative === "" ? root : join(root, directory.relative), {
          withFileTypes: true,
        })
        .catch((error: unknown) => {
          // The directory vanished since its parent listed it: nothing to
          // index. Anything else (permissions, I/O) fails the walk.
          if (isAbsent(error)) return []
          throw error
        }),
    }))
    const subdirectories: Array<WalkDirectory> = []
    for (const { directory, dirents } of listed) {
      for (const dirent of dirents) {
        if (dirent.name === ".git") continue
        if (seen >= WALK_CAP) return true
        seen += 1
        const path =
          directory.relative === "" ? dirent.name : `${directory.relative}/${dirent.name}`
        if (dirent.isDirectory()) {
          if (!isIgnored(directory.scopes, path, true)) {
            subdirectories.push({ relative: path, scopes: directory.scopes })
          }
          continue
        }
        if (!dirent.isFile() && !dirent.isSymbolicLink()) continue
        if (isIgnored(directory.scopes, path, false)) continue
        files.push({ path, name: dirent.name })
      }
    }
    // A subdirectory's own .gitignore governs its subtree from here on.
    const next = await mapBounded(subdirectories, async (directory) => {
      const scope = await readIgnoreScope(join(root, directory.relative), directory.relative)
      return scope === null
        ? directory
        : { relative: directory.relative, scopes: [...directory.scopes, scope] }
    })
    return walkLevel(next)
  }
  const rootScope = await readIgnoreScope(root, "")
  const truncated = await walkLevel([
    { relative: "", scopes: rootScope === null ? [] : [rootScope] },
  ])
  return { files, truncated }
}

// --- Ranking (T3Code searchRanking.ts, reduced to the filename case) ---

const lengthPenalty = (value: string, query: string): number =>
  Math.min(64, Math.max(0, value.length - query.length))

/** Gap-penalized subsequence score; null when `query` is not a subsequence. */
const subsequenceScore = (value: string, query: string): number | null => {
  let queryIndex = 0
  let first = -1
  let previous = -1
  let gaps = 0
  for (let index = 0; index < value.length; index += 1) {
    if (value[index] !== query[queryIndex]) continue
    if (first === -1) first = index
    if (previous !== -1) gaps += index - previous - 1
    previous = index
    queryIndex += 1
    if (queryIndex === query.length) {
      return first * 2 + gaps * 3 + (index - first + 1 - query.length) + lengthPenalty(value, query)
    }
  }
  return null
}

const BOUNDARIES = [" ", "-", "_", "/", "."]

/** Tiered score of `query` against `value` (both lowercased): exact, prefix,
 * word boundary, substring, subsequence — offsets keep the tiers apart. */
const tieredScore = (value: string, query: string, base: number): number | null => {
  if (value === query) return base
  if (value.startsWith(query)) return base + 100 + lengthPenalty(value, query)
  const boundary = BOUNDARIES.map((marker) => value.indexOf(`${marker}${query}`)).filter(
    (index) => index !== -1,
  )
  if (boundary.length > 0) {
    return base + 200 + Math.min(...boundary) * 2 + lengthPenalty(value, query)
  }
  const index = value.indexOf(query)
  if (index !== -1) return base + 300 + index * 2 + lengthPenalty(value, query)
  const fuzzy = subsequenceScore(value, query)
  return fuzzy === null ? null : base + 400 + fuzzy
}

/** The name's tiers beat the path's: a match on what the user sees first. */
const score = (file: FileMatch, query: string): number | null =>
  tieredScore(file.name.toLowerCase(), query, 0) ??
  tieredScore(file.path.toLowerCase(), query, 1_000)

const rank = (index: FileIndex, query: string, limit: number): ReadonlyArray<FileMatch> => {
  const normalized = query.trim().toLowerCase()
  if (normalized === "") {
    return index.files.toSorted((a, b) => a.path.localeCompare(b.path, "en")).slice(0, limit)
  }
  const scored: Array<{ readonly file: FileMatch; readonly score: number }> = []
  for (const file of index.files) {
    const value = score(file, normalized)
    if (value !== null) scored.push({ file, score: value })
  }
  return scored
    .toSorted((a, b) => a.score - b.score || a.file.path.localeCompare(b.file.path, "en"))
    .slice(0, limit)
    .map((entry) => entry.file)
}

export const layer = Layer.effect(FileSearch)(
  Effect.gen(function* () {
    const directories = yield* Directories
    // One index per canonical directory, shared by concurrent queries; a
    // failed walk is not kept (zero TTL), so the next query retries.
    const indexes = yield* Cache.makeWith(
      (dir: string) =>
        Effect.tryPromise({
          try: (signal) => buildIndex(dir, signal),
          // A walk that cannot read part of the tree is no listing at all —
          // the same fail-closed answer /fs/list gives.
          catch: () => new DirectoryUnavailable({ path: dir, state: "inaccessible" }),
        }),
      {
        capacity: 32,
        timeToLive: (exit) => (Exit.isSuccess(exit) ? INDEX_TTL : Duration.zero),
      },
    )
    return {
      search: (options) =>
        Effect.gen(function* () {
          const dir = yield* directories.canonicalize(options.dir)
          const index = yield* Cache.get(indexes, dir)
          return {
            dir,
            entries: rank(index, options.query ?? "", options.limit ?? DEFAULT_LIMIT),
            truncated: index.truncated,
          }
        }),
    } satisfies FileSearch["Service"]
  }),
)
