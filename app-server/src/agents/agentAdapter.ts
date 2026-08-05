import { Effect, Schema, Stream } from "effect"
import type { Scope } from "effect"
import { existsSync, realpathSync } from "node:fs"
import type { Subprocess } from "../platform/subprocess.ts"
import { resolveExecutable } from "../platform/subprocess.ts"

// The shared provider seam (ATC-123): how ATC drives coding-agent providers
// (Codex app-server, Claude Agent SDK). Adapters own provider transports,
// wire shapes, and version details; everything above the seam sees only
// stable provider identity, exact-session create/resume, truthful normalized
// status, and safe control flow. Invariants every adapter must hold:
//
//   - Create and resume are distinct; neither ever falls back to the other.
//     Resume verifies the exact provider session id and working directory
//     from provider evidence and fails closed on any mismatch — a changed
//     identity is never adopted as a repair.
//   - Exactly one active writer per provider session. Connections are
//     writers; observation never opens a second writer.
//   - Status is derived from structured provider events only — never from
//     transcript or terminal-output parsing.
//   - Responding to provider requests (approvals, questions) is deferred to
//     ATC-124: adapters surface `requestOpened` and answer the provider
//     conservatively (reject) so a turn can never hang on ATC.
//
// The fake adapter for tests lives in test/agents/fakeAgentAdapter.ts; per-provider
// service tags live with their adapters (codexAdapter.ts, claudeAdapter.ts).

export const AGENT_PROVIDERS = ["codex", "claude"] as const

export type AgentProvider = (typeof AGENT_PROVIDERS)[number]

/**
 * Normalized activity for one provider session. `unknown` means no evidence
 * — never a guess. A pending provider request always wins over the
 * provider's more general running state (`needs_input` beats `working`).
 */
export type AgentActivity = "idle" | "working" | "needs_input" | "unknown"

export type AgentTurnOutcome = "completed" | "interrupted" | "failed"

export type AgentRequestKind = "approval" | "question"

/**
 * One entry of the normalized status feed. Turn and request ids are
 * adapter-scoped correlation ids — provider-native where the provider has
 * them (Codex), adapter-minted and process-local where it does not
 * (Claude); never persist them as durable identity. `turnStarted` always
 * precedes its `turnCompleted`. Raw provider events stay inside the
 * adapters (logged there for diagnostics, never re-parsed above the seam).
 */
export type AgentEvent =
  | { readonly type: "activity"; readonly activity: AgentActivity }
  | { readonly type: "turnStarted"; readonly turnId: string }
  | {
      readonly type: "turnCompleted"
      readonly turnId: string
      readonly outcome: AgentTurnOutcome
      /** Human-readable failure/interrupt detail, for diagnostics only. */
      readonly detail?: string
    }
  | { readonly type: "requestOpened"; readonly requestId: string; readonly kind: AgentRequestKind }
  | { readonly type: "requestClosed"; readonly requestId: string }

/** Correlation handle for exactly one turn — interrupt targets this turn. */
export interface AgentTurn {
  readonly turnId: string
}

/** What an adapter supports, for callers that must not guess. */
export interface AgentCapabilities {
  /** Provider version the adapter was validated against (drift warns, never blocks). */
  readonly testedVersion: string
  /**
   * How live activity is observed while a TUI drives the session:
   * `shared-server` = full event fan-out from the multiplexed provider
   * server; `hooks` = provider hook callbacks (coarser).
   */
  readonly tuiObservation: "shared-server" | "hooks"
}

/**
 * How to launch the provider's TUI attached to an existing session. The
 * caller owns the actual launch (a Terminal session); the adapter owns argv
 * so provider flags never leak above the seam.
 */
export interface TuiLaunchSpec {
  /** Full argv; argv[0] is the resolved provider executable. */
  readonly command: ReadonlyArray<string>
  /** Environment the TUI needs on top of the terminal's own. */
  readonly env: Readonly<Record<string, string>>
}

/**
 * A live writer connection to one provider session. Lives in a Scope:
 * closing the scope stops the driver (releasing the writer role) and never
 * deletes provider history.
 *
 * A connection can also end BEFORE its scope closes: transport loss, or a
 * provider whose sessions terminate with a non-success turn (Claude). The
 * uniform contract for consumers: `AgentUnavailable` from a control call
 * means "this session is over — resume it for a fresh connection";
 * `AgentConflict` means the target itself is invalid (busy/stale turn, or
 * a handle the caller already closed).
 */
