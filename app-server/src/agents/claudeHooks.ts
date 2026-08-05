import { Context, Effect, Layer, Schema } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"
import type { AgentActivity } from "./agentAdapter.ts"

// Claude hook ingestion (ATC-123): one normalization of Claude Code's hook
// vocabulary into activity signals, fed from two transports — in-process
// SDK callbacks while ATC drives a session (claudeAdapter.ts), and the
// internal webhook below while a TUI drives one. TUI-driven sessions have
// no adapter connection (one-writer rule), so webhook activity is consumed
// via `subscribe` by whatever owns TUI session state — ATC-124's job; in
// ATC-123 deliveries are validated and then dropped. The webhook is
// machine-to-machine plumbing whose payload shape Claude Code owns, so it
// is deliberately NOT in the public contract or openapi.json. Spoofing is
// guarded by a per-session secret carried in the x-atc-hook-secret header
// (minted at TUI launch; a re-mint invalidates the session's prior secret);
// a payload whose session_id disagrees with the secret's registration is
// dropped. Hooks carry no turn or request correlation ids, so they
// normalize to activity only — never to turn or request events.

/** What one hook event says about the session's activity, if anything. */
export const hookActivity = (
  eventName: string,
  payload: Record<string, unknown>,
): AgentActivity | null => {
  switch (eventName) {
    case "UserPromptSubmit":
    case "PreToolUse":
    case "PostToolUse":
      return "working"
    case "Stop":
    case "StopFailure":
      return "idle"
    case "PermissionRequest":
      return "needs_input"
    case "Notification": {
      const kind = payload["notification_type"]
      if (kind === "idle_prompt") return "idle"
      if (
        kind === "permission_prompt" ||
        kind === "elicitation_dialog" ||
        kind === "agent_needs_input"
      ) {
        return "needs_input"
      }
      return null
    }
    default:
      // Lifecycle events (SessionStart/SessionEnd, ...) are ATC-owned facts
      // elsewhere; everything unrecognized is ignored, never guessed at.
      return null
  }
}

/** The only fields the receiver reads; Claude Code owns everything else. */
const HookPayload = Schema.Struct({
  session_id: Schema.String,
  hook_event_name: Schema.String,
})

export type HookListener = (providerSessionId: string, activity: AgentActivity) => void

export class ClaudeHooks extends Context.Service<
  ClaudeHooks,
  {
    /**
     * Mint and register the webhook secret for one provider session,
     * replacing (and invalidating) any prior secret for that session. The
     * registration lives until replaced; scoping secrets to the TUI
     * terminal session's lifetime is ATC-124 work.
     */
    readonly registerSecret: (providerSessionId: string) => Effect.Effect<string>
    /**
     * Subscribe to normalized webhook activity for TUI-driven sessions
     * (nothing subscribes in ATC-123). Scoped: closing unsubscribes.
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
    return {
      registerSecret: (providerSessionId: string) =>
        Effect.sync(() => {
          const previous = secretForSession.get(providerSessionId)
          if (previous !== undefined) secrets.delete(previous)
          const secret =
            crypto.randomUUID().replaceAll("-", "") + crypto.randomUUID().replaceAll("-", "")
          secrets.set(secret, providerSessionId)
          secretForSession.set(providerSessionId, secret)
          return secret
        }),
      subscribe: (listener: HookListener) =>
        Effect.gen(function* () {
          listeners.add(listener)
          yield* Effect.addFinalizer(() => Effect.sync(() => listeners.delete(listener)))
        }),
      deliver: (secret: string, payload: unknown) =>
        Effect.gen(function* () {
          const providerSessionId = secrets.get(secret)
          if (providerSessionId === undefined) return 404 as const
          const decoded = yield* Schema.decodeUnknownEffect(HookPayload)(payload).pipe(
            Effect.option,
          )
          if (decoded._tag === "None") return 400 as const
          if (decoded.value.session_id !== providerSessionId) return 400 as const
          const activity = hookActivity(
            decoded.value.hook_event_name,
            payload as Record<string, unknown>,
          )
          if (activity !== null) {
            for (const listener of listeners) listener(providerSessionId, activity)
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
