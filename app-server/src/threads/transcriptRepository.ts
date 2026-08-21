import { Context, Effect, Layer, Option, Schema } from "effect"
import { SqlClient, SqlSchema } from "effect/unstable/sql"
import { ThreadItem, ThreadTurn } from "../api/contract.ts"
import type { HistoryTurn } from "../agents/agentAdapter.ts"

// The Thread runtime's repository (ATC-193): the only module that speaks SQL
// for the transcript copy (thread_items, thread_turns), the prompt queue
// (thread_queue), and the two runtime counters on threads
// (transcript_seq, snapshot_version). Invariants:
//
//   - `seq` is allocated HERE, atomically with the write it stamps: every
//     item or turn upsert bumps threads.transcript_seq inside one
//     transaction and records the new value as the row's updated_seq. A
//     row's `ord` is the seq it was first inserted at, so transcript order
//     is `ORDER BY ord` and never changes on update.
//   - Items are stored as the ThreadItem JSON verbatim, encoded and decoded
//     through the contract schema — an unreadable row is a defect (a
//     schema this build cannot decode), never a silent skip.
//   - Reads that pair rows with the head seq (`read`, `changesAfter`) run
//     in one transaction too: a write landing between the row query and
//     the counter read would otherwise be missing from the rows yet covered
//     by the head — a permanent gap for a subscriber filtering on it.
//   - `replace` is the re-read: everything goes, the history lands with
//     fresh ords, snapshot_version bumps, and snapshot_seq records the seq
//     the replacement took — one transaction, so a concurrent reader sees
//     the old copy or the new one, never a mix.
//   - Queue rows start oldest-first: `beginTurn` stamps started_turn_id and
//     inserts the running turn atomically, and a started prompt is no
//     longer waiting (deleteWaiting refuses it).
//   - Item timestamps (ATC-214) are stamped HERE, not by adapters, with one
//     precedence: the adapter's provider time when it passes one, else the
//     stamp the row already carries, else the write time — so an ATC
//     approximation upgrades to the provider's truth but never re-stamps.
//     A tool item reaching a terminal status takes completedAt the same
//     way, and `replace` carries both forward by item id — a re-read must
//     not re-date what ATC already dated. The stamped values live in the
//     item JSON (and mirrored in columns for the carry-forward lookup), so
//     every read serves them. A prompt's attachments (ATC-216) carry
//     forward too, keyed by TURN id and the prompt's ordinal within the
//     turn: providers re-number items on a history read (Codex
//     positionally, Claude per message) and none echoes ATC's attachment
//     ids, but Codex turn ids are stable — Claude's are not, so its re-read
//     loses them (documented on the contract).

export type ThreadTurnRecord = typeof ThreadTurn.Type
export type ThreadItemRecord = typeof ThreadItem.Type
/** A waiting prompt as stored: the attachment ids, not the attachments —
 * the runtime resolves them (attachments.ts) when it lists or starts one. */
export interface QueuedPromptRecord {
  readonly id: string
  readonly prompt: string
  readonly attachmentIds: ReadonlyArray<string>
  readonly queuedAt: string
}
export type TurnSource = "native" | "observed" | "history"

/** One replayable change: the row's current state at the seq that last touched it. */
export interface TranscriptChange {
  readonly seq: number
  readonly change:
    | { readonly kind: "item"; readonly item: ThreadItemRecord }
    | { readonly kind: "turn"; readonly turn: ThreadTurnRecord }
}

const encodeItem = Schema.encodeSync(Schema.fromJsonString(ThreadItem))
const decodeItem = Schema.decodeUnknownSync(Schema.fromJsonString(ThreadItem))

const TurnRow = Schema.Struct({
  thread_id: Schema.String,
  turn_id: Schema.String,
  ord: Schema.Number,
  updated_seq: Schema.Number,
  status: Schema.Literals(["running", "completed", "interrupted", "failed"]),
  error: Schema.NullOr(Schema.String),
  prompt_id: Schema.NullOr(Schema.String),
  started_at: Schema.NullOr(Schema.String),
  ended_at: Schema.NullOr(Schema.String),
  source: Schema.Literals(["native", "observed", "history"]),
})

