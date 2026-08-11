import { Context, Effect, Layer, Schema } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"
import type { AgentActivity, AgentSessionEvent } from "./agentAdapter.ts"
import { aggregateActivity } from "./agentAdapter.ts"

// Claude hook ingestion (ATC-123): one normalization of Claude Code's hook
// vocabulary into activity signals, fed from two transports — in-process
// SDK callbacks while ATC drives a session (claudeAdapter.ts), and the
// internal webhook below while a TUI drives one. TUI-driven sessions have
// no adapter connection (one-writer rule), so webhook activity reaches
// consumers via `subscribe` — the Claude adapter's observeSession is the
// subscriber, filtering per session for the seam's activity stream. The
// webhook is machine-to-machine plumbing whose payload shape Claude Code
// owns, so it is deliberately NOT in the public contract or openapi.json.
// Spoofing is guarded by a per-session secret carried in the
// x-atc-hook-secret header (stable per thread: persisted in the thread's
// opaque provider metadata and adopted back after restarts; registering a
// different secret invalidates the prior one); a payload whose session_id
// disagrees with the secret's registration is dropped. Hooks carry no turn
// or request correlation ids, so they normalize to activity — never to
// turn or request events — plus root UserPromptSubmit prompt text
// (ATC-155 auto-naming evidence).
//
// Activity is the AGGREGATE of the whole session tree (ATC-158): the root
// agent loop plus its background work (subagents, backgrounded shells,
// workflows, session crons). Each session gets an ActivityTracker whose
// background set is REPLACED from the level snapshots Claude attaches to
// Stop/SubagentStop payloads (`background_tasks`, `session_crons`) — never
// an edge-only counter that could wedge after a missed event. Tracker
// state is in-memory; after a restart the first level-bearing payload
// rebuilds it.

/**
 * Per-session aggregate activity state. `update` consumes one hook payload
 * (either transport) and returns the new aggregate, or null when the event
 * carries no activity signal. Probe evidence (2026-08-10, findings in
 * experiments/subagent-activity): the SubagentStop level snapshot still
 * CONTAINS the stopping agent, so it is subtracted by the payload's
 * `agent_id` — that subtraction is the last-child idle transition.
 */
export interface ActivityTracker {
  readonly update: (eventName: string, payload: Record<string, unknown>) => AgentActivity | null
  /** Root-loop evidence from outside the hook vocabulary (SDK
   * session_state_changed). Returns the new aggregate. */
  readonly setRoot: (activity: AgentActivity) => AgentActivity
  /** Replace the background set from a non-hook level snapshot (SDK
   * background_tasks_changed). Returns the new aggregate. */
  readonly replaceBackground: (ids: ReadonlyArray<string>) => AgentActivity
  readonly aggregate: () => AgentActivity
}

const NEEDS_INPUT_NOTIFICATIONS = new Set([
  "permission_prompt",
  "elicitation_dialog",
  "agent_needs_input",
])

