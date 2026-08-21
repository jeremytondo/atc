import { Cache, Context, Duration, Effect, Exit, Layer, Option } from "effect"
import { AGENT_IDS, AgentNotFound, isAgentId, ProviderUnavailable } from "../api/contract.ts"
import type * as Contract from "../api/contract.ts"
import type { AgentId, DirectoryCheckTimedOut, DirectoryUnavailable } from "../api/contract.ts"
import { AppConfig } from "../platform/config.ts"
import { Directories } from "../platform/directories.ts"
import * as Subprocess from "../platform/subprocess.ts"
import type { AgentAdapter, AgentCommand, AgentModel, ThreadSettings } from "./agentAdapter.ts"
import {
  memoizeSuccess,
  PROVIDER_FOR_AGENT,
  readInstalledVersion,
  resolveProviderExecutable,
} from "./agentAdapter.ts"
import { AgentDefaultsRepository } from "./agentDefaultsRepository.ts"
import { ClaudeAdapter } from "./claudeAdapter.ts"
import { CodexAdapter } from "./codexAdapter.ts"

export type Agent = typeof Contract.Agent.Type

// The built-in agent registry (ATC-124): the one place the public agent
// slugs meet the provider adapters. Availability and versions are detected
// on demand — nothing persisted. Adding an agent is one new adapter layer
// (wired in server.ts) plus registry entries: the contract's AgentId
// literal, PROVIDER_FOR_AGENT in agentAdapter.ts, and the table here — the
// Record types make a missed entry a compile error, and threads/ never
// changes.
//
// It also owns the per-agent facts the Chat settings and composer need:
//   - the model catalog (ATC-205), read from the adapter and cached for a
//     short while (Claude's read spawns a process; Codex's is a socket call)
//     — a failed read is never cached, so a provider that comes up later is
//     seen;
//   - the command list per directory (ATC-216), read the same way and cached
//     per agent and canonical directory — the probe is the freshness model;
//   - the write-through defaults a new thread inherits: the agent_defaults
//     row when one exists, else the entry's seed here. The seed is a
//     starting point only (the first change any thread makes replaces it);
//     it moves with the adapters' tested versions, like their title models.

/** How long one catalog read is served before the provider is asked again. */
const MODEL_CATALOG_TTL: Duration.Input = "10 minutes"

/** How long one command listing (per agent and directory) is served. */
const COMMANDS_TTL: Duration.Input = "1 minute"

export class AgentRegistry extends Context.Service<
  AgentRegistry,
  {
    readonly list: () => Effect.Effect<ReadonlyArray<Agent>>
    readonly get: (id: string) => Effect.Effect<Agent, AgentNotFound>
    /** The adapter behind a public slug — the domain's only door to providers. */
    readonly adapterFor: (id: typeof AgentId.Type) => AgentAdapter
    /** The agent's model catalog (cached; see the header). */
    readonly models: (
      id: string,
    ) => Effect.Effect<ReadonlyArray<AgentModel>, AgentNotFound | ProviderUnavailable>
    /** The agent's commands in `dir` (validated and canonicalized here; see the header). */
    readonly commands: (
      id: string,
      dir: string,
    ) => Effect.Effect<
      ReadonlyArray<AgentCommand>,
      AgentNotFound | ProviderUnavailable | DirectoryUnavailable | DirectoryCheckTimedOut
    >
    /** What a new thread of this agent starts with (see the header). */
    readonly defaults: (id: typeof AgentId.Type) => Effect.Effect<ThreadSettings>
    /** Write-through: the settings the last changed thread ended up with. */
    readonly setDefaults: (id: typeof AgentId.Type, settings: ThreadSettings) => Effect.Effect<void>
  }
>()("app-server/AgentRegistry") {}

export const layer = Layer.effect(AgentRegistry)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    const directories = yield* Directories
    const subprocess = yield* Subprocess.Subprocess
    const defaultsRepository = yield* AgentDefaultsRepository
    const codex = yield* CodexAdapter
    const claude = yield* ClaudeAdapter

    // The one registry table: adding an agent adds one entry here.
    const entries: Record<
      typeof AgentId.Type,
      {
        readonly adapter: AgentAdapter
        readonly executable: string
        readonly seed: ThreadSettings
      }
    > = {
      codex: {
        adapter: codex,
        executable: config.codexExecutable,
        seed: { model: "gpt-5.6-sol", reasoning: "high", mode: "chat", access: "auto" },
      },
      "claude-code": {
        adapter: claude,
        executable: config.claudeExecutable,
        seed: { model: "opus[1m]", reasoning: "high", mode: "chat", access: "auto" },
      },
    }

    const defaults = (id: typeof AgentId.Type): Effect.Effect<ThreadSettings> =>
      defaultsRepository.get(id).pipe(Effect.map(Option.getOrElse(() => entries[id].seed)))

    // One memoized catalog read per agent (memoizeSuccess: a failed read
    // is never served to later callers).
    const catalogs = yield* Effect.forEach(AGENT_IDS, (id) =>
      memoizeSuccess(
        entries[id].adapter
          .listModels()
          .pipe(
            Effect.mapError(
              (error) => new ProviderUnavailable({ agentId: id, reason: error.message }),
            ),
          ),
        MODEL_CATALOG_TTL,
      ).pipe(Effect.map((models) => [id, models] as const)),
    ).pipe(Effect.map((pairs) => new Map(pairs)))

    // One command cache per agent, keyed by canonical directory; a failed
    // probe has zero time to live, so the next ask retries.
    const commandLists = yield* Effect.forEach(AGENT_IDS, (id) =>
      Cache.makeWith(
        (dir: string) =>
          entries[id].adapter
            .listCommands({ cwd: dir })
            .pipe(
              Effect.mapError(
                (error) => new ProviderUnavailable({ agentId: id, reason: error.message }),
              ),
            ),
        {
          capacity: 32,
          timeToLive: (exit) => (Exit.isSuccess(exit) ? COMMANDS_TTL : Duration.zero),
        },
      ).pipe(Effect.map((cache) => [id, cache] as const)),
    ).pipe(Effect.map((pairs) => new Map(pairs)))

    const describe = (id: typeof AgentId.Type): Effect.Effect<Agent> =>
      Effect.gen(function* () {
        const availability: Omit<Agent, "id" | "defaults"> = yield* resolveProviderExecutable(
          PROVIDER_FOR_AGENT[id],
          entries[id].executable,
        ).pipe(
          Effect.flatMap((executable) =>
            readInstalledVersion(subprocess, executable).pipe(
              Effect.map((detected) => ({
                available: true,
                ...(detected !== null ? { detectedVersion: detected } : {}),
              })),
            ),
          ),
          Effect.catchTag("AgentUnavailable", (error) =>
            Effect.succeed({ available: false, reason: error.reason }),
          ),
        )
        return { id, ...availability, defaults: yield* defaults(id) }
      })

    return {
      list: () => Effect.forEach(AGENT_IDS, describe),
      get: (id) => (isAgentId(id) ? describe(id) : Effect.fail(new AgentNotFound({ agentId: id }))),
      adapterFor: (id) => entries[id].adapter,
      models: (id) =>
        isAgentId(id) ? catalogs.get(id)! : Effect.fail(new AgentNotFound({ agentId: id })),
      commands: (id, dir) =>
        isAgentId(id)
          ? directories
              .canonicalize(dir)
              .pipe(Effect.flatMap((canonical) => Cache.get(commandLists.get(id)!, canonical)))
          : Effect.fail(new AgentNotFound({ agentId: id })),
      defaults,
      setDefaults: (id, settings) => defaultsRepository.set(id, settings),
    }
  }),
)
