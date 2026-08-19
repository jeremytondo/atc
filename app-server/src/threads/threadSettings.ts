import { Schema } from "effect"
import { isReasoningLevel, ThreadAccess, ThreadMode } from "../api/contract.ts"
import type * as Contract from "../api/contract.ts"
import type { ProviderSettings } from "../agents/agentAdapter.ts"

// The pure settings rules (ATC-205), shared by the repositories that store
// settings, the Threads domain that patches them, and the runtime that
// adopts the provider's reports. Nothing here touches a provider or the
// database.

export type ThreadSettings = typeof Contract.ThreadSettings.Type
export type ThreadSettingsPatch = typeof Contract.ThreadSettingsPatch.Type
export type AgentModel = typeof Contract.AgentModel.Type

const isMode = Schema.is(ThreadMode)
const isAccess = Schema.is(ThreadAccess)

/**
 * The permissive stored→settings read both repositories use: a literal
 * this build does not know (written by a newer build) falls back to its
 * seed value rather than failing the row, so rows stay readable and the
 * provider's next report corrects them.
 */
export const settingsFromColumns = (row: {
  readonly model: string
  readonly reasoning: string | null
  readonly mode: string
  readonly access: string
}): ThreadSettings => ({
  model: row.model,
  ...(isReasoningLevel(row.reasoning) ? { reasoning: row.reasoning } : {}),
  mode: isMode(row.mode) ? row.mode : "chat",
  access: isAccess(row.access) ? row.access : "auto",
})

/** The inverse: the settings as the four columns store them. */
export const settingsToColumns = (settings: ThreadSettings) => ({
  model: settings.model,
  reasoning: settings.reasoning ?? null,
  mode: settings.mode,
  access: settings.access,
})

/** Field-wise equality (reasoning absent ≡ absent). */
export const sameSettings = (a: ThreadSettings, b: ThreadSettings): boolean =>
  a.model === b.model && a.reasoning === b.reasoning && a.mode === b.mode && a.access === b.access

/**
 * Adopt a provider's report over the settings a thread holds — provider
 * state wins — EXCEPT for the fields that merely confirm what ATC itself
 * pushed for the running turn (`pushed`): a delayed confirmation must never
 * roll back a change the user made meanwhile (Claude's child reports its
 * settings only once its ~seconds-long spawn has completed; Codex echoes
 * every override). Undefined when nothing changes.
 */
export const applyProviderSettings = (
  current: ThreadSettings,
  reported: ProviderSettings,
  pushed: ThreadSettings | undefined,
): ThreadSettings | undefined => {
  const pushedReasoning = pushed === undefined ? undefined : (pushed.reasoning ?? null)
  const reasoning =
    reported.reasoning === undefined || reported.reasoning === pushedReasoning
      ? current.reasoning
      : reported.reasoning === null
        ? undefined
        : reported.reasoning
  const adopted: ThreadSettings = {
    model:
      reported.model === undefined || reported.model === pushed?.model
        ? current.model
        : reported.model,
    ...(reasoning !== undefined ? { reasoning } : {}),
    mode:
      reported.mode === undefined || reported.mode === pushed?.mode ? current.mode : reported.mode,
    access:
      reported.access === undefined || reported.access === pushed?.access
        ? current.access
        : reported.access,
  }
  return sameSettings(adopted, current) ? undefined : adopted
}

/**
 * Apply a settings patch against the agent's model catalog. Reasoning is
 * per model: an explicit reasoning must be one the model supports, and on
 * a model change without one the current level carries over when the new
 * model supports it, else the new model's default applies (absent when the
 * model has no effort support). `catalog` is null when the caller did not
 * need it (a patch touching neither model nor reasoning); with a catalog,
 * a change to an unknown model is a rejection.
 */
export const applySettingsPatch = (
  current: ThreadSettings,
  patch: ThreadSettingsPatch,
  catalog: ReadonlyArray<AgentModel> | null,
): { readonly settings: ThreadSettings } | { readonly rejected: string } => {
  const model = patch.model ?? current.model
  const entry = catalog?.find((candidate) => candidate.value === model)
  // Only a model CHANGE is held to the catalog: a thread already on a model
  // the catalog no longer lists (retired, or hidden by the provider) keeps
  // taking mode/access/reasoning patches; its reasoning cannot be validated
  // then and is accepted as given.
  if (patch.model !== undefined && catalog !== null && entry === undefined) {
    return { rejected: `unknown model "${model}"` }
  }
  if (
    entry !== undefined &&
    patch.reasoning !== undefined &&
    !entry.supportedEffortLevels.includes(patch.reasoning)
  ) {
    return { rejected: `model "${model}" does not support reasoning "${patch.reasoning}"` }
  }
  const carried =
    entry === undefined || model === current.model
      ? current.reasoning
      : current.reasoning !== undefined && entry.supportedEffortLevels.includes(current.reasoning)
        ? current.reasoning
        : entry.defaultEffortLevel
  const reasoning = patch.reasoning ?? carried
  return {
    settings: {
      model,
      ...(reasoning !== undefined ? { reasoning } : {}),
      mode: patch.mode ?? current.mode,
      access: patch.access ?? current.access,
    },
  }
}
