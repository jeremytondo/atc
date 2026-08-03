# atc Agent Instructions

## Core Priorities
- Performance
- Reliability
- Simplicity
- User Experience

If a tradeoff is required, choose correctness and robustness over short-term convenience.

## Maintainability

Long term maintainability is a core priority. If you add new functionality, first check if there is shared logic that can be extracted to a separate module. Duplicate logic across multiple files is a code smell and should be avoided. Don't be afraid to change existing code. Don't take shortcuts by just adding local logic to solve a problem.

## Server

The server stands on its own and is meant as a flexible resource that other apps can connect to and be built on top of. The Web UI is more admin interface than API client. It's meant as a place to manage all aspects of the server and document the CLI and API. The CLI is a thin client of the API; its purpose is to give agents and scripts access to the app's functionality. Design its command surface for that job — it does not need to mirror the API operation-for-operation, and no tooling should enforce such a mapping.

## Source Control

Jujutsu (jj) Protocol: You are in a jj repository; strictly do not use git
add/commit/stash/checkout. When a logical step passes tests, checkpoint your
work by running `jj describe -m "<msg>"` followed by `jj new`. If you write
code that breaks the build, immediately run `jj undo` to revert before trying
again. To push a branch, use a jj bookmark and push it to the git remote. To
create a PR push a branch and then create a PR in GitHub. Follow JJ best
practices.

## Reference Apps 

Use the follwing apps as references and inspiration of similar projects. Can be used for design and UX inspiration as well as code and architecture ideas.

