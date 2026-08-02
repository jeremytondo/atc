import { Effect } from "effect"
import { SqlClient } from "effect/unstable/sql"

// The checked-in, append-only migration record (ATC-121). Keys are
// `<id>_<name>` (zero-padded for readability; the loader parses the numeric
// id). Never edit or remove an entry once it has shipped — append a new one.
// The record is imported into the binary, so the compiled executable migrates
// without any filesystem dependency.
//
// `Migrator.fromRecord` silently drops keys that don't match /^(\d+)_(.+)$/;
// persistence.test.ts asserts the loaded count equals the record size so a
// malformed key cannot silently skip a migration.

export const migrations: Record<string, Effect.Effect<void, unknown, SqlClient.SqlClient>> = {
  "0001_init": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`
      CREATE TABLE projects (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        default_working_directory TEXT NOT NULL,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
      ) STRICT
    `
  }),
}
