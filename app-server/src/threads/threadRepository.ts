import { Context, Effect, Layer, Option, Schema } from "effect"
import { SqlClient, SqlSchema } from "effect/unstable/sql"
import { requireFound, rowHelpers } from "../platform/repositoryHelpers.ts"
import { ThreadNotFound } from "../api/contract.ts"
import type { AgentId } from "../api/contract.ts"

// The Threads repository (ATC-124): the only module that speaks SQL for
// threads. Row types stay here — the service and handlers see camelCase
// records. Provider identity (providerSessionId, confirmedAt) and the
// opaque adapter-owned providerMetadata live on the record but never reach
// public contract schemas.
//
// agent_id is validated at the write boundary (the contract's AgentId) but
// decoded permissively as a plain string: a row holding a slug this build
// does not know (written by a newer build) must never brick reads — above
// all delete, the recovery path.

export interface ThreadRecord {
  readonly id: string
  readonly projectId: string
  readonly agentId: string
  readonly name?: string
  readonly workingDirectory: string
  readonly providerSessionId?: string
  /** Set when the session's first turn completed (the confirmed marker). */
  readonly confirmedAt?: string
  /** Opaque adapter-owned blob; the domain never reads inside it. */
  readonly providerMetadata?: string
  readonly pinnedAt?: string
  readonly archivedAt?: string
  readonly createdAt: string
  readonly updatedAt: string
}

/** Internal row shape (snake_case columns, as stored). */
const ThreadRow = Schema.Struct({
  id: Schema.String,
  project_id: Schema.String,
  agent_id: Schema.String,
  name: Schema.NullOr(Schema.String),
  working_directory: Schema.String,
  provider_session_id: Schema.NullOr(Schema.String),
  confirmed_at: Schema.NullOr(Schema.String),
  provider_metadata: Schema.NullOr(Schema.String),
  pinned_at: Schema.NullOr(Schema.String),
  archived_at: Schema.NullOr(Schema.String),
  created_at: Schema.String,
  updated_at: Schema.String,
})

const toRecord = (row: typeof ThreadRow.Type): ThreadRecord => ({
  id: row.id,
  projectId: row.project_id,
  agentId: row.agent_id,
  ...(row.name !== null ? { name: row.name } : {}),
  workingDirectory: row.working_directory,
  ...(row.provider_session_id !== null ? { providerSessionId: row.provider_session_id } : {}),
  ...(row.confirmed_at !== null ? { confirmedAt: row.confirmed_at } : {}),
  ...(row.provider_metadata !== null ? { providerMetadata: row.provider_metadata } : {}),
  ...(row.pinned_at !== null ? { pinnedAt: row.pinned_at } : {}),
  ...(row.archived_at !== null ? { archivedAt: row.archived_at } : {}),
  createdAt: row.created_at,
  updatedAt: row.updated_at,
})

export class ThreadRepository extends Context.Service<
  ThreadRepository,
  {
    /** Insert a new record and return it (id is generated). */
    readonly create: (input: {
      readonly projectId: string
      readonly agentId: typeof AgentId.Type
      readonly name?: string | undefined
      readonly workingDirectory: string
    }) => Effect.Effect<ThreadRecord>
    /** All records (archived included), newest first; optionally one project's. */
    readonly list: (projectId?: string) => Effect.Effect<ReadonlyArray<ThreadRecord>>
    readonly get: (id: string) => Effect.Effect<Option.Option<ThreadRecord>>
    readonly require: (id: string) => Effect.Effect<ThreadRecord, ThreadNotFound>
    /** Update the display label; callers hold the record — a vanished row is a bug. */
    readonly rename: (id: string, name: string) => Effect.Effect<ThreadRecord>
    /**
     * Adopt a generated name ONLY while the thread is still unnamed
     * (ATC-155): the name-IS-NULL guard is in the UPDATE itself, so a
     * manual rename landing mid-generation wins the race atomically. None
     * when nothing changed — already named, or the row vanished.
     */
    readonly renameIfUnnamed: (
      id: string,
      name: string,
    ) => Effect.Effect<Option.Option<ThreadRecord>>
    /**
     * Adopt a freshly established provider identity: session id and opaque
     * metadata together, clearing the confirmed marker (a fresh session has
     * no completed turn yet). Callers hold the record (see rename).
     */
    readonly setProviderSession: (
      id: string,
      providerSessionId: string,
      providerMetadata: string | null,
    ) => Effect.Effect<ThreadRecord>
    /** Update only the opaque adapter metadata; callers hold the record. */
    readonly setProviderMetadata: (
      id: string,
      providerMetadata: string,
    ) => Effect.Effect<ThreadRecord>
    /**
     * Set the confirmed marker (first completed turn observed). Idempotent
     * and tolerant: fed by the live status feed, which can race a delete —
     * a vanished row is a no-op, and an existing marker is never rewritten.
     */
    readonly confirm: (id: string) => Effect.Effect<void>
    /** Set or clear the pin marker; callers hold the record (see rename). */
    readonly setPinned: (id: string, pinned: boolean) => Effect.Effect<ThreadRecord>
    /** Set or clear the archive marker; callers hold the record (see rename). */
    readonly setArchived: (id: string, archived: boolean) => Effect.Effect<ThreadRecord>
    readonly delete: (id: string) => Effect.Effect<void>
  }
