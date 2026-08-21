import { BunTerminal } from "@effect/platform-bun"
import { Effect, Queue, Ref } from "effect"
import type * as Terminal from "effect/Terminal"
import * as AppServer from "./appServer.ts"
import * as Zmx from "./zmx.ts"
import * as View from "./view.ts"

// Application coordinator: one scoped SSE subscription maintains an
// authoritative snapshot. The manager releases its terminal scope before a
// direct zmx client inherits the TTY, then starts fresh when zmx returns.

interface ManagerState {
  readonly selectedThreadId?: string | undefined
  readonly status?: string | undefined
}

type ManagerResult =
  | { readonly type: "quit"; readonly selectedThreadId?: string | undefined }
  | { readonly type: "attach"; readonly threadId: string }

const describeError = (error: unknown): string =>
  error instanceof Error && error.message !== "" ? error.message : String(error)

const renderManager = (
  terminal: Terminal.Terminal,
  server: AppServer.AppServer["Service"],
  snapshotRef: Ref.Ref<AppServer.Snapshot | undefined>,
  reachabilityRef: Ref.Ref<View.Reachability>,
  backgroundStatusRef: Ref.Ref<string | undefined>,
  state: ManagerState,
): Effect.Effect<void, unknown> =>
  Effect.gen(function* () {
    const snapshot = yield* Ref.get(snapshotRef)
    const reachability = yield* Ref.get(reachabilityRef)
    const backgroundStatus = yield* Ref.get(backgroundStatusRef)
    const columns = yield* terminal.columns
    const rows = yield* terminal.rows
    yield* terminal.display(
      View.render({
        endpoint: server.config.endpoint,
        reachability,
        snapshot,
        state: {
          selectedThreadId: View.normalizeSelection(snapshot, state.selectedThreadId),
          status: state.status ?? backgroundStatus,
        },
        columns,
        rows,
      }),
    )
  })

const managerInput = (
  input: Terminal.UserInput,
  snapshot: AppServer.Snapshot | undefined,
  state: ManagerState,
):
  | { readonly type: "state"; readonly state: ManagerState }
  | { readonly type: "quit" }
  | { readonly type: "attach"; readonly threadId: string }
  | { readonly type: "refresh"; readonly state: ManagerState } => {
  const name = input.key.name
  if (input.key.ctrl && name === "c") return { type: "quit" }
  if (name === "q") return { type: "quit" }
  if (name === "up" || name === "k") {
    return {
      type: "state",
      state: {
        ...state,
        selectedThreadId: View.moveSelection(snapshot, state.selectedThreadId, -1),
        status: undefined,
      },
    }
  }
  if (name === "down" || name === "j") {
    return {
      type: "state",
      state: {
        ...state,
        selectedThreadId: View.moveSelection(snapshot, state.selectedThreadId, 1),
        status: undefined,
      },
    }
  }
  if (name === "r") {
    return { type: "refresh", state: { ...state, status: "Refreshing…" } }
  }
  if (name === "return" || name === "enter") {
    const threadId = View.normalizeSelection(snapshot, state.selectedThreadId)
    return threadId === undefined
      ? {
          type: "state",
          state: { ...state, status: "There is no active Thread to attach." },
        }
      : { type: "attach", threadId }
  }
  return { type: "state", state }
}

const runManager = (
  server: AppServer.AppServer["Service"],
  snapshotRef: Ref.Ref<AppServer.Snapshot | undefined>,
  reachabilityRef: Ref.Ref<View.Reachability>,
  backgroundStatusRef: Ref.Ref<string | undefined>,
  uiUpdates: Queue.Dequeue<void>,
  refreshRequests: Queue.Enqueue<void>,
  initial: ManagerState,
): Effect.Effect<ManagerResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const terminal = yield* BunTerminal.make(() => false)
      const inputs = yield* terminal.readInput
      yield* terminal.display("\x1b[?1049h\x1b[2J\x1b[H")
      yield* Effect.addFinalizer(() =>
        terminal.display("\x1b[?25h\x1b[0m\x1b[?1049l").pipe(Effect.orDie),
      )

      const loop = (state: ManagerState): Effect.Effect<ManagerResult, unknown> =>
        renderManager(
          terminal,
          server,
          snapshotRef,
          reachabilityRef,
          backgroundStatusRef,
          state,
        ).pipe(
          Effect.andThen(
            Effect.race(
              Queue.take(inputs).pipe(
                Effect.map((input) => ({ type: "input" as const, input })),
                Effect.catch(() => Effect.succeed({ type: "closed" as const })),
              ),
              Queue.take(uiUpdates).pipe(Effect.as({ type: "update" as const })),
            ),
          ),
          Effect.flatMap((event) => {
            if (event.type === "closed") {
              return Effect.succeed({
                type: "quit",
                selectedThreadId: state.selectedThreadId,
              } as const)
            }
            if (event.type === "update") {
              return Ref.get(snapshotRef).pipe(
                Effect.flatMap((snapshot) =>
                  loop({
                    selectedThreadId: View.normalizeSelection(snapshot, state.selectedThreadId),
                  }),
                ),
              )
            }

            return Ref.get(snapshotRef).pipe(
              Effect.flatMap((snapshot) => {
                const action = managerInput(event.input, snapshot, state)
                if (action.type === "quit") {
                  return Effect.succeed({
                    type: "quit",
                    selectedThreadId: state.selectedThreadId,
                  } as const)
                }
                if (action.type === "attach") return Effect.succeed(action)
                if (action.type === "refresh") {
                  return Queue.offer(refreshRequests, void 0).pipe(
                    Effect.andThen(loop(action.state)),
                  )
                }
                return loop(action.state)
              }),
            )
          }),
        )

      return yield* loop(initial)
    }),
  )

