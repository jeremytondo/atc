import { SqliteClient, SqliteMigrator } from "@effect/sql-sqlite-bun"
import { Effect, FileSystem, Layer } from "effect"
import { Migrator, SqlClient } from "effect/unstable/sql"
import { AppConfig } from "./config.ts"
import { migrations } from "./migrations.ts"

// The persistence foundation (ATC-121): a SQLite database in the settled data
// directory, exposed as the `SqlClient` service with the checked-in migration
// record already applied. `SqlClient` is the persistence boundary —
// repositories consume it and own their row types; `sql.withTransaction` is
// the transaction boundary. Nothing outside a repository speaks SQL.
//
// Deliberate SQLite settings:
//   journal_mode = WAL   concurrent readers during writes (driver default)
//   busy_timeout = 5s    writers wait instead of failing fast on a locked db
//   foreign_keys = ON    enforce references once cross-resource tables land
//
// Migrations run transactionally at layer build with library bookkeeping
// (effect_sql_migrations), duplicate-id detection, and concurrent-run
// locking. A failed migration rolls back and halts boot as a defect naming
// the migration; the CLI boundary maps it to the one-line stderr contract.

/** PRAGMAs then migration application on top of an existing `SqlClient`. */
const setup = Layer.effectDiscard(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`PRAGMA busy_timeout = 5000`
    yield* sql`PRAGMA foreign_keys = ON`
    yield* SqliteMigrator.run({ loader: Migrator.fromRecord(migrations) })
  }),
)

/** The migrated `SqlClient` at `file` (test layers point this at temp files). */
export const layerFile = (file: string): Layer.Layer<SqlClient.SqlClient, unknown, never> =>
  setup.pipe(Layer.provideMerge(SqliteClient.layer({ filename: file })))

/**
 * Production persistence: the database at the configured location, creating
 * the data directory on first run (the directory, never user directories —
 * see the Domain Model's "ATC never creates or deletes directories" rule,
 * which is about project working directories).
 */
export const layer = Layer.unwrap(
  Effect.gen(function* () {
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    yield* fs.makeDirectory(config.dataDir, { recursive: true })
    return layerFile(config.dbFile)
  }),
)
