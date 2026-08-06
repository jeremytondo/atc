import { assert, describe, it } from "@effect/vitest"
import { SqliteClient, SqliteMigrator } from "@effect/sql-sqlite-bun"
import { Effect, Layer } from "effect"
import { Migrator, SqlClient } from "effect/unstable/sql"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll } from "vitest"
import { migrations } from "../../src/platform/migrations.ts"
import * as Persistence from "../../src/platform/persistence.ts"

const scratch = mkdtempSync(join(tmpdir(), "atc-persistence-test-"))
afterAll(() => rmSync(scratch, { recursive: true, force: true }))

let dbId = 0
const freshDbFile = () => join(scratch, `test-${dbId++}.db`)

/** Run `effect` against one boot of the migrated database at `file`; the
 * client closes when the effect finishes, so sequential calls model restarts. */
const withDb = <A, E>(file: string, effect: Effect.Effect<A, E, SqlClient.SqlClient>) =>
  effect.pipe(Effect.provide(Persistence.layerFile(file)))

const tableNames = Effect.gen(function* () {
  const sql = yield* SqlClient.SqlClient
  const rows = yield* sql<{ name: string }>`
    SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name
  `
  return rows.map((row) => row.name)
})

const appliedCount = Effect.gen(function* () {
  const sql = yield* SqlClient.SqlClient
  const rows = yield* sql<{ count: number }>`
    SELECT COUNT(*) AS count FROM effect_sql_migrations
  `
  return Number(rows[0]!.count)
})

describe("persistence", () => {
  it.effect("creates and migrates a fresh database", () =>
    Effect.gen(function* () {
      const names = yield* withDb(freshDbFile(), tableNames)
      assert.include(names, "projects")
      assert.include(names, "effect_sql_migrations")
    }),
  )

  // fromRecord silently drops keys that don't match /^(\d+)_(.+)$/ — this
  // pins the whole record to the loader so a malformed key cannot silently
  // skip a migration.
  it.effect("the loader resolves every migration in the record", () =>
    Effect.gen(function* () {
      const resolved = yield* Migrator.fromRecord(migrations)
      assert.strictEqual(resolved.length, Object.keys(migrations).length)
    }),
  )

  it.effect("a restart is a clean no-op", () => {
    const file = freshDbFile()
    return Effect.gen(function* () {
      yield* withDb(file, Effect.void)
      const count = yield* withDb(file, appliedCount)
      assert.strictEqual(count, Object.keys(migrations).length)
    })
  })

  it.effect("applies the documented SQLite settings", () =>
    withDb(
      freshDbFile(),
      Effect.gen(function* () {
        const sql = yield* SqlClient.SqlClient
        const journalMode = yield* sql<{ journal_mode: string }>`PRAGMA journal_mode`
        const busyTimeout = yield* sql<{ timeout: number }>`PRAGMA busy_timeout`
        const foreignKeys = yield* sql<{ foreign_keys: number }>`PRAGMA foreign_keys`
        assert.strictEqual(journalMode[0]!.journal_mode, "wal")
        assert.strictEqual(Number(busyTimeout[0]!.timeout), 5000)
        assert.strictEqual(Number(foreignKeys[0]!.foreign_keys), 1)
      }),
    ),
  )

  it.effect("a failed migration halts boot naming the migration and rolls back", () => {
    const file = freshDbFile()
    const broken = {
      ...migrations,
      "0099_explode": Effect.gen(function* () {
        const sql = yield* SqlClient.SqlClient
        yield* sql`CREATE TABLE half_done (id TEXT PRIMARY KEY)`
        yield* sql`THIS IS NOT SQL`
      }),
    }
    const brokenBoot = Effect.void.pipe(
      Effect.provide(
        Layer.effectDiscard(SqliteMigrator.run({ loader: Migrator.fromRecord(broken) })).pipe(
          Layer.provideMerge(SqliteClient.layer({ filename: file })),
        ),
      ),
    )
    return Effect.gen(function* () {
      const exit = yield* Effect.exit(brokenBoot)
      assert.strictEqual(exit._tag, "Failure")
      // The diagnostic names the migration (the loader parses the numeric id,
      // so zero padding drops out of the rendered name).
      assert.include(String(exit), 'Migration "99_explode" failed')

      // The failed migration's DDL and bookkeeping rolled back together:
      // booting again with the fixed record succeeds and sees no leftovers.
      const names = yield* withDb(file, tableNames)
      assert.notInclude(names, "half_done")
      const count = yield* withDb(file, appliedCount)
      assert.strictEqual(count, Object.keys(migrations).length)
    })
  })

  it.effect("withTransaction commits on success and rolls back on failure", () =>
    withDb(
      freshDbFile(),
      Effect.gen(function* () {
        const sql = yield* SqlClient.SqlClient

        yield* sql.withTransaction(
          sql`INSERT INTO projects (id, name, default_working_directory, created_at, updated_at)
              VALUES ('a', 'kept', '/tmp', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
        )
        yield* sql`INSERT INTO projects (id, name, default_working_directory, created_at, updated_at)
                   VALUES ('b', 'dropped', '/tmp', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`.pipe(
          Effect.andThen(Effect.fail("boom" as const)),
          sql.withTransaction,
          Effect.ignore,
        )

        const rows = yield* sql<{ id: string }>`SELECT id FROM projects ORDER BY id`
        assert.deepStrictEqual(
          rows.map((row) => row.id),
          ["a"],
        )
      }),
    ),
  )
})