T3Code (https://github.com/pingdotgg/t3code/): Great user expereience and similar feature set.
Codex Desktop App (https://chatgpt.com/codex): Greate user experience
AGTerm (https://github.com/umputun/agterm): LibGhostty app with a lot of similar features.
CMUX (https://github.com/manaflow-ai/cmux): Agentic coding focused terminal based on libghostty.

## Reference Source Checkouts

`mise run refs` shallow-clones read-only reference source into `repos/` (gitignored):
the Effect monorepo pinned to the app-server's Effect version, T3Code and
OpenCode (https://github.com/sst/opencode) as Effect architecture references, and
zmx (https://github.com/neurosnap/zmx) pinned to the installed zmx version as the
terminal-multiplexer behavior reference.

- Treat everything under `repos/` as read-only reference material. Never import
  from it, edit it, or copy files out of it wholesale.
- For Effect v4 APIs and idioms, prefer reading this source over web search or
  training data — both skew heavily toward Effect v3.
- To update a checkout, delete its directory and re-run `mise run refs`.

## Effect Conventions (App Server)

All TypeScript App Server code (`app-server/`) is written on Effect
(`effect@4.0.0-beta.x` line + `@effect/platform-bun`, exact-pinned and upgraded
deliberately). Write new code the same way:

- Services are `Context.Service` classes with `Layer` implementations
  (`Layer.succeed` / `Layer.effect`). One service per module; export the class
  and its `layer`. `HttpApiBuilder.group` handler layers are the exception:
  name them `<Group>Handlers`.
- Errors are Schema-based tagged error classes (`Schema.TaggedErrorClass`).
  API-visible errors carry an `httpApiStatus` annotation so HTTP status
  mapping derives from the contract. No plain `throw`/`Error` for domain
  failures.
- HTTP is schema-first: endpoints are declared in the `HttpApi` contract
  (`src/api.ts`) with Schema-typed responses, implemented with
  `HttpApiBuilder.group`. The server, OpenAPI document, and typed clients all
  derive from the contract.
- All validation and domain types are Effect Schema. No zod or hand-rolled
  parsing.
- Exactly one runtime entrypoint: `BunRuntime.runMain` in `src/main.ts`. No
  `runPromise`/manual runtimes anywhere else — tests use `@effect/vitest`
  (`it.effect`), which manages the runtime for you.
- Lifecycle is structured concurrency: resources live in Layers/Scopes so
  SIGINT/SIGTERM interruption releases them. No hand-rolled signal listeners
  or drain loops.
- Tests substitute test Layers for production Layers; prefer in-process tests
  (e.g. `HttpApiTest`) and reserve real listeners for lifecycle/black-box
  coverage.
- For Effect v4 APIs and idioms, read the pinned source under `repos/`
  (`mise run refs`) and follow T3Code/OpenCode patterns; web search and
  training data skew to Effect v3.

## OpenAPI Contract (App Server)

The `HttpApi` contract module (`app-server/src/api.ts`) is the single source of
truth for the public API. The checked-in OpenAPI document
(`app-server/openapi.json`) is generated from it — never edit the document by
hand, and never maintain a parallel route-description layer.

- Regenerate with `mise run -C app-server openapi` (or `mise run
  app-server:openapi` at the root) after any contract change and commit the
  result. Generation is pure (`OpenApi.fromApi`, no server) and byte-identical
  for unchanged source; `mise run -C app-server openapi:check` (part of
  `check` and CI) fails on drift.
- Every endpoint pins a stable operation id with
  `.annotate(OpenApi.Identifier, "...")`: camelCase verb+resource (e.g.
  `getHealth`), unique across the whole API. Generated clients key off these
  ids — renaming one is a breaking change.
- Every request/response schema carries an `identifier` annotation (PascalCase
  type name, e.g. `HealthResponse`) so it becomes a named component schema,
  plus a short `description`. Endpoints carry an `OpenApi.Description`.
- JSON fields are camelCase. Fields the server always returns are
  non-optional in the schema and appear in `required`; health/version have no
  optional fields yet, so no optionality convention exists beyond that.
- The document version is the contract version (`v1`), never the compile-time
  build metadata (`commit`/`builtAt`), which would break deterministic
  generation.
- `openapi.json` is generated output: `src/openapi.ts` is its single
  formatting authority, and it sits in `.prettierignore` so `fmt` never
  rewrites it.

## Clients and the CLI (App Server)

Both public clients derive from the same contract:

- **TypeScript** (`app-server/src/client.ts`): derived directly from the
  contract module with Effect's `HttpApiClient` — no generated artifact, so
  nothing can go stale. It depends only on `api.ts`, never server internals.
- **Swift** (`ATCAppServerAPI` in `packages/ATCKit`): generated at build time
  by the Apple Swift OpenAPI Generator plugin from
  `Sources/ATCAppServerAPI/openapi.json`, a symlink to the checked-in
  `app-server/openapi.json`. Generator, runtime, and transport versions are
  exact-pinned in `Package.swift`; upgrade them deliberately and rerun
  `mise run kit:test`. The legacy `ATCAPI` product (Go server contract) is
  unchanged and coexists until the app migrates.

CLI commands backed by API operations use the contract-derived TypeScript
client (`atc health`, `atc version`, `atc project …`, `atc terminal …`,
`atc fs check`): JSON payload on stdout and exit 0 on success; diagnostics on
stderr with exit 1 on invalid usage, configuration, or request failure.
`atc terminal attach` is the one non-JSON command: it bridges the local
TTY onto the WebSocket attach endpoint in raw mode (detach with Ctrl-]).
API-backed commands take zero connection flags: the base URL derives from
the settled configuration (`http://127.0.0.1:<port>`), resolved in the
single seam in `cli.ts` — remote endpoint addressing (endpoint + token)
lands there with the auth work. The
CLI may resolve relative directory arguments client-side before calling the
API (which takes server-host absolute paths only).

The CLI command surface is curated, not a 1:1 mirror of the API: commands
exist to give agents and scripts good access to the app's functionality, and
one command may compose several API calls (or an endpoint may have no command
at all). API-backed commands go through the contract-derived TypeScript
client — never re-implement server logic in the CLI. Process commands
(`serve`, `smoke`) run the server rather than call it.

After any contract change: `mise run app-server:openapi` to regenerate the
artifact, then `mise run contract:check` (OpenAPI drift + TS client tests +
Swift client build/tests) to verify the whole pipeline.

## Configuration and Data Locations (App Server)

One precedence rule: **command flags > environment > config file > defaults**,
implemented in `src/config.ts` (the `AppConfig` service). Everything that
needs a setting or a path consumes `AppConfig` — never `process.env` or
re-derived locations. Invalid configuration fails fast with one stderr line
naming the offending source; never a partial boot.

- Paths: one XDG rule on every platform (macOS included), honoring `XDG_*`
  overrides. Config `~/.config/atc/config.toml`; data (SQLite `atc.db`)
  `~/.local/share/atc/`; state (log file `atc.log`, zmx sockets
  `terminals/`) `~/.local/state/atc/`.
- Environment variables are flat `ATC_<KEY>` (`ATC_PORT`, `ATC_LOG_LEVEL`,
  `ATC_DATA_DIR`, `ATC_CONFIG`, `ATC_ZMX_EXECUTABLE`). No sectioned naming.
- The config file is TOML with camelCase keys (`port`, `logLevel`,
  `dataDir`, `zmxExecutable`); unknown keys are rejected. The TOML format
  never leaks past `config.ts`.
- Compiled behavior never depends on the working directory; tests isolate
  themselves by pointing `XDG_*`/`ATC_*` at temp dirs (`test/blackbox.ts`
  `isolatedEnv`).

## Persistence (App Server)

SQLite via Effect's SQL tooling (`effect/unstable/sql` +
`@effect/sql-sqlite-bun`, exact-pinned) — not Drizzle, not flat files. The
stack stays behind the persistence boundary:

- `src/persistence.ts` provides the migrated `SqlClient` at the configured
  location with documented pragmas (WAL on by default, `busy_timeout = 5000`,
  `foreign_keys = ON`). `sql.withTransaction` is the transaction boundary.
- Migrations are an in-code, append-only record in `src/migrations.ts`
  (`Migrator.fromRecord`, keys `"0001_init"`, compiled into the binary —
  never a filesystem dependency). Applied transactionally at startup with
  library bookkeeping (`effect_sql_migrations`); a failed migration rolls
  back and halts boot naming the migration. Never edit or remove a shipped
  entry — append. `test/persistence.test.ts` pins loader count to record
  size because malformed keys are silently dropped.
- Repositories (e.g. `src/projectRepository.ts`) are the only modules that
  speak SQL; row types never leak into contract schemas, handlers, or
  clients. Database failures are defects (500s), not domain errors.
- Tests get isolated databases via `Persistence.layerFile` (`:memory:` or a
  temp file); see `test/testLayers.ts`.

## Local Trust (App Server)

The settled trust architecture: one HTTP-over-TCP transport, loopback-only
bind, with `Host`/`Origin` validation on every request (`src/localTrust.ts`)
blocking DNS-rebinding/CSRF. Remote access later adds bearer tokens over the
same listener, purely additively; non-loopback binding stays refused until
then.

## Compiled Executable and Subprocesses (App Server)

- `mise run -C app-server build` compiles the standalone `atc` executable for
  the host into `app-server/dist/atc-<os>-<arch>`; `build:all` cross-compiles
  all four release targets (darwin/linux × arm64/x64). Artifact names are
  deterministic — CI and release tooling rely on them.
- Build metadata (commit, build time) is injected at compile time by
  `scripts/build.ts` via `--define`; running from source reports `dev`.
- Compiled behavior must never depend on the working directory. `.env`/bunfig
  autoloading is disabled at compile; any asset that ships with the
  executable must resolve relative to `process.execPath`, never the cwd.
- All child processes go through the `Subprocess` service
  (`src/subprocess.ts`): scoped acquisition (scope close terminates the child,
  SIGTERM escalating to SIGKILL), bounded stderr diagnostics, explicit
  environment. No ad-hoc `Bun.spawn` in server code. The PTY variant
  (`spawnPty`, Bun native `terminal:`) carries the same scope guarantees and
  is the only sanctioned pseudo-terminal path.
- atc ships no provider binaries: the user installs and authenticates the
  Codex CLI and Claude Code themselves. Provider executables resolve
  explicitly, never implicitly: env override first (`ATC_CODEX_EXECUTABLE` /
  `ATC_CLAUDE_CODE_EXECUTABLE`), then the user's install on PATH. The Claude
  Agent SDK is always given an explicit `pathToClaudeCodeExecutable`; its
  packaged platform binaries are never relied on.
- Cross-compilation success is not runtime validation. CI runs the compiled
  black-box suite natively on macOS arm64 and Linux x64 only; darwin-x64 and
  linux-arm64 stay cross-compile-only until the release milestone.
- After upgrading Bun, Effect, or the Claude Agent SDK, rerun
  `mise run -C app-server test:compiled` and the opt-in live
  `mise run -C app-server test:smoke` suite.

## Terminals and zmx (App Server)

Terminals are backed by the user-installed zmx multiplexer (never bundled),
reached exclusively through the narrow `TerminalAdapter` seam
(`src/terminalAdapter.ts`; zmx implementation `src/zmxAdapter.ts`; in-memory
fake `test/fakeTerminalAdapter.ts`). The seam speaks derived session names
(`atc-` + the terminal id's 32 hex chars — never persisted) and terminal
bytes; policy and transports live above it.

- The executable resolves explicitly: `ATC_ZMX_EXECUTABLE` / config
  `zmxExecutable`, else `zmx` on PATH; a missing install is one actionable
  diagnostic (`ZmxUnavailable`), never a crash.
- Every zmx child runs with ATC's private socket directory
  (`<stateDir>/terminals` as `ZMX_DIR`) and a scrubbed environment:
  `ZMX_SESSION` and `ZMX_SESSION_PREFIX` are always cleared (nested-client
  trap; silent name rewriting). Socket-path length (103-byte unix cap) is
  validated at adapter boot. Debug the same inventory with
  `ZMX_DIR=~/.local/state/atc/terminals zmx list`.
- The zmx behavioral guards the adapter encodes (attach auto-creates, exit
  codes prove nothing, kill returns before death, only a reachable inventory
  entry proves a live session) are documented where they are enforced — the
  header of `src/zmxAdapter.ts`, derived from the pinned source in
  `repos/zmx`.
- Terminals are durable, project-scoped records (`src/terminals.ts`,
  `src/terminalRepository.ts`): UUIDv7 id, immutable command argv and
  canonicalized initial working directory, mutable label, public states
  `live`/`ended` (tombstones persist until explicit delete; `starting` is an
  internal crash-mid-create marker). Create starts the zmx session; a failed
  launch leaves no record; project deletion is restricted while terminals
  exist. Reconciliation is demand-driven (startup during layer build, and on
  list/read/attach): only a complete inventory marks anything ended, an
  unavailable inventory leaves stored state untouched (`ZmxUnavailable`,
  503, retryable), and startup also cleans orphan sessions in the private
  dir.
- The WebSocket attach endpoint is contract-declared (`attachTerminal`)
  and marked `OpenApi.Exclude`, so it never enters the serialized OpenAPI
  document (REST clients and the Swift generator cannot represent it). Wire protocol
  and close vocabulary have one code home, `src/attachProtocol.ts` (Schema-typed
  control frames, close reasons, the attach URL builder), consumed by both the
  server bridge (`src/terminalAttach.ts`) and the CLI client and documented in
  prose on the contract endpoint: binary frames are bytes, text frames are JSON
  control (`resize`, `ping`/`pong`), close 1000 `terminal_ended` is
  authoritative, 1011 reasons are retryable. Attach bridges are forked into
  the handler layer's scope — Bun aborts the upgraded request's fiber — and
  server shutdown reaps them.
- Coverage: deterministic tests drive the adapter and the whole transport
  against the `test/fixtures/fake-zmx.ts` stand-in (a spawned serve with
  `ATC_ZMX_EXECUTABLE` pointed at the fixture wrapper);
  `mise run -C app-server test:zmx` opts into real-zmx smoke plus compiled
  restart-recovery tests.

## Code Style

- Always strive for simplicity. This is not a complex enterprise app.
- Code readability is critical. Code should be easily understandable by
  developers coming into the project.
- Developer ergonoics is important. It should be easy for developers to work with and test the codebase.

## Model Selection

When starting sub agents or running workflows, be smart about which agents to choose in order to save on token cost. Use agents like Opus, Sonnet, Terra, or Luna when it makes sense. Always review and check their work.

## Using Linear

When doing work in Linear, always work within the atc team: https://linear.app/elevenideas/team/ATC.

Current work lives in the "Application Refactor" project. The canceled
"Archived - Application Refactor" project is stale planning reference from a
superseded planning pass — never treat its issues as open or current work.

## Working With XCode

If using XcodeBuildMCP, use the installed XcodeBuildMCP skill before calling XcodeBuildMCP tools.
