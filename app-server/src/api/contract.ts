import { Schema } from "effect"
import { HttpApi, HttpApiEndpoint, HttpApiGroup, OpenApi } from "effect/unstable/httpapi"

// The public HTTP contract. The server implementation (handlers.ts), the
// checked-in OpenAPI document (openapi.ts), and typed clients all derive from
// this module. Authoring conventions (pinned operation ids, schema
// identifiers, descriptions) live in AGENTS.md "OpenAPI Contract".

/** Default TCP port of a locally running App Server. */
export const DEFAULT_PORT = 7332

export const HealthResponse = Schema.Struct({
  status: Schema.Literal("ok"),
}).annotate({
  identifier: "HealthResponse",
  description: "Result of the liveness probe.",
})

export const VersionResponse = Schema.Struct({
  version: Schema.String.annotate({ description: "App Server release version." }),
  apiVersion: Schema.Literal("v1"),
  commit: Schema.String.annotate({
    description: 'Commit the executable was built from ("dev" when running from source).',
  }),
  builtAt: Schema.String.annotate({
    description: 'Build timestamp of the executable ("dev" when running from source).',
  }),
}).annotate({
  identifier: "VersionResponse",
  description: "Build and contract version information for the running server.",
})

/** A server-host absolute path. */
export const AbsolutePath = Schema.String.check(
  Schema.isStartsWith("/", { description: "a server-host absolute path (starting with /)" }),
)

export const ProjectName = Schema.NonEmptyString.annotate({
  description: "Human-readable project name.",
})

export const Project = Schema.Struct({
  id: Schema.String.annotate({ description: "UUIDv7 project id." }),
  name: ProjectName,
  defaultWorkingDirectory: Schema.String.annotate({
    description: "Canonical (symlink-resolved) absolute path new Threads and Terminals default to.",
  }),
  createdAt: Schema.String.annotate({ description: "Creation time (ISO 8601 UTC)." }),
  updatedAt: Schema.String.annotate({ description: "Last update time (ISO 8601 UTC)." }),
}).annotate({
  identifier: "Project",
  description: "A Project: the user-facing container organizing a body of work.",
})

export const ProjectList = Schema.Array(Project).annotate({
  identifier: "ProjectList",
  description: "All projects, newest first.",
})

export const CreateProjectRequest = Schema.Struct({
  name: ProjectName,
  defaultWorkingDirectory: AbsolutePath.annotate({
    description: "Existing directory; stored canonicalized (symlinks resolved).",
  }),
}).annotate({
  identifier: "CreateProjectRequest",
  description: "Payload for creating a project.",
})

// optionalKey (absent key), not optional (key-or-undefined): the undefined
// union renders as an anyOf-with-null the pinned Swift generator drops,
// which would strip these fields from the generated client entirely.
export const UpdateProjectRequest = Schema.Struct({
  name: Schema.optionalKey(ProjectName),
  defaultWorkingDirectory: Schema.optionalKey(
    AbsolutePath.annotate({
      description: "Existing directory; stored canonicalized (symlinks resolved).",
    }),
  ),
}).annotate({
  identifier: "UpdateProjectRequest",
  description: "Partial update; affects future Threads and Terminals only.",
})

export const DirectoryState = Schema.Literals([
  "available",
  "missing",
  "inaccessible",
  "not_directory",
  "unknown",
]).annotate({
  identifier: "DirectoryState",
  description: "Tagged result of a demand-driven directory health check.",
})

export const FsCheckResponse = Schema.Struct({
  path: Schema.String.annotate({
    description: "The checked path (canonicalized when the directory is available).",
  }),
  state: DirectoryState,
  checkedAt: Schema.String.annotate({ description: "Check time (ISO 8601 UTC)." }),
  // Absent when the state is conclusive (not null — see UpdateProjectRequest).
  reason: Schema.optionalKey(
    Schema.String.annotate({
      description:
        'Why the state is not conclusive (e.g. "timeout" for unknown); absent otherwise.',
    }),
  ),
}).annotate({
  identifier: "FsCheckResponse",
  description: "Result of a directory health check. Never persisted.",
})

export const TerminalStatus = Schema.Literals(["live", "ended"]).annotate({
  identifier: "TerminalStatus",
  description:
    "Public terminal lifecycle state: live (zmx session running) or ended (tombstone until deleted).",
})

export const TerminalCommand = Schema.Array(Schema.String).check(
  Schema.isMinLength(1, { description: "an exec-style argv with at least the program name" }),
)

