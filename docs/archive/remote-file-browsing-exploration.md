> **Historical (archived 2026-07):** Describes the pre-monorepo Cockpit-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# Remote File Browsing — Exploration / Rough Draft

> **Status: exploratory. Nothing here is decided.** This is a thinking document
> capturing the problem, what we learned from the current code, and a *design
> space* of options. Where a direction is currently appealing it's called out as
> a "leaning" — a starting hypothesis to poke at, not a commitment. Expect this
> to change as we explore further. No code has been written against any of it.

## Overview

We want AtelierCode to work with the file trees on the remote Cockpit
workstation — browsing, picking, and selecting files/folders. This is broad, and
we expect to grow into several UI shapes over time:

- a **file-tree browser** (drill down / expand a directory tree),
- a **fuzzy-finder picker** (type-to-match a file within a project),
- and likely others we haven't named yet.

Everything the app knows about the remote host comes through the **Cockpit
API**, so any of this is really two intertwined problems: (1) the **SwiftUI**
mechanisms to render and drive each interaction, and (2) the **Cockpit API**
surface that feeds them. Neither exists yet.

The proposed *first* concrete target — small enough to learn from, useful on its
own — is **selecting a remote folder when starting a new session**. The rest of
this doc explores that slice while trying to keep the door open for the bigger
picture.

A useful reference for the *shape* of a mature file-tree component (web, so not
directly usable here, but worth borrowing ideas from):
<https://trees.software/>.

## Where things stand today

Two findings frame the whole effort:

1. **The current folder picker is local, and that's semantically wrong.**
   `CreateSessionSheet` uses SwiftUI's `.fileImporter` to pick a folder on *this
   Mac* and copies its path into `workingDir` — but `workingDir` is a path on
   the *Cockpit workstation*. The field's own help text admits it: "Choose a
   local folder (path is used on the server)." So this feature isn't net-new
   surface so much as fixing an existing mismatch.

2. **Cockpit has no filesystem API at all.** There are no list/browse/find
   endpoints. `workingDir` is passed straight through to the session as an
   opaque string — never listed, validated, or resolved. So *any* remote
   browsing depends on new server work; it cannot be built against today's API.

A helpful third observation: Cockpit's existing **"actions/environments
discovery + dynamic param renderer"** pattern is a close analog to a directory
browser (server describes a shape → client fetches and renders it generically),
and the app's async-load conventions (`.task`, `@State` for loading/error,
`ContentUnavailableView`, `ProgressView`) would carry over. So there's a
well-worn groove to follow if/when we build.

## Ideas worth borrowing from trees.software

Not endorsing the library — just its concepts, which seem sound for a native
reimplementation:

- **Path-as-identity.** Canonical path strings are the primary key for a node,
  not object references. Keeps UI state and data in sync trivially. For
  symlinked directories, navigation preserves the path the user followed (for
  example `/Projects/shared/...`) rather than rewriting breadcrumbs to the
  symlink's resolved target.
