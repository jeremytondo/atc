# atc Agent Instructions

atc is a standalone server that owns Projects and Terminal Sessions, plus
native clients that attach to it. The TypeScript App Server (`app-server/`,
Effect + Bun) is the active implementation; `server/` is the legacy Go server
still being migrated away from.

This file holds what you cannot learn by reading the code: priorities, taste,
protocol, and the invariants that are invisible from the call site.

## Core Priorities

- Performance
- Reliability
- Simplicity
- User Experience

If a tradeoff is required, choose correctness and robustness over short-term
convenience.

## Maintainability

Long term maintainability is a core priority. If you add new functionality,
first check if there is shared logic that can be extracted to a separate
module. Duplicate logic across multiple files is a code smell and should be
avoided. Don't be afraid to change existing code. Don't take shortcuts by just
adding local logic to solve a problem.

## Code Style

- Always strive for simplicity. This is not a complex enterprise app.
- Code readability is critical. Code should be easily understandable by
  developers coming into the project.
- Developer ergonomics is important. It should be easy for developers to work
  with and test the codebase.

## Documentation

Documentation defaults to the code. A module's header comment records its
responsibility and invariants; for most changes that is the only documentation
edit needed. Shipping a feature is not a reason to describe it here.

This file and the READMEs were rewritten once already because each feature
appended its own section until both had drifted out of date. Do not restart
that:

- **No per-feature or per-subsystem sections in this file.** Before adding a
  line, apply the test: would an agent do the wrong thing without it, *even
  after reading the relevant code*? Only three kinds of thing pass — facts not
  in the repo, invariants invisible from the call site, and taste. Anything
  describing what the system *is* belongs in a module header.
- **READMEs cover how to run a thing, where its data lives, and which task to
  reach for.** Never enumerate routes, files, or CLI commands; point at
  `openapi.json`, module headers, and `--help`, which cannot go stale.
- Prefer editing an existing line over adding one. If this file grows past
  ~250 lines, something crept in that belongs next to the code.

## Surfaces

The server stands on its own and is meant as a flexible resource that other
apps can connect to and be built on top of. The Web UI is more admin interface
than API client. It's meant as a place to manage all aspects of the server and
document the CLI and API. The CLI is a thin client of the API; its purpose is
to give agents and scripts access to the app's functionality. Design its
command surface for that job — it does not need to mirror the API
operation-for-operation, and no tooling should enforce such a mapping.

## The ways to hurt yourself

1. **Formatting above `app-server/`.** Only ever `mise run -C app-server fmt`.
   A bare `prettier --write .` from the repo root rewrites the read-only
   `repos/` clones and dozens of unrelated tracked files.
2. **Touching the user's real zmx sessions.** `zmx` with no `ZMX_DIR` points at
   the developer's own multiplexer, not ATC's. Always scope debugging to
   `ZMX_DIR=~/.local/state/atc/terminals zmx list`, and never kill a session
   you did not create.
3. **Editing generated output.** `app-server/openapi.json` is generated from
   the contract, and `packages/ATCKit/Sources/ATCAppServerAPI/openapi.json` is
   a symlink to it. Regenerate; never hand-edit either.
4. **Killing by pattern.** No `pkill -f` / `pgrep | kill`. Your own agent
   process carries this repo's path in its argv. Kill only a PID you captured
   at spawn.

## Source Control

Jujutsu (jj) Protocol: You are in a jj repository; strictly do not use git
add/commit/stash/checkout. When a logical step passes tests, checkpoint your
work by running `jj describe -m "<msg>"` followed by `jj new`. If you write
code that breaks the build, immediately run `jj undo` to revert before trying
again. To push a branch, use a jj bookmark and push it to the git remote. To
create a PR push a branch and then create a PR in GitHub. Follow JJ best
practices.

## Verifying

Run the smallest gate that covers what you changed; `mise tasks` lists them
all. The ones you will actually reach for:

- `mise run -C app-server check` — fmt, typecheck, tests, OpenAPI drift.
- `mise run contract:check` — after **any** contract change: OpenAPI drift, TS
  client tests, Swift client build.
- `mise run -C app-server test:zmx` — opt into real-zmx smoke and compiled
  restart-recovery tests.
- `mise run -C app-server test:compiled` and the live `test:smoke` suite after
  upgrading Bun, Effect, or the Claude Agent SDK.

Tests substitute test Layers for production Layers (`test/testLayers.ts`) and
prefer in-process coverage such as `HttpApiTest`; reserve real listeners for
lifecycle and black-box tests. Tests wait on real conditions, never on sleeps.
A test that needs a timeout to pass is wrong.

## Effect Style Guide (App Server)

All of `app-server/` is written on Effect (`effect@4.0.0-beta.x` +
`@effect/platform-bun`, exact-pinned and upgraded deliberately). Effect v4
differs substantially from v3 — **read the pinned source in `repos/effect` and
follow the T3Code/OpenCode patterns in `repos/`; web search and training data
will give you v3 idioms that do not compile here.**

### Modules and services

- One service per module. Define it as a `Context.Service` class and export the
  class plus a `layer` (`Layer.succeed` / `Layer.effect`). The one exception is
  `HttpApiBuilder.group` handler layers, named `<Group>Handlers`.
- Import own modules as namespaces — `import * as Terminals from
  "./terminals.ts"` — and reference `Terminals.layer`. Always use the module's
  canonical name (`terminals.ts` is `Terminals` everywhere, `zmxAdapter.ts` is
  `Zmx` everywhere), so one grep finds every call site. Never alias an import.