export const Terminal = Schema.Struct({
  id: Schema.String.annotate({ description: "UUIDv7 terminal id." }),
  projectId: Schema.String.annotate({ description: "Owning project id." }),
  // Absent keys (not null) for optional fields — see UpdateProjectRequest.
  name: Schema.optionalKey(Schema.String.annotate({ description: "Mutable display label." })),
  command: Schema.optionalKey(
    TerminalCommand.annotate({
      description:
        "Immutable exec-style argv the terminal was launched with; absent for an interactive login shell.",
    }),
  ),
  initialWorkingDirectory: Schema.String.annotate({
    description: "Canonical directory the terminal was launched in (immutable).",
  }),
  status: TerminalStatus,
  createdAt: Schema.String.annotate({ description: "Creation time (ISO 8601 UTC)." }),
  updatedAt: Schema.String.annotate({ description: "Last update time (ISO 8601 UTC)." }),
  endedAt: Schema.optionalKey(
    Schema.String.annotate({
      description: "When the terminal was observed ended (ISO 8601 UTC); absent while live.",
    }),
  ),
}).annotate({
  identifier: "Terminal",
  description: "A durable, project-scoped terminal backed by a zmx session.",
})

export const TerminalList = Schema.Array(Terminal).annotate({
  identifier: "TerminalList",
  description: "Terminals, newest first.",
})

export const CreateTerminalRequest = Schema.Struct({
  projectId: Schema.String.annotate({ description: "Owning project id." }),
  name: Schema.optionalKey(Schema.String.annotate({ description: "Display label." })),
  command: Schema.optionalKey(
    TerminalCommand.annotate({
      description: "Exec-style argv to run instead of an interactive login shell.",
    }),
  ),
  workingDirectory: Schema.optionalKey(
    AbsolutePath.annotate({
      description:
        "Directory to launch in; defaults to the project's default working directory. Stored canonicalized.",
    }),
  ),
}).annotate({
  identifier: "CreateTerminalRequest",
  description: "Payload for creating a terminal. Creation starts the zmx session immediately.",
})

export const UpdateTerminalRequest = Schema.Struct({
  name: Schema.optionalKey(Schema.String.annotate({ description: "New display label." })),
}).annotate({
  identifier: "UpdateTerminalRequest",
  description: "Partial update; only the display label is mutable.",
})

// The error classes carry human `message`s so every consumer (the CLI above
// all) can print a real diagnostic — a TaggedErrorClass message is otherwise
// empty.

/** Unknown project id. */
export class ProjectNotFound extends Schema.TaggedErrorClass<ProjectNotFound>()(
  "ProjectNotFound",
  { projectId: Schema.String },
  {
    identifier: "ProjectNotFound",
    description: "No project exists with the given id.",
    httpApiStatus: 404,
  },
) {
  override get message(): string {
    return `no project with id ${this.projectId}`
  }
}

/** The directory failed validation; `state` carries the tagged failure. */
export class DirectoryUnavailable extends Schema.TaggedErrorClass<DirectoryUnavailable>()(
  "DirectoryUnavailable",
  {
    path: Schema.String,
    state: Schema.Literals(["missing", "inaccessible", "not_directory"]),
  },
  {
    identifier: "DirectoryUnavailable",
    description: "The directory does not exist, is not a directory, or cannot be read/traversed.",
    httpApiStatus: 422,
  },
) {
  override get message(): string {
    switch (this.state) {
      case "missing":
        return `directory ${this.path} does not exist`
      case "inaccessible":
        return `directory ${this.path} cannot be read or traversed`
      case "not_directory":
        return `${this.path} is not a directory`
    }
  }
}

/** The bounded directory check did not complete; retryable, fail-closed. */
export class DirectoryCheckTimedOut extends Schema.TaggedErrorClass<DirectoryCheckTimedOut>()(
  "DirectoryCheckTimedOut",
  { path: Schema.String },
  {
    identifier: "DirectoryCheckTimedOut",
    description:
      "The directory check did not complete within its bounded timeout. Retryable — a timeout is not proof the path is missing.",
    httpApiStatus: 422,
  },
) {
  override get message(): string {
    return `the check of directory ${this.path} timed out; retry`
  }
}

/** Unknown terminal id. */
export class TerminalNotFound extends Schema.TaggedErrorClass<TerminalNotFound>()(
  "TerminalNotFound",
  { terminalId: Schema.String },
  {
    identifier: "TerminalNotFound",
    description: "No terminal exists with the given id.",
    httpApiStatus: 404,
  },
) {
  override get message(): string {
    return `no terminal with id ${this.terminalId}`
  }
}

