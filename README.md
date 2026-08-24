# ATC

ATC is being rebuilt from a clean foundation under
[ATC-243](https://linear.app/elevenideas/issue/ATC-243).

The active tree holds the Go scaffold for the rebuild: a single Go module
rooted at the repository with one entrypoint, [`cmd/atc`](cmd/atc/). Run
`atc help` (or a bare `atc`) for the command reference.

## Building and testing

Tools and tasks are managed by [mise](https://mise.jdx.dev) via
[`mise.toml`](mise.toml); `mise install` provisions the toolchain.

- `mise run build` — build a static `atc` binary into `bin/`
- `mise run check` — build, lint, vet, and test (CI runs the same task)
- `mise run refs` — fetch read-only T3 Code, OpenCode, and zmx source into `repos/`
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

Existing GitHub releases are artifacts of the archived product and should not
be treated as builds of the new implementation.
