# Legacy product snapshot

This repository snapshot preserves the ATC product that preceded
[ATC-243](https://linear.app/elevenideas/issue/ATC-243/new-atc-server-definition).
That issue superseded the end-to-end TypeScript App Server and native macOS
client direction with a standalone Go server. The archived code is reference
material, not an implementation constraint for the new server.

The durable source reference is `legacy-product-2026-08`. It preserves the
whole repository because the macOS app is not independently buildable from
`macos/` alone.

## Snapshot boundary

The native app depends on all of the following source and build inputs:

- `macos/` — the Xcode project, SwiftUI app, tests, and configuration
- `packages/ATCKit/` — shared UI, generated API client, and transports
- `packages/attach-protocol/` — fixtures shared with the App Server
- `app-server/openapi.json` — the generated contract consumed by ATCKit
- `scripts/` — OpenAPI preparation plus developer and release builds
- `mise.toml`, `.swift-format`, `.swiftlint.yml`, and `.xcodebuildmcp/` —
  development tooling and checks
- `.github/workflows/macos-ci.yml` and
  `.github/workflows/product-release.yml` — the proven CI and signed release
  recipes

The tag also preserves the TypeScript App Server that the client expects and
the experiments that explain how the product evolved. Do not extract only the
`macos/` directory when using this work as the basis for another project.

## Known-good environment

The final merged pre-pivot source was tested on August 24, 2026, with:

- macOS 26.5.2 (`25F84`)
- the GitHub Actions `macos-26-arm64` image `20260728.0273.1`
- Xcode 26.6 (`17F113`)
- Swift tools 6.2 and macOS/iOS 26 package targets
- Bun 1.3.14 for the App Server
- SwiftLint 0.65.0

SwiftPM dependencies are captured by the two committed `Package.resolved`
files. Bun dependencies are captured by the committed lockfiles. The generated
OpenAPI input is committed and must stay paired with this snapshot.

The canonical development commands are:

```sh
mise install
mise run app-server:check
mise run swift:fmt:check
mise run kit:test
mise run macos:test
mise run macos:build
```

`mise run check` runs every standing legacy gate, including the App Server,
terminal client prototype, Swift packages, macOS app, and release tooling.
The macOS commands require a compatible Mac and Xcode installation. Developer
builds and tests disable signing.

## Verification evidence

The final merged legacy source is commit
`e2839ae9c9f29c0d2f40ee6e7807bc42f067e65f`. Its
[macOS CI run](https://github.com/jeremytondo/atc/actions/runs/32747682910)
passed on August 24, 2026. The run covered SwiftLint, swift-format, ATCKit
tests, and 342 macOS app tests, with compiler warnings promoted to errors.

This archival documentation was assembled in a Linux workspace, so the tagged
documentation commit was not rebuilt locally with Xcode. It changes no app,
package, contract, or build source relative to the successful CI commit above.
The linked CI run is the macOS build evidence for the snapshot.

The latest preserved signed and notarized app is the
[v0.0.4 release](https://github.com/jeremytondo/atc/releases/tag/v0.0.4),
published August 14, 2026. Its Apple Silicon disk image is
`atc-macos-arm64.dmg` with SHA-256
`1e486b99a53d1ed80f78aa9da3cd3ab8b90c07d816ff983f1b1d555a63ad4bc2`.
That release predates the final tested source and is preserved as the last
installable historical artifact, not as a binary built from the archive tag.

## Signed release requirements

The non-publishing developer build is `scripts/build-macos.sh`. The complete
signed, notarized, stapled DMG procedure is encoded in
`scripts/release-macos.sh` and `.github/workflows/product-release.yml`.
Reproducing a signed release also requires the Eleven Ideas Developer ID
identity and either an `ATC_NOTARY_PROFILE` or the three
`ATC_APP_STORE_CONNECT_KEY_*` credentials. The credentials are intentionally
not stored in Git; their GitHub environment names and consumption points are
preserved by the workflow.

## Restoring the snapshot

Clone the repository, fetch tags, and create a jj working commit at the archive
reference:

```sh
jj git fetch
jj new legacy-product-2026-08
mise trust
mise install
```

Start with the canonical commands above. Remote package registries, Apple
toolchains, signing certificates, and notarization services are external
dependencies and may require adaptation in the future. Keep any such recovery
work on a branch from the archive tag; the tag itself is immutable evidence.
