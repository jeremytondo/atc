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

The server stands on its own and is meant as a flexible resource that other apps can connect to and be built on top of. The Web UI is more admin interface than API client. It's meant as a place to manage all aspects of the server and document the CLI and API. The CLI and API should mirror each other in terms of functionality unless there is a good reason they should not.

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
the Effect monorepo pinned to the app-server's Effect version, plus T3Code and
OpenCode (https://github.com/sst/opencode) as Effect architecture references.

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

## Compiled Executable and Subprocesses (App Server)

- `mise run -C app-server build` compiles the standalone `atc` executable for
  the host into `app-server/dist/atc-<os>-<arch>`; `build:all` cross-compiles
  all four release targets (darwin/linux × arm64/x64). Artifact names are
  deterministic — CI and release tooling rely on them.
- Build metadata (commit, build time) is injected at compile time by
  `scripts/build.ts` via `--define`; running from source reports `dev`.
- Compiled behavior must never depend on the working directory. `.env`/bunfig
  autoloading is disabled at compile; assets that ship with the executable
  (e.g. the packaged Claude Code binary staged at `dist/claude-<os>-<arch>`)
  resolve relative to `process.execPath` under target-scoped names.
- All child processes go through the `Subprocess` service
  (`src/subprocess.ts`): scoped acquisition (scope close terminates the child,
  SIGTERM escalating to SIGKILL), bounded stderr diagnostics, explicit
  environment. No ad-hoc `Bun.spawn` in server code.
- Provider executables resolve explicitly, never implicitly: env override
  first (`ATC_CODEX_EXECUTABLE` / `ATC_CLAUDE_CODE_EXECUTABLE`), then a known
  location (PATH for codex; the executable-adjacent staged binary, then the
  platform package in `node_modules`, for Claude Code).
- Cross-compilation success is not runtime validation. CI runs the compiled
  black-box suite natively on macOS arm64 and Linux x64 only; darwin-x64 and
  linux-arm64 stay cross-compile-only until the release milestone.
- After upgrading Bun, Effect, or the Claude Agent SDK, rerun
  `mise run -C app-server test:compiled` and the opt-in live
  `mise run -C app-server test:smoke` suite.

## Code Style

- Always strive for simplicity. This is not a complex enterprise app.
- Code readability is critical. Code should be easily understandable by
  developers coming into the project.
- Developer ergonoics is important. It should be easy for developers to work with and test the codebase.

## Model Selection

When starting sub agents or running workflows, be smart about which agents to choose in order to save on token cost. Use agents like Opus, Sonnet, Terra, or Luna when it makes sense. Always review and check their work.

## Using Linear

When doing work in Linear, always work within the atc team: https://linear.app/elevenideas/team/ATC.

## Working With XCode

If using XcodeBuildMCP, use the installed XcodeBuildMCP skill before calling XcodeBuildMCP tools.
