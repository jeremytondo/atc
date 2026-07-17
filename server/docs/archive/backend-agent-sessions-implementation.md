> Archived: superseded by `docs/plans/sessions-actions-environments.md` and `docs/specs/sessions-actions-environments.md`.
>
> **Lifecycle amendment (2026-07):** this plan predates the live/ended
> Session lifecycle (ATC-29). `MarkRunning`/`MarkTerminated` became
> `PromoteToLive`/`MarkEnded`, and `MarkFailed`/`MarkArchived` were removed:
> failed launches leave no Session and archiving no longer exists. The text
> below is preserved as originally written.

# Backend Agent Sessions — implementation playbook

The executable companion to [`backend-agent-sessions.md`](backend-agent-sessions.md).
Work it one phase at a time, each on its own branch. Each phase lists its
prerequisites and an exit gate so a fresh session can pick it up cold.

## Progress

- [x] **Phase 1** — Store foundation (`feat/agent-sessions-store`)
- [x] **Phase 2** — Core cutover · KEYSTONE (`feat/agent-sessions-core`)
- [x] **Phase 3** — Agent discovery API (`feat/agent-sessions-agents-api`)
- [x] **Phase 4** — Attach protocol (`feat/agent-sessions-attach`)
- [x] **Phase 5** — Config & agent registry surface (`feat/agent-sessions-config`)
- [x] **Phase 6** — CLI (`feat/agent-sessions-cli`)
- [x] **Phase 7** — Web app (`feat/agent-sessions-web`)

## Context

atc today proves the terminal-session loop (now archived at
`docs/archive/terminal-session-orchestration.md`): it launches registry agents in
`zmx`, lists atc-managed sessions by parsing a `atc:<kind>:<id>` multiplexer
name, sends text/keys, and bridges a WebSocket attach. That proof loop is **not** a
durable backend: identity lives in the `zmx` name, nothing is persisted, failed
launches vanish, and agents are hard-coded.

`server/docs/archive/backend-agent-sessions.md` (Draft v2, with ADR 0005 "SQLite for state"
and ADR 0006 "atc-owned identity") turns this into a stable contract: a
SQLite-backed session registry, opaque atc-owned `ses_` ids, agent discovery, a
resource-oriented `/api/sessions/{id}/...` API, a documented WebSocket attach
protocol, and plural `atc agents/sessions` CLI groups. The spec **supersedes** the
MVP where they conflict. Goal: implement that spec end-to-end (backend, CLI, embedded
SvelteKit app) so one developer can start, list, read, attach, send input to,
terminate, and archive agent sessions through one backend, with state surviving
restarts.

**Decisions locked:**
- DB stack: `modernc.org/sqlite` (pure-Go, preserves the CGO-free build) +
  `pressly/goose/v3` with embedded SQL migrations run in-process at startup.
- Scope: full backend + new CLI + update the embedded web app to the new contract.
- Phases ordered so the daemon runs on SQLite as early as possible (server wiring
  rides with the core cutover, not deferred to the end).

**Spec note (a brief/spec conflict, resolved for the spec):** the *brief* says the
start prompt is injected-but-not-submitted; spec v2 §7.2.3 supersedes — the prompt is
passed to the agent as a **single safely-quoted launch argument** (per the agent's
`prompt` placement), with no separate submit step. Follow the spec.

## Why 7 phases — the compile-coupling constraint

Go compiles the whole module: the moment `session.Service`'s public surface changes
(id-based, store-backed, `Scope`/`Kind` removed), **every compile-time importer breaks
in the same branch** and must change with it. Importers of `internal/session`:

- `internal/api/{routes,sessions,attach}.go` — call `Service.Start/Send/Key/List/
  Attach/EnsureExists`, use `session.Session`/`Scope`/`ScopeFrom` + sentinel errors.
- `internal/server/{server,router}.go` — construct `session.NewService(...)`, pass to
  `Router`/`Routes`.