const ItemRow = Schema.Struct({
  thread_id: Schema.String,
  item_id: Schema.String,
  turn_id: Schema.String,
  ord: Schema.Number,
  updated_seq: Schema.Number,
  item: Schema.String,
  created_at: Schema.NullOr(Schema.String),
  completed_at: Schema.NullOr(Schema.String),
})

const ItemTimestamps = Schema.Struct({
  created_at: Schema.NullOr(Schema.String),
  completed_at: Schema.NullOr(Schema.String),
})

const QueueRow = Schema.Struct({
  id: Schema.String,
  thread_id: Schema.String,
  prompt: Schema.String,
  /** JSON array of attachment ids ('[]' for none). */
  attachments: Schema.String,
  queued_at: Schema.String,
  started_turn_id: Schema.NullOr(Schema.String),
})
const AttachmentIds = Schema.fromJsonString(Schema.Array(Schema.String))
const decodeAttachmentIds = Schema.decodeSync(AttachmentIds)
const encodeAttachmentIds = Schema.encodeSync(AttachmentIds)

const CountersRow = Schema.Struct({
  transcript_seq: Schema.Number,
  snapshot_version: Schema.Number,
  snapshot_seq: Schema.Number,
})

const toTurn = (row: typeof TurnRow.Type): ThreadTurnRecord => ({
  id: row.turn_id,
  status: row.status,
  ...(row.error !== null ? { error: row.error } : {}),
  ...(row.prompt_id !== null ? { promptId: row.prompt_id } : {}),
  ...(row.started_at !== null ? { startedAt: row.started_at } : {}),
  ...(row.ended_at !== null ? { endedAt: row.ended_at } : {}),
})

const toQueued = (row: typeof QueueRow.Type): QueuedPromptRecord => ({
  id: row.id,
  prompt: row.prompt,
  attachmentIds: decodeAttachmentIds(row.attachments),
  queuedAt: row.queued_at,
})

type UserMessageRecord = Extract<ThreadItemRecord, { type: "userMessage" }>
type AttachmentList = NonNullable<UserMessageRecord["attachments"]>

/** The images ATC attached, per turn in prompt order, as the stored copy
 * holds them — what a re-read must not lose (ATC-216, the header). */
const attachmentsByTurn = (
  items: ReadonlyArray<ThreadItemRecord>,
): Map<string, Array<AttachmentList>> => {
  const byTurn = new Map<string, Array<AttachmentList>>()
  for (const item of items) {
    if (item.type !== "userMessage" || item.attachments === undefined) continue
    byTurn.set(item.turnId, [...(byTurn.get(item.turnId) ?? []), item.attachments])
  }
  return byTurn
}

/** The stamped finish of a tool item; undefined for non-tool items. */
const itemCompletedAt = (item: ThreadItemRecord): string | undefined => {
  switch (item.type) {
    case "command":
    case "fileChange":
    case "mcpCall":
    case "toolCall":
      return item.completedAt
    case "userMessage":
    case "assistantText":
    case "reasoning":
    case "compaction":
      return undefined
  }
}

/** The header's timestamp rule: first write dates the item, a terminal tool
 * status dates the finish, and existing stamps always win over `now`. */
const stampItem = (
  item: ThreadItemRecord,
  existing: typeof ItemTimestamps.Type | undefined,
  now: string,
): ThreadItemRecord => {
  const createdAt = item.createdAt ?? existing?.created_at ?? now
  switch (item.type) {
    case "command":
    case "fileChange":
    case "mcpCall":
    case "toolCall": {
      const completedAt =
        item.completedAt ??
        existing?.completed_at ??
        (item.status === "completed" || item.status === "error" ? now : undefined)
      return completedAt === undefined
        ? { ...item, createdAt }
        : { ...item, createdAt, completedAt }
    }
    case "userMessage":
    case "assistantText":
    case "reasoning":
    case "compaction":
      return { ...item, createdAt }
  }
}

