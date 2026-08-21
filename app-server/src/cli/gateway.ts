import { Console, Effect, FileSystem, Option } from "effect"
import { Argument, Command, Flag } from "effect/unstable/cli"
import { HttpClient, HttpClientRequest } from "effect/unstable/http"
import { AppConfig, CONTEXT_VARIABLES } from "../platform/config.ts"
import * as Cli from "./cli.ts"

// The agent gateway (ATC-131): `atc api`, `atc context`, `atc capabilities`.
//
// The HTTP API is the complete, canonical App Server interface; `atc api`
// gives agents and scripts method-and-path access to all of it, so a curated
// command only exists where it genuinely improves on raw API access. The
// gateway is a plain HTTP passthrough over the same base-URL seam the
// curated commands use — the contract-derived client exposes only typed
// per-operation functions, and the server validates every request against
// the contract regardless of origin.

const API_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const

const methodArgument = Argument.string("method").pipe(
  Argument.mapTryCatch(
    (raw) => {
      const upper = raw.toUpperCase()
      const method = API_METHODS.find((candidate) => candidate === upper)
      if (method === undefined) throw new Error()
      return method
    },
    () => `method must be one of ${API_METHODS.join(", ")} (case-insensitive)`,
  ),
  Argument.withDescription(`HTTP method: ${API_METHODS.join(", ")} (case-insensitive)`),
)

/**
 * Join a server-relative path onto the configured base URL. Strict and
 * fallible: network-path forms (`//host`, and `/\host` — the URL parser
 * treats backslash as slash) would silently re-target the request to
 * another origin, so they are rejected up front and the resolved origin is
 * verified unchanged. An endpoint with a path prefix (e.g.
 * `http://host:1234/prefix`) keeps it, matching the typed client's joining.
 */
const requestUrl = (baseUrl: URL, requestPath: string): Effect.Effect<URL, Error> => {
  if (!requestPath.startsWith("/") || /^\/[/\\]/.test(requestPath)) {
    return Effect.fail(
      new Error(
        `path must be server-relative (start with /, but not // or /\\), got "${requestPath}"`,
      ),
    )
  }
  const prefix = baseUrl.pathname.replace(/\/$/, "")
  return Effect.try({
    try: () => new URL(baseUrl.origin + prefix + requestPath),
    catch: () => new Error(`cannot build a request URL from "${requestPath}"`),
  }).pipe(
    Effect.filterOrFail(
      (url) => url.origin === baseUrl.origin,
      () => new Error(`path "${requestPath}" escapes the configured endpoint`),
    ),
  )
}

/** Whether a response body is text to print as lines (JSON, problem
 * details, plain text); anything else is raw bytes. */
const isTextual = (contentType: string | undefined): boolean =>
  contentType === undefined || /json|text\/|xml/i.test(contentType)

/** Request body source: a file path, or "-" for stdin. Bytes, not text —
 * an image upload (createThreadAttachment) rides the same flag. */
const readRequestBody = (source: string) =>
  source === "-"
    ? Effect.tryPromise({
        try: () => Bun.stdin.bytes(),
        catch: () => new Error("cannot read the request body from stdin"),
      })
    : Effect.gen(function* () {
        const fileSystem = yield* FileSystem.FileSystem
        return yield* fileSystem.readFile(source)
      })

