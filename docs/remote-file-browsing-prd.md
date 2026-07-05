# PRD: Remote Folder Selection

## Problem Statement

When a user starts a new AtelierCode session today, the create-session flow offers a local Mac folder picker and copies that path into `workingDir`. That is semantically wrong because `workingDir` is evaluated on the remote Cockpit workstation, not on the local Mac. Users either hand-type remote paths from memory or risk starting sessions in invalid or unintended directories.

Cockpit also has no read-only filesystem API. AtelierCode cannot list Remote Workspace Roots, validate a pasted remote path, or browse the remote workstation before creating a session. This blocks a correct folder-selection experience and prevents future remote file-tree and fuzzy-finder workflows.

## Solution

Add remote folder selection for session creation. Cockpit will expose a small read-only `/api/fs` browse contract, rooted in static Remote Workspace Roots. AtelierCode will replace the local folder importer with a drill-down remote folder picker that lists roots, browses one directory at a time, supports hidden entries, validates manually entered remote paths on commit, and writes the Chosen Folder back to `workingDir`.

The first version focuses only on choosing a remote working directory. It establishes the Cockpit filesystem contract and a narrow app-side browsing model that later remote file-tree and fuzzy-finder presentations can reuse.

## User Stories

1. As an AtelierCode user, I want to choose a folder on the remote Cockpit workstation, so that my new session starts in the correct remote working directory
2. As an AtelierCode user, I want the create-session folder button to browse remote folders instead of local Mac folders, so that I do not accidentally choose an unusable local path
3. As an AtelierCode user, I want to see configured Remote Workspace Roots when opening the picker, so that I know where remote browsing is expected to start
4. As an AtelierCode user, I want Cockpit to provide a Home root when no roots are configured, so that remote folder selection works out of the box
5. As an AtelierCode user, I want invalid or unreadable configured roots to be absent from the picker, so that I only interact with usable roots
6. As a Cockpit operator, I want invalid root configuration to be logged server-side, so that I can troubleshoot workstation configuration drift
7. As an AtelierCode user, I want to drill into remote folders with the keyboard, so that I can choose a working directory without using the mouse
8. As an AtelierCode user, I want to drill into remote folders by double-clicking, so that the picker behaves like a familiar file browser
9. As an AtelierCode user, I want single-click and arrow-key navigation to set the Highlighted Entry, so that I can inspect rows before drilling in
10. As an AtelierCode user, I want Enter on a highlighted folder to drill in, so that keyboard navigation is efficient
11. As an AtelierCode user, I want Enter or double-click on a highlighted file to do nothing in v1, so that files can be visible without implying file opening exists yet
12. As an AtelierCode user, I want Use This Folder to choose the current viewed directory, so that confirming a folder is explicit and not confused with the Highlighted Entry
13. As an AtelierCode user, I want files to be visible in directory listings, so that the browser reflects the actual contents of a remote directory
14. As an AtelierCode user, I want only directories to be usable as the Chosen Folder, so that session creation receives a valid working directory
15. As an AtelierCode user, I want hidden entries excluded by default, so that common directories remain easy to scan
16. As an AtelierCode user, I want a hidden-files toggle, so that I can navigate to developer directories such as dot-config or dot-repo folders when needed
17. As an AtelierCode user, I want an editable remote path field, so that I can paste or type a known workstation path
18. As an AtelierCode user, I want manual path validation to happen only when I press Return or click Go, so that the app does not show noisy errors while I am still typing
19. As an AtelierCode user, I want valid pasted remote directory paths to open in the picker, so that I can combine terminal-copy workflows with browsing
20. As an AtelierCode user, I want invalid pasted paths to show a useful error, so that I understand whether the path was missing, not a directory, unreadable, outside the browsable roots, or malformed
21. As an AtelierCode user, I want the picker to preserve the path I followed through symlinks, so that breadcrumbs match my navigation rather than jumping to resolved targets
22. As an AtelierCode user, I want directory symlinks to behave like folders, so that common linked workspace layouts work naturally
23. As an AtelierCode user, I want file symlinks to behave like files, so that the listing stays consistent with the effective filesystem entry
24. As an AtelierCode user, I want broken symlinks to remain visible but not enterable, so that filesystem issues are not silently hidden
25. As an AtelierCode user, I want unreadable directories under readable parents to remain visible, so that the listing reflects that the directory exists even if opening it fails
26. As an AtelierCode user, I want parent navigation to stop at the active Remote Workspace Root, so that the picker does not become a full-host browser by climbing above a root
27. As an AtelierCode user, I want clickable breadcrumb segments, so that I can jump back multiple levels quickly
28. As an AtelierCode user, I want an Up button, so that parent navigation is always available
29. As an AtelierCode user, I want long paths to middle-truncate, so that the picker stays readable without wrapping path text across multiple rows
30. As an AtelierCode user, I want very large directories to show a truncation warning, so that I know the listing is incomplete
31. As an AtelierCode user, I want the picker to use basic system folder and file icons, so that it is visually clear without adding icon complexity
32. As an AtelierCode user, I want the final chosen remote path to populate the session working directory field, so that session creation uses the path I confirmed
33. As an AtelierCode user, I want the old misleading local-folder help text removed, so that the UI no longer suggests that local folders are valid session working directories
34. As a Cockpit API consumer, I want stable path-string identity for filesystem entries, so that client state can use paths directly without separate IDs
35. As a Cockpit API consumer, I want deterministic default sorting, so that simple clients and tests get stable directory output
36. As a Cockpit API consumer, I want clients to remain free to re-sort entries, so that future UIs can sort by their own criteria
37. As a Cockpit API consumer, I want directory listing to return files and folders, so that future file-viewing features can build on the same browse contract
38. As a Cockpit API consumer, I want file content reading to be separate from directory listing, so that `fs/list` remains simple and predictable
39. As a Cockpit maintainer, I want slow listings to respect request cancellation and server-side timeouts, so that abandoned picker requests do not consume resources indefinitely
40. As a future AtelierCode user, I want the first browse contract to leave room for a file tree and fuzzy finder, so that v1 folder selection is not throwaway work

