import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Context, Effect, Layer, Option } from "effect"
import {
  HttpBody,
  HttpClient,
  HttpClientRequest,
  HttpRouter,
  HttpServerRequest,
  HttpServerResponse,
} from "effect/unstable/http"
import { HttpApi, OpenApi } from "effect/unstable/httpapi"
import { Api, DEFAULT_PORT } from "../../src/api/contract.ts"
import * as ClaudeHooks from "../../src/agents/claudeHooks.ts"
import { openApiDocument, openApiJson } from "../../src/api/openapi.ts"
import * as Server from "../../src/server.ts"
import { appServerRoot } from "../blackbox.ts"
import { TestBuildInfoLayer, testBuildInfo } from "../testBuildInfo.ts"
import { startServer, TestAuthTokenLayer, TestRepositoryLayers } from "../testLayers.ts"

// Contract-generation tests: the document must be deterministic, match the
// checked-in artifact, cover every contract operation, and agree with what
// the runtime routes actually return. Runtime behavior itself is covered
// separately in api.test.ts, so failures here point at the document, not the
// handlers.

const operation = (path: string, method = "get") =>
  ((openApiDocument.paths[path] as Record<string, unknown>)?.[method] ??
    assert.fail(`no ${method.toUpperCase()} operation documented for ${path}`)) as {
    readonly operationId: string
    readonly responses: Record<string, { content: Record<string, { schema: { $ref?: string } }> }>
  }

const componentSchema = (name: string) =>
  ((openApiDocument.components.schemas as Record<string, unknown>)[name] ??
    assert.fail(`no component schema named ${name}`)) as {
    readonly properties: Record<string, unknown>
    readonly required: ReadonlyArray<string>
  }

describe("openapi document", () => {
  it("pins the document version to the contract version, never build metadata", () => {
    assert.strictEqual(openApiDocument.info.title, "ATC App Server API")
    assert.strictEqual(openApiDocument.info.version, "v1")
  })

  it("documents the templated default loopback server", () => {
    // Generated clients build their default base URL from this entry
    // (e.g. Swift's Servers.Server1), so it is contract, not decoration.
    assert.deepStrictEqual(openApiDocument.servers, [
      {
        url: "http://127.0.0.1:{port}",
        description: "Local App Server",
        variables: { port: { default: `${DEFAULT_PORT}` } },
      },
    ])
  })

  it("matches the checked-in artifact, byte-identical across fresh processes", async () => {
    // Fresh processes defeat fromApi's in-memory cache, so this proves real
    // determinism, not just reference equality.
    const generate = () => {
      const proc = Bun.spawnSync([process.execPath, "run", "scripts/openapi.ts", "--print"], {
        cwd: appServerRoot,
      })
      assert.strictEqual(proc.exitCode, 0, proc.stderr.toString())
      return proc.stdout.toString()
    }
    const first = generate()
    assert.strictEqual(first, generate())
    assert.strictEqual(first, openApiJson)
    assert.strictEqual(
      await Bun.file(`${appServerRoot}openapi.json`).text(),
      openApiJson,
      "openapi.json is stale — run `mise run openapi` and commit the result",
    )
  })

  it("documents the optional bearer security scheme", () => {
    // The server gates every non-loopback request on a bearer token
    // (localTrust.ts). The empty requirement documents that anonymous
    // (verified loopback or Tailscale) access is valid, so generated clients
    // treat the token as optional but know how to send it.
    const document = openApiDocument as unknown as {
      security: ReadonlyArray<Record<string, unknown>>
      components: { securitySchemes: Record<string, { type: string; scheme: string }> }
      paths: Record<string, Record<string, { security?: unknown }>>
    }
    assert.deepStrictEqual(document.security, [{}, { bearerAuth: [] }])
    const scheme = document.components.securitySchemes["bearerAuth"]!
    assert.strictEqual(scheme.type, "http")
    assert.strictEqual(scheme.scheme, "bearer")
    // An operation-level `security` array overrides the document default
    // (OpenAPI 3.1), so no operation may carry one — the generator's empty
    // stamps would silently declare every operation credential-free.
    for (const [path, operations] of Object.entries(document.paths)) {
      for (const [method, operation] of Object.entries(operations)) {
        assert.notProperty(operation, "security", `${method.toUpperCase()} ${path}`)
      }
    }
  })

  it("documents every contract operation exactly once, with pinned camelCase ids", () => {
    const expected: Array<{ path: string; method: string }> = []
    HttpApi.reflect(Api, {
      onGroup() {},
      onEndpoint({ endpoint, mergedAnnotations }) {
        // OpenApi.Exclude drops an endpoint from the document by design
        // (the WebSocket attach endpoint is the one current use).
        if (Context.get(mergedAnnotations, OpenApi.Exclude)) return
        expected.push({
          path: endpoint.path.replace(/:(\w+)\??/g, "{$1}"),
          method: endpoint.method.toLowerCase(),
        })
      },
    })

    const documented = Object.entries(openApiDocument.paths).flatMap(([path, operations]) =>
      Object.keys(operations).map((method) => ({ path, method })),
    )
    assert.sameDeepMembers(documented, expected)

    const operationIds = documented.map(({ path, method }) => {
      const id = operation(path, method).operationId
      // Derived ids look like "v1.health" — a forgotten OpenApi.Identifier
      // annotation would ship a bad id whose later fix breaks clients (see
      // AGENTS.md "OpenAPI Contract").
      assert.match(id, /^[a-z][A-Za-z0-9]*$/, `operation id for ${path} must be pinned camelCase`)
      return id
    })
    assert.strictEqual(new Set(operationIds).size, expected.length)
  })
})

