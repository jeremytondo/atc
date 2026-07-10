> **Historical (archived 2026-07):** Describes the pre-monorepo Cockpit-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# Tech Design — Remote File Browsing, Part 2: AtelierCode Remote Folder Picker

> Implements the client half of `docs/remote-file-browsing-prd.md` against the
> Cockpit contract defined in `docs/remote-file-browsing-design-1-cockpit.md`
> (Part 1). **Prerequisite:** Part 1 is deployed to the workstation and its
> captured response fixtures are available. Work happens in this repo; jj
> protocol per `AGENTS.md`.

## 1. Scope

Replace `CreateSessionSheet`'s local `.fileImporter` with a remote folder
picker driven by Cockpit's `/api/fs`:

- `CockpitAPI` package: FS DTOs, two `CockpitClient` methods, HTTP
  implementation, decoding tests against captured fixtures.
- `MockCockpitClient`: a canned in-memory remote tree for previews and tests.
- `RemoteFileBrowser`: a narrow `@Observable` picker-workflow model in
  `Features/RemoteFiles/`.
- `RemoteFolderPickerSheet`: the drill-down UI.
- `CreateSessionSheet` integration + copy fix.
- A new app unit-test target for the browser model.

## 2. Contract recap (from Part 1)

| Endpoint | Success shape |
|---|---|
| `GET /api/fs/roots` | `{"roots":[{"label":"Projects","path":"/home/j/Projects"}]}` |
| `GET /api/fs/list?path=<abs>&showHidden=<bool>` | `{"path":"/abs/lexical","truncated":false,"entries":[{"name","path","kind","isSymlink","size?","modifiedAt?"}]}` |

- `kind` ∈ `directory` \| `file` \| `unknown` (broken symlinks, sockets, etc. —
  visible, not enterable). `isSymlink` is metadata only; symlinked dirs behave
  as folders.
- `path` values are lexical (symlink-preserving) — the client's canonical
  navigation state. Entry identity is the path string; no IDs.
- Entries arrive pre-sorted (dirs first, dotfiles first, case-insensitive);
  hidden entries are already filtered server-side per `showHidden`.
- Errors use the existing envelope and surface as
  `CockpitError.api(code:message:sessionID:)` with `apiCode` one of:
  `invalid_path` (400), `outside_browsable_roots` (403), `not_found` (404),
  `not_directory` (400), `permission_denied` (403), `internal_error` (500).
- `modifiedAt` is RFC3339Nano — decodes via the existing
  `JSONDecoder.cockpit()` / `.cockpitRFC3339Nano` strategy unchanged.

## 3. Changes from the PRD (and why)

1. **The mock tree stays in the app's `PreviewSupport/MockCockpitClient.swift`**,
   not the package. The PRD says the package owns "mock data," but the codebase
   already keeps `MockCockpitClient` in the app; moving it is unrelated churn.
   The package owns DTOs, decoding, methods, and the HTTP implementation.
