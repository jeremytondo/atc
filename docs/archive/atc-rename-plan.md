# `atc` Rename Implementation Record

Status: Implemented in source; GitHub repository cutover pending

## Purpose

Adopt `atc` as the complete product and repository identity. This is a clean,
breaking rename for an early-development project. It intentionally provides no
compatibility aliases, settings migration, deprecated Swift names, or legacy
branding.

The source changes are prepared in the existing GitHub repository. Renaming the
repository itself to `atc` remains a separate manual step performed immediately
after this change merges.

## Final Naming

- Product branding: `atc`
- Server description: `atc server`
- Native app description: `atc for macOS`
- Repository and Go module: `github.com/jeremytondo/atc`
- CLI and server binary: `atc`
- Xcode project, scheme, target, and app bundle: `atc`
- Swift app module: `ATC`
- Swift package and API modules: `ATCKit` and `ATCAPI`
- Production bundle identifier: `ElevenIdeas.atc`
- Development bundle identifier: `ElevenIdeas.atc.dev`
- Test bundle identifier: `ElevenIdeas.atcTests`
- Release-script environment variables: `ATC_*`
- Default notary profile: `atc-notary`

## Implemented Scope

- Renamed the Swift package, modules, targets, public types, mocks, decoder
  helpers, tests, source directories, and matching filenames.
- Renamed the macOS Xcode project, scheme, targets, synchronized source
  directories, build products, module, bundle identifiers, test host, logging
  subsystems, and release tooling.
- Updated the Go module, internal imports, GoReleaser linker paths and repository
  metadata, installer URLs, CLI help, server logs, embedded web UI, fixtures,
  and tests.
- Updated active and archived documentation, comments, examples, context files,
  agent instructions, filenames, and internal links.
- Preserved the existing `ATC_*` server environment variables, configuration
  and runtime paths, database names, sockets, API routes, wire contracts, and
  persistence formats. Server state is not reset or migrated.
- Kept the existing `atc` CLI command tree unchanged.

## Compatibility Boundary

- Swift package modules and renamed public symbols have no compatibility
  aliases.
- The Go module uses the breaking `github.com/jeremytondo/atc` path.
- HTTP and WebSocket APIs, CLI subcommands, server configuration, and server
  persistence formats are unchanged.
- The macOS bundle identifier creates a fresh app identity, so saved local
  Connections are intentionally not migrated.
- Existing server data remains valid.

## Verification

Before checkpointing the source rename:

- Run the root `mise run check` gate.
- Run `mise run server:build` and confirm `server/dist/atc` exists.
- Run `swift package describe` and `swift test` from `packages/ATCKit`.
- Run `xcodebuild -list`, build, and tests for `macos/atc.xcodeproj` with the
  `atc` scheme.
- Validate `scripts/release-dev.sh` syntax and help output.
- Run a GoReleaser snapshot and confirm lowercase `atc` artifacts.
- Search tracked paths and content for retired identifiers; completion requires
  zero matches.

## Post-Merge GitHub Cutover

1. Rename the existing repository to `atc` in GitHub settings immediately after
   this source change merges.
2. Do not publish a release between merge and repository rename because the new
   URLs will not resolve during that interval.
3. Update this checkout's remote:

   ```sh
   jj git remote set-url origin https://github.com/jeremytondo/atc.git
   ```

4. Verify repository settings, branch protections, Actions, release
   permissions, raw install URLs, and installer behavior.
5. Do not create another repository at the retired URL because that would
   interfere with GitHub's redirect.

Renaming the local checkout directory is optional because it is not tracked by
the repository.
