# Workspaces Phase 1 Implementation Spec

Status: Draft v1

Scope: the server, CLI, web admin, and `packages/` work described in
[workspaces-prd.md](workspaces-prd.md) Section 5, plus the minimal macOS
bridge that keeps the repository green. Phase 2 (macOS Dashboard and Workspace
shell) is planned separately once this contract is real.

Related: [ADR 0004](../server/docs/adr/0004-sessions-launch-agents-from-a-registry.md)
(start contract), [ADR 0008](../server/docs/adr/0008-workspace-deletion-stops-sessions-but-not-files.md)
(deletion never touches files).

Throughout, an **active Session** has status `starting` or `running`.

## 1. Schema and Migration

One new goose migration, `0003_workspaces.sql`:

```sql
CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id),
    name        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    archived_at TEXT
);
CREATE INDEX workspaces_project_created ON workspaces(project_id, created_at DESC);
```

The migration then rebuilds `sessions` as a clean break: `DROP TABLE sessions`
and recreate it with the previous columns except:

- `project_id` is removed.
- `workspace_id TEXT NOT NULL REFERENCES workspaces(id)` is added, with an
  index.
- `action` becomes nullable; `NULL` means the Interactive Shell.

All pre-Workspace Session rows are destroyed by the rebuild; Projects are
preserved. No data migration is attempted, and no manual reset is required for
an existing database.

Workspace IDs use the existing `internal/publicid` generator with prefix
`wsp_`.

## 2. Domain: `internal/workspace`

Mirror `internal/project`: a `Workspace` struct
(`ID, ProjectID, Name, CreatedAt, UpdatedAt, ArchivedAt *time.Time`), a
`Service` over `*store.Store`, and sentinel errors mapped to API error codes.

Rules (all enforced in store transactions, following the existing
`ArchiveProject` guarded-transaction and `CreateStarting` guarded-insert
patterns):

- **Create**: name required after trimming (same validation as Project name);
  rejected when the Project is missing (404) or archived (409).
- **Rename**: same name validation; allowed while archived.
- **Archive**: rejected while the Workspace has any active Session (409).
  Archived Sessions and ended Sessions do not block archive.
- **Unarchive**: rejected while the parent Project is archived (409).
- **List**: ordered `created_at DESC`; `includeArchived` filter; optional
  `projectId` filter (omitted lists all Workspaces, which the macOS Dashboard
  needs in one call per Connection).

### Workspace delete

Deletion follows ADR 0008 and deliberately avoids a new `deleting` state:

1. Read the Workspace (404 if missing) and list its active Sessions.
2. Stop each active Session through the existing `session.Service.Terminate`
   path. The first stop failure aborts the whole delete with that error
   (surfaced as 502 like other multiplexer failures); no metadata has been
   deleted.
3. In one transaction: re-check that no active Session references the
   Workspace — if a concurrent start slipped in, fail with 409 — then delete
   all Session rows for the Workspace and the Workspace row.

The 409 in step 3 is the entire concurrency story: session start already
guards on Workspace existence (Section 3), so a start that commits before the
final transaction makes the delete fail cleanly and the user retries. Stops
already performed are not rolled back; Terminate is safe to repeat.

### Project lifecycle changes

- `Archive` precondition changes from "no active Sessions" to "every Workspace
  is archived" (409 otherwise). Unarchive is unchanged.
- New `Delete`: allowed only when the Project has zero Workspaces (409
  otherwise). Deletion removes only the Project row.

## 3. Session Contract Changes

`session.StartInput` replaces `ProjectID`/`WorkingDir` with a required
`WorkspaceID`. Resolution: Workspace → Project → Project working directory,
revalidated at start exactly as `ResolveForStart` does today. The guarded
insert extends the existing pattern: the Session row is created only if the
Workspace exists and is not archived (and, transitively, its Project is not
archived — the archive preconditions maintain that invariant).

