> **Historical (archived 2026-07):** Describes the pre-monorepo Cockpit-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# Implementation Plan — Cockpit Projects

Spec: `docs/specs/projects.md`. Brief: `docs/ideas/projects.md`.

Three phases. Each phase ends green (`go test ./...`, `git diff --check`;
phase 2 also `npm run build`) and is independently mergeable.

## Phase 0 — Cohesion Groundwork

No Projects code; makes the codebase ready and better on its own.

1. **`internal/publicid`**: extract the ID generator from
   `internal/session/id.go` into `publicid.New(prefix string)`. Session keeps
   its `ses_` call site. Unit test the encoding shape once, here.
2. **Share Go wire types**: export the session wire structs and response
   envelopes in `internal/api/sessions.go`; delete the duplicate declarations
   in `cli/sessions.go` and import `internal/api` from the CLI. No wire
   change — assert with existing CLI end-to-end tests.
3. **Rename `store.ErrNotFound` → `store.ErrSessionNotFound`** and update the
   store package doc comment to "Cockpit-owned state". Mechanical; compiler
   finds every caller.
4. **Validate working directory on all Session starts**: add the
   absolute/exists/is-directory check to `session.Start` (helper placed where
   phase 1 will adopt it), map it to 400 `invalid_working_dir` in
   `writeSessionError`, update session/api/CLI tests that start sessions with
   fake directories to use `t.TempDir()`.

## Phase 1 — Backend, API, CLI

The full Projects contract for non-web clients.

1. **Migration `0002_projects.sql`**: `projects` table +
   `sessions.project_id` FK column + indexes, per spec. Store tests assert
   schema and that pre-existing session rows read back with no project.
2. **Store layer**: `store.Project` types, CRUD/rename/archive/unarchive
   queries, `ProjectListFilter`, `ListFilter.ProjectID`, LEFT JOIN hydration
   of `store.Session.Project`, `CreateSessionInput.ProjectID`.
3. **`internal/project` domain**: `Project`, sentinel errors,
   `ValidateWorkingDir`, `Service` with `Create`/`Get`/`List`/`Rename`/
   `Archive`/`Unarchive`/`ResolveForStart`. Phase 0's session start
   validation moves onto/uses this helper so the rule exists once.
4. **Session domain integration**: `StartInput.ProjectID`,
   `ProjectResolver` dependency, inherit-and-snapshot working directory,
   archived-project rejection, `Session.Project` ref, `List` project filter.
5. **Server wiring**: construct `project.NewService` in `server.Serve`, pass
   it to `session.NewService` (resolver) and `api.Routes`.
6. **API**: `internal/api/projects.go` with the seven routes, exported wire
   structs, `writeProjectError`, strict-decode PATCH; `sessions.go` gains
   `projectId` on start, the exclusivity check, the nested `project` object,
   and the project-scoped listing handler sharing the session list
   serialization.
7. **CLI**: `cli/projects.go` command family, `apiClient.patch`, `--dir`
   resolution (relative/`~`), `sessions start --project` with `--dir`
   mutual exclusion (and no cwd default), `sessions list --project`,
   `sessions show` project fields in text output.
8. **Tests** per the spec's test plan (store, project, session, api, cli).
9. **Docs**: update `README.md` API/CLI sections; add an ADR only if a
   decision proves contentious during review (the brief already records the
   v1 decisions).

## Phase 2 — Admin Web UI

1. **`api.ts`**: `Project` types, seven Project functions, optional
   `project` on session types, `listSessions({ includeArchived })`.
2. **`ErrorBanner` component**: extract, use in new pages, swap into
   sessions/environments/actions pages.
3. **`/projects` list page**: newest-first list, include-archived toggle,
   create modal (`ProjectEditor`: name + directory text input).
4. **`/projects/[id]` detail page**: record fields, rename, archive/
   unarchive, Sessions list (include-archived toggle, rows link to the
   session view), Start Session form (Action/Environment selects, name,
   prompt, params from the Action's spec) posting `projectId`.
5. **Sessions page retrofit**: project name/link on rows, include-archived
   control.
6. **Reference console**: widen `endpoints.ts` method union with `PATCH`,
   teach the Try-it builder PATCH bodies, add the Project endpoints.
7. **Nav**: Projects entry in the sidebar shell.
8. **Verify**: `npm run build`, manual pass over create → start session →
   archive → unarchive flows against a running daemon.

## Deferred (from spec/brief)

- `/api/fs`-backed directory picker in the create form.
- Everything in the brief's non-goals list (deletion, dir changes, search,
  templates, app clients, …).
