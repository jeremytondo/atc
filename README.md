# ATC

ATC is being rebuilt from a clean foundation under
[ATC-243](https://linear.app/elevenideas/issue/ATC-243).

The active tree is intentionally between implementations. The previous
TypeScript App Server, macOS app, shared packages, release tooling, and CI have
been removed so their assumptions do not become accidental constraints on the
Go rebuild.

## Repository status

- The complete previous product is preserved at
  [`legacy-product-2026-08`](https://github.com/jeremytondo/atc/tree/legacy-product-2026-08).
- [`experiments/`](experiments/) contains research prototypes and findings
  that may inform the rebuild. They are evidence, not production code or
  settled architecture.
- There is currently no supported build, install, release, or test command on
  the active tree. Those will be introduced with the new foundation.

Existing GitHub releases are artifacts of the archived product and should not
be treated as builds of the new implementation.