// A raw in-process HTTP client over the real routes layer — the same pattern
// HttpApiTest uses internally, but returning undecoded responses so assertions
// see the raw wire payload instead of contract-decoded values.
const rawClient = Effect.gen(function* () {
  const handler = yield* HttpRouter.toHttpEffect(
    Server.routes.pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
  )
  return HttpClient.make(
    Effect.fnUntraced(function* (request) {
      // fromClientRequest synthesizes no Host header or peer address; the
      // trust guard in Server.routes requires both a loopback Host and a
      // loopback peer, just as Bun reports for a real local client.
      const serverRequest = HttpServerRequest.fromClientRequest(
        HttpClientRequest.setHeader(request, "host", "127.0.0.1"),
      ).modify({ remoteAddress: Option.some("127.0.0.1") })
      const response = yield* handler.pipe(
        Effect.provideService(HttpServerRequest.HttpServerRequest, serverRequest),
        // The internal Claude hook route resolves its service per request;
        // the trust middleware resolves AuthToken the same way.
        Effect.provide([ClaudeHooks.layer, TestAuthTokenLayer]),
        Effect.orDie,
      )
      return HttpServerResponse.toClientResponse(response)
    }, Effect.scoped),
  )
})

// Concrete per-endpoint assertions: each case ties the documented response
// schema to the actual wire payload. The path-coverage assertion in the first
// test above ("documents every contract operation") plus the fixed path list
// here force a new case whenever the contract grows.
describe("openapi document vs runtime", () => {
  it("these tests cover every documented path", () => {
    assert.sameMembers(Object.keys(openApiDocument.paths), [
      "/api/v1/health",
      "/api/v1/server-info",
      "/api/v1/version",
      "/api/v1/projects",
      "/api/v1/projects/{projectId}",
      "/api/v1/terminals",
      "/api/v1/terminals/{terminalId}",
      "/api/v1/threads",
      "/api/v1/threads/{threadId}",
      "/api/v1/threads/{threadId}/archive",
      "/api/v1/threads/{threadId}/unarchive",
      "/api/v1/threads/{threadId}/pin",
      "/api/v1/threads/{threadId}/unpin",
      "/api/v1/threads/{threadId}/viewed",
      "/api/v1/threads/{threadId}/terminal",
      "/api/v1/threads/{threadId}/prompt",
      "/api/v1/threads/{threadId}/transcript",
      "/api/v1/threads/{threadId}/events",
      "/api/v1/threads/{threadId}/interrupt",
      "/api/v1/threads/{threadId}/requests",
      "/api/v1/threads/{threadId}/requests/{requestId}/answer",
      "/api/v1/threads/{threadId}/queue",
      "/api/v1/threads/{threadId}/queue/{promptId}",
      "/api/v1/agents",
      "/api/v1/agents/{agentId}",
      "/api/v1/agents/{agentId}/models",
      "/api/v1/events",
      "/api/v1/fs/check",
      "/api/v1/fs/list",
    ])
    // The WebSocket attach endpoint is contract-declared but excluded from
    // the document — REST clients cannot represent an upgrade.
    assert.notProperty(openApiDocument.paths, "/api/v1/terminals/{terminalId}/attach")
  })

  it.effect("GET /api/v1/health returns the documented payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/health")
      assert.strictEqual(response.status, 200)
      assert.deepStrictEqual(yield* response.json, { status: "ok" })

      const ref = operation("/api/v1/health").responses["200"]!.content["application/json"]!.schema
      assert.deepStrictEqual(ref, { $ref: "#/components/schemas/HealthResponse" })
      const schema = componentSchema("HealthResponse")
      assert.sameMembers(Object.keys(schema.properties), ["status"])
      assert.sameMembers([...schema.required], ["status"])
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/server-info returns the documented payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/server-info")
      assert.strictEqual(response.status, 200)
      assert.deepStrictEqual(yield* response.json, { tailscale: { state: "disabled" } })

      const ref =
        operation("/api/v1/server-info").responses["200"]!.content["application/json"]!.schema
      assert.deepStrictEqual(ref, { $ref: "#/components/schemas/ServerInfoResponse" })
      const state = componentSchema("TailscaleState")
      assert.sameMembers(Object.keys(state.properties), ["state", "url", "reason"])
      assert.sameMembers([...state.required], ["state"])
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/version returns the documented payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/version")
      assert.strictEqual(response.status, 200)
      assert.deepStrictEqual(yield* response.json, {
        version: testBuildInfo.version,
        apiVersion: "v1",
        commit: testBuildInfo.commit,
        builtAt: testBuildInfo.builtAt,
      })

      const ref = operation("/api/v1/version").responses["200"]!.content["application/json"]!.schema
      assert.deepStrictEqual(ref, { $ref: "#/components/schemas/VersionResponse" })
      const schema = componentSchema("VersionResponse")
      assert.sameMembers(Object.keys(schema.properties), [
        "version",
        "apiVersion",
        "commit",
        "builtAt",
      ])
      assert.sameMembers([...schema.required], ["version", "apiVersion", "commit", "builtAt"])
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("/api/v1/projects: created and listed payloads match the documented schemas", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const created = yield* client.post("http://127.0.0.1/api/v1/projects", {
        body: HttpBody.jsonUnsafe({ name: "Doc Test", defaultWorkingDirectory: "/tmp" }),
      })
      assert.strictEqual(created.status, 200)
      const createdBody = (yield* created.json) as Record<string, unknown>
      const schema = componentSchema("Project")
      assert.sameMembers(Object.keys(schema.properties), Object.keys(createdBody))
      assert.sameMembers([...schema.required], Object.keys(createdBody))
      assert.deepStrictEqual(
        operation("/api/v1/projects", "post").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/Project" },
      )

      const listed = yield* client.get("http://127.0.0.1/api/v1/projects")
      assert.strictEqual(listed.status, 200)
      assert.deepStrictEqual(yield* listed.json, [createdBody] as unknown)
      assert.deepStrictEqual(
        operation("/api/v1/projects").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/ProjectList" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/projects/{projectId} documents and returns the 404 error payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/projects/nope")
      assert.strictEqual(response.status, 404)
      // The wire error carries the required, precomputed message field.
      assert.deepStrictEqual(yield* response.json, {
        _tag: "ProjectNotFound",
        projectId: "nope",
        message: "no project with id nope",
      })
      assert.deepStrictEqual(
        operation("/api/v1/projects/{projectId}").responses["404"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/ProjectNotFoundJsonEncoding" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("/api/v1/terminals: created and listed payloads match the documented schemas", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const project = yield* client.post("http://127.0.0.1/api/v1/projects", {
        body: HttpBody.jsonUnsafe({ name: "Terminal Docs", defaultWorkingDirectory: "/tmp" }),
      })
      const projectBody = (yield* project.json) as { id: string }

      const created = yield* client.post("http://127.0.0.1/api/v1/terminals", {
        body: HttpBody.jsonUnsafe({ projectId: projectBody.id }),
      })
      assert.strictEqual(created.status, 200)
      const createdBody = (yield* created.json) as Record<string, unknown>
      const schema = componentSchema("Terminal")
      // A shell terminal without a label carries exactly the required keys;
      // name/command/endedAt are absent optional keys, never null.
      assert.sameMembers([...schema.required], Object.keys(createdBody))
      assert.includeMembers(Object.keys(schema.properties), Object.keys(createdBody))
      assert.strictEqual(createdBody["status"], "live")
      assert.deepStrictEqual(
        operation("/api/v1/terminals", "post").responses["200"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/Terminal" },
      )

      const listed = yield* client.get("http://127.0.0.1/api/v1/terminals")
      assert.strictEqual(listed.status, 200)
      assert.deepStrictEqual(yield* listed.json, [createdBody] as unknown)
      assert.deepStrictEqual(
        operation("/api/v1/terminals").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/TerminalList" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/terminals/{terminalId} documents and returns the 404 error payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/terminals/nope")
      assert.strictEqual(response.status, 404)
      assert.deepStrictEqual(yield* response.json, {
        _tag: "TerminalNotFound",
        terminalId: "nope",
        message: "no terminal with id nope",
      })
      assert.deepStrictEqual(
        operation("/api/v1/terminals/{terminalId}").responses["404"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/TerminalNotFoundJsonEncoding" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("/api/v1/threads: created and listed payloads match the documented schemas", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const project = yield* client.post("http://127.0.0.1/api/v1/projects", {
        body: HttpBody.jsonUnsafe({ name: "Thread Docs", defaultWorkingDirectory: "/tmp" }),
      })
      const projectBody = (yield* project.json) as { id: string }

      const created = yield* client.post("http://127.0.0.1/api/v1/threads", {
        body: HttpBody.jsonUnsafe({ projectId: projectBody.id, agentId: "codex" }),
      })
      assert.strictEqual(created.status, 200)
      const createdBody = (yield* created.json) as Record<string, unknown>
      const schema = componentSchema("Thread")
      // A fresh unnamed thread carries exactly the required keys;
      // name/linkedTerminalId/pinnedAt/archivedAt are absent optional keys, never null.
      assert.sameMembers([...schema.required], Object.keys(createdBody))
      assert.includeMembers(Object.keys(schema.properties), Object.keys(createdBody))
      assert.deepStrictEqual(
        operation("/api/v1/threads", "post").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/Thread" },
      )

      const listed = yield* client.get(
        `http://127.0.0.1/api/v1/threads?projectId=${projectBody.id}`,
      )
      assert.strictEqual(listed.status, 200)
      assert.deepStrictEqual(yield* listed.json, [createdBody] as unknown)
      assert.deepStrictEqual(
        operation("/api/v1/threads").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/ThreadList" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/threads/{threadId} documents and returns the 404 error payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/threads/nope")
      assert.strictEqual(response.status, 404)
      assert.deepStrictEqual(yield* response.json, {
        _tag: "ThreadNotFound",
        threadId: "nope",
        message: "no thread with id nope",
      })
      assert.deepStrictEqual(
        operation("/api/v1/threads/{threadId}").responses["404"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/ThreadNotFoundJsonEncoding" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("thread pin and archive actions round-trip the Thread schema", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const project = yield* client.post("http://127.0.0.1/api/v1/projects", {
        body: HttpBody.jsonUnsafe({ name: "Archive Docs", defaultWorkingDirectory: "/tmp" }),
      })
      const projectBody = (yield* project.json) as { id: string }
      const created = yield* client.post("http://127.0.0.1/api/v1/threads", {
        body: HttpBody.jsonUnsafe({ projectId: projectBody.id, agentId: "claude-code" }),
      })
      const createdBody = (yield* created.json) as { id: string }

      const pinned = yield* client.post(`http://127.0.0.1/api/v1/threads/${createdBody.id}/pin`, {
        body: HttpBody.empty,
      })
      assert.strictEqual(pinned.status, 200)
      const pinnedBody = (yield* pinned.json) as Record<string, unknown>
      assert.isString(pinnedBody["pinnedAt"])
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/pin`, "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/Thread" },
      )

      const unpinned = yield* client.post(
        `http://127.0.0.1/api/v1/threads/${createdBody.id}/unpin`,
        { body: HttpBody.empty },
      )
      assert.strictEqual(unpinned.status, 200)
      const unpinnedBody = (yield* unpinned.json) as Record<string, unknown>
      assert.notProperty(unpinnedBody, "pinnedAt")
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/unpin`, "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/Thread" },
      )

      const archived = yield* client.post(
        `http://127.0.0.1/api/v1/threads/${createdBody.id}/archive`,
        { body: HttpBody.empty },
      )
      assert.strictEqual(archived.status, 200)
      const archivedBody = (yield* archived.json) as Record<string, unknown>
      assert.isString(archivedBody["archivedAt"])
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/archive`, "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/Thread" },
      )

      const restored = yield* client.post(
        `http://127.0.0.1/api/v1/threads/${createdBody.id}/unarchive`,
        { body: HttpBody.empty },
      )
      assert.strictEqual(restored.status, 200)
      const restoredBody = (yield* restored.json) as Record<string, unknown>
      assert.notProperty(restoredBody, "archivedAt")
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/unarchive`, "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/Thread" },
      )

      // The viewed stamp rounds-trips the Thread schema too; on a thread
      // with no finished turn it is a pure no-op with `unread: false`.
      const viewed = yield* client.post(
        `http://127.0.0.1/api/v1/threads/${createdBody.id}/viewed`,
        { body: HttpBody.empty },
      )
      assert.strictEqual(viewed.status, 200)
      const viewedBody = (yield* viewed.json) as Record<string, unknown>
      assert.strictEqual(viewedBody["unread"], false)
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/viewed`, "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/Thread" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("POST /api/v1/threads/{threadId}/terminal opens a linked Terminal", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const project = yield* client.post("http://127.0.0.1/api/v1/projects", {
        body: HttpBody.jsonUnsafe({ name: "Open Docs", defaultWorkingDirectory: "/tmp" }),
      })
      const projectBody = (yield* project.json) as { id: string }
      const thread = yield* client.post("http://127.0.0.1/api/v1/threads", {
        body: HttpBody.jsonUnsafe({ projectId: projectBody.id, agentId: "codex" }),
      })
      const threadBody = (yield* thread.json) as { id: string }

      const opened = yield* client.post(
        `http://127.0.0.1/api/v1/threads/${threadBody.id}/terminal`,
        { body: HttpBody.empty },
      )
      assert.strictEqual(opened.status, 200)
      const terminal = (yield* opened.json) as { threadId?: string; status?: string }
      assert.strictEqual(terminal.threadId, threadBody.id)
      assert.strictEqual(terminal.status, "live")
      assert.deepStrictEqual(
        operation("/api/v1/threads/{threadId}/terminal", "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/Terminal" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("/api/v1/agents: listed and fetched payloads match the documented schemas", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const listed = yield* client.get("http://127.0.0.1/api/v1/agents")
      assert.strictEqual(listed.status, 200)
      const agents = (yield* listed.json) as Array<Record<string, unknown>>
      assert.deepStrictEqual(
        agents.map((agent) => agent["id"]),
        ["codex", "claude-code"],
      )
      const schema = componentSchema("Agent")
      for (const agent of agents) {
        assert.includeMembers(Object.keys(schema.properties), Object.keys(agent))
        assert.sameMembers(
          [...schema.required].filter((key) => !(key in agent)),
          [],
        )
      }
      assert.deepStrictEqual(
        operation("/api/v1/agents").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/AgentList" },
      )

      const fetched = yield* client.get("http://127.0.0.1/api/v1/agents/codex")
      assert.strictEqual(fetched.status, 200)
      assert.deepStrictEqual(
        operation("/api/v1/agents/{agentId}").responses["200"]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/Agent" },
      )

      const missing = yield* client.get("http://127.0.0.1/api/v1/agents/nope")
      assert.strictEqual(missing.status, 404)
      assert.deepStrictEqual(yield* missing.json, {
        _tag: "AgentNotFound",
        agentId: "nope",
        message: "no agent with id nope",
      })

      // The model catalog (ATC-205), over the fake adapter's stock catalog.
      const models = yield* client.get("http://127.0.0.1/api/v1/agents/codex/models")
      assert.strictEqual(models.status, 200)
      const catalog = (yield* models.json) as Array<Record<string, unknown>>
      assert.isAtLeast(catalog.length, 1, "the fake catalog lists at least one model")
      const modelSchema = componentSchema("AgentModel")
      for (const model of catalog) {
        assert.includeMembers(Object.keys(modelSchema.properties), Object.keys(model))
        assert.sameMembers(
          [...modelSchema.required].filter((key) => !(key in model)),
          [],
        )
      }
      assert.deepStrictEqual(
        operation("/api/v1/agents/{agentId}/models").responses["200"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/AgentModelList" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  // The Thread runtime routes (ATC-193): documented shapes against the raw
  // wire, over the fake adapter (TestRepositoryLayers).
  it.effect("thread prompt / queue / interrupt document and return their payloads", () =>
    Effect.gen(function* () {
      const client = yield* rawClient
      const project = yield* client.post("http://127.0.0.1/api/v1/projects", {
        body: HttpBody.jsonUnsafe({ name: "Runtime Docs", defaultWorkingDirectory: "/tmp" }),
      })
      const projectBody = (yield* project.json) as { id: string }
      const created = yield* client.post("http://127.0.0.1/api/v1/threads", {
        body: HttpBody.jsonUnsafe({ projectId: projectBody.id, agentId: "codex" }),
      })
      const thread = (yield* created.json) as { id: string }
      const base = `http://127.0.0.1/api/v1/threads/${thread.id}`

      const prompted = yield* client.post(`${base}/prompt`, {
        body: HttpBody.jsonUnsafe({ prompt: "hello" }),
      })
      assert.strictEqual(prompted.status, 200)
      const promptBody = (yield* prompted.json) as { promptId: string; turnId?: string }
      assert.isString(promptBody.promptId)
      assert.isString(promptBody.turnId)
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/prompt`, "post").responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/PromptThreadResponse" },
      )
      assert.sameMembers(Object.keys(componentSchema("PromptThreadResponse").properties), [
        "promptId",
        "turnId",
      ])
      // A second prompt queues (the fake turn is still running).
      const queued = yield* client.post(`${base}/prompt`, {
        body: HttpBody.jsonUnsafe({ prompt: "later" }),
      })
      const queuedBody = (yield* queued.json) as { promptId: string; turnId?: string }
      assert.isUndefined(queuedBody.turnId)
      const list = yield* client.get(`${base}/queue`)
      assert.strictEqual(list.status, 200)
      const listBody = (yield* list.json) as Array<{ id: string; prompt: string; queuedAt: string }>
      assert.deepStrictEqual(
        listBody.map((entry) => [entry.id, entry.prompt]),
        [[queuedBody.promptId, "later"]],
      )
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/queue`).responses["200"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/QueuedPromptList" },
      )
      const removed = yield* client.del(`${base}/queue/${queuedBody.promptId}`)
      assert.strictEqual(removed.status, 204)
      const gone = yield* client.del(`${base}/queue/${queuedBody.promptId}`)
      assert.strictEqual(gone.status, 404)
      // Two 404s share the status: the thread, or the prompt.
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/queue/{promptId}`, "delete").responses["404"]!
          .content["application/json"]!.schema as unknown,
        {
          anyOf: [
            { $ref: "#/components/schemas/ThreadNotFoundJsonEncoding" },
            { $ref: "#/components/schemas/QueuedPromptNotFoundJsonEncoding" },
          ],
        },
      )
      const interrupted = yield* client.post(`${base}/interrupt`, { body: HttpBody.empty })
      assert.strictEqual(interrupted.status, 204)
      const requests = yield* client.get(`${base}/requests`)
      assert.strictEqual(requests.status, 200)
      assert.deepStrictEqual(yield* requests.json, [])
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/requests`).responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/ThreadRequestList" },
      )
      const unanswered = yield* client.post(`${base}/requests/nope/answer`, {
        body: HttpBody.jsonUnsafe({ kind: "approval", decision: "accept" }),
      })
      assert.strictEqual(unanswered.status, 404)
      assert.deepStrictEqual(yield* unanswered.json, {
        _tag: "RequestNotFound",
        threadId: thread.id,
        requestId: "nope",
        message: `thread ${thread.id} has no pending request nope`,
      })
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/requests/{requestId}/answer`, "post").responses[
          "400"
        ]!.content["application/json"]!.schema,
        { $ref: "#/components/schemas/InvalidRequestAnswerJsonEncoding" },
      )
      const transcript = yield* client.get(`${base}/transcript?limit=5`)
      assert.strictEqual(transcript.status, 200)
      const transcriptBody = (yield* transcript.json) as {
        items: Array<{ type: string }>
        turns: Array<{ id: string; status: string }>
        seq: number
        snapshotVersion: number
        hasMore: boolean
      }
      assert.deepStrictEqual(
        transcriptBody.turns.map((turn) => turn.status),
        ["interrupted"],
      )
      assert.isNumber(transcriptBody.seq)
      assert.strictEqual(transcriptBody.snapshotVersion, 0)
      assert.isFalse(transcriptBody.hasMore)
      assert.deepStrictEqual(
        operation(`/api/v1/threads/{threadId}/transcript`).responses["200"]!.content[
          "application/json"
        ]!.schema,
        { $ref: "#/components/schemas/ThreadTranscript" },
      )
      assert.sameMembers(Object.keys(componentSchema("ThreadTranscript").properties), [
        "items",
        "turns",
        "seq",
        "snapshotVersion",
        "hasMore",
      ])
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.live("GET /api/v1/threads/{threadId}/events: the documented SSE shape matches the bytes", () =>
    Effect.gen(function* () {
      const content = operation("/api/v1/threads/{threadId}/events").responses["200"]!.content[
        "text/event-stream"
      ]!
      assert.deepStrictEqual(content.schema as unknown, {
        $ref: "#/components/schemas/ThreadEventJsonEncoding",
      })
      const description = (
        operation("/api/v1/threads/{threadId}/events").responses["200"]! as unknown as {
          description: string
        }
      ).description
      for (const token of [": connected", ": heartbeat", "`data:`", "ThreadEvent"]) {
        assert.include(description, token)
      }
      // The event union is a discriminated component with one variant per
      // event type — what generated clients decode the frames into.
      const event = componentSchema("ThreadEvent") as unknown as {
        oneOf: Array<{ $ref: string }>
        discriminator: { propertyName: string; mapping: Record<string, string> }
      }
      assert.strictEqual(event.discriminator.propertyName, "type")
      assert.sameMembers(Object.keys(event.discriminator.mapping), [
        "item.started",
        "item.updated",
        "item.completed",
        "turn.started",
        "turn.completed",
        "text.delta",
        "request.opened",
        "request.closed",
        "queue.updated",
        "snapshot.invalidated",
      ])

      const { base } = yield* startServer(
        Server.layer({ port: 0 }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
      )
      const json = <A>(response: Promise<Response>) => response.then((r) => r.json() as Promise<A>)
      const project = yield* Effect.promise(() =>
        json<{ id: string }>(
          fetch(`${base}/api/v1/projects`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ name: "ThreadSse", defaultWorkingDirectory: "/tmp" }),
          }),
        ),
      )
      const thread = yield* Effect.promise(() =>
        json<{ id: string }>(
          fetch(`${base}/api/v1/threads`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ projectId: project.id, agentId: "codex" }),
          }),
        ),
      )
      const response = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/threads/${thread.id}/events`, {
          headers: { accept: "text/event-stream" },
        }),
      )
      assert.match(response.headers.get("content-type") ?? "", /^text\/event-stream/)
      const reader = response.body!.getReader()
      const opening = yield* Effect.promise(() => reader.read())
      // A prefix, not the whole chunk: the transport may batch a frame in.
      const openingText = new TextDecoder().decode(opening.value)
      assert.isTrue(
        openingText.startsWith(": connected\n\n"),
        `expected the connected comment first, got ${JSON.stringify(openingText)}`,
      )

      yield* Effect.promise(() =>
        fetch(`${base}/api/v1/threads/${thread.id}/prompt`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ prompt: "stream me" }),
        }),
      )
      // Frames arrive as bare `data:` lines; the prompt started at once, so
      // the first frame is its turn start (with a seq), then the queue
      // publish — never a queue holding a prompt that is already a turn
      // (ATC-214). Bounded: a missing frame must fail naming what was seen,
      // not hang on heartbeats.
      let text = openingText
      yield* Effect.promise(async () => {
        while (
          !text.includes('"type":"turn.started"') ||
          !text.includes('"type":"queue.updated"')
        ) {
          const chunk = await reader.read()
          if (chunk.done) break
          text += new TextDecoder().decode(chunk.value)
        }
      }).pipe(
        Effect.timeoutOrElse({
          duration: "10 seconds",
          orElse: () =>
            Effect.die(new Error(`no turn.started + queue.updated within 10s; saw: ${text}`)),
        }),
      )
      const frames = text.split("\n\n").filter((frame) => frame.startsWith("data: "))
      const parsed = frames.map(
        (frame) =>
          JSON.parse(frame.slice("data: ".length)) as {
            type: string
            seq?: number
          },
      )
      assert.strictEqual(parsed[0]?.type, "turn.started")
      assert.isNumber(parsed[0]?.seq)
      const queue = parsed.find((event) => event.type === "queue.updated")
      assert.isDefined(queue)
      assert.isUndefined(queue?.seq)
      assert.isFalse(text.includes("\nid:"))
      assert.isFalse(text.includes("\nevent:"))
      yield* Effect.promise(() => reader.cancel())
    }),
  )

  // it.live: real socket I/O — the point is pinning the *documented* frame
  // shape to the exact bytes a real subscriber receives: data-only frames
  // plus comment lines, never the id/event envelope fields.
  it.live("GET /api/v1/events: the documented data-only SSE shape matches the emitted bytes", () =>
    Effect.gen(function* () {
      // The document declares each frame's payload as the JSON-encoded
      // ResourceChangedEvent string...
      const content = operation("/api/v1/events").responses["200"]!.content["text/event-stream"]!
      assert.deepStrictEqual(content.schema as unknown, {
        $ref: "#/components/schemas/ResourceChangedEventJsonEncoding",
      })
      assert.deepStrictEqual(componentSchema("ResourceChangedEventJsonEncoding") as unknown, {
        type: "string",
        contentMediaType: "application/json",
      })
      // ...the comment protocol is documented where clients will read it...
      const description = (
        operation("/api/v1/events").responses["200"]! as unknown as { description: string }
      ).description
      for (const token of [": connected", ": heartbeat", "`data:`"]) {
        assert.include(description, token)
      }
      // ...and the plain payload schema is emitted as an ordinary component
      // (openapi.ts), so generated clients get a real model type to decode
      // that JSON into.
      const payload = componentSchema("ResourceChangedEvent")
      assert.sameMembers(Object.keys(payload.properties), ["resource", "id", "change"])
      assert.sameMembers([...payload.required], ["resource", "id", "change"])

      // Now the bytes: a real subscriber sees the documented opening
      // comment, then a bare `data:` frame whose JSON carries exactly the
      // documented payload fields — no `id:` or `event:` lines.
      const { base } = yield* startServer(
        Server.layer({ port: 0 }).pipe(Layer.provide([TestBuildInfoLayer, TestRepositoryLayers])),
      )

      const response = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/events`, { headers: { accept: "text/event-stream" } }),
      )
      assert.match(response.headers.get("content-type") ?? "", /^text\/event-stream/)
      const reader = response.body!.getReader()
      const opening = yield* Effect.promise(() => reader.read())
      assert.strictEqual(new TextDecoder().decode(opening.value), ": connected\n\n")

      const created = yield* Effect.promise(() =>
        fetch(`${base}/api/v1/projects`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ name: "SseShape", defaultWorkingDirectory: "/tmp" }),
        }).then((r) => r.json() as Promise<{ id: string }>),
      )
      const decoder = new TextDecoder()
      let raw = ""
      for (let attempt = 0; !raw.includes("\n\n"); attempt++) {
        assert.isBelow(attempt, 10, `no frame arrived; saw ${JSON.stringify(raw)}`)
        const chunk = yield* Effect.promise(() => reader.read())
        assert.isFalse(chunk.done, "the stream ended before delivering the event")
        raw += decoder.decode(chunk.value, { stream: true })
      }
      const frame = raw.slice(0, raw.indexOf("\n\n"))
      const lines = frame.split("\n")
      assert.strictEqual(lines.length, 1, `expected a single data line, got ${frame}`)
      assert.match(lines[0]!, /^data: /, "the frame must be data-only")
      const event = JSON.parse(lines[0]!.slice("data: ".length)) as Record<string, unknown>
      assert.sameMembers(Object.keys(event), ["resource", "id", "change"])
      assert.deepStrictEqual(event, { resource: "project", id: created.id, change: "created" })
    }),
  )

  it.effect("GET /api/v1/fs/check returns the documented payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get(
        "http://127.0.0.1/api/v1/fs/check?path=/definitely/not/here",
      )
      assert.strictEqual(response.status, 200)
      const body = (yield* response.json) as Record<string, unknown>
      assert.strictEqual(body["state"], "missing")
      const ref =
        operation("/api/v1/fs/check").responses["200"]!.content["application/json"]!.schema
      assert.deepStrictEqual(ref, { $ref: "#/components/schemas/FsCheckResponse" })
      const schema = componentSchema("FsCheckResponse")
      assert.sameMembers(Object.keys(schema.properties), ["path", "state", "checkedAt", "reason"])
      // reason is optional (absent when conclusive) — and deliberately not a
      // nullable required field, which the pinned Swift generator would drop.
      assert.sameMembers([...schema.required], ["path", "state", "checkedAt"])
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/fs/list returns the documented payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/fs/list?path=/")
      assert.strictEqual(response.status, 200)
      const body = (yield* response.json) as Record<string, unknown>
      assert.strictEqual(body["path"], "/")
      assert.isArray(body["entries"])

      const ref = operation("/api/v1/fs/list").responses["200"]!.content["application/json"]!.schema
      assert.deepStrictEqual(ref, { $ref: "#/components/schemas/FsListResponse" })
      const schema = componentSchema("FsListResponse")
      assert.sameMembers(Object.keys(schema.properties), ["path", "parent", "entries"])
      // parent is optional (absent at the root) — and deliberately not a
      // nullable required field, which the pinned Swift generator would drop.
      assert.sameMembers([...schema.required], ["path", "entries"])

      const entry = componentSchema("FsListEntry")
      assert.sameMembers(Object.keys(entry.properties), ["name", "path"])
      assert.sameMembers([...entry.required], ["name", "path"])

      // An unusable path is the tagged 422, same as canonicalize.
      const missing = yield* (yield* rawClient).get(
        "http://127.0.0.1/api/v1/fs/list?path=/definitely/not/here",
      )
      assert.strictEqual(missing.status, 422)
      const failure = (yield* missing.json) as Record<string, unknown>
      assert.strictEqual(failure["_tag"], "DirectoryUnavailable")
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )
})
