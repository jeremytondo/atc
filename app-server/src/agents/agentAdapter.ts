import { Effect, Schema, Stream } from "effect"
import type { Scope } from "effect"
import { existsSync, realpathSync } from "node:fs"
import type { AgentId } from "../api/contract.ts"
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
//   - Responding to provider requests (approvals, questions) is native-mode
//     work: adapters surface `requestOpened` and answer the provider
//     conservatively (reject) so a turn can never hang on ATC.
//
// The fake adapter for tests lives in test/agents/fakeAgentAdapter.ts; per-provider
// service tags live with their adapters (codexAdapter.ts, claudeAdapter.ts).

export const AGENT_PROVIDERS = ["codex", "claude"] as const

export type AgentProvider = (typeof AGENT_PROVIDERS)[number]

/**
 * Public registry slug (contract `AgentId`) → seam provider. The Record
 * type makes an unmapped slug a compile error, so the public vocabulary
 * and the seam's can never drift apart silently.
 */
export const PROVIDER_FOR_AGENT: Record<typeof AgentId.Type, AgentProvider> = {
  codex: "codex",
  "claude-code": "claude",
}

/**
 * Nested-session environment markers, scrubbed wherever ATC launches an
 * agent process or TUI (uniformly — not per provider): inherited from a
 * dev-mode ATC running inside a session, they silently change provider
 * behavior (e.g. disable Claude transcript persistence, which breaks
 * resume).
 */
export const NESTED_SESSION_ENV_VARIABLES = [
  "CLAUDE_CODE_CHILD_SESSION",
  "CLAUDECODE",
  "CLAUDE_CODE_ENTRYPOINT",
  "CLAUDE_CODE_SSE_PORT",
  "CLAUDE_CODE_SESSION_ID",
  "CLAUDE_CODE_BRIDGE_SESSION_ID",
  "CLAUDE_PID",
] as const

/**
 * Normalized activity for one provider session. `unknown` means no evidence
 * — never a guess. A pending provider request always wins over the
 * provider's more general running state (`needs_input` beats `working`).
 */
export type AgentActivity = "idle" | "working" | "needs_input" | "unknown"

const ACTIVITY_PRECEDENCE: Record<AgentActivity, number> = {
  needs_input: 3,
  working: 2,
  unknown: 1,
  idle: 0,
}

/**
 * Reduce a session tree's activities (ATC-158): the root agent loop plus
 * every tracked background descendant (subagents, backgrounded shells,
 * workflows, pending session crons). Precedence is
 * `needs_input > working > unknown > idle`: any member needing user action
 * wins, any active member keeps the whole tree busy, and `idle` requires
 * EVERY member known-inactive — one unestablished member keeps the
 * aggregate `unknown`, never a guess.
 */
export const aggregateActivity = (
  root: AgentActivity,
  descendants: Iterable<AgentActivity>,
): AgentActivity => {
  let aggregate = root
  for (const activity of descendants) {
    if (ACTIVITY_PRECEDENCE[activity] > ACTIVITY_PRECEDENCE[aggregate]) aggregate = activity
  }
  return aggregate
}

export type AgentTurnOutcome = "completed" | "interrupted" | "failed"

/**
 * One entry of a TUI-driven session's observation feed (observeSession).
 * `userPrompt` is best-effort evidence of a user prompt submitted to the
 * session — adapters emit at least the session's first prompt when their
 * evidence source carries it (Claude: the UserPromptSubmit webhook; Codex:
 * a demand-driven thread/read of the session's preview), and consumers
 * must tolerate duplicates, later prompts, and absence.
 */
export type AgentSessionEvent =
  | { readonly type: "activity"; readonly activity: AgentActivity }
  | { readonly type: "userPrompt"; readonly text: string }

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

/** A resume launch, plus any adapter metadata the launch minted or rotated
 * — the caller must persist it, or state minted here (e.g. a hook secret)
 * dies with the process while the TUI keeps using it. */
