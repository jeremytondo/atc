import { Cause, Effect, Queue, Ref } from "effect"
import * as AppServer from "./appServer.ts"
import * as OpenTui from "./openTui.ts"
import * as Zmx from "./zmx.ts"
import * as View from "./view.ts"

// Application coordinator: one scoped SSE subscription maintains an
// authoritative snapshot. The manager keeps its renderer across server
// interactions and releases it only while a direct zmx client owns the TTY.

const describeError = (error: unknown): string =>
  error instanceof Error && error.message !== "" ? error.message : String(error)

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
        Effect.catchCause((cause) =>
          Ref.set(backgroundStatusRef, `Refresh failed: ${Cause.pretty(cause)}`),
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

    const continueWith = (state: OpenTui.ManagerState): OpenTui.ManagerTransition => ({
      type: "continue",
      state,
    })

    const refreshAfter = (transition: Effect.Effect<OpenTui.ManagerTransition, never>) =>
      transition.pipe(Effect.tap(() => Queue.offer(refreshRequests, void 0)))

    const runAction: OpenTui.RunAction = (action) => {
      if (action.type === "attach") {
        return refreshAfter(
          server.openThreadTerminal(action.threadId).pipe(
            Effect.map((terminal) => ({
              type: "attach" as const,
              terminal,
              state: {
                ...action.state,
                section: "threads" as const,
                selectedThreadId: action.threadId,
              },
            })),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not open Thread: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "createProject") {
        return refreshAfter(
          server.createProject(action.input).pipe(
            Effect.map((project) =>
              continueWith({
                ...action.state,
                section: "projects",
                selectedProjectId: project.id,
                status: `Project “${project.name}” created.`,
              }),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not create Project: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "archiveThread") {
        return refreshAfter(
          server.archiveThread(action.threadId).pipe(
            Effect.map((thread) =>
              continueWith({
                ...action.state,
                selectedThreadId: undefined,
                status: `Archived “${thread.name}”.`,
              }),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not archive Thread: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "updateThread") {
        return refreshAfter(
          server.updateThread(action.threadId, action.input).pipe(
            Effect.map((thread) =>
              continueWith({
                ...action.state,
                section: "threads",
                selectedThreadId: thread.id,
                status: `Renamed Thread to “${View.threadLabel(thread)}”.`,
              }),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not rename Thread: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "unarchiveThread") {
        return refreshAfter(
          server.unarchiveThread(action.threadId).pipe(
            Effect.map((thread) =>
              continueWith({
                ...action.state,
                selectedArchivedThreadId: undefined,
                status: `Restored “${thread.name}”.`,
              }),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not restore Thread: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "updateProject") {
        return refreshAfter(
          server.updateProject(action.projectId, action.input).pipe(
            Effect.map((project) =>
              continueWith({
                ...action.state,
                section: "projects",
                selectedProjectId: project.id,
                status: `Renamed Project to “${project.name}”.`,
              }),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not rename Project: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "deleteProject") {
        return refreshAfter(
          server.deleteProject(action.projectId).pipe(
            Effect.as(
              continueWith({
                ...action.state,
                section: "projects",
                selectedProjectId: undefined,
                status: "Project deleted.",
              }),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not delete Project: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      if (action.type === "createThread") {
        return refreshAfter(
          server.createTuiThread(action.input).pipe(
            Effect.flatMap((thread) =>
              server.openThreadTerminal(thread.id).pipe(
                Effect.map((terminal) => ({
                  type: "attach" as const,
                  terminal,
                  state: {
                    ...action.state,
                    section: "threads" as const,
                    selectedThreadId: thread.id,
                  },
                })),
                Effect.catch((error) =>
                  Effect.succeed(
                    continueWith({
                      ...action.state,
                      section: "threads",
                      selectedThreadId: thread.id,
                      status: `Thread created, but could not open: ${describeError(error)}`,
                    }),
                  ),
                ),
              ),
            ),
            Effect.catch((error) =>
              Effect.succeed(
                continueWith({
                  ...action.state,
                  status: `Could not create Thread: ${describeError(error)}`,
                }),
              ),
            ),
          ),
        )
      }

      const exhaustive: never = action
      return exhaustive
    }

    const manager = (initial: OpenTui.ManagerState) =>
      OpenTui.run(
        {
          endpoint: server.config.endpoint,
          listDirectory: server.listDirectory,
          snapshotRef,
          reachabilityRef,
          backgroundStatusRef,
          uiUpdates,
          refreshRequests,
          initial,
        },
        runAction,
      )

    const attach = (terminal: AppServer.Terminal) =>
      zmx.attach(terminal).pipe(Effect.as("Returned from zmx; session state refreshed."))

    const loop = (state: OpenTui.ManagerState): Effect.Effect<void, unknown> =>
      manager(state).pipe(
        Effect.flatMap((result) => {
          if (result.type === "quit") return Effect.void
          return attach(result.terminal).pipe(
            Effect.catch((error) => Effect.succeed(`Could not attach: ${describeError(error)}`)),
            Effect.tap(() => Queue.offer(refreshRequests, void 0)),
            Effect.flatMap((status) => loop({ ...result.state, status })),
          )
        }),
      )

    yield* loop({})
  }),
)