export const api = Command.make(
  "api",
  {
    method: methodArgument,
    path: Argument.string("path").pipe(
      Argument.withDescription("Server-relative request path (e.g. /api/v1/projects)"),
    ),
    input: Flag.string("input").pipe(
      Flag.optional,
      Flag.withDescription("Request body: a file path, or - to read stdin"),
    ),
    contentType: Flag.string("content-type").pipe(
      Flag.withDefault("application/json"),
      Flag.withDescription(
        "Content-Type of the request body (default application/json; e.g. image/png for an attachment upload)",
      ),
    ),
  },
  ({ method, path: requestPath, input, contentType }) =>
    Cli.withCliContext(
      "api",
      Effect.gen(function* () {
        const baseUrl = yield* Cli.resolveBaseUrl
        const url = yield* requestUrl(baseUrl, requestPath)
        const client = yield* HttpClient.HttpClient
        const body = Option.isSome(input) ? yield* readRequestBody(input.value) : undefined
        const request = HttpClientRequest.make(method)(url, {
          acceptJson: true,
        }).pipe((base) =>
          body === undefined ? base : HttpClientRequest.bodyUint8Array(base, body, contentType),
        )
        const response = yield* client.execute(request)
        if (response.status >= 200 && response.status < 300) {
          // Success bodies pass through unchanged: text (JSON) plus the
          // trailing newline — an empty response prints nothing — and a
          // binary body (an attachment download) byte for byte.
          if (!isTextual(response.headers["content-type"])) {
            const bytes = yield* response.arrayBuffer
            yield* Effect.promise(() => Bun.write(Bun.stdout, new Uint8Array(bytes)))
            return
          }
          const text = yield* response.text
          if (text !== "") yield* Console.log(text)
          return
        }
        const text = yield* response.text
        // API failures: the machine-readable error body, then the one-line
        // diagnostic — both on stderr, exit 1 like every other failure.
        if (text !== "") yield* Console.error(text)
        return yield* Effect.fail(new Error(`server returned HTTP ${response.status}`))
      }),
    ),
).pipe(
  Command.withDescription(
    "Send one request to any App Server API endpoint (the generic gateway; see /openapi.json)",
  ),
)

const jsonFlag = Flag.boolean("json").pipe(
  Flag.withDescription("Emit machine-readable JSON instead of text"),
)

export const context = Command.make("context", { json: jsonFlag }, ({ json }) =>
  Cli.withCliContext(
    "context",
    Effect.gen(function* () {
      const config = yield* AppConfig
      // Reported in vocabulary order, present variables only — absent ones
      // are omitted entirely, never invented.
      if (json) {
        yield* Console.log(JSON.stringify(config.context, null, 2))
        return
      }
      for (const name of CONTEXT_VARIABLES) {
        const value = config.context[name]
        if (value !== undefined) yield* Console.log(`${name}=${value}`)
      }
    }),
  ),
).pipe(
  Command.withDescription(
    "Report the ATC_* agent-context variables present in this process's environment",
  ),
)

/**
 * The stable capability description for agents. Version the shape: additions
 * bump nothing, breaking changes bump capabilitiesVersion.
 */
const CAPABILITIES = {
  capabilitiesVersion: 1,
  api: {
    command: "atc api <method> <path> [--input <file|->] [--content-type <type>]",
    description:
      "Complete access to the canonical App Server HTTP API: every operation via GET, POST, PUT, PATCH, or DELETE, a body from a file or stdin (JSON by default, any Content-Type via --content-type), the response on stdout (JSON as text, anything else byte for byte).",
    example: "atc api GET /api/v1/projects",
  },
  openapi: {
    path: "/openapi.json",
    description:
      "The full OpenAPI document (every operation and schema), served by the App Server.",
    example: "atc api GET /openapi.json",
  },
  context: {
    command: "atc context --json",
    description: "The ATC_* context variables present in this process.",
    variables: CONTEXT_VARIABLES,
  },
  workflows: [
    { command: "atc serve", description: "Run the App Server in the foreground." },
    {
      command: "atc project create --name <name> --directory <dir>",
      description: "Create a project (relative directories resolve against the caller's cwd).",
    },
    {
      command: "atc terminal create --project <id> [command...]",
      description: "Create a durable terminal and start its session immediately.",
    },
    {
      command: "atc terminal attach <terminal-id>",
      description: "Attach the local TTY to a live terminal session (detach with Ctrl-]).",
    },
  ],
} as const

export const capabilities = Command.make("capabilities", { json: jsonFlag }, ({ json }) =>
  json
    ? Console.log(JSON.stringify(CAPABILITIES, null, 2))
    : Console.log(
        [
          `${CAPABILITIES.api.command} — ${CAPABILITIES.api.description}`,
          `${CAPABILITIES.openapi.example} — ${CAPABILITIES.openapi.description}`,
          `${CAPABILITIES.context.command} — ${CAPABILITIES.context.description}`,
          ...CAPABILITIES.workflows.map((w) => `${w.command} — ${w.description}`),
        ].join("\n"),
      ),
).pipe(Command.withDescription("Describe the CLI's agent-facing capabilities (stable, versioned)"))
