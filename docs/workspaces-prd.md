# Workspaces Product Requirements Document

Status: Draft v1

Purpose: Make Workspaces the durable unit of coding work in atc, with a
server-owned model and a focused macOS experience for organizing Sessions and
Terminals.

## 1. Product Boundary

atc has this hierarchy:

```text
Connection → Project → Workspace → Session
```

- A **Project** names one codebase and its default directory on one atc server.
- A **Workspace** is a durable task context inside one Project. In v1, it
  always uses the Project directory.
- A **Session** is the generic durable wrapper around a zmx session.
- The macOS app labels Sessions started by an **Agent Action** as **Sessions**;
  Sessions started from the Interactive Shell or a general Action appear as
  **Terminals**.

This is a clean break. The schema migration deletes every pre-Workspace
Session row and preserves Projects, so an existing database keeps working
without manual resets. The implementation MUST NOT migrate or preserve direct
Project-scoped or unscoped Sessions.

## 2. Goals

- Group related coding work under durable Workspaces.
- Keep one generic Session lifecycle and zmx integration for Agents, shells,
  and other Actions.
- Provide a native macOS Dashboard and Workspace shell that are simple,
  keyboard-friendly, and terminal-first.
- Keep the server web UI current as the API documentation source of truth and
  as an administrative control panel for all server capabilities.

## 3. Non-Goals

- Git worktree or Jujutsu workspace creation.
- Automatic Editor/Neovim Sessions.
- Agent Activity, hooks, notifications, prompt composer UI, or agent-specific
  orchestration controls.
- Dashboard activity feeds, recent-workspace sections, or recency tracking.
- A macOS Environment picker or structured initial-prompt UI.
- Filesystem deletion or repository cleanup from any Project, Workspace, or
  Session delete operation.
- Making the server web UI resemble the macOS coding experience.

## 4. Domain and Lifecycle Rules

Throughout this document, an **active Session** is a Session whose process
lifecycle is `starting` or `running`. Ended, failed, and archived Sessions are
not active.

### Workspace

A Workspace MUST contain:

- `id`
- `projectId`
- `name` — required, renameable, and not unique
- `createdAt`, `updatedAt`, and optional `archivedAt`

A Workspace inherits the Project working directory; it MUST NOT store an
independent v1 directory. New Sessions MUST reference `workspaceId`; their
Project and working directory are derived from that Workspace.

Workspaces persist after their Sessions end. Archive is reversible and allowed
only when the Workspace has no active Sessions. Unarchive is rejected while the
parent Project is archived. Workspace creation is rejected in an archived
Project. Deleting a Workspace:

- MUST stop all associated live Sessions.
- MUST delete Workspace and Session metadata only after every stop succeeds.
- MUST leave all filesystem state untouched.
- MUST leave the Workspace and all metadata intact if any stop fails.

Projects may archive only after every Workspace is archived. Projects may delete
only after every Workspace is deleted. Project deletion never affects files.

### Session and Action

Every new Session MUST have `workspaceId`.

- A **general Action** is a configured command template.
- An **Agent Action** is a configured command template typed to launch an Agent.
- Action type is selected at Action creation, defaults to general Action, and
  is immutable.
- Built-in Codex and Claude Actions are Agent Actions.
- A custom Action MUST NOT be deleted while it has active Sessions.
- A Session with no Action launches the server-selected Interactive Shell. The
  caller MUST NOT supply an arbitrary command.

The server preserves existing generic Session Input and initial-prompt API/CLI
support. The macOS v1 UI does not expose a structured prompt composer.

Session archive is reversible. Session delete stops a live process when needed,
then removes only atc metadata. It never affects files.

## 5. Phase 1 — Server, CLI, and Web Admin

Implementation detail for this phase — schema, route contracts, error
semantics, concurrency rules, and build order — lives in
[workspaces-phase1-spec.md](workspaces-phase1-spec.md).

Because contract fixtures and the ATCAPI Swift models live in `packages/` and
are exercised by shared contract tests, Phase 1 MUST update those models and
apply a minimal mechanical macOS bridge (a Workspace picker inside the existing
create-session sheet) so the repository builds and the app stays usable. The
Phase 2 UI replaces that bridge.

### Server API and Storage

The Go server MUST add Workspace persistence, domain validation, API routes,
and contract fixtures. SQLite foreign keys and transactions MUST protect
Project → Workspace → Session ownership.

Required Workspace operations:

- Create, list, read, rename, archive, unarchive, and delete Workspaces.
- List Sessions in one Workspace, including archived filtering.
- Reject Project archive/delete when its Workspace lifecycle requirements are
  not met.
- Enforce the Workspace deletion failure rule from Section 4.

The Session start contract MUST require `workspaceId` and accept an optional
Action, Environment, name, parameters, and initial prompt. An omitted Action
starts the Interactive Shell; parameters and initial prompt are rejected in
that case. Project ID and caller-supplied working directory are removed from
the new start contract.

The Action API and configuration MUST expose Action type (`action` or `agent`)
and preserve the immutability/deletion rules in Section 4.

Recommended route shape:

- `POST /api/workspaces`
- `GET /api/workspaces?projectId=&includeArchived=`
- `GET|PATCH|DELETE /api/workspaces/{id}`
- `POST /api/workspaces/{id}/archive`
- `POST /api/workspaces/{id}/unarchive`
- `GET /api/workspaces/{id}/sessions`
- `POST /api/sessions/start` with `workspaceId`
- `POST /api/sessions/{id}/unarchive`
- `DELETE /api/sessions/{id}`
- `DELETE /api/projects/{id}`

