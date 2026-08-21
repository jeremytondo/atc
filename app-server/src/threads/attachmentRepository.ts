import { Context, Effect, Layer, Schema } from "effect"
import { SqlClient, SqlSchema } from "effect/unstable/sql"

// The attachments repository (ATC-216): the only module that speaks SQL for
// thread_attachments. Rows are metadata only — the bytes live on disk under
// the attachments service's directory layout, and the row's id is what names
// them there. Database failures are defects, not domain errors.

export interface AttachmentRecord {
  readonly id: string
  readonly threadId: string
  readonly name: string
  readonly mediaType: string
  readonly byteSize: number
  readonly createdAt: string
}

const AttachmentRow = Schema.Struct({
  id: Schema.String,
  thread_id: Schema.String,
  name: Schema.String,
  media_type: Schema.String,
  byte_size: Schema.Number,
  created_at: Schema.String,
})

const toRecord = (row: typeof AttachmentRow.Type): AttachmentRecord => ({
  id: row.id,
  threadId: row.thread_id,
  name: row.name,
  mediaType: row.media_type,
  byteSize: row.byte_size,
  createdAt: row.created_at,
})

export class AttachmentRepository extends Context.Service<
  AttachmentRepository,
  {
    /** Insert one record with a generated id and return it. */
    readonly create: (input: {
      readonly threadId: string
      readonly name: string
      readonly mediaType: string
      readonly byteSize: number
    }) => Effect.Effect<AttachmentRecord>
    /**
     * The thread's records among `ids`, in the order of `ids`; an id the
     * thread does not own is simply absent (the caller decides what that
     * means). Attachments never cross threads.
     */
    readonly findMany: (
      threadId: string,
      ids: ReadonlyArray<string>,
    ) => Effect.Effect<ReadonlyArray<AttachmentRecord>>
  }
>()("app-server/AttachmentRepository") {}

export const layer = Layer.effect(AttachmentRepository)(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient

    const insertRow = SqlSchema.void({
      Request: AttachmentRow,
      execute: (row) => sql`INSERT INTO thread_attachments ${sql.insert(row)}`,
    })

    const rowsByIds = SqlSchema.findAll({
      Request: Schema.Struct({ thread_id: Schema.String, ids: Schema.Array(Schema.String) }),
      Result: AttachmentRow,
      execute: (request) => sql`
        SELECT * FROM thread_attachments
        WHERE thread_id = ${request.thread_id} AND id IN ${sql.in(request.ids)}
      `,
    })

    return {
      create: (input) =>
        Effect.suspend(() => {
          const row: typeof AttachmentRow.Type = {
            id: Bun.randomUUIDv7(),
            thread_id: input.threadId,
            name: input.name,
            media_type: input.mediaType,
            byte_size: input.byteSize,
            created_at: new Date().toISOString(),
          }
          return insertRow(row).pipe(Effect.as(toRecord(row)))
        }).pipe(Effect.orDie),
      findMany: (threadId, ids) =>
        ids.length === 0
          ? Effect.succeed([])
          : rowsByIds({ thread_id: threadId, ids: [...new Set(ids)] }).pipe(
              Effect.map((rows) => {
                const byId = new Map(rows.map((row) => [row.id, toRecord(row)]))
                return ids.flatMap((id) => {
                  const found = byId.get(id)
                  return found === undefined ? [] : [found]
                })
              }),
              Effect.orDie,
            ),
    } satisfies AttachmentRepository["Service"]
  }),
)