/**
 * The zmx multiplexer cannot be consulted (executable missing, inventory
 * failed, command timed out). Retryable: it says nothing about any
 * terminal's existence, so stored state stays untouched.
 */
export class ZmxUnavailable extends Schema.TaggedErrorClass<ZmxUnavailable>()(
  "ZmxUnavailable",
  { reason: Schema.String },
  {
    identifier: "ZmxUnavailable",
    description:
      "The zmx multiplexer cannot be consulted right now. Retryable; stored terminal state is untouched.",
    httpApiStatus: 503,
  },
) {
  override get message(): string {
    return this.reason
  }
}

/** The zmx session for a new terminal failed to launch; conclusive. */
export class TerminalLaunchFailed extends Schema.TaggedErrorClass<TerminalLaunchFailed>()(
  "TerminalLaunchFailed",
  { reason: Schema.String },
  {
    identifier: "TerminalLaunchFailed",
    description:
      "The terminal's zmx session failed to launch (bad command, immediate exit, or settle failure). The failed launch leaves no terminal record.",
    httpApiStatus: 422,
  },
) {
  override get message(): string {
    return this.reason
  }
}

/** Project deletion is restricted while it still owns terminals. */
export class ProjectHasTerminals extends Schema.TaggedErrorClass<ProjectHasTerminals>()(
  "ProjectHasTerminals",
  { projectId: Schema.String, terminalCount: Schema.Int },
  {
    identifier: "ProjectHasTerminals",
    description: "The project still owns terminals (live or ended); delete them first.",
    httpApiStatus: 409,
  },
) {
  override get message(): string {
    return `project ${this.projectId} still owns ${this.terminalCount} terminal(s); delete them first`
  }
}

const projectIdParam = { projectId: Schema.String }
const terminalIdParam = { terminalId: Schema.String }

