# ATC

ATC is being rebuilt from a clean foundation under
[ATC-243](https://linear.app/elevenideas/issue/ATC-243).

The active tree holds the Go scaffold for the rebuild: a single Go module
rooted at the repository with one entrypoint, [`cmd/atc`](cmd/atc/). Run
`atc help` (or a bare `atc`) for the command reference.

## Installing and upgrading

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | sh
```

Installs the latest release into `~/.local/bin` (override with
`ATC_INSTALL_DIR`, select a tag with `ATC_VERSION`). To install the rolling
development build directly on a new machine:

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | env ATC_VERSION=dev sh
```

Supported platforms: macOS arm64, Linux amd64/arm64. After the first install
the binary keeps itself current:

- `atc upgrade` — move to the latest production release
- `atc upgrade --dev` — install the current rolling dev build

Releases are cut by the [Release workflow](.github/workflows/release.yml):
`mise run release:patch|minor|major|dev`, `gh workflow run release.yml`, or
the Actions "Run workflow" button.

## Building and testing

Tools and tasks are managed by [mise](https://mise.jdx.dev) via
[`mise.toml`](mise.toml); `mise install` provisions the toolchain.

- `mise run build` — build a static `atc` binary into `bin/`
- `mise run check` — build, lint, vet, and test (CI runs the same task)
- `mise run refs` — fetch read-only T3 Code, Herdr, Agent Client Protocol,
  and zmx v0.6.0 source into `repos/`
- `mise tasks` — list all tasks

## Repository status

- The complete previous product is preserved at
  [`legacy-product-2026-08`](https://github.com/jeremytondo/atc/tree/legacy-product-2026-08).
  The prior TypeScript App Server, macOS app, shared packages, release
  tooling, and CI were removed so their assumptions do not constrain the Go
  rebuild.
- [`experiments/`](experiments/) contains research prototypes and findings
  that may inform the rebuild. They are evidence, not production code or
  settled architecture.

GitHub releases `v0.0.x` and older are artifacts of the archived product;
the rebuild's releases start at `v0.1.0`.