- **Focus vs. selection as distinct concepts.** "Where keyboard actions land"
  is separate from "what the app treats as chosen." For a folder *picker* these
  genuinely differ (you navigate through folders you don't ultimately pick).
  For the first picker, the **Highlighted Entry** drives navigation: pressing
  Enter on a highlighted folder drills into it; single-click highlights with the
  mouse; double-click drills in. The **Chosen Folder** is the current viewed
  directory committed by a separate UI action, such as "Use This Folder."
- **Framework-agnostic core.** The tree logic is a plain model any UI can wrap.
  Suggests keeping our browsing logic out of the views.
- **Prepare once, off the UI boundary.** They lean toward shaping the tree on
  the server rather than doing heavy work in the client.
- **Search modes** (for an eventual fuzzy finder): hide-non-matches /
  collapse-non-matches / expand-matches.

## The design space

### API: likely two loading modes, not one

The two future use cases pull in different directions, and it may be cleanest to
name both even if we only build one first:

- **Browse — lazy, one directory at a time.** For "pick a folder anywhere," you
  can't enumerate a whole host up front; load per-directory on demand. Natural
  fit for a drill-down picker or an expandable tree.
- **Find — bounded set prepared up front.** For a fuzzy finder scoped to a
  project root, walk once on the server (potentially `.gitignore`-aware, capped)
  and let the client match on every keystroke. This is the trees.software
  "prepare once" idea. Keep this as a separate future endpoint (for example
  `GET /api/fs/prepare?...` or `GET /api/fs/find-index?...`) rather than making
  `fs/list` do recursive search.

These aren't mutually exclusive — they'd likely be two endpoints serving two
interaction styles. Only *browse* is needed for the folder-picker slice.

Browse API shape. The browse API should return both files and folders from day
one, even though the first picker UI only enables selecting folders:

```
GET /api/fs/roots
  → { "roots": [ { "label":"Projects", "path":"/home/me/Projects" } ] }
```

```
GET /api/fs/list?path=<abs>&showHidden=<bool>
  → { "path":    "/…/atelier",          // canonicalized absolute
      "parent":  "/…",                  // null when at a boundary
      "truncated": false,
      "entries": [ { "name":"src", "path":"/…/atelier/src", "kind":"directory", "isSymlink":false, "size":4096, "modifiedAt":"…" }, … ] }
```

Use path strings as entry identity and navigation state. Do not introduce
separate entry IDs unless Cockpit later supports virtual roots or non-filesystem
providers. Hidden entries are excluded by default (`showHidden=false`), but the
endpoint supports including them because developer workflows often need
directories such as `.git`, `.codex`, or `.config`.
V1 entry metadata should stay focused: `name`, `path`, `kind`, and `isSymlink`
are required; `size` and `modifiedAt` are optional/nullable. Defer permissions,
owner/group, MIME type, git status, children counts, and resolved symlink
targets. Broken symlinks should be returned as `kind: "unknown"` with
`isSymlink: true`; they are visible but not enterable.
Unreadable directories discovered while listing a readable parent should remain
visible as directory entries; attempting to enter them returns
`permission_denied`. If the requested parent itself is unreadable, `fs/list`
returns `permission_denied` for that request.
Cockpit should clean/normalize requested paths before validation. Raw `..`
segments are allowed only when the normalized result remains in the browsable
namespace.
Cockpit should return a stable default order: directories first, then files,
dot-prefixed entries before others within each group, then case-insensitive by
name. Clients may still re-sort for their own UI.
`GET /api/fs/list` is directory-only: it returns children for directory paths
and returns a typed `not_directory` error for file paths. Files are still
included as entries in directory listings, and future file viewing should use a
separate endpoint such as `GET /api/fs/read?path=...`.
`fs/list` should apply a generous server-side entry cap of 10,000 entries and
return `truncated: true` when the result was cut off, so huge directories cannot
make the picker sluggish or memory-heavy.
V1 `fs/list` errors should use Cockpit's existing error envelope with one of:
`not_found`, `not_directory`, `permission_denied`, `outside_browsable_roots`,
or `invalid_path`.
Cockpit should respect normal request-context cancellation and apply a
server-side timeout for slow listings; no custom cancel endpoint is needed.

Whatever we pick should fit Cockpit's existing conventions (stdlib `net/http`
`ServeMux`, `writeJSON`, named-wrapper responses like `{"entries":[…]}`, the
`{error,message}` envelope, free bearer auth under `/api`).

### Access scope: Remote Workspace Roots

Cockpit's auth today is all-or-nothing at the transport, and session-start
already runs arbitrary commands in any `workingDir` — so *listing* doesn't
really widen the trust boundary, but it does widen what's easily readable.
AtelierCode should browse only inside **Remote Workspace Roots**: named folders
on the Cockpit workstation that define starting namespaces for
remote browsing and session working-directory selection.

Alternatives considered:

- **Full-host browse**, seeded at `$HOME`, relying on the existing token
  boundary (+ a symlink/`..` escape guard).
- **Remote Workspace Roots** — a new Cockpit config section names the parent
  folders the API is allowed to expose as browsing starting points. This is not
  a resolved-path sandbox: directory symlinks reachable from a root remain
  browseable, including their children, even when the symlink target resolves
  outside the root.
- **Hybrid** — browse widely but seed the UI with configured favorites /
  recents.

**Decision:** use Remote Workspace Roots, expressed as settings in Cockpit. This
turns the browsing boundary into a small config list and gives the client an
obvious set of starting points. For the first version, roots are static server
configuration: AtelierCode reads and displays them, but does not add, edit, or
remove them at runtime. Root paths may use `~` for the server user's home
directory, but v1 should not expand arbitrary environment variables. The exact
config shape is still open. A rough illustration only:

```
# candidate config — shape TBD
[[fs.roots]]
label = "Projects"
path  = "/…/Projects"
```

If no roots are configured, Cockpit should synthesize a default `Home` root at
the server user's `$HOME` so the feature works out of the box. Explicit config
replaces that default. If configured roots are invalid or unreadable, Cockpit
should omit them from `fs/roots` and log the cause server-side; the picker shows
an empty state if no usable roots are returned.

A small `GET /api/fs/roots` companion endpoint (returning the allowed roots) is
a natural partner to `GET /api/fs/list`. Directory listing should treat symlinks
like normal filesystem entries: a directory symlink is browsed like a directory,
and a file symlink is represented like a file. The API can still expose
`isSymlink` metadata, but symlinks do not need traversal tokens or special
capabilities. Broken symlinks appear as visible, non-enterable unknown entries.
For the first version, Remote Workspace Roots constrain remote browsing and
AtelierCode-driven working-directory selection only. They are not yet a global
Cockpit execution policy: `POST /api/sessions/start` can keep accepting
arbitrary `workingDir` strings until Cockpit intentionally introduces broader
workspace enforcement.

### Client architecture: layered, with a reusable core

Borrowing trees.software's "framework-agnostic core," a candidate layering that
lets one investment serve all three future UIs:

1. **`CockpitAPI` (package):** directory-listing method(s) on the `CockpitClient`
   protocol + `HTTPCockpitClient` + `MockCockpitClient`, plus `RemoteEntry` /
   `DirectoryListing` DTOs (with `RemoteEntry.id == path`). Pure Foundation, no
   UI — same boundary the rest of the package respects.