## Implementation Decisions

- Remote folder selection is v1 of remote file browsing. File-tree browsing, fuzzy finding, and file viewing are future presentations or endpoints.
- Cockpit is the source of truth for remote filesystem data. Server contract work happens before the Swift UI is built against it.
- Remote browsing starts from static Remote Workspace Roots configured on the Cockpit server.
- When no Remote Workspace Roots are configured, Cockpit synthesizes a `Home` root at the server user's home directory.
- Explicitly configured roots replace the synthesized Home root.
- Configured roots that are invalid or unreadable are omitted from the roots response and logged server-side.
- Remote Workspace Root paths may use `~` for the server user's home directory. Arbitrary environment-variable expansion is out of scope for v1.
- Remote Workspace Roots are a browsing namespace, not a resolved-path sandbox. Directory symlinks reached from a root remain browseable, including their children, even when they resolve outside the root.
- Remote Workspace Roots constrain remote browsing and AtelierCode-driven working-directory selection only. They do not globally restrict session-start requests in v1.
- The Cockpit browse API lives under `/api/fs`.
- The roots endpoint returns a named wrapper containing usable roots with `label` and `path`.
- The list endpoint lists immediate children for a directory path and accepts a `showHidden` flag.
- The list endpoint is directory-only. If the requested path is a file, it returns `not_directory`.
- File reading or previewing will use a future endpoint, not the directory-list endpoint.
- Entry identity is the path string. Do not introduce entry IDs in v1.
- The path shown during navigation preserves the path the user followed, including through symlinked directories, rather than rewriting breadcrumbs to resolved targets.
- Directory entries include required `name`, `path`, `kind`, and `isSymlink` fields.
- Directory entries may include nullable `size` and `modifiedAt` fields.
- Deferred entry metadata includes permissions, owner/group, MIME type, Git status, children count, and resolved symlink target.
- Entry `kind` supports `directory`, `file`, and `unknown`.
- Broken symlinks are visible as `unknown` entries with `isSymlink` set and are not enterable.
- Unreadable directories discovered under a readable parent remain visible as directory entries; entering them returns `permission_denied`.
- If the requested directory itself is unreadable, the list endpoint returns `permission_denied`.
- Cockpit cleans and normalizes requested paths before validation. Raw `..` segments are allowed only when the normalized result remains in the browsable namespace.
- Cockpit returns a stable default order: directories first, then files; dot-prefixed entries before others within each group; then case-insensitive by name.
- Clients may re-sort entries for their own UI.
- The list endpoint applies a server-side cap of 10,000 entries and returns `truncated` when the result is cut off.
- The v1 list errors are `not_found`, `not_directory`, `permission_denied`, `outside_browsable_roots`, and `invalid_path`, using Cockpit's existing error envelope.
- Cockpit respects request-context cancellation and applies a server-side timeout for slow listings.
- The Cockpit API package owns DTOs, decoding, request methods, mock data, and HTTP implementation for the remote filesystem contract.
- The app owns a narrow observable remote-file browsing model for picker workflow state: current listing, loading/error, highlighted path, typed path, hidden toggle, root loading, opening roots, descending, going up, and setting a manual path.
- The browsing model is not a generic tree engine, not an API DTO, and not a SwiftUI view.
- The first UI is a drill-down sheet: roots list, directory list, breadcrumbs, Up button, path field, hidden-files toggle, rows for files and folders, and a Use This Folder action.
- The picker starts at the roots list in v1 and does not remember recents.
- If the create-session flow already has a working-directory value, the picker may prefill and validate it.
- Manual path validation happens only on field commit via Return or Go.
- Up navigation stops at the active Remote Workspace Root for ordinary paths.
- The Highlighted Entry is the file or folder row targeted by pointer or keyboard navigation.
- The Chosen Folder is the current viewed directory committed by Use This Folder.
- Single-click or arrow keys set the Highlighted Entry.
- Enter or double-click on a highlighted folder drills into that folder.
- Enter or double-click on a highlighted file does nothing in v1.
- Use This Folder writes the Chosen Folder, not the Highlighted Entry, back to the session working-directory field.
- The UI uses basic SF Symbols for folders and files and does not add symlink-specific visual treatment in v1.
- Long paths middle-truncate rather than wrap.
- Truncated listings show a small warning row or banner.
- The create-session flow replaces its local folder importer with the remote folder picker and fixes misleading local-folder copy.
- A future fuzzy finder should use a separate prepared, bounded path-list endpoint. The directory-list endpoint remains lazy and one-directory-at-a-time.