export class V1 extends HttpApiGroup.make("v1")
  .add(
    HttpApiEndpoint.get("health", "/health", { success: HealthResponse })
      .annotate(OpenApi.Identifier, "getHealth")
      .annotate(OpenApi.Description, "Liveness probe: confirms the server is accepting requests."),
    HttpApiEndpoint.get("version", "/version", { success: VersionResponse })
      .annotate(OpenApi.Identifier, "getVersion")
      .annotate(OpenApi.Description, "Report the server's build metadata and API version."),
    HttpApiEndpoint.get("listProjects", "/projects", { success: ProjectList })
      .annotate(OpenApi.Identifier, "listProjects")
      .annotate(OpenApi.Description, "List all projects."),
    // Create returns 200 with the resource body (not 201): a per-endpoint
    // status annotation on the shared named Project schema would fork it into
    // a duplicate component in the document and every generated client.
    HttpApiEndpoint.post("createProject", "/projects", {
      payload: CreateProjectRequest,
      success: Project,
      error: [DirectoryUnavailable, DirectoryCheckTimedOut],
    })
      .annotate(OpenApi.Identifier, "createProject")
      .annotate(
        OpenApi.Description,
        "Create a project. The default working directory must be an existing, readable directory; it is stored canonicalized and never created by ATC.",
      ),
    HttpApiEndpoint.get("getProject", "/projects/:projectId", {
      params: projectIdParam,
      success: Project,
      error: ProjectNotFound,
    })
      .annotate(OpenApi.Identifier, "getProject")
      .annotate(OpenApi.Description, "Fetch one project by id."),
    HttpApiEndpoint.patch("updateProject", "/projects/:projectId", {
      params: projectIdParam,
      payload: UpdateProjectRequest,
      success: Project,
      error: [ProjectNotFound, DirectoryUnavailable, DirectoryCheckTimedOut],
    })
      .annotate(OpenApi.Identifier, "updateProject")
      .annotate(
        OpenApi.Description,
        "Update a project's name and/or default working directory. Directory changes affect future Threads and Terminals only.",
      ),
    HttpApiEndpoint.delete("deleteProject", "/projects/:projectId", {
      params: projectIdParam,
      error: [ProjectNotFound, ProjectHasTerminals],
    })
      .annotate(OpenApi.Identifier, "deleteProject")
      .annotate(
        OpenApi.Description,
        "Delete the project record. Restricted while the project still owns terminals; never touches the filesystem or any directory.",
      ),
    HttpApiEndpoint.get("listTerminals", "/terminals", {
      query: {
        projectId: Schema.optionalKey(
          Schema.String.annotate({ description: "Restrict the listing to one project." }),
        ),
      },
      success: TerminalList,
    })
      .annotate(OpenApi.Identifier, "listTerminals")
      .annotate(
        OpenApi.Description,
        "List terminals, reconciled against the zmx inventory on a best-effort basis (an unavailable inventory returns the stored state untouched).",
      ),
    HttpApiEndpoint.post("createTerminal", "/terminals", {
      payload: CreateTerminalRequest,
      success: Terminal,
      error: [
        ProjectNotFound,
        DirectoryUnavailable,
        DirectoryCheckTimedOut,
        ZmxUnavailable,
        TerminalLaunchFailed,
      ],
    })
      .annotate(OpenApi.Identifier, "createTerminal")
      .annotate(
        OpenApi.Description,
        "Create a terminal and start its zmx session immediately (an interactive login shell, or the exec-style command argv). A failed launch leaves no record.",
      ),
    HttpApiEndpoint.get("getTerminal", "/terminals/:terminalId", {
      params: terminalIdParam,
      success: Terminal,
      error: TerminalNotFound,
    })
      .annotate(OpenApi.Identifier, "getTerminal")
      .annotate(OpenApi.Description, "Fetch one terminal by id, reconciled on read."),
    HttpApiEndpoint.patch("updateTerminal", "/terminals/:terminalId", {
      params: terminalIdParam,
      payload: UpdateTerminalRequest,
      success: Terminal,
      error: TerminalNotFound,
    })
      .annotate(OpenApi.Identifier, "updateTerminal")
      .annotate(OpenApi.Description, "Update a terminal's display label (the only mutable field)."),
    HttpApiEndpoint.delete("deleteTerminal", "/terminals/:terminalId", {
      params: terminalIdParam,
      error: [TerminalNotFound, ZmxUnavailable],
    })
      .annotate(OpenApi.Identifier, "deleteTerminal")
      .annotate(
        OpenApi.Description,
        "Delete a terminal: kill its zmx session if present, verify absence, then remove the record (tombstones included).",
      ),
    // WebSocket upgrade endpoint: declared in the contract for the typed
    // route tree and params, excluded from the OpenAPI document (REST
    // clients and the Swift generator cannot represent an upgrade). Wire
    // protocol: binary frames are terminal bytes both ways; text frames are
    // JSON control messages ({"type":"resize","cols","rows"} from the
    // client; {"type":"ping"}/{"type":"pong"} keepalives). Close
    // vocabulary — the single statement of it, mirrored by the bridge: 1000
    // terminal_ended is authoritative (confirmed absent, tombstone
    // written); 1011 attach_failed / zmx_unavailable / ping_timeout are
    // retryable and say nothing about the terminal's existence. Clients
    // detach by closing 1000 "detach" (the session keeps running).
    HttpApiEndpoint.get("attachTerminal", "/terminals/:terminalId/attach", {
      params: terminalIdParam,
      query: {
        cols: Schema.optionalKey(
          Schema.String.annotate({ description: "Initial terminal width (columns)." }),
        ),
        rows: Schema.optionalKey(
          Schema.String.annotate({ description: "Initial terminal height (rows)." }),
        ),
      },
    })
      .annotate(OpenApi.Identifier, "attachTerminal")
      .annotate(OpenApi.Exclude, true)
      .annotate(
        OpenApi.Description,
        "WebSocket attach: upgrade to a live bidirectional bridge onto the terminal's zmx session.",
      ),
    HttpApiEndpoint.get("checkDirectory", "/fs/check", {
      query: { path: AbsolutePath },
      success: FsCheckResponse,
    })
      .annotate(OpenApi.Identifier, "checkDirectory")
      .annotate(
        OpenApi.Description,
        "Demand-driven directory health check with a bounded timeout. Takes a server-host absolute path; the result is never persisted.",
      ),
  )
  // .prefix only applies to endpoints added above it — add new endpoints
  // before this line so they stay under /api/v1.
  .prefix("/api/v1") {}

// The OpenAPI version is the contract version, not the build version, so this
// module stays client-safe with no dependency on server internals.
export class Api extends HttpApi.make("atc")
  .add(V1)
  .annotateMerge(
    OpenApi.annotations({
      title: "ATC App Server API",
      version: "v1",
      servers: [
        {
          url: "http://127.0.0.1:{port}",
          description: "Local App Server",
          variables: { port: { default: `${DEFAULT_PORT}` } },
        },
      ],
    }),
  ) {}
