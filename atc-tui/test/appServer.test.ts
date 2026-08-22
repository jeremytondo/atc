import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import { HttpClient, HttpClientResponse } from "effect/unstable/http"
import * as AppServer from "../src/appServer.ts"
import * as Config from "../src/config.ts"
import * as View from "../src/view.ts"

const project: AppServer.Project = {
  id: "p1",
  name: "Alpha",
  defaultWorkingDirectory: "/work/alpha",
  createdAt: "2026-08-20T00:00:00.000Z",
  updatedAt: "2026-08-20T00:00:00.000Z",
}

const thread = (
  id: string,
  kind: AppServer.Thread["kind"],
  archived = false,
): AppServer.Thread => ({
  id,
  projectId: project.id,
  agentId: "codex",
  kind,
  name: id,
  workingDirectory: project.defaultWorkingDirectory,
  settings: { model: "gpt-5", reasoning: "medium", mode: "chat", access: "auto" },
  activityState: "idle",
  unread: false,
  ...(archived ? { archivedAt: "2026-08-21T00:00:00.000Z" } : {}),
  createdAt: "2026-08-20T00:00:00.000Z",
  updatedAt: "2026-08-20T00:00:00.000Z",
})

const agent: AppServer.Agent = {
  id: "codex",
  available: true,
  detectedVersion: "1.0.0",
  defaults: { model: "gpt-5", reasoning: "medium", mode: "chat", access: "auto" },
}

const config: Config.ClientConfig["Service"] = {
  endpoint: new URL("http://127.0.0.1:7331"),
  zmxExecutable: "zmx",
  zmxDir: "/tmp/atc/terminals",
  environment: {},
}

const response = (request: Parameters<Parameters<typeof HttpClient.make>[0]>[0], body: unknown) =>
  HttpClientResponse.fromWeb(request, Response.json(body))

describe("App Server facade", () => {
  it.effect("partitions one all-kind snapshot into TUI Threads and Project-wide counts", () =>
    Effect.gen(function* () {
      let threadListRequests = 0
      const allThreads = [
        thread("tui-active", "tui"),
        thread("chat-active", "chat"),
        thread("tui-archived", "tui", true),
        thread("chat-archived", "chat", true),
      ]
      const client = HttpClient.make((request) => {
        const url = new URL(request.url)
        if (url.pathname === "/api/v1/projects") {
          return Effect.succeed(response(request, [project]))
        }
        if (url.pathname === "/api/v1/threads") {
          threadListRequests += 1
          const query = new Map(request.urlParams)
          assert.strictEqual(query.get("archived"), "all")
          assert.strictEqual(query.has("kind"), false)
          return Effect.succeed(response(request, allThreads))
        }
        if (url.pathname === "/api/v1/agents") {
          return Effect.succeed(response(request, [agent]))
        }
        return Effect.die(new Error(`unexpected request: ${request.method} ${request.url}`))
      })
      const server = yield* AppServer.make.pipe(
        Effect.provideService(Config.ClientConfig, config),
        Effect.provideService(HttpClient.HttpClient, client),
      )

      const snapshot = yield* server.snapshot

      assert.strictEqual(threadListRequests, 1)
      assert.deepStrictEqual(
        snapshot.threads.map((item) => item.id),
        ["tui-active", "tui-archived"],
      )
      assert.deepStrictEqual(AppServer.threadCountsForProject(snapshot, project.id), {
        active: 2,
        archived: 2,
      })
      assert.deepStrictEqual(
        View.threadOptions(snapshot).map((item) => item.threadId),
        ["tui-active"],
      )
      assert.deepStrictEqual(
        View.threadOptions(snapshot, true).map((item) => item.threadId),
        ["tui-archived"],
      )
      assert.strictEqual(View.normalizeSelection(snapshot, "chat-active"), "tui-active")
      assert.strictEqual(
        View.projectOptions(snapshot)[0]?.description,
        "/work/alpha  ·  2 active  ·  2 archived",
      )
    }),
  )

  it.effect("injects the TUI kind and opens the Thread terminal before marking it viewed", () =>
    Effect.gen(function* () {
      const requests: Array<{ readonly method: string; readonly path: string }> = []
      let createPayload: unknown
      const created = thread("tui-created", "tui")
      const terminal: AppServer.Terminal = {
        id: "terminal-1",
        projectId: project.id,
        threadId: created.id,
        initialWorkingDirectory: project.defaultWorkingDirectory,
        status: "live",
        sessionName: "atc-terminal-1",
        createdAt: "2026-08-20T00:00:00.000Z",
        updatedAt: "2026-08-20T00:00:00.000Z",
      }
      const client = HttpClient.make((request) => {
        const path = new URL(request.url).pathname
        requests.push({ method: request.method, path })
        if (request.method === "POST" && path === "/api/v1/threads") {
          if (request.body._tag !== "Uint8Array") {
            return Effect.die(new Error(`unexpected request body: ${request.body._tag}`))
          }
          createPayload = JSON.parse(new TextDecoder().decode(request.body.body))
          return Effect.succeed(response(request, created))
        }
        if (path === `/api/v1/threads/${created.id}/terminal`) {
          return Effect.succeed(response(request, terminal))
        }
        if (path === `/api/v1/threads/${created.id}/viewed`) {
          return Effect.succeed(response(request, created))
        }
        return Effect.die(new Error(`unexpected request: ${request.method} ${request.url}`))
      })
      const server = yield* AppServer.make.pipe(
        Effect.provideService(Config.ClientConfig, config),
        Effect.provideService(HttpClient.HttpClient, client),
      )

      assert.strictEqual(
        (yield* server.createTuiThread({ projectId: project.id, agentId: "codex" })).kind,
        "tui",
      )
      assert.deepStrictEqual(createPayload, {
        projectId: project.id,
        agentId: "codex",
        kind: "tui",
      })
      assert.deepStrictEqual(yield* server.openThreadTerminal(created.id), terminal)
      assert.deepStrictEqual(requests, [
        { method: "POST", path: "/api/v1/threads" },
        { method: "POST", path: `/api/v1/threads/${created.id}/terminal` },
        { method: "POST", path: `/api/v1/threads/${created.id}/viewed` },
      ])
    }),
  )
})
