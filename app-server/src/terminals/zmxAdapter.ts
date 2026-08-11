import { Effect, FileSystem, Layer, Schema, Stream } from "effect"
import type { Duration } from "effect"
import * as path from "node:path"
import { AppConfig, CONTEXT_VARIABLES } from "../platform/config.ts"
import * as Subprocess from "../platform/subprocess.ts"
import { ZmxUnavailable } from "../api/contract.ts"
import {
  LONGEST_SESSION_NAME,
  SessionNotFound,
  SessionOperationFailed,
  TerminalAdapter,
} from "./terminalAdapter.ts"
import type { SessionInfo } from "./terminalAdapter.ts"

// The zmx implementation of the TerminalAdapter seam (ATC-122), built on the
// pinned zmx v0.6.0 behavior (repos/zmx):
//
//   - Every zmx child gets ATC's private ZMX_DIR (only ATC sessions live
//     there) and a scrubbed environment: ZMX_SESSION would turn `attach`
//     into a session switch whose error path can kill the target session,
//     and ZMX_SESSION_PREFIX silently rewrites every name.
//   - `attach`/`run` auto-create, so attach pre-flights the inventory and
//     verifies (by daemon pid) that it reached the original session, never
//     a silently resurrected one.
//   - Exit codes are not existence proof, and `kill` returns before death
//     (~500ms SIGHUP→SIGKILL); absence is verified by polling the inventory.
//   - `zmx list` parsing is tolerant: malformed lines are skipped, `err=`
//     lines count as existing-but-unreachable. Only a *reachable* entry
//     proves a live session; only a complete inventory proves absence.
//
// Manual debugging against the same inventory: `ZMX_DIR=<stateDir>/terminals
// zmx list`.

const SESSION_TERM_TYPE = "xterm-256color"
/** Longest usable unix socket path (sun_path minus the NUL terminator). */
const MAX_SOCKET_PATH_BYTES = process.platform === "linux" ? 107 : 103
/** Bound on a single zmx command; beyond it the inventory is unavailable. */
const RUN_TIMEOUT: Duration.Input = "10 seconds"

/**
 * Inventory-polling bounds; tests tighten them, production uses defaults.
 * Verification counts complete inventory passes, not wall time, so a slow
 * inventory extends the wait instead of manufacturing a conclusive failure.
 */
export interface ZmxOptions {
  readonly pollInterval?: Duration.Input
  readonly verifyPasses?: number
}

/** Boot-time misconfiguration of the adapter; fails serve, names the fix. */
export class ZmxConfigError extends Schema.TaggedErrorClass<ZmxConfigError>()("ZmxConfigError", {
  reason: Schema.String,
}) {
  override get message(): string {
    return this.reason
  }
}

/**
 * Parse `zmx list` output. Lines are tab-separated `key=value` fields after
 * an optional reachability marker; lines without a `name` are skipped so one
 * broken entry never fails the whole inventory. `err=` entries exist but did
 * not answer the probe.
 */
export const parseSessionList = (stdout: ReadonlyArray<string>): Array<SessionInfo> => {
  const sessions: Array<SessionInfo> = []
  for (const raw of stdout) {
    const line = raw.trim().replace(/^→\s*/, "")
    if (line === "") continue
    const fields = new Map<string, string>()
    for (const field of line.split("\t")) {
      const separator = field.indexOf("=")
      if (separator > 0) fields.set(field.slice(0, separator), field.slice(separator + 1))
    }
    const name = fields.get("name")
    if (name === undefined || name === "") continue
    const pid = Number.parseInt(fields.get("pid") ?? "", 10)
    sessions.push({
      name,
      reachable: !fields.has("err"),
      ...(Number.isNaN(pid) ? {} : { pid }),
    })
  }
  return sessions
}

