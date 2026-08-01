import { assert, describe, it } from "@effect/vitest"
import { BunHttpServer } from "@effect/platform-bun"
import { Context, Effect, Layer } from "effect"
import { HttpClient, HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"
import { HttpApi, OpenApi } from "effect/unstable/httpapi"
import { Api, DEFAULT_PORT } from "../src/api.ts"
import { openApiDocument, openApiJson } from "../src/openapi.ts"
import * as Server from "../src/server.ts"
import { appServerRoot } from "./blackbox.ts"
import { TestBuildInfoLayer, testBuildInfo } from "./testBuildInfo.ts"

// Contract-generation tests: the document must be deterministic, match the
// checked-in artifact, cover every contract operation, and agree with what
// the runtime routes actually return. Runtime behavior itself is covered
// separately in api.test.ts, so failures here point at the document, not the
// handlers.

const operation = (path: string) =>
  (openApiDocument.paths[path]?.get ?? assert.fail(`no GET operation documented for ${path}`)) as {
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
        // OpenApi.Exclude drops an endpoint from the document by design.
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

    const operationIds = Object.keys(openApiDocument.paths).map((path) => {
      const id = operation(path).operationId
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
    Server.routes.pipe(Layer.provide(TestBuildInfoLayer)),
  )
  return HttpClient.make(
    Effect.fnUntraced(function* (request) {
      const serverRequest = HttpServerRequest.fromClientRequest(request)
      const response = yield* handler.pipe(
        Effect.provideService(HttpServerRequest.HttpServerRequest, serverRequest),
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
    assert.sameMembers(Object.keys(openApiDocument.paths), ["/api/v1/health", "/api/v1/version"])
  })

  it.effect("GET /api/v1/health returns the documented payload", () =>
    Effect.gen(function* () {
      const response = yield* (yield* rawClient).get("http://in-process/api/v1/health")
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
      const response = yield* (yield* rawClient).get("http://in-process/api/v1/version")
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
})