const startLiveState = (
  server: AppServer.AppServer["Service"],
  snapshotRef: Ref.Ref<AppServer.Snapshot | undefined>,
  reachabilityRef: Ref.Ref<View.Reachability>,
  backgroundStatusRef: Ref.Ref<string | undefined>,
  refreshRequests: Queue.Queue<void>,
  uiUpdates: Queue.Queue<void>,
) =>
  Effect.gen(function* () {
    const refresh = Effect.forever(
      Queue.take(refreshRequests).pipe(
        Effect.andThen(Effect.sleep("100 millis")),
        Effect.andThen(Queue.poll(refreshRequests)),
        Effect.andThen(server.snapshot),
        Effect.tap((snapshot) => Ref.set(snapshotRef, snapshot)),
        Effect.tap(() => Ref.set(backgroundStatusRef, undefined)),
        Effect.catch((error) =>
          Ref.set(backgroundStatusRef, `Refresh failed: ${describeError(error)}`),
        ),
        Effect.andThen(Queue.offer(uiUpdates, void 0)),
      ),
    )
    yield* Effect.forkScoped(refresh)

    yield* Effect.forkScoped(
      server.subscribe((signal) => {
        if (signal.type === "connected") {
          return Ref.set(reachabilityRef, "connected").pipe(
            Effect.andThen(Queue.offer(refreshRequests, void 0)),
            Effect.andThen(Queue.offer(uiUpdates, void 0)),
          )
        }
        if (signal.type === "disconnected") {
          const recordFailure =
            signal.reason === undefined
              ? Effect.void
              : Ref.set(backgroundStatusRef, `Connection failed: ${signal.reason}`)
          return recordFailure.pipe(
            Effect.andThen(Ref.set(reachabilityRef, "disconnected")),
            Effect.andThen(Queue.offer(uiUpdates, void 0)),
          )
        }
        return Queue.offer(refreshRequests, void 0)
      }),
    )
  })

export const run = Effect.scoped(
  Effect.gen(function* () {
    if (process.stdin.isTTY !== true || process.stdout.isTTY !== true) {
      return yield* Effect.fail(new Error("an interactive terminal is required"))
    }

    const server = yield* AppServer.AppServer
    const zmx = yield* Zmx.Zmx
    const snapshotRef = yield* Ref.make<AppServer.Snapshot | undefined>(undefined)
    const reachabilityRef = yield* Ref.make<View.Reachability>("connecting")
    const backgroundStatusRef = yield* Ref.make<string | undefined>(undefined)
    const refreshRequests = yield* Queue.sliding<void>(1)
    const uiUpdates = yield* Queue.sliding<void>(1)
    yield* startLiveState(
      server,
      snapshotRef,
      reachabilityRef,
      backgroundStatusRef,
      refreshRequests,
      uiUpdates,
    )

    const loop = (
      selectedThreadId: string | undefined,
      status: string | undefined,
    ): Effect.Effect<void, unknown> =>
      runManager(
        server,
        snapshotRef,
        reachabilityRef,
        backgroundStatusRef,
        uiUpdates,
        refreshRequests,
        { selectedThreadId, status },
      ).pipe(
        Effect.flatMap((result) => {
          if (result.type === "quit") return Effect.void

          return server.openThread(result.threadId).pipe(
            Effect.flatMap((terminal) => zmx.attach(terminal)),
            Effect.as("Returned from zmx; session state refreshed."),
            Effect.catch((error) => Effect.succeed(`Could not attach: ${describeError(error)}`)),
            Effect.tap(() => Queue.offer(refreshRequests, void 0)),
            Effect.flatMap((nextStatus) => loop(result.threadId, nextStatus)),
          )
        }),
      )

    yield* loop(undefined, undefined)
  }),
)