- `action` becomes optional. When omitted, the server launches the
  **Interactive Shell**: the host user's shell (`$SHELL`, falling back to
  `/bin/sh`) run through the selected Environment *without* a command payload —
  for `host-login-shell` the argv is `[$SHELL_OR_/bin/sh, -l, -i]`, i.e. the
  existing wrapper minus `-c`. `params` and `prompt` are rejected (400) when
  `action` is omitted, per ADR 0004.
- Session read payloads keep `workingDir` (still snapshotted at start) and gain
  `workspace: {id, name}`. They also keep a derived `project: {id, name}` ref so
  existing clients that group by Project keep working.
- New `Unarchive`: mirrors Project unarchive semantics (clears `archivedAt`).
- New `Delete`: if the Session is active, Terminate it first (a stop failure
  aborts the delete, metadata intact); then delete the row in one transaction.
  Never touches files.

### Action delete guard

`action.Store.Delete` gains a check: deletion of a custom Action (or of a
built-in override — which today reverts to the built-in — is exempt) is
rejected with 409 while any active Session references the Action name.
Terminated and archived Sessions never block deletion.

## 4. Action Type

`Action` gains `Type` (`toml:"type" json:"type"`), values `action` | `agent`,
defaulting to `action` when absent on create. Built-in `claude` and `codex`
are `agent`.

- Type is immutable: `PUT /api/actions/{name}` rejects a type change (400).
- An overlay entry that overrides a built-in inherits the built-in's type; an
  override declaring a different type is a config error (500 on read-through,
  400 on API write), consistent with ADR 0007 error conventions.
- The legacy `kind` parse in `Action.UnmarshalJSON` is repurposed rather than
  discarded: `"agent"` → `agent`, `"command"` → `action`, absent → default.
- `GET /api/actions` and `GET /api/actions/{name}` expose `type`.

## 5. API Routes

All routes follow the existing error envelope. New conflict-style failures use
409 with descriptive codes (suggested: `project_archived`,
`workspace_archived`, `workspace_has_active_sessions`,
`project_has_unarchived_workspaces`, `project_has_workspaces`,
`action_in_use`); exact slugs may follow existing taxonomy.

| Route | Notes |
| --- | --- |
| `POST /api/workspaces` | `{projectId, name}` → 201 Workspace |
| `GET /api/workspaces?projectId=&includeArchived=` | both filters optional |
| `GET /api/workspaces/{id}` | |
| `PATCH /api/workspaces/{id}` | rename only: `{name}` |
| `POST /api/workspaces/{id}/archive` | 409 with active Sessions |
| `POST /api/workspaces/{id}/unarchive` | 409 while Project archived |
| `DELETE /api/workspaces/{id}` | Section 2 procedure; 502 stop failure, 409 race |
| `GET /api/workspaces/{id}/sessions?includeArchived=` | |
| `POST /api/sessions/start` | `{workspaceId, action?, environment?, params?, prompt?, name?}` |
| `POST /api/sessions/{id}/unarchive` | new |
| `DELETE /api/sessions/{id}` | stops if active, then deletes metadata |
| `DELETE /api/projects/{id}` | 409 while Workspaces exist |

Changed routes: `POST /api/sessions/start` (contract), `GET /api/sessions*`
(payload gains `workspace`, nullable `action`), `GET /api/actions*` (adds
`type`), `DELETE /api/actions/{name}` (adds 409 guard),
`POST /api/projects/{id}/archive` (new precondition).

## 6. CLI

- New `atc workspaces` group: `create --project <id> --name <name>`, `list
  [--project <id>] [--include-archived]`, `show <id>`, `rename <id> <name>`,
  `archive <id>`, `unarchive <id>`, `delete <id>`.
- `sessions start`: `--workspace <id>` replaces `--dir`/`--project`; `--action`
  becomes optional (omitted starts the Interactive Shell); add `sessions
  unarchive <id>` and `sessions delete <id>`.
- `projects delete <id>`.
- `actions list` (and any show output) includes the Action type column.
- Destructive CLI commands print the files-are-not-touched statement in their
  confirmation/output, matching ADR 0008 language.