>()("app-server/ThreadRepository") {}

// Database-failure policy (defects, orDie at this boundary): see
// platform/repositoryHelpers.ts.
export const layer = Layer.effect(ThreadRepository)(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient

    const insertRow = SqlSchema.void({
      Request: ThreadRow,
      execute: (row) => sql`INSERT INTO threads ${sql.insert(row)}`,
    })

    const listRows = SqlSchema.findAll({
      Request: Schema.NullOr(Schema.String),
      Result: ThreadRow,
      execute: (projectId) =>
        projectId === null
          ? sql`SELECT * FROM threads ORDER BY id DESC`
          : sql`SELECT * FROM threads WHERE project_id = ${projectId} ORDER BY id DESC`,
    })

    const getRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: ThreadRow,
      execute: (id) => sql`SELECT * FROM threads WHERE id = ${id}`,
    })

    const renameRows = SqlSchema.findAll({
      Request: Schema.Struct({ id: Schema.String, name: Schema.String, updated_at: Schema.String }),
      Result: ThreadRow,
      execute: (patch) => sql`
        UPDATE threads SET name = ${patch.name}, updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    const renameIfUnnamedRows = SqlSchema.findAll({
      Request: Schema.Struct({ id: Schema.String, name: Schema.String, updated_at: Schema.String }),
      Result: ThreadRow,
      execute: (patch) => sql`
        UPDATE threads SET name = ${patch.name}, updated_at = ${patch.updated_at}
        WHERE id = ${patch.id} AND name IS NULL
        RETURNING *
      `,
    })

    const setProviderSessionRows = SqlSchema.findAll({
      Request: Schema.Struct({
        id: Schema.String,
        provider_session_id: Schema.String,
        provider_metadata: Schema.NullOr(Schema.String),
        updated_at: Schema.String,
      }),
      Result: ThreadRow,
      execute: (patch) => sql`
        UPDATE threads SET
          provider_session_id = ${patch.provider_session_id},
          provider_metadata = ${patch.provider_metadata},
          confirmed_at = NULL,
          updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    const setProviderMetadataRows = SqlSchema.findAll({
      Request: Schema.Struct({
        id: Schema.String,
        provider_metadata: Schema.String,
        updated_at: Schema.String,
      }),
      Result: ThreadRow,
      execute: (patch) => sql`
        UPDATE threads SET
          provider_metadata = ${patch.provider_metadata},
          updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    // Idempotent confirmation: the first observation wins, and a row that
    // vanished (deleted mid-feed) is a no-op.
    const confirmRows = SqlSchema.void({
      Request: Schema.Struct({ id: Schema.String, confirmed_at: Schema.String }),
      execute: (patch) => sql`
        UPDATE threads SET
          confirmed_at = COALESCE(confirmed_at, ${patch.confirmed_at}),
          updated_at = CASE WHEN confirmed_at IS NULL THEN ${patch.confirmed_at} ELSE updated_at END
        WHERE id = ${patch.id}
      `,
    })

    const setArchivedRows = SqlSchema.findAll({
      Request: Schema.Struct({
        id: Schema.String,
        archived_at: Schema.NullOr(Schema.String),
        updated_at: Schema.String,
      }),
      Result: ThreadRow,
      execute: (patch) => sql`
        UPDATE threads SET
          archived_at = ${patch.archived_at},
          pinned_at = CASE WHEN ${patch.archived_at} IS NOT NULL THEN NULL ELSE pinned_at END,
          updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    const setPinnedRows = SqlSchema.findAll({
      Request: Schema.Struct({
        id: Schema.String,
        pinned_at: Schema.NullOr(Schema.String),
        updated_at: Schema.String,
      }),
      Result: ThreadRow,
      execute: (patch) => sql`
        UPDATE threads SET
          pinned_at = CASE WHEN archived_at IS NOT NULL THEN NULL ELSE ${patch.pinned_at} END,
          updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    const deleteRows = SqlSchema.void({
      Request: Schema.String,
      execute: (id) => sql`DELETE FROM threads WHERE id = ${id}`,
    })

    const { firstRecord, requireFirst } = rowHelpers("thread", toRecord)

    const get = (id: string) => getRows(id).pipe(Effect.map(firstRecord), Effect.orDie)

    // Timestamps (and the generated id) are captured inside Effect.suspend so
    // an effect built early and run later stamps the run time, not the build
    // time.
    return {
      create: (input) =>
        Effect.suspend(() => {
          const now = new Date().toISOString()
          const row: typeof ThreadRow.Type = {
            id: Bun.randomUUIDv7(),
            project_id: input.projectId,
            agent_id: input.agentId,
            name: input.name ?? null,
            working_directory: input.workingDirectory,
            provider_session_id: null,
            confirmed_at: null,
            provider_metadata: null,
            pinned_at: null,
            archived_at: null,
            created_at: now,
            updated_at: now,
          }
          return insertRow(row).pipe(Effect.as(toRecord(row)))
        }).pipe(Effect.orDie),
      list: (projectId) =>
        listRows(projectId ?? null).pipe(
          Effect.map((rows) => rows.map(toRecord)),
          Effect.orDie,
        ),
      get,
      require: (id) => get(id).pipe(requireFound(() => new ThreadNotFound({ threadId: id }))),
      rename: (id, name) =>
        Effect.suspend(() => renameRows({ id, name, updated_at: new Date().toISOString() })).pipe(
          Effect.orDie,
          Effect.flatMap(requireFirst("rename")),
        ),
      renameIfUnnamed: (id, name) =>
        Effect.suspend(() =>
          renameIfUnnamedRows({ id, name, updated_at: new Date().toISOString() }),
        ).pipe(Effect.orDie, Effect.map(firstRecord)),
      setProviderSession: (id, providerSessionId, providerMetadata) =>
        Effect.suspend(() =>
          setProviderSessionRows({
            id,
            provider_session_id: providerSessionId,
            provider_metadata: providerMetadata,
            updated_at: new Date().toISOString(),
          }),
        ).pipe(Effect.orDie, Effect.flatMap(requireFirst("setProviderSession"))),
      setProviderMetadata: (id, providerMetadata) =>
        Effect.suspend(() =>
          setProviderMetadataRows({
            id,
            provider_metadata: providerMetadata,
            updated_at: new Date().toISOString(),
          }),
        ).pipe(Effect.orDie, Effect.flatMap(requireFirst("setProviderMetadata"))),
      confirm: (id) =>
        Effect.suspend(() => confirmRows({ id, confirmed_at: new Date().toISOString() })).pipe(
          Effect.orDie,
        ),
      setPinned: (id, pinned) =>
        Effect.suspend(() => {
          const now = new Date().toISOString()
          return setPinnedRows({ id, pinned_at: pinned ? now : null, updated_at: now })
        }).pipe(Effect.orDie, Effect.flatMap(requireFirst("setPinned"))),
      setArchived: (id, archived) =>
        Effect.suspend(() => {
          const now = new Date().toISOString()
          return setArchivedRows({ id, archived_at: archived ? now : null, updated_at: now })
        }).pipe(Effect.orDie, Effect.flatMap(requireFirst("setArchived"))),
      delete: (id) => deleteRows(id).pipe(Effect.orDie),
    }
  }),
)