So the domain rewrite, the API route rewrite, and the store/server wiring are **one
atomic green branch** (the *core cutover*, Phase 2). Faking separation with temporary
name-based + id-based shims would leave throwaway cruft — rejected.

What *is* cleanly separable:
- `internal/store` — new package, no importers → independent branch **before** cutover.
- **CLI** — HTTP-only over the Unix socket; does **not** import `internal/session`.
  Fully decoupled at compile time → independent branch **after** cutover.
- `GET /api/agents` (additive route), the attach-protocol refinement, the web app, and
  config polish (`internal/config` only references the `AgentRegistry` *type*; new agent
  fields are additive) → independent branches **after** cutover.

## Branching model

- Each phase = one branch forked from `agent-sessions-backend` (so it inherits the
  docs), merged back when its exit gate is green; the next phase forks from the updated
  `agent-sessions-backend`.
- When all phases are merged, `agent-sessions-backend` → `main` via PR.
- Every phase branch must keep `mise run test` (`go test ./...`) green so it is
  independently mergeable.

---

# Phases

## Phase 1 — Store foundation (`internal/store`)

**Branch:** `feat/agent-sessions-store` · **Prereqs:** none (independent; nothing
imports it yet).

- Add deps: `modernc.org/sqlite`, `github.com/pressly/goose/v3`.
- `store.go`: `Open(path) (*Store, error)` — ensure parent dir owner-only `0o700`
  (reuse the `paths.EnsureControlDir`-style logic), open the DB (`sqlite` driver,
  `_pragma=foreign_keys(1)`, `busy_timeout`), run migrations, fail clearly on
  open/migrate error. `Close()`.
- `migrations/0001_sessions.sql` + `//go:embed migrations/*.sql`; apply via `goose.Up`
  against the embedded FS. Schema = `sessions` (§6.1.1): `id TEXT PK`, `name`, `agent`,
  `params` (JSON TEXT), `working_dir`, `prompt`, `status`, `failure_reason`,
  `failure_code`, `created_at`/`updated_at`/`terminated_at`/`archived_at` (RFC3339 UTC
  or unix; convert at the boundary).
- CRUD/queries used by the domain: `CreateStarting`, `MarkRunning`, `MarkFailed(reason,
  code)`, `MarkTerminated`, `MarkArchived`, `Get(id)`, `List(filter)` (non-archived
  default, optional status filter, `created_at DESC`), plus a transaction seam for the
  start record→result update (§7.1.2).

**Exit gate:** `go test ./internal/store/...` green — temp DB opens, migration applies,
CRUD round-trips, list ordering + status filter. Package compiles (dead-but-tested
until Phase 2 wires it in).

## Phase 2 — Core cutover — KEYSTONE (`internal/session`, `internal/zmx`, `internal/api`, `internal/server`, `internal/paths`)

**Branch:** `feat/agent-sessions-core` · **Prereqs:** Phase 1 merged.

The big one. Atomic for the reason above — it lands the new identity model, the
store-backed domain, the new API routes, and the server/store wiring together so the
build stays green. After this branch the daemon runs end-to-end on SQLite and survives
restarts.

**Identity (`internal/session/id.go`, `internal/zmx/zmx.go`)**
- `newID()` → `ses_` + ≥128 bits base32hex/crockford, URL-safe, opaque (replaces
  `freeID`). Remove `Scope`/`Kind`, `ScopeFrom`, `sessionName`/`parseName` from the
  domain.
- `zmx.NameForID(id) string` — deterministic, recomputable
  (`atc-<hex(sha256(id))[:N]>`), used internally by Start/Send/Attach/List-filter/
  Terminate; never persisted, never exposed (§7.3). Add `zmx.Terminate(ctx, name)`
  (best-effort, idempotent for absent sessions); confirm `Attach` PTY `Resize` exists
  (it does). Keep all `zmx` command knowledge in this package.