Exact route spelling may vary only if the corresponding API reference,
contracts, CLI, and native client change together.

### CLI

The CLI MUST expose the complete Workspace lifecycle and remain a practical
server-administration surface. It MUST support Workspace create, list, show,
rename, archive, unarchive, and delete operations. Session start MUST accept a
Workspace rather than a Project or raw directory. CLI Action management MUST
surface Action type.

### Server Web UI and Documentation

The server web UI is an admin control panel and the human-facing source of truth
for server documentation. It is not a coding client and MUST NOT copy the macOS
Workspace shell.

For every public server capability, the web UI MUST provide:

- Accurate API-reference documentation: purpose, inputs, responses, errors,
  and CLI equivalent where one exists.
- Administrative controls appropriate to the capability.

For this feature, the web UI MUST administer Projects, Workspaces, Actions,
Sessions, archive/unarchive, and delete flows, including destructive-action
confirmation and filesystem-safety language. API route/fixture coverage MUST
prevent the reference from silently omitting a public endpoint.

### Server Acceptance Criteria

- A clean database can create a Project, then multiple same-named Workspaces.
- Every new Session belongs to exactly one Workspace and inherits its Project
  directory.
- An Agent Action starts a generic Session; an Action-less start opens the
  Interactive Shell.
- Archive/unarchive/delete constraints behave exactly as Section 4 describes.
- API contract tests, Go domain/store/API tests, CLI tests, and web tests cover
  Workspace and delete failure paths.
- The web UI documentation and controls cover every new public route.

## 6. Phase 2 — macOS Dashboard and Workspace UI

The macOS app MUST use the server Workspace model and retain the existing native
atc design direction. The Dashboard and Workspace wireframes are hierarchy
references, not a replacement design system; in particular, the wireframe's
automatic Editor and activity timestamps are out of scope for v1.

### Dashboard

The app opens on the Dashboard. It shows all Projects across Connections in
Connection sections, using a compact card/list hybrid:

- Each Connection shows its name, reachability, and local/remote context.
- Each Project card shows its name, directory, Workspaces, and New Workspace
  action.
- Workspace rows are newest-created-first and show no runtime/activity status.
- A Project with no Workspaces has a quiet empty state and New Workspace action.
- There is no Project detail screen, Recent Workspaces section, or activity
  feed in v1.

New Workspace is one shared command. A Project-row or current-Workspace entry
preselects its Project; a context-free invocation asks the user to choose one.
Creation asks for a name only and opens the Workspace on success.

### Workspace Shell

The Workspace shell contains:

- Window-level Workspace, Project, Connection, and Dashboard-back context.
- A sidebar with **Sessions** and **Terminals** sections, each with a creation
  action.
- A main terminal surface for the selected Session or Terminal.
- A compact content header with name, Action label where applicable, working
  directory, and generic process lifecycle.

The app restores the last selected Session when it still exists. Otherwise it
shows a Workspace empty state with New Session and New Terminal actions.

New Session lists Agent Actions only. New Terminal offers the Interactive Shell
and general Actions only. Both commands are enabled only inside an active
Workspace and use the server default Environment. A user-provided name is
optional; fallback labels are the Agent Action label, `Terminal`, or general
Action label respectively.

Session rows show only process lifecycle (starting, running, ended, failed),
not Agent Activity. The app retains normal terminal typing; it does not add a
prompt composer or agent controls.

### macOS Lifecycle UX

Archive is reversible for Projects, Workspaces, and Sessions. Archived Sessions
appear through an Archived filter inside their Workspace.

Every delete action MUST confirm its effect:

- Session delete may stop the process and removes atc metadata.
- Workspace delete names the Workspace, counts affected Sessions, and stops
  them before metadata removal.
- Project delete is enabled only after all Workspaces are deleted.
- Every confirmation states that files are not touched.

### macOS Acceptance Criteria

- The Dashboard can create and open a Workspace from each supported command
  context.
- A Workspace can start, switch, archive/unarchive, and delete Sessions and
  Terminals through the new model.
- The UI never creates a Session without an active Workspace.
- Dashboard rows do not imply Workspace runtime state or recency.
- Keyboard focus preserves terminal typing and supports the documented sidebar
  navigation and command-palette model.
- Swift unit, store/grouping, and UI-hosting tests cover the Dashboard,
  Workspace empty state, command contexts, and destructive flows.

## 7. Implementation Profile

- **Server**: Go 1.26, SQLite with Goose migrations, Cobra CLI, existing zmx
  boundary, and server contract fixtures.
- **Server web UI**: Svelte 5, TypeScript, Vite, and the existing embedded-web
  build path.
- **macOS**: Swift 6.2, SwiftUI, macOS 26+, existing ATCAPI package and
  libghostty terminal bridge.
- **Repository boundaries**: server domain/store/API/CLI/web changes stay in
  `server/`; API client models and fixtures stay in `packages/`; native UI stays
  in `macos/`.
- **Dependencies**: reuse existing libraries and patterns. New production
  dependencies require explicit justification.

## 8. Definition of Done

The feature is done when both phases meet their acceptance criteria, their API
contracts and web documentation agree, existing quality gates pass for affected
surfaces, and the new Workspace model is usable end-to-end from server CLI,
server web admin UI, and macOS.