export interface TuiLaunch {
  readonly launchSpec: TuiLaunchSpec
  readonly providerMetadata?: string
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
 * Verified identity of a freshly launched TUI session (ATC-124). The
 * optional metadata is an opaque adapter-owned blob the caller persists on
 * the thread and hands back on later seam calls (e.g. the Claude hook
 * secret); the domain never reads inside it.
 */
export interface EstablishedIdentity {
  readonly providerSessionId: string
  /** The verified canonical working directory the caller asked for. */
  readonly cwd: string
  readonly providerMetadata?: string
}

/**
 * What `prepareTuiSession` hands back: how to launch the fresh provider
 * TUI, and one awaitable resolving to the session's verified identity.
 * How identity is established is adapter-internal: Claude pre-assigns an
 * id and resolves immediately; Codex resolves when the launched TUI's
 * `thread/started` is captured (cwd-checked fail closed). Awaiting is the
 * caller's job and must be bounded by the caller.
 */
export interface PreparedTuiSession {
  readonly launchSpec: TuiLaunchSpec
  readonly identity: Effect.Effect<
    EstablishedIdentity,
    AgentUnavailable | AgentIdentityMismatch | AgentProtocolError
  >
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
  /**
   * How to launch the provider TUI attached to an existing session.
   * `providerMetadata` is deliberately required (pass undefined when the
   * thread has none): forgetting to thread it through would silently
   * rotate adapter state out from under a running TUI.
   *
   * A provider that can probe session existence (Codex, via thread/list)
   * does so here and fails `AgentResumeFailed` when the session is gone —
   * launching blind would die inside the pty with no typed error. A
   * provider that cannot probe (Claude: checkSession is hardwired
   * `unknown`) launches blind, so "the open succeeded but the terminal
   * died within seconds" remains a state clients must expect.
   */
  readonly tuiLaunch: (options: {
    readonly providerSessionId: string
    readonly cwd: string
    readonly providerMetadata: string | undefined
  }) => Effect.Effect<TuiLaunch, AgentUnavailable | AgentResumeFailed>
  /**
   * Prepare a FRESH provider TUI session in `cwd`: the uniform contract
   * behind "provider sessions materialize on first interaction". The Scope
   * owns the adapter's capture/serialization resources (Codex holds the
   * per-provider launch lock until the scope closes, so concurrent
   * launches cannot mis-adopt each other's identity); close it once
   * `identity` has resolved, or to abandon the launch.
   */
  readonly prepareTuiSession: (options: {
    readonly cwd: string
  }) => Effect.Effect<PreparedTuiSession, AgentUnavailable, Scope.Scope>
  /**
   * The one normalized per-session subscription for TUI-driven sessions:
   * activity plus best-effort user-prompt evidence (AgentSessionEvent).
   * Whether it is fed by hook webhooks (Claude) or the shared server's
   * status fan-out (Codex) is adapter-internal. Metadata is handed back so
   * adapters can restore per-session state (hook secrets) across ATC
   * restarts. Scoped: closing unsubscribes. The stream goes silent — never
   * lies — when the adapter loses its evidence source.
   */
  readonly observeSession: (options: {
    readonly providerSessionId: string
    readonly providerMetadata: string | undefined
  }) => Effect.Effect<Stream.Stream<AgentSessionEvent>, AgentUnavailable, Scope.Scope>
  /**
   * One bounded, session-less completion: a short display title for a
   * thread whose first user prompt is `prompt` (ATC-155). Runs as the
   * provider's cheapest ephemeral invocation — never a persisted provider
   * session, never a writer on any existing one. Returns the model's raw
   * text; callers apply `sanitizeTitle`. Failures are ordinary typed
   * errors — the caller decides that a missing title is not an error.
   */
  readonly generateTitle: (options: {
    readonly cwd: string
    readonly prompt: string
  }) => Effect.Effect<string, AgentUnavailable | AgentProtocolError>
  /**
   * Demand-driven reconciliation: the provider's current word on the
   * session's activity, `unknown` when it offers no evidence. Never
   * guesses; failures are the retryable AgentUnavailable.
   */
  readonly checkSession: (options: {
    readonly providerSessionId: string
  }) => Effect.Effect<AgentActivity, AgentUnavailable>
  /**
   * Release adapter-owned per-session resources (launch files, secret
   * registrations) when the owning thread is deleted. Best-effort and
   * idempotent — cleanup problems are logged, never surfaced.
   */
  readonly releaseSession: (options: {
    readonly providerSessionId: string
    readonly providerMetadata: string | undefined
  }) => Effect.Effect<void>
}

// Internal-only failures (ATC-123): not part of the HTTP contract, so no
// httpApiStatus annotations. The Threads domain maps them to the public
// tagged errors (threads.ts mapAgentError).

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

/**
 * The one thread-title instruction (ATC-155), shared by every adapter's
 * generateTitle so Claude and Codex titles read the same. The user prompt
 * is capped so a pasted wall of text cannot blow up the one-shot's cost —
 * the opening lines are what a title needs.
 */
export const titleInstruction = (prompt: string): string =>
  [
    "You write concise titles for coding-agent conversation threads.",
    "Reply with the title only: a single line of plain text.",
    "Rules:",
    "- Summarize the user's request; never answer it or restate it verbatim.",
    "- Make the descriptive part 3-8 words, specific over generic.",
    "- Use sentence case, not title case: capitalize only the first word and proper nouns.",
    "- No quotes, no markdown, no trailing punctuation.",
    "- When the request clearly matches one category below, prefix the title with its exact label followed by ' - ':",
    "  - Build: create, implement, change, or fix something.",
    "  - Review: review or critique existing code or work.",
    "  - Grill: explicitly grill or stress-test a plan or design.",
    "  - Explore: brainstorm, research, compare, or understand options.",
    "  - Investigate: diagnose a suspected bug or problem without asking for a fix.",
    "- If no category clearly matches, omit the prefix.",
    "",
    "The user's first message:",
    prompt.length > 4_000 ? `${prompt.slice(0, 4_000)}…` : prompt,
  ].join("\n")

const WRAPPING_QUOTES = [
  ['"', '"'],
  ["'", "'"],
  ["“", "”"],
  ["‘", "’"],
  ["`", "`"],
] as const

/**
 * Output hygiene for generated titles, enforced in code rather than trusted
 * to the prompt (T3Code/OpenCode precedent): first non-empty line only,
 * whitespace collapsed, wrapping quotes stripped, trailing punctuation
 * dropped, capped at ~50 characters on a word boundary. Returns null when
 * nothing usable remains — the caller gives up silently.
 */
export const sanitizeTitle = (raw: string): string | null => {
  const line = raw
    .split("\n")
    .map((candidate) => candidate.trim())
    .find((candidate) => candidate !== "")
  if (line === undefined) return null
  let title = line.replace(/\s+/g, " ").trim()
  for (let stripped = true; stripped;) {
    stripped = false
    for (const [open, close] of WRAPPING_QUOTES) {
      if (title.length >= 2 && title.startsWith(open) && title.endsWith(close)) {
        title = title.slice(1, -1).trim()
        stripped = true
      }
    }
  }
  title = title.replace(/[.,;:!?…]+$/, "").trim()
  if (title.length > 50) {
    const cut = title.slice(0, 50)
    const lastSpace = cut.lastIndexOf(" ")
    title = (lastSpace > 20 ? cut.slice(0, lastSpace) : cut).trimEnd()
  }
  return title === "" ? null : title
}

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
 * Read `<executable> --version` and extract an x.y.z version string;
 * null when the version cannot be determined (any spawn or parse failure).
 */
export const readInstalledVersion = (
  subprocess: Subprocess["Service"],
  executable: string,
): Effect.Effect<string | null> =>
  Effect.scoped(
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
  ).pipe(
    // Bounded: a wedged binary must not hang a demand-driven registry read.
    Effect.timeoutOrElse({ duration: "5 seconds", orElse: () => Effect.succeed("") }),
    Effect.orElseSucceed(() => ""),
    Effect.map(
      (output) =>
        output
          .match(/(\d+)\.(\d+)\.(\d+)/)
          ?.slice(1, 4)
          .join(".") ?? null,
    ),
  )

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
    const installed = yield* readInstalledVersion(subprocess, executable)
    if (installed === null) {
      return yield* Effect.logWarning(
        `could not determine the installed ${provider} version (tested against ${testedVersion})`,
      )
    }
    // Components are small; packing keeps the comparison one expression.
    const pack = (version: string) =>
      version
        .split(".")
        .map(Number)
        .reduce((total, part) => total * 1_000 + part, 0)
    if (pack(installed) < pack(testedVersion)) {
      yield* Effect.logWarning(
        `installed ${provider} ${installed} is older than the tested ${testedVersion}; proceeding, but upgrade it if provider behavior misbehaves`,
      )
    }
  })
}
