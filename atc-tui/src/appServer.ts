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

export interface Snapshot {
  readonly projects: ReadonlyArray<Project>
  readonly threads: ReadonlyArray<Thread>
  readonly fetchedAt: Date
}

export class AppServer extends Context.Service<
  AppServer,
  {
    readonly config: Config.ClientConfig["Service"]
    readonly snapshot: Effect.Effect<Snapshot, unknown>
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
    const threads = yield* client.v1.listThreads({ query: {} })
    return { projects, threads, fetchedAt: new Date() }
  })

  return AppServer.of({
    config,
    snapshot,
    openThread: (threadId) => client.v1.openThreadTerminal({ params: { threadId } }),
    subscribe: (publish) => Sse.subscribe(config, publish),
  })
})

export const layer = Layer.effect(AppServer)(make).pipe(Layer.provide(BunHttpClient.layer))