2. **Scripted UI tests are replaced by a manual verification checklist plus a
   model-level test suite** (§9). The app has no test target of any kind today;
   XCUITest for keyboard/double-click list behavior on macOS is brittle and a
   poor first investment. Instead: a new *unit*-test target covers the
   `RemoteFileBrowser` workflow (the PRD's third seam) with the mock client,
   and the picker's visual behaviors are a manual checklist. This trades
   automation of the thinnest layer for a durable seam we keep.
3. **No `parent` field exists in the contract** (Part 1 §2.1) — the app
   computes parents and breadcrumbs lexically from the current path, which it
   needs to do anyway to enforce the active-root Up boundary.
4. **The server is the sole path validator.** On manual path commit the app
   sends the trimmed string to `fs/list` and renders the typed error; it does
   no client-side syntax checking beyond trimming. One validator, no drift.
5. **Unrecognized `kind` values decode as `.unknown`** (custom `Decodable`
   init), so a future server adding kinds doesn't break old clients. Unknown
   entries are already inert in the UI, making this safe forward compatibility.

Everything else follows the PRD as written.

## 4. `CockpitAPI` package changes

New file `Sources/CockpitAPI/Models/FileSystemModels.swift`:

```swift
public struct RemoteWorkspaceRoot: Codable, Sendable, Hashable, Identifiable {
    public var id: String { path }        // path-as-identity, like everywhere else
    public let label: String
    public let path: String
}

public enum RemoteEntryKind: String, Codable, Sendable {
    case directory, file, unknown
    // custom init(from:) — unrecognized raw values decode as .unknown
}

public struct RemoteEntry: Codable, Sendable, Hashable, Identifiable {
    public var id: String { path }
    public let name: String
    public let path: String
    public let kind: RemoteEntryKind
    public let isSymlink: Bool
    public let size: Int64?
    public let modifiedAt: Date?
}

public struct DirectoryListing: Codable, Sendable, Hashable {
    public let path: String
    public let truncated: Bool
    public let entries: [RemoteEntry]
}

struct RootsEnvelope: Decodable { let roots: [RemoteWorkspaceRoot] }  // internal, like SessionsEnvelope
```

`CockpitClient` protocol additions (both implementors must conform):

```swift
func workspaceRoots() async throws -> [RemoteWorkspaceRoot]
func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing
```

Plus a protocol-extension default `listDirectory(path:)` with
`showHidden: false`, mirroring the `sessions()` default.

`HTTPCockpitClient`:

- `workspaceRoots()` → `get(RootsEnvelope.self, "fs/roots").roots`.
- `listDirectory` → `get(DirectoryListing.self, "fs/list", query: items)` where
  `items` always contains `path` and appends `showHidden=true` **only when
  true** — the exact `includeArchived` precedent.

No decoder changes: fields are camelCase (no key strategy needed) and
`modifiedAt` rides the existing date strategy. Server errors flow through the
existing non-2xx → `ErrorEnvelope` → `CockpitError.api` path untouched.

## 5. Mock data — `MockCockpitClient`

Extend the existing `nonisolated struct` with a canned tree. A programmatic
dictionary beats inline JSON here because the mock must *navigate*, not just
decode:

```swift
// roots: [RemoteWorkspaceRoot], tree: [String: [RemoteEntry]] keyed by directory path
```

Content requirements (so previews exercise every UI state):

- Two roots (`Projects`, `Home`); 3-4 levels of nesting.
- Files and folders mixed; several dotfiles/dot-dirs (hidden-toggle demo);
  a directory symlink, a file symlink, and a broken symlink (`.unknown`);
  one directory whose listing throws `CockpitError.api(code:
  "permission_denied", ...)`; one listing with `truncated: true`; one empty
  directory.
- `listDirectory` filters dot-entries itself when `showHidden` is false and
  throws `.api(code: "not_found", ...)` for unknown paths — the mock mimics
  server behavior so `RemoteFileBrowser` tests cover error handling.

## 6. `RemoteFileBrowser` — the `@Observable` picker model

New file `AtelierCode/Features/RemoteFiles/RemoteFileBrowser.swift`. Follows
the `SessionsStore` template: `@Observable final class` (main-actor by project
default), knows nothing about views, takes `any CockpitClient` in `init`
(injected from `AppModel.client` by the presenting view; no `AppModel`
dependency, which keeps the model trivially testable).

Not a generic tree engine — exactly the picker's workflow state:

```swift
@Observable final class RemoteFileBrowser {
    // State (private(set) unless bound by the UI)
    private(set) var roots: [RemoteWorkspaceRoot] = []
    private(set) var activeRoot: RemoteWorkspaceRoot?
    private(set) var listing: DirectoryListing?      // nil ⇒ showing the roots list
    private(set) var isLoading = false
    private(set) var hasLoadedRoots = false
    var lastError: String?
    var highlightedPath: String?                     // Highlighted Entry (List selection)
    var typedPath: String = ""                       // path-field draft; synced on navigation
    var showHidden = false                           // didSet → reload current directory

    // Commands
    func loadRoots() async
    func open(root: RemoteWorkspaceRoot) async
    func descend(into entry: RemoteEntry) async      // no-op unless kind == .directory
    func goUp() async
    func commitTypedPath() async                     // Return / Go
    func jump(to path: String) async                 // breadcrumb segment tap

    // Derived
    var currentPath: String? { listing?.path }       // the Chosen Folder candidate
    var canGoUp: Bool                                // false at activeRoot or roots list
    var breadcrumbs: [(label: String, path: String)] // root label + lexical segments below it
}
```

Semantics:

- **Loading**: every command sets `isLoading`/clears-or-sets `lastError` with
  the `SessionsStore` `defer` pattern; failures store
  `error.localizedDescription` (which is the server's human message for
  `.api`). A failed navigation keeps the previous listing on screen with the
  error shown — no blanking.
- **Active root**: set directly by `open(root:)`. After `commitTypedPath` /
  prefill, it is the root whose path is the **longest lexical prefix** of the
  listed path (ties impossible after Part 1's duplicate-path config rule). The
  server already guaranteed containment, so a match always exists.
- **Up boundary**: `canGoUp` is false when `currentPath == activeRoot.path`.
  `goUp()` lists the lexical parent (string operation on `currentPath`).
  Going "up" from a root returns to the roots list (`listing = nil`,
  `activeRoot = nil`) — cheap, and gives the roots list a way back.
- **Breadcrumbs**: `activeRoot.label` followed by the path segments of
  `currentPath` relative to `activeRoot.path` — lexical, symlink-preserving by
  construction. `jump(to:)` re-lists any crumb.
- **Typed path**: `typedPath` mirrors `currentPath` after every successful
  navigation. `commitTypedPath()` trims, then calls `listDirectory` — the
  server validates (§3.4). Success replaces the listing and recomputes
  `activeRoot`; failure sets `lastError` and leaves navigation state alone.
- **Hidden toggle**: `showHidden` change re-lists `currentPath` (server-side
  filter; entries are not cached client-side). Highlight is cleared if the
  highlighted entry disappears.
- **Descend** ignores `.file` and `.unknown` entries (PRD: inert in v1).
- Navigation clears `highlightedPath`.

## 7. `RemoteFolderPickerSheet` — the UI

New file `Features/RemoteFiles/RemoteFolderPickerSheet.swift`. Presented as a
nested sheet from `CreateSessionSheet`; fixed frame (~`520×480`); standard
sheet chrome (bottom `Divider` + `HStack`: Cancel with `.cancelAction`,
primary with `.defaultAction`); `.preferredColorScheme(.dark)` in previews.

```
┌──────────────────────────────────────────────┐
│ [path field ............................][Go]│  ← typedPath; commit on Return/Go only
│ [↑] Projects ▸ atelier ▸ src                 │  ← Up button + clickable breadcrumbs
├──────────────────────────────────────────────┤
│ ⚠ Listing truncated at 10,000 entries        │  ← only when listing.truncated
│ folder  .config                              │
│ folder  src                                  │  ← List(selection:), rows = entries
│ doc     README.md            (dimmed)        │
│ …                                            │
├──────────────────────────────────────────────┤
│ ☐ Show hidden files      Cancel  Use This Folder │
└──────────────────────────────────────────────┘
```

- **Two visual modes on one sheet**: `browser.listing == nil` shows the roots
  list (rows: `folder.badge.gearshape`-free, just `folder` + label + dimmed
  path); otherwise the directory list. Empty roots →
  `ContentUnavailableView("No browsable roots", …)` explaining that roots are
  configured on the Cockpit server. Loading → `ProgressView`; errors → the
  existing inline red `Label` convention (persistent field-adjacent text for
  path-commit errors, not an alert).
- **List + Highlighted Entry**: `List(selection: $browser.highlightedPath)`
  with rows `.tag(entry.path)` — single-click and arrow keys (when the list
  has focus) come free from AppKit-backed `List`. This is the app's first
  keyboard-navigable list; no precedent exists, and `List` selection is the
  lowest-machinery way to get PRD stories 9-10.
- **Drill-in**: Return via `.onKeyPress(.return)` scoped to the list (macOS
  14+): if the highlighted entry is a `.directory`, `descend`; `.file` /
  `.unknown` → consume and do nothing. Double-click via
  `TapGesture(count: 2)` on the row content (no first-party List double-click
  API; this is the standard approach). Rows for roots behave the same
  (Enter/double-click opens the root).
- **Rows**: SF Symbols only — `folder` for `.directory`, `doc` for `.file`,
  `questionmark.square.dashed` (or `doc` dimmed) for `.unknown`. No
  symlink-specific treatment (PRD). `.file`/`.unknown` rows render with
  `.foregroundStyle(.secondary)` — visible, clearly not choosable, still
  highlightable.
- **Paths**: path field and breadcrumb overflow use `.lineLimit(1)` +
  `.truncationMode(.middle)` (deliberate PRD deviation from the app's existing
  `.head` truncation for `workingDir` display — middle keeps both the root and
  the leaf visible).
- **Truncation warning**: when `listing.truncated`, a compact warning row
  pinned above the list (`exclamationmark.triangle`, `.secondary`).
- **Use This Folder**: enabled iff `browser.currentPath != nil`; commits the
  **Chosen Folder** = current viewed directory (never the Highlighted Entry),
  calls `onChoose(path)` and dismisses. Cancel dismisses with no write-back.

Component boundary: keep the sheet one file with small private subviews
(`rootsList`, `directoryList`, `breadcrumbBar`, `entryRow`) — same style as
`CreateSessionSheet`. No new abstractions until a second presentation (tree /
fuzzy finder) exists.

## 8. `CreateSessionSheet` integration

- Delete the `.fileImporter(...)` modifier, the
  `"Choose a local folder (path is used on the server)"` `.help` string, and
  the now-unused `import UniformTypeIdentifiers`.
- The `folder` button now sets `showFolderPicker = true` to present
  `RemoteFolderPickerSheet` via `.sheet(isPresented:)`, constructing
  `RemoteFileBrowser(client: appModel.client)`. New `.help`:
  `"Browse folders on the Cockpit workstation"`.
- `onChoose:` writes the chosen path into the existing `workingDir` `@State`.
  The `TextField` prompt stays `/path/on/the/server`.
- **Prefill** (PRD "may"): on picker appear, if trimmed `workingDir` is
  non-empty → `typedPath = workingDir; await commitTypedPath()`; on failure the
  picker shows the roots list with the error visible in the path-field area.
  Empty `workingDir` → `loadRoots()` directly.

## 9. Testing

**Seam 1 — package (`CockpitAPITests`, Swift Testing, existing target).**
Fixtures are verbatim captures from the live Part 1 server (raw-string `Data`,
capture-date comment — house convention):
- Decode `fs/roots` fixture via `RootsEnvelope`; decode `fs/list` fixture
  (entries incl. nullable `size`/`modifiedAt`, a symlink, an `unknown`,
  `truncated` both ways, empty `entries`); unrecognized `kind` → `.unknown`.
- `CockpitServerTests`-style URL tests: `fs/list` query contains `path`;
  `showHidden` present only when true; path percent-encoding.
- `ErrorEnvelopeTests`-style: each of the five FS error bodies decodes and
  surfaces the right `apiCode`/`errorDescription`.

**Seam 2 — new app unit-test target `AtelierCodeTests`** (Swift Testing; first
app test target — add to the pbxproj). Tests drive `RemoteFileBrowser` with
`MockCockpitClient`:
- loadRoots populates roots; open(root:) lists it and sets activeRoot.
- descend into directory updates listing/typedPath and clears highlight;
  descend into file/unknown is a no-op.
- goUp stops at activeRoot (`canGoUp == false`), then returns to roots list.
- breadcrumbs reflect the lexical path incl. through the mock's symlinked dir.
- commitTypedPath: valid path lists and recomputes activeRoot (longest-prefix);
  error path sets `lastError` and preserves the current listing.
- showHidden toggle re-lists; truncated listing exposes `truncated`.
- permission_denied listing surfaces the server message in `lastError`.
- CreateSession seam: choosing a folder writes `workingDir` (test the
  `onChoose` wiring at the model/callback level).

**Manual checklist (replaces scripted UI tests — §3.2)** against the live
workstation: open picker → roots appear; arrow-key + Enter drill-in;
double-click drill-in; Enter/double-click inert on files; breadcrumb jump; Up
stops at root then shows roots list; hidden toggle reveals dotfiles; paste a
valid path + Return opens it; paste each invalid-path class and see the typed
error; truncation banner on a huge dir; symlinked dir browses with preserved
breadcrumbs; **Use This Folder → session actually starts in that directory on
the workstation** (end-to-end, per PRD).

## 10. Milestones

1. **Fixtures + DTOs**: capture live responses; `FileSystemModels.swift`,
   protocol methods, `HTTPCockpitClient` impl; Seam-1 tests green
   (`swift test --package-path Packages/CockpitKit`).
2. **Mock tree**: `MockCockpitClient` conformance + canned tree (§5).
3. **Model + test target**: `RemoteFileBrowser`; create `AtelierCodeTests`;
   Seam-2 tests green.
4. **UI**: `RemoteFolderPickerSheet` with previews for roots/list/error/empty/
   truncated states.
5. **Integration**: `CreateSessionSheet` swap, prefill, copy fix.
6. **Verify**: full manual checklist against the live server.

Acceptance: both automated seams green; manual checklist passes end-to-end;
`.fileImporter` and the misleading help text are gone.
