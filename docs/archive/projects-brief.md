> **Historical (archived 2026-07):** Describes the pre-monorepo atc-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# Projects Brief

Status: Draft

## Purpose

Projects should give atc and atc a durable way to group related
Terminal Sessions around a specific workstation directory. A Terminal Session is
a ZMX-backed terminal process, which may run an agent, editor, Git tool, shell,
or other terminal app. A project becomes the user-facing container for work in a
codebase, while atc remains useful as a standalone API and CLI service that
other front ends or agents can build on.

## Idea Definition

A project is an atc-owned record with metadata such as name and working
directory. Terminal Sessions can be started within a project, and
project-scoped Terminal Sessions inherit the project's working directory unless
the API intentionally allows an override.

The first version should treat projects as environment-local workstation
resources:

- A project points to one absolute directory on the workstation where atc is
  installed.
- A project can have one or more associated Terminal Sessions.
- Terminal Sessions can still exist outside a project for backward
  compatibility and direct atc API/CLI usage.
- atc exposes project behavior through API and CLI surfaces before the
  SwiftUI app treats it as a primary navigation concept.

## Recommended Direction

Start with a server-first model. atc should own project persistence, project
IDs, validation, project/Terminal Session relationships, and CLI workflows. The
SwiftUI app should consume those APIs rather than inventing local-only project
state.

Keep the v1 project model intentionally small:

- `id`
- `name`
- `workingDir`
- `createdAt`
- `updatedAt`
- optional `archivedAt`
- optional lightweight display metadata, such as `lastSessionAt` or Terminal
  Session counts, if it is cheap for atc to provide

Use projects as the preferred Terminal Session creation path, but avoid making
every existing session endpoint project-mandatory immediately. That lets
atc remain compatible with scripts, agents, and other clients that only
know how to create a Terminal Session with an action, environment, and working
directory. atc itself should treat project-scoped Terminal Sessions as
the v1 app surface and not expose unscoped Terminal Sessions in its primary UI.

The T3 Code reference is useful mostly for product shape: projects group units
of work, project lists sort by recent activity, and only a limited number of
recent Terminal Sessions may need to appear in the project view. atc
should borrow that grouping behavior without treating projects as agent-only
containers or coupling the model to a specific front end.

## Key Features

- Create, list, inspect, update, archive, and unarchive projects in atc.
- Start a Terminal Session from a project through atc API and CLI.
- List Terminal Sessions by project, with recent activity ordering.
- Surface project metadata in Terminal Session responses so clients can render
  mixed project and non-project Terminal Sessions during migration.
- Add Swift models and client methods in `ATCAPI` for project endpoints.
- Add an atc project list or project-first sidebar that drills into the
  project's Terminal Sessions.
- Add project selection to Terminal Session creation, with the project's working
  directory prefilled or inherited.
- Preserve direct working-directory Terminal Session creation for atc API/CLI
  usage, but do not expose it as an atc v1 app workflow.

## Non-Goals / Deferred Ideas

- Cross-workstation project identity.
- Repository identity or clone correlation across machines.
- Project-level agent memory, rules, environment variables, or secrets.
- Treating projects as agent-only containers.
- Agent-specific session semantics such as turns, prompts, checkpoints, model
  metadata, or approval history.
- Project templates.
- Multi-root projects.
- Fine-grained project permissions.
- Automatic project discovery from every Git checkout on the workstation.
- Moving remote workspace roots into projects immediately.

## System Shape

- **atc Domain Model**: Owns project records, validates project working
  directories, and defines the project/Terminal Session relationship.
- **atc API**: Exposes project CRUD, project-scoped Terminal Session
  creation, and project-scoped Terminal Session listing.
- **atc CLI**: Provides standalone workflows such as listing projects,
  creating a project, showing project Terminal Sessions, and starting a
  Terminal Session in a project.
- **atc Terminal Session Model**: Adds an optional `projectId` on sessions
  and keeps `workingDir` on each session as the historical execution snapshot.
  Existing API names may remain `sessions` for compatibility even if product
  language becomes more explicit.
- **atc ATCAPI Package**: Adds DTOs, request models, decoding tests,
  and `HTTPATCClient` methods for projects.
- **atc App State**: Adds a `ProjectsStore` alongside `SessionsStore`,
  probably polling at the same cadence until atc has push events.
- **atc UI**: Makes projects a first-class browsing and creation
  surface and intentionally limits the v1 app workflow to project-scoped
  Terminal Sessions.

## Core Concepts

- **Project**: A named atc record that points to one workstation working
  directory and groups related Terminal Sessions.
- **Project Working Directory**: The default directory used when starting a
  Terminal Session from a project.
- **Terminal Session**: A ZMX-backed terminal process. It may run an agent,
  editor, Git tool, shell, or another terminal app.
- **Agent Session**: A future specialization of Terminal Session with
  agent-specific behavior and metadata.
- **Project Terminal Session**: A Terminal Session associated with a project
  through `projectId`.
- **Unscoped Terminal Session**: A backward-compatible Terminal Session with no
  project, retained for atc API/CLI usage rather than atc v1 app
  navigation.
- **Archived Project**: A hidden-but-retained project record; associated
  historical Terminal Sessions remain intact. A project with active Terminal
  Sessions cannot be archived.

## Open Questions

- Should a project working directory be required to sit under a configured
  Remote Workspace Root, or should atc allow any valid absolute path like
  direct Terminal Session creation does today?
- Should Terminal Sessions started from a project be allowed to override
  `workingDir`, or should project association imply exactly one working
  directory?
- Should project names be unique globally, unique per working directory, or not
  unique at all?
- Should v1 include project renaming and working-directory edits, or only create
  plus archive?