export interface AgentConnection {
  /**
   * The provider session identity. Exact and verified as soon as provider
   * evidence exists; for a deferred-verification provider (Claude resume)
   * it is the expected id until the first turn verifies it fail-closed.
   */
  readonly providerSessionId: string
  /** Verified canonical working directory. */
  readonly cwd: string
  /** Current normalized activity snapshot. */
  readonly activity: Effect.Effect<AgentActivity>
  /**
   * The normalized status feed. Single-consumer. Ends cleanly when the
   * caller closes the connection scope, or after delivering a terminal
   * `turnCompleted` on a provider whose connections end with non-success
   * turns; FAILS with `AgentProtocolError` when the connection dies with
   * no truthful terminal event (transport loss, failed verification).
   * Fan-out to multiple observers happens above the seam.
   */
  readonly events: Stream.Stream<AgentEvent, AgentProtocolError>
  /**
   * Start exactly one turn with user input. Fails with `AgentConflict`
   * while another turn is active on this connection; completion arrives on
   * the feed as `turnCompleted`. A provider whose resume verification is
   * deferred (Claude) completes it here on the connection's first turn, so
   * `AgentResumeFailed` / `AgentIdentityMismatch` can surface fail-closed.
   */
  readonly startTurn: (
    input: string,
  ) => Effect.Effect<
    AgentTurn,
    | AgentConflict
    | AgentUnavailable
    | AgentResumeFailed
    | AgentIdentityMismatch
    | AgentProtocolError
  >
  /**
   * Interrupt exactly `turn`. A stale or unknown target fails with
   * `AgentConflict` — never "interrupt whatever is active". Success means
   * the provider accepted the interrupt; the truthful outcome is the feed's
   * `turnCompleted` with outcome `interrupted`.
   */
  readonly interrupt: (
    turn: AgentTurn,
  ) => Effect.Effect<void, AgentConflict | AgentUnavailable | AgentProtocolError>
}

/** A successful create: the connection plus the already-started first turn. */
export interface AgentSessionStart {
  readonly connection: AgentConnection
  readonly turn: AgentTurn
}

/**
 * The shared adapter interface. Create requires initial input because both
 * providers establish durable identity while starting a turn (and a Codex
 * thread with zero completed turns is not restart-resumable), so a created
 * session is durable only once its first turn completes.
 */
export interface AgentAdapter {
  readonly provider: AgentProvider
  readonly capabilities: AgentCapabilities
  /**
   * Create a new provider session in `cwd` and start its first turn. The
   * provider's echoed working directory is verified like resume's — a
   * disagreeing echo is `AgentIdentityMismatch`, never adopted.
   */
  readonly createSession: (options: {
    readonly cwd: string
    readonly input: string
  }) => Effect.Effect<
    AgentSessionStart,
    AgentUnavailable | AgentConflict | AgentIdentityMismatch | AgentProtocolError,
    Scope.Scope
  >
  /**
   * Resume the exact existing session. Never creates one: an unknown id is
   * `AgentResumeFailed`, and evidence disagreeing with the expected id or
   * cwd is `AgentIdentityMismatch`. Verification uses the earliest provider
   * evidence available: Codex verifies here (thread/resume echoes identity);
   * the Claude SDK emits no identity evidence until a turn starts (probed
   * 2026-08-03), so its verification completes — still fail-closed — at the
   * connection's first `startTurn`.
   */
  readonly resumeSession: (options: {
    readonly providerSessionId: string
    readonly cwd: string
  }) => Effect.Effect<
    AgentConnection,
    | AgentUnavailable
    | AgentConflict
    | AgentResumeFailed
    | AgentIdentityMismatch
    | AgentProtocolError,
    Scope.Scope
  >
  /** How to launch the provider TUI attached to an existing session. */
  readonly tuiLaunch: (options: {
    readonly providerSessionId: string
    readonly cwd: string
  }) => Effect.Effect<TuiLaunchSpec, AgentUnavailable>
}

// Internal-only failures (ATC-123): not part of the HTTP contract, so no
// httpApiStatus annotations. ATC-124 maps them to public errors when agent
// sessions become an API surface.

/** Provider missing, not ready, or gone — retryable once the cause is fixed. */
export class AgentUnavailable extends Schema.TaggedErrorClass<AgentUnavailable>()(
  "AgentUnavailable",
  { provider: Schema.Literals(AGENT_PROVIDERS), reason: Schema.String },
) {
  override get message(): string {
    return `${this.provider} unavailable: ${this.reason}`
  }
}

/** The provider rejected the exact persisted session id (fail closed). */
export class AgentResumeFailed extends Schema.TaggedErrorClass<AgentResumeFailed>()(
  "AgentResumeFailed",
  {
    provider: Schema.Literals(AGENT_PROVIDERS),
    providerSessionId: Schema.String,
    reason: Schema.String,
  },
) {
  override get message(): string {
    return `${this.provider} resume of ${this.providerSessionId} failed: ${this.reason}`
  }
}

