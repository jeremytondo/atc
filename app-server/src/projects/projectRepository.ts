import { Context, Effect, Layer, Option, Schema } from "effect"
import { SqlClient, SqlSchema } from "effect/unstable/sql"
import { ProjectNotFound } from "../api/contract.ts"
import type { Project as ProjectSchema } from "../api/contract.ts"

// The Projects repository (ATC-121): the only module that speaks SQL for
// projects. Row types stay here — handlers and the contract see plain
// camelCase project objects shaped like the contract's Project schema.

export type Project = typeof ProjectSchema.Type

/** Internal row shape (snake_case columns, as stored). */
const ProjectRow = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
  default_working_directory: Schema.String,
  created_at: Schema.String,
  updated_at: Schema.String,
})

const toProject = (row: typeof ProjectRow.Type): Project => ({
  id: row.id,
  name: row.name,
  defaultWorkingDirectory: row.default_working_directory,
  createdAt: row.created_at,
  updatedAt: row.updated_at,
})

export class ProjectRepository extends Context.Service<
  ProjectRepository,
  {
    /** All projects, newest first (UUIDv7 ids are time-ordered). */
    readonly list: () => Effect.Effect<ReadonlyArray<Project>>
    readonly create: (input: {
      readonly name: string
      readonly defaultWorkingDirectory: string
    }) => Effect.Effect<Project>
    readonly get: (id: string) => Effect.Effect<Option.Option<Project>>
    /** `get` with the contract's not-found error already applied. */
    readonly require: (id: string) => Effect.Effect<Project, ProjectNotFound>
    /** Applies the provided fields and bumps `updatedAt`; `None` if unknown. */
    readonly update: (
      id: string,
      patch: {
        readonly name?: string | undefined
        readonly defaultWorkingDirectory?: string | undefined
      },
    ) => Effect.Effect<Option.Option<Project>>
    /** `false` if no such project existed. */
    readonly delete: (id: string) => Effect.Effect<boolean>
  }
>()("app-server/ProjectRepository") {}

// Database failures are defects, not domain errors: the API surfaces them as
// 500s and the one-line CLI contract reports them; nothing can handle them.
export const layer = Layer.effect(ProjectRepository)(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient

    const listRows = SqlSchema.findAll({
      Request: Schema.Void,
      Result: ProjectRow,
      execute: () => sql`SELECT * FROM projects ORDER BY id DESC`,
    })

    const getRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: ProjectRow,
      execute: (id) => sql`SELECT * FROM projects WHERE id = ${id}`,
    })

    const insertRow = SqlSchema.void({
      Request: ProjectRow,
      execute: (row) => sql`INSERT INTO projects ${sql.insert(row)}`,
    })

    const updateRows = SqlSchema.findAll({
      Request: Schema.Struct({
        id: Schema.String,
        name: Schema.NullOr(Schema.String),
        default_working_directory: Schema.NullOr(Schema.String),
        updated_at: Schema.String,
      }),
      Result: ProjectRow,
      execute: (patch) => sql`
        UPDATE projects SET
          name = COALESCE(${patch.name}, name),
          default_working_directory = COALESCE(${patch.default_working_directory}, default_working_directory),
          updated_at = ${patch.updated_at}
        WHERE id = ${patch.id}
        RETURNING *
      `,
    })

    const deleteRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: Schema.Struct({ id: Schema.String }),
      execute: (id) => sql`DELETE FROM projects WHERE id = ${id} RETURNING id`,
    })

    const firstProject = (rows: ReadonlyArray<typeof ProjectRow.Type>) =>
      Option.fromNullishOr(rows[0]).pipe(Option.map(toProject))

    const get = (id: string) => getRows(id).pipe(Effect.map(firstProject), Effect.orDie)

    return {
      list: () =>
        listRows().pipe(
          Effect.map((rows) => rows.map(toProject)),
          Effect.orDie,
        ),
      create: (input) => {
        const now = new Date().toISOString()
        const row = {
          id: Bun.randomUUIDv7(),
          name: input.name,
          default_working_directory: input.defaultWorkingDirectory,
          created_at: now,
          updated_at: now,
        }
        return insertRow(row).pipe(Effect.as(toProject(row)), Effect.orDie)
      },
      get,
      require: (id) =>
        get(id).pipe(
          Effect.flatMap(
            Option.match({
              onNone: () => Effect.fail(new ProjectNotFound({ projectId: id })),
              onSome: Effect.succeed,
            }),
          ),
        ),
      update: (id, patch) =>
        // An empty patch changes nothing — including updatedAt.
        patch.name === undefined && patch.defaultWorkingDirectory === undefined
          ? get(id)
          : updateRows({
              id,
              name: patch.name ?? null,
              default_working_directory: patch.defaultWorkingDirectory ?? null,
              updated_at: new Date().toISOString(),
            }).pipe(Effect.map(firstProject), Effect.orDie),
      delete: (id) =>
        deleteRows(id).pipe(
          Effect.map((rows) => rows.length > 0),
          Effect.orDie,
        ),
    }
  }),
)
