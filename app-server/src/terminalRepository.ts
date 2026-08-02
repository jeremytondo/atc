import { Context, Effect, Layer, Option, Schema } from "effect"
import { SqlClient, SqlSchema } from "effect/unstable/sql"

// The Terminals repository (ATC-130): the only module that speaks SQL for
// terminals. Row types stay here — the service and handlers see camelCase
// records. `starting` is the internal crash-mid-create marker; the public
// contract only ever sees `live` and `ended`.

export type TerminalStatus = "starting" | "live" | "ended"

export interface TerminalRecord {
  readonly id: string
  readonly projectId: string
  readonly name?: string
  readonly command?: ReadonlyArray<string>
  readonly initialWorkingDirectory: string
  readonly status: TerminalStatus
  readonly createdAt: string
  readonly updatedAt: string
  readonly endedAt?: string
}

/** Internal row shape (snake_case columns, as stored). */
const TerminalRow = Schema.Struct({
  id: Schema.String,
  project_id: Schema.String,
  name: Schema.NullOr(Schema.String),
  command: Schema.NullOr(Schema.String),
  initial_working_directory: Schema.String,
  status: Schema.Literals(["starting", "live", "ended"]),
  created_at: Schema.String,
  updated_at: Schema.String,
  ended_at: Schema.NullOr(Schema.String),
})

const toRecord = (row: typeof TerminalRow.Type): TerminalRecord => ({
  id: row.id,
  projectId: row.project_id,
  ...(row.name !== null ? { name: row.name } : {}),
  ...(row.command !== null ? { command: JSON.parse(row.command) as Array<string> } : {}),
  initialWorkingDirectory: row.initial_working_directory,
  status: row.status,
  createdAt: row.created_at,
  updatedAt: row.updated_at,
  ...(row.ended_at !== null ? { endedAt: row.ended_at } : {}),
})

export class TerminalRepository extends Context.Service<
  TerminalRepository,
  {
    /** Insert a new `starting` record and return it (id is generated). */
    readonly create: (input: {
      readonly projectId: string
      readonly name?: string | undefined
      readonly command?: ReadonlyArray<string> | undefined
      readonly initialWorkingDirectory: string
    }) => Effect.Effect<TerminalRecord>
    /** All records (every status), newest first; optionally one project's. */
    readonly list: (projectId?: string) => Effect.Effect<ReadonlyArray<TerminalRecord>>
    readonly get: (id: string) => Effect.Effect<Option.Option<TerminalRecord>>
    /**
     * `starting` → `live`. Callers hold the record; a vanished row is a
     * defect, like every other database failure.
     */
    readonly markLive: (id: string) => Effect.Effect<TerminalRecord>
    /** Tombstone: `ended` with `endedAt`; idempotent for already-ended. */
    readonly markEnded: (id: string) => Effect.Effect<void>
    /** Update the display label; callers hold the record (see markLive). */
    readonly rename: (id: string, name: string) => Effect.Effect<TerminalRecord>
    /** `false` if no such terminal existed. */
    readonly delete: (id: string) => Effect.Effect<boolean>
    readonly countForProject: (projectId: string) => Effect.Effect<number>
  }
>()("app-server/TerminalRepository") {}

// Database failures are defects, not domain errors: the API surfaces them as
// 500s and the one-line CLI contract reports them; nothing can handle them.
export const layer = Layer.effect(TerminalRepository)(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient

    const insertRow = SqlSchema.void({
      Request: TerminalRow,
      execute: (row) => sql`INSERT INTO terminals ${sql.insert(row)}`,
    })

    const listRows = SqlSchema.findAll({
      Request: Schema.NullOr(Schema.String),
      Result: TerminalRow,
      execute: (projectId) =>
        projectId === null
          ? sql`SELECT * FROM terminals ORDER BY id DESC`
          : sql`SELECT * FROM terminals WHERE project_id = ${projectId} ORDER BY id DESC`,
    })

    const getRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: TerminalRow,
      execute: (id) => sql`SELECT * FROM terminals WHERE id = ${id}`,
    })

    const markLiveRows = SqlSchema.findAll({
      Request: Schema.Struct({ id: Schema.String, updated_at: Schema.String }),
      Result: TerminalRow,
      execute: (patch) => sql`
        UPDATE terminals SET status = 'live', updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    // Idempotent tombstoning: the first observation of death wins, so a
    // re-reconcile never rewrites endedAt.
    const markEndedRows = SqlSchema.void({
      Request: Schema.Struct({ id: Schema.String, ended_at: Schema.String }),
      execute: (patch) => sql`
        UPDATE terminals SET
          status = 'ended',
          ended_at = COALESCE(ended_at, ${patch.ended_at}),
          updated_at = CASE WHEN status = 'ended' THEN updated_at ELSE ${patch.ended_at} END
        WHERE id = ${patch.id}
      `,
    })

    const renameRows = SqlSchema.findAll({
      Request: Schema.Struct({ id: Schema.String, name: Schema.String, updated_at: Schema.String }),
      Result: TerminalRow,
      execute: (patch) => sql`
        UPDATE terminals SET name = ${patch.name}, updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    const deleteRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: Schema.Struct({ id: Schema.String }),
      execute: (id) => sql`DELETE FROM terminals WHERE id = ${id} RETURNING id`,
    })

    const countRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: Schema.Struct({ n: Schema.Int }),
      execute: (projectId) =>
        sql`SELECT COUNT(*) AS n FROM terminals WHERE project_id = ${projectId}`,
    })

    const firstRecord = (rows: ReadonlyArray<typeof TerminalRow.Type>) =>
      Option.fromNullishOr(rows[0]).pipe(Option.map(toRecord))

    /** For updates whose caller holds the record: a vanished row is a bug. */
    const requireFirst = (what: string) => (rows: ReadonlyArray<typeof TerminalRow.Type>) =>
      rows[0] !== undefined
        ? Effect.succeed(toRecord(rows[0]))
        : Effect.die(new Error(`${what}: terminal row vanished mid-update`))

    return {
      create: (input) => {
        const now = new Date().toISOString()
        const row: typeof TerminalRow.Type = {
          id: Bun.randomUUIDv7(),
          project_id: input.projectId,
          name: input.name ?? null,
          command: input.command !== undefined ? JSON.stringify(input.command) : null,
          initial_working_directory: input.initialWorkingDirectory,
          status: "starting",
          created_at: now,
          updated_at: now,
          ended_at: null,
        }
        return insertRow(row).pipe(Effect.as(toRecord(row)), Effect.orDie)
      },
      list: (projectId) =>
        listRows(projectId ?? null).pipe(
          Effect.map((rows) => rows.map(toRecord)),
          Effect.orDie,
        ),
      get: (id) => getRows(id).pipe(Effect.map(firstRecord), Effect.orDie),
      markLive: (id) =>
        markLiveRows({ id, updated_at: new Date().toISOString() }).pipe(
          Effect.orDie,
          Effect.flatMap(requireFirst("markLive")),
        ),
      markEnded: (id) =>
        markEndedRows({ id, ended_at: new Date().toISOString() }).pipe(Effect.orDie),
      rename: (id, name) =>
        renameRows({ id, name, updated_at: new Date().toISOString() }).pipe(
          Effect.orDie,
          Effect.flatMap(requireFirst("rename")),
        ),
      delete: (id) =>
        deleteRows(id).pipe(
          Effect.map((rows) => rows.length > 0),
          Effect.orDie,
        ),
      countForProject: (projectId) =>
        countRows(projectId).pipe(
          Effect.map((rows) => rows[0]?.n ?? 0),
          Effect.orDie,
        ),
    }
  }),
)
