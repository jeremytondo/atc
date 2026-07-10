# `atc` Rename Implementation Plan

Status: Planned

## Purpose

Rename the entire product and repository from Atelier Code to `atc`. This is a
clean, breaking rename for an early-development project: no compatibility
aliases, settings migration, deprecated Swift names, or legacy product branding
will be retained.

The code changes will be prepared and merged in the existing GitHub repository.
The repository itself will then be renamed from `atelier-code` to `atc` on
GitHub as a separate manual cutover step.

## Naming Rules

- Human-facing product branding is always lowercase `atc`.
- Commands, paths, repository names, release artifacts, and configuration use
  lowercase `atc`.
- Swift modules and types use conventional uppercase identifiers such as
  `ATC`, `ATCAPI`, and `ATCClient`.
- The server is described as `atc server`; the native app is described as
  `atc for macOS`.

| Current name | Replacement |
| --- | --- |
| Atelier Code | `atc` |
| Atelier Code Server | `atc server` |
| Atelier Code for macOS | `atc for macOS` |
| `atelier-code` repository | `atc` |
| `github.com/jeremytondo/atelier-code` | `github.com/jeremytondo/atc` |
| `AtelierCode.xcodeproj`, scheme, and target | `atc.xcodeproj`, `atc` |
| `AtelierCode.app` | `atc.app` |
| Swift app module | `ATC` |
| `AtelierCodeKit` | `ATCKit` |
| `AtelierCodeAPI` | `ATCAPI` |
| `AtelierCodeClient` | `ATCClient` |
| `HTTPAtelierCodeClient` | `HTTPATCClient` |
| `AtelierCodeServer` | `ATCServer` |
| `AtelierCodeError` | `ATCError` |
| `AtelierCodeAction` | `ATCAction` |
| `MockAtelierCodeClient` | `MockATCClient` |
| Production bundle identifier | `ElevenIdeas.atc` |
| Development bundle identifier | `ElevenIdeas.atc.dev` |
| Test bundle identifier | `ElevenIdeas.atcTests` |

## Implementation Sequence

### 1. Rename the Swift package and API

- Rename `packages/AtelierCodeKit` to `packages/ATCKit`.
- Rename the package, library product, targets, test targets, source folders,
  test folders, and matching filenames to `ATCKit`, `ATCAPI`, and
  `ATCAPITests`.
- Rename all public `AtelierCode*` types to their `ATC*` equivalents.
- Rename helper APIs such as `JSONDecoder.atelierCode()` to
  `JSONDecoder.atc()`.
- Update imports, mocks, tests, documentation comments, task-runner paths, and
  Xcode local-package references.
- Do not add deprecated type aliases for the old names.

### 2. Rename the macOS application

- Rename the Xcode project, shared scheme, application target, test target,
  synchronized source directories, build products, and test-host references.
- Produce lowercase `atc.app` while explicitly using `ATC` as the Swift module
  name; tests should use `@testable import ATC`.
- Rename `AtelierCodeApp` to `ATCApp` and rename other files whose names contain
  the retired product identity.
- Change the production, development, and test bundle identifiers to the values
  in the naming table.
- Change logging subsystems to `ElevenIdeas.atc`.
- Treat the new bundle identifier as a fresh application identity. Existing
  UserDefaults, including saved Connections, are intentionally not migrated.
- Update the development-release script for the new project, scheme, product,
  archive, DMG, and release-title names.
- Rename release-script environment variables from `AC_*` to `ATC_*` and use
  `atc-notary` as the default notary profile name.

### 3. Finish the server and repository rename

- Change the Go module and all Go imports to
  `github.com/jeremytondo/atc`.
- Update GoReleaser linker paths, repository metadata, release configuration,
  install URLs, and release notes.
- Change server, CLI, web UI, logging, help text, examples, development tools,
  fixtures, and tests to lowercase `atc` branding.
- Keep the existing `atc` CLI command tree unchanged.
- Keep existing `ATC_*` server environment variables, `~/.config/atc`, database
  names, sockets, runtime paths, API routes, JSON contracts, and persistence
  formats unchanged because they already use the final identity.
- Do not reset or migrate server state.

### 4. Remove all legacy branding

- Update active and archived documentation, comments, examples, fixtures,
  agent instructions, context files, and user-facing strings.