export interface TranscriptPage {
  readonly items: ReadonlyArray<ThreadItemRecord>
  readonly turns: ReadonlyArray<ThreadTurnRecord>
  readonly seq: number
  readonly snapshotVersion: number
  readonly hasMore: boolean
}

/** The thread's runtime counters (see the header). */
export interface TranscriptCounters {
  readonly seq: number
  readonly snapshotVersion: number
  /** The seq the latest `replace` took (0 = the copy was never replaced). */
  readonly snapshotSeq: number
}

export class TranscriptRepository extends Context.Service<
  TranscriptRepository,
  {
    /** Insert or update one item (upsert by id); returns the seq it took and
     * the item as stored — with createdAt (and completedAt on a finished
     * tool item) stamped, so callers publish exactly what a read serves. */
    readonly upsertItem: (
      threadId: string,
      item: ThreadItemRecord,
    ) => Effect.Effect<{ readonly seq: number; readonly item: ThreadItemRecord }>
    /** Insert or update one turn (upsert by id); returns the seq it took and
     * the row as it now stands (an update keeps promptId/startedAt). */
    readonly upsertTurn: (
      threadId: string,
      turn: ThreadTurnRecord,
      source: TurnSource,
    ) => Effect.Effect<{ readonly seq: number; readonly turn: ThreadTurnRecord }>
    /** Start a queued prompt as a turn: stamp the queue row and insert the
     * running turn atomically; returns the seq the turn took. */
    readonly beginTurn: (
      threadId: string,
      promptId: string,
      turn: ThreadTurnRecord,
    ) => Effect.Effect<number>
    /** Every turn of the thread with status running, oldest first. */
    readonly runningTurns: (threadId: string) => Effect.Effect<ReadonlyArray<ThreadTurnRecord>>
    /** Thread ids holding a running turn or a waiting prompt (the startup pass). */
    readonly threadsNeedingAttention: () => Effect.Effect<ReadonlyArray<string>>
    /**
     * A page of the transcript: the newest `limit` items (or the `limit`
     * items before `before`), oldest first, with every turn they reference
     * (and any running turn), the head seq, and the snapshot version. An
     * unknown `before` — a cursor from a replaced copy — reads as an empty
     * page: the client sees the new snapshotVersion and starts over.
     */
    readonly read: (
      threadId: string,
      options: { readonly before?: string | undefined; readonly limit: number },
    ) => Effect.Effect<TranscriptPage>
    /** Every row changed after `after`, in seq order, plus the counters. */
    readonly changesAfter: (
      threadId: string,
      after: number,
    ) => Effect.Effect<{
      readonly changes: ReadonlyArray<TranscriptChange>
      readonly counters: TranscriptCounters
    }>
    readonly counters: (threadId: string) => Effect.Effect<TranscriptCounters>
    /** Replace the whole copy with provider history (see the header). */
    readonly replace: (
      threadId: string,
      history: ReadonlyArray<HistoryTurn>,
    ) => Effect.Effect<TranscriptCounters>
    /** Admit a prompt to the queue, with the ids of the attachments it carries. */
    readonly enqueue: (
      threadId: string,
      prompt: string,
      attachmentIds: ReadonlyArray<string>,
    ) => Effect.Effect<QueuedPromptRecord>
    /** Prompts still waiting, oldest first. */
    readonly listWaiting: (threadId: string) => Effect.Effect<ReadonlyArray<QueuedPromptRecord>>
    /** The oldest waiting prompt (None when nothing waits). */
    readonly peek: (threadId: string) => Effect.Effect<Option.Option<QueuedPromptRecord>>
    /** Remove a WAITING prompt; false when unknown or already started. */
    readonly deleteWaiting: (threadId: string, promptId: string) => Effect.Effect<boolean>
  }
>()("app-server/TranscriptRepository") {}

