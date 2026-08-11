import { Context, Effect, Layer } from "effect"
import { AGENT_IDS, AgentNotFound } from "../api/contract.ts"
import type { Agent as AgentSchema, AgentId } from "../api/contract.ts"
import { AppConfig } from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"
import type { AgentAdapter } from "./agentAdapter.ts"
import {
  PROVIDER_FOR_AGENT,
  readInstalledVersion,
  resolveProviderExecutable,
} from "./agentAdapter.ts"
import { ClaudeAdapter } from "./claudeAdapter.ts"
import { CodexAdapter } from "./codexAdapter.ts"

export type Agent = typeof AgentSchema.Type

// The read-only built-in agent registry (ATC-124): the one place the public
// agent slugs meet the provider adapters. Availability and versions are
// detected on demand — nothing persisted, no migration. Adding an agent is
// one new adapter layer (wired in server.ts) plus registry entries: the
// contract's AgentId literal, PROVIDER_FOR_AGENT in agentAdapter.ts, and
// the table here — the Record types make a missed entry a compile error,
// and threads/ never changes.

export class AgentRegistry extends Context.Service<
  AgentRegistry,
  {
    readonly list: () => Effect.Effect<ReadonlyArray<Agent>>
    readonly get: (id: string) => Effect.Effect<Agent, AgentNotFound>
    /** The adapter behind a public slug — the domain's only door to providers. */
    readonly adapterFor: (id: typeof AgentId.Type) => AgentAdapter
  }
>()("app-server/AgentRegistry") {}

export const layer = Layer.effect(AgentRegistry)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    const subprocess = yield* Subprocess.Subprocess
    const codex = yield* CodexAdapter
    const claude = yield* ClaudeAdapter

    // The one registry table: adding an agent adds one entry here.
    const entries: Record<
      typeof AgentId.Type,
      { readonly adapter: AgentAdapter; readonly executable: string }
    > = {
      codex: { adapter: codex, executable: config.codexExecutable },
      "claude-code": { adapter: claude, executable: config.claudeExecutable },
    }

    const describe = (id: typeof AgentId.Type): Effect.Effect<Agent> => {
      const { executable } = entries[id]
      return resolveProviderExecutable(PROVIDER_FOR_AGENT[id], executable).pipe(
        Effect.flatMap((executable) =>
          readInstalledVersion(subprocess, executable).pipe(
            Effect.map((detected) => ({
              id,
              available: true,
              ...(detected !== null ? { detectedVersion: detected } : {}),
            })),
          ),
        ),
        Effect.catchTag("AgentUnavailable", (error) =>
          Effect.succeed({ id, available: false, reason: error.reason }),
        ),
      )
    }

    const isAgentId = (id: string): id is typeof AgentId.Type =>
      (AGENT_IDS as ReadonlyArray<string>).includes(id)

    return {
      list: () => Effect.forEach(AGENT_IDS, describe),
      get: (id) => (isAgentId(id) ? describe(id) : Effect.fail(new AgentNotFound({ agentId: id }))),
      adapterFor: (id) => entries[id].adapter,
    }
  }),
)