/** Provider evidence disagrees with the expected identity — never adopted. */
export class AgentIdentityMismatch extends Schema.TaggedErrorClass<AgentIdentityMismatch>()(
  "AgentIdentityMismatch",
  {
    provider: Schema.Literals(AGENT_PROVIDERS),
    field: Schema.Literals(["sessionId", "cwd"]),
    expected: Schema.String,
    actual: Schema.String,
  },
) {
  override get message(): string {
    return `${this.provider} ${this.field} mismatch: expected ${this.expected}, got ${this.actual}`
  }
}

/** Required structured evidence was absent or invalid, or the transport broke. */
export class AgentProtocolError extends Schema.TaggedErrorClass<AgentProtocolError>()(
  "AgentProtocolError",
  { provider: Schema.Literals(AGENT_PROVIDERS), reason: Schema.String },
) {
  override get message(): string {
    return `${this.provider} protocol error: ${this.reason}`
  }
}

/** Writer or turn-target conflict: the control target is not (or no longer) valid. */
export class AgentConflict extends Schema.TaggedErrorClass<AgentConflict>()("AgentConflict", {
  provider: Schema.Literals(AGENT_PROVIDERS),
  reason: Schema.String,
}) {
  override get message(): string {
    return `${this.provider} conflict: ${this.reason}`
  }
}

const INSTALL_HINTS: Record<AgentProvider, string> = {
  codex: "install the Codex CLI",
  claude: "install and authenticate Claude Code",
}

const CONFIG_KEYS: Record<AgentProvider, string> = {
  codex: "codexExecutable",
  claude: "claudeExecutable",
}

/**
 * Resolve a provider executable from its settled configuration value
 * (AppConfig `codexExecutable` / `claudeExecutable`), or fail with the one
 * actionable install-or-configure diagnostic. Configured paths are
 * existence-checked here so a typo'd path fails as this diagnostic, not as
 * a downstream launch timeout.
 */
export const resolveProviderExecutable = (
  provider: AgentProvider,
  configured: string,
): Effect.Effect<string, AgentUnavailable> =>
  Effect.suspend(() => {
    const found = resolveExecutable(configured)
    if (found !== null && existsSync(found)) return Effect.succeed(found)
    const key = CONFIG_KEYS[provider]
    return Effect.fail(
      new AgentUnavailable({
        provider,
        reason:
          `${configured} not found; ${INSTALL_HINTS[provider]} ` +
          `or set ${key} in config.toml / ATC_${provider.toUpperCase()}_EXECUTABLE`,
      }),
    )
  })

/** Symlink-tolerant path equality (macOS tmpdir lives behind /private). */
export const samePath = (left: string, right: string): boolean => {
  const canonical = (value: string): string => {
    try {
      return realpathSync(value)
    } catch {
      return value
    }
  }
  return canonical(left) === canonical(right)
}

/**
 * The shared version-drift rule (record + warn, never block), memoized to
 * one check per adapter: resolve the configured executable, read the
 * installed version via `--version`, and log one actionable warning when
 * it is below the floor the adapter was validated against. Any failure to
 * determine the version is itself just a warning.
 */
export const makeVersionGate = (
  subprocess: Subprocess["Service"],
  provider: AgentProvider,
  configured: string,
  testedVersion: string,
): Effect.Effect<void, AgentUnavailable> => {
  let checked = false
  return Effect.gen(function* () {
    if (checked) return
    checked = true
    const executable = yield* resolveProviderExecutable(provider, configured)
    const output = yield* Effect.scoped(
      Effect.gen(function* () {
        const child = yield* subprocess.spawn({
          executable,
          args: ["--version"],
          env: {},
          extendEnv: true,
        })
        const lines = yield* Stream.runCollect(child.stdoutLines)
        yield* child.exitCode
        return lines.join(" ")
      }),
    ).pipe(Effect.orElseSucceed(() => ""))
    const match = output.match(/(\d+)\.(\d+)\.(\d+)/)
    if (match === null) {
      return yield* Effect.logWarning(
        `could not determine the installed ${provider} version (tested against ${testedVersion})`,
      )
    }
    // Components are small; packing keeps the comparison one expression.
    const pack = (parts: ReadonlyArray<number>) =>
      (parts[0] ?? 0) * 1_000_000 + (parts[1] ?? 0) * 1_000 + (parts[2] ?? 0)
    const installed = [Number(match[1]), Number(match[2]), Number(match[3])]
    if (pack(installed) < pack(testedVersion.split(".").map(Number))) {
      yield* Effect.logWarning(
        `installed ${provider} ${installed.join(".")} is older than the tested ${testedVersion}; proceeding, but upgrade it if provider behavior misbehaves`,
      )
    }
  })
}
