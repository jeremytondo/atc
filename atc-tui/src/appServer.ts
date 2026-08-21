import { BunHttpClient } from "@effect/platform-bun"
import { Context, Effect, Layer } from "effect"
import type * as Contract from "../../app-server/src/api/contract.ts"
import * as Client from "../../app-server/src/api/client.ts"
import * as Config from "./config.ts"
import * as Sse from "./sse.ts"

// Public App Server facade for the terminal client. It depends only on the
// contract-derived client and the documented SSE wire stream.

export type Project = typeof Contract.Project.Type
export type Thread = typeof Contract.Thread.Type
export type Terminal = typeof Contract.Terminal.Type
export type Agent = typeof Contract.Agent.Type

export interface Snapshot {
  readonly projects: ReadonlyArray<Project>
  readonly threads: ReadonlyArray<Thread>
  readonly agents: ReadonlyArray<Agent>
  readonly fetchedAt: Date
}

export type CreateProjectInput = typeof Contract.CreateProjectRequest.Type
export type UpdateProjectInput = typeof Contract.UpdateProjectRequest.Type
export type CreateThreadInput = typeof Contract.CreateThreadRequest.Type

export class AppServer extends Context.Service<
  AppServer,
  {
    readonly config: Config.ClientConfig["Service"]
    readonly snapshot: Effect.Effect<Snapshot, unknown>
    readonly createProject: (input: CreateProjectInput) => Effect.Effect<Project, unknown>
    readonly updateProject: (
      projectId: string,
      input: UpdateProjectInput,
    ) => Effect.Effect<Project, unknown>
    readonly deleteProject: (projectId: string) => Effect.Effect<void, unknown>
    readonly createThread: (input: CreateThreadInput) => Effect.Effect<Thread, unknown>
    readonly archiveThread: (threadId: string) => Effect.Effect<Thread, unknown>
    readonly unarchiveThread: (threadId: string) => Effect.Effect<Thread, unknown>
    readonly openThread: (threadId: string) => Effect.Effect<Terminal, unknown>
    readonly subscribe: (
      publish: (signal: Sse.ResourceSignal) => Effect.Effect<void>,
    ) => Effect.Effect<never>
  }
>()("atc-tui/AppServer") {}

const make = Effect.gen(function* () {
  const config = yield* Config.ClientConfig
  const client = yield* Client.make({
    baseUrl: config.endpoint,
    ...(config.token === undefined ? {} : { token: config.token }),
  })

  const snapshot = Effect.gen(function* () {
    const projects = yield* client.v1.listProjects()
    const threads = yield* client.v1.listThreads({ query: { archived: "all" } })
    const agents = yield* client.v1.listAgents()
    return { projects, threads, agents, fetchedAt: new Date() }
  })

  return AppServer.of({
    config,
    snapshot,
    createProject: (input) => client.v1.createProject({ payload: input }),
    updateProject: (projectId, input) =>
      client.v1.updateProject({ params: { projectId }, payload: input }),
    deleteProject: (projectId) => client.v1.deleteProject({ params: { projectId } }),
    createThread: (input) => client.v1.createThread({ payload: input }),
    archiveThread: (threadId) => client.v1.archiveThread({ params: { threadId } }),
    unarchiveThread: (threadId) => client.v1.unarchiveThread({ params: { threadId } }),
    openThread: (threadId) => client.v1.openThreadTerminal({ params: { threadId } }),
    subscribe: (publish) => Sse.subscribe(config, publish),
  })
})

export const layer = Layer.effect(AppServer)(make).pipe(Layer.provide(BunHttpClient.layer))
