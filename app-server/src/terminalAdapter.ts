import { Context, Schema } from "effect"
import type { Effect, Scope, Stream } from "effect"
import type { ZmxUnavailable } from "./api.ts"
import type { SubprocessError } from "./subprocess.ts"

// The narrow seam through which ATC talks to the terminal multiplexer
// (ATC-122). The adapter deals only in derived session names and terminal
// bytes; Terminal records, reconciliation policy, and transports live above
// it. Confining multiplexer invocation here keeps it swappable and gives
// tests a fake-adapter seam (test/fakeTerminalAdapter.ts).

/** Prefix marking a zmx session as ATC-owned inside ATC's private ZMX_DIR. */
export const SESSION_NAME_PREFIX = "atc-"

/**
 * The derived (never persisted) session name for a terminal id:
 * `atc-` + the UUID's 32 hex chars — reversible for debugging (strip the
 * prefix, re-insert the dashes).
 */
export const sessionNameForTerminalId = (terminalId: string): string =>
  SESSION_NAME_PREFIX + terminalId.replaceAll("-", "").toLowerCase()

/** The longest name the derivation produces — the socket-path length guard. */
export const LONGEST_SESSION_NAME = sessionNameForTerminalId("00000000-0000-0000-0000-000000000000")

/**
 * One entry of a complete session inventory. `reachable: false` marks a
 * session whose daemon did not answer the multiplexer's probe — it exists,
 * so it must never be treated as absent.
 */
export interface SessionInfo {
  readonly name: string
  readonly reachable: boolean
  /** Daemon pid — the session's identity across a name reuse. */
  readonly pid?: number
}

export interface SessionSize {
  readonly cols: number
  readonly rows: number
}

/** A live, bidirectional attach client bound to one session. */
export interface SessionConnection {
  /** Raw terminal output. Single-consumer; ends when the client detaches. */
  readonly output: Stream.Stream<Uint8Array, SubprocessError>
  /** Raw terminal input (keystrokes/bytes) to the session. */
  readonly write: (data: Uint8Array | string) => Effect.Effect<void, SubprocessError>
  readonly resize: (size: SessionSize) => Effect.Effect<void, SubprocessError>
  /** Await this attach client's exit (never the session's). */
  readonly exitCode: Effect.Effect<number, SubprocessError>
}

// `ZmxUnavailable` (retryable: the multiplexer cannot be consulted, stored
// state must stay untouched) lives in the contract (api.ts) — the API
// surfaces it directly.

/** A session operation failed conclusively (with diagnostics). */
export class SessionOperationFailed extends Schema.TaggedErrorClass<SessionOperationFailed>()(
  "SessionOperationFailed",
  {
    operation: Schema.Literals(["create", "kill", "attach"]),
    sessionName: Schema.String,
    reason: Schema.String,
  },
) {
  override get message(): string {
    return `zmx ${this.operation} ${this.sessionName}: ${this.reason}`
  }
}

/** Attach pre-flight: the session is not in a complete inventory. */
export class SessionNotFound extends Schema.TaggedErrorClass<SessionNotFound>()("SessionNotFound", {
  sessionName: Schema.String,
}) {
  override get message(): string {
    return `no session named ${this.sessionName}`
  }
}

export class TerminalAdapter extends Context.Service<
  TerminalAdapter,
  {
    /**
     * Create the persistent session `name` (working directory `cwd`,
     * exec-style `command` argv, or an interactive login shell when absent)
     * and return once it is settled and detached. Succeeds only when a
     * reachable session is confirmed in the inventory; fails when `name`
     * already exists (creating is never silently attaching), and a command
     * that finishes before the session settles is a failed launch.
     */
    readonly createSession: (options: {
      readonly name: string
      readonly cwd: string
      readonly command?: ReadonlyArray<string> | undefined
    }) => Effect.Effect<void, ZmxUnavailable | SessionOperationFailed>
    /**
     * A complete inventory of ATC's private session directory. Any failure
     * is `ZmxUnavailable` — a partial listing never masquerades as absence.
     */
    readonly listSessions: () => Effect.Effect<ReadonlyArray<SessionInfo>, ZmxUnavailable>
    /**
     * Kill `name` and verify its absence from the inventory (the multiplexer
     * returns before death, and its exit codes are not existence proof).
     * Killing an absent session succeeds — the goal state already holds.
     */
    readonly killSession: (
      name: string,
    ) => Effect.Effect<void, ZmxUnavailable | SessionOperationFailed>
    /**
     * Attach to the existing session `name`. Pre-flights the inventory
     * (attach would otherwise auto-create). The connection lives in the
     * Scope; closing it detaches the client, never the session.
     */
    readonly attachSession: (
      name: string,
      size: SessionSize,
    ) => Effect.Effect<
      SessionConnection,
      ZmxUnavailable | SessionNotFound | SessionOperationFailed,
      Scope.Scope
    >
  }
>()("app-server/TerminalAdapter") {}
