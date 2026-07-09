# Cockpit Projects Brief

Status: Ready for spec

## Purpose

Projects give Cockpit a durable way to group related Sessions around a named
work container. A Project gives users a stable place to create, find, and manage
Sessions for a workstation directory while preserving Cockpit as a standalone
local API, CLI, and admin web UI for persistent terminal sessions.

## Idea Definition

A Project is a Cockpit-owned record with a stable ID, required display name,
required working directory, lifecycle timestamps, and archive state. Sessions may
optionally belong to a Project. Project Sessions inherit the Project Working
Directory, while unscoped Sessions continue to work for compatibility and direct
Cockpit usage.

Projects are user-defined work containers, not unique directory records.
Multiple Projects may use the same Project Working Directory, and Project names
do not have to be unique.

## V1 Decisions

- Project IDs are Cockpit-owned opaque public IDs with a `prj_` prefix. They do
  not encode Project name, directory, archive state, or client meaning.
- Project references in the API and CLI use Project IDs, not Project names.
- Project names are required at creation and may be renamed later.
- Project Working Directory is required at creation and is fixed afterward.
  Changing directories means creating a new Project.
- Project Working Directory is not constrained by a configured filesystem root.
  Cockpit may use any valid absolute directory it can access.
- Project creation validates that the Project Working Directory is absolute,
  exists, and is a directory.
- CLI Project creation may accept relative `--dir` values, `~`, and `~/...`,
  resolving them to absolute paths before calling the API. It does not expand
  `~user` or environment variables in v1.
- Archived Projects retain their associated Sessions but cannot start new
  Sessions.
- Unarchiving a Project only clears archive state; it does not enforce name or
  directory uniqueness.
- Project lists hide archived Projects by default and include them only when
  explicitly requested.
- Project lists are ordered newest-created-first. V1 exposes no configurable
  sort or search beyond archived visibility.
- Existing direct Session creation remains valid without a Project.
- Project-scoped Session creation does not accept a `workingDir` override.
  Project Sessions always inherit the Project Working Directory.
- Project-scoped Session creation revalidates the Project Working Directory
  before launch and fails clearly if it no longer exists or is not a directory.
- Project-scoped Session creation still requires the caller to choose an Action
  and Environment and still supports Session-level fields such as Session name,
  prompt, action params, and environment selection.

## API Shape

- Add Project CRUD-style endpoints for create, list, show, and rename.
- Project updates use `PATCH /api/projects/{id}` and only accept `name` in v1.
  Unknown fields and Project Working Directory updates are rejected.
- Project archive state changes use explicit action routes:
  `POST /api/projects/{id}/archive` and
  `POST /api/projects/{id}/unarchive`.
- Project mutation endpoints return the full updated Project record.
- Project-scoped Session creation uses `POST /api/sessions/start` with
  `projectId`. There is no nested Project Session creation route in v1.
- Project-scoped Session listing is available as both
  `GET /api/sessions?projectId=<id>` and
  `GET /api/projects/{id}/sessions`, with the same response shape and backend
  query behavior.
- Session list and detail responses include a lightweight nested `project`
  object for Project Sessions: Project ID, name, working directory, and archive
  state. Unscoped Sessions omit `project`; Sessions for archived Projects keep
  the nested Project metadata with archive state.
- Project detail responses include only Project record fields. Clients fetch
  Project Sessions through the Session-list routes rather than inline Session
  data on Project detail.

## CLI Shape

- Add `cockpit projects create`, `list`, `show`, `rename`, `archive`, and
  `unarchive`.
- Project-scoped Session creation stays under
  `cockpit sessions start --project <id>`.
- Project CLI commands support `-o, --output text|json`; JSON output returns the
  raw API response.
- In text mode, `projects create` prints the Project ID and name. `projects
  show` prints full Project detail.

## Admin Web UI

The admin web UI should be a complete Projects management surface:

- List Projects, with an explicit include-archived control.
- Create Projects.
- Show Project detail.
- Rename Projects.
- Archive and unarchive Projects.
- Show associated Sessions on Project detail using the Project Sessions list
  route.
- Start a Project-scoped Session from Project detail.

Project detail hides archived Sessions by default, with an explicit
include-archived control following normal Session list behavior.

## Non-Goals / Deferred Ideas

- AtelierCode Swift models, API client methods, app state, or app UI.
- Cross-workstation Project identity.
- Repository identity or clone correlation across machines.
- Multi-root Projects.
- Project templates.
- Project search or filtering beyond archived visibility.
- Project-level agent memory, rules, environment variables, secrets, or
  permissions.
- Treating Projects as agent-only containers.
- Agent-specific session semantics such as turns, checkpoints, model metadata,
  or approval history.
- Automatic Project discovery from every Git checkout on the workstation.
- Requiring Projects for every Session.
- Deleting Projects.

## System Shape

- **Project Domain**: Owns Project validation, IDs, timestamps, archive state,
  and directory rules.
- **Store**: Adds a `projects` table and an optional `sessions.project_id`.
- **Session Domain**: Accepts optional Project association and records inherited
  working directory as the Session snapshot.
- **API**: Exposes Project record workflows, archive/unarchive, Session creation
  with optional `projectId`, and project-scoped Session listing.
- **CLI**: Provides Project record workflows over the local Unix-socket API.
  Session creation remains part of the Session command family.
- **Admin Web UI**: Lets operators manage Projects and start or inspect
  project-scoped Sessions.
- **Future App Clients**: Consume Cockpit Project APIs after the Cockpit
  contract is stable.

## Core Concepts

- **Project**: A Cockpit record that names one workstation directory and groups
  Sessions.
- **Project Working Directory**: The default directory used when starting a
  Project-scoped Session.
- **Project Session**: A Session associated with a Project through `projectId`.
- **Unscoped Session**: A backward-compatible Session with no Project.
- **Archived Project**: A hidden retained Project record. Associated Sessions
  remain intact.
