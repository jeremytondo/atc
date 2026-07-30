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

## App Server (app-server/)

The TypeScript/Bun successor to the Go server. Conventions established there:

- One Bun package: one `package.json`, one committed `bun.lock`. Install with
  `bun install --frozen-lockfile` (the `install` mise task).
- The Bun version is pinned exactly in `app-server/mise.toml`; no floating
  channels.
- Dependencies are pinned exactly and stay minimal. Hono and Commander are the
  only runtime dependencies; adding another requires concrete justification.
- Prettier is the single formatting solution; strict `tsc --noEmit` is the
  type gate. No overlapping lint/format systems.
- Boundaries: `src/main.ts` (entrypoint) → `src/cli/` (Commander registration
  and parsing) → `src/server/` (serve lifecycle, listener start/stop, app
  factory, routes) → `src/buildInfo.ts` (injected build metadata). The HTTP
  app is constructible without a listener; build metadata, listener address,
  and process lifecycle are injectable in tests.
- Public HTTP routes are versioned under `/api/v1`; responses are typed JSON
  with camelCase fields.
- Tests live in `app-server/tests/` and run with `bun test`; they include a
  black-box test that spawns the real entrypoint on an ephemeral loopback
  port and verifies clean SIGTERM shutdown.
- Workflows are mise tasks (`install`, `dev`, `fmt`, `fmt:check`, `typecheck`,
  `test`, `check`); root `check`/`test` fan out to the package, and CI runs
  the same `mise run check`.

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