- Rename filenames containing `atelier-code` or the retired pre-monorepo
  product name, including the monorepo brief and affected ADR/design-document
  names.
- Repair every internal Markdown link and source reference affected by renamed
  files or directories.
- Require tracked-file searches to find no remaining `Atelier Code`,
  `AtelierCode`, `atelier-code`, or retired pre-monorepo product identifiers.
- Preserve unrelated sample names such as `Atelier` only when they do not refer
  to this product.

### 5. Deliver the code rename atomically

- Keep the dependent Swift, Xcode, server, release, and documentation changes
  in one pull request so no merged revision contains mismatched module or
  project references.
- Treat the complete rename as one logical source-control change. Once the full
  change passes its tests, follow the repository's `jj` checkpoint protocol.
- Push and merge the rename through the existing `atelier-code` GitHub
  repository.
- Do not publish a release between merging this change and completing the
  GitHub repository rename because the prepared `/atc` URLs will not resolve
  during that short interval.

### 6. Rename the GitHub repository

- Immediately after the code rename merges, rename the existing GitHub
  repository from `atelier-code` to `atc` in repository settings.
- Update the local remote to the new URL:

  ```sh
  jj git remote set-url origin https://github.com/jeremytondo/atc.git
  ```

- Rename the local checkout directory separately if desired; the local folder
  name is not tracked by the repository.
- Do not create another repository using the old `atelier-code` name because it
  would interfere with GitHub's redirect from the old repository URL.
- Verify repository settings, branch protections, Actions, release permissions,
  and raw install URLs after the rename.

GitHub's repository-rename behavior and redirect limitations are documented in
[Renaming a repository](https://docs.github.com/en/repositories/creating-and-managing-repositories/renaming-a-repository).

## Compatibility Boundary

- Swift package modules and all `AtelierCode*` symbols receive breaking names
  with no aliases.
- The Go module receives the breaking path `github.com/jeremytondo/atc`.
- HTTP and WebSocket APIs, CLI subcommands, server configuration, environment
  variables, and server persistence formats do not change.
- The macOS application starts with empty local settings because its bundle
  identifier changes.
- Existing server data remains valid and is not removed.

## Verification

### Automated checks

- Run the root `mise run check` gate to cover Go formatting, vetting and tests,
  web checks, Swift package tests, and macOS tests.
- Run `mise run server:build` and confirm the executable is produced at
  `server/dist/atc`.
- Run `swift package describe` and `swift test` from `packages/ATCKit`.
- Run `xcodebuild -list`, build, and tests using `macos/atc.xcodeproj` and the
  `atc` scheme.
- Validate the development-release script's shell syntax and help output.
- Run a GoReleaser snapshot or test release to verify the renamed module linker
  paths and `atc` artifacts.
- Search all tracked files for retired identifiers. Completion requires zero
  matches.

### Manual checks

- Launch the macOS app and confirm the application bundle, process, menu bar,
  Settings window, and visible product wording use lowercase `atc`.
- Confirm the development release targets `atc.app`, uses
  `ElevenIdeas.atc.dev`, and produces lowercase `atc` artifact names.
- Confirm the server CLI help, embedded web UI, install instructions, and logs
  use lowercase `atc`.
- After the GitHub rename, fetch through the updated remote, confirm CI passes,
  run a test server release, and verify the installer resolves from
  `github.com/jeremytondo/atc`.

## Definition of Done

- All naming changes in this document are implemented consistently.
- The Swift package, macOS app, server, web UI, tests, release tooling, and
  documentation build and pass their applicable checks.
- The repository contains no retired product branding or identifiers.
- The GitHub repository is named `jeremytondo/atc` and local remotes point to
  it.
- The installer and release workflows resolve through the new repository name.
- No compatibility aliases or local-settings migration were introduced.

## Confirmed Decisions

- Product branding is lowercase `atc` everywhere.
- Swift modules and type identifiers use conventional uppercase `ATC`.
- The rename includes implementation symbols, not only visible branding.
- The bundle identifier is `ElevenIdeas.atc`.
- Existing macOS Connections may be discarded.
- Active and archived documentation must contain no legacy branding.
- Code will be prepared for `github.com/jeremytondo/atc` before the GitHub
  repository is renamed.
- The existing GitHub repository will be renamed after the code change merges.