**Domain (`internal/session/session.go` rewrite, store-backed, id-based)**
- `Service` gains a `*store.Store`. `Session` = full domain model (id, name, agent,
  params, workingDir, prompt, status, failureReason/Code, timestamps, `attachable`).
- `Start(ctx, StartInput)`: validate (agent exists → params → prompt-placement if
  prompt → workingDir non-empty), `store.CreateStarting`, build command via registry,
  `zmx.Start(NameForID(id), dir, cmd)`, then `MarkRunning` (return full session) or
  `MarkFailed` + return a typed launch error carrying `failureCode`+`sessionId`
  (§7.2.3, §9.2).
- `List(includeArchived, statusFilter)`, `Read(id)`: load from store, reconcile
  `attachable`/status from `zmx.List` liveness — mutate only on positive evidence,
  never on a failed liveness query (§7.2.4, §8.5).
- `SendText`/`SendKey(id, ...)`: resolve `NameForID`, require live (else
  `session_not_live`/`session_not_found`), inject via `zmx.Send`. Keep the existing key
  registry (`keys.go`: enter/ctrl-c/escape).
- `Attach(id)`: gate on live + status, return `zmx.PTY`.
- `Terminate(id)`: idempotent; `zmx.Terminate`, set `terminated`/`terminatedAt`.
- `Archive(id)`: metadata-only, reject live (`session_live`/409), set `archivedAt`,
  leave `status` unchanged.
- `Reconcile(ctx)`: startup pass over `starting`/`running` records (§8.5 rules).

**Agent registry (`internal/session/agent.go`)**
- Extend `Agent` with `Label`, `Description`, `Prompt *PromptSpec` (`Flag` string;
  absent ⇒ no prompt accepted), and `ParamSpec` with `Default`, `Label`, `Description`.
- `buildCommand` prompt handling: single `shellQuote`d arg, positional or behind
  `Flag`; `enum default ∈ values` validation; keep typed-param safety.
- (`Availability(ctx)` is needed only by `GET /api/agents` → defer to Phase 3; add the
  struct fields here so the shape is settled.)

**API (`internal/api` rewrite)**
- Migrate routing to a Go 1.22+ `http.ServeMux` with patterns + `r.PathValue("id")`:
  `POST /sessions/start`, `GET /sessions`, `GET /sessions/{id}`,
  `POST /sessions/{id}/send-text|send-key|terminate|archive`,
  `GET /sessions/{id}/attach`. Keep `writeJSON`/`errorResponse` helpers.
- `sessions.go` to new JSON shapes (§7.5.3): start (`agent,params,workingDir,prompt,
  name`) → full session; list omits prompt/params, honors `?status=`; detail includes
  them. Add send-text/send-key/terminate/archive.
- Central `error`-code mapper (§7.5.4): `invalid_request`, `unknown_agent`,
  `invalid_params`, `session_not_found`, `session_not_live`, `session_live`,
  `launch_failed`, `agent_misconfigured`, `internal_error` with the right statuses;
  launch failures return non-2xx with `error==failureCode` + `sessionId`. Update
  `writeSessionError` to `errors.Is` the domain sentinels. **Remove** `/sessions/send`,
  `/sessions/key`, `/sessions/attach?name=`.
- `attach.go`: route by id (`/sessions/{id}/attach`) via `Service.Attach`; keep current
  binary I/O + JSON resize + 1 MiB limit + current auth for now (protocol refinement is
  Phase 4).

**Wiring (`internal/paths`, `internal/server`)**
- `paths`: `StateDir`/`DBPath` — `$XDG_STATE_HOME/atc/atc.db`, fallback
  `~/.local/state/atc/atc.db` (distinct from the runtime control dir, which
  stays socket/PID/logs only).
