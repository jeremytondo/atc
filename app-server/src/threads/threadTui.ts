import { Context, Effect, Layer, Option } from "effect"
import type { AgentAdapter, TuiLaunchSpec } from "../agents/agentAdapter.ts"
import { NESTED_SESSION_ENV_VARIABLES } from "../agents/agentAdapter.ts"
import { ProviderSessionConflict, ProviderUnavailable, ThreadNotFound } from "../api/contract.ts"
import type {
  DirectoryCheckTimedOut,
  DirectoryUnavailable,
  TerminalLaunchFailed,
  ZmxUnavailable,
} from "../api/contract.ts"
import { Terminals } from "../terminals/terminals.ts"
import type { Terminal } from "../terminals/terminals.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"

// A tui thread's terminal (ATC-124): the one place a Terminal is found,
// launched, or ended on a thread's behalf, shared by the Threads domain
// (open, archive, delete) and the ThreadObserver (ending a TUI at its
// observed idle) so both launch and end a TUI the same way. Invariants:
//
//   - Every launch scrubs the nested-session markers uniformly
//     (agentAdapter.ts NESTED_SESSION_ENV_VARIABLES): inherited from a
//     dev-mode ATC running inside an agent session, they silently change
//     provider behavior.
//   - `reopen` is the confirmed-session relaunch: the adapter's launch spec
//     against the thread's exact persisted identity, and any metadata the
//     launch minted or rotated persisted BEFORE the terminal exists — a
//     TUI must never run on state that dies with the process. A thread
//     deleted mid-launch adopts nothing (ThreadNotFound).
//   - `end` follows the terminals confirmed-kill rules: a terminal that
//     vanished since the lookup is already the goal state, and a kill the
//     multiplexer cannot verify fails ZmxUnavailable — the caller decides
//     whether that blocks it.

export class ThreadTui extends Context.Service<
  ThreadTui,
  {
    /** The thread's live linked TUI terminal, from the reconciled inventory
     * — a terminal that just died stops being linked the moment the
     * inventory says so. */
    readonly linked: (record: ThreadRecord) => Effect.Effect<Terminal | undefined>
    /** Launch a TUI terminal from an adapter launch spec. */
    readonly create: (
      record: ThreadRecord,
      spec: TuiLaunchSpec,
    ) => Effect.Effect<
      Terminal,
      DirectoryUnavailable | DirectoryCheckTimedOut | ZmxUnavailable | TerminalLaunchFailed
    >
    /** Relaunch the TUI on the thread's confirmed session; returns the
     * terminal and the record as persisted (metadata may have rotated). */
    readonly reopen: (
      record: ThreadRecord,
      adapter: AgentAdapter,
    ) => Effect.Effect<
      { readonly terminal: Terminal; readonly record: ThreadRecord },
      | ThreadNotFound
      | ProviderUnavailable
      | ProviderSessionConflict
      | DirectoryUnavailable
      | DirectoryCheckTimedOut
      | ZmxUnavailable
      | TerminalLaunchFailed
    >
    /** Kill the terminal (its zmx session, hence the TUI process) and
     * remove its record; already gone is success. */
    readonly end: (terminalId: string) => Effect.Effect<void, ZmxUnavailable>
  }
>()("app-server/ThreadTui") {}

export const layer = Layer.effect(ThreadTui)(
  Effect.gen(function* () {
    const terminals = yield* Terminals
    const repository = yield* ThreadRepository

    const create: ThreadTui["Service"]["create"] = (record, spec) =>
      terminals
        .create({
          projectId: record.projectId,
          threadId: record.id,
          ...(record.name !== undefined ? { name: record.name } : {}),
          command: spec.command,
          workingDirectory: record.workingDirectory,
          env: {
            ...spec.env,
            ...Object.fromEntries(NESTED_SESSION_ENV_VARIABLES.map((name) => [name, undefined])),
          },
        })
        // The owning project cannot vanish under a live thread (FK).
        .pipe(Effect.catchTag("ProjectNotFound", (error) => Effect.die(error)))

    return {
      linked: (record) =>
        terminals
          .list({ threadId: record.id })
          .pipe(Effect.map((list) => list.find((terminal) => terminal.status === "live"))),
      create,
      reopen: (record, adapter) =>
        Effect.gen(function* () {
          const providerSessionId = record.providerSessionId ?? ""
          const launch = yield* adapter
            .tuiLaunch({
              providerSessionId,
              cwd: record.workingDirectory,
              providerMetadata: record.providerMetadata,
            })
            .pipe(
              Effect.mapError((error) =>
                error._tag === "AgentUnavailable"
                  ? new ProviderUnavailable({ agentId: record.agentId, reason: error.message })
                  : new ProviderSessionConflict({ threadId: record.id, reason: error.message }),
              ),
            )
          // Deleted mid-open: adopt nothing (the metadata write would die
          // on the vanished row, the terminal would violate the FK).
          const still = yield* repository.get(record.id)
          if (Option.isNone(still)) {
            return yield* Effect.fail(new ThreadNotFound({ threadId: record.id }))
          }
          const current =
            launch.providerMetadata !== undefined &&
            launch.providerMetadata !== record.providerMetadata
              ? yield* repository.setProviderMetadata(record.id, launch.providerMetadata)
              : record
          const terminal = yield* create(current, launch.launchSpec)
          return { terminal, record: current }
        }),
      end: (terminalId) =>
        terminals.delete(terminalId).pipe(Effect.catchTag("TerminalNotFound", () => Effect.void)),
    }
  }),
)