## Testing Decisions

- Good tests should cover externally visible behavior at the highest seam available. Avoid tests that assert private view state, internal helper structure, or implementation-only method calls.
- The first test seam is the Cockpit filesystem API contract. Server tests should exercise roots fallback, configured roots, invalid roots being omitted, path normalization, root boundary enforcement, symlink behavior, hidden filtering, sorting, truncation, unreadable directories, broken symlinks, typed errors, request cancellation, and timeout behavior.
- The second test seam is the Cockpit API package. Package tests should decode roots and directory listings, encode query parameters correctly, surface Cockpit error envelopes, and verify the remote filesystem methods behave like the existing sessions/actions/environments methods.
- The third test seam is the app's create-session workflow with an injected mock Cockpit client. Tests should verify that picking a Chosen Folder updates the session working-directory value and that the local folder importer behavior is gone.
- UI tests should focus on user-visible picker behavior: opening roots, drilling into folders with Enter and double-click, leaving files inert on Enter/double-click, using breadcrumbs and Up, toggling hidden files, committing manual paths, showing truncation warnings, and confirming the current viewed directory.
- Existing package tests for session/action/environment decoding are prior art for DTO decoding and error-envelope coverage.
- Existing app preview and mock-client patterns are prior art for building the picker against canned remote filesystem data.
- End-to-end manual verification should run against a live Cockpit server and confirm that a new session starts in the remote path chosen by the picker.

## Out of Scope

- File content reading, previewing, editing, upload, download, create, rename, move, delete, or any other filesystem mutation.
- A full remote file-tree browser.
- A fuzzy finder or prepared path-list endpoint implementation.
- Runtime management of Remote Workspace Roots from AtelierCode.
- Persisted recents or favorites.
- Global enforcement of Remote Workspace Roots on session-start requests.
- Resolved-path sandboxing for symlinks.
- Rich file metadata, Git status, custom file-type icons, and symlink-specific UI treatment.
- Server-side fuzzy ranking or `.gitignore`-aware indexing.
- App-managed Cockpit server configuration editing.

## Further Notes

- This PRD follows the domain language in the glossary: Remote Workspace Root, Highlighted Entry, and Chosen Folder.
- The Remote Workspace Root boundary is recorded as an ADR and should be treated as a durable design decision for v1.
- The design borrows the path-first and prepared-input split from the Pierre Trees reference: directory browsing stays lazy, while future fuzzy finding can use a separate prepared path-list endpoint.
- The work spans the Cockpit server and the AtelierCode client. The server contract should land first so the client and mocks implement a real contract rather than inventing one.