// Database failures are defects, not domain errors (the threads repository rule).
export const layer = Layer.effect(TranscriptRepository)(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient

    const nextSeqRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: Schema.Struct({ transcript_seq: Schema.Number }),
      execute: (threadId) => sql`
        UPDATE threads SET transcript_seq = transcript_seq + 1
        WHERE id = ${threadId}
        RETURNING transcript_seq
      `,
    })

    /** Allocate the thread's next seq; a vanished thread is a defect (callers hold the record). */
    const nextSeq = (threadId: string) =>
      nextSeqRows(threadId).pipe(
        Effect.flatMap((rows) =>
          rows[0] === undefined
            ? Effect.die(new Error(`nextSeq: thread ${threadId} vanished`))
            : Effect.succeed(rows[0].transcript_seq),
        ),
      )

    const upsertItemRow = SqlSchema.void({
      Request: ItemRow,
      execute: (row) => sql`
        INSERT INTO thread_items ${sql.insert(row)}
        ON CONFLICT (thread_id, item_id) DO UPDATE SET
          turn_id = excluded.turn_id,
          updated_seq = excluded.updated_seq,
          item = excluded.item,
          created_at = excluded.created_at,
          completed_at = excluded.completed_at
      `,
    })

    const itemTimestampRows = SqlSchema.findAll({
      Request: Schema.Struct({ thread_id: Schema.String, item_id: Schema.String }),
      Result: ItemTimestamps,
      execute: (request) => sql`
        SELECT created_at, completed_at FROM thread_items
        WHERE thread_id = ${request.thread_id} AND item_id = ${request.item_id}
      `,
    })

    const allItemTimestampRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: Schema.Struct({
        item_id: Schema.String,
        created_at: Schema.NullOr(Schema.String),
        completed_at: Schema.NullOr(Schema.String),
      }),
      execute: (threadId) => sql`
        SELECT item_id, created_at, completed_at FROM thread_items WHERE thread_id = ${threadId}
      `,
    })

    // The userMessage items carrying attachments, in transcript order, for
    // replace's carry-forward.
    const attachedItemRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: Schema.Struct({ item: Schema.String }),
      execute: (threadId) => sql`
        SELECT item FROM thread_items
        WHERE thread_id = ${threadId} AND json_extract(item, '$.attachments') IS NOT NULL
        ORDER BY ord ASC
      `,
    })

    const upsertTurnRows = SqlSchema.findAll({
      Request: TurnRow,
      Result: TurnRow,
      execute: (row) => sql`
        INSERT INTO thread_turns ${sql.insert(row)}
        ON CONFLICT (thread_id, turn_id) DO UPDATE SET
          updated_seq = excluded.updated_seq,
          status = excluded.status,
          error = excluded.error,
          prompt_id = COALESCE(excluded.prompt_id, thread_turns.prompt_id),
          started_at = COALESCE(excluded.started_at, thread_turns.started_at),
          ended_at = excluded.ended_at,
          source = excluded.source
        RETURNING *
      `,
    })

    const turnRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: TurnRow,
      execute: (threadId) =>
        sql`SELECT * FROM thread_turns WHERE thread_id = ${threadId} ORDER BY ord ASC`,
    })

    const runningTurnRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: TurnRow,
      execute: (threadId) => sql`
        SELECT * FROM thread_turns
        WHERE thread_id = ${threadId} AND status = 'running'
        ORDER BY ord ASC
      `,
    })

    const attentionRows = SqlSchema.findAll({
      Request: Schema.Void,
      Result: Schema.Struct({ thread_id: Schema.String }),
      execute: () => sql`
        SELECT DISTINCT thread_id FROM (
          SELECT thread_id FROM thread_turns WHERE status = 'running'
          UNION
          SELECT thread_id FROM thread_queue WHERE started_turn_id IS NULL
        )
      `,
    })

    // The newest `limit` items, or the `limit` before an ord — fetched
    // newest-first (LIMIT + 1 to learn hasMore) and reversed in code.
    const itemPageRows = SqlSchema.findAll({
      Request: Schema.Struct({
        thread_id: Schema.String,
        before_ord: Schema.NullOr(Schema.Number),
        limit: Schema.Number,
      }),
      Result: ItemRow,
      execute: (request) =>
        request.before_ord === null
          ? sql`
              SELECT * FROM thread_items WHERE thread_id = ${request.thread_id}
              ORDER BY ord DESC LIMIT ${request.limit}
            `
          : sql`
              SELECT * FROM thread_items
              WHERE thread_id = ${request.thread_id} AND ord < ${request.before_ord}
              ORDER BY ord DESC LIMIT ${request.limit}
            `,
    })

    const itemOrdRows = SqlSchema.findAll({
      Request: Schema.Struct({ thread_id: Schema.String, item_id: Schema.String }),
      Result: Schema.Struct({ ord: Schema.Number }),
      execute: (request) => sql`
        SELECT ord FROM thread_items
        WHERE thread_id = ${request.thread_id} AND item_id = ${request.item_id}
      `,
    })

    const changedItemRows = SqlSchema.findAll({
      Request: Schema.Struct({ thread_id: Schema.String, after: Schema.Number }),
      Result: ItemRow,
      execute: (request) => sql`
        SELECT * FROM thread_items
        WHERE thread_id = ${request.thread_id} AND updated_seq > ${request.after}
        ORDER BY ord ASC
      `,
    })

    const changedTurnRows = SqlSchema.findAll({
      Request: Schema.Struct({ thread_id: Schema.String, after: Schema.Number }),
      Result: TurnRow,
      execute: (request) => sql`
        SELECT * FROM thread_turns
        WHERE thread_id = ${request.thread_id} AND updated_seq > ${request.after}
        ORDER BY ord ASC
      `,
    })

    const countersRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: CountersRow,
      execute: (threadId) => sql`
        SELECT transcript_seq, snapshot_version, snapshot_seq FROM threads WHERE id = ${threadId}
      `,
    })

    // The replacement itself takes a seq (the invalidation's position) and
    // records it as snapshot_seq.
    const bumpSnapshotRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: CountersRow,
      execute: (threadId) => sql`
        UPDATE threads SET
          transcript_seq = transcript_seq + 1,
          snapshot_version = snapshot_version + 1,
          snapshot_seq = transcript_seq + 1
        WHERE id = ${threadId}
        RETURNING transcript_seq, snapshot_version, snapshot_seq
      `,
    })

    const clearItems = SqlSchema.void({
      Request: Schema.String,
      execute: (threadId) => sql`DELETE FROM thread_items WHERE thread_id = ${threadId}`,
    })
    const clearTurns = SqlSchema.void({
      Request: Schema.String,
      execute: (threadId) => sql`DELETE FROM thread_turns WHERE thread_id = ${threadId}`,
    })

    const insertQueueRow = SqlSchema.void({
      Request: QueueRow,
      execute: (row) => sql`INSERT INTO thread_queue ${sql.insert(row)}`,
    })

    const waitingRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: QueueRow,
      execute: (threadId) => sql`
        SELECT * FROM thread_queue
        WHERE thread_id = ${threadId} AND started_turn_id IS NULL
        ORDER BY queued_at ASC, id ASC
      `,
    })

    const markStartedRows = SqlSchema.void({
      Request: Schema.Struct({ id: Schema.String, turn_id: Schema.String }),
      execute: (request) =>
        sql`UPDATE thread_queue SET started_turn_id = ${request.turn_id} WHERE id = ${request.id}`,
    })

    const deleteWaitingRows = SqlSchema.findAll({
      Request: Schema.Struct({ thread_id: Schema.String, id: Schema.String }),
      Result: Schema.Struct({ id: Schema.String }),
      execute: (request) => sql`
        DELETE FROM thread_queue
        WHERE thread_id = ${request.thread_id} AND id = ${request.id} AND started_turn_id IS NULL
        RETURNING id
      `,
    })

    const turnRow = (
      threadId: string,
      turn: ThreadTurnRecord,
      seq: number,
      source: TurnSource,
    ): typeof TurnRow.Type => ({
      thread_id: threadId,
      turn_id: turn.id,
      ord: seq,
      updated_seq: seq,
      status: turn.status,
      error: turn.error ?? null,
      prompt_id: turn.promptId ?? null,
      started_at: turn.startedAt ?? null,
      ended_at: turn.endedAt ?? null,
      source,
    })

    const itemRow = (
      threadId: string,
      item: ThreadItemRecord,
      seq: number,
    ): typeof ItemRow.Type => ({
      thread_id: threadId,
      item_id: item.id,
      turn_id: item.turnId,
      ord: seq,
      updated_seq: seq,
      item: encodeItem(item),
      created_at: item.createdAt ?? null,
      completed_at: itemCompletedAt(item) ?? null,
    })

    const upsertItem = (threadId: string, item: ThreadItemRecord) =>
      sql.withTransaction(
        Effect.gen(function* () {
          const existing = (yield* itemTimestampRows({ thread_id: threadId, item_id: item.id }))[0]
          const stamped = stampItem(item, existing, new Date().toISOString())
          const seq = yield* nextSeq(threadId)
          yield* upsertItemRow(itemRow(threadId, stamped, seq))
          return { seq, item: stamped }
        }),
      )

    const upsertTurn = (threadId: string, turn: ThreadTurnRecord, source: TurnSource) =>
      sql.withTransaction(
        Effect.gen(function* () {
          const seq = yield* nextSeq(threadId)
          const rows = yield* upsertTurnRows(turnRow(threadId, turn, seq, source))
          if (rows[0] === undefined) {
            return yield* Effect.die(new Error(`upsertTurn: no row returned for ${turn.id}`))
          }
          return { seq, turn: toTurn(rows[0]) }
        }),
      )

    const beginTurn = (threadId: string, promptId: string, turn: ThreadTurnRecord) =>
      sql.withTransaction(
        Effect.gen(function* () {
          yield* markStartedRows({ id: promptId, turn_id: turn.id })
          const seq = yield* nextSeq(threadId)
          yield* upsertTurnRows(turnRow(threadId, turn, seq, "native"))
          return seq
        }),
      )

    const toCounters = (row: typeof CountersRow.Type | undefined): TranscriptCounters => ({
      seq: row?.transcript_seq ?? 0,
      snapshotVersion: row?.snapshot_version ?? 0,
      snapshotSeq: row?.snapshot_seq ?? 0,
    })

    const counters = (threadId: string) =>
      countersRows(threadId).pipe(Effect.map((rows) => toCounters(rows[0])))

    return {
      upsertItem: (threadId, item) => upsertItem(threadId, item).pipe(Effect.orDie),
      upsertTurn: (threadId, turn, source) => upsertTurn(threadId, turn, source).pipe(Effect.orDie),
      beginTurn: (threadId, promptId, turn) =>
        beginTurn(threadId, promptId, turn).pipe(Effect.orDie),
      runningTurns: (threadId) =>
        runningTurnRows(threadId).pipe(
          Effect.map((rows) => rows.map(toTurn)),
          Effect.orDie,
        ),
      threadsNeedingAttention: () =>
        attentionRows().pipe(
          Effect.map((rows) => rows.map((row) => row.thread_id)),
          Effect.orDie,
        ),
      read: (threadId, options) =>
        sql
          .withTransaction(
            Effect.gen(function* () {
              const cursor =
                options.before === undefined
                  ? undefined
                  : (yield* itemOrdRows({ thread_id: threadId, item_id: options.before }))[0]
              const head = yield* counters(threadId)
              if (options.before !== undefined && cursor === undefined) {
                return {
                  items: [],
                  turns: [],
                  hasMore: false,
                  seq: head.seq,
                  snapshotVersion: head.snapshotVersion,
                }
              }
              const rows = yield* itemPageRows({
                thread_id: threadId,
                before_ord: cursor?.ord ?? null,
                limit: options.limit + 1,
              })
              const hasMore = rows.length > options.limit
              const page = rows.slice(0, options.limit).toReversed()
              const items = page.map((row) => decodeItem(row.item))
              const turnIds = new Set(page.map((row) => row.turn_id))
              // Every turn the page references, plus a running turn that has
              // no item yet — it is current state a client must show.
              const turns = (yield* turnRows(threadId)).filter(
                (row) => turnIds.has(row.turn_id) || row.status === "running",
              )
              return {
                items,
                turns: turns.map(toTurn),
                hasMore,
                seq: head.seq,
                snapshotVersion: head.snapshotVersion,
              }
            }),
          )
          .pipe(Effect.orDie),
      changesAfter: (threadId, after) =>
        sql
          .withTransaction(
            Effect.gen(function* () {
              const items = yield* changedItemRows({ thread_id: threadId, after })
              const turns = yield* changedTurnRows({ thread_id: threadId, after })
              // Seq order, not transcript order: a client resumes from the
              // highest seq it saw, so nothing may follow a higher seq.
              const changes: Array<TranscriptChange> = [
                ...turns.map((row) => ({
                  seq: row.updated_seq,
                  change: { kind: "turn" as const, turn: toTurn(row) },
                })),
                ...items.map((row) => ({
                  seq: row.updated_seq,
                  change: { kind: "item" as const, item: decodeItem(row.item) },
                })),
              ].toSorted((left, right) => left.seq - right.seq)
              return { changes, counters: yield* counters(threadId) }
            }),
          )
          .pipe(Effect.orDie),
      counters: (threadId) => counters(threadId).pipe(Effect.orDie),
      replace: (threadId, history) =>
        sql
          .withTransaction(
            Effect.gen(function* () {
              // Timestamps carry forward by item id: a re-read must not
              // re-stamp items ATC already dated.
              const carried = new Map(
                (yield* allItemTimestampRows(threadId)).map((row) => [
                  row.item_id,
                  { created_at: row.created_at, completed_at: row.completed_at },
                ]),
              )
              const attached = attachmentsByTurn(
                (yield* attachedItemRows(threadId)).map((row) => decodeItem(row.item)),
              )
              const now = new Date().toISOString()
              yield* clearItems(threadId)
              yield* clearTurns(threadId)
              for (const entry of history) {
                const turnSeq = yield* nextSeq(threadId)
                yield* upsertTurnRows(turnRow(threadId, entry.turn, turnSeq, "history"))
                // The turn's prompts, oldest first, take back the stored
                // images in the same order (history names no ATC ids).
                const turnAttachments = attached.get(entry.turn.id) ?? []
                let promptIndex = 0
                for (const item of entry.items) {
                  const itemSeq = yield* nextSeq(threadId)
                  const restore =
                    item.type === "userMessage" && item.attachments === undefined
                      ? turnAttachments[promptIndex++]
                      : undefined
                  const restored: ThreadItemRecord =
                    item.type === "userMessage" && restore !== undefined
                      ? { ...item, attachments: restore }
                      : item
                  const stamped = stampItem(restored, carried.get(item.id), now)
                  yield* upsertItemRow(itemRow(threadId, stamped, itemSeq))
                }
              }
              const rows = yield* bumpSnapshotRows(threadId)
              if (rows[0] === undefined) {
                return yield* Effect.die(new Error(`replace: thread ${threadId} vanished`))
              }
              return toCounters(rows[0])
            }),
          )
          .pipe(Effect.orDie),
      enqueue: (threadId, prompt, attachmentIds) =>
        Effect.suspend(() => {
          const row: typeof QueueRow.Type = {
            id: Bun.randomUUIDv7(),
            thread_id: threadId,
            prompt,
            attachments: encodeAttachmentIds(attachmentIds),
            queued_at: new Date().toISOString(),
            started_turn_id: null,
          }
          return insertQueueRow(row).pipe(Effect.as(toQueued(row)))
        }).pipe(Effect.orDie),
      listWaiting: (threadId) =>
        waitingRows(threadId).pipe(
          Effect.map((rows) => rows.map(toQueued)),
          Effect.orDie,
        ),
      peek: (threadId) =>
        waitingRows(threadId).pipe(
          Effect.map((rows) => Option.fromNullishOr(rows[0]).pipe(Option.map(toQueued))),
          Effect.orDie,
        ),
      deleteWaiting: (threadId, promptId) =>
        deleteWaitingRows({ thread_id: threadId, id: promptId }).pipe(
          Effect.map((rows) => rows.length > 0),
          Effect.orDie,
        ),
    }
  }),
)
