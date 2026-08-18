import { Context, Effect, Layer, Option, Schema } from "effect"
import { SqlClient, SqlSchema } from "effect/unstable/sql"
import { rowHelpers } from "../platform/repositoryHelpers.ts"
import { settingsFromColumns, settingsToColumns } from "../threads/threadSettings.ts"
import type { ThreadSettings } from "../threads/threadSettings.ts"

// The per-agent write-through defaults (ATC-205): the only module that
// speaks SQL for `agent_defaults`. One row per agent id, holding the settings
// the last changed thread of that agent ended up with — what a new thread
// inherits. There is no editor and no write endpoint; the Threads domain
// writes it whenever a thread's settings change, and reads it on create.
// Rows are decoded permissively (settingsFromColumns), so a row a newer
// build wrote never bricks reads.

const DefaultsRow = Schema.Struct({
  agent_id: Schema.String,
  model: Schema.String,
  reasoning: Schema.NullOr(Schema.String),
  mode: Schema.String,
  access: Schema.String,
  updated_at: Schema.String,
})

export class AgentDefaultsRepository extends Context.Service<
  AgentDefaultsRepository,
  {
    readonly get: (agentId: string) => Effect.Effect<Option.Option<ThreadSettings>>
    /** Upsert the agent's row. */
    readonly set: (agentId: string, settings: ThreadSettings) => Effect.Effect<void>
  }
>()("app-server/AgentDefaultsRepository") {}

// Database failures are defects, not domain errors (see threadRepository.ts).
export const layer = Layer.effect(AgentDefaultsRepository)(
  Effect.gen(function* () {
    const sql = yield* SqlClient.SqlClient

    const getRows = SqlSchema.findAll({
      Request: Schema.String,
      Result: DefaultsRow,
      execute: (agentId) => sql`SELECT * FROM agent_defaults WHERE agent_id = ${agentId}`,
    })

    const upsertRow = SqlSchema.void({
      Request: DefaultsRow,
      execute: (row) => sql`
        INSERT INTO agent_defaults ${sql.insert(row)}
        ON CONFLICT (agent_id) DO UPDATE SET
          model = excluded.model,
          reasoning = excluded.reasoning,
          mode = excluded.mode,
          access = excluded.access,
          updated_at = excluded.updated_at
      `,
    })

    const { firstRecord } = rowHelpers("agent defaults", settingsFromColumns)

    return {
      get: (agentId) => getRows(agentId).pipe(Effect.map(firstRecord), Effect.orDie),
      set: (agentId, settings) =>
        Effect.suspend(() =>
          upsertRow({
            agent_id: agentId,
            ...settingsToColumns(settings),
            updated_at: new Date().toISOString(),
          }),
        ).pipe(Effect.orDie),
    }
  }),
)
