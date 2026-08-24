import { BunHttpClient } from "@effect/platform-bun"
import { Context, Effect, Layer } from "effect"
import { HttpClient } from "effect/unstable/http"
import type * as Contract from "../../app-server/src/api/contract.ts"
import * as Client from "../../app-server/src/api/client.ts"
import * as Config from "./config.ts"
import * as Sse from "./sse.ts"
import * as Transport from "./transport.ts"

// Public App Server facade for the terminal client. Thread objects crossing
// this boundary are TUI-only; all-kind server results survive only as
// per-Project counts so destructive actions still describe their full scope.

export type Project = typeof Contract.Project.Type
export type Thread = typeof Contract.Thread.Type
export type TuiThread = Omit<Thread, "kind"> & { readonly kind: "tui" }
export type Terminal = typeof Contract.Terminal.Type
export type Agent = typeof Contract.Agent.Type
export type DirectoryListing = typeof Contract.FsListResponse.Type
export type DirectoryEntry = typeof Contract.FsListEntry.Type

export interface ProjectThreadCounts {
  readonly active: number
  readonly archived: number
}

export interface Snapshot {
  readonly projects: ReadonlyArray<Project>
  readonly threads: ReadonlyArray<TuiThread>
  readonly threadCountsByProject: ReadonlyMap<Project["id"], ProjectThreadCounts>
  readonly agents: ReadonlyArray<Agent>
  readonly fetchedAt: Date
}

export type CreateProjectInput = typeof Contract.CreateProjectRequest.Type
export type UpdateProjectInput = typeof Contract.UpdateProjectRequest.Type
export type CreateTuiThreadInput = Omit<typeof Contract.CreateThreadRequest.Type, "kind">
export type UpdateThreadInput = typeof Contract.UpdateThreadRequest.Type

const isTuiThread = (thread: Thread): thread is TuiThread => thread.kind === "tui"

const countThreadsByProject = (
  threads: ReadonlyArray<Thread>,
): ReadonlyMap<Project["id"], ProjectThreadCounts> => {
  const counts = new Map<Project["id"], ProjectThreadCounts>()
  for (const thread of threads) {
    const current = counts.get(thread.projectId) ?? { active: 0, archived: 0 }
    counts.set(
      thread.projectId,
      thread.archivedAt === undefined
        ? { ...current, active: current.active + 1 }
        : { ...current, archived: current.archived + 1 },
    )
  }
  return counts
}

export const threadCountsForProject = (
  snapshot: Snapshot,
  projectId: Project["id"],
): ProjectThreadCounts =>
  snapshot.threadCountsByProject.get(projectId) ?? { active: 0, archived: 0 }

// The facade intentionally erases generated client error unions because this
// prototype presents every operation failure at the same UI boundary.
export class AppServer extends Context.Service<
  AppServer,
  {
    readonly config: Config.ClientConfig["Service"]
    readonly snapshot: Effect.Effect<Snapshot, unknown>
    readonly listDirectory: (path?: string) => Effect.Effect<DirectoryListing, unknown>
    readonly createProject: (input: CreateProjectInput) => Effect.Effect<Project, unknown>
    readonly updateProject: (
      projectId: string,
      input: UpdateProjectInput,
    ) => Effect.Effect<Project, unknown>
    readonly deleteProject: (projectId: string) => Effect.Effect<void, unknown>
    readonly createTuiThread: (input: CreateTuiThreadInput) => Effect.Effect<TuiThread, unknown>
    readonly updateThread: (
      threadId: string,
      input: UpdateThreadInput,
    ) => Effect.Effect<Thread, unknown>
    readonly archiveThread: (threadId: string) => Effect.Effect<Thread, unknown>
    readonly unarchiveThread: (threadId: string) => Effect.Effect<Thread, unknown>
    readonly markThreadViewed: (threadId: string) => Effect.Effect<TuiThread, unknown>
    readonly openThreadTerminal: (threadId: string) => Effect.Effect<Terminal, unknown>
    readonly subscribe: (
      publish: (signal: Sse.ResourceSignal) => Effect.Effect<void>,
    ) => Effect.Effect<never>
  }
>()("atc-tui/AppServer") {}

export const make = Effect.gen(function* () {
  const config = yield* Config.ClientConfig
  const httpClient = yield* HttpClient.HttpClient
  const transportClient = HttpClient.transformResponse(httpClient, (request) =>
    Transport.provideFetchOptions(request, config),
  )
  const client = yield* Client.make({
    baseUrl: config.endpoint,
    ...(config.token === undefined ? {} : { token: config.token }),
  }).pipe(Effect.provideService(HttpClient.HttpClient, transportClient))

  const snapshot = Effect.gen(function* () {
    const [projects, allThreads, agents] = yield* Effect.all(
      [
        client.v1.listProjects(),
        client.v1.listThreads({ query: { archived: "all" } }),
        client.v1.listAgents(),
      ],
      { concurrency: 3 },
    )
    const threads = allThreads.filter(isTuiThread)
    return {
      projects,
      threads,
      threadCountsByProject: countThreadsByProject(allThreads),
      agents,
      fetchedAt: new Date(),
    }
  })

  const markThreadViewed = (threadId: string) =>
    client.v1.markThreadViewed({ params: { threadId } }).pipe(
      Effect.flatMap((thread): Effect.Effect<TuiThread> => {
        if (!isTuiThread(thread)) {
          return Effect.die(new Error(`viewed TUI Thread ${thread.id} has kind ${thread.kind}`))
        }
        return Effect.succeed(thread)
      }),
    )

  return AppServer.of({
    config,
    snapshot,
    listDirectory: (path) => client.v1.listDirectory({ query: path === undefined ? {} : { path } }),
    createProject: (input) => client.v1.createProject({ payload: input }),
    updateProject: (projectId, input) =>
      client.v1.updateProject({ params: { projectId }, payload: input }),
    deleteProject: (projectId) => client.v1.deleteProject({ params: { projectId } }),
    createTuiThread: (input) =>
      client.v1.createThread({ payload: { ...input, kind: "tui" } }).pipe(
        Effect.flatMap((thread): Effect.Effect<TuiThread> => {
          if (!isTuiThread(thread)) {
            return Effect.die(new Error(`created TUI Thread ${thread.id} has kind ${thread.kind}`))
          }
          return Effect.succeed(thread)
        }),
      ),
    updateThread: (threadId, input) =>
      client.v1.updateThread({ params: { threadId }, payload: input }),
    archiveThread: (threadId) => client.v1.archiveThread({ params: { threadId } }),
    unarchiveThread: (threadId) => client.v1.unarchiveThread({ params: { threadId } }),
    markThreadViewed,
    openThreadTerminal: (threadId) =>
      client.v1
        .openThreadTerminal({ params: { threadId } })
        .pipe(Effect.tap(() => Effect.ignore(markThreadViewed(threadId)))),
    subscribe: (publish) => Sse.subscribe(config, publish),
  })
})

export const layer = Layer.effect(AppServer)(make).pipe(Layer.provide(BunHttpClient.layer))
