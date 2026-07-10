> **Historical (archived 2026-07):** Describes the pre-monorepo Cockpit-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# Tech Design — Remote File Browsing, Part 1: Cockpit `/api/fs`

> Implements the server half of `docs/remote-file-browsing-prd.md` and ADR
> `docs/adr/0001-remote-workspace-roots.md`. This part lands first, in the
> `cockpit` repo (https://github.com/jeremytondo/cockpit), so the AtelierCode
> client (Part 2, `docs/remote-file-browsing-design-2-app.md`) implements a real
> contract instead of inventing one. This document is self-contained: it can be
> handed to a session working only in the cockpit repo.

## 1. Scope

Add a read-only remote filesystem browse API to Cockpit:

- `GET /api/fs/roots` — usable Remote Workspace Roots.
- `GET /api/fs/list` — immediate children of one directory.
- A `[fs]` config section defining Remote Workspace Roots, with a synthesized
  `Home` root when none are configured.

No file reading, no mutation, no recursive walking, no fuzzy-find index. Those
are future endpoints; nothing here should block them.

## 2. Changes from the PRD (and why)

The PRD allowed judgment calls. These are the deltas this design makes:

1. **No `parent` field in the `fs/list` response.** The earlier exploration
   draft sketched one. With symlink-preserving lexical paths, the parent is a
   pure string operation the client must do anyway to enforce the
   active-root Up boundary — and the server *cannot* know the client's active
   root (a path can sit under multiple roots). Returning `parent` would imply
   server knowledge that doesn't exist. The response is `path` + `entries` +
   `truncated`, nothing else.
2. **`label` is optional in root config**, defaulting to the last path
   component. Less config to type; the PRD's "roots have `label` and `path`"
   still holds on the wire — the server always sends a label.
3. **Containment is defined as lexical-prefix containment** (§5.2). This is the
   concrete algorithm that operationalizes the ADR's "browsing namespace, not a
   resolved-path sandbox": the server never resolves symlinks for boundary
   checks, only for classifying entries.
4. **Non-regular, non-directory entries (sockets, FIFOs, devices) map to
   `kind: "unknown"`**, same as broken symlinks: visible, not enterable. The PRD
   only named broken symlinks; this rule covers everything that is neither a
   listable directory nor an ordinary file.
5. **Deterministic sort tie-break**: case-insensitive name compare, ties broken
   by byte-wise compare. The PRD asked for "case-insensitive by name," which is
   non-deterministic for names differing only by case.
6. **Roots are re-validated on every `fs/roots` request** (stat + open), not
   only at config load. This is what makes user story 6 (config drift
   troubleshooting) true at runtime; config load only validates shape.
7. **`size` is populated only for `kind: "file"`** (null for directories and
   unknown); `modifiedAt` is populated for files and directories (null for
   unknown). Directory byte sizes are filesystem noise.
8. **Timeout/cancellation map to `internal_error` (500)**, not a new error
   code. The PRD fixed the v1 error vocabulary at five codes; a server-side
   deadline is an infrastructure failure, not a contract state.

Everything else follows the PRD as written.

## 3. API contract

Both endpoints live under the existing `/api` mount, so bearer/Unix-socket auth
(`internal/server/auth.go`) applies automatically. JSON field names are
camelCase; error codes are lower_snake_case — both per existing convention.

### 3.1 `GET /api/fs/roots`

Returns the usable Remote Workspace Roots, in configuration order.

```json
{
  "roots": [
    { "label": "Projects", "path": "/home/jeremy/Projects" },
    { "label": "Home",     "path": "/home/jeremy" }
  ]
}
```

- `path` is always the expanded, cleaned, absolute path (`~` already resolved).
  Clients feed it directly to `fs/list`.
- A configured root is **omitted** (and logged at `warn`, §7) when, at request
  time, it does not exist, is not a directory, or cannot be opened for reading.
- Zero configured roots → the server synthesizes one root:
  `{ "label": "Home", "path": <os.UserHomeDir()> }`. Explicit configuration
  replaces the synthesized root entirely — if all configured roots are invalid,
  the response is `{ "roots": [] }` (the client shows an empty state).
- Errors: only `internal_error` (500), e.g. home-dir resolution failure with no
  configured roots.

### 3.2 `GET /api/fs/list?path=<abs>&showHidden=<bool>`

Lists the immediate children of one directory. Directory-only and lazy — never
recursive.

Query parameters:

| Param        | Required | Meaning                                                        |
|--------------|----------|----------------------------------------------------------------|
| `path`       | yes      | Absolute path on the workstation. Cleaned server-side (§5.1).  |
| `showHidden` | no       | Include dot-prefixed entries. Default `false`.                 |

Success (200):

```json
{
  "path": "/home/jeremy/Projects/atelier",
  "truncated": false,
  "entries": [
    { "name": ".config", "path": "/home/jeremy/Projects/atelier/.config",
      "kind": "directory", "isSymlink": false,
      "modifiedAt": "2026-07-05T14:03:22.123456789Z" },
    { "name": "src", "path": "/home/jeremy/Projects/atelier/src",
      "kind": "directory", "isSymlink": true,
      "modifiedAt": "2026-07-01T09:00:00Z" },
    { "name": "README.md", "path": "/home/jeremy/Projects/atelier/README.md",
      "kind": "file", "isSymlink": false,
      "size": 2048, "modifiedAt": "2026-06-28T18:12:45.5Z" },
    { "name": "dangling", "path": "/home/jeremy/Projects/atelier/dangling",
      "kind": "unknown", "isSymlink": true }
  ]
}
```

Field semantics:

- `path` — the cleaned **lexical** path that was listed. Never
  symlink-resolved; if the client navigated `/Projects/shared/...` through a
  symlink, that is what comes back. This string is the client's canonical
  current-directory state.
- `entries[].path` — `path` + `/` + `name` (lexical). Path strings are entry
  identity; there are no entry IDs.
- `kind` — `directory` | `file` | `unknown`.
  - Symlinks are classified by their **target**: a symlink to a directory is
    `directory` (enterable like any folder), to a regular file is `file`.
  - Broken symlinks, sockets, FIFOs, and devices are `unknown` — visible, not
    enterable.
- `isSymlink` — from `lstat`, orthogonal to `kind`.
- `size` — bytes; present only for `kind: "file"` (of the symlink target when
  applicable). Absent otherwise.
- `modifiedAt` — RFC3339Nano UTC (existing `formatTime` convention); present
  for `file` and `directory`, absent for `unknown`.
- `truncated` — `true` when the listing was cut at the cap (§5.5).
- Empty directory → `"entries": []` (never `null`; pre-allocate the slice, same
  as `sessions.go`).

Ordering (stable, applied server-side; clients may re-sort):

1. Directories before non-directories (`file` and `unknown` are one group).
2. Within each group, dot-prefixed names before others.
3. Then case-insensitive name compare; ties broken byte-wise.

Errors — existing envelope `{"error": <code>, "message": <human text>}`:

| Code                      | HTTP | When                                                                 |
|---------------------------|------|----------------------------------------------------------------------|
| `invalid_path`            | 400  | Missing/empty `path`, not absolute, or contains a NUL byte.          |
| `outside_browsable_roots` | 403  | Cleaned path is not lexically inside any configured root (§5.2).     |
| `not_found`               | 404  | Path does not exist (including a dangling symlink as the target).    |
| `not_directory`           | 400  | Path exists but is not a directory (files, sockets, etc.).           |
| `permission_denied`       | 403  | The requested directory itself cannot be read.                       |
| `internal_error`          | 500  | Timeout, unexpected I/O failure.                                     |

Invalid `showHidden` values follow the existing `boolQuery` helper's behavior
for consistency with `GET /sessions`.

## 4. Configuration

New TOML section (parsed by the existing `pelletier/go-toml/v2` loader in
`internal/config/config.go`):

```toml
[[fs.roots]]
label = "Projects"      # optional — defaults to the last path component
path  = "~/Projects"

[[fs.roots]]
path  = "/srv/work"     # label defaults to "work"
```

Struct additions to `config.Config` (`config.go:43`):

```go
FS FSConfig `toml:"fs"`

type FSConfig struct {
    Roots []FSRootConfig `toml:"roots"`
}

type FSRootConfig struct {
    Label string `toml:"label"`
    Path  string `toml:"path"`
}
```

Load-time validation (in `Config.validate()`, following the `Environments`
registry precedent):

- `path` must be non-empty. Error otherwise.
- `~` and `~/...` expand to `os.UserHomeDir()`. `~user` and environment
  variables are **not** expanded (PRD: out of scope) — a path containing `$` is
  passed through literally.
- After expansion the path must be absolute; it is `filepath.Clean`ed.
- Duplicate expanded paths are a config error. Duplicate labels are allowed
  (paths are identity).
- Empty `label` → last path component of the expanded path.
- Existence/readability is **not** checked at load time — that is runtime
  drift, handled per-request by `fs/roots` (§2 item 6).

No env var or flag for roots in v1 (config file only). No TOML knob for the
listing cap or timeout — they are constants (§5.5, §5.6).

## 5. Semantics

### 5.1 Path normalization

For every `fs/list` request:

1. Reject empty, non-absolute, or NUL-containing paths → `invalid_path`.
2. `filepath.Clean` the path. This collapses `//`, `/./`, and resolves `..`
   **lexically** — which is exactly the PRD's rule that raw `..` segments are
   allowed only when the normalized result stays inside the namespace.
3. All subsequent checks and all filesystem calls use the cleaned lexical path.

The server never calls `filepath.EvalSymlinks` on the request path. The OS
resolves symlinks naturally when `os.Stat`/`os.ReadDir` hit the lexical path —
that is what keeps breadcrumbs on the path the user followed while still
listing real contents.

### 5.2 Containment: the lexical namespace

A cleaned path `p` is **inside the browsable namespace** iff for some root `r`
(expanded, cleaned):

```go
p == r || strings.HasPrefix(p, r + "/")   // special-case r == "/"
```

Consequences (all intended, per the ADR):

- Descending through a directory symlink keeps the lexical path under the root,
  so symlinked subtrees remain browseable even when they resolve outside the
  root. The boundary is a namespace, not a sandbox.
- Pasting the symlink's *resolved* target (e.g. `/mnt/big/shared`) when only
  `/home/jeremy/Projects` is a root → `outside_browsable_roots`. Consistent:
  the namespace is defined by lexical paths.
- Symlink cycles cannot hang the server: listing is one directory per request
  and never recursive; a user can drill into a cycle forever, one bounded
  request at a time.
- When no roots are configured, the synthesized Home root defines the
  namespace.

### 5.3 Entry classification

For each `os.ReadDir` entry of the (followed) directory:

| Condition (lstat, then stat for symlinks)         | `kind`      | `isSymlink` |
|---------------------------------------------------|-------------|-------------|
| directory                                         | `directory` | false       |
| regular file                                      | `file`      | false       |
| symlink → directory                               | `directory` | true        |
| symlink → regular file                            | `file`      | true        |
| symlink → missing target (broken)                 | `unknown`   | true        |
| symlink → socket/FIFO/device                      | `unknown`   | true        |
| socket / FIFO / device / other                    | `unknown`   | false       |

`size`/`modifiedAt` come from the followed (`os.Stat`) info; if that stat
fails, the entry stays visible as `unknown` with both fields absent.

An unreadable directory under a readable parent is still classified
`directory` here (lstat/stat succeed; only reading it fails) — it appears in
listings, and attempting to list it returns `permission_denied`. If the
**requested** directory is unreadable, the request itself is
`permission_denied`.

### 5.4 Hidden filtering

An entry is hidden iff its name starts with `.`. With `showHidden=false`
(default), hidden entries are dropped **before** sorting and the cap, so
truncation counts only entries the client will see.

### 5.5 Cap and truncation

Pipeline: read all entries → filter hidden → sort → cap at **10,000** →
`truncated: true` if cut. (`os.ReadDir` reads the whole directory regardless,
so the cap protects response size and client memory, not server reads.)

### 5.6 Cancellation and timeout

- Handlers pass `r.Context()` down, so a client disconnect cancels metadata
  work. Check `ctx.Err()` every ~1,000 entries during the per-entry stat pass.
- The service wraps each `List` in `context.WithTimeout(ctx, 10*time.Second)`.
  The blocking `os.ReadDir` runs in a goroutine raced against `ctx.Done()`
  (a leaked goroutine on a dead-NFS hang is acceptable; note it in a comment).
- Deadline exceeded → `internal_error` (500). A canceled client context just
  aborts; the response is unread.

The timeout is a field on the service (default 10s) so tests can inject a tiny
value — it is not exposed in config.

## 6. Implementation plan

Follows the existing package layout: a domain package + thin API handlers.

### 6.1 New package `internal/fs`

Mirrors `internal/session`'s shape (domain service + sentinel errors):

```go
package fs

type Root struct{ Label, Path string }          // Path: expanded, cleaned, absolute

type Entry struct {
    Name, Path, Kind string                     // Kind: "directory" | "file" | "unknown"
    IsSymlink        bool
    Size             *int64
    ModifiedAt       *time.Time
}

type Listing struct {
    Path      string
    Entries   []Entry
    Truncated bool
}

var (
    ErrInvalidPath      = errors.New("invalid path")
    ErrOutsideRoots     = errors.New("outside browsable roots")
    ErrNotFound         = errors.New("not found")
    ErrNotDirectory     = errors.New("not a directory")
    ErrPermissionDenied = errors.New("permission denied")
)

func NewService(roots []Root, logger *slog.Logger) *Service
func (s *Service) Roots(ctx context.Context) []Root                 // stat+open filtered, logs omissions
func (s *Service) List(ctx context.Context, path string, showHidden bool) (Listing, error)
```

- Home-root synthesis happens in `NewService` (or a small constructor helper)
  when `roots` is empty, so `Roots`/`List` always operate on a concrete
  namespace.
- OS error mapping: `errors.Is(err, iofs.ErrNotExist)` → `ErrNotFound`,
  `iofs.ErrPermission` → `ErrPermissionDenied`, `syscall.ENOTDIR` (and
  stat-says-not-dir) → `ErrNotDirectory`.
- POSIX-only assumptions (path separators, dot-hidden convention) are fine —
  Cockpit targets Linux/macOS workstations.

### 6.2 API handlers `internal/api/fs.go`

Follow the canonical handler pattern (`sessions.go`):

- `apiRoutes` (`routes.go:12`) gains `fs *fs.Service`; `Routes(...)` gains the
  parameter. Handlers guard with a `requireFS` helper (same shape as
  `requireSessions`), tolerating nil for isolated tests.
- Register in `Routes`:
  `mux.HandleFunc("GET /fs/roots", routes.fsRoots)` and
  `mux.HandleFunc("GET /fs/list", routes.fsList)`.
- Response DTO structs local to `fs.go` (`rootsResponse`, `listResponse`,
  `entryResponse`) with camelCase tags; `modifiedAt` via the existing
  `formatOptionalTime`; `entries` pre-allocated with `make([]entryResponse, 0, n)`.
- `writeFSError(w, err)` mapper switching on the `fs` sentinels per the table
  in §3.2, defaulting to 500 `internal_error`.
- `showHidden` parsed with the existing `boolQuery` helper.

### 6.3 Plumbing

Thread the config exactly like `Environments`/`AuthToken`:

1. `config.Config.FS` (§4) with validation + `~` expansion in `validate()`.
2. `cli/root.go:99` — map into `server.Config` (new field, e.g.
   `FSRoots []fs.Root`).
3. `internal/server/server.go` — construct `fs.NewService(cfg.FSRoots, logger)`
   next to the session service; pass to `Router`.
4. `internal/server/router.go` — pass through to `api.Routes(...)`.

Auth requires zero new code: the `/api/` mount already wraps everything.

### 6.4 Logging

`log/slog`, key/value style, from the `fs` service (the api package does not
log, per existing convention):

- Root omitted: `logger.Warn("fs root unusable", "label", r.Label, "path", r.Path, "err", err)` — every `fs/roots` request that omits it (drift stays visible).
- Optional debug on `List` failures. No logging on the success path.

## 7. Test plan

Standard `testing` + `net/http/httptest` + `t.TempDir()`, tests beside code
(`internal/fs/service_test.go`, `internal/api/fs_test.go`,
`internal/config/config_test.go` additions). Build real temp trees with
`os.MkdirAll` / `os.WriteFile` / `os.Symlink` / `os.Chmod`.

Permission-denied cases use `chmod 0000` and must
`t.Skip` when `os.Geteuid() == 0` (root ignores modes).

**Config (`internal/config`)**
- `[fs.roots]` parses; `~` and `~/x` expand to home; label defaults to
  basename; empty path errors; relative path errors; duplicate expanded paths
  error; `$VAR` passes through literally; absent `[fs]` section → empty roots.

**Service — roots (`internal/fs`)**
- No roots configured → synthesized Home root at `os.UserHomeDir()`.
- Configured roots returned in order with expanded paths.
- Missing / non-directory / unreadable root omitted and logged; other roots
  survive. All-invalid → empty slice (not Home fallback).

**Service — list**
- Happy path: mixed tree lists with correct `name`/`path`/`kind`/`isSymlink`;
  `size` only on files; `modifiedAt` on files+dirs; empty dir → `[]`.
- Sorting: dirs first; dotfiles first within group; case-insensitive order;
  case-only tie broken deterministically.
- Hidden: excluded by default; included with `showHidden`; cap counts
  post-filter.
- Truncation: directory with cap+N entries → 10,000 entries, `truncated: true`
  (use a lowered cap via an internal field if 10k files per test is too slow —
  prefer testing the real constant once).
- Normalization: trailing slash, `//`, `/./`, and `a/b/../b` all list; `..`
  climbing lexically outside every root → `ErrOutsideRoots`; relative/empty/NUL
  → `ErrInvalidPath`.
- Containment: path equal to root OK; sibling-with-common-prefix
  (`/root2` vs root `/root`) → `ErrOutsideRoots`; path under a symlinked dir
  inside the root (lexical) OK even when target is outside; pasting the
  resolved outside target → `ErrOutsideRoots`.
- Symlinks: dir symlink listed as `directory` and enterable (listing it returns
  the target's children under the *lexical* path); file symlink → `file`;
  broken symlink → `unknown`, `isSymlink: true`, no size/modifiedAt; FIFO →
  `unknown` (use `syscall.Mkfifo`).
- Errors: missing path → `ErrNotFound`; file path → `ErrNotDirectory`;
  unreadable requested dir → `ErrPermissionDenied`; unreadable child remains
  visible as `directory` in the parent listing.
- Cancellation: pre-canceled context → error promptly. Timeout: service with
  1ns timeout → `internal_error`-mapped failure.

**Handlers (`internal/api`)**
- `GET /fs/roots` → 200 `{"roots":[...]}` shape; empty → `{"roots":[]}`.
- `GET /fs/list` happy path → 200, camelCase fields, RFC3339Nano `modifiedAt`.
- Each error code → exact `{error,message}` body + status per §3.2 table.
- `showHidden=true` honored; absent → hidden filtered.
- nil fs service → 500 `internal_error` (the `requireFS` guard), matching
  `Routes(diagnostics, nil, nil)`-style isolated tests.
- Auth: one test confirming `/api/fs/roots` 401s without a token when a token
  is configured (piggybacks existing auth tests).

## 8. Manual verification

Against a live server (Unix socket is auth-free; TCP needs the bearer token):

```sh
curl -s -H "Authorization: Bearer $TOKEN" "http://HOST:PORT/api/fs/roots" | jq
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://HOST:PORT/api/fs/list?path=/home/jeremy/Projects&showHidden=true" | jq
# error spot-checks
curl -s ... "…/api/fs/list?path=/etc/passwd"        # → not_directory
curl -s ... "…/api/fs/list?path=relative/path"      # → invalid_path
curl -s ... "…/api/fs/list?path=/nonexistent-root"  # → outside_browsable_roots
```

Capture the real `fs/roots` and `fs/list` response bodies (including one error
body) — Part 2 uses verbatim captured fixtures for its decoding tests, per the
existing CockpitKit convention.

## 9. Milestones

1. **Config**: `FSConfig` + validation + `~` expansion + tests.
2. **Domain**: `internal/fs` service — roots synthesis/filtering, normalization,
   containment, classification, sort/cap, timeout — with the full service test
   suite.
3. **API**: handlers, error mapper, route registration, plumbing through
   `cli/root.go` → `server.Config` → `Router` → `api.Routes`, handler tests.
4. **Verify**: run against the real workstation; walk a symlinked dir, an
   unreadable dir, a huge dir; capture fixtures for Part 2.

Acceptance: all §7 tests pass; the §8 manual checks return the documented
shapes and codes; fixtures are captured and handed to Part 2.
