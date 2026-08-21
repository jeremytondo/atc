import { Effect, Queue, Ref } from "effect"
import * as AppServer from "./appServer.ts"
import * as OpenTui from "./openTui.ts"
import * as Zmx from "./zmx.ts"
import * as View from "./view.ts"

// Application coordinator: one scoped SSE subscription maintains an
// authoritative snapshot. The manager releases its terminal scope before a
// direct zmx client inherits the TTY, then starts fresh when zmx returns.

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

    const manager = (initial: OpenTui.ManagerState) =>
      OpenTui.run({
        endpoint: server.config.endpoint,
        snapshotRef,
        reachabilityRef,
        backgroundStatusRef,
        uiUpdates,
        refreshRequests,
        initial,
      })

    const attach = (threadId: string) =>
      server.openThread(threadId).pipe(
        Effect.flatMap((terminal) => zmx.attach(terminal)),
        Effect.as("Returned from zmx; session state refreshed."),
      )

    const loop = (state: OpenTui.ManagerState): Effect.Effect<void, unknown> => {
      const resume = (next: Effect.Effect<OpenTui.ManagerState, never>) =>
        next.pipe(
          Effect.tap(() => Queue.offer(refreshRequests, void 0)),
          Effect.flatMap(loop),
        )

      return manager(state).pipe(
        Effect.flatMap((result) => {
          if (result.type === "quit") return Effect.void

          if (result.type === "attach") {
            return attach(result.threadId).pipe(
              Effect.catch((error) => Effect.succeed(`Could not attach: ${describeError(error)}`)),
              Effect.tap(() => Queue.offer(refreshRequests, void 0)),
              Effect.flatMap((status) =>
                loop({
                  ...result.state,
                  section: "threads",
                  selectedThreadId: result.threadId,
                  status,
                }),
              ),
            )
          }

          if (result.type === "createProject") {
            return resume(
              server.createProject(result.input).pipe(
                Effect.map((project) => ({
                  ...result.state,
                  section: "projects" as const,
                  selectedProjectId: project.id,
                  status: `Project “${project.name}” created.`,
                })),
                Effect.catch((error) =>
                  Effect.succeed({
                    ...result.state,
                    status: `Could not create Project: ${describeError(error)}`,
                  }),
                ),
              ),
            )
          }

          if (result.type === "archiveThread") {
            return resume(
              server.archiveThread(result.threadId).pipe(
                Effect.map((thread) => ({
                  ...result.state,
                  selectedThreadId: undefined,
                  status: `Archived “${thread.name}”.`,
                })),
                Effect.catch((error) =>
                  Effect.succeed({
                    ...result.state,
                    status: `Could not archive Thread: ${describeError(error)}`,
                  }),
                ),
              ),
            )
          }

          if (result.type === "unarchiveThread") {
            return resume(
              server.unarchiveThread(result.threadId).pipe(
                Effect.map((thread) => ({
                  ...result.state,
                  selectedArchivedThreadId: undefined,
                  status: `Restored “${thread.name}”.`,
                })),
                Effect.catch((error) =>
                  Effect.succeed({
                    ...result.state,
                    status: `Could not restore Thread: ${describeError(error)}`,
                  }),
                ),
              ),
            )
          }

          if (result.type === "updateProject") {
            return resume(
              server.updateProject(result.projectId, result.input).pipe(
                Effect.map((project) => ({
                  ...result.state,
                  section: "projects" as const,
                  selectedProjectId: project.id,
                  status: `Renamed Project to “${project.name}”.`,
                })),
                Effect.catch((error) =>
                  Effect.succeed({
                    ...result.state,
                    status: `Could not rename Project: ${describeError(error)}`,
                  }),
                ),
              ),
            )
          }

          if (result.type === "deleteProject") {
            return resume(
              server.deleteProject(result.projectId).pipe(
                Effect.as({
                  ...result.state,
                  section: "projects" as const,
                  selectedProjectId: undefined,
                  status: "Project deleted.",
                }),
                Effect.catch((error) =>
                  Effect.succeed({
                    ...result.state,
                    status: `Could not delete Project: ${describeError(error)}`,
                  }),
                ),
              ),
            )
          }

          return resume(
            server.createThread(result.input).pipe(
              Effect.flatMap((thread) =>
                attach(thread.id).pipe(
                  Effect.catch((error) =>
                    Effect.succeed(`Thread created, but could not attach: ${describeError(error)}`),
                  ),
                  Effect.map((status) => ({
                    ...result.state,
                    section: "threads" as const,
                    selectedThreadId: thread.id,
                    status,
                  })),
                ),
              ),
              Effect.catch((error) =>
                Effect.succeed({
                  ...result.state,
                  status: `Could not create Thread: ${describeError(error)}`,
                }),
              ),
            ),
          )
        }),
      )
    }

    yield* loop({})
  }),
)
