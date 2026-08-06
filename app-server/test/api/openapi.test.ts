import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Context, Effect, Layer } from "effect"
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
import { TestRepositoryLayers } from "../testLayers.ts"

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
      // fromClientRequest synthesizes no Host header; the local-trust guard in
      // Server.routes requires a loopback one just like a real client sends.
      const serverRequest = HttpServerRequest.fromClientRequest(
        HttpClientRequest.setHeader(request, "host", "127.0.0.1"),
      )
      const response = yield* handler.pipe(
        Effect.provideService(HttpServerRequest.HttpServerRequest, serverRequest),
        // The internal Claude hook route resolves its service per request.
        Effect.provide(ClaudeHooks.layer),
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
      "/api/v1/version",
      "/api/v1/projects",
      "/api/v1/projects/{projectId}",
      "/api/v1/terminals",
      "/api/v1/terminals/{terminalId}",
      "/api/v1/threads",
      "/api/v1/threads/{threadId}",
      "/api/v1/threads/{threadId}/archive",
      "/api/v1/threads/{threadId}/unarchive",
      "/api/v1/threads/{threadId}/terminal",
      "/api/v1/agents",
      "/api/v1/agents/{agentId}",
      "/api/v1/events",
      "/api/v1/fs/check",
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
      assert.deepStrictEqual(yield* response.json, { _tag: "ProjectNotFound", projectId: "nope" })
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
      // name/linkedTerminalId/archivedAt are absent optional keys, never null.
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
      assert.deepStrictEqual(yield* response.json, { _tag: "ThreadNotFound", threadId: "nope" })
      assert.deepStrictEqual(
        operation("/api/v1/threads/{threadId}").responses["404"]!.content["application/json"]!
          .schema,
        { $ref: "#/components/schemas/ThreadNotFoundJsonEncoding" },
      )
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("/api/v1/threads/{threadId}/archive and /unarchive round-trip the Thread schema", () =>
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
      assert.deepStrictEqual(yield* missing.json, { _tag: "AgentNotFound", agentId: "nope" })
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
  )

  it.effect("GET /api/v1/events serves the documented SSE stream", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://127.0.0.1/api/v1/events")
      assert.strictEqual(response.status, 200)
      assert.match(response.headers["content-type"] ?? "", /^text\/event-stream/)

      // The documented payload is the SSE envelope whose data line carries a
      // JSON-encoded ResourceChangedEvent...
      const content = operation("/api/v1/events").responses["200"]!.content["text/event-stream"]!
      const envelope = content.schema as unknown as {
        properties: { data: { $ref?: string } }
      }
      assert.deepStrictEqual(envelope.properties.data, {
        $ref: "#/components/schemas/ResourceChangedEventJsonEncoding",
      })
      assert.deepStrictEqual(componentSchema("ResourceChangedEventJsonEncoding") as unknown, {
        type: "string",
        contentMediaType: "application/json",
      })

      // ...and the plain payload schema is emitted as an ordinary component
      // (openapi.ts), so generated clients get a real model type to decode
      // that JSON into. Delivery itself is covered in api.test.ts.
      const payload = componentSchema("ResourceChangedEvent")
      assert.sameMembers(Object.keys(payload.properties), ["resource", "id", "change"])
      assert.sameMembers([...payload.required], ["resource", "id", "change"])
    }).pipe(Effect.provide(BunHttpServer.layerHttpServices)),
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
})