- Any module carrying non-obvious invariants opens with a header comment
  stating them and why they exist — not what each line does. This is where
  subsystem documentation lives; keep it current rather than restating it in
  this file.
- Add comments for non-obvious constraints and surprising behavior, never for
  obvious assignments or control flow.

### Effects

- Bind services to named variables at the top of an `Effect.gen`, then call
  methods on them. Never nest service yields like
  `yield* (yield* Foo.Service).bar()`.
- Exactly one runtime entrypoint: `BunRuntime.runMain` in `src/main.ts`. No
  `runPromise` or hand-built runtimes anywhere else — `@effect/vitest`
  (`it.effect`) manages the runtime in tests.
- Lifecycle is structured concurrency: resources live in Layers and Scopes so
  SIGINT/SIGTERM interruption releases them. No hand-rolled signal listeners or
  drain loops.
- Do not return `Effect` from helpers that do no effectful work. Synchronous
  parsing, validation, and option building stay synchronous.
- Avoid `try`/`catch`; use Effect's error channel. Reserve `try` for wrapping a
  foreign API at the boundary.

### Types and errors

- All validation and domain types are Effect Schema. No zod, no hand-rolled
  parsing.
- Domain failures are Schema-based tagged errors (`Schema.TaggedErrorClass`),
  declared in the contract with an `httpApiStatus` annotation so HTTP status
  mapping derives from the contract. Never `throw` for a domain failure.
- Bugs are defects, not errors. A database failure is a 500, not a modeled
  failure case.
- Never use `any` — there are currently zero in `src/`, keep it that way. Rely
  on inference; annotate only where an export or clarity demands it.

### General

- Prefer `const`; use ternaries or early returns instead of reassignment.
- Avoid `else` — prefer early returns.
- Keep logic in one function unless it is genuinely reusable. Do not extract
  single-use helpers preemptively, but when a function grows validation
  branches, make the main body read as the happy path and put supporting
  helpers below it.
- Use Bun APIs where they fit (`Bun.file()`).

## Invariants

Rules you cannot infer from the file you are editing.

- **The contract is the source of truth.** `app-server/src/api.ts` defines the
  API; the OpenAPI document, the TypeScript client, and the Swift client all
  derive from it. Changing it ripples through every surface — regenerate with
  `mise run app-server:openapi`, commit the artifact, then run
  `mise run contract:check`. Operation ids (`OpenApi.Identifier`) are a public
  API: renaming one is a breaking change.
- **The CLI never re-implements server logic.** API-backed commands go through
  the contract-derived TypeScript client and take zero connection flags; the
  base URL derives from settled configuration in the single seam in `cli.ts`.
- **Migrations are append-only.** Never edit or remove a shipped entry in
  `src/migrations.ts` — append a new one.
- **Repositories are the only modules that speak SQL.** Row types never leak
  into contract schemas, handlers, or clients.
- **Everything reads `AppConfig`.** Never `process.env` and never re-derive a
  path. Precedence is always flags > environment > config file > defaults.
- **Compiled behavior never depends on the working directory.** Assets resolve
  relative to `process.execPath`. Tests isolate via `XDG_*`/`ATC_*` temp dirs
  (`test/blackbox.ts` `isolatedEnv`).
- **All child processes go through the `Subprocess` service.** No ad-hoc
  `Bun.spawn` in server code; `spawnPty` is the only sanctioned PTY path.
- **atc bundles no third-party binaries.** zmx, the Codex CLI, and Claude Code
  are installed by the user and resolved explicitly (env override, then PATH).
  A missing install is one actionable diagnostic, never a crash.
- **zmx is reached only through the `TerminalAdapter` seam.** Tests point
  `ATC_ZMX_EXECUTABLE` at `test/fixtures/fake-zmx.ts`; policy and transports
  live above the seam.
- **Trust is loopback-only.** One HTTP-over-TCP transport bound to 127.0.0.1
  with `Host`/`Origin` validation. Remote access will add bearer tokens purely
  additively; non-loopback binding stays refused until then.

## Reference Apps

Similar projects, useful for design, UX, and architecture inspiration:

- [T3Code](https://github.com/pingdotgg/t3code/) — great UX, similar feature set
- [Codex Desktop App](https://chatgpt.com/codex) — great UX
- [AGTerm](https://github.com/umputun/agterm) — libghostty app, similar features
- [CMUX](https://github.com/manaflow-ai/cmux) — agentic coding terminal on libghostty

## Reference Source Checkouts

`mise run refs` shallow-clones read-only reference source into `repos/`
(gitignored): the Effect monorepo pinned to our Effect version, T3Code and
OpenCode as Effect architecture references, and zmx pinned to the installed
version as the multiplexer behavior reference.

Everything under `repos/` is read-only. Never import from it, edit it, or copy
files out of it wholesale. To update a checkout, delete its directory and
re-run `mise run refs`.

## Model Selection

When starting sub agents or running workflows, be smart about which agents to
choose in order to save on token cost. Use agents like Opus, Sonnet, or Haiku
when it makes sense. Always review and check their work.

## Using Linear

When doing work in Linear, always work within the atc team:
https://linear.app/elevenideas/team/ATC.

Current work lives in the "Application Refactor" project. The canceled
"Archived - Application Refactor" project is stale planning reference from a
superseded planning pass — never treat its issues as open or current work.

## Working With Xcode

If using XcodeBuildMCP, use the installed XcodeBuildMCP skill before calling
XcodeBuildMCP tools.
