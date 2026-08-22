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
  // `command` is a JSON argv array or NULL (interactive login shell).
  // `status` is 'starting' (internal, crash-mid-create marker), 'live', or
  // 'ended' (tombstone until deleted). ON DELETE RESTRICT enforces the
  // project-deletion rule at the storage layer too.
  "0002_terminals": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`
      CREATE TABLE terminals (
        id TEXT PRIMARY KEY,
        project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
        name TEXT,
        command TEXT,
        initial_working_directory TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('starting', 'live', 'ended')),
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        ended_at TEXT
      ) STRICT
    `
    yield* sql`CREATE INDEX terminals_project_id ON terminals(project_id)`
  }),
  // Threads (ATC-124): durable ATC identity separate from provider identity.
  // `provider_session_id` stays NULL until the provider session materializes
  // on first interaction; `confirmed_at` marks a completed first turn;
  // `provider_metadata` is an opaque adapter-owned blob the domain never
  // reads. `agent_id` is validated against the registry in code, not here —
  // adding an agent must never need a migration. `terminals.thread_id` is
  // the authoritative side of the Thread↔Terminal link; SET NULL keeps ended
  // linked terminals as unlinked tombstones when their thread is deleted
  // (the domain kills the live one first).
  "0003_threads": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`
      CREATE TABLE threads (
        id TEXT PRIMARY KEY,
        project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
        agent_id TEXT NOT NULL,
        name TEXT,
        working_directory TEXT NOT NULL,
        provider_session_id TEXT,
        confirmed_at TEXT,
        provider_metadata TEXT,
        archived_at TEXT,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
      ) STRICT
    `
    yield* sql`CREATE INDEX threads_project_id ON threads(project_id)`
    yield* sql`
      ALTER TABLE terminals
      ADD COLUMN thread_id TEXT REFERENCES threads(id) ON DELETE SET NULL
    `
  }),
  "0004_thread_pins": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`ALTER TABLE threads ADD COLUMN pinned_at TEXT`
  }),
  // Unread overlay (ATC-160): `last_finished_at` is stamped at the observed
  // busy→idle activity drop, `last_viewed_at` by markThreadViewed. Both NULL
  // on existing rows, so rollout marks nothing unread.
  "0005_thread_unread": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`ALTER TABLE threads ADD COLUMN last_finished_at TEXT`
    yield* sql`ALTER TABLE threads ADD COLUMN last_viewed_at TEXT`
  }),
  // The Thread runtime's durable state (ATC-193): the normalized transcript
  // copy — a projection of provider history, never the authority — and the
  // prompt queue. `transcript_seq` is the thread's monotonic change counter
  // (every item/turn write takes the next value into `updated_seq`; `ord`
  // is the value taken at first insert, i.e. transcript order);
  // `snapshot_version` bumps when a provider re-read replaces the copy and
  // `snapshot_seq` records the seq that replacement took, so a subscriber
  // rejoining from before it is told to refetch instead of replayed rows
  // that no longer describe what was deleted.
  // `item` is the ThreadItem JSON verbatim. `source` is internal
  // (native | observed | history). Queue rows with a NULL started_turn_id
  // are still waiting. CASCADE: a deleted thread takes its runtime state.
  "0006_thread_runtime": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`ALTER TABLE threads ADD COLUMN transcript_seq INTEGER NOT NULL DEFAULT 0`
    yield* sql`ALTER TABLE threads ADD COLUMN snapshot_version INTEGER NOT NULL DEFAULT 0`
    yield* sql`ALTER TABLE threads ADD COLUMN snapshot_seq INTEGER NOT NULL DEFAULT 0`
    yield* sql`
      CREATE TABLE thread_turns (
        thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
        turn_id TEXT NOT NULL,
        ord INTEGER NOT NULL,
        updated_seq INTEGER NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'interrupted', 'failed')),
        error TEXT,
        prompt_id TEXT,
        started_at TEXT,
        ended_at TEXT,
        source TEXT NOT NULL CHECK (source IN ('native', 'observed', 'history')),
        PRIMARY KEY (thread_id, turn_id)
      ) STRICT
    `
    yield* sql`
      CREATE TABLE thread_items (
        thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
        item_id TEXT NOT NULL,
        turn_id TEXT NOT NULL,
        ord INTEGER NOT NULL,
        updated_seq INTEGER NOT NULL,
        item TEXT NOT NULL,
        PRIMARY KEY (thread_id, item_id)
      ) STRICT
    `
    yield* sql`CREATE INDEX thread_items_thread_ord ON thread_items(thread_id, ord)`
    // The replay query (`updated_seq > ?` per thread) on both tables.
    yield* sql`CREATE INDEX thread_items_thread_seq ON thread_items(thread_id, updated_seq)`
    yield* sql`CREATE INDEX thread_turns_thread_seq ON thread_turns(thread_id, updated_seq)`
    yield* sql`
      CREATE TABLE thread_queue (
        id TEXT PRIMARY KEY,
        thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
        prompt TEXT NOT NULL,
        queued_at TEXT NOT NULL,
        started_turn_id TEXT
      ) STRICT
    `
    yield* sql`CREATE INDEX thread_queue_thread_id ON thread_queue(thread_id)`
  }),
  // Chat mode settings (ATC-205): the thread's model / reasoning / mode /
  // access — what its next turn runs with — and the per-agent write-through
  // defaults a new thread inherits. Rows that predate the columns are
  // backfilled with the seed each agent's registry entry carried when this
  // migration shipped (the registry table is the living seed for agents
  // with no defaults row); their next turn runs with it until the user
  // picks another. `reasoning` is NULL for models without effort support.
  "0007_thread_settings": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`ALTER TABLE threads ADD COLUMN model TEXT NOT NULL DEFAULT ''`
    yield* sql`ALTER TABLE threads ADD COLUMN reasoning TEXT`
    yield* sql`ALTER TABLE threads ADD COLUMN mode TEXT NOT NULL DEFAULT 'chat'`
    yield* sql`ALTER TABLE threads ADD COLUMN access TEXT NOT NULL DEFAULT 'auto'`
    yield* sql`
      UPDATE threads SET
        model = CASE agent_id WHEN 'claude-code' THEN 'opus[1m]' ELSE 'gpt-5.6-sol' END,
        reasoning = 'high'
    `
    yield* sql`
      CREATE TABLE agent_defaults (
        agent_id TEXT PRIMARY KEY,
        model TEXT NOT NULL,
        reasoning TEXT,
        mode TEXT NOT NULL,
        access TEXT NOT NULL,
        updated_at TEXT NOT NULL
      ) STRICT
    `
  }),
  // Item timestamps (ATC-214): when an item was created and when a tool item
  // finished. Stamped by the transcript repository (provider time when the
  // adapter passes one) and preserved across provider re-reads by item id.
  // Rows that predate the columns stay NULL — their stored item JSON carries
  // no timestamps either, and the contract fields are optional.
  "0008_thread_item_timestamps": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`ALTER TABLE thread_items ADD COLUMN created_at TEXT`
    yield* sql`ALTER TABLE thread_items ADD COLUMN completed_at TEXT`
  }),
  // Attachments (ATC-216): the metadata of images uploaded to a thread
  // (the bytes live under the data directory, keyed by thread and
  // attachment id — attachments.ts), and the attachment ids a queued prompt
  // carries (a JSON array of ids, '[]' for none). Both cascade with the
  // thread row; the blob directory is removed by Threads.delete.
  "0009_thread_attachments": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`
      CREATE TABLE thread_attachments (
        id TEXT PRIMARY KEY,
        thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
        name TEXT NOT NULL,
        media_type TEXT NOT NULL,
        byte_size INTEGER NOT NULL,
        created_at TEXT NOT NULL
      ) STRICT
    `
    yield* sql`CREATE INDEX thread_attachments_thread_id ON thread_attachments(thread_id)`
    yield* sql`ALTER TABLE thread_queue ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'`
  }),
  // Thread kinds (ATC-224): `kind` is 'chat' (ATC's own process drives the
  // thread) or 'tui' (a terminal ATC launches and observes drives it),
  // chosen at creation and never changed. One-time classification of
  // existing rows, no compat path: a row is tui only when it has at least
  // one turn, every turn was observed or read from history (never driven
  // by ATC), and nothing is queued; everything else — threads with no
  // turns included — is chat. A live TUI terminal still linked to a row
  // that comes out chat is accepted residue: the thread never shows or
  // observes it again, it stays an ordinary terminal in the terminal list
  // until it exits or is deleted, and archive/delete still reap it.
  "0010_thread_kind": Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient
    yield* sql`
      ALTER TABLE threads
      ADD COLUMN kind TEXT NOT NULL DEFAULT 'chat' CHECK (kind IN ('chat', 'tui'))
    `
    yield* sql`
      UPDATE threads SET kind = 'tui'
      WHERE EXISTS (SELECT 1 FROM thread_turns WHERE thread_turns.thread_id = threads.id)
        AND NOT EXISTS (
          SELECT 1 FROM thread_turns
          WHERE thread_turns.thread_id = threads.id AND thread_turns.source = 'native'
        )
        AND NOT EXISTS (SELECT 1 FROM thread_queue WHERE thread_queue.thread_id = threads.id)
    `
  }),
}