- `server.Serve`: open the store (fail-clear) before serving, build `session.Service`
  with it, run `Reconcile`, close on shutdown. Update `Router`/`Routes` signatures and
  `server.Config`.

**Tests:** id format/uniqueness; deterministic `NameForID`; param/default/prompt
command construction incl. prompt-for-unsupported-agent error; unknown-agent /
invalid-params create no record; start success / launch-failure recording / `starting`
reconciliation (live→running, dead→failed) / liveness-failure leaves status unmutated;
API request validation, status-code mapping, response shaping, launch-failure body.
Reuse the existing `fakeMux` pattern (`session_test.go`) + a temp store.

**Exit gate:** `go test ./...` green. Smoke (set `XDG_STATE_HOME` to a temp dir; the
`ATC_DB_PATH` override is Phase 5): DB created/migrated on first start;
`POST /api/sessions/start` (valid→`running` full session; unknown agent→400 no record;
bad param→400; prompt to no-prompt agent→fail); `GET /api/sessions` (newest-first,
omits prompt/params, `?status=`); `GET /api/sessions/{id}`; send-text then send-key
`enter`; terminate (idempotent, then input fails); archive (rejects live, hidden from
default list, status preserved); restart → reconcile (running if live, else
failed/terminated), history persists. No `zmx` name appears in any response.

## Phase 3 — Agent discovery API (`internal/api/agents.go`)

**Branch:** `feat/agent-sessions-agents-api` · **Prereqs:** Phase 2.

- `GET /api/agents` → `{agents:[{name,label,description,availability,
  availabilityMessage,prompt,params:{...}}]}` (§7.5.3); per-param
  `type/values/default/flag/label/description`; optional `prompt` signals that the
  agent accepts an initial prompt (empty object = positional prompt); omit `bin`/`args`.
- Add registry `Availability(ctx)` (`available|unavailable|unknown` via
  `exec.LookPath`/cheap probe; never hides agents).

**Exit gate:** `go test ./internal/api/...` green; curl `GET /api/agents` returns
agents with availability + param schema.

## Phase 4 — Attach protocol (`internal/api/attach.go`, `internal/server/auth.go`)

**Branch:** `feat/agent-sessions-attach` · **Prereqs:** Phase 2.

- Drop the `?token=` path: remove `attachTokenMatches` in `auth.go`. Browsers present
  the token via `Sec-WebSocket-Protocol`; read it there and echo the accepted
  subprotocol in `websocket.AcceptOptions`. CLI/native use the `Authorization` header.
  No token in the URL (§7.6.3).
- Close codes (§7.6.2): `1000`/`session_ended` when the session ends (terminate, agent
  exit, reconciliation); `1011`/`internal_error` on server fault. Keep binary I/O +
  JSON resize + 1 MiB limit.

**Exit gate:** `go test ./internal/api/... ./internal/server/...` green; WS connect with
the token via `Sec-WebSocket-Protocol` only (none in URL), binary I/O + resize work,
`session_ended` close on terminate.

## Phase 5 — Config & agent registry surface (`internal/config`, agent TOML)

**Branch:** `feat/agent-sessions-config` · **Prereqs:** Phase 2.

