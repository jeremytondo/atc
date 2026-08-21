import { assert, describe, it } from "@effect/vitest"
import { Effect, Layer } from "effect"
import { chmodSync, mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import * as Directories from "../../src/platform/directories.ts"
import * as FileSearch from "../../src/platform/fileSearch.ts"
import { testAppConfig } from "../testLayers.ts"

// The filename search engine (ATC-216): the walk's exclusions, the ranking
// tiers, the limit, and the directory validation it shares with /fs/list.

const scratch = mkdtempSync(join(tmpdir(), "atc-file-search-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

const root = join(scratch, "project")
for (const directory of [
  "src/util",
  "src/generated",
  "node_modules/dep",
  ".git",
  "docs",
  "build",
]) {
  mkdirSync(join(root, directory), { recursive: true })
}
writeFileSync(join(root, ".gitignore"), "node_modules/\nbuild\n")
writeFileSync(join(root, "src", ".gitignore"), "generated/\n")
for (const file of [
  "README.md",
  "app.ts",
  "src/app.ts",
  "src/util/helpers.ts",
  "src/util/help.md",
  "src/generated/out.ts",
  "node_modules/dep/index.js",
  ".git/HEAD",
  "docs/Helping-Hands.md",
  "build/app.js",
  ".env",
]) {
  writeFileSync(join(root, file), "x")
}

const TestLayer = FileSearch.layer.pipe(
  Layer.provide(Directories.layer.pipe(Layer.provide(testAppConfig({})))),
)

const paths = (result: FileSearch.FileSearchResult) => result.entries.map((entry) => entry.path)

describe("FileSearch", () => {
  it.effect("walks the tree minus .git and every .gitignore's exclusions, in path order", () =>
    Effect.gen(function* () {
      const search = yield* FileSearch.FileSearch
      const result = yield* search.search({ dir: root, query: "", limit: 50 })
      assert.strictEqual(result.dir, realpathSync(root))
      assert.isFalse(result.truncated)
      // The .gitignore files themselves are files like any other; the order is
      // the pinned case-insensitive collation (README after docs).
      assert.deepStrictEqual(paths(result), [
        ".env",
        ".gitignore",
        "app.ts",
        "docs/Helping-Hands.md",
        "README.md",
        "src/.gitignore",
        "src/app.ts",
        "src/util/help.md",
        "src/util/helpers.ts",
      ])
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect(
    "ranks the name's tiers before the path's: exact, prefix, boundary, substring, fuzzy",
    () =>
      Effect.gen(function* () {
        const search = yield* FileSearch.FileSearch
        const help = yield* search.search({ dir: root, query: "help", limit: 10 })
        // Prefix on the name (help.md, helpers.ts), then a boundary match
        // (Helping-Hands.md starts with it too, case-insensitively, but is
        // longer), then nothing else carries the letters in order.
        assert.deepStrictEqual(paths(help), [
          "src/util/help.md",
          "src/util/helpers.ts",
          "docs/Helping-Hands.md",
        ])
        const app = yield* search.search({ dir: root, query: "app.ts", limit: 10 })
        // An exact name match beats an equal name deeper in the tree only by path.
        assert.deepStrictEqual(paths(app), ["app.ts", "src/app.ts"])
        const fuzzy = yield* search.search({ dir: root, query: "suhl", limit: 10 })
        // Subsequence on the path: s/u(til)/h(elp)...
        assert.deepStrictEqual(paths(fuzzy), ["src/util/help.md", "src/util/helpers.ts"])
        const none = yield* search.search({ dir: root, query: "zzz", limit: 10 })
        assert.deepStrictEqual(paths(none), [])
      }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("a nested .gitignore's negation re-includes what the root excluded", () =>
    Effect.gen(function* () {
      const nested = join(scratch, "nested")
      mkdirSync(join(nested, "logs"), { recursive: true })
      writeFileSync(join(nested, ".gitignore"), "*.log\n")
      writeFileSync(join(nested, "logs", ".gitignore"), "!keep.log\n")
      writeFileSync(join(nested, "logs", "keep.log"), "")
      writeFileSync(join(nested, "logs", "drop.log"), "")
      writeFileSync(join(nested, "top.log"), "")
      const search = yield* FileSearch.FileSearch
      const result = yield* search.search({ dir: nested, query: "" })
      assert.deepStrictEqual(paths(result), [".gitignore", "logs/.gitignore", "logs/keep.log"])
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("a subtree that cannot be read fails the walk instead of indexing around it", () =>
    Effect.gen(function* () {
      const locked = join(scratch, "locked")
      mkdirSync(join(locked, "secret"), { recursive: true })
      writeFileSync(join(locked, "open.txt"), "")
      chmodSync(join(locked, "secret"), 0o000)
      const search = yield* FileSearch.FileSearch
      const failure = yield* Effect.flip(search.search({ dir: locked, query: "" }))
      chmodSync(join(locked, "secret"), 0o755)
      assert.isTrue(failure._tag === "DirectoryUnavailable" && failure.state === "inaccessible")
    }).pipe(Effect.provide(TestLayer)),
  )

  it.effect("the limit caps the ranked list; an unusable directory is the tagged error", () =>
    Effect.gen(function* () {
      const search = yield* FileSearch.FileSearch
      const two = yield* search.search({ dir: root, query: "", limit: 2 })
      assert.deepStrictEqual(paths(two), [".env", ".gitignore"])
      const missing = yield* Effect.flip(
        search.search({ dir: join(scratch, "nope"), query: "", limit: 2 }),
      )
      assert.strictEqual(missing._tag, "DirectoryUnavailable")
    }).pipe(Effect.provide(TestLayer)),
  )
})