/**
 * The SpawnSpec env fragment for every zmx child — composed by Subprocess,
 * the one place child environments are built: the parent environment with
 * the mandatory scrubs applied, ATC's private socket directory pinned, and
 * this server's ATC_ENDPOINT injected so agents inside launched sessions
 * can reach the API (ATC-131 context propagation — no secrets, just the
 * URL). Every inherited ATC_* context variable is scrubbed: a server
 * running inside another ATC session would otherwise leak that session's
 * ids into every terminal it launches, and stale context is worse than
 * none.
 */
export const zmxSpawnEnv = (socketDir: string, endpoint: string) => ({
  env: {
    ZMX_DIR: socketDir,
    TERM: SESSION_TERM_TYPE,
    ATC_ENDPOINT: endpoint,
  },
  extendEnv: true,
  unsetEnv: ["ZMX_SESSION", "ZMX_SESSION_PREFIX", ...CONTEXT_VARIABLES],
})

/** Printable diagnostic from raw terminal output: control bytes stripped. */
const printableTail = (tail: string): string =>
  tail.replace(/\x1b\[[0-9;?]*[a-zA-Z]|[\x00-\x09\x0b-\x1f]/g, "").trim()

export const layerWith = (options: ZmxOptions) =>
  Layer.effect(TerminalAdapter)(
    Effect.gen(function* () {
      const pollInterval = options.pollInterval ?? "250 millis"
      const verifyPasses = options.verifyPasses ?? 20
      const config = yield* AppConfig
      const subprocess = yield* Subprocess.Subprocess
      const fs = yield* FileSystem.FileSystem

      const socketDir = config.terminalSocketDir
      // Socket paths must fit sun_path; validated at boot so a deep state
      // dir fails serve with one actionable line, not on every create.
      const longestSocketPath = path.join(socketDir, LONGEST_SESSION_NAME)
      if (Buffer.byteLength(longestSocketPath) > MAX_SOCKET_PATH_BYTES) {
        return yield* Effect.fail(
          new ZmxConfigError({
            reason:
              `terminal socket directory ${socketDir} is too deep: socket paths would exceed ` +
              `${MAX_SOCKET_PATH_BYTES} bytes; move the state directory (XDG_STATE_HOME)`,
          }),
        )
      }
      // Private: sockets grant full read/write on the user's terminals.
      // mkdir's mode only applies when it creates the directory, so a
      // pre-existing permissive one is explicitly tightened.
      yield* fs.makeDirectory(socketDir, { recursive: true, mode: 0o700 })
      yield* fs.chmod(socketDir, 0o700)

      const spawnEnv = zmxSpawnEnv(socketDir, `http://127.0.0.1:${config.port}`)

      // Explicit resolution, never implicit (the shared resolveExecutable
      // rule), memoized on success — a resolved install does not move mid-run.
      let resolvedExecutable: string | undefined
      const resolveZmxExecutable = Effect.suspend(() => {
        if (resolvedExecutable !== undefined) return Effect.succeed(resolvedExecutable)
        const configured = config.zmxExecutable
        const resolved = Subprocess.resolveExecutable(configured)
        if (resolved !== null) {
          resolvedExecutable = resolved
          return Effect.succeed(resolved)
        }
        return Effect.fail(
          new ZmxUnavailable({
            reason:
              `zmx executable "${configured}" not found; install zmx on PATH or set ` +
              `zmxExecutable in config.toml / ATC_ZMX_EXECUTABLE`,
          }),
        )
      })

      const unavailable = (error: Subprocess.SubprocessError): ZmxUnavailable =>
        new ZmxUnavailable({
          reason: `cannot run zmx (${error.operation}): ${error.message}`,
        })

      /** Run a run-to-completion zmx command, capturing stdout + exit code. */
      const runZmx = (args: ReadonlyArray<string>) =>
        Effect.gen(function* () {
          const executable = yield* resolveZmxExecutable
          const child = yield* subprocess.spawn({ executable, args, ...spawnEnv })
          yield* child.endInput
          const stdout = yield* Stream.runCollect(child.stdoutLines)
          const exitCode = yield* child.exitCode
          const stderr = yield* child.stderrTail
          return { stdout, exitCode, stderr }
        }).pipe(
          Effect.scoped,
          Effect.catchTag("SubprocessError", (e) => Effect.fail(unavailable(e))),
          // A hung zmx must not hang its caller; slowness is unavailability.
          Effect.timeoutOrElse({
            duration: RUN_TIMEOUT,
            orElse: () => Effect.fail(new ZmxUnavailable({ reason: `zmx ${args[0]} timed out` })),
          }),
        )

      const listSessions = () =>
        runZmx(["list"]).pipe(
          Effect.flatMap(({ exitCode, stderr, stdout }) =>
            // "no sessions" is exit 0 with empty stdout (a note on stderr);
            // any non-zero exit means the inventory is not trustworthy.
            exitCode === 0
              ? Effect.succeed(parseSessionList(stdout))
              : Effect.fail(
                  new ZmxUnavailable({
                    reason: `zmx list exited with code ${exitCode}: ${stderr.join(" ") || "(no stderr)"}`,
                  }),
                ),
          ),
        )

      const findSession = (name: string) =>
        listSessions().pipe(Effect.map((sessions) => sessions.find((s) => s.name === name)))

      /**
       * Up to `verifyPasses` complete inventory passes until `predicate`
       * accepts one; resolves to the accepted inventory (so callers can read
       * it without another `zmx list`), or undefined if none was accepted.
       */
      const pollInventory = (predicate: (sessions: ReadonlyArray<SessionInfo>) => boolean) =>
        Effect.gen(function* () {
          for (let pass = 0; pass < verifyPasses; pass++) {
            if (pass > 0) yield* Effect.sleep(pollInterval)
            const sessions = yield* listSessions()
            if (predicate(sessions)) return sessions
          }
          return undefined
        })

      const liveSession = (name: string) => (sessions: ReadonlyArray<SessionInfo>) =>
        sessions.some((s) => s.name === name && s.reachable)

      const createSession: TerminalAdapter["Service"]["createSession"] = (options) =>
        Effect.gen(function* () {
          const executable = yield* resolveZmxExecutable
          const fail = (reason: string) =>
            Effect.fail(
              new SessionOperationFailed({
                operation: "create",
                sessionName: options.name,
                reason,
              }),
            )

          // A bad working directory is the caller's conclusive error, not
          // multiplexer unavailability (the spawn would report a confusing
          // retryable ENOENT).
          const cwdInfo = yield* fs
            .stat(options.cwd)
            .pipe(Effect.catch(() => fail(`working directory ${options.cwd} does not exist`)))
          if (cwdInfo.type !== "Directory") {
            return yield* fail(`working directory ${options.cwd} is not a directory`)
          }

          // Creating is never silently attaching: `zmx attach` on an
          // existing name ignores cwd and command entirely. An unreachable
          // leftover with this name blocks creation the same way — it holds
          // the socket path.
          if ((yield* findSession(options.name)) !== undefined) {
            return yield* fail("session already exists")
          }

          // The short-lived creation client: a PTY running `zmx attach
          // <name> [command…]` (attach auto-creates). Once a *reachable*
          // session is in the inventory the scope closes, detaching the
          // client; the session daemon persists. Exec-style commands come
          // from a Schema-typed argv — never a shell string.
          // Per-session overlay on the base fragment; an undefined value
          // removes the key from the *inherited* environment (TUI terminals
          // scrub nested-session markers this way) — the pinned base keys
          // above always win.
          const overlay = Object.entries(options.env ?? {})
          const sessionEnv = {
            env: {
              ...spawnEnv.env,
              ...Object.fromEntries(overlay.filter(([, value]) => value !== undefined)),
            },
            extendEnv: true,
            unsetEnv: [
              ...spawnEnv.unsetEnv,
              ...overlay.filter(([, value]) => value === undefined).map(([key]) => key),
            ],
          }
          const client = yield* Effect.gen(function* () {
            const child = yield* subprocess
              .spawnPty({
                executable,
                args: ["attach", options.name, ...(options.command ?? [])],
                cwd: options.cwd,
                ...sessionEnv,
                // The tail is the launch-failure diagnostic below.
                captureOutputTail: true,
              })
              .pipe(Effect.catchTag("SubprocessError", (e) => Effect.fail(unavailable(e))))
            const settled = yield* Effect.raceFirst(
              pollInventory(liveSession(options.name)),
              // A client that dies first (bad command, zmx error) ends the
              // wait early; the final check below stays the authority.
              child.exitCode.pipe(
                Effect.as(undefined),
                Effect.catchTag("SubprocessError", () => Effect.succeed(undefined)),
              ),
            )
            return { settled: settled !== undefined, tail: yield* child.outputTail }
          }).pipe(Effect.scoped)

          if (client.settled) return
          // The client is gone (exited or detached by the closing scope).
          // One final inventory check is the authority: the session may have
          // settled in the instant the client died, but a session that is
          // not reachable now is not a live terminal.
          if (liveSession(options.name)(yield* listSessions())) return
          const detail = printableTail(client.tail)
          return yield* fail(`the session never settled${detail === "" ? "" : `: ${detail}`}`)
        })

      const killSession: TerminalAdapter["Service"]["killSession"] = (name) =>
        Effect.gen(function* () {
          // Killing an absent session succeeds: the goal state holds.
          if ((yield* findSession(name)) === undefined) return
          // Exit code deliberately ignored: kill's codes are not existence
          // proof. The inventory is the only authority.
          yield* runZmx(["kill", name])
          const gone = yield* pollInventory((sessions) => !sessions.some((s) => s.name === name))
          if (gone === undefined) {
            return yield* Effect.fail(
              new SessionOperationFailed({
                operation: "kill",
                sessionName: name,
                reason: `session still present after ${verifyPasses} inventory passes`,
              }),
            )
          }
        })

      const attachSession: TerminalAdapter["Service"]["attachSession"] = (name, size) =>
        Effect.gen(function* () {
          // Pre-flight: attach auto-creates, so absence must be checked
          // first, and only a reachable session can be attached (attaching
          // an unreachable one would clean its stale socket and resurrect).
          const before = yield* findSession(name)
          if (before === undefined) {
            return yield* Effect.fail(new SessionNotFound({ sessionName: name }))
          }
          if (!before.reachable) {
            return yield* Effect.fail(
              new SessionOperationFailed({
                operation: "attach",
                sessionName: name,
                reason: "session exists but is unreachable; retry",
              }),
            )
          }
          const executable = yield* resolveZmxExecutable
          const child = yield* subprocess
            .spawnPty({
              executable,
              args: ["attach", name],
              ...spawnEnv,
              cols: size.cols,
              rows: size.rows,
            })
            .pipe(Effect.catchTag("SubprocessError", (e) => Effect.fail(unavailable(e))))

          // TOCTOU guard: if the session died between pre-flight and spawn,
          // attach silently created a fresh one. The daemon pid is the
          // session's identity — a changed pid means a resurrected phantom,
          // which is killed and reported as the original being gone.
          const settled = yield* pollInventory(liveSession(name))
          const after = settled?.find((s) => s.name === name)
          if (after === undefined || after.pid !== before.pid) {
            yield* runZmx(["kill", name]).pipe(Effect.ignore)
            return yield* Effect.fail(new SessionNotFound({ sessionName: name }))
          }

          return {
            output: child.output,
            write: child.write,
            resize: child.resize,
            exitCode: child.exitCode,
          }
        })

      return { createSession, listSessions, killSession, attachSession }
    }),
  )

/** Production adapter with the default polling bounds. */
export const layer = layerWith({})