- `StoreConfig{ DBPath string }` (`store.db_path`) + env `ATC_DB_PATH`, standard
  precedence, no CLI flag (layered over Phase 2's `paths.DBPath` default).
- Extend agent TOML for the new `label/description/prompt` + param `default/label/
  description` fields.

**Exit gate:** `go test ./internal/config/...` green; DB-path precedence
(env→config→default); agent-registry defaults vs file override with the new fields.

## Phase 6 — CLI (`cli/agents.go`, `cli/sessions.go`, `cli/root.go`, `cli/apiclient.go`)

**Branch:** `feat/agent-sessions-cli` · **Prereqs:** Phase 2 (+ Phase 3 for
`agents list`, + Phase 4 for attach subprotocol).

- Remove the singular `session` group (`cli/session.go`); add plural groups over the
  API. Reuse the existing `apiClient` (`cli/apiclient.go`, Unix-socket HTTP) — extend
  with a typed GET-with-query and the new endpoints.
- `agents list` (`-o text|json`, json matches `GET /api/agents`).
- `sessions`: `start` (`--agent --param k=v... --dir --prompt --name`, prints id +
  status), `list` (`-o`, omits prompt/params), `show <id>` (`-o`, full),
  `attach <id>`, `send-text <id> <text>`, `send-key <id> <key>`, `terminate <id>`,
  `archive <id>`. Action commands print the affected id and use exit status.
- `attach`: bridge the local terminal to the WS stream — add `golang.org/x/term`
  (near-standard terminal dep, justified under the dependency policy) for raw-mode +
  resize; connect with the `Authorization` header.

**Exit gate:** `go test ./cli/...` green (request/response incl. `-o json` against a
fake API server/socket — existing pattern); against a running daemon, `atc agents
list` and the `atc sessions` verbs work end-to-end; `atc sessions attach <id>`
bridges a real terminal.

## Phase 7 — Web app (`web/src/...`)

**Branch:** `feat/agent-sessions-web` · **Prereqs:** Phases 2, 3, 4.

- `routes/sessions/+page.svelte`: replace hard-coded `['claude','codex']` with a
  `GET /api/agents` fetch (render params from the schema); start body →
  `{agent,params,workingDir,prompt,name}`; list/show keyed by `id` + `status`/
  `attachable`; surface failed sessions.
- Terminal route → `/sessions/[id]`; WS URL → `/api/sessions/{id}/attach`; pass the
  token via `Sec-WebSocket-Protocol` (second arg to `new WebSocket`) instead of
  `?token=`; handle `session_ended`/`internal_error` close codes. Replace send/key
  usage with `send-text`/`send-key`.
- Rebuild + stage embedded assets (`mise run web:build` + `assets:stage`) so the Go
  binary serves the updated app.

**Exit gate:** web builds; staged assets served by the Go binary; manual — start a
session from the UI (agents from the API), attach in the browser (subprotocol auth, no
token in URL), confirm the agent is working, `session_ended` close on terminate.

---

## Critical files

- **New:** `internal/store/{store.go,store_test.go,migrations/0001_sessions.sql}`,
  `internal/session/id.go`, `internal/api/agents.go`, `cli/agents.go`,
  `cli/sessions.go`, `web/src/lib/session-dashboard.svelte`,
  `web/src/routes/sessions/[id]/{+page.svelte,+page.ts}`.
- **Rewrite:** `internal/session/{session.go,agent.go}`,
  `internal/api/{routes.go,sessions.go,attach.go}`, `cli/session.go` → removed,
  `web/src/routes/sessions/+page.svelte`,
  `web/src/routes/terminal/[name]/+page.svelte` → removed.
- **Modify:** `internal/zmx/zmx.go` (NameForID, Terminate), `internal/server/{server.go,
  router.go,auth.go}`, `internal/config/config.go`, `internal/paths/paths.go`,
  `cli/root.go`, `cli/apiclient.go`, `go.mod`/`go.sum`.

## End-to-end verification (after the full stack)

- `mise run test` (`go test ./...`) green; each phase independently green at its gate.
- Startup migration: launch against an empty temp DB path; confirm create/migrate and a
  clear failure on an unwritable path.
- Full loop against real `zmx` + Claude/Codex: `mise run serve`, start a session with a
  prompt, attach in the browser, confirm the agent is working; restart the service and
  confirm the session reconciles (`running` if live, else `failed`/`terminated`) and
  history persists.
- Confirm no `zmx` name appears in any API/CLI response.

## Out of scope (per spec non-goals)

Project/item associations, `Context` model, terminal-output persistence, SSE/events,
rename endpoint, generic diagnostics endpoint, multi-user/hosted, ACP.