export const makeActivityTracker = (): ActivityTracker => {
  let root: AgentActivity = "unknown"
  // Background descendants by id: subagents, backgrounded shells, workflows,
  // and task-list edges pending their next snapshot. Values are only ever
  // busy states ("working" | "needs_input"); finished work is removed.
  const background = new Map<string, AgentActivity>()
  let cronCount = 0

  const aggregate = (): AgentActivity => {
    const items = [...background.values()]
    // A pending session cron will wake the session and write (ATC-158
    // decision): it counts as busy until the provider reports it gone.
    if (cronCount > 0) items.push("working")
    return aggregateActivity(root, items)
  }

  /** Replace the set from a level snapshot, keeping a retained descendant's
   * needs_input (the snapshot's coarse `status` cannot see prompts). */
  const replace = (ids: ReadonlyArray<string>): void => {
    const next = new Map<string, AgentActivity>()
    for (const id of ids) {
      next.set(id, background.get(id) === "needs_input" ? "needs_input" : "working")
    }
    background.clear()
    for (const [id, activity] of next) background.set(id, activity)
  }

  const update = (eventName: string, payload: Record<string, unknown>): AgentActivity | null => {
    let signal = false
    const agentId = typeof payload["agent_id"] === "string" ? payload["agent_id"] : null
    const taskId = typeof payload["task_id"] === "string" ? payload["task_id"] : null

    // Level snapshots are authoritative wherever they appear, and always
    // land before the event's own edge below (SubagentStop's snapshot
    // still includes the stopping agent).
    const tasks = payload["background_tasks"]
    if (Array.isArray(tasks)) {
      signal = true
      replace(
        tasks.flatMap((task) => {
          const id = (task as { id?: unknown }).id
          return typeof id === "string" ? [id] : []
        }),
      )
    }
    const crons = payload["session_crons"]
    if (Array.isArray(crons)) {
      signal = true
      cronCount = crons.length
    }

    switch (eventName) {
      case "SessionStart":
      case "SessionEnd":
        // Background tasks and session crons are process-local: a session
        // (re)starting or ending means the previous process's in-flight
        // work is gone, whatever this tracker still holds — and the new or
        // ended process is running nothing yet. Honest reset, not a guess.
        background.clear()
        cronCount = 0
        root = "idle"
        return aggregate()
      case "UserPromptSubmit":
      case "PreToolUse":
      case "PostToolUse":
      case "PostToolUseFailure":
      case "PermissionDenied":
        // With agent_id the event fired inside a descendant: it is that
        // descendant's liveness (and clears its needs_input — a denied
        // permission or failed tool means the prompt resolved), never the
        // root's.
        if (agentId === null) root = "working"
        else background.set(agentId, "working")
        return aggregate()
      case "Stop":
      case "StopFailure":
        if (agentId === null) {
          root = "idle"
          return aggregate()
        }
        return signal ? aggregate() : null
      case "SubagentStart":
        if (agentId !== null) background.set(agentId, "working")
        return aggregate()
      case "SubagentStop":
        if (agentId !== null) background.delete(agentId)
        return aggregate()
      case "TaskCreated":
        // Task-list edge evidence pending the next snapshot; a snapshot
        // replacement drops ids the provider does not count as background
        // work, so a mislabeled edge self-corrects at the next Stop.
        if (taskId !== null) background.set(taskId, "working")
        return aggregate()
      case "TaskCompleted":
        if (taskId !== null) background.delete(taskId)
        return aggregate()
      case "PermissionRequest":
        if (agentId === null) root = "needs_input"
        else background.set(agentId, "needs_input")
        return aggregate()
      case "Notification": {
        const kind = payload["notification_type"]
        if (kind === "idle_prompt") {
          if (agentId !== null) return signal ? aggregate() : null
          root = "idle"
          return aggregate()
        }
        if (typeof kind === "string" && NEEDS_INPUT_NOTIFICATIONS.has(kind)) {
          if (agentId === null) root = "needs_input"
          else background.set(agentId, "needs_input")
          return aggregate()
        }
        return signal ? aggregate() : null
      }
      default:
        // Everything unrecognized is ignored, never guessed at — unless
        // it carried a level snapshot above.
        return signal ? aggregate() : null
    }
  }

  return {
    update,
    setRoot: (activity) => {
      root = activity
      return aggregate()
    },
    replaceBackground: (ids) => {
      replace(ids)
      return aggregate()
    },
    aggregate,
  }
}

/** The only fields the receiver reads; Claude Code owns everything else. */
const HookPayload = Schema.Struct({
  session_id: Schema.String,
  hook_event_name: Schema.String,
})

export type HookListener = (providerSessionId: string, event: AgentSessionEvent) => void

/**
 * The ROOT session's user prompt from a UserPromptSubmit payload, or null:
 * a payload carrying `agent_id` fired inside a subagent — its prompt is
 * synthetic dispatch, never the user's message (ATC-155).
 */
const userPromptOf = (eventName: string, payload: Record<string, unknown>): string | null => {
  if (eventName !== "UserPromptSubmit") return null
  if (typeof payload["agent_id"] === "string") return null
  const prompt = payload["prompt"]
  return typeof prompt === "string" && prompt !== "" ? prompt : null
}

