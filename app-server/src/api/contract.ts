import { Schema } from "effect"
import {
  HttpApi,
  HttpApiEndpoint,
  HttpApiGroup,
  HttpApiSchema,
  OpenApi,
} from "effect/unstable/httpapi"

// The public HTTP contract. The server implementation (handlers.ts), the
// checked-in OpenAPI document (openapi.ts), and typed clients all derive from
// this module. The authoring conventions are stated inline where they bite
// (pinned operation ids, optionalKey over optional, create returning 200).
//
// No listing endpoint paginates, deliberately: a single-user server's
// collections stay small enough to return whole, and every client consumes
// full lists. Revisit only when a real client needs paging.

/** Default TCP port of a locally running App Server. */
export const DEFAULT_PORT = 7331

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
  // Deliberately NOT a timestamp(): the literal "dev" would break a Date
  // decode in generated clients.
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

/** A wire timestamp: RFC 3339 UTC with millisecond precision (`Date.toISOString()`). */
const timestamp = (description: string) =>
  Schema.String.annotate({ format: "date-time", description })

export const ProjectName = Schema.NonEmptyString.annotate({
  description: "Human-readable project name.",
})

export const Project = Schema.Struct({
  id: Schema.String.annotate({ description: "UUIDv7 project id." }),
  name: ProjectName,
  defaultWorkingDirectory: Schema.String.annotate({
    description: "Canonical (symlink-resolved) absolute path new Threads and Terminals default to.",
  }),
  createdAt: timestamp("Creation time."),
  updatedAt: timestamp("Last update time."),
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
  checkedAt: timestamp("Check time."),
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

export const FsListEntry = Schema.Struct({
  name: Schema.String.annotate({ description: "Directory name within the listed directory." }),
  path: Schema.String.annotate({
    description: "Absolute path of the subdirectory (not symlink-resolved).",
  }),
}).annotate({
  identifier: "FsListEntry",
  description: "One subdirectory of a listed directory.",
})

export const FsListResponse = Schema.Struct({
  path: Schema.String.annotate({
    description: "Canonical (symlink-resolved) absolute path of the listed directory.",
  }),
  // Absent at the filesystem root (not null — see UpdateProjectRequest).
  parent: Schema.optionalKey(
    Schema.String.annotate({
      description: "Parent directory of `path`; absent at the filesystem root.",
    }),
  ),
  entries: Schema.Array(FsListEntry).annotate({
    description:
      "Subdirectories only — non-recursive, dotfolders excluded, symlinks included when they " +
      "resolve to directories, sorted case-insensitively by name.",
  }),
}).annotate({
  identifier: "FsListResponse",
  description: "A directory listing for the server-backed folder browser. Never persisted.",
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
  threadId: Schema.optionalKey(
    Schema.String.annotate({
      description:
        "Owning thread id when this terminal is a Thread's TUI terminal; absent otherwise.",
    }),
  ),
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
  createdAt: timestamp("Creation time."),
  updatedAt: timestamp("Last update time."),
  endedAt: Schema.optionalKey(
    timestamp("When the terminal was observed ended; absent while live."),
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

/** The built-in agent registry slugs (adding an agent extends this tuple). */
export const AGENT_IDS = ["codex", "claude-code"] as const

export const AgentId = Schema.Literals(AGENT_IDS).annotate({
  identifier: "AgentId",
  description: "Built-in agent registry slug.",
})

/** Whether `id` names a built-in agent (narrows to the registry slug union). */
export const isAgentId = (id: string): id is typeof AgentId.Type =>
  (AGENT_IDS as ReadonlyArray<string>).includes(id)

export const Agent = Schema.Struct({
  id: AgentId,
  available: Schema.Boolean.annotate({
    description: "Whether the agent's CLI is installed and resolvable right now.",
  }),
  // Absent keys (not null) for optional fields — see UpdateProjectRequest.
  reason: Schema.optionalKey(
    Schema.String.annotate({
      description: "Actionable install-or-configure hint; absent when available.",
    }),
  ),
  detectedVersion: Schema.optionalKey(
    Schema.String.annotate({
      description: "Installed CLI version, when it could be determined.",
    }),
  ),
}).annotate({
  identifier: "Agent",
  description:
    "A built-in agent provider: availability report, detected on demand and never persisted.",
})

export const AgentList = Schema.Array(Agent).annotate({
  identifier: "AgentList",
  description: "The built-in agent registry.",
})

export const ThreadActivityState = Schema.Literals([
  "idle",
  "working",
  "needs_input",
  "unknown",
]).annotate({
  identifier: "ThreadActivityState",
  description:
    "Normalized activity of the thread's agent session, derived from live provider evidence only; `unknown` means no evidence — never a guess.",
})

export const Thread = Schema.Struct({
  id: Schema.String.annotate({ description: "UUIDv7 thread id." }),
  projectId: Schema.String.annotate({ description: "Owning project id." }),
  agentId: AgentId,
  // Absent keys (not null) for optional fields — see UpdateProjectRequest.
  name: Schema.optionalKey(Schema.String.annotate({ description: "Mutable display label." })),
  workingDirectory: Schema.String.annotate({
    description: "Canonical directory the thread's agent session works in (immutable).",
  }),
  activityState: ThreadActivityState,
  linkedTerminalId: Schema.optionalKey(
    Schema.String.annotate({
      description: "The thread's live TUI terminal, when one is open (derived; never required).",
    }),
  ),
  pinnedAt: Schema.optionalKey(
    timestamp("When the thread was pinned; absent while unpinned. The pin-order key."),
  ),
  archivedAt: Schema.optionalKey(timestamp("When the thread was archived; absent while active.")),
  createdAt: timestamp("Creation time."),
  updatedAt: timestamp("Last update time."),
}).annotate({
  identifier: "Thread",
  description:
    "A durable Thread: the primary unit of agent work. Its ATC identity is separate from provider identity, and it never requires a Terminal.",
})

export const ThreadList = Schema.Array(Thread).annotate({
  identifier: "ThreadList",
  description: "Threads, newest first.",
})

export const CreateThreadRequest = Schema.Struct({
  projectId: Schema.String.annotate({ description: "Owning project id." }),
  agentId: AgentId,
  name: Schema.optionalKey(Schema.String.annotate({ description: "Display label." })),
  workingDirectory: Schema.optionalKey(
    AbsolutePath.annotate({
      description:
        "Directory the agent session will work in; defaults to the project's default working directory. Stored canonicalized; immutable afterwards.",
    }),
  ),
}).annotate({
  identifier: "CreateThreadRequest",
  description:
    "Payload for creating a thread. Creation is local-only: the durable record is written and no provider session is created until the first interaction.",
})

export const UpdateThreadRequest = Schema.Struct({
  name: Schema.optionalKey(Schema.String.annotate({ description: "New display label." })),
}).annotate({
  identifier: "UpdateThreadRequest",
  description: "Partial update; only the display label is mutable.",
})

export const ResourceChangedEvent = Schema.Struct({
  resource: Schema.Literals(["project", "thread", "terminal"]).annotate({
    description: "Which resource collection changed.",
  }),
  id: Schema.String.annotate({ description: "Id of the changed resource." }),
  change: Schema.Literals(["created", "updated", "deleted"]).annotate({
    description:
      'What happened. Every field-level transition (status, archive state, activity) is "updated".',
  }),
}).annotate({
  identifier: "ResourceChangedEvent",
  description:
    "Thin invalidation event: names what changed, never carries resource state. Clients coalesce events and refetch through the corresponding GETs, which stay the single source of truth.",
})

// Every error carries a required `message` field ON THE WIRE, computed from
// the structured fields by the class's own constructor — the one place each
// message rule lives. Construction sites never pass it, every consumer (the
// CLI, the Swift client) gets a printable diagnostic without re-deriving it,
// and decode recomputes it so it can never disagree with the fields.

/** The wire `message` field every tagged error carries. */
const errorMessage = Schema.String.annotate({
  description: "Human-readable diagnostic derived from the structured fields.",
})

/** Unknown project id. */
export class ProjectNotFound extends Schema.TaggedErrorClass<ProjectNotFound>()(
  "ProjectNotFound",
  { projectId: Schema.String, message: errorMessage },
  {
    identifier: "ProjectNotFound",
    description: "No project exists with the given id.",
    httpApiStatus: 404,
  },
) {
  constructor(props: { readonly projectId: string }) {
    super({ ...props, message: `no project with id ${props.projectId}` })
  }
}

const UnavailableState = Schema.Literals(["missing", "inaccessible", "not_directory"])
type UnavailableState = typeof UnavailableState.Type

const directoryUnavailableMessage = (props: {
  readonly path: string
  readonly state: UnavailableState
}): string => {
  switch (props.state) {
    case "missing":
      return `directory ${props.path} does not exist`
    case "inaccessible":
      return `directory ${props.path} cannot be read or traversed`
    case "not_directory":
      return `${props.path} is not a directory`
  }
}

/** The directory failed validation; `state` carries the tagged failure. */
export class DirectoryUnavailable extends Schema.TaggedErrorClass<DirectoryUnavailable>()(
  "DirectoryUnavailable",
  {
    path: Schema.String,
    state: UnavailableState,
    message: errorMessage,
  },
  {
    identifier: "DirectoryUnavailable",
    description: "The directory does not exist, is not a directory, or cannot be read/traversed.",
    httpApiStatus: 422,
  },
) {
  constructor(props: { readonly path: string; readonly state: UnavailableState }) {
    super({ ...props, message: directoryUnavailableMessage(props) })
  }
}

/** The bounded directory check did not complete; retryable, fail-closed. */
export class DirectoryCheckTimedOut extends Schema.TaggedErrorClass<DirectoryCheckTimedOut>()(
  "DirectoryCheckTimedOut",
  { path: Schema.String, message: errorMessage },
  {
    identifier: "DirectoryCheckTimedOut",
    description:
      "The directory check did not complete within its bounded timeout. Retryable — a timeout is not proof the path is missing.",
    httpApiStatus: 422,
  },
) {
  constructor(props: { readonly path: string }) {
    super({ ...props, message: `the check of directory ${props.path} timed out; retry` })
  }
}

/** Unknown terminal id. */
export class TerminalNotFound extends Schema.TaggedErrorClass<TerminalNotFound>()(
  "TerminalNotFound",
  { terminalId: Schema.String, message: errorMessage },
  {
    identifier: "TerminalNotFound",
    description: "No terminal exists with the given id.",
    httpApiStatus: 404,
  },
) {
  constructor(props: { readonly terminalId: string }) {
    super({ ...props, message: `no terminal with id ${props.terminalId}` })
  }
}

/**
 * The zmx multiplexer cannot be consulted (executable missing, inventory
 * failed, command timed out). Retryable: it says nothing about any
 * terminal's existence, so stored state stays untouched.
 */
export class ZmxUnavailable extends Schema.TaggedErrorClass<ZmxUnavailable>()(
  "ZmxUnavailable",
  { reason: Schema.String, message: errorMessage },
  {
    identifier: "ZmxUnavailable",
    description:
      "The zmx multiplexer cannot be consulted right now. Retryable; stored terminal state is untouched.",
    httpApiStatus: 503,
  },
) {
  constructor(props: { readonly reason: string }) {
    super({ ...props, message: props.reason })
  }
}

/** The zmx session for a new terminal failed to launch; conclusive. */
export class TerminalLaunchFailed extends Schema.TaggedErrorClass<TerminalLaunchFailed>()(
  "TerminalLaunchFailed",
  { reason: Schema.String, message: errorMessage },
  {
    identifier: "TerminalLaunchFailed",
    description:
      "The terminal's zmx session failed to launch (bad command, immediate exit, or settle failure). The failed launch leaves no terminal record.",
    httpApiStatus: 422,
  },
) {
  constructor(props: { readonly reason: string }) {
    super({ ...props, message: props.reason })
  }
}

/** Unknown agent registry slug. */
export class AgentNotFound extends Schema.TaggedErrorClass<AgentNotFound>()(
  "AgentNotFound",
  { agentId: Schema.String, message: errorMessage },
  {
    identifier: "AgentNotFound",
    description: "No built-in agent exists with the given id.",
    httpApiStatus: 404,
  },
) {
  constructor(props: { readonly agentId: string }) {
    super({ ...props, message: `no agent with id ${props.agentId}` })
  }
}

/** Unknown thread id. */
export class ThreadNotFound extends Schema.TaggedErrorClass<ThreadNotFound>()(
  "ThreadNotFound",
  { threadId: Schema.String, message: errorMessage },
  {
    identifier: "ThreadNotFound",
    description: "No thread exists with the given id.",
    httpApiStatus: 404,
  },
) {
  constructor(props: { readonly threadId: string }) {
    super({ ...props, message: `no thread with id ${props.threadId}` })
  }
}

/** The thread is archived; unarchive it before opening or driving it. */
export class ThreadArchived extends Schema.TaggedErrorClass<ThreadArchived>()(
  "ThreadArchived",
  { threadId: Schema.String, message: errorMessage },
  {
    identifier: "ThreadArchived",
    description: "The thread is archived; unarchive it first.",
    httpApiStatus: 409,
  },
) {
  constructor(props: { readonly threadId: string }) {
    super({ ...props, message: `thread ${props.threadId} is archived; unarchive it first` })
  }
}

/** The thread's agent provider cannot be used right now. Retryable. */
export class ProviderUnavailable extends Schema.TaggedErrorClass<ProviderUnavailable>()(
  "ProviderUnavailable",
  { agentId: Schema.String, reason: Schema.String, message: errorMessage },
  {
    identifier: "ProviderUnavailable",
    description:
      "The thread's agent provider cannot be used right now (not installed, not ready, or unreachable). Retryable once the cause is fixed.",
    httpApiStatus: 503,
  },
) {
  constructor(props: { readonly agentId: string; readonly reason: string }) {
    super({ ...props, message: props.reason })
  }
}

/** The provider session cannot be driven as asked; nothing was adopted. */
export class ProviderSessionConflict extends Schema.TaggedErrorClass<ProviderSessionConflict>()(
  "ProviderSessionConflict",
  { threadId: Schema.String, reason: Schema.String, message: errorMessage },
  {
    identifier: "ProviderSessionConflict",
    description:
      "The thread's provider session cannot be driven as requested (resume failed, identity mismatch, or a concurrent writer). Fail-closed: no changed identity was adopted.",
    httpApiStatus: 409,
  },
) {
  constructor(props: { readonly threadId: string; readonly reason: string }) {
    super({ ...props, message: props.reason })
  }
}

/** The thread's agent session is actively working; retry once the turn ends. */
export class ThreadBusy extends Schema.TaggedErrorClass<ThreadBusy>()(
  "ThreadBusy",
  { threadId: Schema.String, message: errorMessage },
  {
    identifier: "ThreadBusy",
    description: "The thread's agent session is actively working; retry once the turn completes.",
    httpApiStatus: 409,
  },
) {
  constructor(props: { readonly threadId: string }) {
    super({
      ...props,
      message: `thread ${props.threadId} is working; retry once the turn completes`,
    })
  }
}

const projectIdParam = { projectId: Schema.String }
const terminalIdParam = { terminalId: Schema.String }
const threadIdParam = { threadId: Schema.String }

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
      error: [ProjectNotFound, ZmxUnavailable],
    })
      .annotate(OpenApi.Identifier, "deleteProject")
      .annotate(
        OpenApi.Description,
        "Delete the project and everything it owns: every thread (archived included) and every terminal (ended tombstones included) is deleted through the normal delete flows, then the project record. Never touches the filesystem or any directory. A mid-cascade failure leaves already-deleted children deleted; retrying finishes the job.",
      ),
    HttpApiEndpoint.get("listTerminals", "/terminals", {
      query: {
        projectId: Schema.optionalKey(
          Schema.String.annotate({ description: "Restrict the listing to one project." }),
        ),
        threadId: Schema.optionalKey(
          Schema.String.annotate({
            description: "Restrict the listing to one thread's terminals.",
          }),
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
    // clients and the Swift generator cannot represent an upgrade). The
    // client-facing protocol spec — framing, control frames, close
    // vocabulary, sizing — is packages/attach-protocol/README.md with
    // shared test vectors in its fixtures/; the code authority TypeScript
    // sides import is terminals/attachProtocol.ts.
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
    HttpApiEndpoint.get("listThreads", "/threads", {
      query: {
        projectId: Schema.optionalKey(
          Schema.String.annotate({ description: "Restrict the listing to one project." }),
        ),
        archived: Schema.optionalKey(
          Schema.Literals(["true", "false", "all"]).annotate({
            description:
              '"true" lists archived threads only, "all" lists active and archived together; ' +
              'omitted (or "false") lists active threads only.',
          }),
        ),
      },
      success: ThreadList,
    })
      .annotate(OpenApi.Identifier, "listThreads")
      .annotate(
        OpenApi.Description,
        "List threads, newest first. Archived threads are excluded unless requested.",
      ),
    HttpApiEndpoint.post("createThread", "/threads", {
      payload: CreateThreadRequest,
      success: Thread,
      error: [ProjectNotFound, DirectoryUnavailable, DirectoryCheckTimedOut],
    })
      .annotate(OpenApi.Identifier, "createThread")
      .annotate(
        OpenApi.Description,
        "Create a thread: a durable local record only. The provider session materializes on the thread's first interaction, from whichever surface initiates it.",
      ),
    HttpApiEndpoint.get("getThread", "/threads/:threadId", {
      params: threadIdParam,
      success: Thread,
      error: ThreadNotFound,
    })
      .annotate(OpenApi.Identifier, "getThread")
      .annotate(OpenApi.Description, "Fetch one thread by id."),
    HttpApiEndpoint.patch("updateThread", "/threads/:threadId", {
      params: threadIdParam,
      payload: UpdateThreadRequest,
      success: Thread,
      error: ThreadNotFound,
    })
      .annotate(OpenApi.Identifier, "updateThread")
      .annotate(OpenApi.Description, "Update a thread's display label (the only mutable field)."),
    HttpApiEndpoint.post("archiveThread", "/threads/:threadId/archive", {
      params: threadIdParam,
      success: Thread,
      error: [ThreadNotFound, ThreadBusy, ZmxUnavailable],
    })
      .annotate(OpenApi.Identifier, "archiveThread")
      .annotate(
        OpenApi.Description,
        "Archive the thread (idempotent and convergent): its live linked terminal is killed first, so an archived thread consumes no multiplexer session; the provider conversation survives for a later exact resume via unarchive + open. Refused while the agent session is actively working, and refused (retryably) when terminal death cannot be verified.",
      ),
    HttpApiEndpoint.post("unarchiveThread", "/threads/:threadId/unarchive", {
      params: threadIdParam,
      success: Thread,
      error: ThreadNotFound,
    })
      .annotate(OpenApi.Identifier, "unarchiveThread")
      .annotate(OpenApi.Description, "Restore an archived thread (idempotent)."),
    HttpApiEndpoint.post("pinThread", "/threads/:threadId/pin", {
      params: threadIdParam,
      success: Thread,
      error: [ThreadNotFound, ThreadArchived],
    })
      .annotate(OpenApi.Identifier, "pinThread")
      .annotate(
        OpenApi.Description,
        "Pin an active thread (idempotent). Refused while the thread is archived.",
      ),
    HttpApiEndpoint.post("unpinThread", "/threads/:threadId/unpin", {
      params: threadIdParam,
      success: Thread,
      error: ThreadNotFound,
    })
      .annotate(OpenApi.Identifier, "unpinThread")
      .annotate(OpenApi.Description, "Unpin a thread (idempotent)."),
    HttpApiEndpoint.delete("deleteThread", "/threads/:threadId", {
      params: threadIdParam,
      error: [ThreadNotFound, ZmxUnavailable],
    })
      .annotate(OpenApi.Identifier, "deleteThread")
      .annotate(
        OpenApi.Description,
        "Delete the thread: kill its live linked terminal if one is open, then remove the record. Provider-owned conversation history is never touched.",
      ),
    HttpApiEndpoint.post("openThreadTerminal", "/threads/:threadId/terminal", {
      params: threadIdParam,
      success: Terminal,
      error: [
        ThreadNotFound,
        ThreadArchived,
        ProviderUnavailable,
        ProviderSessionConflict,
        DirectoryUnavailable,
        DirectoryCheckTimedOut,
        ZmxUnavailable,
        TerminalLaunchFailed,
      ],
    })
      .annotate(OpenApi.Identifier, "openThreadTerminal")
      .annotate(
        OpenApi.Description,
        "Open the thread's TUI terminal. Idempotent: a live linked terminal is returned as-is. Otherwise the provider TUI is launched in a new terminal — a confirmed session is relaunched against its exact persisted identity (ATC never adopts a different one), an unconfirmed one materializes a fresh provider session whose identity is established and persisted before this call returns. " +
          "A provider that can be probed (Codex) fails fast when the persisted session no longer exists; one that cannot (Claude) launches blind — a successful open whose terminal dies within seconds is a state clients must expect and surface.",
      ),
    HttpApiEndpoint.get("listAgents", "/agents", { success: AgentList })
      .annotate(OpenApi.Identifier, "listAgents")
      .annotate(
        OpenApi.Description,
        "List the built-in agents with availability, detected on demand.",
      ),
    HttpApiEndpoint.get("getAgent", "/agents/:agentId", {
      params: { agentId: Schema.String },
      success: Agent,
      error: AgentNotFound,
    })
      .annotate(OpenApi.Identifier, "getAgent")
      .annotate(OpenApi.Description, "Fetch one built-in agent by its registry slug."),
    HttpApiEndpoint.get("subscribeEvents", "/events", {
      success: HttpApiSchema.StreamSse({ data: ResourceChangedEvent }),
    })
      .annotate(OpenApi.Identifier, "subscribeEvents")
      // The generated 200 for this endpoint documents StreamSse's decoded
      // frame envelope, not the data-only bytes the server actually emits —
      // openapi.ts (withDataOnlySseResponse) replaces it with the wire-true
      // shape, and openapi.test.ts pins that documented shape to the bytes.
      .annotate(
        OpenApi.Description,
        "Subscribe to live resource-change events (SSE). The stream is ephemeral — no replay. " +
          "Resync race-free on every (re)connect: open the stream, wait for the opening `: connected` comment, THEN refetch lists — " +
          "fetching before the stream is open leaves a missed-event window. " +
          "Events are thin invalidations and arrive in bursts: coalesce (~100ms) and refetch once per named collection, not per event " +
          "(terminal-bearing refetches consult the multiplexer and are priced accordingly). " +
          "The server emits a comment heartbeat roughly every 25 seconds; treat a stream silent for much longer as dead — reconnect and resync. " +
          "The server ends the stream of a subscriber that falls too far behind; reconnect and resync.",
      ),
    HttpApiEndpoint.get("checkDirectory", "/fs/check", {
      query: { path: AbsolutePath },
      success: FsCheckResponse,
    })
      .annotate(OpenApi.Identifier, "checkDirectory")
      .annotate(
        OpenApi.Description,
        "Demand-driven directory health check with a bounded timeout. Takes a server-host absolute path; the result is never persisted. " +
          'A timed-out check is deliberately a 200 with state "unknown" and reason "timeout" — the check is a report, so an inconclusive ' +
          "result is still a successful report — while /fs/list models the same timeout as a 422 error, because a listing cannot be " +
          "partially delivered.",
      ),
    HttpApiEndpoint.get("listDirectory", "/fs/list", {
      query: {
        path: Schema.optionalKey(
          AbsolutePath.annotate({
            description: "Directory to list; omitted lists the server's home directory.",
          }),
        ),
      },
      success: FsListResponse,
      error: [DirectoryUnavailable, DirectoryCheckTimedOut],
    })
      .annotate(OpenApi.Identifier, "listDirectory")
      .annotate(
        OpenApi.Description,
        "List a directory's subdirectories for the server-backed folder browser: non-recursive, " +
          "dotfolders excluded, symlinks included when they resolve to directories, sorted " +
          "case-insensitively. Omitting `path` lists the server's home directory. Same bounded " +
          "timeout and tagged errors as the health check; nothing is persisted.",
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
      // The server gates every non-loopback request on a bearer token
      // (localTrust.ts); loopback requests need no credentials. The empty
      // requirement documents that anonymous (loopback) access is valid, so
      // generated clients treat the token as optional but know how to send
      // it. The generator stamps `security: []` on every operation, and an
      // operation-level array overrides the document default (OpenAPI 3.1),
      // so those empty arrays must go for the default to mean anything.
      transform: (spec) => ({
        ...spec,
        security: [{}, { bearerAuth: [] }],
        components: {
          ...spec["components"],
          securitySchemes: {
            ...spec["components"]["securitySchemes"],
            bearerAuth: {
              type: "http",
              scheme: "bearer",
              description:
                "Required for every non-loopback request; loopback requests are trusted without credentials.",
            },
          },
        },
        paths: Object.fromEntries(
          Object.entries(spec["paths"] as Record<string, Record<string, unknown>>).map(
            ([path, operations]) => [
              path,
              Object.fromEntries(
                Object.entries(operations).map(([method, operation]) => {
                  const { security, ...rest } = operation as { security?: ReadonlyArray<unknown> }
                  return [
                    method,
                    security === undefined || security.length === 0 ? rest : { ...rest, security },
                  ]
                }),
              ),
            ],
          ),
        ),
      }),
    }),
  ) {}