2. **A narrow app-side `@Observable` browsing model** (e.g.
   `RemoteFileBrowser` in `Features/RemoteFiles/`): picker workflow state such
   as current listing, loading/error, selected path, typed path, `showHidden`,
   and commands such as `loadRoots`, `openRoot`, `descend`, `goUp`, and
   `setPath`. The Cockpit-owned remote filesystem contract belongs in
   `CockpitKit` (DTOs and `CockpitClient` methods); `RemoteFileBrowser` belongs
   in the app because it represents what the picker is currently doing with
   that data. It is not an API DTO, not a SwiftUI view, and not a generic
   tree/search engine.
3. **UI presentations** on top: start with a folder picker; later a tree view
   (SwiftUI `OutlineGroup`/`List(children:)` gives lazy-expandable trees
   natively) and a fuzzy finder — all fed by the same model + API.

The appeal is that the first small slice isn't throwaway: it establishes the API
contract and the core model that the later shapes reuse. Whether that
abstraction earns its keep before we've built more than one presentation is
itself worth questioning.

## A possible first slice: remote folder picker

One concrete way to start (again — *a* starting point, not the plan):

- Add the browse DTOs + client method, with a canned in-memory tree in
  `MockCockpitClient` so the UI is buildable before any server work exists.
- Build the `RemoteFileBrowser` model.
- Build a drill-down `RemoteFolderPicker` sheet: roots list → directory list,
  clickable breadcrumb segments plus an Up button, an editable path field, a
  hidden-files toggle, visible-but-disabled file rows, and a "use this folder"
  action. Long paths should middle-truncate instead of wrapping into multiple
  rows. Standard load/loading/error conventions. Manual path entry remains in
  the first slice: pasted paths are remote paths validated by Cockpit, and the
  action is enabled only when the path lists as a directory within the browsable
  namespace. Manual path validation happens only when the user commits the field
  with Return or a Go button, not on every keystroke.
  V1 starts at the roots list rather than remembering recents; if
  `CreateSessionSheet` already has a `workingDir`, the picker may prefill and
  validate that path.
  If a directory listing is truncated, show the returned entries with a small
  warning row or banner.
  Up navigation stops at the active Remote Workspace Root for ordinary paths, so
  the picker cannot climb above a root via parent navigation. Directory symlinks
  still behave like normal folders when entered.
  Interaction model: single-click or arrow keys set the Highlighted Entry, which
  can be a file or folder. Enter or double-click drills into a highlighted
  folder; Enter or double-click on a highlighted file does nothing in v1. The
  explicit "Use This Folder" action writes the Chosen Folder (the current viewed
  directory), not the Highlighted Entry, back to `workingDir`.
  Use basic SF Symbols only (`folder` and `doc`); do not add symlink-specific
  visual treatment in v1.
- Replace `CreateSessionSheet`'s `.fileImporter` button with this sheet, writing
  the chosen path into `workingDir` (and fix the misleading help text).

Build the server contract first, then the app UI against that contract. The
boundary decisions live mostly in Cockpit (roots, `$HOME` fallback, symlink
behavior, path identity, hidden files, and error codes), so a mock-first UI would
risk inventing a contract the server then fights. Once the server shape exists,
add matching `CockpitKit` DTOs/tests, then `MockCockpitClient` data for previews,
then `RemoteFileBrowser` and `RemoteFolderPicker`.

**Current leaning on scope:** keep the first slice to *just* the folder picker
(defer the tree browser and fuzzy finder), so we learn from a small, shippable
piece before investing in the broader shapes. Not decided.

## Open questions / still to explore

- **Access scope & config:** final config shape.
- **Path validation & safety:** Cockpit has no browsing guard today — how do we
  represent and validate traversal through directory symlinks without treating
  Remote Workspace Roots as a resolved-path sandbox?
- **API shape details:** resolved for v1.
- **Lazy vs. prepared trade-off:** resolved at the endpoint level — `fs/list`
  stays lazy and one-directory-at-a-time; a future fuzzy finder gets a separate
  prepared, bounded path-list endpoint. Exact DTO sharing is still open.
- **Is the reusable-core abstraction worth it now,** or premature before a second
  presentation exists?
- **Folder-only vs. file-aware browsing:** resolved for the API — return both
  files and folders from day one; the first picker UI only enables selecting
  folders.
- **UI form factor for the picker:** resolved for the first slice — use a
  drill-down list; columns and expandable trees are later presentation options.
- **Fuzzy finder mechanics (later):** server-side vs. client-side ranking,
  `.gitignore` awareness, result caps, the search-mode behaviors.
- **Cross-repo coordination:** the server changes live in the separate
  `cockpit` repo; how we stage client + server work together.

## References

- Reference component (web): <https://trees.software/>
- Cockpit server: <https://github.com/jeremytondo/cockpit>
- Related: `docs/poc-plan.md` (documents the current Cockpit API surface and the
  app's layering conventions).