export class ClaudeHooks extends Context.Service<
  ClaudeHooks,
  {
    /**
     * Mint and register the webhook secret for one provider session,
     * replacing (and invalidating) any prior secret for that session.
     * The registration lives until replaced, adopted over, or revoked.
     */
    readonly registerSecret: (providerSessionId: string) => Effect.Effect<string>
    /**
     * Register a known secret for one provider session (the persisted
     * per-thread secret being restored after a restart or reused across
     * launches), replacing any prior registration. Idempotent.
     */
    readonly adoptSecret: (providerSessionId: string, secret: string) => Effect.Effect<void>
    /** Drop the session's registration (thread deletion). Idempotent. */
    readonly revokeSecret: (providerSessionId: string) => Effect.Effect<void>
    /**
     * Subscribe to normalized webhook session events (activity and root
     * user prompts) for TUI-driven sessions. Scoped: closing unsubscribes.
     */
    readonly subscribe: (
      listener: HookListener,
    ) => Effect.Effect<void, never, import("effect").Scope.Scope>
    /**
     * One webhook delivery. Returns the HTTP status to answer with: 204
     * accepted, 404 unknown secret (nothing is leaked about why), 400
     * malformed payload or session mismatch.
     */
    readonly deliver: (secret: string, payload: unknown) => Effect.Effect<204 | 400 | 404>
  }
>()("app-server/ClaudeHooks") {}

export const layer = Layer.effect(ClaudeHooks)(
  Effect.sync(() => {
    const secrets = new Map<string, string>()
    const secretForSession = new Map<string, string>()
    const listeners = new Set<HookListener>()
    // One aggregate tracker per TUI-driven session, keyed by session id so
    // secret rotation keeps the accumulated background evidence.
    const trackers = new Map<string, ActivityTracker>()
    const adopt = (providerSessionId: string, secret: string): void => {
      const previous = secretForSession.get(providerSessionId)
      if (previous !== undefined) secrets.delete(previous)
      secrets.set(secret, providerSessionId)
      secretForSession.set(providerSessionId, secret)
    }
    return {
      registerSecret: (providerSessionId: string) =>
        Effect.sync(() => {
          const secret =
            crypto.randomUUID().replaceAll("-", "") + crypto.randomUUID().replaceAll("-", "")
          adopt(providerSessionId, secret)
          return secret
        }),
      adoptSecret: (providerSessionId: string, secret: string) =>
        Effect.sync(() => adopt(providerSessionId, secret)),
      revokeSecret: (providerSessionId: string) =>
        Effect.sync(() => {
          const previous = secretForSession.get(providerSessionId)
          if (previous !== undefined) secrets.delete(previous)
          secretForSession.delete(providerSessionId)
          trackers.delete(providerSessionId)
        }),
      subscribe: (listener: HookListener) =>
        Effect.acquireRelease(
          Effect.sync(() => listeners.add(listener)),
          () => Effect.sync(() => listeners.delete(listener)),
        ).pipe(Effect.asVoid),
      deliver: (secret: string, payload: unknown) =>
        Effect.gen(function* () {
          const providerSessionId = secrets.get(secret)
          if (providerSessionId === undefined) return 404 as const
          const decoded = yield* Schema.decodeUnknownEffect(HookPayload)(payload).pipe(
            Effect.option,
          )
          if (decoded._tag === "None") return 400 as const
          if (decoded.value.session_id !== providerSessionId) return 400 as const
          const tracker = trackers.get(providerSessionId) ?? makeActivityTracker()
          trackers.set(providerSessionId, tracker)
          const record = payload as Record<string, unknown>
          // The prompt (its cause) goes out before the activity transition.
          const prompt = userPromptOf(decoded.value.hook_event_name, record)
          if (prompt !== null) {
            for (const listener of listeners) {
              listener(providerSessionId, { type: "userPrompt", text: prompt })
            }
          }
          const activity = tracker.update(decoded.value.hook_event_name, record)
          if (activity !== null) {
            for (const listener of listeners) {
              listener(providerSessionId, { type: "activity", activity })
            }
          }
          return 204 as const
        }),
    }
  }),
)

/** The header carrying the per-session secret (never the URL: request
 * paths land in tracer span names and logs). */
export const SECRET_HEADER = "x-atc-hook-secret"

/**
 * The internal webhook route. Not part of the public contract: it exists
 * for Claude Code's hook commands only, and its shape may change with the
 * provider. Mounted by server.ts alongside the contract routes.
 */
export const route = HttpRouter.add(
  "POST",
  "/internal/claude/hooks",
  Effect.gen(function* () {
    const hooks = yield* ClaudeHooks
    const request = yield* HttpServerRequest.HttpServerRequest
    const payload = yield* request.json.pipe(Effect.orElseSucceed(() => null))
    const status = yield* hooks.deliver(request.headers[SECRET_HEADER] ?? "", payload)
    return HttpServerResponse.empty({ status })
  }),
)