## 7. Web Admin and Reference

- Project detail page (`/projects/[id]`) manages that Project's Workspaces
  (create, rename, archive/unarchive, delete). A Workspace detail page
  (`/workspaces/[id]`) lists its Sessions with the archived filter and hosts
  Workspace-level actions. Session pages gain unarchive/delete. Every delete
  confirmation names the target, counts affected Sessions where relevant, and
  states that files are not touched.
- `web/src/lib/docs/endpoints.ts` gains entries for every route in Section 5,
  including CLI equivalents.
- New coverage test on the web side: assert every route named in
  `packages/contracts/fixtures/*.json` has an `ENDPOINTS` entry. Combined with
  the existing Go `TestContractFixturesCoverEveryRoute`, this closes the chain
  routes → fixtures → reference, satisfying the PRD's "no silently omitted
  endpoint" requirement mechanically.

## 8. Fixtures, ATCAPI, and the macOS Bridge

- New fixtures: workspace create/list/detail/rename/archive/unarchive/delete,
  workspace-sessions, session-unarchive, session-delete, project-delete.
  Updated fixtures: session-start (new contract), session detail/list
  (workspace ref, nullable action), actions (type).
- `packages/ATCKit` (`ATCAPI`): add `Workspace` model and endpoints; change
  `StartSessionRequest` to `workspaceId` + optional `action`; add `workspace`
  ref to Session models; expose Action `type`; update `MockATCClient` and the
  Swift contract/decoding tests.
- Minimal macOS bridge (explicitly throwaway, replaced by Phase 2):
  `CreateSessionSheet` gains a Workspace picker — a list of the Project's
  Workspaces fetched from the new endpoint plus an inline "new Workspace name"
  field. Sidebar grouping and the rest of the app are untouched; Sessions
  continue to display under their Project via the derived `project` ref.

## 9. Test Plan

Mapped to the PRD's server acceptance criteria, using existing patterns:

- **Store** (`internal/store`, real SQLite): migration runs on a populated
  0002 database and destroys Session rows while preserving Projects; FK
  enforcement; workspace delete transaction guards; project archive/delete
  preconditions.
- **Domain** (`internal/workspace`, `internal/session`, fake `Multiplexer`):
  same-named Workspaces per Project; archive/unarchive/create rules; delete
  stop-failure path leaves all metadata intact; interactive-shell argv; params
  and prompt rejected without an Action; action delete guard.
- **API** (`internal/api`): handler tests per new route including the delete
  failure and race paths; `contract_test.go` fixtures for every new route.
- **CLI** (`cli/*_test.go`): one test file per changed command group.
- **Web**: reference coverage test (Section 7); contract test updates.
- **Swift** (`ATCAPITests`): fixture decoding for Workspace and changed
  Session shapes.

## 10. Build Order

Each step leaves the repo green and is a jj checkpoint:

1. Migration + store layer (workspaces table, sessions rebuild, queries).
2. `internal/workspace` domain + Project lifecycle changes.
3. Session start contract, Interactive Shell launch, unarchive/delete.
4. Action type + delete guard.
5. API routes, handlers, error mapping, contract fixtures (Go tests green).
6. ATCAPI models + macOS bridge (Swift tests and app build green).
7. CLI commands.
8. Web admin pages, `endpoints.ts`, reference coverage test.

## Resolved Decisions

Decisions made while drafting this spec, for review:

- The migration destroys old Session rows instead of requiring a manual
  database reset (deterministic; an old database can never brick the server).
- No `deleting` state on Workspaces; the delete race resolves as a 409 retry.
- Session read payloads keep a derived `project` ref to soften client churn.
- Legacy `kind` values are mapped onto the new `type` instead of discarded.
- `GET /api/workspaces` without `projectId` lists all Workspaces.
- Phase 1 includes the ATCAPI update and a throwaway macOS Workspace picker so
  the monorepo quality gates stay green between phases.
